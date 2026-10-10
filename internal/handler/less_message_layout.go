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

package handler

import (
	"errors"
	"fmt"
	"strings"
	"text/template/parse"

	"github.com/unstablebuild/rune-go-sdk/term"
	componenttemplate "unstable.build/rune/internal/component/template"
)

// LessMessageLayout configures the message rendered in Less's command bar.
type LessMessageLayout struct {
	Template   string
	Attributes term.Attributes
}

// DefaultLessMessageLayout renders the message verbatim, styled with the
// command bar attributes.
func DefaultLessMessageLayout() LessMessageLayout {
	return LessMessageLayout{Template: "%s"}
}

// ParseLessMessageLayout parses a Go template containing one Message component.
func ParseLessMessageLayout(layout string) (LessMessageLayout, error) {
	tree, err := parse.Parse("less.message_bar.layout", layout, "{{", "}}",
		componenttemplate.AllowedFuncs())
	if err != nil {
		return LessMessageLayout{}, err
	}
	root := tree["less.message_bar.layout"].Root
	if root == nil {
		return LessMessageLayout{}, errors.New("parse template error: nil root")
	}

	var ret LessMessageLayout
	var found bool
	for _, node := range root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
			ret.Template += string(n.Text)
		case *parse.ActionNode:
			act, err := componenttemplate.ParseAction(n)
			if err != nil {
				return LessMessageLayout{}, err
			}
			if act.Field != "Message" {
				return LessMessageLayout{}, fmt.Errorf(
					"unknown less message bar component: %q", act.Field)
			}
			if found {
				return LessMessageLayout{}, errors.New(
					"less message bar layout must contain exactly one .Message")
			}
			found = true
			ret.Template += "%s"
			ret.Attributes = act.Attributes
		default:
			return LessMessageLayout{}, fmt.Errorf(
				"unsupported node type %T at position %d", n, node.Position())
		}
	}
	if !found {
		return LessMessageLayout{}, errors.New(
			"less message bar layout must contain .Message")
	}
	if strings.Count(ret.Template, "%s") != 1 {
		return LessMessageLayout{}, errors.New(
			"less message bar layout must contain exactly one .Message")
	}
	return ret, nil
}
