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
	"os"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
)

// Row IDs are internal identifiers used to track node identity across
// user edits. They are stored in a parallel slice (rowIDs) indexed by
// buffer row, NOT embedded in the buffer itself. This keeps the shared
// cell.Buffer free of invisible cells that would confuse the text
// editor renderer.
//
// The values are drawn from the Unicode Supplementary Private Use
// Area-A (U+F0000..U+FFFFF), which gives us a generous 65k-wide pool
// before wrap-around. The specific range doesn't matter anymore
// (they're no longer rendered), but we keep the constants for
// readability and to make wrap-around deterministic.
const (
	firstRowID rune = 0xF0000
	lastRowID  rune = 0xFFFFF
)

// Config defines options for the file explorer component.
type Config struct {
	// Icons selects glyphs for directories, the default file
	// fallback, and per-extension overrides. The same IconSet
	// the editor renders in tabs is reused so the explorer's
	// glyphs match the rest of the IDE.
	Icons text.IconSet
	// IndentRune is drawn once at the start of each depth level.
	// Defaults to '│' (U+2502) when zero.
	IndentRune rune
	// IndentWidth is the total number of cells each depth level
	// occupies, including the leading IndentRune. Zero defaults to
	// 2 (IndentRune + single space), which matches terminals where
	// a tab expands to 2 spaces. Set this to the editor's Tabspaces
	// so `>>` / `<<` cursor motions and depth boundaries align.
	IndentWidth int
	// IndentAttr is applied to the leading IndentRune of every depth
	// level. The default (zero value) renders the indent guides in
	// term.ColorGray so they recede visually behind file names.
	IndentAttr term.Attributes
	// IconAttr is applied to the per-row icon glyph (directory,
	// default file, or per-extension override) drawn after the indent
	// guides. The default (zero value) renders the icons in
	// term.ColorGray so they recede visually behind file names,
	// matching the indent guides.
	IconAttr term.Attributes
	// Ignore filters out entries whose URI matches the matcher.
	// Used to hide e.g. `.git/`, `*.swp`, and anything listed in
	// the workspace's `.gitignore` — the same noise the rest of
	// the IDE (workspace event dispatcher, fuzzy finder,
	// idetask watcher) hides via vctrl.Matcher.
	//
	// A nil value means "no filtering" and is equivalent to
	// vctrl.NopMatcher(false); New normalizes nil to that
	// matcher so readChildren can call it unconditionally.
	Ignore vctrl.Matcher
}

// Component renders a file tree into a shared *cell.Buffer. It
// implements component.Floating and supports expand/collapse plus
// change detection over user edits performed directly on the shared
// buffer (e.g. via a text editor opened on the same buffer).
//
// Architecture:
//   - The shared *cell.Buffer IS the source of truth for the rendered
//     tree. The Component writes to it only on construction and when
//     expand/collapse/flush mutate the view.
//   - baseTree tracks the last known filesystem state plus per-dir
//     expanded flags. Children are kept on baseTree even when a
//     directory is collapsed in the view.
//   - rowIDs is a []rune whose length tracks buf.Rows(); rowIDs[y] is
//     the identifier of the tree node rendered at buffer row y, or 0
//     for blank rows or fresh rows a user has typed in.
//
// The Component subscribes to the buffer so it can keep rowIDs
// aligned when the user inserts or deletes rows via the editor.
type Component struct {
	fs       workspaceapi.FileSystem
	buf      *cell.Buffer
	root     workspaceapi.URI
	cfg      Config
	width    int
	height   int
	baseTree *node
	rowIDs   []rune
	nextID   rune

	// internal is set to true while the Component itself writes to the
	// buffer, so OnDidEdit does not try to re-splice rowIDs: the
	// Component assigns the new rowIDs explicitly.
	internal bool

	// orphans holds rowIDs removed by the most recent user edit
	// (together with the rendered text that was on that row at the
	// time of removal).
	orphans []orphanRow

	// dirty is true when the buffer holds user edits that have not
	// yet been flushed (or discarded by a refresh).
	dirty bool
}

type orphanRow struct {
	id      rune
	content string
}

var _ component.Floating = (*Component)(nil)
var _ cell.Subscriber = (*Component)(nil)

// New creates a new Component backed by the shared buf. On return,
// buf contains the rendering of the tree read from fs. The caller
// keeps ownership of buf; passing the same *cell.Buffer to a
// text.Editor via Editor.Edit lets the user mutate the tree directly
// in the editor while the Component observes edits to maintain node
// identity.
func New(
	buf *cell.Buffer,
	fs workspaceapi.FileSystem,
	root workspaceapi.URI,
	cfg Config,
) (*Component, error) {
	if buf == nil {
		return nil, fmt.Errorf("fileexplorer: buffer is required")
	}
	c := &Component{
		fs:   fs,
		buf:  buf,
		root: root,
		cfg:  normalizeConfig(cfg),
	}
	if err := c.init(); err != nil {
		return nil, err
	}
	buf.Subscribe(c)
	return c, nil
}

