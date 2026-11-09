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

package llmsearch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// function is a function of body lines, each width characters of string.
func function(name string, body, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func %s() {\n", name)
	for i := 1; i <= body; i++ {
		fmt.Fprintf(&b, "    let v%d = \"%s\"\n", i, strings.Repeat("x", width))
	}
	b.WriteString("}\n")
	return b.String()
}

func TestLines(t *testing.T) {
	t.Run("a short file is one stretch", func(t *testing.T) {
		assert.Equal(t, []piece{{"import Foundation\n\nfunc a() {}", 1, 3}},
			lines("import Foundation\n\nfunc a() {}\n", 1800))
	})

	t.Run("stretches end where the code does", func(t *testing.T) {
		text := strings.Join([]string{function("first", 22, 40), function("second", 22, 40), function("third", 22, 40)}, "\n")
		pieces := lines(text, 1800)
		require.Len(t, pieces, 3)
		all := strings.Split(text, "\n")
		for i, name := range []string{"first", "second", "third"} {
			p := pieces[i]
			assert.True(t, strings.HasPrefix(p.text, "func "+name+"() {"))
			assert.True(t, strings.HasSuffix(p.text, "}"))
			assert.Equal(t, strings.Join(all[p.first-1:p.last], "\n"), p.text)
			assert.LessOrEqual(t, len([]rune(p.text)), 1800)
		}
		assert.Equal(t, [2]int{1, 24}, [2]int{pieces[0].first, pieces[0].last})
		assert.Equal(t, [2]int{26, 49}, [2]int{pieces[1].first, pieces[1].last})
	})

	t.Run("a block too long for one stretch overlaps by two lines", func(t *testing.T) {
		pieces := lines(function("huge", 100, 40), 1800)
		require.Greater(t, len(pieces), 2)
		for i := range len(pieces) - 1 {
			assert.Equal(t, pieces[i].last-1, pieces[i+1].first)
		}
		assert.Equal(t, 102, pieces[len(pieces)-1].last)
	})

	t.Run("a very long line is cut for the model", func(t *testing.T) {
		pieces := lines("let a = 1\n"+strings.Repeat("y", 9000)+"\nlet b = 2\n", 1800)
		require.Len(t, pieces, 3)
		assert.Equal(t, []int{1, 2, 3}, []int{pieces[0].first, pieces[1].first, pieces[2].first})
		assert.Len(t, pieces[1].text, longestPiece)
	})

	t.Run("windows line endings and blank edges", func(t *testing.T) {
		assert.Equal(t, []piece{{"func a() {\n}", 3, 4}}, lines("\r\n\r\nfunc a() {\r\n}\r\n\r\n", 1800))
	})

	t.Run("blank text", func(t *testing.T) {
		assert.Empty(t, lines("", 1800))
		assert.Empty(t, lines("\n  \n", 1800))
	})
}

func notebookJSON(t *testing.T, format int, cells [][2]string) []byte {
	t.Helper()
	list := []map[string]any{}
	for _, c := range cells {
		var source []string
		for l := range strings.SplitSeq(c[1], "\n") {
			source = append(source, l+"\n")
		}
		cell := map[string]any{"cell_type": c[0], "metadata": map[string]any{}}
		if format == 3 {
			cell["input"] = c[1]
		} else {
			cell["source"] = source
		}
		if c[0] == "code" {
			cell["outputs"] = []any{map[string]any{"output_type": "display_data",
				"data": map[string]any{"image/png": strings.Repeat("A", 5000)}}}
		}
		list = append(list, cell)
	}
	nb := map[string]any{"cells": list, "nbformat": 4}
	if format == 3 {
		nb = map[string]any{"worksheets": []any{map[string]any{"cells": list}}, "nbformat": 3}
	}
	data, err := json.Marshal(nb)
	require.NoError(t, err)
	return data
}

func TestNotebookCells(t *testing.T) {
	cells := [][2]string{{"markdown", "# Class balance"}, {"code", "counts = df.label.value_counts()"},
		{"code", ""}, {"raw", "notes"}}
	for _, format := range []int{3, 4} {
		t.Run(fmt.Sprintf("nbformat %d", format), func(t *testing.T) {
			got, err := notebookCells(notebookJSON(t, format, cells))
			require.NoError(t, err)
			require.Len(t, got, 4)
			assert.NotContains(t, strings.Join(got, ""), "AAAA")
			pieces := notebookPieces(got, 1800)
			require.Len(t, pieces, 1)
			assert.Equal(t, [2]int{1, 4}, [2]int{pieces[0].first, pieces[0].last})
			assert.True(t, strings.HasPrefix(pieces[0].text, "# Class balance\n\ncounts"))
		})
	}
	_, err := notebookCells([]byte("not json"))
	assert.Error(t, err)
}

func TestNotebookPieces_HoldWholeCells(t *testing.T) {
	cell := strings.Repeat("x = 1\n", 100)
	pieces := notebookPieces([]string{cell, cell, cell, strings.Repeat("y = 2\n", 500)}, 1800)
	var firsts, lasts []int
	for _, p := range pieces {
		firsts, lasts = append(firsts, p.first), append(lasts, p.last)
	}
	assert.Equal(t, []int{1, 3, 4, 4}, firsts)
	assert.Equal(t, []int{2, 3, 4, 4}, lasts)
}

func TestCheckCode(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		source  bool
		leftOut bool
	}{
		{"generated", "// Code generated by protoc. DO NOT EDIT.\nx", true, true},
		{"private key", "KEY = \"\"\"\n-----BEGIN RSA PRIVATE KEY-----\nabc\n\"\"\"", false, true},
		{"minified", "!function(){" + strings.Repeat("var a=1;", 1000) + "}();", true, true},
		{"notes may say do not edit", "# Notes\n\nDO NOT EDIT the generated files by hand.\n", false, false},
		{"notes may run long lines", strings.Repeat("word ", 2000), false, false},
		{"code", "func a() {}\n", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCode(tt.text, tt.source)
			if !tt.leftOut {
				assert.NoError(t, err)
				return
			}
			var lo leftOut
			assert.ErrorAs(t, err, &lo)
		})
	}
}

