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
	"errors"
	"fmt"
	"sync"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// scriptedPrompter answers prompts in order, and records them.
type scriptedPrompter struct {
	mu      sync.Mutex
	answers []func(agent.PromptRequest) (agent.PromptResponse, error)
	asked   []agent.PromptRequest
}

func (p *scriptedPrompter) Prompt(_ context.Context, req agent.PromptRequest) (agent.PromptResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, req)
	if len(p.answers) == 0 {
		return agent.PromptResponse{}, errors.New("unexpected prompt")
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer(req)
}

// pick selects the option labeled label.
func pick(label string) func(agent.PromptRequest) (agent.PromptResponse, error) {
	return func(req agent.PromptRequest) (agent.PromptResponse, error) {
		for _, o := range req.Options {
			if o.Label == label {
				return agent.PromptResponse{Values: []string{o.Value}}, nil
			}
		}
		return agent.PromptResponse{}, fmt.Errorf("no option %q in %+v", label, req.Options)
	}
}

// answer types text into the option that takes input.
func answer(text string) func(agent.PromptRequest) (agent.PromptResponse, error) {
	return func(req agent.PromptRequest) (agent.PromptResponse, error) {
		for _, o := range req.Options {
			if o.RequiresInput {
				return agent.PromptResponse{Values: []string{o.Value}, TextInput: text}, nil
			}
		}
		return agent.PromptResponse{}, errors.New("no input option")
	}
}

func elicitRequest(params *gomcp.ElicitParams) *gomcp.ElicitRequest {
	return &gomcp.ElicitRequest{Params: params}
}

func TestElicitForm(t *testing.T) {
	schema := func(prop map[string]any) map[string]any {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"field": prop},
			"required":   []string{"field"},
		}
	}
	tests := []struct {
		name        string
		prop        map[string]any
		answers     []func(agent.PromptRequest) (agent.PromptResponse, error)
		wantAction  string
		wantContent map[string]any
	}{
		{
			name:        "string",
			prop:        map[string]any{"type": "string", "description": "Your name"},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){answer("Ada")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": "Ada"},
		},
		{
			name:        "number retried until valid",
			prop:        map[string]any{"type": "number"},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){answer("many"), answer("2.5")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": 2.5},
		},
		{
			name:        "integer",
			prop:        map[string]any{"type": "integer"},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){answer("42")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": int64(42)},
		},
		{
			name:        "boolean",
			prop:        map[string]any{"type": "boolean"},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){pick("No")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": false},
		},
		{
			name:        "enum",
			prop:        map[string]any{"type": "string", "enum": []any{"low", "high"}},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){pick("high")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": "high"},
		},
		{
			name: "titled enum",
			prop: map[string]any{"type": "string", "oneOf": []any{
				map[string]any{"const": "p1", "title": "Urgent"},
				map[string]any{"const": "p3", "title": "Whenever"},
			}},
			answers:     []func(agent.PromptRequest) (agent.PromptResponse, error){pick("Urgent")},
			wantAction:  "accept",
			wantContent: map[string]any{"field": "p1"},
		},
		{
			name:       "decline",
			prop:       map[string]any{"type": "string"},
			answers:    []func(agent.PromptRequest) (agent.PromptResponse, error){pick("Decline")},
			wantAction: "decline",
		},
		{
			name: "dismissed",
			prop: map[string]any{"type": "string"},
			answers: []func(agent.PromptRequest) (agent.PromptResponse, error){
				func(agent.PromptRequest) (agent.PromptResponse, error) {
					return agent.PromptResponse{}, context.Canceled
				},
			},
			wantAction: "cancel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedPrompter{answers: tt.answers}
			m := NewManagerWithTransport(nil)
			res, err := m.elicit(agent.WithPrompter(t.Context(), p), elicitRequest(&gomcp.ElicitParams{
				Message:         "Who are you?",
				RequestedSchema: schema(tt.prop),
			}))
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, res.Action)
			assert.Equal(t, tt.wantContent, res.Content)
			require.NotEmpty(t, p.asked)
			assert.Equal(t, "field", p.asked[0].Header)
			assert.Contains(t, p.asked[0].Body, "Who are you?")
		})
	}
}

