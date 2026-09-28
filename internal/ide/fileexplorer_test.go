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

package ide

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	fileexplorercomp "unstable.build/rune/internal/component/fileexplorer"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
)

func TestFileExplorerHandlerRenderAndInteraction(t *testing.T) {
	tests := []struct {
		name     string
		dirs     map[string][]explorerMockEntry
		width    int
		height   int
		sequence []handlertest.SequenceTestCase
		assert   func(t *testing.T, h *fileExplorerHandler, host *testFileExplorerHost)
	}{
		{
			name: "initial render does not leak hidden row id",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: ".claude", isDir: true},
					{name: ".gitignore", isDir: false},
				},
			},
			width:  20,
			height: 6,
			sequence: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected:      " ▐  .claude/        \n o .gitignore       \n                    \n                    \n                    \n save <meta-s>      ",
			}},
		},
		{
			name: "enter toggles directory expansion",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: "src", isDir: true},
				},
				"/project/src": {
					{name: "main.go", isDir: false},
				},
			},
			width:  20,
			height: 6,
			sequence: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: " ▐  src/            \n                    \n                    \n                    \n                    \n save <meta-s>      "},
				{InputSequence: "<enter>", Expected: " ▐  src/            \n │   o main.go      \n                    \n                    \n                    \n save <meta-s>      "},
				{InputSequence: "<enter>", Expected: " ▐  src/            \n                    \n                    \n                    \n                    \n save <meta-s>      "},
			},
		},
		{
			name: "enter opens file at cursor",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: "file.go", isDir: false},
				},
			},
			width:  20,
			height: 6,
			sequence: []handlertest.SequenceTestCase{{
				InputSequence: "<enter>",
				Expected:      " ▐ file.go          \n                    \n                    \n                    \n                    \n save <meta-s>      ",
			}},
			assert: func(t *testing.T, _ *fileExplorerHandler, host *testFileExplorerHost) {
				require.Len(t, host.opened, 1)
				require.Equal(t, "file:///project/file.go", host.opened[0].String())
			},
		},
		{
			name: "click toggles directory expansion",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: "src", isDir: true},
				},
				"/project/src": {
					{name: "main.go", isDir: false},
				},
			},
			width:  20,
			height: 6,
			sequence: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected:      " ▐  src/            \n                    \n                    \n                    \n                    \n save <meta-s>      ",
			}},
			assert: func(t *testing.T, h *fileExplorerHandler, _ *testFileExplorerHost) {
				click := func() {
					_, handled := h.Handle(term.Event{
						Type: term.EventMouse, Key: term.MouseLeft, MouseY: 0,
					})
					require.True(t, handled)
					h.Handle(term.Event{
						Type: term.EventMouse, Key: term.MouseRelease, MouseY: 0,
					})
				}
				require.Equal(t, 1, h.ed.CellView().Rows())
				click()
				require.Equal(t, 2, h.ed.CellView().Rows(),
					"click on a directory row must expand it")
				click()
				require.Equal(t, 1, h.ed.CellView().Rows(),
					"clicking again must collapse it")
			},
		},
		{
			name: "click opens file at clicked row",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: "src", isDir: true},
					{name: "file.go", isDir: false},
				},
				"/project/src": {
					{name: "main.go", isDir: false},
				},
			},
			width:  20,
			height: 6,
			sequence: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected:      " ▐  src/            \n o file.go          \n                    \n                    \n                    \n save <meta-s>      ",
			}},
			assert: func(t *testing.T, h *fileExplorerHandler, host *testFileExplorerHost) {
				// Click the second row (the file), not the cursor's
				// initial row, to prove the click targets the pointer
				// position rather than the current cursor.
				_, handled := h.Handle(term.Event{
					Type: term.EventMouse, Key: term.MouseLeft, MouseY: 1,
				})
				require.True(t, handled)
				require.Len(t, host.opened, 1)
				require.Equal(t, "file:///project/file.go", host.opened[0].String())
				if sel, ok := h.Selection(); ok {
					require.Empty(t, sel, "click must not start a selection")
				}
			},
		},
		{
			name: "dimensions include longest rendered row",
			dirs: map[string][]explorerMockEntry{
				"/project": {
					{name: "very-long-file-name.go", isDir: false},
				},
			},
			width:  40,
			height: 4,
			sequence: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected:      " ▐ very-long-file-name.go               \n                                        \n                                        \n save <meta-s>                          ",
			}},
			assert: func(t *testing.T, h *fileExplorerHandler, _ *testFileExplorerHost) {
				w, hgt := h.Dimensions()
				require.GreaterOrEqual(t, w, len("  very-long-file-name.go"))
				require.Equal(t, 1+fileExplorerHintHeight, hgt)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, host := newTestFileExplorerHandler(t, tt.dirs)
			handlertest.RunHandlerSequence(t, h, tt.width, tt.height, tt.sequence)
			if tt.assert != nil {
				tt.assert(t, h, host)
			}
		})
	}
}

// TestFileExplorerClickBelowTreeIsNoop asserts that clicking on an
// empty row past the last tree entry resolves to no node and is a
// safe no-op: nothing toggles, nothing opens, and no error fires.
func TestFileExplorerClickBelowTreeIsNoop(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {
			{name: "src", isDir: true},
		},
		"/project/src": {
			{name: "main.go", isDir: false},
		},
	})
	h.Resize(20, 6)
	rowsBefore := h.ed.CellView().Rows()

	_, handled := h.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseY: 4,
	})
	require.True(t, handled, "the click is still consumed")
	require.Empty(t, host.opened, "no file must be opened below the tree")
	require.Equal(t, rowsBefore, h.ed.CellView().Rows(),
		"clicking below the last row must not toggle anything")
	require.Empty(t, host.errs, "an empty-area click is not an error")
}