func (c *Component) init() error {
	c.nextID = firstRowID
	c.rowIDs = nil
	c.orphans = nil
	c.dirty = false
	c.baseTree = &node{
		uri:      c.root,
		isDir:    true,
		expanded: true,
		depth:    -1,
	}
	if err := readChildren(c.fs, c.cfg.Ignore, c.baseTree); err != nil {
		return err
	}
	c.assignIDs(c.baseTree)
	c.rewriteBufferFromTree(c.baseTree)
	// Pin undo at the initial render so the user can never `u`
	// their way back to an empty buffer: a Flush at that point
	// would interpret the blank view as "delete every file".
	c.buf.ResetVersion()
	return nil
}

func normalizeConfig(cfg Config) Config {
	if cfg.Icons.Extensions == nil {
		cfg.Icons.Extensions = map[string]rune{}
	}
	if cfg.Icons.Directory == 0 {
		cfg.Icons.Directory = ''
	}
	if cfg.Icons.OpenDirectory == 0 {
		cfg.Icons.OpenDirectory = ''
	}
	if cfg.Icons.Default == 0 {
		cfg.Icons.Default = ''
	}
	if cfg.IndentRune == 0 {
		cfg.IndentRune = '│'
	}
	if cfg.IndentWidth <= 1 {
		cfg.IndentWidth = 4
	}
	if cfg.IndentAttr == (term.Attributes{}) {
		cfg.IndentAttr = term.Attributes{Fg: term.ColorGray}
	}
	if cfg.IconAttr == (term.Attributes{}) {
		cfg.IconAttr = term.Attributes{Fg: term.ColorGray}
	}
	if cfg.Ignore == nil {
		cfg.Ignore = vctrl.NopMatcher(false)
	}
	return cfg
}

// Root returns the workspace URI the explorer is rooted at. This is
// the URI passed to New; callers can use it to compute paths
// relative to the workspace root for display purposes.
func (c *Component) Root() workspaceapi.URI { return c.root }

// Resize stores clip dimensions for Draw.
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
}

// Draw renders the shared buffer clipped to width/height.
func (c *Component) Draw(w term.Writer) {
	for y, row := range c.buf.RawCells() {
		if y >= c.height {
			break
		}
		var offset int
		for x, cel := range row {
			xi := x + offset
			if cel.Width > 1 {
				offset += int(cel.Width) - 1
			}
			if xi >= c.width {
				break
			}
			w.SetCell(term.Coordinates{X: xi, Y: y}, cel)
		}
	}
}

// Dimensions returns the optimal (width, height) needed to render
// the shared buffer without clipping.
func (c *Component) Dimensions() (int, int) {
	maxWidth := 0
	cells := c.buf.RawCells()
	for _, row := range cells {
		w := 0
		for _, cel := range row {
			if cel.Ch == '\t' {
				w++
				continue
			}
			if cel.Width > 1 {
				w += int(cel.Width)
				continue
			}
			w++
		}
		if w > maxWidth {
			maxWidth = w
		}
	}
	return maxWidth, c.visibleRowCount()
}

// visibleRowCount returns the number of non-empty rows in the buffer.
// The buffer always keeps at least one row (possibly empty); when the
// tree is empty we report height 0 so callers sizing a window don't
// allocate a row for whitespace.
func (c *Component) visibleRowCount() int {
	cells := c.buf.RawCells()
	n := len(cells)
	for n > 0 && len(cells[n-1]) == 0 {
		n--
	}
	return n
}

// NodeAt returns the URI of the node at the given buffer row.
// Returns false if pos.Y is out of range or if the row does not
// correspond to a known node.
func (c *Component) NodeAt(
	pos term.Coordinates,
) (workspaceapi.URI, bool) {
	if pos.Y < 0 || pos.Y >= len(c.rowIDs) {
		return workspaceapi.URI{}, false
	}
	id := c.rowIDs[pos.Y]
	if id == 0 {
		return workspaceapi.URI{}, false
	}
	n := c.findNodeByID(c.baseTree, id)
	if n == nil {
		return workspaceapi.URI{}, false
	}
	return n.uri, true
}

// ExpandNodeAt toggles expand/collapse for the directory whose
// rowIDs[pos.Y] matches a known node. For files, it returns
// (uri, true) without mutating the buffer. For directories, it
// rewrites the buffer to reflect the new view and returns
// (URI{}, false).
//
// Before mutating, the current buffer is re-parsed and merged with
// baseTree, so any unflushed user edits (renames, creates, ...) are
// preserved across the toggle.
func (c *Component) ExpandNodeAt(
	pos term.Coordinates,
) (workspaceapi.URI, bool) {
	if pos.Y < 0 || pos.Y >= len(c.rowIDs) {
		return workspaceapi.URI{}, false
	}
	id := c.rowIDs[pos.Y]
	if id == 0 {
		return workspaceapi.URI{}, false
	}
	view := c.parseViewTree()
	target := c.findNodeByID(view, id)
	if target == nil {
		return workspaceapi.URI{}, false
	}
	if !target.isDir {
		// For files, resolve to the on-disk URI from the base
		// tree: the user may have renamed the row (but not yet
		// flushed), in which case the view URI points at a
		// not-yet-existing path. Opening should target the real
		// file so the editor has content to show.
		if base := c.findNodeByID(c.baseTree, id); base != nil {
			return base.uri, true
		}
		return target.uri, true
	}
	if target.expanded {
		target.expanded = false
		if baseNode := c.findNodeByID(c.baseTree, id); baseNode != nil {
			baseNode.expanded = false
		}
	} else {
		if target.children == nil {
			if err := readChildren(c.fs, c.cfg.Ignore, target); err != nil {
				return workspaceapi.URI{}, false
			}
			c.assignIDs(target)
			if baseNode := c.findNodeByID(c.baseTree, id); baseNode != nil {
				if baseNode.children == nil {
					if err := readChildren(c.fs, c.cfg.Ignore, baseNode); err == nil {
						c.copyIDsToBase(target, baseNode)
					}
				}
				baseNode.expanded = true
			}
		}
		target.expanded = true
	}
	c.rewriteBufferFromTree(view)
	return workspaceapi.URI{}, false
}

