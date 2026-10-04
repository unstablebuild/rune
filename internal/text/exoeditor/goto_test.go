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

package exoeditor

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// renderToString renders a key sequence to its KeyComb.String()
// form, concatenating tokens without separators. KeyComb.String()
// is also the canonical input form for term.ParseKeys, which makes
// rendered output easy to assert against expected templates.
func renderToString(keys []term.KeyComb) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.String())
	}
	return b.String()
}

// digitKeyCombs returns the KeyComb sequence Render produces for the
// digits of n (using strconv.Itoa). Used to build exact expected
// outputs in table tests without re-implementing Render's digit loop.
func digitKeyCombs(n int) []term.KeyComb {
	s := strconv.Itoa(n)
	out := make([]term.KeyComb, 0, len(s))
	for _, r := range s {
		out = append(out, term.KeyComb{Ch: r})
	}
	return out
}

func TestGotoTemplateRender(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		line int
		col  int
		want string
	}{
		// Documented sample templates from rune.star. The
		// expected `want` is exactly what Render produces when
		// each rendered KeyComb is stringified back to its
		// canonical "<...>" form.
		// Expected `want` is what KeyComb.String() emits for the
		// rendered sequence. Shifted-form ASCII characters such
		// as ':', '|', '_' come out as "<shift-...>" because the
		// SDK normalises them into their unshifted base + Shift
		// modifier.
		{
			name: "vim",
			tpl:  "<esc>:{line}<enter>{col}|",
			line: 10, col: 5,
			want: `<esc><shift-;>10<enter>5<shift-\\>`,
		},
		{
			name: "helix",
			tpl:  "<esc>:goto<space>{line}<enter>",
			line: 10, col: 5,
			want: "<esc><shift-;>goto<space>10<enter>",
		},
		{
			name: "micro",
			tpl:  "<c-l>{line}:{col}<enter>",
			line: 10, col: 5,
			want: "<ctrl-l>10<shift-;>5<enter>",
		},
		{
			name: "emacs",
			tpl:  "<a-x>goto-line<enter>{line}<enter>",
			line: 10, col: 5,
			want: "<alt-x>goto-line<enter>10<enter>",
		},
		{
			name: "nano",
			tpl:  "<c-_>{line},{col}<enter>",
			line: 10, col: 5,
			want: "<ctrl-shift-->10,5<enter>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := parseGotoTemplate(tc.tpl)
			require.NoError(t, err)
			assert.False(t, g.IsEmpty())
			keys := g.Render(tc.line, tc.col)
			assert.Equal(t, tc.want, renderToString(keys))
		})
	}
}

func TestGotoTemplateEmpty(t *testing.T) {
	g, err := parseGotoTemplate("")
	require.NoError(t, err)
	assert.True(t, g.IsEmpty())
	assert.Empty(t, g.Render(1, 1))
	assert.Empty(t, g.Render(0, 0))
	assert.Empty(t, g.Render(-7, 99))
}