func TestReadCode(t *testing.T) {
	opts := DefaultOptions()

	f, err := readCode([]byte("package a\n\nfunc A() {}\n"), "a/a.go", "ws/a/a.go", opts)
	require.NoError(t, err)
	assert.Equal(t, codeFile{pieces: []piece{{"package a\n\nfunc A() {}", 1, 3}}, title: "ws/a/a.go", lines: 3}, f)

	f, err = readCode([]byte("caf\xe9\n"), "a.txt", "ws/a.txt", opts)
	require.NoError(t, err)
	assert.Equal(t, "café", f.pieces[0].text)

	_, err = readCode([]byte("a\x00b"), "a.go", "ws/a.go", opts)
	assert.Equal(t, leftOut("not text"), err)

	f, err = readCode(notebookJSON(t, 4, [][2]string{{"code", "x = 1"}, {"markdown", "# Notes"}}), "n.ipynb", "ws/n.ipynb", opts)
	require.NoError(t, err)
	assert.Equal(t, 2, f.cells)
	assert.Equal(t, []piece{{"x = 1\n\n# Notes", 1, 2}}, f.pieces)
}

func TestSummary(t *testing.T) {
	tests := []struct{ text, want string }{
		{"import os\n\n# Retry things\ndef retry_with_backoff(attempts=5):\n    pass", "def retry_with_backoff(attempts=5):"},
		{"// header\nlet total = items.reduce(0, +)\n", "let total = items.reduce(0, +)"},
		{"// only a comment", "// only a comment"},
		{"x := \"" + strings.Repeat("a", 200) + "\"", "x := \"" + strings.Repeat("a", 154) + "…"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, summary(tt.text))
		})
	}
}

func TestIdentifier(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"quantize_row_q8_0_ref", []string{"quantize", "row", "q8", "0", "ref"}},
		{"refreshAccessToken", []string{"refreshaccesstoken"}},
		{"DuplicateFinder", []string{"duplicatefinder"}},
		{"os.path.join", []string{"os", "path", "join"}},
		{"llama_sampler_top_k_impl()", []string{"llama", "sampler", "top", "k", "impl"}},
		{"CodeLookup.definition", []string{"codelookup", "definition"}},
		{"retry", nil},
		{"Renamer", nil},
		{"retry with backoff", nil},
		{"hamming distance", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			assert.Equal(t, tt.want, identifier(tt.query))
		})
	}
}

func TestDefinition(t *testing.T) {
	tests := []struct {
		name, text   string
		line, strong int
		ok           bool
	}{
		{"retry_with_backoff", "x = retry_with_backoff(f)\n\ndef retry_with_backoff(attempts=5):", 12, 2, true},
		{"isMPEGTransportStream", "    static func isMPEGTransportStream(_ url: URL) -> Bool {", 10, 2, true},
		{"refreshAccessToken", "  token = await refreshAccessToken();\nexport async function refreshAccessToken() {", 11, 2, true},
		{"gguf_init_from_reader", "struct gguf_context * result = gguf_init_from_reader(gr, params);\n" +
			"static struct gguf_context * gguf_init_from_reader(const struct gguf_reader & gr, struct gguf_init_params p) {",
			11, 2, true},
		{"llama_sampler_top_k_impl", "    llama_sampler_top_k_impl(cur_p, ctx->k);\n    return;", 0, 0, false},
		{"SUStandardVersionComparator", "@implementation SUStandardVersionComparator", 10, 2, true},
		{"Name", "func (s *Server) Name() string {", 10, 2, true},
		{"quantize_row_q8_0_ref", "GGML_API void quantize_row_q8_0_ref(const float * x, block_q8_0 * y, int64_t k);",
			10, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, strength, ok := definition(tt.name, tt.text, 10)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.line, line)
			assert.Equal(t, tt.strong, strength)
		})
	}
}

func TestFirstLine(t *testing.T) {
	text := "import time\n\n\ndef retry_with_backoff(attempts=5):\n    pass\n"
	module := "\"\"\"Retrying calls to flaky services.\"\"\"\n\nimport random\n\ndef retry_with_backoff(f):\n    pass\n"
	tests := []struct {
		name  string
		words []string
		text  string
		first int
		want  int
		ok    bool
	}{
		{"a definition with the word", []string{"retry"}, text, 40, 43, true},
		{"a part of a camel case name", []string{"backoff"}, "let a = 1\nfunc retryWithBackoff() {}", 1, 2, true},
		{"not there", []string{"zebra"}, text, 1, 0, false},
		{"a definition beats a docstring", []string{"retry", "backoff"}, module, 1, 5, true},
		{"code beats a docstring", []string{"calls"}, module + "x = calls()\n", 1, 7, true},
		{"short words do not count", []string{"x"}, "x = 1", 1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := firstLine(tt.words, tt.text, tt.first)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