func TestElicitFormOptionalAndHeaders(t *testing.T) {
	p := &scriptedPrompter{answers: []func(agent.PromptRequest) (agent.PromptResponse, error){
		answer("x"), pick("Skip"),
	}}
	m := NewManagerWithTransport(nil)
	res, err := m.elicit(agent.WithPrompter(t.Context(), p), elicitRequest(&gomcp.ElicitParams{
		Message: "Details",
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a_very_long_property_name": map[string]any{"type": "string", "description": "Long one"},
				"optional":                  map[string]any{"type": "string"},
			},
			"required": []string{"a_very_long_property_name"},
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, "accept", res.Action)
	assert.Equal(t, map[string]any{"a_very_long_property_name": "x"}, res.Content)
	require.Len(t, p.asked, 2)
	assert.Equal(t, "a_very_long_", p.asked[0].Header)
	assert.Contains(t, p.asked[0].Body, "Long one")
	assert.NotContains(t, labels(p.asked[0].Options), "Skip", "a required property cannot be skipped")
	assert.Contains(t, labels(p.asked[1].Options), "Skip")
}

func labels(opts []agent.PromptOption) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Label)
	}
	return out
}

func TestElicitURL(t *testing.T) {
	tests := []struct {
		label      string
		wantAction string
		wantOpened []string
	}{
		{label: "Open", wantAction: "accept", wantOpened: []string{"https://pay.example.com/checkout?id=1"}},
		{label: "Decline", wantAction: "decline"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			p := &scriptedPrompter{answers: []func(agent.PromptRequest) (agent.PromptResponse, error){pick(tt.label)}}
			m := NewManagerWithTransport(nil)
			var opened []string
			m.Open = func(u string) error {
				opened = append(opened, u)
				return nil
			}
			res, err := m.elicit(agent.WithPrompter(t.Context(), p), elicitRequest(&gomcp.ElicitParams{
				Mode:          "url",
				Message:       "Complete the payment",
				URL:           "https://pay.example.com/checkout?id=1",
				ElicitationID: "e1",
			}))
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, res.Action)
			assert.Equal(t, tt.wantOpened, opened, "the URL opens only with consent")
			require.Len(t, p.asked, 1)
			assert.Equal(t, agent.PromptKindPermission, p.asked[0].Kind)
			assert.Contains(t, p.asked[0].Body, "**pay.example.com**")
			assert.Contains(t, p.asked[0].Body, "https://pay.example.com/checkout?id=1")
		})
	}
}

func TestElicitWithoutPrompterCancels(t *testing.T) {
	m := NewManagerWithTransport(nil)
	res, err := m.elicit(t.Context(), elicitRequest(&gomcp.ElicitParams{Message: "?"}))
	require.NoError(t, err)
	assert.Equal(t, "cancel", res.Action)
}

func TestElicitThroughToolCall(t *testing.T) {
	requested := map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
		"required":   []string{"name"},
	}
	greet := func(res *gomcp.ElicitResult) *gomcp.CallToolResult {
		if res == nil || res.Action != "accept" {
			return &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: "no name"}}}
		}
		return &gomcp.CallToolResult{Content: []gomcp.Content{
			&gomcp.TextContent{Text: fmt.Sprintf("hello %v", res.Content["name"])},
		}}
	}
	tests := []struct {
		name      string
		stateless bool
		handler   gomcp.ToolHandlerFor[struct{}, any]
	}{
		{
			name:      "legacy elicitation request",
			stateless: false,
			handler: func(ctx context.Context, req *gomcp.CallToolRequest, _ struct{}) (*gomcp.CallToolResult, any, error) {
				res, err := req.Session.Elicit(ctx, &gomcp.ElicitParams{
					Message: "Name?", RequestedSchema: requested,
				})
				if err != nil {
					return nil, nil, err
				}
				return greet(res), nil, nil
			},
		},
		{
			name:      "multi round-trip input request",
			stateless: true,
			handler: func(_ context.Context, req *gomcp.CallToolRequest, _ struct{}) (*gomcp.CallToolResult, any, error) {
				resp, ok := req.Params.InputResponses["name"]
				if !ok {
					return &gomcp.CallToolResult{InputRequests: gomcp.InputRequestMap{
						"name": &gomcp.ElicitParams{Message: "Name?", RequestedSchema: requested},
					}}, nil, nil
				}
				res, _ := resp.(*gomcp.ElicitResult)
				return greet(res), nil, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newToolServer()
			gomcp.AddTool(server, &gomcp.Tool{Name: "greet"}, tt.handler)
			ts := serveHTTP(t, httpHandler(server, tt.stateless))

			m := NewManager(nil, "", nil)
			t.Cleanup(func() { _ = m.Close() })
			tools, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL})
			require.NoError(t, err)

			p := &scriptedPrompter{answers: []func(agent.PromptRequest) (agent.PromptResponse, error){answer("Ada")}}
			ctx := agent.WithPrompter(t.Context(), p)
			res := toolByName(t, tools, "mcp__srv__greet").Execute(ctx, `{}`)
			require.False(t, res.IsError, res.Content)
			assert.Equal(t, "hello Ada", res.Content)
			require.Len(t, p.asked, 1)
			assert.Contains(t, p.asked[0].Title, `MCP server "srv"`)
		})
	}
}