// TestFileExplorerClickDoesNotScroll asserts that clicking a visible
// row in a scrolled tree opens/toggles that row without moving the
// viewport. The editor runs with autoCenter on (as in production),
// which would otherwise recenter the viewport on the click. Jerking
// the scroll out from under the user on click is a poor experience.
func TestFileExplorerClickDoesNotScroll(t *testing.T) {
	files := make([]explorerMockEntry, 0, 40)
	for i := range 40 {
		files = append(files, explorerMockEntry{
			name: fmt.Sprintf("file%02d.go", i), isDir: false,
		})
	}
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": files,
	}, vi.WithAutoCenter(true))
	h.Resize(20, 8)

	for range 5 {
		require.True(t, h.SeekDown(), "tree must be tall enough to scroll")
	}
	offsetBefore := h.SeekOffset()
	require.Positive(t, offsetBefore, "precondition: tree is scrolled")

	// Click a middle visible window row (not the row the cursor sits
	// on); with autoCenter on, moving the cursor there would recenter
	// the viewport unless the offset is preserved. The clicked file is
	// row offsetBefore+clickRow.
	const clickRow = 5
	_, handled := h.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseY: clickRow,
	})
	require.True(t, handled)

	require.Equal(t, offsetBefore, h.SeekOffset(),
		"clicking a visible row must not move the viewport")
	require.Len(t, host.opened, 1)
	require.Equal(t,
		fmt.Sprintf("file:///project/file%02d.go", offsetBefore+clickRow),
		host.opened[0].String(),
		"the clicked row must be the one opened")
}

// TestFileExplorerClickDirToggleDoesNotScroll asserts that clicking a
// visible directory row in a scrolled tree expands it without moving
// the viewport, even with the editor's autoCenter enabled (as in
// production).
func TestFileExplorerClickDirToggleDoesNotScroll(t *testing.T) {
	dirs := map[string][]explorerMockEntry{"/project": {}}
	for i := range 40 {
		name := fmt.Sprintf("dir%02d", i)
		dirs["/project"] = append(dirs["/project"],
			explorerMockEntry{name: name, isDir: true})
		dirs["/project/"+name] = []explorerMockEntry{
			{name: "child.go", isDir: false},
		}
	}
	h, _ := newTestFileExplorerHandler(t, dirs, vi.WithAutoCenter(true))
	h.Resize(20, 8)

	for range 5 {
		require.True(t, h.SeekDown(), "tree must be tall enough to scroll")
	}
	offsetBefore := h.SeekOffset()
	rowsBefore := h.ed.CellView().Rows()

	_, handled := h.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseY: 5,
	})
	require.True(t, handled)

	require.Greater(t, h.ed.CellView().Rows(), rowsBefore,
		"clicking a directory row must expand it")
	require.Equal(t, offsetBefore, h.SeekOffset(),
		"expanding a visible directory must not move the viewport")
}

// TestFileExplorerDragOpensOnlyInitialPress asserts that dragging the
// left button across rows (press, then MouseLeft moves while held)
// acts only on the initial press. Otherwise a drag would open or
// toggle every node it passes over.
func TestFileExplorerDragOpensOnlyInitialPress(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {
			{name: "a.go", isDir: false},
			{name: "b.go", isDir: false},
			{name: "c.go", isDir: false},
			{name: "d.go", isDir: false},
		},
	})
	h.Resize(20, 6)

	// Press on row 0, then drag down across rows 1..3 without
	// releasing (each is a MouseLeft while the button is held).
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseY: 0})
	for y := 1; y <= 3; y++ {
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseY: y})
	}
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseY: 3})

	require.Len(t, host.opened, 1,
		"a drag must open only the row of the initial press")
	require.Equal(t, "file:///project/a.go", host.opened[0].String())

	// After release the next press is a fresh click again.
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseY: 1})
	require.Len(t, host.opened, 2,
		"a new press after release must open again")
	require.Equal(t, "file:///project/b.go", host.opened[1].String())
}

func TestFileExplorerHandlerEnterDelegatesWhileSearching(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "findme.txt", isDir: false},
		},
		"/project/src": {
			{name: "main.go", isDir: false},
		},
	})
	h.Resize(20, 6)
	beforeRows := h.ed.CellView().Rows()

	keys, err := term.ParseKeys("/findme<enter>")
	require.NoError(t, err)
	for _, key := range keys {
		h.Handle(term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey})
	}

	require.False(t, h.ed.IsSearchMode())
	require.Empty(t, host.opened)
	require.Equal(t, beforeRows, h.ed.CellView().Rows())
	require.Equal(t, 1, h.ed.CursorAtScroll().Y)
}

func TestFileExplorerHandlerRuntimeLikeDimensionsAndRender(t *testing.T) {
	buf := cell.NewBuffer()
	comp, err := fileexplorercomp.New(buf, &explorerMockFS{dirs: map[string][]explorerMockEntry{
		"/project": {
			{name: ".claude", isDir: true},
			{name: "very-long-file-name.go", isDir: false},
		},
	}}, explorerRootURI(), fileexplorercomp.Config{Icons: text.IconSet{Directory: '', Default: 'o'}})
	require.NoError(t, err)

	ed := vi.Editor(
		vi.WithStatusBarConfig(false, text.StatusBarConfig{}),
		vi.WithAuxiliaryBar(true, text.AuxBarConfig{
			LinesEnabled:     true,
			AbsoluteLines:    false,
			FoldsEnabled:     false,
			GitEnabled:       false,
			ScheduleNextTick: func(fn func()) bool { fn(); return true },
		}),
		vi.WithGitBar(false, text.IconsBarConfig{}),
	)
	host := &testFileExplorerHost{focus: &testExplorerWindow{id: 1}, frame: true}
	uri, err := workspaceapi.ParseURI("memory:///fexplorer-test-1")
	require.NoError(t, err)
	edh, err := ed.Edit(context.Background(), uri, buf, false, false)
	require.NoError(t, err)
	h, err := newFileExplorerHandler(host, comp, buf, edh, uri, host.focus,
		testFileExplorerConfig())
	require.NoError(t, err)
	h.SetWindow(&testExplorerWindow{id: 2})
	h.Resize(32, 6)

	handlertest.RunHandlerSequence(t, h, 32, 6, []handlertest.SequenceTestCase{{
		InputSequence: "",
		// text.Editor only installs auxiliary chrome (line numbers,
		// status bar, …) when called via text.Component. The file
		// explorer goes through the bare editor path and so renders
		// the buffer view directly, with no leading "1 " line-number
		// column.
		Expected: " ▐  .claude/                    \n o very-long-file-name.go       \n                                \n                                \n                                \n save <meta-s>                  ",
	}})

	// With no bars, the handler's Dimensions reflect just the
	// buffer view. comp.Dimensions returns the same content width
	// the bare editor renders, so the handler width matches the
	// component width and stays large enough to fit the longest row.
	w, _ := h.Dimensions()
	cw, ch := comp.Dimensions()
	require.Equal(t, ch, 2)
	require.GreaterOrEqual(t, w, cw,
		"handler width must cover the buffer content width")
	require.GreaterOrEqual(t, w, len("o very-long-file-name.go"))
	h.syncWidth()
	require.Equal(t, w+2, host.lastWidth)
}