// ExpandLevel expands all directories at the same depth
// as the node under pos.
func (c *Component) ExpandLevel(pos term.Coordinates) {
	if pos.Y < 0 || pos.Y >= len(c.rowIDs) {
		return
	}
	id := c.rowIDs[pos.Y]
	if id == 0 {
		return
	}
	view := c.parseViewTree()
	n := c.findNodeByID(view, id)
	if n == nil {
		return
	}
	targetDepth := n.depth
	c.expandAtDepth(view, targetDepth)
	c.syncExpandedToBase(view)
	c.rewriteBufferFromTree(view)
}

// CollapseLevel collapses all directories at the same depth
// as the node under pos.
func (c *Component) CollapseLevel(pos term.Coordinates) {
	if pos.Y < 0 || pos.Y >= len(c.rowIDs) {
		return
	}
	id := c.rowIDs[pos.Y]
	if id == 0 {
		return
	}
	view := c.parseViewTree()
	n := c.findNodeByID(view, id)
	if n == nil {
		return
	}
	targetDepth := n.depth
	collapseAtDepth(view, targetDepth)
	collapseAtDepth(c.baseTree, targetDepth)
	c.rewriteBufferFromTree(view)
}

// CollapseAll collapses every directory in the tree.
func (c *Component) CollapseAll() {
	view := c.parseViewTree()
	collapseAll(view)
	collapseAll(c.baseTree)
	c.rewriteBufferFromTree(view)
}

// DryFlush reparses the buffer into a view tree and computes the
// ChangeSet needed to bring the filesystem from the base tree to the
// view tree. It does not mutate the filesystem or the buffer.
func (c *Component) DryFlush() *ChangeSet {
	view := c.parseViewTree()
	cs := computeChangeSet(c.baseTree, view)
	c.appendNameConflicts(cs, view)
	return cs
}

// appendNameConflicts records a conflict for every view entry whose
// name the explorer refuses to write. Reported as conflicts (rather
// than dropped operations) so the whole batch is refused and the user
// sees why, instead of silently losing part of their edit.
func (c *Component) appendNameConflicts(cs *ChangeSet, view *node) {
	for _, e := range collectEntries(view) {
		err := validateEntryName(e.Name, c.cfg)
		if err == nil {
			continue
		}
		cs.Conflicts = append(cs.Conflicts, Conflict{
			Path:    e.URI.Path(),
			Entry1:  e.ID,
			Message: err.Error(),
		})
	}
}

// HasPendingEdits returns true when the user has edited the buffer
// since the last Flush/Refresh.
func (c *Component) HasPendingEdits() bool {
	return c.dirty
}

// Refresh discards any unflushed buffer edits and rebuilds the
// rendered tree from disk.
func (c *Component) Refresh() error {
	return c.init()
}

// ExpandedDirectories returns the URIs of directory nodes currently
// expanded in the rendered tree.
func (c *Component) ExpandedDirectories() []workspaceapi.URI {
	var uris []workspaceapi.URI
	for _, id := range c.rowIDs {
		if id == 0 {
			continue
		}
		n := c.findNodeByID(c.baseTree, id)
		if n != nil && n.isDir && n.expanded {
			uris = append(uris, n.uri)
		}
	}
	return uris
}

// ExpandDirectories expands any directory in uris that still exists in
// the current tree.
func (c *Component) ExpandDirectories(uris []workspaceapi.URI) {
	if len(uris) == 0 {
		return
	}
	wanted := make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		wanted[uri.String()] = struct{}{}
	}
	var changed bool
	var walk func(*node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.isDir {
			if _, ok := wanted[n.uri.String()]; ok {
				if n.children == nil {
					if err := readChildren(c.fs, c.cfg.Ignore, n); err != nil {
						return
					}
					c.assignIDs(n)
				}
				if !n.expanded {
					n.expanded = true
					changed = true
				}
			}
		}
		if n.expanded {
			for _, child := range n.children {
				walk(child)
			}
		}
	}
	walk(c.baseTree)
	if changed {
		c.rewriteBufferFromTree(c.baseTree)
	}
}

