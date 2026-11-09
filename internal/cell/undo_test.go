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

package cell

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

var undoFortune = `Love in your heart wasn't put there to stay.
Love isn't love 'til you give it away.
		-- Oscar Hammerstein 中国`

func initUndoTestBuffer(t *testing.T) (u *undoer, b *Buffer) {
	b = newBufferWithContent(t, undoFortune)
	u = b.undoer
	return
}

func TestDebugUndo(t *testing.T) {
	undoer, buf := initUndoTestBuffer(t)
	prev := buf.String()

	buf.DeleteCell(term.Coordinates{X: 4, Y: 2})
	ok, _ := undoer.undo()
	assert.True(t, ok)
	assert.Equal(t, prev, buf.String())
}

func TestUndoRawEdit(t *testing.T) {
	undoer, buf := initUndoTestBuffer(t)
	prev := buf.String()

	buf.Edit(context.Background(), term.Coordinates{Y: 2, X: 0},
		term.Coordinates{Y: 2, X: 2}, "")
	noTabs := "Love in your heart wasn't put there to stay.\nLove isn't " +
		"love 'til you give it away.\n-- Oscar Hammerstein 中国"
	assert.Equal(t, noTabs, buf.String())
	ok, _ := undoer.undo()

	assert.True(t, ok)
	after := buf.String()
	assert.Equal(t, prev, after)
}

func TestUndo(t *testing.T) {
	suite := []struct {
		name string
		cmd  func(b *Buffer)
	}{
		{"Insert", func(b *Buffer) {
			b.Insert(term.Coordinates{X: 0, Y: 2}, '\t')
		}},
		{"InsertRowAt", func(b *Buffer) {
			insertRowAt(b, 1)
		}},
		{"DeleteCell", func(b *Buffer) {
			b.DeleteCell(term.Coordinates{X: 4, Y: 2})
		}},
		{"ConflateRow", func(b *Buffer) {
			b.ConflateRow(1)
		}},
		{"WrapRow", func(b *Buffer) {
			assert.True(t, b.WrapRow(1, 5))
		}},
		{"TruncateRowFrom", func(b *Buffer) {
			b.TruncateRowFrom(term.Coordinates{X: 0, Y: 1})
		}},
		{"TruncateFrom", func(b *Buffer) {
			b.TruncateFrom(term.Coordinates{X: 6, Y: 0})
		}},
		{"DeleteRow", func(b *Buffer) {
			b.DeleteRow(0)
		}},
		{"Edit which effectively replaces", func(b *Buffer) {
			b.Edit(context.Background(), term.Coordinates{Y: 2, X: 1},
				term.Coordinates{Y: 2, X: 5}, "a\tb\t")
		}},
		{"multiline Edit which effectively replaces", func(b *Buffer) {
			b.Edit(context.Background(), term.Coordinates{Y: 0, X: 1},
				term.Coordinates{Y: 2}, "a\tb\n\t")
		}},
	}

	for _, _tcase := range suite {
		tcase := _tcase
		t.Run(fmt.Sprintf("undo %s", tcase.name), func(t *testing.T) {
			undoer, buf := initUndoTestBuffer(t)
			prev := buf.String()

			for range 5 {
				tcase.cmd(buf)
				ok, _ := undoer.undo()
				// TODO assert.Equal(t, term.Coordinates{}, at)
				assert.True(t, ok)
			}

			after := buf.String()

			assert.Equal(t, prev, after)
		})
	}

	t.Run("undo/redo a series of updates", func(t *testing.T) {
		undoer, buf := initUndoTestBuffer(t)
		prev := buf.String()

		for _, tcase := range suite {
			tcase.cmd(buf)
		}

		middle := buf.String()

		for range suite {
			undoer.undo()
		}

		after := buf.String()
		assert.Equal(t, prev, after)

		for range suite {
			undoer.redo()
		}

		afterRedo := buf.String()
		assert.Equal(t, middle, afterRedo)

		for range suite {
			undoer.undo()
		}

		after = buf.String()
		assert.Equal(t, prev, after)
	})

	t.Run("undo/redo a series of updates all via grouped undo", func(t *testing.T) {
		undoer, buf := initUndoTestBuffer(t)
		prev := buf.String()

		undoer.startMergeUndo()
		for _, tcase := range suite {
			tcase.cmd(buf)
		}
		undoer.endMergeUndo()

		middle := buf.String()

		ok, at := undoer.undo()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{Y: 2}, at)

		after := buf.String()
		assert.Equal(t, prev, after)

		ok, at = undoer.redo()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{X: 1, Y: 1}, at)

		afterRedo := buf.String()
		assert.Equal(t, middle, afterRedo)

		ok, at = undoer.undo()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{Y: 2}, at)

		after = buf.String()
		assert.Equal(t, prev, after)
	})

	t.Run("undo/redo a series of updates partially via grouped undo (after)", func(t *testing.T) {
		undoer, buf := initUndoTestBuffer(t)
		prev := buf.String()

		for i, tcase := range suite {
			if i == 4 {
				undoer.startMergeUndo()
			}
			tcase.cmd(buf)
		}
		undoer.endMergeUndo()

		middle := buf.String()

		undoer.undo()
		for range 6 {
			undoer.undo()
		}

		after := buf.String()
		assert.Equal(t, prev, after)

		for range 6 {
			undoer.redo()
		}
		undoer.redo()

		afterRedo := buf.String()
		assert.Equal(t, middle, afterRedo)

		undoer.undo()
		for range 6 {
			undoer.undo()
		}

		after = buf.String()
		assert.Equal(t, prev, after)
	})

	t.Run("undo/redo a series of updates partially via grouped undo (before)", func(t *testing.T) {
		undoer, buf := initUndoTestBuffer(t)
		prev := buf.String()

		undoer.startMergeUndo()
		for i, tcase := range suite {
			if i == 4 {
				undoer.endMergeUndo()
			}
			tcase.cmd(buf)
		}

		middle := buf.String()

		for range 6 {
			undoer.undo()
		}
		undoer.undo()

		after := buf.String()
		assert.Equal(t, prev, after)

		undoer.redo()
		for range 6 {
			undoer.redo()
		}

		afterRedo := buf.String()
		assert.Equal(t, middle, afterRedo)

		for range 6 {
			undoer.undo()
		}
		undoer.undo()

		after = buf.String()
		assert.Equal(t, prev, after)
	})
}

