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

package fileexplorer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

func newComp(
	t *testing.T, dirs map[string][]mockEntry, cfg Config,
) (*Component, *cell.Buffer, *mockFS) {
	t.Helper()
	mfs := &mockFS{dirs: dirs}
	buf := cell.NewBuffer()
	c, err := New(buf, mfs, rootURI(), cfg)
	require.NoError(t, err)
	return c, buf, mfs
}

// TestNewRequiresBuffer ensures a nil buffer is rejected.
func TestNewRequiresBuffer(t *testing.T) {
	_, err := New(nil, &mockFS{}, rootURI(), Config{})
	require.Error(t, err)
}

// TestRenderEmptyDirectory verifies initial render of an empty dir.
func TestRenderEmptyDirectory(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{"/project": {}}, Config{})
	w, h := c.Dimensions()
	assert.Equal(t, 0, w)
	assert.Equal(t, 0, h)
	assert.Equal(t, "", buf.String())
}

// TestRenderFlatDirectory verifies initial render of a flat dir.
func TestRenderFlatDirectory(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "main.go", isDir: false},
			{name: "README.md", isDir: false},
		},
	}, Config{})
	assert.Equal(t, " README.md\n main.go", buf.String())
	_ = c
}

// TestDrawClipping ensures Draw honors width/height.
func TestDrawClipping(t *testing.T) {
	c, _, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "a.go", isDir: false},
			{name: "b.go", isDir: false},
			{name: "c.go", isDir: false},
		},
	}, Config{})
	w := term.NewStringWriter(4, 2)
	c.Resize(4, 2)
	comptest.TestComponent(t, c, w, []comptest.TestCase{{
		Expected: `
  a
  b`,
	}})
}

// TestDrawLargerThanContent pads remaining rows with blanks.
func TestDrawLargerThanContent(t *testing.T) {
	c, _, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "a.go"}},
	}, Config{})
	w := term.NewStringWriter(20, 10)
	c.Resize(20, 10)
	comptest.TestComponent(t, c, w, []comptest.TestCase{{
		Expected: "  a.go             \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    \n" +
			"                    ",
	}})
}

// TestNodeAt returns the URI at the requested row.
func TestNodeAt(t *testing.T) {
	c, _, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "main.go"},
		},
		"/project/src": {},
	}, Config{})

	uri, ok := c.NodeAt(term.Coordinates{Y: 0})
	require.True(t, ok)
	assert.Equal(t, "file:///project/src", uri.String())

	uri, ok = c.NodeAt(term.Coordinates{Y: 1})
	require.True(t, ok)
	assert.Equal(t, "file:///project/main.go", uri.String())

	_, ok = c.NodeAt(term.Coordinates{Y: 5})
	assert.False(t, ok)

	_, ok = c.NodeAt(term.Coordinates{Y: -1})
	assert.False(t, ok)
}

// TestExpandNodeAtFile returns the file URI, leaves buffer intact.
func TestExpandNodeAtFile(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "main.go"}},
	}, Config{})
	before := buf.String()
	uri, isFile := c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.True(t, isFile)
	assert.Equal(t, "file:///project/main.go", uri.String())
	assert.Equal(t, before, buf.String())
}

// TestExpandNodeAtDirectory toggles directory expansion.
func TestExpandNodeAtDirectory(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{})

	_, isFile := c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.False(t, isFile)
	assert.Equal(t, " src/\n│    app.go", buf.String())

	_, _ = c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.Equal(t, " src/", buf.String())
}

// TestToggleDirectoryWithoutVisibleChildren is a regression for
// issue #53: a directory that renders no child rows (empty on disk)
// used to re-parse as collapsed, so Enter could never close it.
func TestToggleDirectoryWithoutVisibleChildren(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":       {{name: "empty", isDir: true}},
		"/project/empty": {},
	}, Config{})

	_, isFile := c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.False(t, isFile)
	assert.Equal(t, " empty/", buf.String())

	_, isFile = c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.False(t, isFile)
	assert.Equal(t, " empty/", buf.String())
}

// TestExpandLevel expands directories at the same depth.
func TestExpandLevel(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "a", isDir: true},
			{name: "b", isDir: true},
			{name: "c.go"},
		},
		"/project/a": {{name: "a1.go"}},
		"/project/b": {{name: "b1.go"}},
	}, Config{})
	c.ExpandLevel(term.Coordinates{Y: 0})
	assert.Equal(t, " a/\n│    a1.go\n b/\n│    b1.go\n c.go", buf.String())
}

// TestCollapseLevel collapses every dir at a given depth.
func TestCollapseLevel(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "a", isDir: true},
			{name: "b", isDir: true},
		},
		"/project/a": {{name: "a1.go"}},
		"/project/b": {{name: "b1.go"}},
	}, Config{})
	c.ExpandLevel(term.Coordinates{Y: 0})
	c.CollapseLevel(term.Coordinates{Y: 0})
	assert.Equal(t, " a/\n b/", buf.String())
}

// TestCollapseAll collapses every directory.
func TestCollapseAll(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	c.CollapseAll()
	assert.Equal(t, " src/", buf.String())
}

// TestSorting: directories before files, each sorted alphabetically.
func TestSorting(t *testing.T) {
	_, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "z.go"},
			{name: "a.go"},
			{name: "m", isDir: true},
			{name: "b", isDir: true},
		},
		"/project/m": {},
		"/project/b": {},
	}, Config{})
	assert.Equal(t, " b/\n m/\n a.go\n z.go", buf.String())
}

// TestCustomIcons renders per-extension icons.
func TestCustomIcons(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "main.go"},
		},
		"/project/src": {},
	}, Config{
		Icons: text.IconSet{
			Directory:  '\uf07b',
			Extensions: map[string]rune{".go": '\ue627'},
		},
	})
	assert.Equal(t, "\uf07b src/\n\ue627 main.go", buf.String())
	_ = c
}

// TestCustomIndentRune honors Config.IndentRune.
func TestCustomIndentRune(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{IndentRune: '┃'})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.Contains(t, buf.String(), "┃")
}

// TestIndentAttrDefaultsToGray renders a nested tree and asserts the
// leading indent rune of a deep row carries the gray foreground
// attribute by default — this is the visual guide users see behind
// file names and it must be configurable but render by default in a
// muted color that recedes from the content.
func TestIndentAttrDefaultsToGray(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	rows := buf.View().RawCells()
	require.Len(t, rows, 2)
	// The second row renders as "│ <icon> app.go"; cell 0 is the
	// indent rune and must be gray.
	require.Equal(t, '│', rows[1][0].Ch)
	require.Equal(t, term.ColorGray, rows[1][0].Fg)
}

// TestIndentAttrHonorsConfig verifies that an explicit IndentAttr
// overrides the default gray foreground.
func TestIndentAttrHonorsConfig(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{IndentAttr: term.Attributes{Fg: term.ColorRed}})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	rows := buf.View().RawCells()
	require.Len(t, rows, 2)
	require.Equal(t, '│', rows[1][0].Ch)
	require.Equal(t, term.ColorRed, rows[1][0].Fg)
}

// TestIconAttrDefaultsToGray renders a nested tree and asserts that
// the per-row icon glyph carries the gray foreground attribute by
// default — the icons should recede visually behind file names just
// like the indent guides.
func TestIconAttrDefaultsToGray(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	rows := buf.View().RawCells()
	require.Len(t, rows, 2)
	// Row 0 is the root directory at depth 0; the icon sits at
	// column 0.
	require.Equal(t, term.ColorGray, rows[0][0].Fg)
	// Row 1 is the file at depth 1; the icon sits at column
	// 1 * IndentWidth.
	// IndentWidth defaults to 4 (matching the editor's default
	// tabspaces); see normalizeConfig.
	require.Equal(t, term.ColorGray, rows[1][4].Fg)
}

// TestIconAttrHonorsConfig verifies that an explicit IconAttr
// overrides the default gray foreground.
func TestIconAttrHonorsConfig(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{IconAttr: term.Attributes{Fg: term.ColorRed}})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	rows := buf.View().RawCells()
	require.Len(t, rows, 2)
	require.Equal(t, term.ColorRed, rows[0][0].Fg)
	require.Equal(t, term.ColorRed, rows[1][4].Fg)
}

// TestOpenDirectoryIcon verifies that expanded directories render with
// Icons.OpenDirectory while collapsed directories keep Icons.Directory.
func TestOpenDirectoryIcon(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{Icons: text.IconSet{
		Directory:     '\uf4d3',
		OpenDirectory: '\uf07c',
		Default:       '\uf40d',
	}})
	// Initially collapsed: closed icon.
	require.Equal(t, "\uf4d3 src/", buf.String())
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	// Expanded: open icon.
	require.Equal(t, "\uf07c src/\n│   \uf40d app.go", buf.String())
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	// Re-collapsed: closed icon again.
	require.Equal(t, "\uf4d3 src/", buf.String())
}

// TestCustomIndentWidthMatchesEditorTabs renders nested entries with
// IndentWidth=4 (the default editor tabspaces) so that each depth
// level lines up with a 4-cell tab. Both the renderer AND the parser
// must agree on the width: round-tripping the buffer through
// DryFlush must report no operations even though the rendered prefix
// changed shape from "│ " to "│   ".
func TestCustomIndentWidthMatchesEditorTabs(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project":     {{name: "src", isDir: true}},
		"/project/src": {{name: "app.go"}},
	}, Config{IndentWidth: 4})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	// depth 0: directory icon + space + "src/"
	// depth 1: one `│   ` indent run (4 cells) + file icon + space + name.
	require.Equal(t, " src/\n│    app.go", buf.String())
	cs := c.DryFlush()
	require.Empty(t, cs.Operations)
	require.Empty(t, cs.Conflicts)
}

