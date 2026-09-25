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
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	handlerapi "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/text"
)

// minRootWidth is the narrowest the workspaces may be squeezed to make
// room for the tile; below it the tile is not shown at all.
const minRootWidth = 40

// tutorialRunner drives one tutorial at a time. While a lesson runs
// the runner lays the workspaces out beside a full-height tile on the
// right, in a frame union of its own above every workspace's window
// manager: the window commands a lesson teaches act on the windows
// inside the workspaces and cannot reach the tile, so a lesson
// survives the very commands it asks for.
type tutorialRunner struct {
	tui.Handler
	tutorials map[string]idetutorial.Tutorial
	style     idetutorial.TileStyle

	tut        idetutorial.Tutorial
	tile       *idetutorial.Tile
	pane       tilePane
	union      *handler.FrameUnion
	tileSize   int
	activeName string
	// stopRequested is set by the tile's Stop button and cleared by
	// clearActive; it is folded into the next reconcile so the frame
	// the request came from is never torn down under it.
	stopRequested bool
	// mouseDown pins a press that landed on the tile to it until the
	// release, so a drag that leaves the tile still ends there.
	mouseDown bool

	width, height int
	// inset is the column reserved along the right edge for a UI
	// element floating over it. The workspaces reserve it while no
	// lesson runs; the tile does while one does.
	inset     int
	rootInset func(cells int)

	onCompleted func(name string)
	// active mirrors tut != nil for readers off the event loop, such
	// as the package-install gate called from syntax and LSP
	// goroutines.
	active atomic.Bool
}

var _ commandObserver = (*tutorialRunner)(nil)

// init wires the runner around root. rootInset hands the reserved
// right column to root while no lesson runs, and inset is its
// current width.
func (r *tutorialRunner) init(
	root tui.Handler,
	tutorials map[string]idetutorial.Tutorial,
	style idetutorial.TileStyle,
	inset int,
	rootInset func(cells int),
	onCompleted func(name string),
) {
	r.Handler = root
	r.tutorials = tutorials
	r.style = style
	r.inset = inset
	r.rootInset = rootInset
	r.onCompleted = onCompleted
}

func (r *tutorialRunner) setActive(name string, t idetutorial.Tutorial) {
	if r.tut != nil {
		r.closeActive()
	}
	r.tut = t
	r.tile = idetutorial.NewTile(t, r.style, r.back, r.advance, r.requestStop)
	r.pane = tilePane{}
	r.pane.v.C = r.tile
	r.activeName = name
	r.active.Store(true)
	if r.rootInset != nil {
		r.rootInset(0)
	}
	r.tile.SetRightInset(r.inset)
	r.layout()
	t.Reset()
	r.reconcile()
}

// advance moves the tile on by one screen: the step the lesson is on
// is skipped, while a screen the user paged back to is simply stepped
// past, since skipping a step they cannot see would be a surprise. A
// lesson that ended as a result is torn down by the reconcile that
// follows the click.
func (r *tutorialRunner) advance() {
	if r.tut == nil {
		return
	}
	if r.tut.ViewingPast() {
		r.tut.Forward()
		return
	}
	r.tut.Skip()
}

// back pages the tile through the copy the lesson already showed.
func (r *tutorialRunner) back() {
	if r.tut != nil {
		r.tut.Back()
	}
}

func (r *tutorialRunner) requestStop() { r.stopRequested = true }

func (r *tutorialRunner) clearActive() {
	if r.tut == nil {
		return
	}
	r.tut.Stop()
	r.tut = nil
	r.tile = nil
	r.pane = tilePane{}
	r.union = nil
	r.tileSize = 0
	r.activeName = ""
	r.stopRequested = false
	r.mouseDown = false
	r.active.Store(false)
	r.Handler.Resize(r.width, r.height)
	if r.rootInset != nil {
		r.rootInset(r.inset)
	}
}

// finishActive tears the tile down for a lesson that ended without
// reaching its end: an error, a script-side stop, a refusal to run. A
// lesson that ran to completion stays on screen instead, since its
// last step is usually the one the user is still working through;
// closeActive takes it away when the user asks.
func (r *tutorialRunner) finishActive() {
	if r.tut == nil {
		return
	}
	if r.tut.Completed() {
		return
	}
	r.closeActive()
}

