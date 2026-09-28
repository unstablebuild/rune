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
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	fileexplorercomp "unstable.build/rune/internal/component/fileexplorer"
	"unstable.build/rune/internal/text"
)

var _ browser.ScrollableFloating = (*fileExplorerHandler)(nil)

// fileExplorerFSEvents lists the textapi event types the file
// explorer handler subscribes to. They are exactly the FS-watcher
// events the workspace dispatches when files appear/disappear/
// change under the workspace root, not the in-IDE editor lifecycle
// events (Open/Close/Edit/...).
var fileExplorerFSEvents = []textapi.EventType{
	textapi.EventTypeCreate,
	textapi.EventTypeChange,
	textapi.EventTypeRemove,
	textapi.EventTypeRename,
}

// fileExplorerSpanHPad is the horizontal padding applied around the
// file explorer's editor content so that there is breathing room on
// the left and right sides of the rendered tree.
const fileExplorerSpanHPad = 2

// fileExplorerHintHeight is the row reserved at the bottom of the
// explorer for the mode hint.
const fileExplorerHintHeight = 1

// fileExplorerConfig is the user-facing explorer configuration plus
// the bindings only the IDE can resolve.
type fileExplorerConfig struct {
	text.FileExplorerConfig
	// SaveKey is the key label bound to `write`, resolved from the
	// user's own bindings so the hint row names the key they have.
	SaveKey string
}

// lockedEditor gates the buffer's user-edit path. Every mutation the
// editor performs funnels through cell.Buffer's installed Editor, so
// refusing here means a locked explorer never mutates in the first
// place. The Component's own renders enter the buffer below this layer
// via ReloadContents and are unaffected.
type lockedEditor struct {
	inner  cell.Editor
	locked func() bool
}

func (e *lockedEditor) Edit(
	ctx context.Context, start, end term.Coordinates, str string,
) (from, to term.Coordinates, old string) {
	if e.locked() {
		return start, start, ""
	}
	return e.inner.Edit(ctx, start, end, str)
}

type fileExplorerHost interface {
	Prompt(message string, options []string, bindings []term.KeyComb, promptHandler handler.PromptHandler) browser.Window
	SetWindowWidth(win browser.Window, width int) bool
	SetFocus(win browser.Window) (browser.Window, error)
	Focus() (browser.Window, error)
	OpenFile(uri workspaceapi.URI, win browser.Window) error
	SetError(error)
	FrameEnabled() bool
}

type fileExplorerHandler struct {
	browser.ScrollableFloating
	host   fileExplorerHost
	comp   *fileexplorercomp.Component
	buf    *cell.Buffer
	ed     text.Handler
	span   *handler.Span
	uri    workspaceapi.URI
	width  int
	height int

	win    browser.Window
	target browser.Window

	cfg fileExplorerConfig

	// readOnly refuses every filesystem-mutating edit. Enforced here
	// rather than through Editor.Edit's readOnly argument, which only
	// drives the status-bar indicator and does not block buffer
	// mutation in any of the editor modes. It tracks the current
	// visit and is re-armed from cfg.ReadOnly when the explorer is
	// toggled shut, so leaving read-only is a deliberate, per-visit
	// act.
	readOnly bool

	// pendingRefresh is set when an FS event arrives while the
	// explorer is visible AND has unflushed user edits. The
	// refresh is deferred until either the user successfully
	// flushes (replayed from forceFlush after Flush) or the
	// explorer is closed via the toggle (replayed from
	// onWindowClosed); the latter discards the pending edits.
	pendingRefresh bool

	// mouseDown tracks whether the left button is currently held so
	// that drag events (MouseLeft repeated while held) are ignored;
	// only the initial press opens or toggles a node.
	mouseDown bool
}

func newFileExplorerHandler(
	host fileExplorerHost,
	comp *fileexplorercomp.Component,
	buf *cell.Buffer,
	ed text.Handler,
	uri workspaceapi.URI,
	target browser.Window,
	cfg fileExplorerConfig,
) (*fileExplorerHandler, error) {
	h := &fileExplorerHandler{
		host:     host,
		comp:     comp,
		buf:      buf,
		ed:       ed,
		uri:      uri,
		target:   target,
		cfg:      cfg,
		readOnly: cfg.ReadOnly,
	}
	h.span = handler.NewSpan(ed, component.SpanConfig{
		PadHorizontal:    fileExplorerSpanHPad,
		ContentAlignment: component.AlignmentCentered,
	})
	gate := &lockedEditor{locked: func() bool { return h.readOnly }}
	gate.inner = buf.WithEditor(gate)
	h.ScrollableFloating = browser.FuncScrollableFloatingHandler(h, func() error { return nil })
	return h, nil
}