// TestFileExplorerHandlerDimensionsForPrecommitConfig reproduces the
// truncation bug reported against the file explorer where rendering
// `.pre-commit-config.yaml` clipped the trailing `l`. The handler's
// Dimensions() must report a width large enough to cover the icon,
// the trailing space, the full filename, and the aux line-number bar
// — otherwise the parent window sizes itself one cell too narrow and
// the last filename character is dropped.
//
// The root cause was that leaf text handlers used cell.View.Columns()
// for width — a count of cells, not the visual width — which
// under-reports any row containing a wide-glyph (Nerd Font icons,
// CJK). The file icon configured in production is a Nerd Font glyph
// listed by graphemecluster as width 2.
func TestFileExplorerHandlerDimensionsForPrecommitConfig(t *testing.T) {
	const fileName = ".pre-commit-config.yaml"
	// Use a width-2 Nerd Font glyph as the default file icon. The
	// graphemecluster package hardcodes U+EAEE (ok-icon) as a
	// two-cell glyph, which is the case that triggers the truncation
	// bug: cell.View.Columns(y) returns the cell count (=len(cells)),
	// NOT the visual width, so a row ending in a width-2 glyph is
	// reported one cell too narrow.
	const wideFileIcon rune = 0xEAEE // == "" in the graphemecluster width table
	buf := cell.NewBuffer()
	comp, err := fileexplorercomp.New(
		buf, &explorerMockFS{dirs: map[string][]explorerMockEntry{
			"/project": {
				{name: fileName, isDir: false},
			},
		}}, explorerRootURI(),
		fileexplorercomp.Config{
			Icons: text.IconSet{
				Extensions: map[string]rune{},
				Directory:  wideFileIcon,
				Default:    wideFileIcon,
			},
			// Match production: indent width follows editor.tabspaces
			// (default 4), so each depth level uses 4 cells.
			IndentWidth: text.DefaultConfig().Tabspaces,
		},
	)
	require.NoError(t, err)

	ed := vi.Editor(
		vi.WithStatusBarConfig(false, text.StatusBarConfig{}),
		vi.WithAuxiliaryBar(true, text.AuxBarConfig{
			LinesEnabled:     true,
			AbsoluteLines:    false,
			FoldsEnabled:     false,
			GitEnabled:       false,
			ScheduleNextTick: func(fn func()) bool { fn(); return true },
		}),
		// Note: text.Editor only installs auxiliary chrome (line
		// numbers, icons bar, …) when called via text.Component.
		// The file explorer takes the bare-editor path, so the bars
		// configured here have no effect on Dimensions and the
		// handler width covers just the buffer content.
		vi.WithIconsBar(true, text.IconsBarConfig{
			ScheduleNextTick: func(fn func()) bool { fn(); return true },
		}),
	)
	host := &testFileExplorerHost{focus: &testExplorerWindow{id: 1}, frame: true}
	uri, err := workspaceapi.ParseURI("memory:///fexplorer-precommit-test")
	require.NoError(t, err)
	edh, err := ed.Edit(context.Background(), uri, buf, false, false)
	require.NoError(t, err)
	h, err := newFileExplorerHandler(host, comp, buf, edh, uri, host.focus,
		testFileExplorerConfig())
	require.NoError(t, err)
	h.SetWindow(&testExplorerWindow{id: 2})
	// Resize to a generous width so the editor renders the full row.
	h.Resize(64, 6)

	// Required visible width for the rendered row is:
	//   icon (2, because Nerd Font) + space (1) + len(fileName)
	wantContent := 2 + 1 + len(fileName)
	wantHandler := wantContent

	w, hgt := h.Dimensions()
	require.Equal(t, 1+fileExplorerHintHeight, hgt,
		"single visible row plus the hint row")
	require.GreaterOrEqual(t, w, wantHandler,
		"handler.Dimensions().width must cover '%s' "+
			"(icon+space+name=%d); got %d",
		fileName, wantContent, w,
	)

	// Also render into a writer at the reported width and verify the
	// final character of the filename is actually visible (i.e. the
	// reported width matches what the editor renders).
	h.Resize(w, 6)
	writer := term.NewStringWriter(w, 6)
	h.Draw(writer)
	require.NoError(t, writer.Flush())
	rendered := writer.String()
	require.Contains(t, rendered, fileName,
		"rendered output must contain full filename %q (rendered=%q, w=%d)",
		fileName, rendered, w,
	)

	// syncWidth is what the file explorer calls in production after
	// every event to size the hosting window. Verify that the width
	// pushed to the host is sufficient to render the row when the
	// frame surrounds the window.
	h.syncWidth()
	hostW := host.lastWidth
	// frame=true adds 2 cells around the window; the inner cells
	// available for rendering are hostW-2.
	require.GreaterOrEqual(t, hostW-2, wantHandler,
		"host width minus frame must cover the handler's full content; "+
			"hostW=%d wantHandler=%d", hostW, wantHandler,
	)
}

