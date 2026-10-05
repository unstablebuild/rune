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

package streamload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// fsReader is a minimal walkdir.Reader rooted at a real directory on
// disk, sufficient for unit-testing the streamload Handler.
type fsReader struct {
	root  string
	opens atomic.Int32
}

func newFSReader(root string) *fsReader { return &fsReader{root: root} }

func (r *fsReader) URI(p string) (workspaceapi.URI, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return workspaceapi.ParseURI("file://" + p)
}

func (r *fsReader) OpenFile(p string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	r.opens.Add(1)
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.OpenFile(p, flag, perm)
}

func (r *fsReader) Stat(p string) (os.FileInfo, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.Stat(p)
}

func (r *fsReader) ReadDir(p string) ([]os.DirEntry, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.ReadDir(p)
}

// writeLines writes nLines lines of the form "lineN\n" into path and
// returns the URI.
func writeLines(tb testing.TB, dir string, name string, nLines int) workspaceapi.URI {
	tb.Helper()
	full := filepath.Join(dir, name)
	var b strings.Builder
	for i := range nLines {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	require.NoError(tb, os.WriteFile(full, []byte(b.String()), 0o644))
	u, err := workspaceapi.ParseURI("file://" + full)
	require.NoError(tb, err)
	return u
}

// prime waits for the handler's deferred open to complete and
// performs the initial read, mirroring what the first Draw does in
// production once the spinner triggers a redraw.
func prime(tb testing.TB, h *Handler) {
	tb.Helper()
	require.Eventually(tb, h.primeInitial, 5*time.Second, time.Millisecond)
}

func TestHandlerCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	uri := writeLines(t, dir, "a.txt", 3)
	r := newFSReader(dir)

	h, err := New(r, uri, Config{})
	require.NoError(t, err)
	prime(t, h)

	require.NoError(t, h.Close())
	require.NoError(t, h.Close(), "Close must be idempotent")
	assert.True(t, h.atEOF(), "Close should leave handler at EOF")
}

func TestHandlerMissingFileReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	r := newFSReader(dir)
	uri, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, "missing.txt"))
	require.NoError(t, err)

	h, err := New(r, uri, Config{})
	require.NoError(t, err,
		"the open is deferred, so a missing file must not fail New")
	t.Cleanup(func() { _ = h.Close() })
	prime(t, h)
	assert.True(t, h.atEOF())
	assert.Zero(t, h.buf.View().Rows()-1, "placeholder must stay empty")
}

func TestHandlerNilReader(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///tmp/nope")
	require.NoError(t, err)
	assert.PanicsWithValue(t,
		"streamload: nil walkdir.Reader",
		func() { _, _ = New(nil, uri, Config{}) },
		"passing a nil reader is a programmer error and must panic")
}

// blockingReader is a walkdir.Reader whose OpenFile blocks until the
// test closes release. It records whether OpenFile was ever entered
// so tests can prove New performs no I/O.
type blockingReader struct {
	fsReader
	entered chan struct{}
	release chan struct{}
}