// Flush runs the same logic as DryFlush and, if there are no
// conflicts, executes the ordered operations on the filesystem. On
// success, the base tree is updated to match the applied changes and
// the buffer is rewritten to the canonical rendering of the new tree.
// On conflict (or on operation error), the buffer is not mutated and
// the partial ChangeSet plus a non-nil error is returned.
func (c *Component) Flush() (*ChangeSet, error) {
	view := c.parseViewTree()
	cs := computeChangeSet(c.baseTree, view)
	c.appendNameConflicts(cs, view)
	if cs.HasConflicts() {
		return cs, fmt.Errorf(
			"file explorer: %d conflicting change(s)",
			len(cs.Conflicts),
		)
	}
	ordered := OrderOperations(cs.Operations)
	for _, op := range ordered {
		if err := c.applyOperation(op); err != nil {
			return cs, err
		}
	}
	c.assignIDs(view)
	c.baseTree = view.deepCopy()
	c.rewriteBufferFromTree(c.baseTree)
	c.dirty = false
	return cs, nil
}

// findNodeByID searches the tree for a node with the given ID.
func (c *Component) findNodeByID(root *node, id rune) *node {
	if root == nil {
		return nil
	}
	if root.id == id {
		return root
	}
	for _, child := range root.children {
		if found := c.findNodeByID(child, id); found != nil {
			return found
		}
	}
	return nil
}

