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
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// Elicitation actions.
const (
	elicitAccept  = "accept"
	elicitDecline = "decline"
	elicitCancel  = "cancel"
)

// Option values of elicitation prompts. A user's typed answer arrives as
// the text input, so these never collide with one.
const (
	optAnswer  = "\x00answer"
	optDecline = "\x00decline"
	optSkip    = "\x00skip"
	optOpen    = "\x00open"
)

// maxHeaderLen is the longest prompt header the chat shows.
const maxHeaderLen = 12

// elicit asks the user for the input a server requests in the middle
// of a tool call. It goes through the prompter of the conversation
// making the call; with none, the request is cancelled.
func (m *Manager) elicit(ctx context.Context, req *gomcp.ElicitRequest) (*gomcp.ElicitResult, error) {
	e := m.entryForSession(req.Session)
	name := "an MCP server"
	if e != nil {
		name = fmt.Sprintf("MCP server %q", e.name)
	}
	p := agent.PrompterFromContext(ctx)
	if p == nil && e != nil {
		p = e.activePrompter()
	}
	if p == nil {
		return &gomcp.ElicitResult{Action: elicitCancel}, nil
	}
	params := req.Params
	if params.Mode == "url" || (params.Mode == "" && params.URL != "") {
		return m.elicitURL(ctx, p, name, params)
	}
	return elicitForm(ctx, p, name, params)
}

// elicitURL asks the user whether to open the URL a server sends them
// to. It is never opened without their consent.
func (m *Manager) elicitURL(
	ctx context.Context, p agent.Prompter, name string, params *gomcp.ElicitParams,
) (*gomcp.ElicitResult, error) {
	host := params.URL
	if u, err := url.Parse(params.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	var body strings.Builder
	if params.Message != "" {
		body.WriteString(params.Message)
		body.WriteString("\n\n")
	}
	fmt.Fprintf(&body, "It asks you to open a page on **%s**:\n\n`%s`", host, params.URL)
	resp, err := p.Prompt(ctx, agent.PromptRequest{
		Title:  name + " asks you to open a link",
		Header: "Open link",
		Body:   body.String(),
		Kind:   agent.PromptKindPermission,
		Options: []agent.PromptOption{
			{Label: "Open", Description: "Open " + host + " in your browser", Value: optOpen},
			{Label: "Decline", Value: optDecline},
		},
	})
	if err != nil || len(resp.Values) == 0 {
		return &gomcp.ElicitResult{Action: elicitCancel}, nil
	}
	if resp.Values[0] != optOpen {
		return &gomcp.ElicitResult{Action: elicitDecline}, nil
	}
	if m.Open != nil {
		if err := m.Open(params.URL); err != nil {
			slog.Warn("mcp: open elicitation URL", "error", err)
		}
	}
	return &gomcp.ElicitResult{Action: elicitAccept}, nil
}

type elicitSchema struct {
	Properties map[string]elicitProperty `json:"properties"`
	Required   []string                  `json:"required"`
}

type elicitProperty struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Enum        []any    `json:"enum"`
	EnumNames   []string `json:"enumNames"`
	OneOf       []struct {
		Const any    `json:"const"`
		Title string `json:"title"`
	} `json:"oneOf"`
}

type elicitChoice struct {
	label string
	value any
}

func (p elicitProperty) choices() []elicitChoice {
	var out []elicitChoice
	for _, o := range p.OneOf {
		label := o.Title
		if label == "" {
			label = fmt.Sprint(o.Const)
		}
		out = append(out, elicitChoice{label, o.Const})
	}
	for i, v := range p.Enum {
		label := fmt.Sprint(v)
		if i < len(p.EnumNames) && p.EnumNames[i] != "" {
			label = p.EnumNames[i]
		}
		out = append(out, elicitChoice{label, v})
	}
	return out
}

// elicitForm asks one question per property of the requested schema,
// which the protocol restricts to a flat object of primitives.
func elicitForm(
	ctx context.Context, p agent.Prompter, name string, params *gomcp.ElicitParams,
) (*gomcp.ElicitResult, error) {
	var schema elicitSchema
	if params.RequestedSchema != nil {
		data, err := json.Marshal(params.RequestedSchema)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return nil, fmt.Errorf("parse requested schema: %w", err)
		}
	}
	content := make(map[string]any, len(schema.Properties))
	for _, key := range slices.Sorted(maps.Keys(schema.Properties)) {
		value, action := askProperty(ctx, p, name, params.Message, key,
			schema.Properties[key], slices.Contains(schema.Required, key))
		switch action {
		case elicitAccept:
			content[key] = value
		case optSkip:
		default:
			return &gomcp.ElicitResult{Action: action}, nil
		}
	}
	return &gomcp.ElicitResult{Action: elicitAccept, Content: content}, nil
}

// askProperty asks for one property until the answer has its type. It
// returns the value with elicitAccept, optSkip for a skipped optional
// property, or the action ending the whole elicitation.
func askProperty(
	ctx context.Context, p agent.Prompter, name, message, key string,
	prop elicitProperty, required bool,
) (any, string) {
	header := key
	if len(header) > maxHeaderLen {
		header = header[:maxHeaderLen]
	}
	label := key
	if prop.Title != "" {
		label = prop.Title
	}
	var body strings.Builder
	if message != "" {
		body.WriteString(message)
		body.WriteString("\n\n")
	}
	fmt.Fprintf(&body, "**%s**", label)
	if prop.Description != "" {
		body.WriteString(": ")
		body.WriteString(prop.Description)
	}

	var options []agent.PromptOption
	choices := prop.choices()
	switch {
	case len(choices) > 0:
		for i, c := range choices {
			options = append(options, agent.PromptOption{Label: c.label, Value: strconv.Itoa(i)})
		}
	case prop.Type == "boolean":
		options = []agent.PromptOption{{Label: "Yes", Value: "true"}, {Label: "No", Value: "false"}}
	default:
		options = []agent.PromptOption{{Label: "Answer", Value: optAnswer, RequiresInput: true}}
	}
	if !required {
		options = append(options, agent.PromptOption{Label: "Skip", Value: optSkip})
	}
	options = append(options, agent.PromptOption{Label: "Decline", Value: optDecline})

	problem := ""
	for {
		text := body.String()
		if problem != "" {
			text += "\n\n" + problem
		}
		resp, err := p.Prompt(ctx, agent.PromptRequest{
			Title:   name + " requests input",
			Header:  header,
			Body:    text,
			Options: options,
		})
		if err != nil || ctx.Err() != nil || len(resp.Values) == 0 {
			return nil, elicitCancel
		}
		switch v := resp.Values[0]; v {
		case optDecline:
			return nil, elicitDecline
		case optSkip:
			return nil, optSkip
		case "true", "false":
			if len(choices) == 0 {
				return v == "true", elicitAccept
			}
		}
		if len(choices) > 0 {
			i, err := strconv.Atoi(resp.Values[0])
			if err != nil || i < 0 || i >= len(choices) {
				return nil, elicitCancel
			}
			return choices[i].value, elicitAccept
		}
		value, err := convertAnswer(prop.Type, resp.TextInput)
		if err == nil {
			return value, elicitAccept
		}
		problem = err.Error() + "."
	}
}

func convertAnswer(typ, text string) (any, error) {
	text = strings.TrimSpace(text)
	switch typ {
	case "integer":
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a whole number", text)
		}
		return v, nil
	case "number":
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", text)
		}
		return v, nil
	default:
		return text, nil
	}
}
