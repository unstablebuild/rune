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

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
)

// ToolStats tracks execution metrics for a single MCP tool.
// All fields are safe for concurrent use.
type ToolStats struct {
	callCount     atomic.Int64
	errorCount    atomic.Int64
	totalDuration atomic.Int64 // nanoseconds
	lastCall      atomic.Int64 // UnixNano
}

// CallCount returns the total number of calls.
func (s *ToolStats) CallCount() int64 { return s.callCount.Load() }

// ErrorCount returns the total number of failed calls.
func (s *ToolStats) ErrorCount() int64 { return s.errorCount.Load() }

// TotalDuration returns the cumulative call duration.
func (s *ToolStats) TotalDuration() time.Duration {
	return time.Duration(s.totalDuration.Load())
}

// LastCall returns the time of the most recent call, or zero.
func (s *ToolStats) LastCall() time.Time {
	ns := s.lastCall.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (s *ToolStats) record(dur time.Duration, isError bool) {
	s.callCount.Add(1)
	s.totalDuration.Add(int64(dur))
	s.lastCall.Store(time.Now().UnixNano())
	if isError {
		s.errorCount.Add(1)
	}
}

// maxToolNameLen is the longest tool name the model providers accept.
const maxToolNameLen = 64

// qualifiedToolName names a server's tool for the model:
// mcp__<server>__<tool>, with characters the providers reject replaced.
// Built-in tools never start with mcp__, so the two cannot collide. A
// name over the providers' limit is truncated and suffixed with a hash
// of the full name, which keeps it unique.
func qualifiedToolName(server, tool string) string {
	name := "mcp__" + sanitizeToolName(server) + "__" + sanitizeToolName(tool)
	if len(name) <= maxToolNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return name[:maxToolNameLen-len(suffix)] + suffix
}

func sanitizeToolName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// toolAdapter wraps an MCP tool as an agent.Tool. It calls the tool
// through whichever session its server has at the time, so it keeps
// working across reconnects.
type toolAdapter struct {
	entry     *serverEntry
	toolName  string // original MCP tool name (used in CallTool)
	qualified string
	mcpTool   *gomcp.Tool
	stats     *ToolStats
}

func (t *toolAdapter) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name:        t.qualified,
			Description: t.mcpTool.Description,
			Parameters:  sanitizeMCPInputSchema(t.entry.name, t.mcpTool.Name, t.mcpTool.InputSchema),
		},
	}
}

// NeedsDeterministicOrder serializes every MCP tool unless the server
// marks it read-only, since the agent cannot otherwise know a server
// tool's side effects.
func (t *toolAdapter) NeedsDeterministicOrder() bool {
	return t.mcpTool.Annotations == nil || !t.mcpTool.Annotations.ReadOnlyHint
}

func (t *toolAdapter) Execute(ctx context.Context, arguments string) agent.ToolResult {
	start := time.Now()
	result := t.execute(ctx, arguments)
	if t.stats != nil {
		t.stats.record(time.Since(start), result.IsError)
	}
	return result
}

func (t *toolAdapter) execute(ctx context.Context, arguments string) agent.ToolResult {
	var args map[string]any
	if arguments != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return errorResult(fmt.Sprintf("invalid arguments: %v", err))
		}
	}

	session, err := t.entry.currentSession()
	if err != nil {
		return errorResult(err.Error())
	}

	var timeout time.Duration
	if ms := t.entry.config.Timeout; ms >= 1000 {
		timeout = time.Duration(ms) * time.Millisecond
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	// A server asking the user for input mid-call reaches them through
	// the prompter of the conversation making the call.
	if p := agent.PrompterFromContext(ctx); p != nil {
		t.entry.pushPrompter(p)
		defer t.entry.popPrompter(p)
	}

	mcpResult, err := session.CallTool(ctx, &gomcp.CallToolParams{
		Name:      t.toolName,
		Arguments: args,
	})
	if err != nil {
		if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
			return errorResult(fmt.Sprintf("MCP tool call timed out after %s", timeout))
		}
		return errorResult(err.Error())
	}

	text, parts := convertContent(t.entry.name, mcpResult)
	return agent.ToolResult{
		Content:      text,
		MultiContent: parts,
		IsError:      mcpResult.IsError,
	}
}

func errorResult(msg string) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + msg, IsError: true}
}

func (t *toolAdapter) Summary(_ string) string {
	return ""
}

// convertContent renders a tool result for the model. Text and
// descriptions of non-text content become the text; images also become
// image parts, which the model receives after a leading text part.
// Structured content stands in for a result without text.
func convertContent(server string, res *gomcp.CallToolResult) (string, []llmapi.ContentPart) {
	var lines []string
	var images []llmapi.ContentPart
	for _, c := range res.Content {
		switch c := c.(type) {
		case *gomcp.TextContent:
			lines = append(lines, c.Text)
		case *gomcp.ImageContent:
			uri, err := agentools.EncodeImageDataURI(server+" tool image", c.Data, c.MIMEType)
			if err != nil {
				lines = append(lines, fmt.Sprintf("[image (%s) omitted: %v]", c.MIMEType, err))
				continue
			}
			images = append(images, llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: uri})
		case *gomcp.AudioContent:
			lines = append(lines, fmt.Sprintf("[audio (%s) omitted]", c.MIMEType))
		case *gomcp.ResourceLink:
			lines = append(lines, fmt.Sprintf("[resource] %s: %s", c.Name, c.URI))
		case *gomcp.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			if c.Resource.Blob != nil {
				lines = append(lines, fmt.Sprintf("[binary resource %s (%s)]", c.Resource.URI, c.Resource.MIMEType))
			} else {
				lines = append(lines, c.Resource.Text)
			}
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			text = string(data)
		}
	}
	if len(images) == 0 {
		return text, nil
	}
	lead := text
	if lead == "" {
		lead = fmt.Sprintf("MCP server %s returned %d image(s).", server, len(images))
	}
	parts := append([]llmapi.ContentPart{{Type: llmapi.ContentPartTypeText, Text: lead}}, images...)
	return lead, parts
}