// TestInitialRenderIsNotUndoable guards against vi's `u` erasing the
// whole tree: if the initial rewriteBufferFromTree bumped the buffer
// version above 0, an undo would snap the buffer back to empty,
// which a subsequent Flush would interpret as "delete everything".
// The Component must pin version 0 after the first render so undo
// fails at the tree and never wipes it.
func TestInitialRenderIsNotUndoable(t *testing.T) {
	_, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "a.go"},
			{name: "b.go"},
		},
	}, Config{})
	require.Equal(t, 0, buf.Version(),
		"buffer version must be 0 after the initial render "+
			"so vi's undo cannot erase the tree")
	before := buf.String()
	ok, _ := buf.Undo()
	require.False(t, ok, "undo must be a no-op against the initial render")
	require.Equal(t, before, buf.String(),
		"undo must not mutate the buffer when there is nothing to undo")
}

// TestExpandNodeAtReturnsBaseURIForRenamedFile guards against the
// file explorer trying to open a not-yet-flushed URI when the user
// renamed a file and then hit <enter> on it. The returned URI must
// point to the file's actual on-disk location (from baseTree) so
// the host can open it successfully. Without this, the host sees
// the pending view URI (e.g. "/project/newname.go" while the disk
// still has "/project/oldname.go") and fails to open the file.
func TestExpandNodeAtReturnsBaseURIForRenamedFile(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "old.go"}},
	}, Config{})
	// Simulate a pending rename: edit row 0 in place.
	replaceLine(buf, 0, "  new.go")
	uri, ok := c.ExpandNodeAt(term.Coordinates{Y: 0})
	require.True(t, ok, "expected <enter> on file to return (uri, true)")
	assert.Equal(t, "file:///project/old.go", uri.String(),
		"renamed-but-not-flushed rows must open the file at its "+
			"on-disk location, not at the pending view URI")
}

// TestMoveIntoDirectoryAndRenamePreservesIdentity: user `dd`s a
// top-level file, `p`s it under a sibling directory, `>>`s to
// indent into the dir, then renames it. The row ID is preserved
// through the whole sequence, so DryFlush must report a single
// MOVE (different parent + different name) and never a
// delete+create pair — which would lose the file's contents.
func TestMoveIntoDirectoryAndRenamePreservesIdentity(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "old.go"},
		},
		"/project/src": {},
	}, Config{})
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	// After expand, rows:
	//   0: " src/"
	//   1: " old.go"
	//
	// Vi `dd` on row 1: DeleteRow(1). The row is removed
	// entirely; its content is in the unnamed register.
	buf.DeleteRow(1)
	// Vi `p` with cursor at row 0: insert the yanked content
	// (with its trailing newline) at column 0 of row 1, so the
	// line is pasted *after* row 0 (the current cursor line).
	// The yanked text from DeleteRow includes no explicit
	// newline; vi's linewise paste reintroduces one. Simulate
	// that by inserting " old.go\n" at {Y:1, X:0}.
	buf.Edit(context.Background(),
		term.Coordinates{Y: 1, X: 0},
		term.Coordinates{Y: 1, X: 0},
		" old.go\n",
	)
	// Indent the (now-row-1) old.go under src/ via `>>`.
	buf.ShiftRowRight(1)
	// Rename old.go -> new.go on row 1 while still under src/.
	// Preserve the leading tab so the indent survives.
	replaceLine(buf, 1, "\t new.go")

	cs := c.DryFlush()
	require.Empty(t, cs.Conflicts, "unexpected conflicts: %+v", cs.Conflicts)

	got := toOpKeys(cs.Operations)
	want := []opKey{
		{
			Type: OpMove,
			Path: "/project/old.go",
			New:  "/project/src/new.go",
		},
	}
	assert.Equal(t, want, got,
		"move+indent+rename must stay a single MOVE — a delete+create "+
			"pair would drop the file's contents on Flush")
}

// --- DryFlush comprehensive test suite ---

// opKey uniquely identifies an Operation for set comparison. For
// OpCreate/OpMkdir only NewURI is used; for OpDelete only URI; for
// OpRename/OpMove/OpCopy both.
type opKey struct {
	Type OperationType
	Path string
	New  string
}

func toOpKeys(ops []Operation) []opKey {
	out := make([]opKey, len(ops))
	for i, op := range ops {
		k := opKey{Type: op.Type}
		switch op.Type {
		case OpCreate, OpMkdir:
			k.New = op.NewURI.Path()
		case OpDelete:
			k.Path = op.URI.Path()
		default:
			k.Path = op.URI.Path()
			k.New = op.NewURI.Path()
		}
		out[i] = k
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].New < out[j].New
	})
	return out
}

type dryFlushCase struct {
	name    string
	dirs    map[string][]mockEntry
	cfg     Config
	setup   func(t *testing.T, c *Component, buf *cell.Buffer)
	edit    func(t *testing.T, c *Component, buf *cell.Buffer)
	wantOps []opKey
	// wantConflictSubstrings: for each conflict, a substring that
	// must appear in Conflict.Message. Order insensitive.
	wantConflictSubstrings []string
}

func (tc dryFlushCase) run(t *testing.T) {
	t.Helper()
	c, buf, _ := newComp(t, tc.dirs, tc.cfg)
	if tc.setup != nil {
		tc.setup(t, c, buf)
	}
	before := buf.String()
	if tc.edit != nil {
		tc.edit(t, c, buf)
	}
	beforeDry := buf.String()
	cs := c.DryFlush()
	// Buffer must be untouched by DryFlush.
	assert.Equal(t, beforeDry, buf.String(), "DryFlush mutated the buffer")
	// Setup shouldn't affect the post-edit buffer state.
	_ = before

	if len(tc.wantConflictSubstrings) == 0 {
		assert.Empty(t, cs.Conflicts, "unexpected conflicts: %+v", cs.Conflicts)
	} else {
		require.Len(t, cs.Conflicts, len(tc.wantConflictSubstrings))
		for _, want := range tc.wantConflictSubstrings {
			found := false
			for _, cf := range cs.Conflicts {
				if strings.Contains(cf.Message, want) {
					found = true
					break
				}
			}
			assert.True(t, found, "no conflict matched substring %q", want)
		}
	}

	got := toOpKeys(cs.Operations)
	want := make([]opKey, len(tc.wantOps))
	copy(want, tc.wantOps)
	sort.Slice(want, func(i, j int) bool {
		if want[i].Type != want[j].Type {
			return want[i].Type < want[j].Type
		}
		if want[i].Path != want[j].Path {
			return want[i].Path < want[j].Path
		}
		return want[i].New < want[j].New
	})
	assert.Equal(t, want, got)
}

// replaceLine edits a single row in-place. The row's rowID is
// preserved (because the edit coordinates stay within that row).
func replaceLine(buf *cell.Buffer, y int, line string) {
	ctx := context.Background()
	cols := buf.View().Columns(y)
	buf.Edit(ctx,
		term.Coordinates{Y: y, X: 0},
		term.Coordinates{Y: y, X: cols},
		line,
	)
}

// insertAfter inserts a new line immediately after row y. Use y == -1
// to insert at the top. The new line has no associated rowID.
func insertAfter(buf *cell.Buffer, y int, line string) {
	ctx := context.Background()
	if buf.Rows() == 0 || (buf.Rows() == 1 && buf.View().Columns(0) == 0) {
		buf.Edit(ctx, term.Coordinates{}, term.Coordinates{}, line)
		return
	}
	if y < 0 {
		buf.Edit(ctx,
			term.Coordinates{Y: 0, X: 0},
			term.Coordinates{Y: 0, X: 0},
			line+"\n",
		)
		return
	}
	cols := buf.View().Columns(y)
	buf.Edit(ctx,
		term.Coordinates{Y: y, X: cols},
		term.Coordinates{Y: y, X: cols},
		"\n"+line,
	)
}

// removeLine deletes the row at y.
func removeLine(buf *cell.Buffer, y int) {
	buf.DeleteRow(y)
}

