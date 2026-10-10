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

package textrpc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"unstable.build/rune/internal/cell"
)

func TestBufferEditRequest(t *testing.T) {
	tsuite := []struct {
		in  string
		out textrpc.EditRequest
	}{
		{
			in: "a",
			out: textrpc.EditRequest{
				ResourceName: &textrpc.URI{Uri: ""},
				Buffer: []*termrpc.CellRow{
					{Cells: []*termrpc.Cell{{Character: 'a', Width: 1, Bytes: 1}}},
				},
			},
		},
		{
			in: "a\nbb\nccc",
			out: textrpc.EditRequest{
				ResourceName: &textrpc.URI{Uri: ""},
				Buffer: []*termrpc.CellRow{
					{Cells: []*termrpc.Cell{{Character: 'a', Width: 1, Bytes: 1}}},
					{Cells: []*termrpc.Cell{{Character: 'b', Width: 1, Bytes: 1}, {Character: 'b', Width: 1, Bytes: 1}}},
					{Cells: []*termrpc.Cell{{Character: 'c', Width: 1, Bytes: 1}, {Character: 'c', Width: 1, Bytes: 1}, {Character: 'c', Width: 1, Bytes: 1}}},
				},
			},
		},
	}

	for _, tcase := range tsuite {
		buf := cell.NewBuffer()
		buf.WriteString(tcase.in)
		out := NewEditRequest(workspaceapi.URI{}, buf, false, false)
		assert.Equal(t, tcase.out, out)

		outbuf := EditRequestToBuffer(&out)
		assert.Equal(t, tcase.in, outbuf.String())
	}
}

func TestRowsToBufferRowsAreExactSize(t *testing.T) {
	content := strings.Repeat("x", 4096) + "\na\nbb\n\nccc"
	buf := cell.NewBuffer()
	buf.WriteString(content)
	req := NewEditRequest(workspaceapi.URI{}, buf, false, false)

	out := EditRequestToBuffer(&req)
	assert.Equal(t, content, out.String())
	for y, row := range out.RawCells() {
		assert.Equal(t, len(row), cap(row), "row %d must have cap==len", y)
	}
}

func benchmarkEditRequest(b *testing.B, width, height int) {
	var str strings.Builder
	for range width {
		str.WriteString("fjkelwjflk\njflw\njfklewfkjlkew\n")
	}

	buf := cell.NewBuffer()
	buf.WriteString(str.String())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewEditRequest(workspaceapi.URI{}, buf, false, false)
	}
}

func BenchmarkEditRequestTiny(b *testing.B) {
	benchmarkEditRequest(b, 5, 5)
}
func BenchmarkEditRequestSmall(b *testing.B) {
	benchmarkEditRequest(b, 50, 50)
}
func BenchmarkEditRequestMedium(b *testing.B) {
	benchmarkEditRequest(b, 500, 500)
}
func BenchmarkEditRequestBig(b *testing.B) {
	benchmarkEditRequest(b, 5000, 5000)
}