// closeActive takes the tile away and reports a lesson that had run to
// completion, so the run counts once the user is done reading it.
func (r *tutorialRunner) closeActive() {
	if r.tut == nil {
		return
	}
	name := r.activeName
	// A lesson only counts when it ran to its end: stopping one
	// part-way through is not a completion.
	completed := r.tut.Finished() && r.tut.Completed()
	r.clearActive()
	if completed && r.onCompleted != nil {
		r.onCompleted(name)
	}
}

// reconcile brings the runner in line with the lesson and the layout:
// a lesson that was stopped or ended badly is torn down, and one still
// on screen keeps the tile lined up with the workspaces' windows,
// which move whenever a bar appears or goes away. It runs after every
// event, command and frame, since lessons also advance from scheduled
// work.
func (r *tutorialRunner) reconcile() {
	if r.tut == nil {
		return
	}
	if r.stopRequested {
		r.closeActive()
		return
	}
	if r.tut.Finished() {
		r.finishActive()
	}
	r.alignTile()
}

// alignTile pads the tile down to the rows the workspaces' windows
// occupy.
func (r *tutorialRunner) alignTile() {
	if r.tile == nil {
		return
	}
	if rows, ok := r.Handler.(windowRows); ok {
		r.pane.align(rows.windowRows())
		return
	}
	r.pane.align(0, r.height)
}

// layout rebuilds the union around root and the tile for the current
// size. The union only ever appends members and never resizes them,
// so a new tile width means a new union.
func (r *tutorialRunner) layout() {
	if r.tile == nil {
		return
	}
	size := r.tileWidth()
	if r.union != nil && size == r.tileSize {
		r.union.Resize(r.width, r.height)
		return
	}
	r.tileSize = size
	r.union = handler.NewFrameUnion(r.Handler)
	// The tile frames itself, like a window beside other windows:
	// it shares no frame line with the workspaces, so its focus frame
	// is never drawn over.
	r.union.Frame = false
	if size > 0 {
		r.union.UnionRightFrame(&r.pane, size, false)
	} else {
		r.tile.SetFocused(false)
	}
	r.union.Resize(r.width, r.height)
	r.alignTile()
}

// windowRows is implemented by the handler the tile is laid out
// beside, to report the rows its windows occupy: the first row and
// how many. The tile is padded to the same rows so it lines up with
// them across the bars that frame them.
type windowRows interface {
	windowRows() (top, rows int)
}

// tilePane holds the tile in the column reserved for it, padded to
// the rows the windows beside it occupy. Those bars belong to the
// workspaces and stop at their edge, so without the padding the
// lesson would run past them, top and bottom.
type tilePane struct {
	v             handlerapi.Virtual[*idetutorial.Tile]
	width, height int
	top, rows     int
}

// Resize satisfies tui.Component.
func (p *tilePane) Resize(width, height int) {
	p.width, p.height = width, height
	p.resizeTile()
}

// align pads the tile to the rows between top and top+rows.
func (p *tilePane) align(top, rows int) {
	if top == p.top && rows == p.rows {
		return
	}
	p.top, p.rows = top, rows
	p.resizeTile()
}

func (p *tilePane) resizeTile() {
	top := min(max(p.top, 0), p.height)
	rows := min(max(p.rows, 0), p.height-top)
	p.v.Move(term.Coordinates{Y: top})
	p.v.Resize(p.width, rows)
}

// Draw satisfies tui.Component.
func (p *tilePane) Draw(w term.Writer) {
	p.v.Draw(w)
}

// Handle satisfies tui.Handler. Only what lands on the tile's own
// rows is the tile's; the padding belongs to nothing.
func (p *tilePane) Handle(ev term.Event) (bool, bool) {
	if ev.Type == term.EventMouse {
		top := p.v.Position().Y
		if ev.MouseY < top || ev.MouseY >= top+p.v.Height() {
			return false, false
		}
	}
	return p.v.Handle(ev)
}

// Cursor satisfies tui.Handler.
func (p *tilePane) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return p.v.Cursor()
}

// Selection satisfies tui.Handler.
func (p *tilePane) Selection() (string, bool) {
	return p.v.Selection()
}

// tileWidth is the width of the tile's column, frame and reserved
// inset included, or zero when the screen is too narrow to show it.
func (r *tutorialRunner) tileWidth() int {
	size := idetutorial.TileWidth(r.width) + r.inset
	if r.style.Frame {
		size += 2
	}
	if r.width-size < minRootWidth {
		return 0
	}
	return size
}