func TestDryFlush(t *testing.T) {
	cases := []dryFlushCase{
		{
			name: "noop_unchanged",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
		},
		{
			name: "noop_rewrite_same_content",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ReplaceContext(context.Background(), buf.String())
			},
		},
		{
			name: "create_file_root",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				insertAfter(buf, 0, "  new.go")
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/new.go"}},
		},
		{
			name: "create_dir_root",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				insertAfter(buf, 0, "  newdir/")
			},
			wantOps: []opKey{{Type: OpMkdir, New: "/project/newdir"}},
		},
		{
			name: "create_nested_file_in_expanded_dir",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				insertAfter(buf, 1, "│   new.go")
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/src/new.go"}},
		},
		{
			name: "create_nested_dir_in_expanded_dir",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				insertAfter(buf, 1, "│   subdir/")
			},
			wantOps: []opKey{{Type: OpMkdir, New: "/project/src/subdir"}},
		},
		{
			name: "create_nested_file_in_new_dir",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				insertAfter(buf, 0, "  newdir/")
				insertAfter(buf, 1, "│   child.go")
			},
			wantOps: []opKey{
				{Type: OpMkdir, New: "/project/newdir"},
				{Type: OpCreate, New: "/project/newdir/child.go"},
			},
		},
		{
			name: "delete_file_root",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				removeLine(buf, 0)
			},
			wantOps: []opKey{{Type: OpDelete, Path: "/project/a.go"}},
		},
		{
			name: "delete_file_nested",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				removeLine(buf, 1)
			},
			wantOps: []opKey{{Type: OpDelete, Path: "/project/src/main.go"}},
		},
		{
			name: "delete_dir_with_expanded_children",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Remove all rows.
				buf.ReplaceContext(context.Background(), "")
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/src"},
				{Type: OpDelete, Path: "/project/src/main.go"},
			},
		},
		{
			name: "delete_dir_collapsed",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				removeLine(buf, 0)
			},
			wantOps: []opKey{{Type: OpDelete, Path: "/project/src"}},
		},
		{
			name: "delete_dir_with_unknown_children",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "src", isDir: true},
					{name: "keep.go"},
				},
				"/project/src": {{name: "hidden.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// User never expanded src/. Deleting its row should
				// produce exactly one OpDelete for the dir itself.
				removeLine(buf, 0)
			},
			wantOps: []opKey{{Type: OpDelete, Path: "/project/src"}},
		},
		{
			name: "rename_file",
			dirs: map[string][]mockEntry{
				"/project": {{name: "old.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  new.go")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/old.go", New: "/project/new.go"},
			},
		},
		{
			name: "rename_dir",
			dirs: map[string][]mockEntry{
				"/project":        {{name: "olddir", isDir: true}},
				"/project/olddir": {},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  newdir/")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/olddir", New: "/project/newdir"},
			},
		},
		{
			name: "move_file",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "dest", isDir: true},
					{name: "file.go"},
				},
				"/project/dest": {},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0}) // expand dest
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// After expand, buffer is: dest/, file.go. Move file
				// under dest by indenting it and removing the root row.
				replaceLine(buf, 1, "│   file.go")
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/file.go", New: "/project/dest/file.go"},
			},
		},
		{
			name: "move_dir",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "dest", isDir: true},
					{name: "src", isDir: true},
				},
				"/project/dest": {},
				"/project/src":  {},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0}) // expand dest
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 1, "│   src/")
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/src", New: "/project/dest/src"},
			},
		},
		{
			name: "copy_file_duplicate_id",
			dirs: map[string][]mockEntry{
				"/project": {{name: "original.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// We can simulate a copy by inserting a row whose
				// content keeps the same ID implicit. The way the
				// user does this in oil.nvim is to yank+paste a
				// line, which keeps the buffer text identical but
				// there's no way to introduce two rows with the same
				// ID purely from text. Therefore, from DryFlush's
				// point of view, a copy-by-line-duplication is
				// reported as an OpCreate for the new copy. We only
				// check that the result has a create for "copy.go".
				insertAfter(buf, 0, "  copy.go")
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/copy.go"}},
		},
		{
			name: "mixed_ops",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "delete-me.go"},
					{name: "rename-me.go"},
					{name: "keep.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// After sort, rows are: delete-me, keep, rename-me.
				removeLine(buf, 0) // delete-me.go removed
				// Rows now: keep, rename-me.
				replaceLine(buf, 1, "  renamed.go") // rename-me -> renamed
				insertAfter(buf, 1, "  new.go")
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/delete-me.go"},
				{Type: OpRename, Path: "/project/rename-me.go", New: "/project/renamed.go"},
				{Type: OpCreate, New: "/project/new.go"},
			},
		},
		{
			name: "path_conflict_two_rows_same_uri",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  same.go")
				replaceLine(buf, 1, "  same.go")
			},
			wantConflictSubstrings: []string{"/project/same.go"},
			wantOps: []opKey{
				// The first row claims same.go (emitted as a rename),
				// the second row is rejected as a conflict.
				{Type: OpRename, Path: "/project/a.go", New: "/project/same.go"},
			},
		},
		{
			name: "cycle_rename_swap",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// a.go -> b.go, b.go -> a.go (swap).
				replaceLine(buf, 0, "  b.go")
				replaceLine(buf, 1, "  a.go")
			},
			// DryFlush reports both renames; ordering/temp-path
			// handling is a runtime concern covered by ordering.go.
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/a.go", New: "/project/b.go"},
				{Type: OpRename, Path: "/project/b.go", New: "/project/a.go"},
			},
		},
		{
			name: "ambiguous_depth_clamped",
			dirs: map[string][]mockEntry{
				"/project": {{name: "main.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Add a row with crazy depth; parser should clamp to
				// parent.depth+1 == 0 (root children).
				insertAfter(buf, 0, "│   │   │    deep.go")
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/deep.go"}},
		},
		{
			name: "fresh_row_no_id_treated_as_create",
			dirs: map[string][]mockEntry{
				"/project": {},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ReplaceContext(context.Background(), "  fresh.go")
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/fresh.go"}},
		},
		{
			name: "unicode_filename_rename",
			dirs: map[string][]mockEntry{
				"/project": {{name: "日本語.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  ñoño.go")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/日本語.go", New: "/project/ñoño.go"},
			},
		},
		{
			name: "custom_icon_and_indent_parse",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			cfg: Config{
				Icons: text.IconSet{
					Directory:  '\uf07b',
					Extensions: map[string]rune{".go": '\ue627'},
				},
				IndentRune: '┃',
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// User renames main.go under src to app.go.
				replaceLine(buf, 1, "┃   \ue627 app.go")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/src/main.go", New: "/project/src/app.go"},
			},
		},
		{
			name: "trailing_whitespace_ignored",
			dirs: map[string][]mockEntry{
				"/project": {{name: "old.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  new.go   ")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/old.go", New: "/project/new.go"},
			},
		},
		{
			// The user pressed `>>` (vi shift right) on a directory
			// row. ShiftRowRight inserts a single '\t' at column 0,
			// so the rendered row goes from "  src/" to "\t  src/".
			// The rowID at that buffer line is preserved by
			// OnDidEdit, so DryFlush must recognise the line as the
			// SAME directory and produce no operations.
			name: "shift_right_root_dir_one_tab_is_noop",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ShiftRowRight(0)
			},
		},
		{
			// Same setup, but the user pressed `>>` twice. The
			// rendered row goes from "  src/" to "\t\t  src/".
			// rowID is still preserved at the same buffer line, so
			// DryFlush must report no operations.
			name: "shift_right_root_dir_two_tabs_is_noop",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ShiftRowRight(0)
				buf.ShiftRowRight(0)
			},
		},
		{
			// Same setup but the directory is nested at depth 1
			// under an expanded parent. Pressing `>>` on the nested
			// row inserts a single '\t' at column 0; the line goes
			// from "│   nested/" to "\t│   nested/". rowID is
			// preserved, so DryFlush must report no operations and
			// in particular MUST NOT report a delete + create of a
			// directory whose name happens to be the leading
			// box-drawing characters or the dir's own name.
			name: "shift_right_nested_dir_one_tab_is_noop",
			dirs: map[string][]mockEntry{
				"/project":              {{name: "outer", isDir: true}},
				"/project/outer":        {{name: "nested", isDir: true}},
				"/project/outer/nested": {},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Buffer is now: "  outer/\n│   nested/"
				buf.ShiftRowRight(1)
			},
		},
		{
			// Two presses of `>>` on the nested row. Line goes from
			// "│   nested/" to "\t\t│   nested/". rowID preserved.
			name: "shift_right_nested_dir_two_tabs_is_noop",
			dirs: map[string][]mockEntry{
				"/project":              {{name: "outer", isDir: true}},
				"/project/outer":        {{name: "nested", isDir: true}},
				"/project/outer/nested": {},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ShiftRowRight(1)
				buf.ShiftRowRight(1)
			},
		},
		{
			name: "blank_rows_ignored",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				cols := buf.View().Columns(0)
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: cols},
					term.Coordinates{Y: 0, X: cols},
					"\n",
				)
			},
		},
		{
			name: "empty_buffer_deletes_everything",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ReplaceContext(context.Background(), "")
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/a.go"},
				{Type: OpDelete, Path: "/project/b.go"},
			},
		},
		{
			name: "collapsed_dir_preserves_hidden_children",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "src", isDir: true},
					{name: "main.go"},
				},
				"/project/src": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				// expand then collapse: base knows the children
				// but the view no longer displays them.
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Rename main.go at root; src/ is collapsed.
				// Buffer rows: "  src/", "  main.go".
				replaceLine(buf, 1, "  renamed.go")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/main.go", New: "/project/renamed.go"},
			},
		},
		{
			name: "rename_then_recreate_original",
			dirs: map[string][]mockEntry{
				"/project": {{name: "A"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  B")
				insertAfter(buf, 0, "  A")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/A", New: "/project/B"},
				{Type: OpCreate, New: "/project/A"},
			},
		},
		{
			// Regression: vi's `yyp` pastes a copy of the current
			// line AFTER it by inserting the yanked text (ending in
			// \n) at column 0 of the next row. The new row must
			// inherit a FRESH id (=0 → OpCreate), while the
			// existing row that gets "pushed down" must keep its
			// original id so subsequent edits to it report as
			// renames of the correct file.
			name: "yyp_line_paste_inserts_copy_not_moves_id",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "retry.go"},
					{name: "retry_test.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Simulate `yyp` on row 0: insert "  retry.go\n"
				// at {X:0, Y:1}. This pushes retry_test.go down
				// to row 2 while copying retry.go to row 1.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 1, X: 0},
					"  retry.go\n",
				)
				// User adds a "2" after "retry" on row 1,
				// turning "retry.go" into "retry2.go".
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 7},
					term.Coordinates{Y: 1, X: 7},
					"2",
				)
			},
			wantOps: []opKey{
				{Type: OpCreate, New: "/project/retry2.go"},
			},
		},
		{
			// Insertion at column 0 of a middle row, single line
			// WITHOUT trailing newline, splits the row: original
			// id stays with the old name (now on the next line),
			// and the new content takes row from.Y with id 0.
			name: "insert_prefix_line_no_newline",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Insert a complete new line before row 1.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 1, X: 0},
					"  c.go\n",
				)
			},
			wantOps: []opKey{
				{Type: OpCreate, New: "/project/c.go"},
			},
		},
		{
			name: "deeply_nested_deletion",
			dirs: map[string][]mockEntry{
				"/project":       {{name: "a", isDir: true}},
				"/project/a":     {{name: "b", isDir: true}},
				"/project/a/b":   {{name: "c", isDir: true}},
				"/project/a/b/c": {{name: "deep.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				c.ExpandNodeAt(term.Coordinates{Y: 1})
				c.ExpandNodeAt(term.Coordinates{Y: 2})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.ReplaceContext(context.Background(), "")
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/a"},
				{Type: OpDelete, Path: "/project/a/b"},
				{Type: OpDelete, Path: "/project/a/b/c"},
				{Type: OpDelete, Path: "/project/a/b/c/deep.go"},
			},
		},
		// --- Insertion variants ---
		{
			// Inserting a full row at {0,0} before any existing
			// content is a common case: `O` in vi.
			name: "insert_line_at_top_of_buffer",
			dirs: map[string][]mockEntry{
				"/project": {{name: "a.go"}, {name: "b.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 0, X: 0},
					"  zz.go\n",
				)
			},
			wantOps: []opKey{{Type: OpCreate, New: "/project/zz.go"}},
		},
		{
			// Multi-line paste creates several files at once.
			name: "paste_multiple_lines_creates_multiple_files",
			dirs: map[string][]mockEntry{
				"/project": {{name: "a.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				cols := buf.View().Columns(0)
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: cols},
					term.Coordinates{Y: 0, X: cols},
					"\n  x.go\n  y.go\n  z.go",
				)
			},
			wantOps: []opKey{
				{Type: OpCreate, New: "/project/x.go"},
				{Type: OpCreate, New: "/project/y.go"},
				{Type: OpCreate, New: "/project/z.go"},
			},
		},
		{
			// Splitting a row with a newline in the middle of the
			// filename produces a rename (front half stays on the
			// original row id) plus a create (back half on a fresh
			// row). Mirrors pressing Enter mid-name in insert mode.
			name: "split_row_midname_with_newline",
			dirs: map[string][]mockEntry{
				"/project": {{name: "hello_world.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Buffer starts as "  hello_world.go"; split at x=7
				// (between "hello" and "world.go"). Drop the "_"
				// in the same edit by widening the deletion range.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 7},
					term.Coordinates{Y: 0, X: 8},
					"\n  ",
				)
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/hello_world.go", New: "/project/hello"},
				{Type: OpCreate, New: "/project/world.go"},
			},
		},
		{
			// Backspace at column 0 merges two rows into one.
			// The remaining row inherits the id of the PREVIOUS
			// row (from.Y), so it becomes a rename of that file.
			name: "backspace_at_start_joins_rows",
			dirs: map[string][]mockEntry{
				"/project": {{name: "alpha.go"}, {name: "beta.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Simulate joining: delete from end of row 0 to
				// start of row 1. The content "\n" disappears and
				// beta.go's text is appended to alpha.go's row.
				cols := buf.View().Columns(0)
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: cols},
					term.Coordinates{Y: 1, X: 0},
					"",
				)
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/alpha.go", New: "/project/alpha.go beta.go"},
				{Type: OpDelete, Path: "/project/beta.go"},
			},
		},
		// --- Deletion variants ---
		{
			// Delete a contiguous range of rows (like `2dd`).
			name: "delete_range_spanning_multiple_rows",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
					{name: "c.go"},
					{name: "d.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Delete rows 1 and 2 (b.go and c.go) in one Edit.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 3, X: 0},
					"",
				)
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/b.go"},
				{Type: OpDelete, Path: "/project/c.go"},
			},
		},
		{
			// Deleting single characters from a name still
			// produces a rename, not create+delete.
			name: "single_character_deletes_are_renames",
			dirs: map[string][]mockEntry{
				"/project": {{name: "longname.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Delete "long" from "longname.go" via four
				// individual DeleteCell calls at x=2.
				for range 4 {
					buf.Edit(context.Background(),
						term.Coordinates{Y: 0, X: 2},
						term.Coordinates{Y: 0, X: 3},
						"",
					)
				}
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/longname.go", New: "/project/name.go"},
			},
		},
		{
			// `dG` style: delete from the cursor to the end of the
			// buffer. The edit spans to the last row but with
			// `to.X > 0`.
			name: "delete_from_cursor_to_end_of_buffer",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
					{name: "c.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 2, X: buf.View().Columns(2)},
					"",
				)
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/b.go"},
				{Type: OpDelete, Path: "/project/c.go"},
			},
		},
		// --- Replace / substitution variants ---
		{
			// Replace a multi-row range with a single line. The
			// surviving row has a fresh id (= new content).
			name: "replace_range_with_single_line",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
					{name: "c.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 3, X: 0},
					"  single.go\n",
				)
			},
			wantOps: []opKey{
				{Type: OpDelete, Path: "/project/a.go"},
				{Type: OpDelete, Path: "/project/b.go"},
				{Type: OpDelete, Path: "/project/c.go"},
				{Type: OpCreate, New: "/project/single.go"},
			},
		},
		{
			// Replace a single row with multiple lines. Only the
			// first line is pasted over the existing id; the rest
			// get fresh ids and show up as creates.
			name: "replace_single_row_with_multiple_lines",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "orig.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 0, X: buf.View().Columns(0)},
					"  one.go\n  two.go\n  three.go",
				)
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/orig.go", New: "/project/one.go"},
				{Type: OpCreate, New: "/project/two.go"},
				{Type: OpCreate, New: "/project/three.go"},
			},
		},
		// --- Moves via selection + indent (oil.nvim-style) ---
		{
			// Move a group of root-level folders INTO an existing
			// sibling folder by selecting them, removing them from
			// the top, and re-inserting them under the target dir
			// with one level of extra indentation. Oil.nvim's
			// "select + change indent" flow.
			//
			// This is the key test for the refactor: each moved
			// row keeps its id, so the change set must report
			// OpMove for each folder instead of create+delete.
			// One of the moved folders is EXPANDED, so its
			// children must also re-parent correctly (reported as
			// OpMove preserving their ids).
			name: "move_group_of_folders_into_sibling_folder",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "dest", isDir: true},
					{name: "src1", isDir: true},
					{name: "src2", isDir: true},
				},
				"/project/dest": {},
				"/project/src1": {
					{name: "a.go"},
					{name: "b.go"},
				},
				"/project/src2": {{name: "c.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Expand dest and src1 so src1's children are
				// visible (to simulate the "selected expanded
				// folder" case).
				c.ExpandNodeAt(term.Coordinates{Y: 0}) // dest
				c.ExpandNodeAt(term.Coordinates{Y: 1}) // src1
				// After setup buffer is:
				//   0: "  dest/"
				//   1: "│   src1/"    <- WRONG, src1 is at root
				// Let me re-derive: expanding dest at depth 0 adds
				// no rows (dest is empty). Expanding src1 at row
				// 1 adds children at depth 1. So buffer is:
				//   0: "  dest/"
				//   1: "  src1/"
				//   2: "│   a.go"
				//   3: "│   b.go"
				//   4: "  src2/"
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Replace rows 1..4 with the same entries indented
				// one more level under dest/, then remove the
				// now-duplicate src rows at the top. Simulate
				// users doing "V4jd" then "p" inside dest/, which
				// boils down to this mass edit:
				//
				//   0: " dest/"
				//   1: " src1/"
				//   2: "│    a.go"
				//   3: "│    b.go"
				//   4: " src2/"
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 4, X: buf.View().Columns(4)},
					"│    src1/\n│   │    a.go\n│   │    b.go\n│    src2/",
				)
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/src1", New: "/project/dest/src1"},
				{Type: OpMove, Path: "/project/src1/a.go", New: "/project/dest/src1/a.go"},
				{Type: OpMove, Path: "/project/src1/b.go", New: "/project/dest/src1/b.go"},
				{Type: OpMove, Path: "/project/src2", New: "/project/dest/src2"},
			},
		},
		{
			// Move a single file out of a subdirectory by
			// outdenting its row (decreasing indent level). Id is
			// preserved, so it's a move, not a delete+create.
			name: "outdent_row_moves_file_to_parent",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Row 1 "│   main.go" becomes "  main.go"
				// (moved to root level).
				replaceLine(buf, 1, "  main.go")
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/src/main.go", New: "/project/main.go"},
			},
		},
		// --- Adjacent edits ---
		{
			// Swap two adjacent root rows by editing each line's
			// content. Both rows keep their ids so this is two
			// renames (which happen to swap names).
			name: "swap_two_adjacent_rows",
			dirs: map[string][]mockEntry{
				"/project": {{name: "a.go"}, {name: "b.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				replaceLine(buf, 0, "  b.go")
				replaceLine(buf, 1, "  a.go")
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/a.go", New: "/project/b.go"},
				{Type: OpRename, Path: "/project/b.go", New: "/project/a.go"},
			},
		},
		{
			// Delete a line, then re-insert identical content. The
			// positional id for the deleted row is gone, but
			// parseViewTree's name-based fallback recognises the
			// fresh row as the same file, so DryFlush reports no
			// ops. The user's intent was clearly to keep the
			// file.
			name: "delete_then_reinsert_same_name_recovers_identity",
			dirs: map[string][]mockEntry{
				"/project": {{name: "foo.go"}, {name: "keep.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// After sort, rows are: foo.go (0), keep.go (1).
				removeLine(buf, 0) // delete foo.go
				// Insert a new row with identical text.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 0, X: 0},
					"  foo.go\n",
				)
			},
			// No ops — id recovered by name.
		},
		{
			// Type a name character-by-character on a fresh row.
			// Each keystroke is a separate Edit call. The row id
			// remains 0 so the final state is a single OpCreate.
			name: "incremental_typing_on_new_row",
			dirs: map[string][]mockEntry{
				"/project": {{name: "a.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				cols := buf.View().Columns(0)
				// Append newline then prefix cells.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: cols},
					term.Coordinates{Y: 0, X: cols},
					"\n  ",
				)
				// Type "n","e","w",".","g","o" one at a time.
				for _, r := range "new.go" {
					col := buf.View().Columns(1)
					buf.Edit(context.Background(),
						term.Coordinates{Y: 1, X: col},
						term.Coordinates{Y: 1, X: col},
						string(r),
					)
				}
			},
			wantOps: []opKey{
				{Type: OpCreate, New: "/project/new.go"},
			},
		},
		{
			// Reorder three rows: move the middle one to the top
			// by deleting it then pasting at row 0. Even though
			// the positional id was destroyed by the delete, the
			// name-based fallback recovers it, so DryFlush reports
			// no ops (same set of files, different display
			// order).
			name: "reorder_rows_via_cut_paste_no_ops",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "a.go"},
					{name: "b.go"},
					{name: "c.go"},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				buf.DeleteRow(1)
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 0, X: 0},
					"  b.go\n",
				)
			},
		},
		{
			// User pastes from a source file and forgets the
			// trailing newline. The pasted content appears inline
			// on the same buffer row, which becomes garbage. We
			// expect a rename of that row to the merged name
			// (rather than a create), since the id is preserved.
			// This is a regression guard against accidentally
			// splitting on inline spaces.
			name: "paste_no_trailing_newline_merges_on_row",
			dirs: map[string][]mockEntry{
				"/project": {{name: "a.go"}},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				cols := buf.View().Columns(0)
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: cols},
					term.Coordinates{Y: 0, X: cols},
					"x",
				)
			},
			wantOps: []opKey{
				{Type: OpRename, Path: "/project/a.go", New: "/project/a.gox"},
			},
		},
		{
			// Moving a DIRECTORY that wasn't expanded. The user
			// changes its depth (outdent) without ever seeing its
			// children. DryFlush must still move the children on
			// the filesystem. The children come from baseTree
			// (not visible in the buffer), and each must be
			// reported as an OpMove preserving its id so file
			// contents survive.
			name: "move_collapsed_dir_moves_children_too",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "outer", isDir: true},
					{name: "target", isDir: true},
				},
				"/project/outer": {
					{name: "nested", isDir: true},
				},
				"/project/outer/nested": {
					{name: "leaf.go"},
				},
				"/project/target": {},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0}) // outer
				// nested/ is visible but NOT expanded; its
				// leaf.go child is unknown to the view.
				c.ExpandNodeAt(term.Coordinates{Y: 1}) // expand nested
				c.ExpandNodeAt(term.Coordinates{Y: 1}) // collapse nested
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Buffer after setup:
				//   0: "  outer/"
				//   1: "│   nested/"
				//   2: "  target/"
				// Move nested/ under target/ by replacing row 1
				// with "│   nested/" indented one less (impossible
				// without target) — instead cut row 1, then
				// re-insert under target with deeper indent.
				// We emulate a single atomic edit.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 2, X: buf.View().Columns(2)},
					"  target/\n│   nested/",
				)
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/outer/nested", New: "/project/target/nested"},
				{Type: OpMove, Path: "/project/outer/nested/leaf.go", New: "/project/target/nested/leaf.go"},
			},
		},
		{
			// Regression: user yanks an expanded directory row and
			// its child, pastes them under a sibling directory,
			// presses `>>` on each pasted line to indent it, and
			// finally deletes the duplicate originals. Net effect:
			// move the directory (with its visible child) into the
			// sibling.
			//
			// The user reported a bogus single-file move involving
			// SKILL.md when the leading '\t' inserted by `>>` was
			// stripped from the parsed depth. With tabs counted as
			// depth increments, the move resolves correctly.
			name: "copy_paste_indent_move_dir_under_sibling",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "issue-create", isDir: true},
					{name: "issue-implement", isDir: true},
				},
				"/project/issue-create":    {},
				"/project/issue-implement": {{name: "SKILL.md"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Expand issue-implement/ so SKILL.md is visible.
				c.ExpandNodeAt(term.Coordinates{Y: 1})
				// Buffer rows:
				//   0: "  issue-create/"
				//   1: "  issue-implement/"
				//   2: "│   SKILL.md"
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// `yyp` of issue-implement/ at row 0 — paste before
				// row 1 so the copy sits directly under issue-create/.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 1, X: 0},
					"  issue-implement/\n",
				)
				// Rows:
				//   0: "  issue-create/"
				//   1: "  issue-implement/"  (new, id=0)
				//   2: "  issue-implement/"  (original)
				//   3: "│   SKILL.md"
				// `>>` on the pasted row.
				buf.ShiftRowRight(1)
				// `>>` on SKILL.md so it stays a child of the moved
				// issue-implement/ instead of becoming its sibling.
				buf.ShiftRowRight(3)
				// Delete the now-duplicate original issue-implement/.
				buf.DeleteRow(2)
				// Final rows:
				//   0: "  issue-create/"
				//   1: "\t  issue-implement/"  (new, id=0)
				//   2: "\t│   SKILL.md"        (id preserved)
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/issue-implement", New: "/project/issue-create/issue-implement"},
				{Type: OpMove, Path: "/project/issue-implement/SKILL.md", New: "/project/issue-create/issue-implement/SKILL.md"},
			},
		},
		{
			// Same scenario as above but with ANOTHER SKILL.md
			// already in the destination directory. The recovery
			// logic must not pick the wrong base id for the moved
			// child, and the directory move must still resolve
			// children's URIs against the moved parent.
			name: "copy_paste_indent_move_dir_with_sibling_same_name_child",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "issue-create", isDir: true},
					{name: "issue-implement", isDir: true},
				},
				"/project/issue-create":    {{name: "SKILL.md"}},
				"/project/issue-implement": {{name: "SKILL.md"}},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				c.ExpandNodeAt(term.Coordinates{Y: 2})
				// Rows after expand both:
				//   0: "  issue-create/"
				//   1: "│   SKILL.md"     (id=SM_IC)
				//   2: "  issue-implement/"
				//   3: "│   SKILL.md"     (id=SM_II)
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// `yyp` issue-implement/ row (row 2) under
				// issue-create/ (paste before row 2).
				buf.Edit(context.Background(),
					term.Coordinates{Y: 2, X: 0},
					term.Coordinates{Y: 2, X: 0},
					"  issue-implement/\n",
				)
				// Rows:
				//   0: "  issue-create/"
				//   1: "│   SKILL.md"      (id=SM_IC)
				//   2: "  issue-implement/" (new, id=0)
				//   3: "  issue-implement/" (original)
				//   4: "│   SKILL.md"      (id=SM_II)
				buf.ShiftRowRight(2) // indent pasted row
				buf.ShiftRowRight(4) // indent issue-implement's SKILL.md
				buf.DeleteRow(3)     // delete original
				// Final rows:
				//   0: "  issue-create/"
				//   1: "│   SKILL.md"      (id=SM_IC)
				//   2: "\t  issue-implement/" (id recovered from base)
				//   3: "\t│   SKILL.md"      (id=SM_II)
			},
			wantOps: []opKey{
				{Type: OpMove, Path: "/project/issue-implement", New: "/project/issue-create/issue-implement"},
				{Type: OpMove, Path: "/project/issue-implement/SKILL.md", New: "/project/issue-create/issue-implement/SKILL.md"},
			},
		},
		{
			// Regression: user adds leading spaces (not '\t') to
			// an existing row as an ad-hoc indent instead of using
			// `>>`. Old logic interpreted the first two spaces as
			// the "icon + space" separator and shifted the actual
			// icon glyph into the name, producing a bogus rename
			// "pgp.go -> <icon>pgp.go". After the fix, parseLine
			// tolerates the extra whitespace: the rowID preserves
			// identity, the URI stays the same, and DryFlush is a
			// no-op. Users who want to re-parent a row should use
			// `>>` (a literal tab) — spaces alone are not enough.
			name: "leading_user_spaces_with_icon_do_not_shift_name",
			dirs: map[string][]mockEntry{
				"/project":        {{name: "crypto", isDir: true}},
				"/project/crypto": {{name: "pgp.go"}},
			},
			cfg: Config{
				Icons: text.IconSet{
					Directory:  '\uf07b',
					Extensions: map[string]rune{".go": '\ue627'},
				},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				// Buffer rows:
				//   0: "\uf07b crypto/"
				//   1: "│ \ue627 pgp.go"
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// User prepends two plain spaces to row 1 to
				// visually indent pgp.go.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 1, X: 0},
					term.Coordinates{Y: 1, X: 0},
					"  ",
				)
			},
			// No ops expected: the rowID preservation keeps pgp.go's
			// identity, and the parser must not interpret the two
			// leading user spaces as "icon + space" in the rendered
			// form.
		},
		{
			// Same as above but at depth=0 (no leading indent-pair
			// prefix before the icon). This is the exact bug the
			// user hit when they added a couple of spaces at the
			// start of "\ue627 pgp.go" to indent the file. Old
			// parseLine consumed the two spaces as icon+space and
			// produced name="\ue627 pgp.go".
			name: "leading_user_spaces_at_depth_zero_with_icon_do_not_shift_name",
			dirs: map[string][]mockEntry{
				"/project": {{name: "pgp.go"}},
			},
			cfg: Config{
				Icons: text.IconSet{
					Extensions: map[string]rune{".go": '\ue627'},
				},
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Buffer starts as "\ue627 pgp.go". Prepend 2 spaces.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 0, X: 0},
					term.Coordinates{Y: 0, X: 0},
					"  ",
				)
			},
			// No ops: rowID identity preserved, name unchanged.
		},
		{
			// Regression: user adds leading whitespace to an
			// already-indented row at depth=2 (e.g. bolt/service.go
			// under document/). The row has two "│ " pairs as
			// rendered indent. Users sometimes add extra plain
			// spaces between or after those pairs, or a single
			// space before the icon. parseLine must tolerate all of
			// those variants and still parse the row as the same
			// entry with the right name and depth.
			name: "leading_user_spaces_at_depth_two_with_icon_do_not_shift_name",
			dirs: map[string][]mockEntry{
				"/project":               {{name: "document", isDir: true}},
				"/project/document":      {{name: "bolt", isDir: true}},
				"/project/document/bolt": {{name: "service.go"}},
			},
			cfg: Config{
				Icons: text.IconSet{
					Directory:  '\uf07b',
					Extensions: map[string]rune{".go": '\ue627'},
				},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0}) // document/
				c.ExpandNodeAt(term.Coordinates{Y: 1}) // bolt/
				// Rows:
				//   0: "\uf07b document/"
				//   1: "│ \uf07b bolt/"
				//   2: "│ │ \ue627 service.go"
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// User prepends 2 plain spaces to row 2.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 2, X: 0},
					term.Coordinates{Y: 2, X: 0},
					"  ",
				)
				// Row 2 is now "  │ │ \ue627 service.go".
			},
			// No ops: rowID preserved, URI unchanged.
		},
		{
			// Same as above but the user inserts a single space
			// between the indent markers and the icon, producing
			// "│ │  \ue627 service.go" (three spaces between the
			// second indent marker and the icon). This is another
			// shape of ad-hoc user whitespace that must not shift
			// the icon into the name.
			name: "extra_space_between_indent_and_icon_do_not_shift_name",
			dirs: map[string][]mockEntry{
				"/project":               {{name: "document", isDir: true}},
				"/project/document":      {{name: "bolt", isDir: true}},
				"/project/document/bolt": {{name: "service.go"}},
			},
			cfg: Config{
				Icons: text.IconSet{
					Directory:  '\uf07b',
					Extensions: map[string]rune{".go": '\ue627'},
				},
			},
			setup: func(t *testing.T, c *Component, buf *cell.Buffer) {
				c.ExpandNodeAt(term.Coordinates{Y: 0})
				c.ExpandNodeAt(term.Coordinates{Y: 1})
			},
			edit: func(t *testing.T, c *Component, buf *cell.Buffer) {
				// Buffer row 2 starts as "│   │   \ue627 service.go".
				// User inserts one space between the last indent
				// marker and the icon at column 8.
				buf.Edit(context.Background(),
					term.Coordinates{Y: 2, X: 8},
					term.Coordinates{Y: 2, X: 8},
					" ",
				)
				// Row 2 is now "│   │    \ue627 service.go".
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t) })
	}
}

