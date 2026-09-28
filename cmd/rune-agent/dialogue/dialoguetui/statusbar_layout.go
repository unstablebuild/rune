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

package dialoguetui

import (
	"errors"
	"fmt"
	"strings"
	"text/template/parse"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/template"
)

// StatusBarComponentType is one of the elements supported by the
// dialogue status bar.
type StatusBarComponentType uint8

const (
	// StatusBarVoid separates left-aligned elements from right-aligned
	// ones: every element after it is pushed to the right edge.
	StatusBarVoid StatusBarComponentType = iota
	// StatusBarSpinner is the activity spinner, blank while idle.
	StatusBarSpinner
	// StatusBarStatus is the current turn phase, or the plan widget's
	// active task description when one is running.
	StatusBarStatus
	// StatusBarConversation is the name of the open conversation.
	StatusBarConversation
	// StatusBarElapsed is the time spent in the current turn.
	StatusBarElapsed
	// StatusBarModel is the model answering the conversation.
	StatusBarModel
	// StatusBarProvider is the provider serving the model.
	StatusBarProvider
	// StatusBarEffort is the reasoning effort in use.
	StatusBarEffort
	// StatusBarMaxTokens is the max output token budget per response.
	StatusBarMaxTokens
	// StatusBarTokensSent is the cumulative input token count.
	StatusBarTokensSent
	// StatusBarTokensReceived is the cumulative output token count.
	StatusBarTokensReceived
	// StatusBarContextTokens is the context window occupancy in tokens.
	StatusBarContextTokens
	// StatusBarContextWindow is the model's total context window.
	StatusBarContextWindow
	// StatusBarContextPct is the context occupancy as a percentage.
	StatusBarContextPct
	// StatusBarCache is the prompt cache hit rate with the token
	// counts it was computed from.
	StatusBarCache
	// StatusBarCachePct is the prompt cache hit rate as a percentage.
	StatusBarCachePct
	// StatusBarContextGauge is a fixed-width context occupancy gauge.
	StatusBarContextGauge
	// StatusBarCacheGauge is a fixed-width prompt cache hit-rate gauge.
	StatusBarCacheGauge
)

// StatusBarComponent represents an element to be rendered by the
// dialogue status bar.
type StatusBarComponent struct {
	Type       StatusBarComponentType
	Template   string
	Attributes term.Attributes
}

// ParseStatusBarLayout parses the given layout string into a set of
// StatusBarComponent. The expected format is Go templates.
//
// The following ActionNode's are available:
//   - Spinner: activity spinner, blank while the agent is idle.
//   - Status: the current turn phase or active task description.
//   - Conversation: the name of the open conversation.
//   - Elapsed: time spent in the current turn.
//   - Model: the model answering the conversation.
//   - Provider: the provider serving the model.
//   - Effort: the reasoning effort in use.
//   - MaxTokens: the max output token budget per response.
//   - TokensSent: cumulative input tokens.
//   - TokensReceived: cumulative output tokens.
//   - ContextTokens: context window occupancy in tokens.
//   - ContextWindow: the model's total context window.
//   - ContextPct: context occupancy as a percentage.
//   - Cache: prompt cache hit rate, with the tokens behind it.
//   - CachePct: prompt cache hit rate as a percentage.
//   - ContextGauge: fixed-width context occupancy gauge.
//   - CacheGauge: fixed-width prompt cache hit-rate gauge.
//   - ShiftRight: up until this component, all components are aligned left.
//
// Each component can have custom attributes via a pipe operator "|".
//
// The supported functions are the following:
//   - bg <color>: set the background color as a hex value or a W3C named color.
//   - fg <color>: set the foreground color as a hex value or a W3C named color.
//   - bold: set the text style as bold
//   - italic: set the text style as italic
//   - underline: set the text style as underline
//   - reverse: reverse the text foreground and background
//   - dim: dim the foreground color
func ParseStatusBarLayout(layoutStr string) (
	ret []StatusBarComponent, err error,
) {
	const name = "agent_status_bar.layout"
	tree, err := parse.Parse(name, layoutStr, "{{", "}}",
		template.AllowedFuncs())
	if err != nil {
		return nil, err
	}

	root := tree[name].Root
	if root == nil {
		return nil, errors.New("parse template error: nil root")
	}

	var tmpl string
	var shiftRight bool
	for _, node := range root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
			// Literals attach as a suffix to the element on their
			// left, except right after the pivot, which is not an
			// element and would swallow them.
			afterPivot := len(ret) > 0 &&
				ret[len(ret)-1].Type == StatusBarVoid
			if len(n.Text) != 0 && len(ret) > 0 && !afterPivot {
				all := strings.Split(string(n.Text), "  ")
				if shiftRight {
					ret[len(ret)-1].Template += all[0]
					if len(all) > 1 {
						ret[len(ret)-1].Template += "  "
					}
					for i, chunk := range all[1:] {
						tmpl += chunk
						if i < len(all[1:])-1 {
							tmpl += "  "
						}
					}
				} else {
					ret[len(ret)-1].Template += all[0]
					for _, chunk := range all[1:] {
						tmpl += "  "
						tmpl += chunk
					}
				}
			} else {
				tmpl += string(n.Text)
			}

		case *parse.ActionNode:
			fieldName, attrs, err := template.ParseAction(n)
			if err != nil {
				return nil, err
			}

			var compType StatusBarComponentType
			switch fieldName {
			case "Spinner":
				compType = StatusBarSpinner
				tmpl += "%s"
			case "Status":
				compType = StatusBarStatus
				tmpl += "%s"
			case "Conversation":
				compType = StatusBarConversation
				tmpl += "%s"
			case "Elapsed":
				compType = StatusBarElapsed
				tmpl += "%s"
			case "Model":
				compType = StatusBarModel
				tmpl += "%s"
			case "Provider":
				compType = StatusBarProvider
				tmpl += "%s"
			case "Effort":
				compType = StatusBarEffort
				tmpl += "%s"
			case "MaxTokens":
				compType = StatusBarMaxTokens
				tmpl += "%s"
			case "TokensSent":
				compType = StatusBarTokensSent
				tmpl += "%s"
			case "TokensReceived":
				compType = StatusBarTokensReceived
				tmpl += "%s"
			case "ContextTokens":
				compType = StatusBarContextTokens
				tmpl += "%s"
			case "ContextWindow":
				compType = StatusBarContextWindow
				tmpl += "%s"
			case "ContextPct":
				compType = StatusBarContextPct
				tmpl += "%s"
			case "Cache":
				compType = StatusBarCache
				tmpl += "%s"
			case "CachePct":
				compType = StatusBarCachePct
				tmpl += "%s"
			case "ContextGauge":
				compType = StatusBarContextGauge
				tmpl += "%s"
			case "CacheGauge":
				compType = StatusBarCacheGauge
				tmpl += "%s"
			case "ShiftRight":
				shiftRight = true
				ret = append(ret, StatusBarComponent{Type: StatusBarVoid})
				continue
			default:
				return nil, fmt.Errorf(
					"unknown status bar component: %q", fieldName)
			}
			ret = append(ret, StatusBarComponent{
				Type:       compType,
				Template:   tmpl,
				Attributes: attrs,
			})
			tmpl = ""

		default:
			return nil, fmt.Errorf("unsupported node type %T at position %d",
				n, node.Position())
		}
	}

	if tmpl != "" && len(ret) > 0 {
		ret[len(ret)-1].Template += tmpl
	}

	return ret, nil
}