// tileVisible reports whether the tile is laid out on screen.
func (r *tutorialRunner) tileVisible() bool {
	return r.tile != nil && r.tileSize > 0
}

// tileColumn is the screen column the tile's own coordinates start at.
func (r *tutorialRunner) tileColumn() int {
	return r.width - r.tileSize
}

func (r *tutorialRunner) inTile(pos term.Coordinates) bool {
	if !r.tileVisible() {
		return false
	}
	return pos.X >= r.tileColumn() && pos.X < r.width &&
		pos.Y >= 0 && pos.Y < r.height
}

func (r *tutorialRunner) tileFocused() bool {
	return r.tile != nil && r.tile.Focused()
}

// setRightInset moves the reserved right column to whichever of the
// workspaces and the tile sits at the right edge.
func (r *tutorialRunner) setRightInset(cells int) {
	r.inset = max(cells, 0)
	if r.tile == nil {
		if r.rootInset != nil {
			r.rootInset(r.inset)
		}
		return
	}
	r.tile.SetRightInset(r.inset)
	r.layout()
}

// register adds a tutorial under name and reports whether it was added. It
// returns false without overwriting when a tutorial is already registered
// under name. It must be called on the event loop because the tutorials map
// is read there by HandleCommand and Complete.
func (r *tutorialRunner) register(name string, t idetutorial.Tutorial) bool {
	if _, ok := r.tutorials[name]; ok {
		return false
	}
	if r.tutorials == nil {
		r.tutorials = make(map[string]idetutorial.Tutorial, 1)
	}
	r.tutorials[name] = t
	return true
}

func (r *tutorialRunner) has(name string) bool {
	_, ok := r.tutorials[name]
	return ok
}

// running reports whether a tutorial is currently active. It is safe
// to call from any goroutine.
func (r *tutorialRunner) running() bool {
	return r.active.Load()
}

func (r *tutorialRunner) Resize(width, height int) {
	r.width, r.height = width, height
	if r.union == nil {
		r.Handler.Resize(width, height)
		return
	}
	r.layout()
}

func (r *tutorialRunner) Draw(w term.Writer) {
	r.reconcile()
	if r.union == nil {
		r.Handler.Draw(w)
		return
	}
	r.union.Draw(w)
}

func (r *tutorialRunner) Handle(ev term.Event) (bool, bool) {
	exit, handled := r.handle(ev)
	r.reconcile()
	return exit, handled
}

// handle routes ev between the tile and the workspaces. Only the mouse
// reaches the tile, and a press moves focus with it. Everything the
// user types goes to the workspaces wherever the tile's focus is, so
// the editor, a terminal or the command prompt still answer the
// keyboard while the lesson is the pane in focus; the tile is read
// with the mouse and its buttons are clicked. The one exception is a
// step that asks a question: it is the lesson waiting on the user,
// so the keys that answer a prompt reach it first.
func (r *tutorialRunner) handle(ev term.Event) (bool, bool) {
	if r.answersPrompt(ev) {
		if _, handled := r.tut.Handle(ev); handled {
			return false, true
		}
	}
	if ev.Type != term.EventMouse || !r.tileVisible() {
		return r.Handler.Handle(ev)
	}
	toTile := r.mouseDown || r.inTile(term.Coordinates{X: ev.MouseX, Y: ev.MouseY})
	switch ev.Key {
	case term.MouseLeft:
		r.tile.SetFocused(toTile)
		r.mouseDown = toTile
	case term.MouseRelease:
		r.mouseDown = false
	}
	if !toTile {
		return r.Handler.Handle(ev)
	}
	ev.MouseX = max(ev.MouseX-r.tileColumn(), 0)
	_, handled := r.pane.Handle(ev)
	return false, handled
}

// answersPrompt reports whether ev is one of the keys the question on
// the tile is answered with: the arrows (and their ctrl-h/ctrl-l
// twins) walk the options and Enter takes the highlighted one. Esc is
// left to the workspace, where it leaves a modal editor's insert
// mode; a step is dismissed with the tile's own buttons.
func (r *tutorialRunner) answersPrompt(ev term.Event) bool {
	if ev.Type != term.EventKey || !r.tileVisible() {
		return false
	}
	switch {
	case ev.Mod == term.ModCtrl:
		if ev.Ch != 'h' && ev.Ch != 'l' {
			return false
		}
	case ev.Mod != 0:
		return false
	case ev.Key != term.KeyArrowLeft && ev.Key != term.KeyArrowRight &&
		ev.Key != term.KeyEnter:
		return false
	}
	return r.tut.PromptActive()
}

