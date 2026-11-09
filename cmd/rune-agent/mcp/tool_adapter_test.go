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
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

type greetArgs struct {
	Name string `json:"name"`
}

func setupTestSession(t *testing.T) *gomcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	server := gomcp.NewServer(
		&gomcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil,
	)
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "greet",
		Description: "Say hello",
	}, func(_ context.Context, _ *gomcp.CallToolRequest, args greetArgs) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "Hello " + args.Name},
			},
		}, nil, nil
	})
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "fail",
		Description: "Always fails",
	}, func(_ context.Context, _ *gomcp.CallToolRequest, _ any) (*gomcp.CallToolResult, any, error) {
		return nil, nil, errors.New("something broke")
	})
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "hang",
		Description: "Never returns on its own",
	}, func(ctx context.Context, _ *gomcp.CallToolRequest, _ any) (*gomcp.CallToolResult, any, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})

	st, ct := gomcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Wait() })

	client := gomcp.NewClient(
		&gomcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil,
	)
	session, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

// testAdapter wraps the named tool of a session the way the manager
// does, for a server called myserver.
func testAdapter(session *gomcp.ClientSession, tool string, stats *ToolStats) *toolAdapter {
	e := &serverEntry{
		m:       NewManagerWithTransport(nil),
		name:    "myserver",
		session: session,
		info:    &ServerInfo{Status: StatusConnected},
	}
	return &toolAdapter{
		entry:     e,
		toolName:  tool,
		qualified: qualifiedToolName("myserver", tool),
		mcpTool:   &gomcp.Tool{Name: tool},
		stats:     stats,
	}
}

func TestToolAdapterDefinition(t *testing.T) {
	session := setupTestSession(t)

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	var greetTool *gomcp.Tool
	for _, tool := range result.Tools {
		if tool.Name == "greet" {
			greetTool = tool
			break
		}
	}
	require.NotNil(t, greetTool, "greet tool not found")

	adapter := testAdapter(session, "greet", nil)
	adapter.mcpTool = greetTool

	def := adapter.Definition()
	assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
	assert.Equal(t, "mcp__myserver__greet", def.Function.Name)
	assert.Equal(t, "Say hello", def.Function.Description)
	assert.NotNil(t, def.Function.Parameters)
}

func TestQualifiedToolName(t *testing.T) {
	long := strings.Repeat("x", 80)
	tests := []struct {
		name   string
		server string
		tool   string
		want   string
	}{
		{name: "plain", server: "linear", tool: "list_issues", want: "mcp__linear__list_issues"},
		{name: "never a built-in name", server: "apply", tool: "patch", want: "mcp__apply__patch"},
		{name: "dots and spaces", server: "a.b", tool: "get item", want: "mcp__a_b__get_item"},
		{name: "dashes kept", server: "my-srv", tool: "do-it", want: "mcp__my-srv__do-it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, qualifiedToolName(tt.server, tt.tool))
		})
	}

	t.Run("long names are truncated uniquely", func(t *testing.T) {
		a := qualifiedToolName("srv", long+"a")
		b := qualifiedToolName("srv", long+"b")
		assert.Len(t, a, maxToolNameLen)
		assert.Len(t, b, maxToolNameLen)
		assert.NotEqual(t, a, b)
		assert.True(t, strings.HasPrefix(a, "mcp__srv__xxx"))
	})
}

func TestToolAdapterExecute(t *testing.T) {
	adapter := testAdapter(setupTestSession(t), "greet", nil)

	result := adapter.Execute(context.Background(), `{"name":"Alice"}`)
	assert.False(t, result.IsError)
	assert.Equal(t, "Hello Alice", result.Content)
}

func TestToolAdapterExecuteInvalidJSON(t *testing.T) {
	adapter := testAdapter(setupTestSession(t), "greet", nil)

	result := adapter.Execute(context.Background(), `{invalid`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "invalid arguments")
}