func (h *fileExplorerHandler) SetWindow(win browser.Window) {
	h.win = win
}

func (h *fileExplorerHandler) SetTargetWindow(win browser.Window) {
	h.target = win
}

func (h *fileExplorerHandler) Handle(ev term.Event) (exit, handled bool) {
	if h.readOnly && ev.Type == term.EventKey &&
		ev.Key == h.cfg.EditKey.Key && ev.Ch == h.cfg.EditKey.Ch &&
		ev.Mod == h.cfg.EditKey.Mod {
		h.readOnly = false
		// The hint changes with the mode, and may vanish entirely, so
		// the tree's share of the window has to be recomputed.
		h.Resize(h.width, h.height)
		h.syncWidth()
		return false, true
	}
	if ev.Type == term.EventKey && ev.Mod == 0 && ev.Key == term.KeyEnter &&
		h.enterSelectsNode() {
		if handled = h.enterAtCursor(); handled {
			h.syncWidth()
			return false, true
		}
	}
	// A left click opens/closes the node under the pointer (or opens
	// the file) like <Enter>, instead of starting a text selection
	// like the underlying editor would. Mouse events are not forwarded
	// to the editor at all, so it never enters visual mode. Only the
	// initial press acts; subsequent MouseLeft events while the button
	// is held are drags and must be ignored so dragging across rows
	// does not open every node it passes over.
	if ev.Type == term.EventMouse && ev.Key == term.MouseLeft {
		press := !h.mouseDown
		h.mouseDown = true
		if press && ev.MouseY < h.contentHeight() &&
			h.enterAtClick(ev.MouseY) {
			h.syncWidth()
		}
		return false, true
	}
	if ev.Type == term.EventMouse && ev.Key == term.MouseRelease {
		h.mouseDown = false
		return false, true
	}
	exit, handled = h.span.Handle(ev)
	if !handled {
		return exit, handled
	}
	h.syncWidth()
	if exit {
		h.focusTarget()
		return false, true
	}
	return false, true
}

// enterSelectsNode reports whether <Enter> acts on the tree rather
// than on the text. While the explorer is locked the buffer is a
// browser and <Enter> opens or toggles. Once it is editable, <Enter>
// keeps its editor meaning wherever it would insert text: always in a
// modeless editor, and outside normal mode in a modal one. Otherwise
// the row under the cursor could never be split into a new entry.
func (h *fileExplorerHandler) enterSelectsNode() bool {
	if h.ed.IsSearchMode() {
		return false
	}
	return h.readOnly || h.ed.IsNormalMode()
}

// Handle implements text.EventHandler. Filesystem watcher events
// dispatched through ex.comp arrive here and drive the explorer's
// reactive refresh policy:
//
//   - URI outside the explorer's root (or root itself): ignored.
//   - Window not visible: discard unflushed buffer edits and
//     refresh the tree from disk now. The user can't see the
//     buffer state, so silently bringing it in sync with the FS
//     keeps the next open consistent.
//   - Window visible and no pending edits: refresh now.
//   - Window visible with pending edits: defer the refresh until
//     the user resolves the edits — either by flushing them
//     successfully (forceFlush replays the pending refresh) or
//     by closing the explorer (onWindowClosed replays it,
//     dropping the unflushed edits).
//
// Always returns false so the handler stays subscribed for the
// lifetime of the cached fileExplorerHandler.
func (h *fileExplorerHandler) onFSEvent(_ context.Context, ev textapi.Event) bool {
	if !h.eventApplies(ev) {
		return false
	}
	if !h.windowVisible() {
		// Discard unflushed buffer edits and refresh now: the
		// user cannot see them and the on-disk tree is the new
		// source of truth.
		h.refreshTree()
		h.pendingRefresh = false
		return false
	}
	if h.comp.HasPendingEdits() {
		// Wait until the user resolves their edits (flush or
		// close). Multiple events while dirty collapse into one
		// pending refresh.
		h.pendingRefresh = true
		return false
	}
	h.refreshTree()
	h.pendingRefresh = false
	return false
}