// TestFileExplorerConfirmMessageFormat verifies the prompt message
// groups operations by type, uses markdown bold labels, and shows
// paths relative to the workspace root. RENAME is used when the
// source and destination share a parent directory; MOVE is used
// when they have different parents (even if the basename also
// changes).
func TestFileExplorerConfirmMessageFormat(t *testing.T) {
	h, _ := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {{name: "a", isDir: true}, {name: "b", isDir: true}},
	})
	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)
	uri := func(p string) workspaceapi.URI {
		u, err := workspaceapi.ParseURI("file://" + p)
		require.NoError(t, err)
		return u
	}
	ops := []fileexplorercomp.Operation{
		{Type: fileexplorercomp.OpCreate, NewURI: uri("/project/new.go")},
		{Type: fileexplorercomp.OpMkdir, NewURI: uri("/project/dir")},
		// Same parent, different name → RENAME.
		{
			Type:   fileexplorercomp.OpRename,
			URI:    uri("/project/old.go"),
			NewURI: uri("/project/new2.go"),
		},
		// Different parent, same name → MOVE.
		{
			Type:   fileexplorercomp.OpMove,
			URI:    uri("/project/a/x.go"),
			NewURI: uri("/project/b/x.go"),
		},
		// Different parent AND different name → MOVE (not RENAME).
		{
			Type:   fileexplorercomp.OpMove,
			URI:    uri("/project/a/y.go"),
			NewURI: uri("/project/b/z.go"),
		},
		{Type: fileexplorercomp.OpDelete, URI: uri("/project/gone")},
	}

	msg := h.confirmMessageForTest(rootURI, ops)

	require.Contains(t, msg, "Apply the following changes?")
	require.Contains(t, msg, "- **CREATE**\tnew.go")
	require.Contains(t, msg, "- **MKDIR**\tdir")
	require.Contains(t, msg, "- **RENAME**\told.go -> new2.go")
	require.Contains(t, msg, "- **MOVE**\ta/x.go -> b/x.go")
	require.Contains(t, msg, "- **MOVE**\ta/y.go -> b/z.go")
	require.Contains(t, msg, "- **DELETE**\tgone")
	// The rename op whose parents differ should NOT be shown as RENAME.
	require.NotContains(t, msg, "- **RENAME**\ta/y.go")
	// Every rendered operation must be its own list item.
	for _, line := range strings.Split(msg, "\n")[1:] {
		require.True(t, strings.HasPrefix(line, "- **"),
			"expected markdown list item, got %q", line)
	}
}

// TestFileExplorerHandlerFSEventRefreshesWhenVisibleAndClean
// asserts that an FS event arriving while the explorer is visible
// with no pending buffer edits triggers an immediate refresh that
// picks up the newly-created file.
func TestFileExplorerHandlerFSEventRefreshesWhenVisibleAndClean(t *testing.T) {
	dirs := map[string][]explorerMockEntry{
		"/project": {{name: "a.go", isDir: false}},
	}
	h, _ := newTestFileExplorerHandler(t, dirs)
	require.Contains(t, h.ed.CellView().String(), "a.go")
	require.NotContains(t, h.ed.CellView().String(), "b.go")

	// Externally add a sibling file.
	dirs["/project"] = append(dirs["/project"],
		explorerMockEntry{name: "b.go", isDir: false})

	uri, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)
	ev := textapi.Event{Type: textapi.EventTypeCreate, URI: uri}
	require.False(t, h.onFSEvent(context.Background(), ev))

	require.Contains(t, h.ed.CellView().String(), "a.go")
	require.Contains(t, h.ed.CellView().String(), "b.go")
	require.False(t, h.pendingRefresh)
}

// TestFileExplorerHandlerFSEventDefersWhilePending asserts that
// an FS event arriving while the user has unflushed buffer edits
// does not clobber those edits — it queues a refresh that is
// replayed only after the user resolves the edits (flush or
// close).
func TestFileExplorerHandlerFSEventDefersWhilePending(t *testing.T) {
	dirs := map[string][]explorerMockEntry{
		"/project": {{name: "a.go", isDir: false}},
	}
	h, _ := newTestFileExplorerHandler(t, dirs)
	buf := h.buf
	canonical := buf.String()
	// Mutate the buffer to simulate an unflushed user edit.
	buf.ReplaceContext(context.Background(), canonical+"\n typed.go")
	require.True(t, h.comp.HasPendingEdits())
	dirty := buf.String()

	dirs["/project"] = append(dirs["/project"],
		explorerMockEntry{name: "b.go", isDir: false})
	uri, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)
	require.False(t, h.onFSEvent(context.Background(),
		textapi.Event{Type: textapi.EventTypeCreate, URI: uri}))

	require.True(t, h.pendingRefresh)
	// The user's pending edit must be preserved verbatim.
	require.Equal(t, dirty, buf.String())
	require.NotContains(t, buf.String(), "b.go")
}

// TestFileExplorerHandlerOnWindowClosedReplaysPending asserts that
// closing the explorer with a pending refresh discards unflushed
// buffer edits and applies the deferred refresh so the next open
// shows the up-to-date tree.
func TestFileExplorerHandlerOnWindowClosedReplaysPending(t *testing.T) {
	dirs := map[string][]explorerMockEntry{
		"/project": {{name: "a.go", isDir: false}},
	}
	h, _ := newTestFileExplorerHandler(t, dirs)
	buf := h.buf
	canonical := buf.String()
	buf.ReplaceContext(context.Background(), canonical+"\n typed.go")
	dirs["/project"] = append(dirs["/project"],
		explorerMockEntry{name: "b.go", isDir: false})
	uri, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)
	require.False(t, h.onFSEvent(context.Background(),
		textapi.Event{Type: textapi.EventTypeCreate, URI: uri}))
	require.True(t, h.pendingRefresh)

	h.onWindowClosed()

	require.False(t, h.pendingRefresh)
	require.Contains(t, buf.String(), "a.go")
	require.Contains(t, buf.String(), "b.go")
	// The user's unflushed edit was discarded by the refresh.
	require.NotContains(t, buf.String(), "typed.go")
}