// TestFlushExecutesOperations verifies Flush mutates the filesystem
// and updates the buffer to match.
func TestFlushExecutesOperations(t *testing.T) {
	mfs := &mockFS{
		dirs:  map[string][]mockEntry{"/project": {{name: "old.go"}}},
		files: map[string][]byte{"/project/old.go": []byte("hello")},
	}
	buf := cell.NewBuffer()
	c, err := New(buf, mfs, rootURI(), Config{})
	require.NoError(t, err)

	replaceLine(buf, 0, "  new.go")

	cs, err := c.Flush()
	require.NoError(t, err)
	require.Len(t, cs.Operations, 1)
	assert.Equal(t, OpRename, cs.Operations[0].Type)

	// Buffer was rewritten to match the new base tree.
	assert.Equal(t, " new.go", buf.String())
	// FS reflects the rename.
	_, ok := mfs.files["/project/new.go"]
	assert.True(t, ok)
	_, ok = mfs.files["/project/old.go"]
	assert.False(t, ok)
}

// TestFlushBlockedByConflicts returns a non-nil error and does not
// touch the filesystem.
func TestFlushBlockedByConflicts(t *testing.T) {
	mfs := &mockFS{dirs: map[string][]mockEntry{
		"/project": {{name: "a"}, {name: "b"}},
	}}
	buf := cell.NewBuffer()
	c, err := New(buf, mfs, rootURI(), Config{})
	require.NoError(t, err)

	replaceLine(buf, 0, "  dup")
	replaceLine(buf, 1, "  dup")

	cs, err := c.Flush()
	require.Error(t, err)
	assert.True(t, cs.HasConflicts())
}