func TestUndoEOL(t *testing.T) {
	t.Run("undo EOL", func(t *testing.T) {
		const (
			filecontent1 = `package me.drton.jmavsim;
public class Rotor {
     sta  mtyp;


  myClass;
`
			insertStr = "\tmyClassVar\n"
		)

		insertAt := term.Coordinates{Y: 5, X: 9}
		abuf := NewBuffer()
		abuf.ReadFrom(strings.NewReader(filecontent1))
		astr0 := abuf.String()
		arcells0 := abuf.RawCells()

		afrom, ato, _ := abuf.editor.Edit(context.Background(), insertAt,
			insertAt, insertStr)
		astr1 := abuf.String()
		arcells1 := abuf.RawCells()

		abuf.editor.Edit(context.Background(), afrom, ato, "")
		astr2 := abuf.String()
		arcells2 := abuf.RawCells()
		assert.Equal(t, astr0, astr2)
		assert.Equal(t, arcells0, arcells2)

		ok, _ := abuf.Undo()
		assert.True(t, ok)
		bstr1 := abuf.String()
		brcells1 := abuf.RawCells()
		assert.Equal(t, astr1, bstr1)
		assert.Equal(t, arcells1, brcells1)

		ok, _ = abuf.Undo()
		assert.True(t, ok)
		bstr0 := abuf.String()
		brcells0 := abuf.RawCells()
		assert.Equal(t, astr0, bstr0)
		assert.Equal(t, arcells0, brcells0)

		ok, _ = abuf.Redo()
		assert.True(t, ok)
		ok, _ = abuf.Redo()
		assert.True(t, ok)
		bstr2 := abuf.String()
		brcells2 := abuf.RawCells()
		assert.Equal(t, astr2, bstr2)
		assert.Equal(t, arcells2, brcells2)
	})

	t.Run("last EOL", func(t *testing.T) {
		buf := NewBuffer()
		buf.ReadFrom(strings.NewReader("a\n"))
		initialString := buf.String()
		initialCells := buf.RawCells()

		insertRowAt(buf, 1)
		newString := buf.String()
		newCells := buf.RawCells()
		assert.Equal(t, "a\n\n", newString)
		assert.Equal(t,
			[][]term.Cell{{{Ch: 'a', Bytes: 1, Width: 1}}, {}, {}},
			newCells)

		ok, _ := buf.Undo()
		assert.True(t, ok)
		newString2 := buf.String()
		newCells2 := buf.RawCells()
		assert.Equal(t, initialString, newString2)
		assert.Equal(t, initialCells, newCells2)

	})
}