// assignIDs assigns unique IDs to all nodes that don't have one.
func (c *Component) assignIDs(root *node) {
	var walk func(n *node)
	walk = func(n *node) {
		if n.id == 0 && n != root {
			n.id = c.allocateID()
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	walk(root)
}

// allocateID returns the next available row ID, wrapping around the
// Private Use Area window and skipping IDs already present in the
// base tree to avoid collisions after wrap-around.
func (c *Component) allocateID() rune {
	for range (lastRowID - firstRowID) + 1 {
		id := c.nextID
		c.nextID++
		if c.nextID > lastRowID {
			c.nextID = firstRowID
		}
		if c.findNodeByID(c.baseTree, id) == nil {
			return id
		}
	}
	// Pool exhausted (extremely unlikely); fall back to the raw
	// counter value.
	id := c.nextID
	c.nextID++
	if c.nextID > lastRowID {
		c.nextID = firstRowID
	}
	return id
}

// copyIDsToBase copies IDs from view node children to base node children
// by matching names.
func (c *Component) copyIDsToBase(viewNode, baseNode *node) {
	for _, vc := range viewNode.children {
		for _, bc := range baseNode.children {
			if vc.name == bc.name && vc.isDir == bc.isDir {
				bc.id = vc.id
				break
			}
		}
	}
}

// syncExpandedToBase walks view and syncs expanded directories to
// baseTree, ensuring both trees have matching children with IDs.
func (c *Component) syncExpandedToBase(view *node) {
	var walk func(viewNode *node)
	walk = func(viewNode *node) {
		for _, vc := range viewNode.children {
			if !vc.isDir {
				continue
			}
			baseNode := c.findNodeByID(c.baseTree, vc.id)
			if baseNode == nil {
				continue
			}
			if vc.expanded {
				if baseNode.children == nil && vc.children != nil {
					_ = readChildren(c.fs, c.cfg.Ignore, baseNode)
					c.copyIDsToBase(vc, baseNode)
				}
				baseNode.expanded = true
			} else {
				baseNode.expanded = false
			}
			if vc.expanded {
				walk(vc)
			}
		}
	}
	walk(view)
}

// expandAtDepth expands all directories at the given depth.
func (c *Component) expandAtDepth(n *node, depth int) {
	for _, child := range n.children {
		if child.isDir && child.depth == depth {
			if child.children == nil {
				_ = readChildren(c.fs, c.cfg.Ignore, child)
				c.assignIDs(child)
			}
			child.expanded = true
		}
		if child.isDir && child.expanded {
			c.expandAtDepth(child, depth)
		}
	}
}

// applyOperation executes a single operation on the filesystem.
func (c *Component) applyOperation(op Operation) error {
	switch op.Type {
	case OpCreate:
		f, err := c.fs.OpenFile(
			op.NewURI.Path(),
			os.O_CREATE|os.O_WRONLY,
			0644,
		)
		if err != nil {
			return err
		}
		return f.Close()

	case OpMkdir:
		return c.fs.MkdirAll(op.NewURI.Path(), 0755)

	case OpDelete:
		return c.fs.Remove(op.URI.Path())

	case OpRename, OpMove:
		// Copy content to new location, delete old
		info, err := c.fs.Stat(op.URI.Path())
		if err != nil {
			return err
		}
		if info.IsDir() {
			// For directories, just create at new location
			// (children will be handled by subsequent operations)
			return c.fs.MkdirAll(op.NewURI.Path(), 0755)
		}
		if err := c.copyFile(op.URI.Path(), op.NewURI.Path()); err != nil {
			return err
		}
		return c.fs.Remove(op.URI.Path())

	case OpCopy:
		info, err := c.fs.Stat(op.URI.Path())
		if err != nil {
			return err
		}
		if info.IsDir() {
			return c.fs.MkdirAll(op.NewURI.Path(), 0755)
		}
		return c.copyFile(op.URI.Path(), op.NewURI.Path())
	}
	return nil
}

func (c *Component) copyFile(srcPath, dstPath string) (err error) {
	src, err := c.fs.OpenFile(srcPath, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := src.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	dst, err := c.fs.OpenFile(
		dstPath,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0644,
	)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			break
		}
	}
	return nil
}

// parseViewTree re-reads the shared buffer and the parallel rowIDs
// slice to build a view tree that reflects the user's intended state.
// Any directory whose ID matches a dir in baseTree AND that appears in
// the view without children (because it is collapsed or because its
// children are unknown) has its base-tree children grafted back in,
// so descendants hidden from the user do not show up as deletions.
func (c *Component) parseViewTree() *node {
	rows := c.buf.RawCells()
	entries := make([]parsedEntry, 0, len(rows))
	indent := c.cfg.IndentRune
	width := c.cfg.IndentWidth
	for y, row := range rows {
		e, ok := parseCellRow(row, indent, width)
		if !ok {
			continue
		}
		if y < len(c.rowIDs) {
			e.id = c.rowIDs[y]
		}
		entries = append(entries, e)
	}

	root := &node{
		uri:      c.root,
		isDir:    true,
		expanded: true,
		depth:    -1,
	}
	if len(entries) == 0 {
		return root
	}

	stack := []*node{root}
	for _, e := range entries {
		// Clamp depth: a row deeper than the nearest open parent's
		// depth+1 is treated as a direct child of that parent.
		parent := stack[len(stack)-1]
		for len(stack) > 1 && parent.depth >= e.depth {
			stack = stack[:len(stack)-1]
			parent = stack[len(stack)-1]
		}
		if e.depth > parent.depth+1 {
			e.depth = parent.depth + 1
		}

		child := &node{
			id:     e.id,
			name:   e.name,
			uri:    workspaceapi.Join(parent.uri, e.name),
			isDir:  e.isDir,
			depth:  e.depth,
			parent: parent,
		}
		parent.children = append(parent.children, child)
		if e.isDir {
			stack = append(stack, child)
		}
	}

	// A directory in the view is "expanded" iff at least one child
	// row was parsed under it.
	var markExpanded func(n *node)
	markExpanded = func(n *node) {
		for _, c := range n.children {
			if c.isDir {
				if len(c.children) > 0 {
					c.expanded = true
				}
				markExpanded(c)
			}
		}
	}
	markExpanded(root)

	c.releaseMismatchedIDs(root)
	c.recoverLostIDsByName(root)
	c.graftCollapsedChildren(root)
	return root
}

// releaseMismatchedIDs detects a specific reshuffling case where an
// edit shuffled rows such that a row's positional id was assigned to
// a different row's name. Example: swapping rows "nested/" and
// "target/" via a single multi-row replace leaves "target/" with
// id_nested and "nested/" with id=0. Without intervention,
// computeChangeSet would report a nonsensical rename of target ->
// nested. By releasing the mismatched positional id when another
// zero-id row has a name matching the claimed base node, we free it
// up for recoverLostIDsByName to reassign correctly.
//
// Guardrails to avoid regressing in-place rename semantics:
//   - We only release the id if the row's CURRENT name also exists
//     in the baseTree (i.e. this really looks like a shuffle, not a
//     rename). A row renamed to a brand-new name gets to keep its
//     positional id.
//   - We only release when some OTHER zero-id row has the same name
//     as base[current.id]. Otherwise we'd lose information.
func (c *Component) releaseMismatchedIDs(view *node) {
	// Build a map of base nodes by id and by name.
	baseByID := make(map[rune]*node)
	baseNames := make(map[string]int) // nameKey -> count in base
	var walkBase func(n *node)
	walkBase = func(n *node) {
		for _, child := range n.children {
			if child.id != 0 {
				baseByID[child.id] = child
			}
			baseNames[nameKey(child.name, child.isDir)]++
			walkBase(child)
		}
	}
	walkBase(c.baseTree)

	// Collect view entries in order.
	type viewRef struct {
		node *node
	}
	var viewList []*viewRef
	var collectView func(n *node)
	collectView = func(n *node) {
		for _, child := range n.children {
			viewList = append(viewList, &viewRef{node: child})
			collectView(child)
		}
	}
	collectView(view)

	// Count zero-id rows by name and count view rows claiming each id.
	zeroByName := make(map[string]int)
	idClaims := make(map[rune]int)
	for _, v := range viewList {
		if v.node.id == 0 {
			zeroByName[nameKey(v.node.name, v.node.isDir)]++
		} else {
			idClaims[v.node.id]++
		}
	}

	for _, v := range viewList {
		vn := v.node
		if vn.id == 0 {
			continue
		}
		base := baseByID[vn.id]
		if base == nil {
			continue
		}
		// Positional claim is stable if names match.
		if base.name == vn.name && base.isDir == vn.isDir {
			continue
		}
		// Only release if the mismatched row's current name also
		// exists somewhere in the base tree (otherwise this is a
		// rename-to-new-name, which should keep the id).
		if baseNames[nameKey(vn.name, vn.isDir)] == 0 {
			continue
		}
		// Only release if some other zero-id row wants the base
		// name (i.e. this is a reshuffle, not a unique rename).
		if zeroByName[nameKey(base.name, base.isDir)] == 0 {
			continue
		}
		// Release the id; recoverLostIDsByName will reassign it.
		idClaims[vn.id]--
		vn.id = 0
		zeroByName[nameKey(vn.name, vn.isDir)]++
	}
}

// recoverLostIDsByName is a best-effort fallback for edits that
// destroy row ids positionally (e.g. replacing N rows in a single
// multi-row Edit call). For each view node with id == 0 whose name +
// isDir matches a baseTree node whose id is NOT already present in
// the view, reassign the base id. This lets DryFlush recognize moves
// where the user cut a group of rows and re-pasted them somewhere
// else in the same edit.
func (c *Component) recoverLostIDsByName(view *node) {
	// Collect IDs currently claimed by the view.
	claimed := make(map[rune]bool)
	var walkClaim func(n *node)
	walkClaim = func(n *node) {
		for _, child := range n.children {
			if child.id != 0 {
				claimed[child.id] = true
			}
			walkClaim(child)
		}
	}
	walkClaim(view)

	// Build a lookup of baseTree nodes keyed by the last path
	// segment and isDir. When two base nodes share the same (name,
	// isDir) pair we keep a list to disambiguate by depth/URI later.
	type baseEntry struct {
		node *node
		used bool
	}
	byName := make(map[string][]*baseEntry)
	var walkBase func(n *node)
	walkBase = func(n *node) {
		for _, child := range n.children {
			if !claimed[child.id] {
				key := nameKey(child.name, child.isDir)
				byName[key] = append(byName[key], &baseEntry{node: child})
			}
			walkBase(child)
		}
	}
	walkBase(c.baseTree)

	// Pre-compute each view node's intended URI path so we can
	// prefer exact URI matches over bare-name collisions.
	var walkAssign func(n *node)
	walkAssign = func(n *node) {
		for _, child := range n.children {
			if child.id == 0 {
				candidates := byName[nameKey(child.name, child.isDir)]
				var pick *baseEntry
				// Prefer a candidate whose URI matches the view
				// node's URI exactly; otherwise take the first
				// unused candidate.
				for _, cand := range candidates {
					if cand.used {
						continue
					}
					if cand.node.uri.String() == child.uri.String() {
						pick = cand
						break
					}
				}
				if pick == nil {
					for _, cand := range candidates {
						if !cand.used {
							pick = cand
							break
						}
					}
				}
				if pick != nil {
					pick.used = true
					child.id = pick.node.id
					claimed[child.id] = true
				}
			}
			walkAssign(child)
		}
	}
	walkAssign(view)
}

func nameKey(name string, isDir bool) string {
	if isDir {
		return "d:" + name
	}
	return "f:" + name
}

// graftCollapsedChildren walks view and, for every directory whose ID
// matches a directory in baseTree and that does NOT have any children
// in the view (i.e. the user did not expand it), copies the base
// children over with their base URIs. This way, descendants of a
// collapsed directory are treated as unchanged (no-op) by the change
// detector.
func (c *Component) graftCollapsedChildren(view *node) {
	// First collect IDs already claimed elsewhere in the view. Any
	// such id MUST NOT be re-grafted (doing so would produce a path
	// conflict and double the operation for the same file).
	claimed := make(map[rune]bool)
	var walkClaim func(n *node)
	walkClaim = func(n *node) {
		for _, child := range n.children {
			if child.id != 0 {
				claimed[child.id] = true
			}
			walkClaim(child)
		}
	}
	walkClaim(view)

	var walk func(viewNode *node)
	walk = func(viewNode *node) {
		for _, vc := range viewNode.children {
			if !vc.isDir {
				continue
			}
			if len(vc.children) > 0 {
				walk(vc)
				continue
			}
			baseNode := c.findNodeByID(c.baseTree, vc.id)
			if baseNode == nil {
				continue
			}
			// Only graft hidden descendants when the directory
			// is considered COLLAPSED in the base tree. If the
			// base directory is expanded (user has seen its
			// children) and the view shows no children, the
			// user has intentionally deleted them.
			if baseNode.expanded {
				continue
			}
			graftBaseChildrenFiltered(vc, baseNode, claimed)
		}
	}
	walk(view)
}

// graftBaseChildrenFiltered copies identity from base children onto
// dst so that computeChangeSet matches them by id and emits no ops.
// Children URIs are rebased under dst.uri so hidden descendants
// follow along if the user moved/renamed the collapsed directory.
// Any base child whose id is in `claimed` (already used elsewhere in
// the view) is skipped to avoid path conflicts.
func graftBaseChildrenFiltered(dst, base *node, claimed map[rune]bool) {
	if len(base.children) == 0 {
		return
	}
	dst.children = make([]*node, 0, len(base.children))
	for _, bc := range base.children {
		if bc.id != 0 && claimed[bc.id] {
			continue
		}
		nc := &node{
			id:     bc.id,
			name:   bc.name,
			uri:    workspaceapi.Join(dst.uri, bc.name),
			isDir:  bc.isDir,
			depth:  dst.depth + 1,
			parent: dst,
			// Mark grafted directories as expanded so
			// collectEntries walks into their (hidden) children
			// when comparing against the base tree.
			expanded: bc.isDir,
		}
		graftBaseChildrenFiltered(nc, bc, claimed)
		dst.children = append(dst.children, nc)
	}
}

// rewriteBufferFromTree renders tree into the shared buffer and
// rebuilds rowIDs. This is a programmatic refresh, not a user edit, so
// it is dispatched via ReloadContents, which bypasses usage subscribers
// — the editor's copy-on-delete (text.WithCopyDelete) must not copy the
// replaced tree text into the clipboard. OUR own OnDidEdit (a root
// Subscriber, which ReloadContents still notifies) is short-circuited
// via c.internal.
func (c *Component) rewriteBufferFromTree(tree *node) {
	flat := flatten(tree)
	rowIDs := make([]rune, 0, len(flat))
	var b strings.Builder
	for i, n := range flat {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderLine(n, c.cfg))
		rowIDs = append(rowIDs, n.id)
	}
	c.internal = true
	c.buf.ReloadContents(context.Background(), b.String())
	c.applyIndentAttr(flat)
	c.applyIconAttr(flat)
	c.internal = false
	c.rowIDs = rowIDs
}