// TestDryFlushDoesNotMutate confirms DryFlush is pure.
func TestDryFlushDoesNotMutate(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "main.go"}},
	}, Config{})
	before := buf.String()
	cs := c.DryFlush()
	assert.Empty(t, cs.Operations)
	assert.Equal(t, before, buf.String())
}

// TestOperationString and TestOperationTypeString kept as regression
// guards for the Operation stringification helpers.
func TestOperationString(t *testing.T) {
	tests := []struct {
		op   Operation
		want string
	}{
		{Operation{Type: OpCreate, NewURI: mustParseURI("file:///test.go")}, "create test.go"},
		{Operation{Type: OpMkdir, NewURI: mustParseURI("file:///newdir")}, "mkdir newdir"},
		{Operation{Type: OpDelete, URI: mustParseURI("file:///old.go")}, "delete old.go"},
		{Operation{Type: OpRename, URI: mustParseURI("file:///old.go"), NewURI: mustParseURI("file:///new.go")}, "rename old.go -> new.go"},
		{Operation{Type: OpMove, URI: mustParseURI("file:///src/a.go"), NewURI: mustParseURI("file:///dst/a.go")}, "move /src/a.go -> /dst/a.go"},
		{Operation{Type: OpCopy, URI: mustParseURI("file:///a.go"), NewURI: mustParseURI("file:///b.go")}, "copy /a.go -> /b.go"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.op.String())
		})
	}
}