func TestUndoEmojiSequenceTypedRuneByRune(t *testing.T) {
	cases := []struct {
		name  string
		runes []rune
		steps []string
	}{
		{
			name:  "zwj family",
			runes: []rune{'\U0001F468', '\u200D', '\U0001F469', '\u200D', '\U0001F467'},
			steps: []string{
				"// \U0001F468\u200D\U0001F469\u200D",
				"// \U0001F468\u200D\U0001F469",
				"// \U0001F468\u200D",
				"// \U0001F468",
				"// ",
				"//",
				"/",
				"",
			},
		},
		{
			name:  "skin tone",
			runes: []rune{'\U0001F91F', '\U0001F3FC'},
			steps: []string{"// \U0001F91F", "// ", "//", "/", ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuffer()
			b.Init()
			ctx := context.Background()
			pos := term.Coordinates{}
			for _, r := range append([]rune("// "), tc.runes...) {
				_, pos, _ = b.editor.Edit(ctx, pos, pos, string(r))
			}

			for i, want := range tc.steps {
				ok, _ := b.Undo()
				require.True(t, ok, "undo %d must succeed", i)
				assert.Equal(t, want, b.String(), "after undo %d", i)
			}
			ok, _ := b.Undo()
			assert.False(t, ok, "no edits remain to undo")
		})
	}
}

// snapshotCells copies the cell matrix with nil and empty rows made equal,
// since an undone row may come back with either.
func snapshotCells(b *Buffer) [][]term.Cell {
	cells := term.CloneCells(b.RawCells())
	for y, row := range cells {
		cells[y] = append([]term.Cell(nil), row...)
	}
	return cells
}

