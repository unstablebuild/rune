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

package template

import (
	"errors"
	"fmt"
	"strconv"
	"text/template/parse"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// AllowedFuncs returns a list of allowed functions to pass to parse.Parse
// such that ParseAction can parse.ActionNodes into a set of named components
// and their attributes.
func AllowedFuncs() map[string]any {
	funcs := map[string]any{
		"fg":            func(string) string { return "" },
		"bg":            func(string) string { return "" },
		"bold":          func() string { return "" },
		"underline":     func() string { return "" },
		"reverse":       func() string { return "" },
		"blink":         func() string { return "" },
		"dim":           func() string { return "" },
		"italic":        func() string { return "" },
		"strikethrough": func() string { return "" },
	}
	for name := range imageDirectives {
		funcs[name] = func(any) string { return "" }
	}
	return funcs
}

// Action is a layout element parsed by ParseAction.
type Action struct {
	// Field names the element.
	Field string
	// Attributes style the element's text.
	Attributes term.Attributes
	// Image holds the directives of an ImageField element, with every
	// directive left out set to its default. It is the zero value for
	// every other field.
	Image Image
}

// ParseAction can be used alongside the standard library's template/parse
// to parse nodes into a set of string components with attributes after
// calling parse.Parse. Image directives are only accepted on ImageField,
// which requires src.
func ParseAction(n *parse.ActionNode) (Action, error) {
	if n.Pipe == nil || len(n.Pipe.Cmds) == 0 {
		return Action{}, fmt.Errorf("empty pipeline at position %d", n.Pos)
	}

	// first command must be the field reference: {{ .Status }}
	first := n.Pipe.Cmds[0]
	if len(first.Args) != 1 {
		return Action{}, fmt.Errorf("invalid field reference")
	}

	field, ok := first.Args[0].(*parse.FieldNode)
	if !ok || len(field.Ident) != 1 {
		return Action{}, fmt.Errorf("unsupported field expression")
	}

	act := Action{Field: field.Ident[0]}
	var img *imageBuilder
	if act.Field == ImageField {
		img = newImageBuilder()
	}

	// remaining commands are attribute filters: {{ .Status | attr "bold" "red" }}
	attrs := &act.Attributes
	for _, cmd := range n.Pipe.Cmds[1:] {
		if len(cmd.Args) == 0 {
			return Action{}, fmt.Errorf("empty function call in pipeline")
		}

		ident, ok := cmd.Args[0].(*parse.IdentifierNode)
		if !ok {
			return Action{}, fmt.Errorf("expected identifier in pipeline")
		}

		if _, ok := imageDirectives[ident.Ident]; ok {
			if img == nil {
				return Action{}, fmt.Errorf("%s only applies to .%s, not .%s",
					ident.Ident, ImageField, act.Field)
			}
			if err := img.apply(ident.Ident, cmd.Args[1:]); err != nil {
				return Action{}, err
			}
			continue
		}

		switch ident.Ident {
		case "bg":
			for _, arg := range cmd.Args[1:] {
				s, ok := arg.(*parse.StringNode)
				if !ok {
					return Action{}, fmt.Errorf("fg arguments must be a string")
				}
				var err error
				attrs.Bg, err = getColor(s.Text)
				if err != nil {
					return Action{}, err
				}
			}
			if len(cmd.Args[1:]) == 0 {
				return Action{}, errors.New("bg requires a color argument")
			}
		case "fg":
			for _, arg := range cmd.Args[1:] {
				s, ok := arg.(*parse.StringNode)
				if !ok {
					return Action{}, fmt.Errorf("bg arguments must be a string")
				}
				var err error
				attrs.Fg, err = getColor(s.Text)
				if err != nil {
					return Action{}, err
				}
			}
			if len(cmd.Args[1:]) == 0 {
				return Action{}, errors.New("fg requires a color argument")
			}
		case "bold":
			attrs.Attrs |= term.AttrBold
		case "underline":
			attrs.Attrs |= term.AttrUnderline
		case "reverse":
			attrs.Attrs |= term.AttrReverse
		case "blink":
			attrs.Attrs |= term.AttrBlink
		case "dim":
			attrs.Attrs |= term.AttrDim
		case "italic":
			attrs.Attrs |= term.AttrItalic
		case "strikethrough":
			attrs.Attrs |= term.AttrStrikeThrough
		default:
			return Action{}, fmt.Errorf("unsupported pipeline command %q", ident.Ident)
		}
	}
	if img != nil {
		var err error
		if act.Image, err = img.build(); err != nil {
			return Action{}, err
		}
	}
	return act, nil
}

func getColor(name string) (term.Color, error) {
	if name == "default" {
		return term.ColorDefault, nil
	}
	if c := term.GetColor(name); c != term.ColorDefault {
		return c, nil
	}
	if len(name) == 7 && name[0] == '#' {
		v, e := strconv.ParseInt(name[1:], 16, 32)
		if e != nil {
			return 0, errInvalidColor
		}
		return term.NewHexColor(int32(v)), nil
	}
	return 0, errInvalidColor
}

var errInvalidColor = errors.New("error is not a hex color starting with #, " +
	"nor a known named W3C color in lowercase")