func TestOperationTypeString(t *testing.T) {
	tests := map[OperationType]string{
		OpCreate:          "create",
		OpMkdir:           "mkdir",
		OpDelete:          "delete",
		OpRename:          "rename",
		OpMove:            "move",
		OpCopy:            "copy",
		OperationType(99): "unknown",
	}
	for k, v := range tests {
		assert.Equal(t, v, k.String())
	}
}

func mustParseURI(s string) workspaceapi.URI {
	u, err := workspaceapi.ParseURI(s)
	if err != nil {
		panic(err)
	}
	return u
}

// TestHasPendingEditsClean reports false when the buffer matches
// the canonical rendering and there are no FS operations to apply.
func TestHasPendingEditsClean(t *testing.T) {
	c, _, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "a.go"}},
	}, Config{})
	require.False(t, c.HasPendingEdits())
}

// TestHasPendingEditsAfterEdit reports true once the user mutates
// the buffer in a way that would translate to FS operations.
func TestHasPendingEditsAfterEdit(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "a.go"}},
	}, Config{})
	buf.ReplaceContext(context.Background(),
		buf.String()+"\n new.go")
	require.True(t, c.HasPendingEdits())
}

// TestRefreshAddsNewFile reflects an externally-created file in
// the rendered tree without requiring a re-open.
func TestRefreshAddsNewFile(t *testing.T) {
	dirs := map[string][]mockEntry{
		"/project": {{name: "a.go"}},
	}
	c, buf, mfs := newComp(t, dirs, Config{})
	before := buf.String()
	require.Contains(t, before, "a.go")

	mfs.dirs["/project"] = append(mfs.dirs["/project"],
		mockEntry{name: "b.go"})

	require.NoError(t, c.Refresh())
	after := buf.String()
	require.Contains(t, after, "a.go")
	require.Contains(t, after, "b.go")
}

// TestRefreshRemovesDeletedFile reflects an externally-deleted
// file in the rendered tree.
func TestRefreshRemovesDeletedFile(t *testing.T) {
	dirs := map[string][]mockEntry{
		"/project": {{name: "a.go"}, {name: "b.go"}},
	}
	c, buf, mfs := newComp(t, dirs, Config{})
	require.Contains(t, buf.String(), "b.go")

	mfs.dirs["/project"] = []mockEntry{{name: "a.go"}}

	require.NoError(t, c.Refresh())
	after := buf.String()
	require.Contains(t, after, "a.go")
	require.NotContains(t, after, "b.go")
}

// TestRefreshRebuildsFromScratch documents the simplifying
// invariant of Refresh: it takes the same load path as a fresh
// New, so any prior expand/collapse state is discarded and only
// the root directory is read from disk. Subdirectories load
// lazily on ExpandNodeAt, just like on initial open.
func TestRefreshRebuildsFromScratch(t *testing.T) {
	dirs := map[string][]mockEntry{
		"/project": {
			{name: "src", isDir: true},
			{name: "README.md"},
		},
		"/project/src": {
			{name: "main.go"},
		},
	}
	c, buf, mfs := newComp(t, dirs, Config{})
	// Expand src so its children appear in the rendered tree.
	c.ExpandNodeAt(term.Coordinates{Y: 0})
	require.Contains(t, buf.String(), "main.go")

	// Externally add files at the root and inside src.
	mfs.dirs["/project"] = append(mfs.dirs["/project"],
		mockEntry{name: "TODO.md"})
	mfs.dirs["/project/src"] = append(mfs.dirs["/project/src"],
		mockEntry{name: "util.go"})

	require.NoError(t, c.Refresh())
	out := buf.String()
	require.Contains(t, out, "TODO.md",
		"new root entry must appear")
	require.NotContains(t, out, "main.go",
		"src must be collapsed after Refresh")
	require.NotContains(t, out, "util.go",
		"unexpanded subdir must not be read from disk")
}