// eventApplies returns true when ev refers to a path strictly
// under the explorer's root. The root itself is excluded so we
// don't refresh on every save of a workspace-level file (the root
// directory's mtime updates do not affect the rendered tree).
func (h *fileExplorerHandler) eventApplies(ev textapi.Event) bool {
	root := h.comp.Root()
	if !workspaceapi.HasPrefix(ev.URI, root) {
		return false
	}
	return ev.URI.Path() != root.Path()
}

// windowVisible reports whether the explorer split is currently
// shown to the user. The handler is cached across toggles, so
// h.win can be a previously-closed Window even when the explorer
// is no longer rendered; treat such windows as not visible.
func (h *fileExplorerHandler) windowVisible() bool {
	return h.win != nil && !h.win.Closed()
}

// refreshTree calls comp.Refresh and reports any error through the
// host's notification channel. The tree's buffer is replaced
// in-place so the editor's cursor needs to be re-clamped to the
// new bounds, mirroring enterAtCursor.
func (h *fileExplorerHandler) refreshTree() {
	expanded := h.comp.ExpandedDirectories()
	if err := h.comp.Refresh(); err != nil {
		h.host.SetError(err)
		return
	}
	h.comp.ExpandDirectories(expanded)
	if h.ed != nil {
		h.clampCursorToBuffer()
	}
	h.syncWidth()
}

// clampCursorToBuffer keeps the editor cursor inside the buffer
// bounds after a Refresh that may have shrunk the rendered tree.
func (h *fileExplorerHandler) clampCursorToBuffer() {
	pos := h.ed.CursorAtScroll()
	rows := h.ed.CellView().Rows()
	if rows == 0 {
		_ = h.ed.SetCursorAtScroll(term.Coordinates{})
		return
	}
	if pos.Y >= rows {
		pos.Y = rows - 1
	}
	cols := h.ed.CellView().Columns(pos.Y)
	if pos.X > cols {
		pos.X = cols
	}
	_ = h.ed.SetCursorAtScroll(pos)
}

// onWindowClosed is called by ex.fexplorer when the explorer is
// toggled off. If a refresh was pending (i.e. an FS event arrived
// while the user had unflushed edits), run it now and discard
// those edits — they would conflict with the new on-disk state.
func (h *fileExplorerHandler) onWindowClosed() {
	h.readOnly = h.cfg.ReadOnly
	if !h.pendingRefresh {
		return
	}
	h.refreshTree()
	h.pendingRefresh = false
}

func (h *fileExplorerHandler) Draw(w term.Writer) {
	h.span.Draw(w)
	h.drawHint(w)
}

// drawHint renders the bottom row naming the one thing the user can do
// next: write the staged edits, or leave read-only mode.
func (h *fileExplorerHandler) drawHint(w term.Writer) {
	hint := h.hint()
	if hint == "" || h.height < fileExplorerHintHeight+1 {
		return
	}
	tcomponent.WriteText(w, fileExplorerSpanHPad/2, h.height-1, h.width,
		hint, h.cfg.HintAttr)
}

func (h *fileExplorerHandler) hint() string {
	if !h.cfg.Hint {
		return ""
	}
	if h.readOnly {
		return h.cfg.EditKey.String() + " to enter edit mode"
	}
	// Naming the command prompt instead of a chord the user does not
	// have ("save <shift-;> write") is noise, not a hint.
	if h.cfg.SaveKey == "" {
		return ""
	}
	return "save " + h.cfg.SaveKey
}

// hintHeight is the row the hint occupies, or zero when there is
// nothing to say: an empty row is worse than no row.
func (h *fileExplorerHandler) hintHeight() int {
	if h.hint() == "" {
		return 0
	}
	return fileExplorerHintHeight
}

// contentHeight is the height left for the tree once the hint row is
// reserved.
func (h *fileExplorerHandler) contentHeight() int {
	return max(0, h.height-h.hintHeight())
}

func (h *fileExplorerHandler) Resize(width, height int) {
	h.width = width
	h.height = height
	h.span.Resize(width, h.contentHeight())
}