// TestFileExplorerHandlerFSEventRefreshesWhenWindowClosed asserts
// that when the explorer's window is closed (i.e. the explorer is
// not visible to the user), an FS event refreshes the tree
// immediately and discards any unflushed buffer edits.
func TestFileExplorerHandlerFSEventRefreshesWhenWindowClosed(t *testing.T) {
	dirs := map[string][]explorerMockEntry{
		"/project": {{name: "a.go", isDir: false}},
	}
	h, _ := newTestFileExplorerHandler(t, dirs)
	buf := h.buf
	canonical := buf.String()
	buf.ReplaceContext(context.Background(), canonical+"\n typed.go")
	// Simulate the explorer window being closed via the toggle.
	require.NoError(t, h.win.Close())

	dirs["/project"] = append(dirs["/project"],
		explorerMockEntry{name: "b.go", isDir: false})
	uri, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)
	require.False(t, h.onFSEvent(context.Background(),
		textapi.Event{Type: textapi.EventTypeCreate, URI: uri}))

	require.False(t, h.pendingRefresh)
	require.Contains(t, buf.String(), "a.go")
	require.Contains(t, buf.String(), "b.go")
	require.NotContains(t, buf.String(), "typed.go")
}

func TestFileExplorerHandlerRefreshPreservesExpandedDirectories(t *testing.T) {
	tests := []struct {
		name        string
		initial     map[string][]explorerMockEntry
		expand      []string
		mutate      func(map[string][]explorerMockEntry)
		wantVisible []string
		wantHidden  []string
	}{
		{
			name: "top-level expanded directory survives root sibling create",
			initial: map[string][]explorerMockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project"] = append(dirs["/project"],
					explorerMockEntry{name: "README.md", isDir: false})
			},
			wantVisible: []string{"README.md", "main.go"},
		},
		{
			name: "nested expanded directories survive root sibling create",
			initial: map[string][]explorerMockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}},
				"/project/src/pkg": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/", "pkg/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project"] = append(dirs["/project"],
					explorerMockEntry{name: "README.md", isDir: false})
			},
			wantVisible: []string{"README.md", "pkg/", "main.go"},
		},
		{
			name: "expanded directory shows newly-created child file",
			initial: map[string][]explorerMockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project/src"] = append(dirs["/project/src"],
					explorerMockEntry{name: "util.go", isDir: false})
			},
			wantVisible: []string{"main.go", "util.go"},
		},
		{
			name: "expanded directory drops deleted child file",
			initial: map[string][]explorerMockEntry{
				"/project": {{name: "src", isDir: true}},
				"/project/src": {
					{name: "main.go", isDir: false},
					{name: "old.go", isDir: false},
				},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project/src"] = []explorerMockEntry{
					{name: "main.go", isDir: false},
				}
			},
			wantVisible: []string{"main.go"},
			wantHidden:  []string{"old.go"},
		},
		{
			name: "deleted expanded top-level directory is not restored",
			initial: map[string][]explorerMockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project"] = []explorerMockEntry{
					{name: "README.md", isDir: false},
				}
				delete(dirs, "/project/src")
			},
			wantVisible: []string{"README.md"},
			wantHidden:  []string{"src/", "main.go"},
		},
		{
			name: "renamed expanded directory is not reopened under old path",
			initial: map[string][]explorerMockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project"] = []explorerMockEntry{{name: "app", isDir: true}}
				delete(dirs, "/project/src")
				dirs["/project/app"] = []explorerMockEntry{
					{name: "main.go", isDir: false},
				}
			},
			wantVisible: []string{"app/"},
			wantHidden:  []string{"src/", "main.go"},
		},
		{
			name: "deleted expanded nested directory is not restored",
			initial: map[string][]explorerMockEntry{
				"/project": {{name: "src", isDir: true}},
				"/project/src": {
					{name: "pkg", isDir: true},
					{name: "root.go", isDir: false},
				},
				"/project/src/pkg": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/", "pkg/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project/src"] = []explorerMockEntry{
					{name: "root.go", isDir: false},
				}
				delete(dirs, "/project/src/pkg")
			},
			wantVisible: []string{"src/", "root.go"},
			wantHidden:  []string{"pkg/", "main.go"},
		},
		{
			name: "collapsed nested directory remains collapsed",
			initial: map[string][]explorerMockEntry{
				"/project": {{name: "src", isDir: true}},
				"/project/src": {
					{name: "pkg", isDir: true},
					{name: "root.go", isDir: false},
				},
				"/project/src/pkg": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project/src/pkg"] = append(dirs["/project/src/pkg"],
					explorerMockEntry{name: "util.go", isDir: false})
			},
			wantVisible: []string{"src/", "pkg/", "root.go"},
			wantHidden:  []string{"main.go", "util.go"},
		},
		{
			name: "collapsed sibling remains collapsed while expanded sibling is restored",
			initial: map[string][]explorerMockEntry{
				"/project":      {{name: "docs", isDir: true}, {name: "src", isDir: true}},
				"/project/docs": {{name: "guide.md", isDir: false}},
				"/project/src":  {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				dirs["/project/docs"] = append(dirs["/project/docs"],
					explorerMockEntry{name: "api.md", isDir: false})
				dirs["/project/src"] = append(dirs["/project/src"],
					explorerMockEntry{name: "util.go", isDir: false})
			},
			wantVisible: []string{"docs/", "src/", "main.go", "util.go"},
			wantHidden:  []string{"guide.md", "api.md"},
		},
		{
			name: "previously expanded inaccessible directory is left collapsed",
			initial: map[string][]explorerMockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go", isDir: false}},
			},
			expand: []string{"src/"},
			mutate: func(dirs map[string][]explorerMockEntry) {
				delete(dirs, "/project/src")
			},
			wantVisible: []string{"src/"},
			wantHidden:  []string{"main.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dirs := cloneExplorerMockDirs(tc.initial)
			h, _ := newTestFileExplorerHandler(t, dirs)
			for _, label := range tc.expand {
				expandExplorerRowContaining(t, h, label)
			}

			tc.mutate(dirs)
			h.refreshTree()

			for _, want := range tc.wantVisible {
				require.Contains(t, h.buf.String(), want)
			}
			for _, unwanted := range tc.wantHidden {
				require.NotContains(t, h.buf.String(), unwanted)
			}
		})
	}
}