// applyIndentAttr decorates the leading IndentRune of every depth
// level on each rendered row with the configured indent attributes.
// The cells are addressed by depth/width arithmetic (instead of
// scanning for the indent rune) so user-typed runes that happen to
// equal IndentRune are left untouched.
func (c *Component) applyIndentAttr(flat []*node) {
	attr := c.cfg.IndentAttr
	width := c.cfg.IndentWidth
	cells := c.buf.RawCells()
	for y, n := range flat {
		if y >= len(cells) {
			break
		}
		row := cells[y]
		for d := range n.depth {
			x := d * width
			if x >= len(row) {
				break
			}
			row[x].Fg = attr.Fg
			row[x].Bg = attr.Bg
			row[x].Attrs = attr.Attrs
		}
	}
}

// applyIconAttr decorates the per-row icon glyph with the configured
// icon attributes. The icon cell sits immediately after the indent
// guides at column n.depth * IndentWidth, mirroring renderLine.
func (c *Component) applyIconAttr(flat []*node) {
	attr := c.cfg.IconAttr
	width := c.cfg.IndentWidth
	cells := c.buf.RawCells()
	for y, n := range flat {
		if y >= len(cells) {
			break
		}
		row := cells[y]
		x := n.depth * width
		if x >= len(row) {
			continue
		}
		row[x].Fg = attr.Fg
		row[x].Bg = attr.Bg
		row[x].Attrs = attr.Attrs
	}
}