func TestGotoTemplateParseValid(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		line int
		col  int
		want string
	}{
		// ---- literal-only templates ----
		{
			name: "literal only ASCII", tpl: "abc",
			line: 1, col: 1, want: "abc",
		},
		{
			name: "literal only with named keys",
			tpl:  "<esc><enter><space>",
			line: 1, col: 1,
			want: "<esc><enter><space>",
		},
		{
			// `\<` `\>` `\\` parse to bare runes '<', '>',
			// '\\'. Their canonical KeyComb.String() form is
			// "<shift-,>" "<shift-.>" "\\\\" because '<' and
			// '>' are shifted forms of ',' and '.' and '\\'
			// itself is rendered as the escaped form so the
			// rendered string is also a valid ParseKeys input.
			name: "literal only with escape", tpl: `\<\>\\`,
			line: 1, col: 1, want: `<shift-,><shift-.>\\`,
		},
		{
			name: "literal only with multibyte runes",
			tpl:  "éñü",
			line: 1, col: 1, want: "éñü",
		},
		{
			name: "literal only with emoji",
			tpl:  "🚀🔥",
			line: 1, col: 1, want: "🚀🔥",
		},

		// ---- placeholder-only templates ----
		{
			name: "just {line}", tpl: "{line}",
			line: 7, col: 3, want: "7",
		},
		{
			name: "just {col}", tpl: "{col}",
			line: 7, col: 3, want: "3",
		},
		{
			name: "line then col adjacent", tpl: "{line}{col}",
			line: 12, col: 345, want: "12345",
		},
		{
			name: "col then line adjacent", tpl: "{col}{line}",
			line: 12, col: 345, want: "34512",
		},

		// ---- mixed templates ----
		{
			name: "line space col", tpl: "{line}<space>{col}",
			line: 123, col: 7, want: "123<space>7",
		},
		{
			name: "col first then line",
			tpl:  "{col}<space>{line}",
			line: 9, col: 4, want: "4<space>9",
		},
		{
			// ':' is the shifted form of ';' so
			// KeyComb.String() round-trips it as "<shift-;>".
			name: "literal-placeholder-literal",
			tpl:  ":{line}<enter>",
			line: 42, col: 0, want: "<shift-;>42<enter>",
		},
		{
			name: "leading literal only", tpl: ":foo{line}",
			line: 1, col: 9, want: "<shift-;>foo1",
		},
		{
			name: "trailing literal only", tpl: "{line}bar",
			line: 1, col: 9, want: "1bar",
		},
		{
			name: "repeated {line} placeholder",
			tpl:  "{line}-{line}",
			line: 5, col: 0, want: "5-5",
		},
		{
			name: "repeated {col} placeholder",
			tpl:  "{col}/{col}",
			line: 0, col: 8, want: "8/8",
		},
		{
			name: "interleaved line/col/line/col",
			tpl:  "{line}-{col}-{line}-{col}",
			line: 3, col: 4, want: "3-4-3-4",
		},

		// ---- modifier-rich literals ----
		{
			name: "ctrl-letter literal", tpl: "<c-x>{line}",
			line: 1, col: 1, want: "<ctrl-x>1",
		},
		{
			name: "alt-letter literal", tpl: "<a-x>{line}",
			line: 1, col: 1, want: "<alt-x>1",
		},
		{
			name: "ctrl-shift literal",
			tpl:  "<c-s-a>{line}",
			line: 1, col: 1, want: "<ctrl-shift-a>1",
		},

		// ---- unknown placeholders are treated as literal text ----
		{
			// '{' and '}' are the shifted forms of '[' and
			// ']'. KeyComb.String() round-trips them as
			// "<shift-[>" / "<shift-]>".
			name: "unknown placeholder is literal text",
			tpl:  "{foo}{line}",
			line: 7, col: 0,
			want: "<shift-[>foo<shift-]>7",
		},
		{
			name: "incomplete placeholder is literal",
			tpl:  "{line{line}",
			line: 7, col: 0,
			want: "<shift-[>line7",
		},

		// ---- number ranges ----
		{
			name: "zero line and col", tpl: "{line}/{col}",
			line: 0, col: 0, want: "0/0",
		},
		{
			name: "negative line", tpl: "{line}",
			line: -42, col: 0, want: "-42",
		},
		{
			name: "negative col", tpl: "{col}",
			line: 0, col: -1, want: "-1",
		},
		{
			name: "large line", tpl: "{line}",
			line: 2147483647, col: 0, want: "2147483647",
		},

		// ---- whitespace around placeholders ----
		{
			name: "literal contains digits", tpl: "1{line}2",
			line: 9, col: 0, want: "192",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := parseGotoTemplate(tc.tpl)
			require.NoError(t, err,
				"parseGotoTemplate(%q) must succeed", tc.tpl)
			assert.False(t, g.IsEmpty(),
				"non-empty template must not be empty")
			keys := g.Render(tc.line, tc.col)
			assert.Equal(t, tc.want, renderToString(keys),
				"Render(%d, %d) of %q", tc.line, tc.col, tc.tpl)
		})
	}
}