func TestUndoGroup(t *testing.T) {
	ctx := context.Background()
	type op func(b *Buffer)
	edit := func(from, to term.Coordinates, s string) op {
		return func(b *Buffer) { b.Edit(ctx, from, to, s) }
	}
	at := func(y, x int) term.Coordinates { return term.Coordinates{Y: y, X: x} }

	tests := []struct {
		name    string
		content string
		ops     []op
		want    string
	}{
		{
			name:    "every row operation of the undo suite",
			content: undoFortune,
			ops: []op{
				func(b *Buffer) { b.Insert(at(2, 0), '\t') },
				func(b *Buffer) { insertRowAt(b, 1) },
				func(b *Buffer) { b.DeleteCell(at(3, 4)) },
				func(b *Buffer) { b.ConflateRow(1) },
				func(b *Buffer) { require.True(t, b.WrapRow(1, 5)) },
				func(b *Buffer) { b.TruncateRowFrom(at(1, 3)) },
				func(b *Buffer) { b.DeleteRow(0) },
			},
			want: "Lov\nisn't love 'til you give it away.\n\t\t\t- Oscar Hammerstein 中国",
		},
		{
			name:    "wide runes beside tabs",
			content: "\t中国\n界",
			ops: []op{
				edit(at(0, 1), at(0, 1), "界"),
				edit(at(0, 2), at(0, 3), ""),
				edit(at(1, 0), at(1, 1), "\t国\t"),
			},
			want: "\t界国\n\t国\t",
		},
		{
			name:    "grapheme clusters stay whole",
			content: "👨‍👧‍👦\n🇯🇵🇫🇷\ne\u0301",
			ops: []op{
				edit(at(0, 0), at(0, 0), "// "),
				edit(at(1, 1), at(1, 2), ""),
				edit(at(2, 1), at(2, 1), "👍🏽"),
			},
			want: "// 👨‍👧‍👦\n🇯🇵\ne\u0301👍🏽",
		},
		{
			name:    "nul cells and carriage returns are content",
			content: "\x00a\rb",
			ops: []op{
				edit(at(0, 0), at(0, 0), "\x00"),
				edit(at(0, 3), at(0, 4), "\r\r"),
			},
			want: "\x00\x00a\r\rb",
		},
		{
			name:    "inserts that split rows",
			content: "ab",
			ops: []op{
				edit(at(0, 1), at(0, 1), "x\ny\nz"),
				edit(at(2, 0), at(2, 0), "\n"),
			},
			want: "ax\ny\n\nzb",
		},
		{
			name:    "deletes that join rows",
			content: "a\nb\nc\nd",
			ops: []op{
				edit(at(2, 1), at(3, 0), ""),
				edit(at(0, 1), at(1, 0), ""),
			},
			want: "ab\ncd",
		},
		{
			name:    "edits that rewrite the same cell",
			content: "abc",
			ops: []op{
				edit(at(0, 1), at(0, 2), "x"),
				edit(at(0, 1), at(0, 2), "yy"),
				edit(at(0, 1), at(0, 3), ""),
			},
			want: "ac",
		},
		{
			name:    "an empty buffer",
			content: "",
			ops: []op{
				edit(at(0, 0), at(0, 0), "a\n"),
				edit(at(1, 0), at(1, 0), "\tb"),
			},
			want: "a\n\tb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBufferWithContent(t, tc.content)
			before := snapshotCells(b)
			version := b.Version()

			b.undoer.group(func() {
				for _, op := range tc.ops {
					op(b)
				}
			})
			require.Equal(t, tc.want, b.String())
			after := snapshotCells(b)
			assert.Len(t, b.undoer.undoTimeline, 1)

			for round := range 3 {
				ok, _ := b.undoer.undo()
				require.True(t, ok, "round %d", round)
				assert.Equal(t, tc.content, b.String(), "undo round %d", round)
				assert.Equal(t, before, snapshotCells(b), "undo round %d", round)
				assert.Equal(t, version, b.Version(), "undo round %d", round)
				ok, _ = b.undoer.undo()
				assert.False(t, ok, "one undo reverts the whole group")

				ok, _ = b.undoer.redo()
				require.True(t, ok, "round %d", round)
				assert.Equal(t, tc.want, b.String(), "redo round %d", round)
				assert.Equal(t, after, snapshotCells(b), "redo round %d", round)
				ok, _ = b.undoer.redo()
				assert.False(t, ok, "one redo reapplies the whole group")
			}
		})
	}
}

