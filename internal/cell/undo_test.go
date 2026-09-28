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

// TestUndoEmojiSequenceTypedRuneByRune types an emoji ZWJ sequence one
// keystroke at a time and then undoes every step. Merging each
// continuation (joiner and following emoji) as a coordinate-stable
// replace of the base cell keeps the undo timeline valid: previously it
// re-clustered the whole row, collapsed a column, and left an undo op
// pointing past the shortened row, which panicked on the first undo.
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