func TestGotoTemplateParseInvalid(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
	}{
		{"unknown named key", "<bogus-key>"},
		{"unknown named key in middle", "<enter><bogus-key>"},
		{"unterminated key open angle", "<enter"},
		{"raw close angle", ">"},
		{"raw close angle after literal", "foo>"},
		{"trailing backslash", `foo\`},
		{"invalid escape sequence", `foo\x`},
		{"naked space", "a b"},
		{
			"placeholder inside angle-brackets is bad keyname",
			"<{line}>",
		},
		{
			"placeholder split by angle-brackets",
			"<enter{line}>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGotoTemplate(tc.tpl)
			require.Error(t, err,
				"parseGotoTemplate(%q) must fail", tc.tpl)
		})
	}
}

func TestGotoTemplateRenderEmits1BasedDigits(t *testing.T) {
	// `<c-x>` parses to a single KeyComb{Mod: ModCtrl, Ch: 'x'};
	// the digit segments must not inherit ModCtrl from it.
	g, err := parseGotoTemplate("<c-x>{line}{col}")
	require.NoError(t, err)
	keys := g.Render(12, 34)
	require.Len(t, keys, 5,
		"expected 1 ctrl-x + 4 digit KeyCombs, got: %v", keys)
	// First KeyComb carries the modifier.
	assert.Equal(t, term.ModCtrl, keys[0].Mod)
	assert.Equal(t, 'x', keys[0].Ch)
	// Every subsequent KeyComb is a plain digit with no Mod/Key.
	wantDigits := append(digitKeyCombs(12), digitKeyCombs(34)...)
	assert.True(t, reflect.DeepEqual(wantDigits, keys[1:]),
		"digit KeyCombs must be Ch-only, no modifiers leaked\n"+
			"want: %v\n got: %v", wantDigits, keys[1:])
}

func TestGotoTemplateRenderResultIsIndependentBetweenCalls(t *testing.T) {
	g, err := parseGotoTemplate("{line}-{col}")
	require.NoError(t, err)
	a := g.Render(1, 2)
	b := g.Render(9, 8)
	assert.Equal(t, "1-2", renderToString(a))
	assert.Equal(t, "9-8", renderToString(b))

	// Mutate the first result and re-render to confirm subsequent
	// calls return a fresh slice.
	for i := range a {
		a[i] = term.KeyComb{Ch: 'X'}
	}
	c := g.Render(1, 2)
	assert.Equal(t, "1-2", renderToString(c),
		"Render must not share backing storage between calls")
}

func TestSplitGotoTemplate(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		want []rawGotoSegment
	}{
		{
			name: "empty", tpl: "",
			want: nil,
		},
		{
			name: "literal only", tpl: "abc",
			want: []rawGotoSegment{{literal: "abc"}},
		},
		{
			name: "just line", tpl: "{line}",
			want: []rawGotoSegment{{placeholder: "line"}},
		},
		{
			name: "just col", tpl: "{col}",
			want: []rawGotoSegment{{placeholder: "col"}},
		},
		{
			name: "line then col adjacent", tpl: "{line}{col}",
			want: []rawGotoSegment{
				{placeholder: "line"},
				{placeholder: "col"},
			},
		},
		{
			name: "col first", tpl: "{col}{line}",
			want: []rawGotoSegment{
				{placeholder: "col"},
				{placeholder: "line"},
			},
		},
		{
			name: "literal between placeholders",
			tpl:  "{line}-{col}",
			want: []rawGotoSegment{
				{placeholder: "line"},
				{literal: "-"},
				{placeholder: "col"},
			},
		},
		{
			name: "leading literal",
			tpl:  "go:{line}",
			want: []rawGotoSegment{
				{literal: "go:"},
				{placeholder: "line"},
			},
		},
		{
			name: "trailing literal",
			tpl:  "{line}!!",
			want: []rawGotoSegment{
				{placeholder: "line"},
				{literal: "!!"},
			},
		},
		{
			name: "unknown placeholder remains literal",
			tpl:  "{foo}{line}",
			want: []rawGotoSegment{
				{literal: "{foo}"},
				{placeholder: "line"},
			},
		},
		{
			name: "repeated placeholders interleaved",
			tpl:  "{line}-{col}-{line}-{col}",
			want: []rawGotoSegment{
				{placeholder: "line"},
				{literal: "-"},
				{placeholder: "col"},
				{literal: "-"},
				{placeholder: "line"},
				{literal: "-"},
				{placeholder: "col"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitGotoTemplate(tc.tpl)
			assert.Equal(t, tc.want, got,
				"splitGotoTemplate(%q)", tc.tpl)
		})
	}
}

func TestGotoTemplateRoundTrip(t *testing.T) {
	templates := []string{
		"<esc>:{line}<enter>{col}|",
		"<esc>:goto<space>{line}<enter>",
		"<c-l>{line}:{col}<enter>",
		"<a-x>goto-line<enter>{line}<enter>",
		"<c-_>{line},{col}<enter>",
	}
	for _, tpl := range templates {
		t.Run(tpl, func(t *testing.T) {
			g, err := parseGotoTemplate(tpl)
			require.NoError(t, err)
			keys := g.Render(7, 3)
			rendered := renderToString(keys)
			parsedAgain, err := term.ParseKeys(rendered)
			require.NoError(t, err,
				"rendered output of %q must be valid "+
					"ParseKeys input: %q",
				tpl, rendered)
			assert.Equal(t, keys, parsedAgain,
				"round-trip via ParseKeys must preserve the "+
					"KeyComb sequence")
		})
	}
}
