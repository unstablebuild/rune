// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package agentools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/workspace/walkdir"
)

// conversationAuditPrefix is the key prefix used for audit entries in
// the dialogue store. Duplicated here because llmapi.auditKeyPrefix is
// unexported.
const conversationAuditPrefix = "audit:"

// sessionScanBufferSize caps the per-line scan buffer used by
// search_conversations when calling walkdir.ReadLines. Session JSON files
// are serialized on a single line; the largest observed session on disk
// is ~34 MB, so 64 MiB leaves comfortable headroom. The scanner grows
// toward this cap only for the lines that need it.
const sessionScanBufferSize = 64 * 1024 * 1024

type listConversationsTool struct {
	store       dialoguemanager.Store
	sessionsDir string
}

func (t *listConversationsTool) NeedsDeterministicOrder() bool { return false }

func (t *listConversationsTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "list_conversations",
			Description: `List past user to agent conversation sessions.

Returns a table with conversation ID, workspace, session file path, and last
updated time for each stored dialogue session. Each session is a JSON file
containing messages from a prior interaction between the user and an agent.

Use read_file on the returned file path to read the raw conversation JSON.
Prefer search_conversations when looking for a specific topic across all
sessions.`,
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
	}
}

func (t *listConversationsTool) Summary(_ string) string {
	return ""
}

func (t *listConversationsTool) Execute(ctx context.Context, _ string) agent.ToolResult {
	it, err := t.store.List(ctx)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: %v", err),
			IsError: true,
		}
	}
	headers, err := sdkiterator.ToSlice(ctx, it)
	_ = it.Close()
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: %v", err),
			IsError: true,
		}
	}

	var sb strings.Builder
	count := 0
	for _, h := range headers {
		if strings.HasPrefix(h.ID, conversationAuditPrefix) {
			continue
		}
		if count > 0 {
			sb.WriteByte('\n')
		}
		messagesPath := filepath.Join(t.sessionsDir,
			base64.RawURLEncoding.EncodeToString([]byte(h.ID))+".json")
		title := ""
		if h.Title != "" {
			title = fmt.Sprintf("  title=%q", h.Title)
		}
		fmt.Fprintf(&sb, "%s%s  workspace=%s  path=%s  updated=%s",
			h.ID, title, h.WorkspaceURI, messagesPath,
			h.UpdatedAt.Format("2006-01-02T15:04:05Z"))
		count++
	}

	if count == 0 {
		return agent.ToolResult{Content: "No conversations found."}
	}
	return agent.ToolResult{Content: sb.String()}
}

type searchConversationsTool struct {
	fs          walkdir.Reader
	sessionsDir string
}

type searchConversationsArgs struct {
	Pattern string `json:"pattern"`
}

func (t *searchConversationsTool) NeedsDeterministicOrder() bool { return false }

func (t *searchConversationsTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "search_conversations",
			Description: `Search across all past conversation sessions for a regex pattern.

Returns matching lines formatted as "filename:line:content", capped at 200
results. The pattern uses Go regex (RE2) syntax.

Use to find where a topic, file, function, or error was previously discussed
— without reading every conversation. More efficient than list_conversations
+ read_file when you know what to search for but not which session
contains it.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Go regex (RE2) pattern to search for.",
					},
				},
				"required":             []string{"pattern"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *searchConversationsTool) Summary(arguments string) string {
	var args searchConversationsArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Pattern
}

func (t *searchConversationsTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args searchConversationsArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: invalid arguments: %v", err),
			IsError: true,
		}
	}
	if args.Pattern == "" {
		return agent.ToolResult{
			Content: "error: pattern is required",
			IsError: true,
		}
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: invalid regex: %v", err),
			IsError: true,
		}
	}

	walkCtx := boundedWalkdirContext(ctx)
	// Session JSON files are serialized on a single line and routinely exceed
	// walkdir's default 64 KiB scan buffer; without a larger cap,
	// walkdir.ReadLines silently drops them via bufio.ErrTooLong and
	// search_conversations would miss real matches. 64 MiB comfortably
	// covers observed sessions (largest seen ~34 MB) with room to grow.
	walkCtx = walkdir.ContextWithScanBufferSize(walkCtx, sessionScanBufferSize)
	paths, err := walkdir.ListFiles(walkCtx, t.fs, t.sessionsDir)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: listing files: %v", err),
			IsError: true,
		}
	}

	// Only include *.json files and skip audit sessions.
	filtered := iterator.Filter(paths, func(path string) bool {
		if filepath.Ext(path) != ".json" {
			return false
		}
		base := strings.TrimSuffix(filepath.Base(path), ".json")
		if id, err := decodeSessionFilename(base); err == nil {
			if strings.HasPrefix(id, conversationAuditPrefix) {
				return false
			}
		}
		return true
	})

	lines, err := walkdir.ReadLines(walkCtx, t.fs, filtered)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: reading files: %v", err),
			IsError: true,
		}
	}
	defer func() { _ = lines.Close() }()

	var results []string
	truncated := false
	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		content := contentAfterLineNum(line)
		if re.MatchString(content) {
			results = append(results, line)
			if len(results) >= maxSearchResults {
				truncated = true
				break
			}
		}
	}

	if len(results) == 0 {
		return agent.ToolResult{Content: "No matches found."}
	}

	output := strings.Join(results, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n(results capped at %d matches)", maxSearchResults)
	}
	return agent.ToolResult{Content: output}
}

// decodeSessionFilename decodes a base64url-encoded session filename
// back to the original dialogue ID.
func decodeSessionFilename(encoded string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