func TestExpandedDirectories(t *testing.T) {
	tests := []struct {
		name     string
		dirs     map[string][]mockEntry
		expand   []string
		collapse []string
		want     []string
	}{
		{
			name: "no expanded directories",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}, {name: "README.md"}},
				"/project/src": {{name: "main.go"}},
			},
		},
		{
			name: "top-level expanded directory",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			expand: []string{"src/"},
			want:   []string{"file:///project/src"},
		},
		{
			name: "expanded empty directory",
			dirs: map[string][]mockEntry{
				"/project":       {{name: "empty", isDir: true}},
				"/project/empty": {},
			},
			expand: []string{"empty/"},
			want:   []string{"file:///project/empty"},
		},
		{
			name: "nested expanded directories",
			dirs: map[string][]mockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}, {name: "root.go"}},
				"/project/src/pkg": {{name: "main.go"}},
			},
			expand: []string{"src/", "pkg/"},
			want: []string{
				"file:///project/src",
				"file:///project/src/pkg",
			},
		},
		{
			name: "multiple expanded sibling directories",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "docs", isDir: true},
					{name: "src", isDir: true},
				},
				"/project/docs": {{name: "guide.md"}},
				"/project/src":  {{name: "main.go"}},
			},
			expand: []string{"docs/", "src/"},
			want: []string{
				"file:///project/docs",
				"file:///project/src",
			},
		},
		{
			name: "collapsed nested directory is omitted",
			dirs: map[string][]mockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}, {name: "root.go"}},
				"/project/src/pkg": {{name: "main.go"}},
			},
			expand: []string{"src/"},
			want:   []string{"file:///project/src"},
		},
		{
			name: "collapsed directory is omitted after toggle",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			expand:   []string{"src/"},
			collapse: []string{"src/"},
		},
		{
			name: "expanded child is omitted when ancestor collapses",
			dirs: map[string][]mockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}},
				"/project/src/pkg": {{name: "main.go"}},
			},
			expand:   []string{"src/", "pkg/"},
			collapse: []string{"src/"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, buf, _ := newComp(t, tc.dirs, Config{})
			for _, label := range tc.expand {
				toggleComponentRowContaining(t, c, buf, label)
			}
			for _, label := range tc.collapse {
				toggleComponentRowContaining(t, c, buf, label)
			}

			assert.Equal(t, tc.want, uriStrings(c.ExpandedDirectories()))
		})
	}
}

func TestExpandDirectories(t *testing.T) {
	tests := []struct {
		name string
		dirs map[string][]mockEntry
		cfg  Config
		uris []string
		want string
	}{
		{
			name: "empty URI list leaves tree collapsed",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			want: " src/",
		},
		{
			name: "top-level directory is expanded",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}, {name: "README.md"}},
				"/project/src": {{name: "main.go"}},
			},
			uris: []string{"file:///project/src"},
			want: " src/\n│    main.go\n README.md",
		},
		{
			name: "nested directories are expanded when ancestors are included",
			dirs: map[string][]mockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}, {name: "root.go"}},
				"/project/src/pkg": {{name: "main.go"}},
			},
			uris: []string{
				"file:///project/src",
				"file:///project/src/pkg",
			},
			want: " src/\n│    pkg/\n│   │    main.go\n│    root.go",
		},
		{
			name: "nested directory without expanded ancestor remains hidden",
			dirs: map[string][]mockEntry{
				"/project":         {{name: "src", isDir: true}},
				"/project/src":     {{name: "pkg", isDir: true}},
				"/project/src/pkg": {{name: "main.go"}},
			},
			uris: []string{"file:///project/src/pkg"},
			want: " src/",
		},
		{
			name: "multiple sibling directories are expanded",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "docs", isDir: true},
					{name: "src", isDir: true},
				},
				"/project/docs": {{name: "guide.md"}},
				"/project/src":  {{name: "main.go"}, {name: "util.go"}},
			},
			uris: []string{"file:///project/docs", "file:///project/src"},
			want: " docs/\n│    guide.md\n src/\n│    main.go\n│    util.go",
		},
		{
			name: "deleted top-level directory is ignored",
			dirs: map[string][]mockEntry{
				"/project": {{name: "README.md"}},
			},
			uris: []string{"file:///project/src"},
			want: " README.md",
		},
		{
			name: "renamed directory is not reopened from old URI",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "app", isDir: true}},
				"/project/app": {{name: "main.go"}},
			},
			uris: []string{"file:///project/src"},
			want: " app/",
		},
		{
			name: "new child file appears in restored directory",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}, {name: "util.go"}},
			},
			uris: []string{"file:///project/src"},
			want: " src/\n│    main.go\n│    util.go",
		},
		{
			name: "removed child file stays absent in restored directory",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "main.go"}},
			},
			uris: []string{"file:///project/src"},
			want: " src/\n│    main.go",
		},
		{
			name: "collapsed sibling remains collapsed",
			dirs: map[string][]mockEntry{
				"/project": {
					{name: "docs", isDir: true},
					{name: "src", isDir: true},
				},
				"/project/docs": {{name: "api.md"}, {name: "guide.md"}},
				"/project/src":  {{name: "main.go"}, {name: "util.go"}},
			},
			uris: []string{"file:///project/src"},
			want: " docs/\n src/\n│    main.go\n│    util.go",
		},
		{
			name: "inaccessible target directory remains collapsed",
			dirs: map[string][]mockEntry{
				"/project": {{name: "src", isDir: true}},
			},
			uris: []string{"file:///project/src"},
			want: " src/",
		},
		{
			name: "file URI is ignored",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}, {name: "README.md"}},
				"/project/src": {{name: "main.go"}},
			},
			uris: []string{"file:///project/README.md"},
			want: " src/\n README.md",
		},
		{
			name: "ignore filter still applies to restored children",
			dirs: map[string][]mockEntry{
				"/project":     {{name: "src", isDir: true}},
				"/project/src": {{name: "app.go"}, {name: "tmp.swp"}},
			},
			cfg:  Config{Ignore: suffixIgnore{".swp"}},
			uris: []string{"file:///project/src"},
			want: " src/\n│    app.go",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, buf, _ := newComp(t, tc.dirs, tc.cfg)

			c.ExpandDirectories(parseURIs(t, tc.uris))

			assert.Equal(t, tc.want, buf.String())
		})
	}
}

func toggleComponentRowContaining(
	t *testing.T,
	c *Component,
	buf *cell.Buffer,
	label string,
) {
	t.Helper()
	for y, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		_, isFile := c.ExpandNodeAt(term.Coordinates{Y: y})
		require.False(t, isFile)
		return
	}
	require.Failf(t, "row not found", "no explorer row contains %q in:\n%s",
		label, buf.String())
}

func uriStrings(uris []workspaceapi.URI) []string {
	if len(uris) == 0 {
		return nil
	}
	ret := make([]string, 0, len(uris))
	for _, uri := range uris {
		ret = append(ret, uri.String())
	}
	return ret
}

func parseURIs(t *testing.T, raw []string) []workspaceapi.URI {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	ret := make([]workspaceapi.URI, 0, len(raw))
	for _, s := range raw {
		ret = append(ret, mustParseURI(s))
	}
	return ret
}

// TestRefreshDiscardsUnflushedBufferEdits is a property that
// callers must know about: Refresh re-renders the canonical tree
// from disk, dropping any unflushed in-buffer edits. Callers that
// want to preserve user edits must gate on HasPendingEdits().
func TestRefreshDiscardsUnflushedBufferEdits(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {{name: "a.go"}},
	}, Config{})
	canonical := buf.String()
	buf.ReplaceContext(context.Background(),
		canonical+"\n typed.go")
	require.True(t, c.HasPendingEdits())

	require.NoError(t, c.Refresh())
	require.Equal(t, canonical, buf.String())
	require.False(t, c.HasPendingEdits())
}

// TestIgnoreFiltersOutMatchingEntries verifies that entries whose
// URI matches the configured ignore matcher are filtered out of
// the rendered tree at every level of the hierarchy.
//
// This is a regression test for RUNE-143: prior to the fix the file
// explorer rendered every entry returned by FileSystem.ReadDir,
// surfacing noise such as `.git/`, `*.swp` files and anything
// listed in `.gitignore`. The rest of the IDE (workspace event
// dispatcher, fuzzy finder, idetask watcher) all hide those
// entries via vctrl.Matcher; the explorer now does the same.
//
// The test covers:
//   - top-level ignored file (e.g. "noisy.swp")
//   - top-level ignored directory (e.g. ".git/")
//   - a non-ignored sibling (e.g. "main.go") that must remain
//   - filtering is applied lazily on expand: an ignored file
//     inside a non-ignored directory must be hidden when the
//     parent is expanded
func TestIgnoreFiltersOutMatchingEntries(t *testing.T) {
	dirs := map[string][]mockEntry{
		"/project": {
			{name: ".git", isDir: true},
			{name: "main.go", isDir: false},
			{name: "noisy.swp", isDir: false},
			{name: "src", isDir: true},
		},
		"/project/.git": {{name: "HEAD", isDir: false}},
		"/project/src":  {{name: "app.go"}, {name: "tmp.swp"}},
	}
	cfg := Config{Ignore: suffixIgnore{".git", ".swp"}}
	c, buf, _ := newComp(t, dirs, cfg)

	// Top-level: .git/ and noisy.swp are filtered, main.go and
	// src/ stay.
	assert.Equal(t, "\uf4d3 src/\n\uf40d main.go", buf.String())

	// Expanding src/ must also filter ignored siblings.
	_, _ = c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.Equal(t,
		"\uf07c src/\n│   \uf40d app.go\n\uf40d main.go",
		buf.String())

	// NodeAt skips ignored entries entirely — there is no row for
	// .git or .swp files at any depth.
	for y := range 3 {
		uri, ok := c.NodeAt(term.Coordinates{Y: y})
		require.True(t, ok, "row %d", y)
		assert.NotContains(t, uri.Path(), ".git")
		assert.NotContains(t, uri.Path(), ".swp")
	}
}

