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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

const content = `AAAXXBBBBB
CCCCCDDDDD
EEEEEFFFFF
GGGGGHHHHH
IIIIIJJJJJ
KKKKXXLLLL
9999999999
8888888888
3333333333
11111111XX`

func TestLessDrawSuperimposedBar(t *testing.T) {
	b := NewLess(LessConfig{Wrap: true, SuperimposeMessage: true})
	b.Resize(20, 4)

	w := term.NewStringWriter(20, 9)

	tests := []comptest.TestCase{
		{
			nil, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 1) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 9); b.SetMessage("P1Nav") }, `
                    
                    
                    
                    
                    
                    
                    
                    
               P1Nav`,
		}, {
			func() {
				b.Buffer().WriteString("hello world")
				b.Resize(20, 1)
			}, `
               P1Nav
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Resize(20, 9)
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 6)
			}, `
hello world. Let's t
est its responsivene
ss                  
                    
                    
               P1Nav
                    
                    
                    `,
		}, {
			func() {
				b.scroll.Wrap = false
			}, `
hello world. Let's t
                    
                    
                    
                    
               P1Nav
                    
                    
                    `,
		}, {
			func() {
				b.SetMessage("")
			}, `
hello world. Let's t
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.scroll.InvertOffset = true
			}, `
                    
                    
                    
                    
                    
hello world. Let's t
                    
                    
                    `,
		}, {
			func() {
				b.SetMessage("remei")
			}, `
                    
                    
                    
                    
                    
hello world. Leremei
                    
                    
                    `,
		},
	}
	comptest.TestComponent(t, b, w, tests)
}