func TestToolAdapterExecuteServerError(t *testing.T) {
	adapter := testAdapter(setupTestSession(t), "fail", nil)

	result := adapter.Execute(context.Background(), `{}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "something broke")
}

func TestToolAdapterExecuteWithoutSession(t *testing.T) {
	tests := []struct {
		status ServerStatus
		err    string
		want   string
	}{
		{status: StatusConnecting, want: `MCP server "myserver" is reconnecting`},
		{status: StatusNeedsAuth, want: "needs sign-in: run `agent mcp login myserver`"},
		{status: StatusError, err: "gone", want: `MCP server "myserver" is unavailable: gone`},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			adapter := testAdapter(nil, "greet", &ToolStats{})
			adapter.entry.info = &ServerInfo{Status: tt.status, Error: tt.err}

			result := adapter.Execute(context.Background(), `{}`)
			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tt.want)
			assert.Equal(t, int64(1), adapter.stats.ErrorCount())
		})
	}
}

func TestToolAdapterTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeoutMS int
		ctxAfter  time.Duration
		want      string
	}{
		{name: "configured timeout", timeoutMS: 1000, ctxAfter: time.Minute, want: "MCP tool call timed out after 1s"},
		{name: "short timeouts are ignored", timeoutMS: 10, ctxAfter: 200 * time.Millisecond, want: "context deadline exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := testAdapter(setupTestSession(t), "hang", nil)
			adapter.entry.config.Timeout = tt.timeoutMS
			ctx, cancel := context.WithTimeout(context.Background(), tt.ctxAfter)
			defer cancel()

			result := adapter.Execute(ctx, `{}`)
			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tt.want)
		})
	}
}

func TestToolAdapterNeedsDeterministicOrder(t *testing.T) {
	tests := []struct {
		name        string
		annotations *gomcp.ToolAnnotations
		want        bool
	}{
		{name: "no annotations", want: true},
		{name: "not read-only", annotations: &gomcp.ToolAnnotations{IdempotentHint: true}, want: true},
		{name: "read-only", annotations: &gomcp.ToolAnnotations{ReadOnlyHint: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &toolAdapter{mcpTool: &gomcp.Tool{Name: "t", Annotations: tt.annotations}}
			assert.Equal(t, tt.want, adapter.NeedsDeterministicOrder())
		})
	}
}

func TestToolAdapterSummary(t *testing.T) {
	adapter := &toolAdapter{toolName: "greet"}
	assert.Equal(t, "", adapter.Summary(`{"name":"Alice"}`))
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return buf.Bytes()
}

func TestConvertContent(t *testing.T) {
	pngData := tinyPNG(t)
	tests := []struct {
		name       string
		res        *gomcp.CallToolResult
		want       string
		wantImages int
	}{
		{name: "empty", res: &gomcp.CallToolResult{}, want: ""},
		{
			name: "texts",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.TextContent{Text: "line1"}, &gomcp.TextContent{Text: "line2"},
			}},
			want: "line1\nline2",
		},
		{
			name: "image with text",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.TextContent{Text: "before"},
				&gomcp.ImageContent{Data: pngData, MIMEType: "image/png"},
				&gomcp.TextContent{Text: "after"},
			}},
			want:       "before\nafter",
			wantImages: 1,
		},
		{
			name: "image alone",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.ImageContent{Data: pngData, MIMEType: "image/png"},
			}},
			want:       "MCP server srv returned 1 image(s).",
			wantImages: 1,
		},
		{
			name: "undecodable image",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.ImageContent{Data: bytes.Repeat([]byte{1}, 6<<20), MIMEType: "image/png"},
			}},
			want: "[image (image/png) omitted: image file srv tool image is too large",
		},
		{
			name: "audio",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.AudioContent{Data: []byte{1}, MIMEType: "audio/wav"},
			}},
			want: "[audio (audio/wav) omitted]",
		},
		{
			name: "resource link",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.ResourceLink{Name: "readme", URI: "file:///readme.md"},
			}},
			want: "[resource] readme: file:///readme.md",
		},
		{
			name: "embedded text resource",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.EmbeddedResource{Resource: &gomcp.ResourceContents{URI: "x://a", Text: "inline"}},
			}},
			want: "inline",
		},
		{
			name: "embedded blob resource",
			res: &gomcp.CallToolResult{Content: []gomcp.Content{
				&gomcp.EmbeddedResource{Resource: &gomcp.ResourceContents{
					URI: "x://b", MIMEType: "application/pdf", Blob: []byte{1},
				}},
			}},
			want: "[binary resource x://b (application/pdf)]",
		},
		{
			name: "structured content without text",
			res:  &gomcp.CallToolResult{StructuredContent: map[string]any{"temp": 33}},
			want: `{"temp":33}`,
		},
		{
			name: "text wins over structured content",
			res: &gomcp.CallToolResult{
				Content:           []gomcp.Content{&gomcp.TextContent{Text: "text"}},
				StructuredContent: map[string]any{"temp": 33},
			},
			want: "text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, parts := convertContent("srv", tt.res)
			assert.True(t, strings.HasPrefix(text, tt.want), "got %q, want prefix %q", text, tt.want)
			if tt.wantImages == 0 {
				assert.Nil(t, parts)
				return
			}
			require.Len(t, parts, tt.wantImages+1)
			assert.Equal(t, llmapi.ContentPartTypeText, parts[0].Type)
			assert.Equal(t, text, parts[0].Text)
			for _, p := range parts[1:] {
				assert.Equal(t, llmapi.ContentPartTypeImageURL, p.Type)
				assert.True(t, strings.HasPrefix(p.ImageURL, "data:image/png;base64,"))
			}
		})
	}
}

func TestToolStatsRecordSuccess(t *testing.T) {
	stats := &ToolStats{}
	adapter := testAdapter(setupTestSession(t), "greet", stats)

	result := adapter.Execute(context.Background(), `{"name":"Alice"}`)
	assert.False(t, result.IsError)

	assert.Equal(t, int64(1), stats.CallCount())
	assert.Equal(t, int64(0), stats.ErrorCount())
	assert.Greater(t, int64(stats.TotalDuration()), int64(0))
	assert.False(t, stats.LastCall().IsZero())
}

func TestToolStatsRecordError(t *testing.T) {
	stats := &ToolStats{}
	adapter := testAdapter(setupTestSession(t), "fail", stats)

	result := adapter.Execute(context.Background(), `{}`)
	assert.True(t, result.IsError)

	assert.Equal(t, int64(1), stats.CallCount())
	assert.Equal(t, int64(1), stats.ErrorCount())
}

func TestToolStatsRecordInvalidJSON(t *testing.T) {
	stats := &ToolStats{}
	adapter := testAdapter(setupTestSession(t), "greet", stats)

	result := adapter.Execute(context.Background(), `{invalid`)
	assert.True(t, result.IsError)

	assert.Equal(t, int64(1), stats.CallCount())
	assert.Equal(t, int64(1), stats.ErrorCount())
}

func TestToolStatsMultipleCalls(t *testing.T) {
	stats := &ToolStats{}
	adapter := testAdapter(setupTestSession(t), "greet", stats)

	adapter.Execute(context.Background(), `{"name":"A"}`)
	adapter.Execute(context.Background(), `{"name":"B"}`)
	adapter.Execute(context.Background(), `{"name":"C"}`)

	assert.Equal(t, int64(3), stats.CallCount())
	assert.Equal(t, int64(0), stats.ErrorCount())
}

func TestToolStatsLastCallZeroBeforeUse(t *testing.T) {
	stats := &ToolStats{}
	assert.True(t, stats.LastCall().IsZero())
	assert.Equal(t, time.Duration(0), stats.TotalDuration())
}

func TestServerInfoToolStats(t *testing.T) {
	_, factory := testServer(t, "tool_a", "tool_b")

	m := NewManagerWithTransport(factory)

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"srv": {Type: "stdio", Command: "dummy"},
		},
	}
	tools := m.Connect(context.Background(), cfg)

	servers := m.Servers()
	require.Len(t, servers, 1)

	// ToolStats should be accessible for each tool.
	statsA := servers[0].ToolStats("tool_a")
	require.NotNil(t, statsA)
	assert.Equal(t, int64(0), statsA.CallCount())

	// Execute and check stats are updated.
	for _, tool := range tools {
		if tool.Definition().Function.Name == "mcp__srv__tool_a" {
			tool.Execute(context.Background(), `{}`)
			break
		}
	}
	assert.Equal(t, int64(1), statsA.CallCount())

	// Non-existent tool returns nil.
	assert.Nil(t, servers[0].ToolStats("nonexistent"))

	require.NoError(t, m.Close())
}