func TestUndoGroupTimeline(t *testing.T) {
	insert := func(b *Buffer, y int, s string) { b.InsertString(term.Coordinates{Y: y}, s) }
	tests := []struct {
		name      string
		run       func(b *Buffer)
		want      string
		wantUndos []string
	}{
		{
			name: "nested groups merge into the outermost",
			run: func(b *Buffer) {
				b.undoer.group(func() {
					insert(b, 0, "1")
					b.undoer.group(func() {
						insert(b, 1, "2")
						b.undoer.group(func() { insert(b, 2, "3") })
					})
					insert(b, 0, "4")
				})
			},
			want:      "41a\n2b\n3c",
			wantUndos: []string{"a\nb\nc"},
		},
		{
			name: "sibling groups stay separate steps",
			run: func(b *Buffer) {
				b.undoer.group(func() { insert(b, 0, "1"); insert(b, 1, "1") })
				b.undoer.group(func() { insert(b, 1, "2"); insert(b, 2, "2") })
			},
			want:      "1a\n21b\n2c",
			wantUndos: []string{"1a\n1b\nc", "a\nb\nc"},
		},
		{
			name: "a group of one edit is that edit",
			run: func(b *Buffer) {
				insert(b, 0, "1")
				b.undoer.group(func() { insert(b, 1, "2") })
			},
			want:      "1a\n2b\nc",
			wantUndos: []string{"1a\nb\nc", "a\nb\nc"},
		},
		{
			name: "an empty group inside an open scope leaves the scope intact",
			run: func(b *Buffer) {
				insert(b, 0, "0")
				b.undoer.startMergeUndo()
				insert(b, 1, "1")
				b.undoer.group(func() {})
				insert(b, 2, "2")
				b.undoer.endMergeUndo()
			},
			want:      "0a\n1b\n2c",
			wantUndos: []string{"0a\nb\nc", "a\nb\nc"},
		},
		{
			name: "a scope opened inside a nested group still covers the outer group",
			run: func(b *Buffer) {
				insert(b, 0, "0")
				b.undoer.group(func() {
					insert(b, 1, "1")
					b.undoer.group(func() {
						b.undoer.startMergeUndo()
						insert(b, 2, "2")
					})
					insert(b, 2, "3")
				})
				insert(b, 0, "4")
				b.undoer.endMergeUndo()
			},
			want:      "40a\n1b\n32c",
			wantUndos: []string{"0a\nb\nc", "a\nb\nc"},
		},
		{
			name: "closing a scope after the group merges it with the edits before",
			run: func(b *Buffer) {
				insert(b, 0, "0")
				b.undoer.startMergeUndo()
				insert(b, 1, "1")
				b.undoer.group(func() { insert(b, 2, "2"); insert(b, 2, "3") })
				b.undoer.endMergeUndo()
				insert(b, 0, "4")
			},
			want:      "40a\n1b\n32c",
			wantUndos: []string{"0a\n1b\n32c", "0a\nb\nc", "a\nb\nc"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBufferWithContent(t, "a\nb\nc")
			tc.run(b)
			require.Equal(t, tc.want, b.String())
			for _, want := range tc.wantUndos {
				ok, _ := b.undoer.undo()
				require.True(t, ok)
				assert.Equal(t, want, b.String())
			}
			ok, _ := b.undoer.undo()
			assert.False(t, ok, "nothing left to undo")
			for range tc.wantUndos {
				ok, _ := b.undoer.redo()
				require.True(t, ok)
			}
			assert.Equal(t, tc.want, b.String())
		})
	}

	t.Run("a group after an undo drops the redo timeline", func(t *testing.T) {
		b := newBufferWithContent(t, "a\nb")
		insert(b, 0, "1")
		ok, _ := b.undoer.undo()
		require.True(t, ok)
		b.undoer.group(func() { insert(b, 0, "2"); insert(b, 1, "2") })
		ok, _ = b.undoer.redo()
		assert.False(t, ok)
		assert.Equal(t, "2a\n2b", b.String())
	})

	t.Run("an empty group pushes no undo step", func(t *testing.T) {
		b := newBufferWithContent(t, "a")
		b.undoer.group(func() {})
		assert.Empty(t, b.undoer.undoTimeline)
		assert.Equal(t, 0, b.Version())
	})

	t.Run("subscribers see every edit of an undone and redone group", func(t *testing.T) {
		b := newBufferWithContent(t, "a\nb\nc")
		var sub testSubscriber
		b.Subscribe(&sub)
		b.undoer.group(func() {
			insert(b, 2, "// ")
			insert(b, 1, "// ")
			insert(b, 0, "// ")
		})
		require.Equal(t, 3, sub.onDidEdit)

		sub = testSubscriber{}
		ok, _ := b.undoer.undo()
		require.True(t, ok)
		assert.Equal(t, testSubscriber{onWillEdit: 3, onDidEdit: 3}, sub)

		sub = testSubscriber{}
		ok, _ = b.undoer.redo()
		require.True(t, ok)
		assert.Equal(t, testSubscriber{onWillEdit: 3, onDidEdit: 3}, sub)
	})
}