func (h *fileExplorerHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return h.span.Cursor()
}

func (h *fileExplorerHandler) Selection() (string, bool) {
	return h.span.Selection()
}

func (h *fileExplorerHandler) Dimensions() (int, int) {
	// The editor reports its own ideal dimensions including any
	// auxiliary chrome (line numbers, folds, git icons) drawn on
	// top of the shared buffer; the surrounding span adds the
	// configured horizontal breathing room so the parent window
	// can size itself to fit the chrome, the full tree, and the
	// padding without truncation.
	w, height := h.span.Dimensions()
	if _, rows := h.comp.Dimensions(); rows == 0 {
		w, height = max(w, h.cfg.MinWidth), 1
	}
	return max(w, h.hintWidth()), height + h.hintHeight()
}

// hintWidth is the width the hint row needs including the span's
// horizontal padding, so syncWidth never sizes the window narrower
// than the message it is about to draw.
func (h *fileExplorerHandler) hintWidth() int {
	hint := h.hint()
	if hint == "" {
		return 0
	}
	return utf8.RuneCountInString(hint) + fileExplorerSpanHPad
}

func (h *fileExplorerHandler) SeekUp() bool {
	return h.ed.SeekUp()
}

func (h *fileExplorerHandler) SeekDown() bool {
	return h.ed.SeekDown()
}

func (h *fileExplorerHandler) SeekOffset() int {
	return h.ed.SeekOffset()
}

func (h *fileExplorerHandler) MaxSeekOffset() int {
	return h.ed.MaxSeekOffset()
}

// Close is called when the tiled window hosting the file explorer
// is closed (via :fexplorer toggle). It intentionally does NOT close
// the underlying text editor handler: the editor is cached on the
// ex and re-used when the explorer is re-opened, so that Edit is
// only ever called once per URI. Closing here would unsubscribe the
// vi editor's per-file commands (fold/location/git) and then the
// next open would try to re-subscribe them, failing with
// "command already registered".
func (h *fileExplorerHandler) Close() error { return nil }

// closeEditor closes the underlying editor chain. This MUST be
// called exactly once per explorer lifetime, when the owning ex is
// being torn down.
func (h *fileExplorerHandler) closeEditor() error {
	if h.ed == nil {
		return nil
	}
	err := h.ed.Close()
	h.ed = nil
	return err
}

func (h *fileExplorerHandler) enterAtCursor() bool {
	return h.enterAt(h.ed.CursorAtScroll())
}

// enterAtClick toggles or opens the node at the clicked window row.
// The click's window Y is translated to a buffer scroll row using the
// editor's current scroll offset (the delta between the cursor's scroll
// and window coordinates), so it stays correct when the tree is
// scrolled. The toggle/open acts on the computed row directly, so it
// is unaffected by whether the editor cursor actually moves; clicks
// past the last row resolve to no node and are a safe no-op in
// ExpandNodeAt. Cursor placement bypasses the editor's mouse handler
// so no visual-mode selection is started.
func (h *fileExplorerHandler) enterAtClick(windowY int) bool {
	winCur, _, _ := h.ed.Cursor()
	scrollCur := h.ed.CursorAtScroll()
	pos := term.Coordinates{Y: max(0, windowY+(scrollCur.Y-winCur.Y))}
	// Moving the cursor with the editor's autoCenter on would recenter
	// the viewport, jerking the tree out from under the pointer. The
	// clicked row is already visible, so preserve the scroll offset
	// across the cursor move and toggle.
	offset := h.ed.SeekOffset()
	h.ed.SetCursorAtScroll(pos)
	handled := h.enterAt(pos)
	h.restoreOffset(offset)
	return handled
}

// restoreOffset seeks the editor back to a previously captured vertical
// scroll offset, undoing any autoCenter-driven viewport movement from a
// programmatic cursor placement.
func (h *fileExplorerHandler) restoreOffset(offset int) {
	for h.ed.SeekOffset() > offset && h.ed.SeekUp() {
	}
	for h.ed.SeekOffset() < offset && h.ed.SeekDown() {
	}
}