// Cursor satisfies tui.Handler. The tile owns the cursor while it
// holds focus, as any focused window would.
func (r *tutorialRunner) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if !r.tileFocused() {
		if r.union == nil {
			return r.Handler.Cursor()
		}
		return r.union.Cursor()
	}
	pos, style, show := r.pane.Cursor()
	pos.X += r.tileColumn()
	return pos, style, show
}

// Selection satisfies tui.Handler.
func (r *tutorialRunner) Selection() (string, bool) {
	if r.tileFocused() {
		return r.tile.Selection()
	}
	return r.Handler.Selection()
}

func (r *tutorialRunner) Close() error {
	r.clearActive()
	return nil
}

// observeCommand feeds every dispatched command to the lesson.
func (r *tutorialRunner) observeCommand(
	typed, resolved string, args []string, err error,
) {
	if r.tut == nil {
		return
	}
	if r.tut.ObserveCommand(typed, resolved, args, err) {
		r.finishActive()
		return
	}
	r.reconcile()
}

func (r *tutorialRunner) observeEvent(eventType, uri string) {
	if r.tut == nil {
		return
	}
	if runeOwnedResource(uri) {
		return
	}
	if r.tut.ObserveEvent(eventType, uri) {
		r.finishActive()
		return
	}
	r.reconcile()
}

// runeOwnedResource reports whether uri names a buffer Rune opens for
// itself. The file explorer's tree and gitshow's diffs publish the
// same events a file the user opened does, and a lesson waiting on one
// of those would advance without the user having done anything.
func runeOwnedResource(uri string) bool {
	return uri == fileExplorerURI ||
		strings.HasPrefix(uri, gitshowBaseURI().String())
}

// tutorialEventObserver builds the text-event subscriber that feeds the
// tutorial runner. Text/LSP events are delivered on background
// subscriber goroutines, but the runner is otherwise only driven from
// the event loop (Handle/HandleCommand); observe therefore hops onto
// the loop via schedule so the runner's active lesson is never torn
// from two goroutines at once. schedule must be the host's
// event-loop scheduler.
func tutorialEventObserver(
	schedule func(func()) bool, observe func(eventType, uri string),
) text.EventHandler {
	return text.FuncEventHandler(func(_ context.Context, ev textapi.Event) bool {
		eventType, uri := ev.Type.String(), ev.URI.String()
		schedule(func() { observe(eventType, uri) })
		return false
	})
}

func (r *tutorialRunner) HandleCommand(_ context.Context, cmd textapi.Command) error {
	if len(cmd.Args) == 0 {
		return errors.New(
			"usage: tutorial <start|stop> [<name>]")
	}
	switch cmd.Args[0] {
	case "start":
		if len(cmd.Args) != 2 {
			return errors.New("usage: tutorial start <name>")
		}
		name := cmd.Args[1]
		tut, ok := r.tutorials[name]
		if !ok {
			return fmt.Errorf("unknown tutorial %q", name)
		}
		r.setActive(name, tut)
		return nil
	case "stop":
		if len(cmd.Args) > 1 {
			return fmt.Errorf("tutorial stop: takes no arguments, got %d",
				len(cmd.Args)-1)
		}
		if r.tut == nil {
			return errors.New("no tutorial is running")
		}
		r.closeActive()
		return nil
	}
	return fmt.Errorf("unknown subcommand %q: "+
		"want one of start, stop", cmd.Args[0])
}

func (r *tutorialRunner) Complete(_ context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	// First argument: list subcommands.
	if len(cmd.Args) <= 1 {
		return iterator.FromSlice([]string{
			"start", "stop",
		}), "", nil
	}
	// Second argument after `start`: tutorial names.
	if len(cmd.Args) == 2 {
		switch cmd.Args[0] {
		case "start":
			names := make([]string, 0, len(r.tutorials))
			for n := range r.tutorials {
				names = append(names, n)
			}
			sort.Strings(names)
			return iterator.FromSlice(names), "", nil
		}
	}
	return iterator.Empty[string](), "", nil
}