// OnWillEdit is part of cell.Subscriber.
func (c *Component) OnWillEdit(
	_ context.Context,
	_, _ term.Coordinates,
	_ string,
) {
}

// OnDidEdit keeps rowIDs aligned to the buffer when the user inserts
// or removes rows via the shared editor.
//
// The edit replaces the range [from, to) in the buffer with str,
// removing the string `old`. We splice c.rowIDs in the same way the
// buffer spliced its rows:
//
//   - oldLines = number of '\n' in old: this tells us how many row
//     boundaries were crossed by the deletion.
//   - newLines = to.Y - from.Y: how many row boundaries are in str.
//
// The subtle case is whether the edit "owns" the whole row at from.Y
// or splits it. When the edit starts at column 0 AND the inserted
// content ends with a newline, we are inserting complete rows BEFORE
// row from.Y (which gets pushed down). Otherwise the edit splits /
// mutates row from.Y in place, and new rows (if any) go AFTER it.
// The symmetric rule applies to deletion: a column-0 deletion whose
// old content ends with '\n' deletes whole rows starting at from.Y;
// any other deletion merges rows (from.Y, from.Y+oldLines] into
// from.Y, which keeps its id.
func (c *Component) OnDidEdit(
	_ context.Context,
	from, to term.Coordinates,
	old string,
) {
	if c.internal {
		return
	}
	// Any external buffer edit makes the in-memory rendering
	// diverge from the canonical tree. The flag is cleared on
	// Flush (operations applied) or Refresh (edits discarded);
	// HasPendingEdits is implemented by reading it directly so
	// FS-event hot paths don't re-parse the buffer.
	c.dirty = true
	oldLines := strings.Count(old, "\n")
	newLines := to.Y - from.Y

	// Classify the edit: does it "own" full rows at from.Y?
	newEndsWithNewline := newLines > 0 &&
		to.X == 0 // the inserted content ended exactly on a row boundary
	deleteOwnsFullRows := oldLines > 0 && from.X == 0 &&
		strings.HasSuffix(old, "\n")
	insertOwnsFullRows := newLines > 0 && from.X == 0 && newEndsWithNewline

	var delStart, delEnd, insCount int
	switch {
	case deleteOwnsFullRows && insertOwnsFullRows:
		// Whole-row replace: remove oldLines rows, insert newLines
		// fresh rows, all starting at from.Y.
		delStart = from.Y
		delEnd = from.Y + oldLines
		insCount = newLines
	case deleteOwnsFullRows:
		// Whole-row delete + (maybe) partial-line insert on what
		// becomes row from.Y. Fresh rows are inserted at from.Y.
		delStart = from.Y
		delEnd = from.Y + oldLines
		insCount = newLines
	case insertOwnsFullRows:
		// Pure insertion of whole rows before the existing row.
		// No ids removed; newLines zero-ids inserted at from.Y.
		delStart = from.Y
		delEnd = from.Y
		insCount = newLines
	default:
		// Edit splits or joins rows. The row at from.Y keeps its
		// id; rows (from.Y, from.Y+oldLines] are removed; newLines
		// zero-ids are inserted right after from.Y.
		delStart = from.Y + 1
		delEnd = from.Y + 1 + oldLines
		insCount = newLines
	}

	delStart = min(delStart, len(c.rowIDs))
	delEnd = min(delEnd, len(c.rowIDs))

	// Cache the (id, content) of rows we're about to remove so a
	// subsequent insertion can reclaim the id when content matches
	// (vi's `dd` + `p` flow).
	c.captureOrphans(delStart, delEnd, from, old)

	// Seed inserted rows with orphaned ids whose content matches
	// the newly-inserted row. Fallthrough stays 0.
	inserts := c.seedInserts(insCount, delStart)
	tail := append(inserts, c.rowIDs[delEnd:]...)
	c.rowIDs = append(c.rowIDs[:delStart], tail...)

	// Reconcile with the authoritative buffer row count.
	if n := c.buf.Rows(); n > len(c.rowIDs) {
		c.rowIDs = append(c.rowIDs, make([]rune, n-len(c.rowIDs))...)
	} else if n < len(c.rowIDs) {
		c.rowIDs = c.rowIDs[:n]
	}
}