// TestExpandDirectoryWithOnlyIgnoredChildrenShowsThem covers the
// reproduction in issue #53: a visible folder whose contents are all
// gitignored (e.g. test1/*) must still list those files on expand.
func TestExpandDirectoryWithOnlyIgnoredChildrenShowsThem(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "main.go", isDir: false},
			{name: "test1", isDir: true},
		},
		"/project/test1": {
			{name: "a.go", isDir: false},
			{name: "b.go", isDir: false},
		},
	}, Config{Ignore: suffixIgnore{"/a.go", "/b.go"}})

	assert.Equal(t, " test1/\n main.go", buf.String())

	_, isFile := c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.False(t, isFile)
	assert.Equal(t, " test1/\n│    a.go\n│    b.go\n main.go", buf.String())
}

// TestSymlinkedDirectoryIsBrowsable is a regression for issue #53:
// ReadDir is lstat-based, so a symlink-to-dir used to render as a
// file. Stat must decide directory-ness; ignore still sees the
// unresolved (non-dir) entry so a dir-only pattern does not hide it.
func TestSymlinkedDirectoryIsBrowsable(t *testing.T) {
	c, buf, _ := newComp(t, map[string][]mockEntry{
		"/project": {
			{name: "linked", isDir: false, mode: os.ModeSymlink},
			{name: "real", isDir: true},
		},
		"/project/linked": {{name: "inside.go"}},
		"/project/real":   {{name: "inside.go"}},
	}, Config{Ignore: dirOnlyIgnore("linked")})

	assert.Equal(t, " linked/\n real/", buf.String())

	uri, isFile := c.ExpandNodeAt(term.Coordinates{Y: 0})
	assert.False(t, isFile)
	assert.Equal(t, workspaceapi.URI{}, uri)
	assert.Equal(t, " linked/\n│    inside.go\n real/", buf.String())
}

// TestIgnoreNilDefaultsToNoFiltering documents the zero-value
// behavior of Config.Ignore: a nil matcher is equivalent to
// vctrl.NopMatcher(false), preserving the pre-RUNE-143 default of
// "show every entry the FileSystem reports".
func TestIgnoreNilDefaultsToNoFiltering(t *testing.T) {
	dirs := map[string][]mockEntry{
		"/project": {
			{name: ".git", isDir: true},
			{name: "main.go", isDir: false},
		},
		"/project/.git": {},
	}
	c, buf, _ := newComp(t, dirs, Config{})
	assert.Equal(t, "\uf4d3 .git/\n\uf40d main.go", buf.String())
	_ = c
}

// suffixIgnore is a minimal Matcher used by the ignore tests. It
// matches any URI whose path ends with one of the configured
// suffixes (e.g. ".git", ".swp"). Using a tiny in-test matcher
// instead of vctrl.MatcherFromPatterns avoids coupling the
// component test to the gitignore parser; the integration with
// vctrl.LoadGitignore is exercised at the IDE layer in
// ide/fileexplorer_test.go and ide/workspace_handler_test.go.
type suffixIgnore []string

func (s suffixIgnore) Match(uri workspaceapi.URI, _ bool) bool {
	return s.MatchRelPath(uri.Path(), false)
}

func (s suffixIgnore) MatchRelPath(p string, _ bool) bool {
	for _, suf := range s {
		if strings.HasSuffix(p, suf) {
			return true
		}
	}
	return false
}

// dirOnlyIgnore matches a path's final component only when the
// caller reports the entry as a directory, matching gitignore
// patterns such as "linked/".
type dirOnlyIgnore string

func (d dirOnlyIgnore) Match(uri workspaceapi.URI, isDir bool) bool {
	return d.MatchRelPath(uri.Path(), isDir)
}

func (d dirOnlyIgnore) MatchRelPath(p string, isDir bool) bool {
	return isDir && strings.HasSuffix(p, string(d))
}

// --- mock filesystem ---

type mockEntry struct {
	name  string
	isDir bool
	mode  fs.FileMode
}

func (e mockEntry) Name() string { return e.name }
func (e mockEntry) IsDir() bool  { return e.isDir }
func (e mockEntry) Type() fs.FileMode {
	if e.mode != 0 {
		return e.mode
	}
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e mockEntry) Info() (fs.FileInfo, error) { return mockInfo{e}, nil }

type mockInfo struct{ e mockEntry }

func (m mockInfo) Name() string       { return m.e.name }
func (m mockInfo) Size() int64        { return 0 }
func (m mockInfo) Mode() os.FileMode  { return 0 }
func (m mockInfo) ModTime() time.Time { return time.Time{} }
func (m mockInfo) IsDir() bool        { return m.e.isDir }
func (m mockInfo) Sys() any           { return nil }

type mockFS struct {
	dirs  map[string][]mockEntry
	files map[string][]byte
}

func (m *mockFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + path)
}

func (m *mockFS) ReadDir(name string) ([]os.DirEntry, error) {
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

func (m *mockFS) OpenFile(path string, flag int, _ os.FileMode) (workspaceapi.File, error) {
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	if flag&os.O_CREATE != 0 {
		if _, ok := m.files[path]; !ok {
			m.files[path] = nil
		}
	}
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	return &mockFile{fs: m, path: path, data: data, flag: flag}, nil
}

func (m *mockFS) Remove(path string) error {
	if _, ok := m.files[path]; ok {
		delete(m.files, path)
		return nil
	}
	// Also remove from dirs map by searching parents.
	for parent, entries := range m.dirs {
		for i, e := range entries {
			full := parent + "/" + e.Name()
			if full == path {
				m.dirs[parent] = append(entries[:i], entries[i+1:]...)
				delete(m.dirs, path)
				return nil
			}
		}
	}
	return nil
}

func (m *mockFS) Stat(path string) (os.FileInfo, error) {
	if _, ok := m.files[path]; ok {
		return mockInfo{mockEntry{name: path, isDir: false}}, nil
	}
	if _, ok := m.dirs[path]; ok {
		return mockInfo{mockEntry{name: path, isDir: true}}, nil
	}
	// Fall back to scanning parents.
	for parent, entries := range m.dirs {
		for _, e := range entries {
			if parent+"/"+e.Name() == path {
				return mockInfo{e}, nil
			}
		}
	}
	return nil, fmt.Errorf("not found: %s", path)
}

func (m *mockFS) MkdirAll(path string, _ os.FileMode) error {
	if _, ok := m.dirs[path]; !ok {
		m.dirs[path] = nil
	}
	return nil
}

type mockFile struct {
	fs   *mockFS
	path string
	data []byte
	pos  int
	flag int
	buf  []byte
}

func (f *mockFile) Read(p []byte) (int, error) {
	if f.pos >= len(f.data) {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, f.data[f.pos:])
	f.pos += n
	return n, nil
}

func (f *mockFile) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	return len(p), nil
}

func (f *mockFile) Close() error {
	if f.flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		if f.flag&os.O_TRUNC != 0 {
			f.fs.files[f.path] = f.buf
		} else {
			// For O_CREATE only (without O_TRUNC), empty means empty.
			f.fs.files[f.path] = f.buf
		}
	}
	return nil
}

func (f *mockFile) Name() string { return f.path }

func (f *mockFile) Seek(int64, int) (int64, error)         { return 0, nil }
func (f *mockFile) ReadAt(_ []byte, _ int64) (int, error)  { return 0, nil }
func (f *mockFile) WriteAt(_ []byte, _ int64) (int, error) { return 0, nil }
func (f *mockFile) Truncate(int64) error                   { return nil }
func (f *mockFile) Stat() (os.FileInfo, error)             { return nil, nil }
func (f *mockFile) Sync() error                            { return nil }
func (f *mockFile) Chmod(os.FileMode) error                { return nil }
func (f *mockFile) Chown(int, int) error                   { return nil }
func (f *mockFile) SetDeadline(time.Time) error            { return nil }
func (f *mockFile) SetReadDeadline(time.Time) error        { return nil }
func (f *mockFile) SetWriteDeadline(time.Time) error       { return nil }
func (f *mockFile) Chdir() error                           { return nil }
func (f *mockFile) Fd() uintptr                            { return 0 }
func (f *mockFile) Readdir(int) ([]os.FileInfo, error)     { return nil, nil }
func (f *mockFile) Readdirnames(int) ([]string, error)     { return nil, nil }
func (f *mockFile) ReadDir(int) ([]os.DirEntry, error)     { return nil, nil }

func rootURI() workspaceapi.URI {
	u, err := workspaceapi.ParseURI("file:///project")
	if err != nil {
		panic(err)
	}
	return u
}