func (h *fileExplorerHandler) enterAt(pos term.Coordinates) bool {
	prev := pos
	uri, open := h.comp.ExpandNodeAt(pos)
	if open {
		h.open(uri)
		return true
	}
	if _, known := h.comp.NodeAt(pos); !known && h.rowHasContent(pos.Y) {
		h.host.SetError(errors.New(
			"file explorer: unsaved row, write to create it"))
	}
	// Component may have rewritten the buffer; restore the cursor
	// clamped to the new bounds.
	rows := h.ed.CellView().Rows()
	if rows == 0 {
		_ = h.ed.SetCursorAtScroll(term.Coordinates{})
	} else {
		if prev.Y >= rows {
			prev.Y = rows - 1
		}
		cols := h.ed.CellView().Columns(prev.Y)
		if prev.X > cols {
			prev.X = cols
		}
		_ = h.ed.SetCursorAtScroll(prev)
	}
	return true
}

// rowHasContent reports whether y addresses a rendered, non-blank
// buffer row. Clicks past the last row resolve to a no-op and must
// not report an error.
func (h *fileExplorerHandler) rowHasContent(y int) bool {
	view := h.ed.CellView()
	if y < 0 || y >= view.Rows() {
		return false
	}
	return view.Columns(y) > 0
}

func (h *fileExplorerHandler) flush() error {
	if h.readOnly {
		return nil
	}
	cs := h.comp.DryFlush()
	if cs.HasConflicts() {
		h.host.SetError(conflictsError(cs))
		return nil
	}
	if len(cs.Operations) == 0 {
		return nil
	}
	return h.openApplyPrompt(fileexplorercomp.OrderOperations(cs.Operations))
}

func (h *fileExplorerHandler) forceFlush() error {
	return h.flush()
}

func conflictsError(cs *fileexplorercomp.ChangeSet) error {
	msgs := make([]string, 0, len(cs.Conflicts))
	for _, c := range cs.Conflicts {
		msgs = append(msgs, c.Message)
	}
	return &flushError{msg: strings.Join(msgs, "; ")}
}

type flushError struct{ msg string }

func (e *flushError) Error() string { return "file explorer conflicts: " + e.msg }

func (h *fileExplorerHandler) openApplyPrompt(ops []fileexplorercomp.Operation) error {
	message := h.confirmMessage(ops)
	var promptWin browser.Window
	promptWin = h.host.Prompt(message, []string{yesOpt, noOpt}, yesNoKeyCombs,
		handler.FuncPromptHandler(func(_ int, option string) {
			if promptWin != nil {
				_ = promptWin.Close()
			}
			if option != yesOpt {
				return
			}
			if _, err := h.comp.Flush(); err != nil {
				h.host.SetError(err)
				return
			}
			// A pending refresh queued while the user was
			// editing has now been resolved; replay it so the
			// just-written tree picks up any concurrent FS
			// changes that arrived during the edit.
			if h.pendingRefresh {
				h.refreshTree()
				h.pendingRefresh = false
			}
		}, func() error {
			return nil
		}))
	return nil
}

// confirmMessage renders the set of pending operations as a markdown
// prompt message. Operations are grouped and labelled as a markdown
// unordered list so a long change set is easy to scan:
//
//	Apply the following changes?
//	- **CREATE**\t<relative-path>
//	- **MKDIR**\t<relative-path>
//	- **RENAME**\t<old-name> -> <new-name>
//	- **MOVE**\t<old-rel> -> <new-rel>
//	- **DELETE**\t<relative-path>
//
// A RENAME is used only when the source and destination share the
// same parent directory (same URI.Path() dir, different basename).
// A MOVE is used when the parents differ — regardless of whether the
// basename is also different. This matches the user's mental model:
// "rename" never changes where a file lives; "move" always does.
func (h *fileExplorerHandler) confirmMessage(
	ops []fileexplorercomp.Operation,
) string {
	return h.confirmMessageForTest(h.comp.Root(), ops)
}

// confirmMessageForTest is the testable core of confirmMessage; it
// accepts the root URI explicitly so tests can exercise the
// formatting without wiring a component.
func (h *fileExplorerHandler) confirmMessageForTest(
	root workspaceapi.URI,
	ops []fileexplorercomp.Operation,
) string {
	var b strings.Builder
	b.WriteString("Apply the following changes?")
	for _, op := range ops {
		label, body := formatOperation(root, op)
		if label == "" {
			continue
		}
		b.WriteByte('\n')
		b.WriteString("- **")
		b.WriteString(label)
		b.WriteString("**\t")
		b.WriteString(body)
	}
	return b.String()
}