// captureOrphans records the (id, rendered-content) pair for every
// rowID at indices [delStart, delEnd) in c.rowIDs BEFORE the splice
// drops them. A subsequent OnDidEdit call can then reclaim an id
// when the newly-inserted text matches a captured row (vi's
// `dd` + `p` flow). Two edit shapes deliver full-row removals:
//
//  1. Column-0 delete that ends with "\n": `old` is literally the
//     removed rows joined by "\n" plus a trailing "\n".
//  2. End-of-line + newline delete (what DeleteRow produces when
//     y>0): `old` is "\n<row>\n<row>…<row-partial>" because the
//     edit starts at the tail of row y-1 and ends inside row
//     y+oldLines.
func (c *Component) captureOrphans(
	delStart, delEnd int,
	from term.Coordinates, old string,
) {
	n := delEnd - delStart
	if n <= 0 || len(old) == 0 {
		return
	}
	var removed []string
	switch {
	case from.X == 0 && strings.HasSuffix(old, "\n"):
		// Shape 1: full rows from from.Y.
		removed = strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	case strings.HasPrefix(old, "\n") && delStart == from.Y+1:
		// Shape 2: rows at from.Y+1..from.Y+1+N; `old` is
		// "\n<row0>\n<row1>...". The trailing row may end
		// mid-line if to.X > 0, but we only care about the
		// full rows.
		parts := strings.Split(old[1:], "\n")
		if len(parts) > n {
			parts = parts[:n]
		}
		removed = parts
	default:
		return
	}
	count := min(n, len(removed))
	for i := 0; i < count; i++ {
		id := c.rowIDs[delStart+i]
		if id == 0 {
			continue
		}
		c.orphans = append(c.orphans, orphanRow{
			id:      id,
			content: strings.TrimRight(removed[i], " \t"),
		})
	}
}

// seedInserts returns a slice of length insCount where each slot
// is either 0 (fresh id) or an id reclaimed from the orphan pool
// whose previous content matches the buffer row at the insertion
// slot. This is what stitches identity across `dd` + `p`.
//
// The insertion rows occupy [delStart, delStart+insCount) in the
// PRE-SPLICE rowIDs slice — which, because the buffer was already
// updated before OnDidEdit runs, corresponds to the SAME row
// indices in the live buffer.
func (c *Component) seedInserts(insCount, delStart int) []rune {
	ids := make([]rune, insCount)
	if insCount == 0 || len(c.orphans) == 0 {
		return ids
	}
	for i := 0; i < insCount; i++ {
		y := delStart + i
		if y < 0 || y >= c.buf.Rows() {
			continue
		}
		line := strings.TrimRight(rowString(c.buf, y), " \t")
		if line == "" {
			continue
		}
		for j, o := range c.orphans {
			if o.content == line {
				ids[i] = o.id
				// slices.Delete clears the tail so the removed orphanRow's
				// content string is not retained in the backing array.
				c.orphans = slices.Delete(c.orphans, j, j+1)
				break
			}
		}
	}
	return ids
}

func rowString(buf *cell.Buffer, y int) string {
	rows := buf.RawCells()
	if y < 0 || y >= len(rows) {
		return ""
	}
	return cellsToRowString(rows[y])
}

func collapseAtDepth(n *node, depth int) {
	for _, child := range n.children {
		if child.isDir && child.depth == depth {
			child.expanded = false
		}
		if child.isDir && child.expanded {
			collapseAtDepth(child, depth)
		}
	}
}

func collapseAll(n *node) {
	for _, child := range n.children {
		if child.isDir {
			child.expanded = false
			collapseAll(child)
		}
	}
}