// TestFileExplorerHandlerFSEventOutsideRootIgnored asserts that
// FS events for paths not strictly under the explorer's root
// (including the root itself) do not trigger a refresh.
func TestFileExplorerHandlerFSEventOutsideRootIgnored(t *testing.T) {
	dirs := map[string][]explorerMockEntry{
		"/project": {{name: "a.go", isDir: false}},
	}
	h, _ := newTestFileExplorerHandler(t, dirs)
	before := h.buf.String()

	// Add a file on disk that should NOT be picked up because the
	// FS event is for a path outside the root.
	dirs["/project"] = append(dirs["/project"],
		explorerMockEntry{name: "b.go", isDir: false})

	for _, p := range []string{
		"file:///elsewhere/x.go", // outside root entirely
		"file:///project",        // root itself
	} {
		uri, err := workspaceapi.ParseURI(p)
		require.NoError(t, err)
		require.False(t, h.onFSEvent(context.Background(),
			textapi.Event{Type: textapi.EventTypeCreate, URI: uri}))
	}

	require.False(t, h.pendingRefresh)
	require.Equal(t, before, h.buf.String())
}

func cloneExplorerMockDirs(in map[string][]explorerMockEntry) map[string][]explorerMockEntry {
	out := make(map[string][]explorerMockEntry, len(in))
	for path, entries := range in {
		out[path] = append([]explorerMockEntry(nil), entries...)
	}
	return out
}

// TestFileExplorerEmptyWorkspaceIsVisible reproduces the "no size,
// didn't show anything" report. A workspace with nothing to render
// yields a zero-height, zero-width component, which the host would
// turn into an invisible sliver. The handler must floor that into a
// window the user can see and type the first entry into.
func TestFileExplorerEmptyWorkspaceIsVisible(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {},
	})

	compW, compH := h.comp.Dimensions()
	require.Zero(t, compW)
	require.Zero(t, compH)

	w, height := h.Dimensions()
	require.GreaterOrEqual(t, w, h.cfg.MinWidth)
	require.Equal(t, 1+fileExplorerHintHeight, height)

	h.syncWidth()
	require.GreaterOrEqual(t, host.lastWidth, h.cfg.MinWidth)
}

// TestFileExplorerEnterOnUnsavedRowReportsError covers the "I can't do
// anything with it" half of the pasted-row report: a row the user
// typed has no node identity yet, so <enter> resolves to nothing.
// Silently swallowing it leaves the user stuck; say why instead.
func TestFileExplorerEnterOnUnsavedRowReportsError(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {{name: "main.go", isDir: false}},
	})
	cols := h.buf.View().Columns(0)
	h.buf.Edit(context.Background(),
		term.Coordinates{Y: 0, X: cols},
		term.Coordinates{Y: 0, X: cols},
		"\nnewfile.go")
	require.True(t, h.ed.SetCursorAtScroll(term.Coordinates{Y: 1}))

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)
	require.Empty(t, host.opened)
	require.Len(t, host.errs, 1)
	require.Contains(t, host.errs[0].Error(), "unsaved row")
}

// TestFileExplorerReadOnlyRefusesEdits verifies that read-only mode
// refuses buffer mutations outright and refuses writes, while leaving
// navigation, expansion and opening intact.
func TestFileExplorerReadOnlyRefusesEdits(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "main.go", isDir: false},
		},
		"/project/src": {{name: "util.go", isDir: false}},
	})
	h.readOnly, h.cfg.ReadOnly = true, true
	const width, height = 20, 6
	h.Resize(width, height)
	before := handlertest.DrawHandler(h, width, height)
	version := h.buf.Version()

	sendExplorerKeys(t, h, "ihello<esc>")
	require.Equal(t, before, handlertest.DrawHandler(h, width, height))
	require.False(t, h.comp.HasPendingEdits())
	require.Equal(t, h.buf.Version(), version,
		"a locked buffer must never take the edit, not take it and "+
			"undo it")
	require.Empty(t, host.errs,
		"the hint row already says the explorer is locked; a "+
			"notification per keystroke is noise")
	require.NoError(t, h.flush())
	require.Zero(t, host.promptCalls, "read-only must never offer to write")

	// Navigation and expansion still work.
	host.errs = nil
	sendExplorerKeys(t, h, "<enter>")
	require.Equal(t, 3, h.ed.CellView().Rows(),
		"<enter> on a directory row must still expand it")
	sendExplorerKeys(t, h, "j<enter>")
	require.Len(t, host.opened, 1)
	require.Equal(t, "file:///project/src/util.go", host.opened[0].String())
	require.Empty(t, host.errs)
}