// TestLessDrawConfiguredMessageLayout exercises the message bar across the
// layout configuration space and the message/search input surface. Each step
// applies a message before feeding its input sequence, so the table also
// covers clearing and re-setting the message mid-sequence.
func TestLessDrawConfiguredMessageLayout(t *testing.T) {
	type step struct {
		message  string
		input    string
		expected string
	}
	tests := []struct {
		name    string
		layout  string
		config  LessConfig
		content string
		width   int
		height  int
		steps   []step
	}{
		{
			name:    "unconfigured layout superimposed",
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐             QUERY"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "styled layout superimposed",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        QUERY █▓▒░"},
				{message: "I-search: fo", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐ I-search: fo █▓▒░"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "styled layout permanent bar",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        QUERY █▓▒░"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "no bar suppresses the message",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{NoBar: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "message wider than the viewport",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "supercalifragilistic", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐upercalifragilisti"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "wide runes in the message",
			layout:  `░▒▓█ {{ .Message | bg "gray" | fg "white" | bold }} `,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "界界界", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐      ░▒▓█ 界 界 界  "},
				{message: "a界b", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        ░▒▓█ a界 b "},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "tabs and nulls in the message",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "a\tb", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐           a b█▓▒░"},
				{message: "a\x00b", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐           a b█▓▒░"},
				{message: "\t", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐              █▓▒░"},
			},
		},
		{
			name:    "message shares the command bar with the search prompt",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "QUERY", input: "/", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"/▐        QUERY █▓▒░"},
				{message: "QUERY", input: "XX", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"/XX▐      QUERY █▓▒░"},
				{message: "QUERY", input: "<enter>", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        QUERY █▓▒░"},
				{message: "QUERY", input: "n", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        QUERY █▓▒░"},
				{message: "QUERY", input: "/", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"/▐        QUERY █▓▒░"},
				{message: "QUERY", input: "<esc>", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐        QUERY █▓▒░"},
			},
		},
		{
			name:    "search prompt without a message",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "", input: "?", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"?▐                  "},
				{message: "", input: "XX", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"?XX▐                "},
				{message: "", input: "<backspace>", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"?X▐                 "},
				{message: "", input: "<enter>", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "layout without decoration",
			layout:  `{{ .Message }}`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐             QUERY"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "layout with plain decoration",
			layout:  `[{{ .Message }}]`,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAXXBBBBB\nCCCCCDDDDD\nEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "QUERY", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐           [QUERY]"},
				{message: "", input: "", expected: "AAAXXBBBBB          \n" +
					"CCCCCDDDDD          \n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					" ▐                  "},
			},
		},
		{
			name:    "wrapped content under a styled message",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{Wrap: true, SuperimposeMessage: true},
			content: "AAAXXBBBBBCCCCCDDDDDEEEEEFFFFF\nGGGGGHHHHH",
			width:   20,
			height:  5,
			steps: []step{
				{message: "QUERY", input: "", expected: "AAAXXBBBBBCCCCCDDDDD\n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"                    \n" +
					" ▐        QUERY █▓▒░"},
				{message: "QUERY", input: "j", expected: "AAAXXBBBBBCCCCCDDDDD\n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"                    \n" +
					" ▐        QUERY █▓▒░"},
				{message: "", input: "", expected: "AAAXXBBBBBCCCCCDDDDD\n" +
					"EEEEEFFFFF          \n" +
					"GGGGGHHHHH          \n" +
					"                    \n" +
					" ▐                  "},
			},
		},
		{
			name:    "styled message pinned to the top row of a wrapped command bar",
			layout:  `░▒▓█ {{ .Message | bg "gray" | fg "white" }} `,
			config:  LessConfig{Wrap: true, SuperimposeMessage: true},
			content: "AAAA\nBBBB\nCCCC\nDDDD\nEEEE\nFFFF",
			width:   24,
			height:  8,
			steps: []step{
				{message: "QUERY", input: "/", expected: "AAAA                    \n" +
					"BBBB                    \n" +
					"CCCC                    \n" +
					"DDDD                    \n" +
					"EEEE                    \n" +
					"FFFF                    \n" +
					"                        \n" +
					"/▐           ░▒▓█ QUERY "},
				{message: "QUERY", input: "supercalifragilisticexpia", expected: "AAAA                    \n" +
					"BBBB                    \n" +
					"CCCC                    \n" +
					"DDDD                    \n" +
					"EEEE                    \n" +
					"FFFF                    \n" +
					"/supercalifra░▒▓█ QUERY \n" +
					"gilisticexpia           "},
				{message: "QUERY", input: "lidociousandthensomemore", expected: "AAAA                    \n" +
					"BBBB                    \n" +
					"CCCC                    \n" +
					"DDDD                    \n" +
					"EEEE                    \n" +
					"/supercalifra░▒▓█ QUERY \n" +
					"gilisticexpia           \n" +
					"lidociousandt           "},
			},
		},
		{
			name:    "multi row message drops the layout decoration",
			layout:  `░▒▓█ {{ .Message | fg "white" }} `,
			config:  LessConfig{SuperimposeMessage: true},
			content: "AAAA\nBBBB\nCCCC\nDDDD\nEEEE\nFFFF\nGGGG\nHHHH",
			width:   24,
			height:  8,
			steps: []step{
				{message: "single line", input: "", expected: "AAAA                    \n" +
					"BBBB                    \n" +
					"CCCC                    \n" +
					"DDDD                    \n" +
					"EEEE                    \n" +
					"FFFF                    \n" +
					"GGGG                    \n" +
					" ▐     ░▒▓█ single line "},
				{message: "first line of msg\nsecond line here\nthird line", input: "",
					expected: "AAAA                    \n" +
						"BBBB                    \n" +
						"CCCC                    \n" +
						"DDDD                    \n" +
						"EEEE                    \n" +
						"       first line of msg\n" +
						"       second line here \n" +
						" ▐     third line       "},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			if tt.layout != "" {
				layout, err := ParseLessMessageLayout(tt.layout)
				require.NoError(t, err)
				cfg.MessageLayout = layout
			}
			b := NewLess(cfg)
			b.Buffer().WriteString(tt.content)
			b.Resize(tt.width, tt.height)

			writer := term.NewStringWriter(tt.width, tt.height)
			for _, s := range tt.steps {
				b.SetMessage("%s", s.message)
				handlertest.RunHandlerSequenceWriter(t, writer, b, tt.width, tt.height,
					[]handlertest.SequenceTestCase{{InputSequence: s.input, Expected: s.expected}})
			}
		})
	}
}

// TestLessMessageLayoutAttributes covers the styling half of the layout, which
// the rendered strings cannot express.
func TestLessMessageLayoutAttributes(t *testing.T) {
	tests := []struct {
		name     string
		layout   string
		config   LessConfig
		message  string
		column   int
		wantAttr term.Attributes
	}{
		{
			name:    "styled message cell",
			layout:  ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:  LessConfig{SuperimposeMessage: true},
			message: "QUERY",
			column:  10,
			wantAttr: term.Attributes{
				Bg: term.ColorRed, Fg: term.ColorWhite, Attrs: term.AttrBold,
			},
		},
		{
			name:     "cell left of the message keeps the scroll attributes",
			layout:   ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			config:   LessConfig{SuperimposeMessage: true},
			message:  "QUERY",
			column:   0,
			wantAttr: term.Attributes{},
		},
		{
			name:     "unconfigured layout uses the bar attributes",
			config:   LessConfig{BarAttr: term.Attributes{Bg: term.ColorBlue, Fg: term.ColorWhite}},
			message:  "QUERY",
			column:   15,
			wantAttr: term.Attributes{Bg: term.ColorBlue, Fg: term.ColorWhite},
		},
		{
			name:     "bar attributes fill what the layout leaves unset",
			layout:   `░▒▓█ {{ .Message | fg "white" }} `,
			config:   LessConfig{SuperimposeMessage: true, BarAttr: term.Attributes{Bg: term.ColorGray}},
			message:  "QUERY",
			column:   15,
			wantAttr: term.Attributes{Bg: term.ColorGray, Fg: term.ColorWhite},
		},
		{
			name:     "layout styling wins over the bar attributes",
			layout:   `░▒▓█ {{ .Message | bg "red" | fg "white" }} `,
			config:   LessConfig{SuperimposeMessage: true, BarAttr: term.Attributes{Bg: term.ColorGray}},
			message:  "QUERY",
			column:   15,
			wantAttr: term.Attributes{Bg: term.ColorRed, Fg: term.ColorWhite},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			if tt.layout != "" {
				layout, err := ParseLessMessageLayout(tt.layout)
				require.NoError(t, err)
				cfg.MessageLayout = layout
			}
			b := NewLess(cfg)
			b.Resize(20, 2)
			b.SetMessage("%s", tt.message)

			w := term.NewStringWriter(20, 2)
			b.Draw(w)
			require.NoError(t, w.Flush())

			got := w.Cells()[20+tt.column]
			require.Equal(t, tt.wantAttr.Bg, got.Bg)
			require.Equal(t, tt.wantAttr.Fg, got.Fg)
			require.Equal(t, tt.wantAttr.Attrs, got.Attrs)
		})
	}
}

func TestLessSearchDoesNotPaintEmptyMessage(t *testing.T) {
	const (
		width  = 40
		height = 2
	)
	messageAttr := term.Attributes{Bg: term.ColorGray}
	tests := []struct {
		name      string
		layout    string
		moveMode  LessMoveMode
		promptLen int
	}{
		{name: "plain forward", moveMode: LessMoveForward, promptLen: len("/foo")},
		{name: "plain backward", moveMode: LessMoveBackward, promptLen: len("?foo")},
		{
			name:      "decorated forward",
			layout:    `░▒▓█ {{ .Message | fg "white" }} `,
			moveMode:  LessMoveForward,
			promptLen: len("/foo"),
		},
		{
			name:      "decorated backward",
			layout:    `░▒▓█ {{ .Message | fg "white" }} `,
			moveMode:  LessMoveBackward,
			promptLen: len("?foo"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := LessConfig{
				SuperimposeMessage: true,
				BarAttr:            messageAttr,
			}
			if tt.layout != "" {
				layout, err := ParseLessMessageLayout(tt.layout)
				require.NoError(t, err)
				cfg.MessageLayout = layout
			}

			b := NewLess(cfg)
			b.Buffer().WriteString(strings.Repeat("x", width) + "\n" +
				strings.Repeat("x", width))
			b.Resize(width, height)

			backgrounds := func() []term.Color {
				t.Helper()
				w := term.NewStringWriter(width, height)
				b.Draw(w)
				require.NoError(t, w.Flush())
				row := w.Cells()[width:]
				ret := make([]term.Color, len(row))
				for i, cell := range row {
					ret[i] = cell.Bg
				}
				return ret
			}
			assertNoMessageBackground := func() {
				t.Helper()
				for x, bg := range backgrounds() {
					require.NotEqual(t, messageAttr.Bg, bg,
						"empty message painted column %d", x)
				}
			}

			assertNoMessageBackground()
			b.SetSearchMode(tt.moveMode)
			for _, ch := range "foo" {
				_, handled := b.Handle(term.Event{Type: term.EventKey, Ch: ch})
				require.True(t, handled)
			}
			assertNoMessageBackground()

			_, handled := b.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
			require.True(t, handled)
			assertNoMessageBackground()
			_, handled = b.Handle(term.Event{Type: term.EventKey, Ch: 'o'})
			require.True(t, handled)
			require.Equal(t, tt.promptLen, len(b.searchScrollVirt.C.Buffer().String()))

			_, handled = b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			require.True(t, handled)
			b.SetMessage("searching '%s'", "foo")
			got := backgrounds()
			require.Equal(t, messageAttr.Bg, got[width-1])
		})
	}
}

func TestParseLessMessageLayout(t *testing.T) {
	tests := []struct {
		name       string
		layout     string
		want       LessMessageLayout
		wantErrSub string
	}{
		{
			name:   "message with styling",
			layout: ` {{ .Message | bg "red" | fg "white" | bold }} █▓▒░`,
			want: LessMessageLayout{
				Template: " %s █▓▒░",
				Attributes: term.Attributes{
					Bg:    term.ColorRed,
					Fg:    term.ColorWhite,
					Attrs: term.AttrBold,
				},
			},
		},
		{
			name:       "unknown component",
			layout:     `{{ .Status }}`,
			wantErrSub: `unknown less message bar component: "Status"`,
		},
		{
			name:       "missing message",
			layout:     `literal only`,
			wantErrSub: "less message bar layout must contain .Message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLessMessageLayout(tt.layout)
			if tt.wantErrSub != "" {
				require.ErrorContains(t, err, tt.wantErrSub)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLessDrawNoBarWrap(t *testing.T) {
	b := NewLess(LessConfig{Wrap: true, NoBar: true})
	b.Resize(20, 4)

	w := term.NewStringWriter(20, 9)

	tests := []comptest.TestCase{
		{
			nil, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 1) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 9) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString("hello world")
				b.Resize(20, 1)
			}, `
hello world         
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Resize(20, 9)
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 6)
			}, `
hello world. Let's t
est its responsivene
ss                  
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 8)
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness       
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Buffer().WriteString(". Let's test its scrolling. " +
					"Let's make it overflow below and wrap," +
					"which might just take a little bit of text.")
				b.Resize(20, 9)
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh`,
		}, {
			func() {
				_, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				require.True(t, handled)
				_, handled = b.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				require.True(t, handled)
			}, `
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh
t just take a little
 bit of text.       `,
		}, {
			func() {
				_, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
				require.True(t, handled)
				_, handled = b.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
				require.True(t, handled)
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh`,
		},
	}
	comptest.TestComponent(t, b, w, tests)
}

func TestLessHandle(t *testing.T) {
	cases := getLessHandleTestFlow([26]term.Event{
		{},
		{Ch: 'k', Type: term.EventKey},
		{Ch: 'j', Type: term.EventKey},
		{Ch: 'l', Type: term.EventKey},
		{Ch: 'h', Type: term.EventKey},
		{Ch: '$', Type: term.EventKey},
		{Ch: '0', Type: term.EventKey},
		{Ch: 'G', Type: term.EventKey},
		{Ch: 'g', Type: term.EventKey},
		{Ch: '/', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyBackspace, Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyEnter, Type: term.EventKey},
		{Ch: 'g', Type: term.EventKey},
		{Ch: 'N', Type: term.EventKey},
		{Ch: 'n', Type: term.EventKey},
		{Ch: 'G', Type: term.EventKey},
		{Ch: '?', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyEnter, Type: term.EventKey},
		{Ch: 'n', Type: term.EventKey},
		{Ch: 'n', Type: term.EventKey},
		{Ch: 'N', Type: term.EventKey},
	})
	testLessHandle(t, cases)
}

func getLessHandleTestFlow(events [26]term.Event) []handlertest.SingleTestCase {
	return []handlertest.SingleTestCase{
		{
			events[0], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
        `,
		},
		{
			events[1], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
        `,
		},
		{
			events[2], `
CCCCCDDD
EEEEEFFF
GGGGGHHH
        `,
		},
		{
			events[3], `
CCCCDDDD
EEEEFFFF
GGGGHHHH
        `,
		},
		{
			events[4], `
CCCCCDDD
EEEEEFFF
GGGGGHHH
        `,
		},
		{
			events[5], `
CCCDDDDD
EEEFFFFF
GGGHHHHH
        `,
		},
		{
			events[6], `
CCCCCDDD
EEEEEFFF
GGGGGHHH
        `,
		},
		{
			events[7], `
88888888
33333333
11111111
        `,
		},
		{
			events[8], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
        `,
		},
		{
			events[9], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
/       `,
		},
		{
			events[10], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
/X      `,
		},
		{
			events[11], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
/       `,
		},
		{
			events[12], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
/X      `,
		},
		{
			events[13], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
/XX     `,
		},
		{
			events[14], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
        `,
		},
		{
			events[15], `
AAAXXBBB
CCCCCDDD
EEEEEFFF
        `,
		},
		{
			events[16], `
111111XX
        
        
        `,
		},
		{
			events[17], `
AXXBBBBB
CCCDDDDD
EEEFFFFF
        `,
		},
		{
			events[18], `
88888888
33333333
111111XX
        `,
		},
		{
			events[19], `
88888888
33333333
111111XX
?       `,
		},
		{
			events[20], `
88888888
33333333
111111XX
?X      `,
		},
		{
			events[21], `
88888888
33333333
111111XX
?XX     `,
		},
		{
			events[22], `
KKXXLLLL
99999999
88888888
        `,
		},
		{
			events[23], `
AXXBBBBB
CCCDDDDD
EEEFFFFF
        `,
		},
		{
			events[24], `
111111XX
        
        
        `,
		},
		{
			events[25], `
AXXBBBBB
CCCDDDDD
EEEFFFFF
        `,
		},
	}
}

func testLessHandle(t *testing.T, cases []handlertest.SingleTestCase) {
	var less [2]Less
	var less1 *Less
	var writer1, writer2, writer3 *term.StringWriter
	_, writer1 = setup(t, &less[0], 8, 4)
	_, writer2 = setup(t, &less[1], 8, 4)
	less1, writer3 = setup(t, nil, 8, 4)

	// test cases with allocated less
	handlertest.TestHandler(t, &less[0], cases, writer1)
	handlertest.TestHandler(t, &less[1], cases, writer2)

	// test cases with stack less
	handlertest.TestHandler(t, less1, cases, writer3)
}

func setup(t *testing.T, less *Less, width, height int) (*Less, *term.StringWriter) {
	cfg := DefaultLessConfig()
	cfg.BarAttr = term.Attributes{Bg: term.ColorBlack}
	if less == nil {
		less = NewLess(cfg)
	} else {
		less.Init(cfg)
	}

	_, err := less.Buffer().ReadFrom(strings.NewReader(content))
	require.NoError(t, err)

	less.Resize(width, height)

	return less, term.NewStringWriter(width, height)
}

// TestLessNavigationKeys exercises the arrow, ctrl-p/ctrl-n,
// ctrl-b/ctrl-f, and pgup/pgdn bindings against the rendered
// framebuffer. The fixture is a 12-line file of 2-character line
// labels (00..11) so each rendered row is unambiguous and the
// viewport (4x4 with the command bar disabled) shows exactly four
// content rows.
func TestLessNavigationKeys(t *testing.T) {
	const (
		width  = 4
		height = 4
	)
	const numbered = "00\n01\n02\n03\n04\n05\n06\n07\n08\n09\n10\n11"

	cfg := DefaultLessConfig()
	cfg.NoBar = true
	less := NewLess(cfg)
	_, err := less.Buffer().ReadFrom(strings.NewReader(numbered))
	require.NoError(t, err)

	cases := []handlertest.SequenceTestCase{
		{
			InputSequence: "",
			Expected: "" +
				"00  \n" +
				"01  \n" +
				"02  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<down>",
			Expected: "" +
				"01  \n" +
				"02  \n" +
				"03  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<up>",
			Expected: "" +
				"00  \n" +
				"01  \n" +
				"02  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<c-n>",
			Expected: "" +
				"01  \n" +
				"02  \n" +
				"03  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<c-p>",
			Expected: "" +
				"00  \n" +
				"01  \n" +
				"02  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<c-f>",
			Expected: "" +
				"04  \n" +
				"05  \n" +
				"06  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<c-b>",
			Expected: "" +
				"00  \n" +
				"01  \n" +
				"02  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<pgdn>",
			Expected: "" +
				"04  \n" +
				"05  \n" +
				"06  \n" +
				"0▐  ",
		},
		{
			InputSequence: "<pgup>",
			Expected: "" +
				"00  \n" +
				"01  \n" +
				"02  \n" +
				"0▐  ",
		},
	}

	handlertest.RunHandlerSequence(t, less, width, height, cases)
}

// TestLessPromptMode pins SetPromptMode: the label is drawn but is not
// part of the input, backspace cannot eat into it, and the mode is left
// the way SetNormalMode and SetSearchMode expect to find it.
func TestLessPromptMode(t *testing.T) {
	type step struct {
		ev       term.Event
		wantText string
	}
	keyEv := func(ch rune) term.Event { return term.Event{Type: term.EventKey, Ch: ch} }
	named := func(k term.Key) term.Event { return term.Event{Type: term.EventKey, Key: k} }
	tests := []struct {
		name     string
		label    string
		steps    []step
		wantMode LessMode
		wantBar  string
	}{
		{name: "the label is not part of the input", label: "select:",
			steps: []step{{keyEv('a'), "a"}, {keyEv('b'), "ab"}}, wantMode: LessSearchMode, wantBar: "select:ab"},
		{name: "backspace eats the input", label: "select:",
			steps:    []step{{keyEv('a'), "a"}, {keyEv('b'), "ab"}, {named(term.KeyBackspace), "a"}},
			wantMode: LessSearchMode, wantBar: "select:a"},
		{name: "backspace stops at the label", label: "keep:",
			steps:    []step{{keyEv('a'), "a"}, {named(term.KeyBackspace), ""}, {named(term.KeyBackspace), ""}, {keyEv('b'), "b"}},
			wantMode: LessSearchMode, wantBar: "keep:b"},
		{name: "a space is input", label: "split:",
			steps: []step{{named(term.KeySpace), " "}}, wantMode: LessSearchMode, wantBar: "split: "},
		{name: "a single character label", label: "/",
			steps: []step{{keyEv('x'), "x"}}, wantMode: LessSearchMode, wantBar: "/x"},
		{name: "an empty label", label: "",
			steps:    []step{{keyEv('x'), "x"}, {named(term.KeyBackspace), ""}, {named(term.KeyBackspace), ""}},
			wantMode: LessSearchMode, wantBar: ""},
		{name: "esc leaves the prompt", label: "select:",
			steps: []step{{keyEv('a'), "a"}, {named(term.KeyEsc), ""}}, wantMode: LessNormalMode},
		{name: "enter leaves the prompt", label: "select:",
			steps: []step{{keyEv('a'), "a"}, {named(term.KeyEnter), ""}}, wantMode: LessNormalMode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewLess(LessConfig{})
			b.Buffer().WriteString("abc")
			b.Resize(20, 3)
			b.SetPromptMode(tt.label)
			require.Equal(t, LessSearchMode, b.Mode())
			require.Equal(t, "", b.SearchText())
			for i, s := range tt.steps {
				_, handled := b.Handle(s.ev)
				require.True(t, handled, "step %d", i)
				require.Equal(t, s.wantText, b.SearchText(), "step %d", i)
			}
			require.Equal(t, tt.wantMode, b.Mode())
			if tt.wantBar != "" {
				w := term.NewStringWriter(20, 3)
				b.Draw(w)
				require.NoError(t, w.Flush())
				require.Contains(t, w.String(), tt.wantBar)
			}
		})
	}

	t.Run("enter runs the input as a search", func(t *testing.T) {
		var got []LessEvent
		b := NewLess(LessConfig{Handler: func(ev LessEvent) { got = append(got, ev) }})
		b.Buffer().WriteString("abc\nxbc")
		b.Resize(20, 3)
		b.SetPromptMode("select:")
		for _, ch := range "bc" {
			b.Handle(term.Event{Type: term.EventKey, Ch: ch})
		}
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		require.Equal(t, []LessEvent{{Type: Search, Data: []byte("bc")}}, got)
		require.Equal(t, LessNormalMode, b.Mode())
	})

	t.Run("the prompt always searches forward", func(t *testing.T) {
		b := NewLess(LessConfig{})
		b.Buffer().WriteString("x\nx\nx")
		b.Resize(20, 3)
		b.SetSearchMode(LessMoveBackward)
		b.SetPromptMode("select:")
		require.Equal(t, LessMoveForward, b.moveMode)
	})

	t.Run("SetNormalMode puts the / label back for the next search", func(t *testing.T) {
		b := NewLess(LessConfig{})
		b.Buffer().WriteString("abc")
		b.Resize(20, 3)
		b.SetPromptMode("select:")
		b.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
		b.SetNormalMode()
		require.Equal(t, LessNormalMode, b.Mode())
		b.SetSearchMode(LessMoveForward)
		b.Handle(term.Event{Type: term.EventKey, Ch: 'z'})
		require.Equal(t, "z", b.SearchText())
		w := term.NewStringWriter(20, 3)
		b.Draw(w)
		require.NoError(t, w.Flush())
		require.Contains(t, w.String(), "/z")
		require.NotContains(t, w.String(), "select:")
	})

	t.Run("a second prompt replaces the first", func(t *testing.T) {
		b := NewLess(LessConfig{})
		b.Buffer().WriteString("abc")
		b.Resize(20, 3)
		b.SetPromptMode("select:")
		b.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
		b.SetPromptMode("keep:")
		require.Equal(t, "", b.SearchText())
		b.Handle(term.Event{Type: term.EventKey, Ch: 'b'})
		require.Equal(t, "b", b.SearchText())
	})
}
