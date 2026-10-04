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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools/webfetch"
)

const (
	untrustedOpen  = "<<<UNTRUSTED_CONTENT>>>"
	untrustedClose = "<<<END_UNTRUSTED_CONTENT>>>"
)

type webFetchTool struct {
	fetcher webfetch.Fetcher
}

type webFetchArgs struct {
	URL string `json:"url"`
}

// NewWebFetch creates a web fetch tool backed by the
// given Fetcher.
func NewWebFetch(f webfetch.Fetcher) agent.Tool {
	return &webFetchTool{fetcher: f}
}

func (t *webFetchTool) NeedsDeterministicOrder() bool { return false }

func (t *webFetchTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "web_fetch",
			Description: `Fetch a web page and extract its text content. Returns the page title,
final URL, extraction method, and the extracted text.

The content is automatically converted from HTML to readable text. Use
this to read the full contents of a promising search result, or to
fetch documentation, READMEs, or API references by URL.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch.",
					},
				},
				"required":             []string{"url"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *webFetchTool) Summary(arguments string) string {
	var args webFetchArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.URL
}

func (t *webFetchTool) Execute(
	ctx context.Context, arguments string,
) agent.ToolResult {
	var args webFetchArgs
	if err := json.Unmarshal(
		[]byte(arguments), &args,
	); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf(
				"error: invalid arguments: %v", err,
			),
			IsError: true,
		}
	}
	if args.URL == "" {
		return agent.ToolResult{
			Content: "error: url is required",
			IsError: true,
		}
	}

	result, err := t.fetcher.Fetch(ctx, args.URL)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf(
				"error: fetch failed: %v", err,
			),
			IsError: true,
		}
	}

	return agent.ToolResult{
		Content: formatFetchResult(result),
	}
}

func formatFetchResult(r webfetch.FetchResult) string {
	var sb strings.Builder
	if r.Title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", r.Title)
	}
	fmt.Fprintf(&sb, "URL: %s\n", r.URL)
	fmt.Fprintf(&sb, "Extraction: %s\n\n", r.ExtractMode)
	sb.WriteString(untrustedOpen)
	sb.WriteString("\n")
	sb.WriteString(sanitizeBoundaries(r.Content))
	sb.WriteString("\n")
	sb.WriteString(untrustedClose)
	return sb.String()
}

// sanitizeBoundaries strips boundary markers from fetched
// content to prevent escaping the untrusted block.
func sanitizeBoundaries(content string) string {
	s := strings.ReplaceAll(content, untrustedOpen, "")
	s = strings.ReplaceAll(s, untrustedClose, "")
	return s
}