// formatOperation returns the bolded label and the path portion to
// render for a single operation. Rename vs. Move is disambiguated by
// comparing the source and destination parent directories.
func formatOperation(
	root workspaceapi.URI, op fileexplorercomp.Operation,
) (label, body string) {
	rel := func(u workspaceapi.URI) string {
		return relativePath(root, u)
	}
	switch op.Type {
	case fileexplorercomp.OpCreate:
		return "CREATE", rel(op.NewURI)
	case fileexplorercomp.OpMkdir:
		return "MKDIR", rel(op.NewURI)
	case fileexplorercomp.OpDelete:
		return "DELETE", rel(op.URI)
	case fileexplorercomp.OpRename, fileexplorercomp.OpMove:
		if sameParent(op.URI, op.NewURI) {
			return "RENAME",
				op.URI.Name() + " -> " + op.NewURI.Name()
		}
		return "MOVE", rel(op.URI) + " -> " + rel(op.NewURI)
	case fileexplorercomp.OpCopy:
		return "COPY", rel(op.URI) + " -> " + rel(op.NewURI)
	}
	return "", ""
}

// relativePath returns uri expressed relative to root, falling back
// to the absolute path when root is not a prefix of uri.
func relativePath(root, uri workspaceapi.URI) string {
	rp := root.Path()
	up := uri.Path()
	if rp != "" && strings.HasPrefix(up, rp) {
		rel := strings.TrimPrefix(up, rp)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return uri.Name()
		}
		return rel
	}
	return up
}

func sameParent(a, b workspaceapi.URI) bool {
	return workspaceapi.Dir(a).Path() == workspaceapi.Dir(b).Path()
}

func (h *fileExplorerHandler) syncWidth() {
	if h.win == nil || h.win.Closed() {
		return
	}
	width, _ := h.Dimensions()
	if h.host.FrameEnabled() {
		width += 2
	}
	_ = h.host.SetWindowWidth(h.win, width)
}

func (h *fileExplorerHandler) focusTarget() {
	if h.target != nil && !h.target.Closed() && h.target != h.win {
		_, _ = h.host.SetFocus(h.target)
	}
}

func (h *fileExplorerHandler) open(uri workspaceapi.URI) {
	win := h.targetWindow()
	if win == nil {
		return
	}
	if err := h.host.OpenFile(uri, win); err != nil {
		h.host.SetError(err)
	}
}

func (h *fileExplorerHandler) targetWindow() browser.Window {
	if h.target != nil && !h.target.Closed() && h.target != h.win {
		return h.target
	}
	win, _ := h.host.Focus()
	if win != nil && win != h.win && !win.Closed() {
		h.target = win
		return win
	}
	return nil
}

// fsEventHandler returns a text.EventHandler that this
// fileExplorerHandler can be subscribed with via
// (*text.Component).SubscribeEvents. The returned handler stays
// alive for the lifetime of the cached fileExplorerHandler.
func (h *fileExplorerHandler) fsEventHandler() text.EventHandler {
	return text.FuncEventHandler(h.onFSEvent)
}

type exFileExplorerHost struct {
	ex *ex
}

func (h exFileExplorerHost) Prompt(
	message string,
	options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	return h.ex.comp.Prompt(message, options, bindings, promptHandler)
}

func (h exFileExplorerHost) SetWindowWidth(win browser.Window, width int) bool {
	return h.ex.comp.SetWindowWidth(win, width)
}

func (h exFileExplorerHost) SetFocus(win browser.Window) (browser.Window, error) {
	return h.ex.comp.SetFocus(win)
}

func (h exFileExplorerHost) Focus() (browser.Window, error) {
	return h.ex.comp.Focus()
}

func (h exFileExplorerHost) OpenFile(uri workspaceapi.URI, win browser.Window) error {
	_, err := h.ex.editFileURI(uri, win, false)
	return err
}

func (h exFileExplorerHost) SetError(err error) {
	h.ex.setError(err)
}

func (h exFileExplorerHost) FrameEnabled() bool {
	return h.ex.config.Frame
}