// TestFileExplorerReadOnlyUnlocksWithShiftEsc verifies the hint row
// names the way out of read-only, that <shift-esc> takes it, and that
// closing the explorer re-arms the configured default.
func TestFileExplorerReadOnlyUnlocksWithShiftEsc(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {{name: "main.go", isDir: false}},
	})
	h.readOnly, h.cfg.ReadOnly = true, true
	const width, height = 40, 4
	h.Resize(width, height)

	require.Contains(t, handlertest.DrawHandler(h, width, height),
		"<shift-esc> to enter edit mode")

	sendExplorerKeys(t, h, "<shift-esc>")
	require.False(t, h.readOnly)
	require.Contains(t, handlertest.DrawHandler(h, width, height),
		"save <meta-s>",
		"the hint names the key bound to write, not a hard-coded one")

	sendExplorerKeys(t, h, "ix<esc>")
	require.True(t, h.comp.HasPendingEdits(), "edits land once unlocked")
	require.Empty(t, host.errs)

	h.onWindowClosed()
	require.True(t, h.readOnly, "closing re-arms the configured default")
}

// TestFileExplorerUnboundSaveKeyHidesHint verifies that an unbound
// `write` drops the hint instead of naming the command prompt
// ("save <shift-;> write"), and gives the row back to the tree.
func TestFileExplorerUnboundSaveKeyHidesHint(t *testing.T) {
	h, _ := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {{name: "main.go", isDir: false}},
	})
	const width, height = 40, 4
	h.cfg.SaveKey = ""
	h.Resize(width, height)

	frame := handlertest.DrawHandler(h, width, height)
	require.NotContains(t, frame, "save")
	require.NotContains(t, frame, "write")

	_, bare := h.Dimensions()
	h.cfg.SaveKey = "<meta-s>"
	_, hinted := h.Dimensions()
	require.Equal(t, bare+1, hinted,
		"the hint row only exists when there is something to say")
}

// TestFileExplorerUnlockDropsHintWithoutSaveKey covers the transition
// the two other hint tests do not: locked with no `write` binding
// shows the unlock hint, and unlocking then leaves nothing to show.
func TestFileExplorerUnlockDropsHintWithoutSaveKey(t *testing.T) {
	h, _ := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {{name: "main.go", isDir: false}},
	})
	h.readOnly, h.cfg.ReadOnly, h.cfg.SaveKey = true, true, ""
	const width, height = 40, 4
	h.Resize(width, height)
	require.Contains(t, handlertest.DrawHandler(h, width, height),
		"<shift-esc> to enter edit mode")

	sendExplorerKeys(t, h, "<shift-esc>")
	require.NotContains(t, handlertest.DrawHandler(h, width, height),
		"shift-esc")
	require.Equal(t, height, h.contentHeight(),
		"unlocking hands the hint row back to the tree")
}

// TestFileExplorerEnterInInsertModeIsNewline verifies that once a
// modal editor leaves normal mode, <enter> keeps its text meaning in
// an editable explorer instead of expanding the row under the cursor.
func TestFileExplorerEnterInInsertModeIsNewline(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "util.go", isDir: false}},
	})
	h.Resize(20, 6)
	require.True(t, h.ed.IsNormalMode())

	sendExplorerKeys(t, h, "i<enter>")
	require.False(t, h.ed.IsNormalMode())
	require.Equal(t, 2, h.ed.CellView().Rows(),
		"<enter> in insert mode must split the row, not expand it")
	require.NotContains(t, h.buf.String(), "util.go")
	require.True(t, h.comp.HasPendingEdits())
	require.Empty(t, host.opened)

	sendExplorerKeys(t, h, "<esc>")
	require.True(t, h.ed.IsNormalMode())
}

// TestFileExplorerEnterLockedIgnoresEditorMode pins the lock as the
// override: a modal user who pressed `i` on a locked explorer still
// gets expand-or-open from <enter>, since nothing they type lands.
func TestFileExplorerEnterLockedIgnoresEditorMode(t *testing.T) {
	h, _ := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "util.go", isDir: false}},
	})
	h.readOnly, h.cfg.ReadOnly = true, true
	h.Resize(20, 6)

	sendExplorerKeys(t, h, "i<enter>")
	require.Contains(t, h.buf.String(), "util.go",
		"<enter> on a locked explorer expands regardless of mode")
	require.False(t, h.comp.HasPendingEdits())
}

// TestFileExplorerClickOnHintRowIsNoop guards the row reserved for the
// hint: it sits past the tree, so a click there must not resolve to
// the last node.
func TestFileExplorerClickOnHintRowIsNoop(t *testing.T) {
	h, host := newTestFileExplorerHandler(t, map[string][]explorerMockEntry{
		"/project": {
			{name: "a.go", isDir: false},
			{name: "b.go", isDir: false},
		},
	})
	h.Resize(20, 3)

	_, handled := h.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseY: 2,
	})
	require.True(t, handled)
	require.Empty(t, host.opened)
	require.Empty(t, host.errs)
}

func sendExplorerKeys(t *testing.T, h *fileExplorerHandler, seq string) {
	t.Helper()
	keys, err := term.ParseKeys(seq)
	require.NoError(t, err)
	for _, key := range keys {
		h.Handle(term.Event{
			Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey,
		})
	}
}

func expandExplorerRowContaining(t *testing.T, h *fileExplorerHandler, label string) {
	t.Helper()
	for y, line := range strings.Split(h.buf.String(), "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		_, opened := h.comp.ExpandNodeAt(term.Coordinates{Y: y})
		require.False(t, opened)
		return
	}
	require.Failf(t, "row not found", "no explorer row contains %q in:\n%s", label, h.buf.String())
}