func newBlockingReader(root string) *blockingReader {
	return &blockingReader{
		fsReader: fsReader{root: root},
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (r *blockingReader) OpenFile(
	p string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	close(r.entered)
	<-r.release
	return r.fsReader.OpenFile(p, flag, perm)
}

func TestHandlerNewDoesNotBlockOnOpen(t *testing.T) {
	dir := t.TempDir()
	uri := writeLines(t, dir, "a.txt", 5)
	r := newBlockingReader(dir)

	h, err := New(r, uri, Config{InitialPages: 1, PageRows: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	// New returned with the open still parked in OpenFile.
	<-r.entered

	// Events arriving during the open window must not block on it.
	_, _ = h.Handle(term.Event{})
	require.False(t, h.primeInitial(),
		"paging must stay gated while the open is in flight")

	close(r.release)
	prime(t, h)
	assert.False(t, h.atEOF())
}

func TestHandlerUsableBeforeOpenCompletes(t *testing.T) {
	const (
		width  = 10
		height = 4
	)
	dir := t.TempDir()
	uri := writeLines(t, dir, "f.txt", 10)
	r := newBlockingReader(dir)
	h, err := New(r, uri, Config{InitialPages: 2, Overscan: 1, PageRows: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })

	// Scrolling before the open completes must neither panic nor block on the
	// unreleased open.
	handlertest.RunHandlerSequence(t, h, width, height,
		[]handlertest.SequenceTestCase{{
			InputSequence: "jjG",
			Expected: "" +
				"          \n" +
				"          \n" +
				"          \n" +
				"          ",
		}})

	close(r.release)
	prime(t, h)
	handlertest.RunHandlerSequence(t, h, width, height,
		[]handlertest.SequenceTestCase{{
			InputSequence: "g",
			Expected: "" +
				"line0     \n" +
				"line1     \n" +
				"line2     \n" +
				"          ",
		}})
}

func TestHandlerLifecycle(t *testing.T) {
	const (
		width  = 10
		height = 4
	)
	// The streaming handler is a read-only viewer, so Cursor()
	// returns false in normal mode — no ▐ overlay appears in any
	// of these frames. Only entering / search mode would surface
	// a cursor, in the command bar.

	tests := []struct {
		name  string
		lines int
		cfg   Config
		cases []handlertest.SequenceTestCase
	}{
		{
			name:  "initial frame shows top of file",
			lines: 10,
			// PageRows=2, InitialPages=2 ⇒ 4 rows read up front.
			// Viewport is 4 rows tall; the bottom row is reserved
			// for the (empty) less command bar, leaving 3 content
			// rows so the top of the file is shown.
			cfg: Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected: "" +
					"line0     \n" +
					"line1     \n" +
					"line2     \n" +
					"          ",
			}},
		},
		{
			name:  "jjj scrolls down and triggers lazy page reads",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				// Each j is one SeekDown. By the third j the
				// viewport has moved through enough rows to trigger
				// overscan page reads. The content area is 3 rows
				// tall (bottom row is the command bar) so rows 3,4,5
				// are visible.
				InputSequence: "jjj",
				Expected: "" +
					"line3     \n" +
					"line4     \n" +
					"line5     \n" +
					"          ",
			}},
		},
		{
			name:  "k scrolls back up after scrolling down",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				InputSequence: "jjjk",
				Expected: "" +
					"line2     \n" +
					"line3     \n" +
					"line4     \n" +
					"          ",
			}},
		},
		{
			name:  "G seeks to end of loaded buffer and reads ahead one page",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				// G is a single SeekEndFile against the currently
				// loaded buffer (4 rows) followed by one overscan
				// page read. The viewport (3 content rows) shows
				// rows 2,3,4 (the buffer's trailing-newline empty
				// row pushes the max scroll offset to 2). Repeated
				// Gs keep walking forward.
				InputSequence: "G",
				Expected: "" +
					"line2     \n" +
					"line3     \n" +
					"line4     \n" +
					"          ",
			}},
		},
		{
			name:  "repeated G walks lazily all the way to true EOF",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				// Enough Gs to consume the whole file: each one
				// only seeks within the loaded buffer and pulls
				// one more page. By the time we run out of pages
				// the viewport sits at the bottom of the file —
				// the trailing-newline empty row leaves only two
				// content lines visible above the command bar.
				InputSequence: "GGGGGGGGGG",
				Expected: "" +
					"line8     \n" +
					"line9     \n" +
					"          \n" +
					"          ",
			}},
		},
		{
			name:  "g returns to start after walking forward",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				InputSequence: "GGGGGGGGGGg",
				Expected: "" +
					"line0     \n" +
					"line1     \n" +
					"line2     \n" +
					"          ",
			}},
		},
		{
			name:  "file shorter than initial pages renders padded",
			lines: 2,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected: "" +
					"line0     \n" +
					"line1     \n" +
					"          \n" +
					"          ",
			}},
		},
		{
			name:  "entering search mode shows cursor in the command bar",
			lines: 10,
			cfg:   Config{InitialPages: 2, Overscan: 1, PageRows: 2},
			cases: []handlertest.SequenceTestCase{{
				// `/` enters search mode; the search input bar is
				// superimposed at the bottom row with a `/`
				// prompt and the cursor right after it. This is
				// the only flow in which the streaming handler
				// surfaces a cursor.
				InputSequence: "/",
				Expected: "" +
					"line0     \n" +
					"line1     \n" +
					"line2     \n" +
					"/▐        ",
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uri := writeLines(t, dir, "f.txt", tc.lines)
			r := newFSReader(dir)
			h, err := New(r, uri, tc.cfg)
			require.NoError(t, err)
			prime(t, h)
			t.Cleanup(func() { _ = h.Close() })

			handlertest.RunHandlerSequence(t, h, width, height, tc.cases)
		})
	}
}

func TestHandlerLifecyclePagingAcrossLazyReads(t *testing.T) {
	const (
		width  = 10
		height = 4
	)
	dir := t.TempDir()
	uri := writeLines(t, dir, "f.txt", 10)
	r := newFSReader(dir)
	h, err := New(r, uri, Config{InitialPages: 2, Overscan: 1, PageRows: 2})
	require.NoError(t, err)
	prime(t, h)
	t.Cleanup(func() { _ = h.Close() })

	// Before any input, OpenFile has been called exactly once, by
	// the deferred open the priming read waited on.
	require.Equal(t, int32(1), r.opens.Load())

	handlertest.RunHandlerSequence(t, h, width, height, []handlertest.SequenceTestCase{
		{
			InputSequence: "j",
			Expected: "" +
				"line1     \n" +
				"line2     \n" +
				"line3     \n" +
				"          ",
		},
		{
			InputSequence: "jjj",
			Expected: "" +
				"line4     \n" +
				"line5     \n" +
				"line6     \n" +
				"          ",
		},
		{
			// Walk forward to true EOF; each G only steps within
			// the loaded buffer and reads one more page, so we
			// need several to consume the remaining rows.
			InputSequence: "GGGGG",
			Expected: "" +
				"line8     \n" +
				"line9     \n" +
				"          \n" +
				"          ",
		},
	})

	assert.True(t, h.atEOF(), "handler should be at EOF after paging to end")
}

func TestHandlerSingleGoroutineContract(t *testing.T) {
	dir := t.TempDir()
	uri := writeLines(t, dir, "a.txt", 8)
	r := newFSReader(dir)

	h, err := New(r, uri, Config{})
	require.NoError(t, err)
	prime(t, h)

	for range 100 {
		_, _ = h.Handle(term.Event{})
	}
	require.NoError(t, h.Close())
}
