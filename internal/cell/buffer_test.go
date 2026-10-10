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
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/term"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const longStr = `Love in your heart wasn't put there to stay.
Love isn't love 'til you give it away.
		-- Oscar Hammerstein 中国`

func newBufferWithContent(t *testing.T, str string) *Buffer {
	b := NewBuffer()
	_, err := b.ReadFrom(strings.NewReader(str))
	require.NoError(t, err)

	return b
}

func TestBufferTokenAt(t *testing.T) {
	wordMatcher := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}
	suite := []struct {
		content       string
		at            term.Coordinates
		expectedStart term.Coordinates
		expectedTo    term.Coordinates
		expected      string
	}{
		{longStr, term.Coordinates{}, term.Coordinates{}, term.Coordinates{X: 4}, "Love"},
		{longStr, term.Coordinates{X: 1}, term.Coordinates{}, term.Coordinates{X: 4}, "Love"},
		{longStr, term.Coordinates{X: 4}, term.Coordinates{}, term.Coordinates{}, ""},
		{longStr, term.Coordinates{X: 5}, term.Coordinates{X: 5}, term.Coordinates{X: 7}, "in"},
		{longStr, term.Coordinates{X: 6}, term.Coordinates{X: 5}, term.Coordinates{X: 7}, "in"},
		{longStr, term.Coordinates{X: 20}, term.Coordinates{X: 19}, term.Coordinates{X: 23}, "wasn"},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			buf := NewBuffer()
			_, _ = buf.ReadFrom(strings.NewReader(test.content))
			_, _, actual := buf.TokenAt(test.at, wordMatcher)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestBufferInitPerformance(t *testing.T) {
	c := new(Buffer)
	c.InitPerformance(1, 10, 10)

	assert.NotPanics(t, func() {
		c.Undo()
		c.Redo()
		c.Version()
		c.GroupUndo()
		c.MarkStartUndo()
	})
}

func TestBufferConflateWrap(t *testing.T) {
	suite := []struct {
		in       string
		after    string
		at       int
		expectOk bool
	}{
		{
			in:       "",
			after:    "",
			at:       0,
			expectOk: false,
		},
		{
			in:       "a",
			after:    "",
			at:       0,
			expectOk: false,
		},
		{
			in:       "a\n",
			after:    "a",
			at:       0,
			expectOk: true,
		},
		{
			in:       "a\nb",
			after:    "ab",
			at:       0,
			expectOk: true,
		},
		{
			in:       "a\nb\nc",
			after:    "a\nbc",
			at:       1,
			expectOk: true,
		},
		{
			in:       "a\nb\n\tc",
			after:    "a\nb\tc",
			at:       1,
			expectOk: true,
		},
		{
			in:       "a\nb\n\t",
			after:    "a\nb\t",
			at:       1,
			expectOk: true,
		},
		{
			in:       "a\nb\n\t",
			after:    "a\nb\n\t",
			at:       2,
			expectOk: false,
		},
		{
			in:       "aaaa\naa\nbb",
			after:    "aaaaaa\nbb",
			at:       0,
			expectOk: true,
		},
	}

	for i, test := range suite {
		buf := NewBuffer()
		buf.Write([]byte(test.in))
		x, actualOk := buf.ConflateRow(test.at)
		require.Equal(t, test.expectOk, actualOk)
		if test.expectOk {
			assert.Equal(t, test.after, buf.String())
			require.True(t, buf.WrapRow(test.at, x), i)
		}
		assert.Equal(t, test.in, buf.String())
		assert.NotPanics(t, func() {
			buf.Write([]byte("a\n"))
		})
	}

	t.Run("wrap row multiple times", func(t *testing.T) {
		in := "aaaaaa\nbbb"
		expected := "aa\naa\naa\nbb\nb"
		buf := NewBuffer()
		buf.Write([]byte(in))
		width := 2
		at := 2
		var wraps int
		for y := 0; y < at && y < buf.Rows(); y++ {
			cols := buf.Columns(y)
			times := cols / width
			remainder := cols % width
			if remainder == 0 {
				times--
			}
			for i := times; i > 0 && buf.WrapRow(y, width*i); i-- {
				wraps++
				at++
			}
		}
		assert.Equal(t, expected, buf.String())
		assert.Equal(t, 3, wraps)
	})
}

func TestBufferInsert(t *testing.T) {
	buf := NewBuffer()
	var next term.Coordinates

	next = buf.Insert(next, 'h')
	next = buf.Insert(next, 'e')
	next = buf.Insert(next, 'l')
	next = buf.Insert(next, 'l')
	next = buf.Insert(next, 'o')
	next = buf.Insert(next, '\n')
	next = buf.Insert(next, 'w')
	next = buf.Insert(next, 'o')
	next = buf.Insert(next, 'r')
	next = buf.Insert(next, 'l')
	buf.Insert(next, 'd')
	next = buf.Insert(term.Coordinates{X: 4, Y: 0}, '\n')

	str := "hell\no\nworld"
	assert.Equal(t, 3, buf.Rows())

	cols := buf.Columns(0)
	assert.Equal(t, 4, cols)

	cols = buf.Columns(1)
	assert.Equal(t, 1, cols)

	cols = buf.Columns(2)
	assert.Equal(t, 5, cols)

	assert.Equal(t, str, buf.String())

	assert.Equal(t, term.Coordinates{Y: 1}, next)

	next = buf.Insert(term.Coordinates{Y: 1}, '\t')
	assert.Equal(t, term.Coordinates{Y: 1, X: 1}, next)
}

func TestBufferInsertMultiWidth(t *testing.T) {
	t.Run("append, next should consider width", func(t *testing.T) {
		buf := NewBuffer()
		var next term.Coordinates

		next = buf.Insert(next, 'h')
		next = buf.Insert(next, 'e')
		next = buf.Insert(next, 'l')
		next = buf.Insert(next, 'l')
		next = buf.Insert(next, '国')

		assert.Equal(t, 1, buf.Rows())
		cols := buf.Columns(0)
		assert.Equal(t, 5, cols)
		assert.Equal(t, "hell国", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, next)
	})

	t.Run("insert in the middle of row should shift the width of the rune", func(t *testing.T) {
		buf := NewBuffer()
		var next term.Coordinates

		next = buf.Insert(next, 'h')
		next = buf.Insert(next, 'e')
		next = buf.Insert(next, 'l')
		next = buf.Insert(next, 'l')
		next = buf.Insert(term.Coordinates{X: 1}, '国')

		assert.Equal(t, 1, buf.Rows())
		cols := buf.Columns(0)
		assert.Equal(t, 5, cols)
		assert.Equal(t, "h国ell", buf.String())
		assert.Equal(t, term.Coordinates{X: 2}, next)
	})
}

func TestBufferDeleteRow(t *testing.T) {
	tsuite := []struct {
		content     string
		in          int
		wantOk      bool
		wantContent string
	}{
		{"hello\nworld", 0, true, "world"},
		{"world", 0, true, ""},
		{"\nworld", 0, true, "world"},
		{"1234", 1, false, "1234"},
		{"ya-basic\n", 1, true, "ya-basic"},
		{"ya-basic\na", 1, true, "ya-basic"},
		{"a\nb\nc", 1, true, "a\nc"},
		{"a\nb", -1, false, "a\nb"},
		{"a\nb", -100, false, "a\nb"},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			buf := newBufferWithContent(t, tcase.content)
			assert.NotPanics(t, func() {
				assert.Equal(t, tcase.wantOk, buf.DeleteRow(tcase.in))
				assert.Equal(t, tcase.wantContent, buf.String())
			})
		})
	}
}

func TestBufferTruncateRowFrom1(t *testing.T) {
	str := "hello\nworld"
	buf := newBufferWithContent(t, str)

	buf.TruncateRowFrom(term.Coordinates{X: 2, Y: 0})

	assert.Equal(t, "he\nworld", buf.String())
}

func TestBufferTruncateRowFrom2(t *testing.T) {
	str := "hello\nworld"
	buf := newBufferWithContent(t, str)

	buf.TruncateRowFrom(term.Coordinates{X: 2, Y: 1})

	assert.Equal(t, "hello\nwo", buf.String())
}

func TestBufferEditorNegativeCoordinates(t *testing.T) {
	cases := []struct {
		name       string
		start, end term.Coordinates
		str        string
	}{
		{"negative Y insert", term.Coordinates{Y: -3}, term.Coordinates{Y: -3}, "x"},
		{"negative Y delete", term.Coordinates{Y: -3}, term.Coordinates{Y: -3, X: 1}, ""},
		{"negative X insert", term.Coordinates{X: -1}, term.Coordinates{X: -1}, "x"},
		{"end Y negative", term.Coordinates{}, term.Coordinates{Y: -1}, ""},
		{"replace with negative start", term.Coordinates{Y: -1}, term.Coordinates{X: 1}, "y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := newBufferWithContent(t, "hello\nworld")
			before := buf.String()
			assert.NotPanics(t, func() {
				from, to, old := buf.Editor().Edit(
					context.Background(), tc.start, tc.end, tc.str)
				assert.GreaterOrEqual(t, from.Y, 0,
					"safeEditor must not return negative from.Y")
				assert.GreaterOrEqual(t, from.X, 0,
					"safeEditor must not return negative from.X")
				assert.GreaterOrEqual(t, to.Y, 0,
					"safeEditor must not return negative to.Y")
				assert.GreaterOrEqual(t, to.X, 0,
					"safeEditor must not return negative to.X")
				assert.Equal(t, "", old,
					"out-of-bounds edit must not report stale content")
			})
			assert.Equal(t, before, buf.String(),
				"out-of-bounds edits must not mutate the buffer")
		})
	}
}

func TestBufferEditWithAttrNegativeCoordinates(t *testing.T) {
	buf := newBufferWithContent(t, "hello\nworld")
	before := buf.String()
	assert.NotPanics(t, func() {
		buf.EditWithAttr(context.Background(),
			term.Coordinates{Y: -3}, term.Coordinates{Y: -3},
			"x", term.Attributes{})
	})
	assert.Equal(t, before, buf.String())
}

func TestBufferWrapRowNegativeY(t *testing.T) {
	t.Run("performance mode (no undoer)", func(t *testing.T) {
		buf := new(Buffer)
		buf.InitPerformance(10, 10, ' ')
		buf.WriteString("hi")
		assert.NotPanics(t, func() {
			assert.False(t, buf.WrapRow(-1, 0))
		})
	})
	t.Run("default mode (with undoer)", func(t *testing.T) {
		buf := newBufferWithContent(t, "hi")
		assert.NotPanics(t, func() {
			assert.False(t, buf.WrapRow(-1, 0))
		})
	})
}

func TestBufferDeleteCell(t *testing.T) {
	tsuite := []struct {
		description string
		content     string
		pos         term.Coordinates
		wantOk      bool
		wantRune    rune
		wantStart   term.Coordinates
	}{
		{"delete cell out of X bounds returns false",
			"a", term.Coordinates{X: 2}, false, 0, term.Coordinates{}},
		{"delete cell exactly out of X bounds returns false",
			"a", term.Coordinates{X: 1}, false, 0, term.Coordinates{}},
		{"delete cell out of Y bounds returns false",
			"a\nb", term.Coordinates{Y: 2}, false, 0, term.Coordinates{}},
		{"delete last cell of buffer",
			"a\nb", term.Coordinates{Y: 1}, true, 'b', term.Coordinates{Y: 1}},
		{"delete first cell of buffer",
			"a\nb", term.Coordinates{}, true, 'a', term.Coordinates{}},
		{"delete >1 width character before >1 width character",
			"💥💥", term.Coordinates{X: 0}, true, '💥', term.Coordinates{}},
		{"delete >1 width character after >1 width character",
			"💥💥", term.Coordinates{X: 1}, true, '💥', term.Coordinates{X: 1}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.description, func(t *testing.T) {
			buf := newBufferWithContent(t, tcase.content)
			actualPos, actualR, actualOk := buf.DeleteCell(tcase.pos)
			require.Equal(t, tcase.wantOk, actualOk, buf.RawCells())
			assert.Equal(t, tcase.wantRune, actualR, buf.RawCells())
			assert.Equal(t, tcase.wantStart, actualPos, buf.RawCells())
		})
	}
}

type selectCase struct {
	from     term.Coordinates
	to       term.Coordinates
	expected [][]term.Cell
}

func TestBufferTruncateFrom(t *testing.T) {

	tsuite := []struct {
		contents string
		input    term.Coordinates
		ok       bool
		expected string
	}{
		{"hello\nworld\n", term.Coordinates{X: 4, Y: 0}, true, "hell"},
		{"hello\nworld\n", term.Coordinates{X: 0, Y: 1}, true, "hello\n"},
		{longStr, term.Coordinates{X: 6, Y: 0}, true, "Love i"},
	}

	for _, tcase := range tsuite {
		buf := newBufferWithContent(t, tcase.contents)
		ok := buf.TruncateFrom(tcase.input)
		if tcase.ok {
			assert.True(t, ok)
			assert.Equal(t, tcase.expected, buf.String())
		} else {
			assert.False(t, ok)
		}
	}
}

func TestBufferTruncateFromWithUnixView(t *testing.T) {
	tsuite := []struct {
		contents               string
		input                  term.Coordinates
		ok                     bool
		expectedInternalString string
	}{
		{"hello\nworld", term.Coordinates{X: 4, Y: 0}, true, "hell"},
		{"hello\nworld", term.Coordinates{X: 0, Y: 1}, true, "hello\n"},
		{"hello\nworld\n", term.Coordinates{X: 4, Y: 0}, true, "hell\n"},
		{"hello\nworld\n", term.Coordinates{X: 0, Y: 1}, true, "hello\n\n"},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			buf := newBufferWithContent(t, tcase.contents)
			buf.WithView(&testView{reader: buf.View()})

			ok := buf.TruncateFrom(tcase.input)
			if tcase.ok {
				assert.True(t, ok)
				assert.Equal(t, tcase.expectedInternalString, buf.cells.String())
			} else {
				assert.False(t, ok)
			}
		})
	}
}

func TestBufferDelete(t *testing.T) {
	t.Run("calls underlying writer Delete", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		start, str := b.Delete(term.Coordinates{}, term.Coordinates{Y: 1})
		assert.Equal(t, term.Coordinates{}, start)
		assert.Equal(t, "bla\n", str)
	})

	t.Run("does not return last rawcells newline on delete last line", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		start, str := b.Delete(term.Coordinates{Y: 1}, term.Coordinates{Y: 2})
		assert.Equal(t, term.Coordinates{Y: 1}, start)
		assert.Equal(t, "bleh", str)
	})

	t.Run("ignores negative coordinates without panic", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		before := b.String()
		assert.NotPanics(t, func() {
			b.Delete(term.Coordinates{X: 10}, term.Coordinates{X: -1})
		})
		assert.Equal(t, before, b.String(),
			"out-of-bounds Delete must not mutate the buffer")
	})

	t.Run("does not panic if coordinates are partially out of bounds (x)", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		start, str := b.Delete(term.Coordinates{X: 10}, term.Coordinates{X: 11})
		assert.Equal(t, term.Coordinates{X: 3}, start)
		assert.Equal(t, "", str)
	})

	t.Run("does not panic if coordinates are partially out of bounds (y)", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		start, str := b.Delete(term.Coordinates{Y: 1}, term.Coordinates{Y: 1, X: 11})
		assert.Equal(t, term.Coordinates{Y: 1}, start)
		assert.Equal(t, "bleh", str)
	})

	t.Run("does not panic if coordinates are completely out of bounds", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("bla\nbleh"))
		require.NoError(t, err)

		start, str := b.Delete(term.Coordinates{Y: 1, X: 11}, term.Coordinates{Y: 2, X: 10})
		assert.Equal(t, term.Coordinates{}, start)
		assert.Equal(t, "", str)
	})
}

func TestBufferInsertString(t *testing.T) {
	b := NewBuffer()

	from, until := b.InsertString(term.Coordinates{X: 1}, "hello\n")
	assert.Equal(t, term.Coordinates{}, from)
	assert.Equal(t, term.Coordinates{Y: 1}, until)
	b.InsertString(until, "world")
	b.InsertString(until, "")

	assert.Equal(t, " hello\nworld", b.String())
}

type testSubscriber struct {
	onDidEdit, onWillEdit int
}

func (t *testSubscriber) OnWillEdit(
	_ context.Context, from, to term.Coordinates, str string,
) {
	t.onWillEdit++
}

func (t *testSubscriber) OnDidEdit(
	_ context.Context, start, end term.Coordinates, old string,
) {
	t.onDidEdit++
}

func TestBufferReset(t *testing.T) {
	t.Run("resets the contents of the buffer", func(t *testing.T) {
		var b Buffer
		b.Init()
		b.ReadFrom(strings.NewReader("a\tb"))

		assert.Equal(t, "a\tb", b.String())

		b.Reset()
		assert.Equal(t, "", b.String())
	})

	t.Run("does not reset subscribers", func(t *testing.T) {
		b := NewBuffer()
		sub := testSubscriber{}
		b.Subscribe(&sub)

		b.Reset()
		insertRowAt(b, 0)
		b.DeleteRow(0)
		// 1 reset + 1 delete + 1 insert
		assert.Equal(t, 3, sub.onDidEdit)
		assert.Equal(t, 3, sub.onWillEdit)
	})

	t.Run("does not reset undo", func(t *testing.T) {
		b := NewBuffer()
		sub := testSubscriber{}
		b.Subscribe(&sub)

		insertRowAt(b, 0)
		b.Reset()

		ok, _ := b.Undo()
		assert.True(t, ok)

		assert.Equal(t, "\n", b.String())
	})

	t.Run("reads reader content into buffer after reset", func(t *testing.T) {
		b := NewBuffer()
		content := "bla\nbleh"

		_, err := b.ReadFrom(strings.NewReader(content))
		require.NoError(t, err)

		for range 2 {
			b.Reset()
			_, err = b.ReadFrom(strings.NewReader(content))
			require.NoError(t, err)

			assert.Equal(t, content, b.String())
		}
	})

	t.Run("inserts reader content into buffer after reset", func(t *testing.T) {
		b := NewBuffer()
		content := "bla\nbleh"

		_, err := b.ReadFrom(strings.NewReader(content))
		require.NoError(t, err)

		for range 2 {
			b.Reset()
			b.InsertString(term.Coordinates{}, content)
			require.NoError(t, err)

			assert.Equal(t, content, b.String())
		}
	})

	t.Run("underlying content is always erased", func(t *testing.T) {
		tsuite := []struct {
			contents string
		}{
			{"hello\nworld"},
			{"hello\nworld\n"},
			{"hello\nworld\n\n"},
		}

		for i, tcase := range tsuite {
			t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
				buf := newBufferWithContent(t, tcase.contents)
				buf.WithView(&testView{reader: buf.View()})

				buf.Reset()
				assert.Equal(t, "", buf.cells.String())
			})
		}
	})
}

func TestBufferReloadContents(t *testing.T) {
	t.Run("replaces the whole buffer", func(t *testing.T) {
		b := newBufferWithContent(t, "old\ncontent")
		b.ReloadContents(context.Background(), "fresh")
		assert.Equal(t, "fresh", b.String())
	})

	t.Run("does not notify usage subscribers", func(t *testing.T) {
		b := newBufferWithContent(t, "old\ncontent")
		usage := testSubscriber{}
		b.SubscribeUsage(&usage)

		b.ReloadContents(context.Background(), "fresh")

		assert.Equal(t, 0, usage.onWillEdit,
			"reload must not reach SubscribeUsage subscribers")
		assert.Equal(t, 0, usage.onDidEdit,
			"reload must not reach SubscribeUsage subscribers")
	})

	t.Run("notifies root subscribers", func(t *testing.T) {
		b := newBufferWithContent(t, "old\ncontent")
		root := testSubscriber{}
		b.Subscribe(&root)

		b.ReloadContents(context.Background(), "fresh")

		assert.Equal(t, 1, root.onDidEdit,
			"reload must reach Subscribe subscribers")
	})

	t.Run("is undoable", func(t *testing.T) {
		b := newBufferWithContent(t, "old")
		b.ReloadContents(context.Background(), "fresh")
		require.Equal(t, "fresh", b.String())

		ok, _ := b.Undo()
		assert.True(t, ok)
		assert.Equal(t, "old", b.String())
	})
}

// recordingEditor counts Edit invocations so tests can assert whether a
// Buffer routed a mutation through its installed Editor chain.
type recordingEditor struct {
	delegate Editor
	calls    int
}

func (e *recordingEditor) Edit(
	ctx context.Context, start, end term.Coordinates, str string,
) (from, to term.Coordinates, old string) {
	e.calls++
	if e.delegate != nil {
		return e.delegate.Edit(ctx, start, end, str)
	}
	return start, start, ""
}

func TestBufferResetCells(t *testing.T) {
	t.Run("preserves Buffer/rawCells identity and bypasses Editor and subscribers", func(t *testing.T) {
		b := NewBuffer()
		_, err := b.ReadFrom(strings.NewReader("hello\nworld"))
		require.NoError(t, err)

		// Install a recording editor on top of the existing editor
		// chain so we can detect whether ResetCells routes through it.
		rec := &recordingEditor{delegate: b.WithEditor(nil)}
		b.WithEditor(rec)

		// Install a subscriber so we can detect whether ResetCells
		// notifies the publisher.
		sub := &testSubscriber{}
		b.Subscribe(sub)

		bufPtr := b

		newCells := term.StringToCells("restored\ncontents")
		b.ResetCells(newCells)

		assert.Same(t, bufPtr, b, "*Buffer identity must be preserved")
		assert.Equal(t, "restored\ncontents", b.String(),
			"buffer contents must reflect the ResetCells payload")
		assert.Equal(t, 0, rec.calls,
			"ResetCells must NOT route through the installed Editor")
		assert.Equal(t, 0, sub.onWillEdit,
			"ResetCells must NOT notify Subscribers (OnWillEdit)")
		assert.Equal(t, 0, sub.onDidEdit,
			"ResetCells must NOT notify Subscribers (OnDidEdit)")
	})

	t.Run("panics on empty cells", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() { b.ResetCells(nil) },
			"ResetCells must panic on empty cells; the Buffer's "+
				"at-least-one-row invariant is the caller's "+
				"responsibility")
		assert.Panics(t, func() { b.ResetCells([][]term.Cell{}) })
	})

	t.Run("does not alias the caller's slice", func(t *testing.T) {
		b := NewBuffer()
		src := term.StringToCells("hello")
		b.ResetCells(src)

		// Mutate the source after the call; the buffer must not see it.
		src[0][0].Ch = 'X'
		assert.Equal(t, "hello", b.String(),
			"ResetCells must deep-copy the caller's cells")
	})

	t.Run("subsequent Edit through cached Editor mutates the new contents", func(t *testing.T) {
		// This pins the actual *rawCells-identity guarantee: an
		// Editor handle captured before ResetCells must keep editing
		// the same buffer afterwards. If ResetCells silently swapped
		// *rawCells (the original AltBuffer.restore bug), this Edit
		// would either no-op or panic, and the result would not
		// appear via b.String().
		b := new(Buffer)
		b.InitPerformance(64, 64, ' ')
		_, err := b.ReadFrom(strings.NewReader("stale"))
		require.NoError(t, err)

		// b.Editor() returns the safeEditor wrapping the current
		// underlying editor; for performance buffers, b.editor IS
		// the *rawCells.
		cached := b.Editor()
		b.ResetCells(term.StringToCells("fresh"))

		_, _, _ = cached.Edit(
			context.Background(),
			term.Coordinates{Y: 0, X: 5},
			term.Coordinates{Y: 0, X: 5},
			"!")
		assert.Equal(t, "fresh!", b.String(),
			"cached Editor must continue to mutate b's rawCells "+
				"after ResetCells (identity preservation)")
	})
}

func TestBufferDeleteLine(t *testing.T) {
	tsuite := []struct {
		from, to term.Coordinates
		input    string
		output   string
		start    term.Coordinates
	}{
		{
			from:   term.Coordinates{},
			to:     term.Coordinates{},
			input:  "bla\nbleh",
			output: "bla\n",
			start:  term.Coordinates{},
		},
		{
			from:   term.Coordinates{Y: 1},
			to:     term.Coordinates{Y: 1},
			input:  "bla\nbleh",
			output: "\nbleh",
			start:  term.Coordinates{X: 3},
		},
		{
			from:   term.Coordinates{Y: 1},
			to:     term.Coordinates{Y: 1},
			input:  "bla\n",
			output: "\n",
			start:  term.Coordinates{X: 3},
		},
		{
			from:   term.Coordinates{},
			to:     term.Coordinates{Y: 1},
			input:  "bla\nbleh",
			output: "bla\nbleh",
			start:  term.Coordinates{},
		},
		{
			from:   term.Coordinates{},
			to:     term.Coordinates{},
			input:  "bla",
			output: "bla",
			start:  term.Coordinates{},
		},
		{
			from:   term.Coordinates{X: 1},
			to:     term.Coordinates{Y: 1, X: 2},
			input:  "bla\nbleh",
			output: "bla\nbleh",
			start:  term.Coordinates{},
		},
		{
			// inverted from/to
			to:     term.Coordinates{X: 1},
			from:   term.Coordinates{Y: 1, X: 2},
			input:  "bla\nbleh",
			output: "bla\nbleh",
			start:  term.Coordinates{},
		},
		{
			from:   term.Coordinates{X: 1}, // should not matter that is oob
			to:     term.Coordinates{Y: 2},
			input:  "\nbla\n\nbleh\n",
			output: "\nbla\n\n",
			start:  term.Coordinates{},
		},
		{
			from: term.Coordinates{Y: 1},
			to:   term.Coordinates{Y: 2},
			input: `{
	b
	c
}`,
			output: "\tb\n\tc\n",
			start:  term.Coordinates{Y: 1},
		},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			b := NewBuffer()
			b.WriteString(tcase.input)

			start, str := b.DeleteLine(tcase.from, tcase.to)
			assert.Equal(t, tcase.start, start)
			assert.Equal(t, tcase.output, str)
		})
	}
}

func TestBufferDeleteBlock(t *testing.T) {
	tsuite := []struct {
		from, to   term.Coordinates
		input      string
		start, end term.Coordinates
		str        string
	}{
		{
			from:  term.Coordinates{X: 1},
			to:    term.Coordinates{Y: 1, X: 2},
			input: "bla\nbleh",
			start: term.Coordinates{X: 1},
			end:   term.Coordinates{Y: 1, X: 2},
			str:   "l\nl",
		},
		{ // inverted
			to:    term.Coordinates{X: 1},
			from:  term.Coordinates{Y: 1, X: 2},
			input: "bla\nbleh",
			start: term.Coordinates{X: 1},
			end:   term.Coordinates{Y: 1, X: 2},
			str:   "l\nl",
		},
		{
			from:  term.Coordinates{},
			to:    term.Coordinates{Y: 2},
			input: "\nbla\n\nbleh\n",
			start: term.Coordinates{},
			end:   term.Coordinates{Y: 2},
			str:   "\n\n",
		},
	}

	for _, tcase := range tsuite {
		b := NewBuffer()
		b.WriteString(tcase.input)

		start, str := b.DeleteBlock(tcase.from, tcase.to)
		assert.Equal(t, tcase.start, start)
		assert.Equal(t, tcase.str, str)
	}

	const str = `/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{
		curtab->tp_diffbuf[i] = NULL;
		curtab->tp_diff_invalid = TRUE;
		diff_redraw(TRUE);
		}
	}
	}
	else
	diff_buf_add(win->w_buffer);
} /* {                                                                                 */`

	t.Run("does not OOB for lines that are shorter than to", func(t *testing.T) {
		b := NewBuffer()
		b.WriteString(str)

		assert.Equal(t, str, b.String())
		to := term.Coordinates{X: 89, Y: 30}
		start, _ := b.DeleteBlock(term.Coordinates{}, to)
		assert.Equal(t, term.Coordinates{}, start)
	})

	t.Run("does not OOB for lines that are shorter than from", func(t *testing.T) {
		expected := "/\n \n \n \n\t\nd\n{\n\t\n\t\n\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n\t\n}"
		b := NewBuffer()
		b.WriteString(str)

		assert.Equal(t, str, b.String())
		to := term.Coordinates{X: 1, Y: 0}
		from := term.Coordinates{X: 89, Y: 30}
		start, _ := b.DeleteBlock(from, to)
		assert.Equal(t, to, start)

		assert.Equal(t, expected, b.String())
	})
}

func TestBufferShiftRowTabs(t *testing.T) {
	stringNoTab := "the_3T_ring_idea_is_fucking_cool\n:D"
	origString := "\t" + stringNoTab
	buf := NewBuffer()
	buf.ReadFrom(strings.NewReader(origString))

	buf.ShiftRowLeft(0)
	assert.Equal(t, stringNoTab, buf.String())

	buf.ShiftRowLeft(0)
	assert.Equal(t, stringNoTab, buf.String())

	buf.ShiftRowRight(0)
	assert.Equal(t, origString, buf.String())
}

func TestBufferShiftRowTabsWithMultiWidthChar(t *testing.T) {
	stringNoTab := "💥the_3T_ring_idea_is_fucking_cool\n:D"
	origString := "\t" + stringNoTab
	buf := NewBuffer()
	buf.ReadFrom(strings.NewReader(origString))

	buf.ShiftRowLeft(0)
	assert.Equal(t, stringNoTab, buf.String())

	buf.ShiftRowLeft(0)
	assert.Equal(t, stringNoTab, buf.String())

	buf.ShiftRowRight(0)
	assert.Equal(t, origString, buf.String())
}

func TestBufferShiftRowSpaces(t *testing.T) {
	origString := " \t"
	buf := NewBuffer()
	buf.ReadFrom(strings.NewReader(origString))

	buf.ShiftRowLeft(0)
	assert.Equal(t, "\t", buf.String())

	buf.ShiftRowLeft(0)
	assert.Equal(t, "", buf.String())
}

func TestBufferUnsubscribe(t *testing.T) {
	buf := NewBuffer()
	one := &testSubscriber{}
	two := &testSubscriber{}
	buf.Subscribe(one)
	buf.Subscribe(two)

	assert.Panics(t, func() {
		buf.Unsubscribe(&testSubscriber{})
	})

	assert.NotPanics(t, func() {
		buf.Unsubscribe(two)
		buf.Unsubscribe(one)
	})
}

func TestBufferSubscribe(t *testing.T) {
	t.Run("dispatches events", func(t *testing.T) {
		buf := NewBuffer()
		one := &testSubscriber{}
		two := &testSubscriber{}
		buf.Subscribe(one)
		buf.Subscribe(two)

		buf.WriteString("\n")
		assert.Equal(t, 1, one.onWillEdit)
		assert.Equal(t, 1, one.onDidEdit)
		assert.Equal(t, 1, two.onWillEdit)
		assert.Equal(t, 1, two.onDidEdit)
	})
	t.Run("dispatches events with custom editor via WithEditor", func(t *testing.T) {
		buf := NewBuffer()

		one := &testSubscriber{}
		buf.Subscribe(one)

		ed := &testEditor{}
		ed.Editor = buf.WithEditor(ed)

		two := &testSubscriber{}
		buf.Subscribe(two)

		buf.WriteString("\n")
		assert.Equal(t, 1, one.onWillEdit)
		assert.Equal(t, 1, one.onDidEdit)
		assert.Equal(t, 1, two.onWillEdit)
		assert.Equal(t, 1, two.onDidEdit)
	})
}

func TestBufferInsertWithAttr(t *testing.T) {
	buf := NewBuffer()
	attr := term.Attributes{Fg: term.ColorRed, Bg: term.ColorYellow, Attrs: term.AttrBold}

	buf.InsertWithAttr(term.Coordinates{}, 'A', attr)
	cell := buf.RawCells()[0][0]
	assert.Equal(t, term.Cell{Ch: 'A', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1}, cell)

	buf.InsertWithAttr(term.Coordinates{X: 1}, '\n', attr)

	buf.InsertWithAttr(term.Coordinates{Y: 1}, 'E', attr)
	cell = buf.RawCells()[1][0]
	assert.Equal(t, term.Cell{Ch: 'E', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1}, cell)
}

func TestBufferInsertStringWithAttr(t *testing.T) {
	attr := term.Attributes{Fg: term.ColorRed, Bg: term.ColorYellow, Attrs: term.AttrItalic}
	t.Run("insert single line string", func(t *testing.T) {
		buf := NewBuffer()
		buf.InsertStringWithAttr(term.Coordinates{}, "Atza", attr)
		row := buf.RawCells()[0]
		assert.Equal(t, []term.Cell{
			{Ch: 'A', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
			{Ch: 't', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
			{Ch: 'z', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
			{Ch: 'a', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
		}, row)
	})
	t.Run("insert multi line string", func(t *testing.T) {
		buf := NewBuffer()
		buf.InsertStringWithAttr(term.Coordinates{}, "Lola\nGranola", attr)
		cells := buf.RawCells()
		assert.Equal(t, [][]term.Cell{
			{
				{Ch: 'L', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'o', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'l', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'a', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
			},
			{
				{Ch: 'G', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'r', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'a', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'n', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'o', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'l', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
				{Ch: 'a', Fg: attr.Fg, Bg: attr.Bg, Attrs: attr.Attrs, Bytes: 1, Width: 1},
			},
		}, cells)
	})
}

func TestBufferHeightWidth(t *testing.T) {
	buf := NewBuffer()
	buf.WriteString("aaaaaaaaaaaaaaaaaaaa\naaa\naaaaaaa\naaa")
	assert.Equal(t, 4, buf.Height())
	assert.Equal(t, 20, buf.Width())
}

func TestBufferMaxColumns(t *testing.T) {
	buf := newBufferWithContent(t, longStr)
	assert.Equal(t, buf.MaxColumns(), 44)

	buf = newBufferWithContent(t, "hello\n\tworld\n")
	assert.Equal(t, buf.MaxColumns(), 6)
}

func testBufferSelect(t *testing.T, fn func(b *Buffer, from, to term.Coordinates) ([][]term.Cell, []Selection, bool)) {
	t.Run("does not panic if oob", func(t *testing.T) {
		b := NewBuffer()
		b.WriteString("a")
		_, _, ok := fn(b, term.Coordinates{X: 2}, term.Coordinates{Y: 1})
		assert.False(t, ok)
	})
}

func TestBufferSelect(t *testing.T) {
	testBufferSelect(t, (*Buffer).Select)
}

func TestBufferSelectLine(t *testing.T) {
	testBufferSelect(t, (*Buffer).SelectLine)
}

func TestBufferSelectBlock(t *testing.T) {
	testBufferSelect(t, (*Buffer).SelectBlock)
}

func TestBufferVersion(t *testing.T) {
	b := NewBuffer()
	assert.Equal(t, 0, b.Version())

	b.WriteString("bla")
	assert.Equal(t, 1, b.Version())

	b.DeleteRow(0)
	assert.Equal(t, 2, b.Version())

	b.Undo()
	assert.Equal(t, 1, b.Version())

	b.Undo()
	assert.Equal(t, 0, b.Version())

	b.Undo()
	assert.Equal(t, 0, b.Version())

	for range 5 {
		b.Redo()
	}
	assert.Equal(t, 2, b.Version())

	b.Reset()
	assert.Equal(t, 3, b.Version())

	b.Insert(term.Coordinates{}, 'a')
	assert.Equal(t, 4, b.Version())
}

func TestBufferWriteStringRawCells(t *testing.T) {
	b := NewBuffer()
	b.WriteString("a\nb\nc")
	expected := [][]term.Cell{
		{{Ch: 'a', Bytes: 1, Width: 1}},
		{{Ch: 'b', Bytes: 1, Width: 1}},
		{{Ch: 'c', Bytes: 1, Width: 1}},
	}
	assert.Equal(t, expected, b.RawCells())

	b.WriteString("xyz")
	expected = [][]term.Cell{
		{{Ch: 'a', Bytes: 1, Width: 1}},
		{{Ch: 'b', Bytes: 1, Width: 1}},
		{
			{Ch: 'c', Bytes: 1, Width: 1},
			{Ch: 'x', Bytes: 1, Width: 1},
			{Ch: 'y', Bytes: 1, Width: 1},
			{Ch: 'z', Bytes: 1, Width: 1},
		},
	}
	assert.Equal(t, expected, b.RawCells())
}

func TestBufferInsertRowAt(t *testing.T) {
	tsuite := []struct {
		desc    string
		inStr   string
		inY     int
		wantOut string
	}{
		{"first row", "", 0, "\n"},
		{"first row above last row", "a", 0, "\na"},
		{"last row only one row", "a", 1, "a\n"},
		{"last row with prev rows", "z\na", 2, "z\na\n"},
		{"past last row", "a", 2, "a\n\n"},
		{"in the middle of buffer", "a\nb\nc", 1, "a\n\nb\nc"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			b := NewBuffer()
			b.WriteString(tcase.inStr)

			insertRowAt(b, tcase.inY)
			assert.Equal(t, tcase.wantOut, b.String())
		})
	}
}

type testView struct {
	reader View
}

func newUnixFileReader(r View) *testView {
	b := new(testView)
	b.reader = r
	return b
}

func (b *testView) endsWithEOL() bool {
	cells := b.reader.RawCells()
	return len(cells) > 1 && len(cells[len(cells)-1]) == 0
}

func (b *testView) Rows() (rows int) {
	rows = b.reader.Rows()
	if !b.endsWithEOL() {
		return
	}
	rows--
	return

}

func (b *testView) Columns(row int) int {
	return b.reader.Columns(row)
}

func (b *testView) Cell(pos term.Coordinates) (term.Cell, bool) {
	return b.reader.Cell(pos)
}

func (b *testView) RawCells() (cells [][]term.Cell) {
	cells = b.reader.RawCells()
	if !b.endsWithEOL() {
		return
	}
	cells = cells[:len(cells)-1]
	return
}

func (b *testView) String() string {
	if !b.endsWithEOL() {
		return b.reader.String()
	}
	return term.CellsToString(b.RawCells())
}

func TestBufferInsertRowAtWithUnixView(t *testing.T) {
	tsuite := []struct {
		desc    string
		inStr   string
		inY     int
		wantOut string
	}{
		{"(with EOL) first row", "\n", 0, "\n"},
		{"(with EOL) first row above last row", "a\n", 0, "\na"},
		{"(with EOL) last row only one row", "a\n", 1, "a\n"},
		{"(with EOL) last row with prev rows", "z\na\n", 2, "z\na\n"},
		{"(with EOL) past last row", "a\n", 2, "a\n"},
		{"(with EOL) in the middle of buffer", "a\nb\nc\n", 1, "a\n\nb\nc"},
		{"(no EOL) first row", "", 0, ""},
		{"(no EOL) first row above last row", "a", 0, "\na"},
		{"(no EOL) last row only one row", "a", 1, "a"},
		{"(no EOL) last row with prev rows", "z\na", 2, "z\na"},
		{"(no EOL) past last row", "a", 2, "a\n"},
		{"(no EOL) in the middle of buffer", "a\nb\nc", 1, "a\n\nb\nc"},
	}

	for _, tcase := range tsuite {
		t.Run("WriteString "+tcase.desc, func(t *testing.T) {
			b := NewBuffer()
			b.WithView(&testView{reader: b.View()})
			b.WriteString(tcase.inStr)

			insertRowAt(b, tcase.inY)
			assert.Equal(t, tcase.wantOut, b.String())
		})

		t.Run("ReadFrom "+tcase.desc, func(t *testing.T) {
			b := NewBuffer()
			b.WithView(&testView{reader: b.View()})
			b.ReadFrom(strings.NewReader(tcase.inStr))

			insertRowAt(b, tcase.inY)
			assert.Equal(t, tcase.wantOut, b.String())
		})
	}
}

func TestBufferReplaceAll(t *testing.T) {
	tsuite := []struct {
		desc string
		in   string
		out  string
	}{
		{"replace small content with large content no newline", "a\nb\nc\n", "aaaa\nbbbb\nccccc\ndddd\n"},
		{"replace small content with large content newline", "a\nb\nc\n", "aaaa\nbbbb\nccccc\ndddd\n"},
		{"replace large content with small content no newline", "aaaa\nbbbb\nccccc\ndddd\n", "a\nb\nc\n"},
		{"replace large content with small content newline", "aaaa\nbbbb\nccccc\ndddd\n", "a\nb\nc\n"},
		{"replace empty content with non-empty", "", "a"},
		{"replace empty content", "", ""},
		{"replace non-empty content with empty", "a", ""},
		{"replace newline content with empty", "\n", ""},
		{"replace newline content with empty", "a\n\tb\n", "\tc\nd\n"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			b := NewBuffer()
			b.WriteString(tcase.in)
			b.Replace(tcase.out)
			assert.Equal(t, tcase.out, b.String())
		})
	}

}

func insertRowAt(b *Buffer, y int) {
	at := term.Coordinates{Y: y}
	b.editor.Edit(context.Background(), at, at, "\n")
}

type testEditor struct {
	Editor
	edited int
}

func (e *testEditor) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	e.edited++
	return e.Editor.Edit(ctx, start, end, str)
}