func newTestFileExplorerHandler(t *testing.T, dirs map[string][]explorerMockEntry, opts ...vi.Option) (*fileExplorerHandler, *testFileExplorerHost) {
	t.Helper()
	buf := cell.NewBuffer()
	comp, err := fileexplorercomp.New(buf, &explorerMockFS{dirs: dirs}, explorerRootURI(), fileexplorercomp.Config{
		Icons: text.IconSet{Directory: '', Default: 'o'},
	})
	require.NoError(t, err)
	ed := vi.Editor(append([]vi.Option{
		vi.WithStatusBarConfig(false, text.StatusBarConfig{}),
		vi.WithAuxiliaryBar(false, text.AuxBarConfig{}),
		vi.WithGitBar(false, text.IconsBarConfig{}),
	}, opts...)...)
	host := &testFileExplorerHost{focus: &testExplorerWindow{id: 1}}
	uri, err := workspaceapi.ParseURI("memory:///fexplorer-test-2")
	require.NoError(t, err)
	edh, err := ed.Edit(context.Background(), uri, buf, false, false)
	require.NoError(t, err)
	h, err := newFileExplorerHandler(host, comp, buf, edh, uri, host.focus,
		testFileExplorerConfig())
	require.NoError(t, err)
	win := &testExplorerWindow{id: 2}
	h.SetWindow(win)
	h.SetTargetWindow(host.focus)
	h.Resize(40, 10)
	return h, host
}

// testFileExplorerConfig mirrors the shipped defaults, with a `write`
// binding so the hint row has a key to name.
func testFileExplorerConfig() fileExplorerConfig {
	return fileExplorerConfig{
		FileExplorerConfig: text.DefaultFileExplorerConfig(),
		SaveKey:            "<meta-s>",
	}
}

type testFileExplorerHost struct {
	focus       browser.Window
	opened      []workspaceapi.URI
	lastWidth   int
	errs        []error
	promptCalls int
	frame       bool
}

func (h *testFileExplorerHost) Prompt(message string, options []string, bindings []term.KeyComb, promptHandler handler.PromptHandler) browser.Window {
	h.promptCalls++
	return &testExplorerWindow{id: 99}
}

func (h *testFileExplorerHost) SetWindowWidth(win browser.Window, width int) bool {
	h.lastWidth = width
	return true
}

func (h *testFileExplorerHost) SetFocus(win browser.Window) (browser.Window, error) {
	prev := h.focus
	h.focus = win
	return prev, nil
}

func (h *testFileExplorerHost) Focus() (browser.Window, error) {
	return h.focus, nil
}

func (h *testFileExplorerHost) OpenFile(uri workspaceapi.URI, win browser.Window) error {
	h.opened = append(h.opened, uri)
	return nil
}

func (h *testFileExplorerHost) SetError(err error) {
	h.errs = append(h.errs, err)
}

func (h *testFileExplorerHost) FrameEnabled() bool {
	return h.frame
}

type testExplorerWindow struct {
	id      uint64
	closed  bool
	content browserapi.Handler
}

func (w *testExplorerWindow) SetContent(h browserapi.Handler) error {
	w.content = h
	return nil
}

func (w *testExplorerWindow) Focus() (bool, error) {
	return true, nil
}

func (w *testExplorerWindow) Close() error {
	w.closed = true
	return nil
}

func (w *testExplorerWindow) Content() (browserapi.Handler, error) {
	return w.content, nil
}

func (w *testExplorerWindow) WindowID() uint64 {
	return w.id
}

func (w *testExplorerWindow) Closed() bool {
	return w.closed
}

func (w *testExplorerWindow) IsFloating() bool {
	return false
}

func (w *testExplorerWindow) IsMinimized() (component.Alignment, bool) {
	return 0, false
}

func (w *testExplorerWindow) MinimizeUp(padding int) bool {
	return false
}

func (w *testExplorerWindow) MinimizeDown(padding int) bool {
	return false
}

func (w *testExplorerWindow) MinimizeLeft(padding int) bool {
	return false
}

func (w *testExplorerWindow) MinimizeRight(padding int) bool {
	return false
}

func (w *testExplorerWindow) Unminimize() bool {
	return false
}

func (w *testExplorerWindow) SetFrameAttr(attr term.Attributes) (term.Attributes, bool) {
	return term.Attributes{}, false
}

func (w *testExplorerWindow) Position() term.Coordinates { return term.Coordinates{} }
func (w *testExplorerWindow) Width() int                 { return 0 }
func (w *testExplorerWindow) Height() int                { return 0 }

type explorerMockEntry struct {
	name  string
	isDir bool
}

func (e explorerMockEntry) Name() string      { return e.name }
func (e explorerMockEntry) IsDir() bool       { return e.isDir }
func (e explorerMockEntry) Type() fs.FileMode { return 0 }
func (e explorerMockEntry) Info() (fs.FileInfo, error) {
	return explorerMockInfo{e}, nil
}

type explorerMockInfo struct{ e explorerMockEntry }

func (m explorerMockInfo) Name() string       { return m.e.name }
func (m explorerMockInfo) Size() int64        { return 0 }
func (m explorerMockInfo) Mode() os.FileMode  { return 0 }
func (m explorerMockInfo) ModTime() time.Time { return time.Time{} }
func (m explorerMockInfo) IsDir() bool        { return m.e.isDir }
func (m explorerMockInfo) Sys() any           { return nil }

type explorerMockFS struct {
	dirs map[string][]explorerMockEntry
}

func (m *explorerMockFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + path)
}

func (m *explorerMockFS) ReadDir(name string) ([]os.DirEntry, error) {
	entries, ok := m.dirs[name]
	if !ok {
		return nil, fmt.Errorf("not found: %s", name)
	}
	ret := make([]os.DirEntry, len(entries))
	for i, e := range entries {
		ret[i] = e
	}
	return ret, nil
}

func (m *explorerMockFS) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	panic("not implemented")
}

func (m *explorerMockFS) Remove(string) error {
	panic("not implemented")
}

func (m *explorerMockFS) Stat(string) (os.FileInfo, error) {
	panic("not implemented")
}

func (m *explorerMockFS) MkdirAll(string, os.FileMode) error {
	panic("not implemented")
}

func explorerRootURI() workspaceapi.URI {
	u, err := workspaceapi.ParseURI("file:///project")
	if err != nil {
		panic(err)
	}
	return u
}
