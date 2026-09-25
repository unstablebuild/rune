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

package gui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"sync/atomic"
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/term/gui/drawrect"
	"unstable.build/rune/internal/term/gui/font"
)

var (
	_ ebiten.Game = (*GUI)(nil)

	// ErrHandlerExited is returned by GUI.Run to indicate that
	// the root tui.Handler exited.
	ErrHandlerExited = errors.New("tui handler exited")
)

const (
	defaultWidth, defaultHeight = 800, 600
	// echoPollInterval is the sleep slice while awaiting a
	// post-keystroke interrupt.
	echoPollInterval = 50 * time.Microsecond
)

// echoWaitBudget bounds the once-per-tick wait for the focused
// handler's asynchronous post-keystroke update (a pty echo), letting it
// render in the keystroke's own frame instead of the next one. It must
// stay well under a frame period: with vsync the present time is
// unchanged as long as Update plus Draw still fit the frame. It is a
// variable so tests can widen it for deterministic timing margins.
var echoWaitBudget = 2 * time.Millisecond

// GUI implements a graphical TUI runtime as an alternative runtime to what
// the tui packages provides.
type GUI struct {
	ctx               context.Context
	cancelCtx         func()
	mu                sync.Locker
	fontManager       *font.Manager
	updateChan        chan term.Event
	handler           tui.Handler
	writer            *frameWriter
	mouse             *mouse
	input             *input
	drag              *dragPoller
	theme             string
	bgOpacity         float64
	fgOpacity         float64
	defaultWidth      int
	defaultHeight     int
	explicitSize      bool
	startPositionX    int
	startPositionY    int
	printFPS          bool
	bgBlurRadius      int
	enableTransparent bool
	enableLigatures   bool
	forceFullRepaint  bool
	renderOffset      image.Point
	cursorAttributes  term.Attributes
	defaultAttr       term.Attributes
	renderer          *renderer

	originalColorValues map[tcell.Color]int32
	originalValuesColor map[int32]tcell.Color
	initialTheme        string
	colorThemes         map[string]Theme

	cursor struct {
		pos   term.Coordinates
		style term.CursorStyle
		show  bool
	}

	pendingEvents []term.Event
	needsDraw     bool
	needsRender   bool
	width         int
	height        int
	lastPositionX int
	lastPositionY int
	iteration     int64
	deviceScale   float64
	// cellPixelSize is the cell pitch in device pixels, packed as
	// width<<32|height and published on every resize for readers off
	// the event loop.
	cellPixelSize atomic.Uint64

	links linkScanner

	// echoLikely arms the once-per-tick echo wait. It is learned, not
	// configured: an interrupt pending at tick entry right after a
	// single-key tick means the focused handler echoes asynchronously
	// (a terminal); a timed-out wait disarms it, so handlers that
	// update synchronously (the editor) never pay the wait.
	echoLikely  bool
	prevTickKey bool

	interruptPending atomic.Bool
	started          atomic.Bool
	// processWindowClosed turns a pending window close request into
	// events for the handler. WithCloseRequestEvent installs it; it
	// defaults to a no-op, leaving ebiten's default behavior in place.
	processWindowClosed func() []term.Event
	closingHandled      bool
	closeOnce           sync.Once
}

// New allocates storage for a new GUI and initializes it with the given
// tui.Handler and options.
func New(handler tui.Handler, options ...Option) (*GUI, error) {
	drawrect.Init()
	const (
		cellOverlapX = 0
		cellOverlapY = 0
	)
	fontManager, err := font.NewManager(cellOverlapX, cellOverlapY)
	if err != nil {
		return nil, fmt.Errorf("font manager: %v", err)
	}
	ret := &GUI{
		mu:               new(sync.Mutex),
		handler:          handler,
		updateChan:       make(chan term.Event, 4096),
		bgOpacity:        1,
		fgOpacity:        1,
		fontManager:      fontManager,
		enableLigatures:  true,
		cursorAttributes: term.Attributes{Bg: term.FromTcellColor(tcell.ColorRed)},
		startPositionX:   0,
		startPositionY:   0,
		defaultWidth:     defaultWidth,
		defaultHeight:    defaultHeight,
	}
	ret.input = newInput(ret.fontManager)
	ret.mouse = newMouse(ret.fontManager)
	ret.drag = newDragPoller(ret.mouse)
	ret.links = newLinkScanner()
	ret.ctx = context.Background()
	ret.ctx, ret.cancelCtx = context.WithCancel(ret.ctx)

	ret.defaultAttr.Fg = term.FromTcellColor(tcell.ColorWhite)
	ret.defaultAttr.Bg = term.FromTcellColor(tcell.ColorBlack)
	for _, option := range options {
		if err := option(ret); err != nil {
			return nil, fmt.Errorf("option: %w", err)
		}
	}
	if ret.processWindowClosed == nil {
		ret.processWindowClosed = func() []term.Event { return nil }
	}

	// clone original values for restoration
	ret.originalColorValues = tcell.GetColorValues()
	ret.originalValuesColor = tcell.GetValuesColor()

	if ret.initialTheme != "" {
		ret.setTheme(ret.initialTheme, ret.colorThemes[ret.initialTheme])
	}

	// initialze renderer, writer, etc.
	ret.resize(ret.defaultWidth, ret.defaultHeight, ret.fontManager.DeviceScale())

	return ret, nil
}

// Run starts the main loop and runs the graphical TUI with the specified options.
func (g *GUI) Run(title string) error {
	defer func() { _ = g.Close() }()

	ebiten.SetScreenClearedEveryFrame(false)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetRunnableOnUnfocused(true)
	// A frame only runs when an OS input event or ScheduleFrame arrives.
	// TPS must stay synced to frames: a finite TPS would quantize event
	// pickup back onto a tick grid.
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
	ebiten.SetTPS(ebiten.SyncWithFPS)

	ebiten.SetWindowPosition(g.startPositionX, g.startPositionY)
	width, height := g.defaultWidth, g.defaultHeight
	// On the first launch there is no stored size, so size the window to
	// the screen instead of the small built-in default.
	if !g.explicitSize {
		// ebiten.Monitor can be nil before the window is associated with a
		// monitor; only query its size when one is available.
		if m := ebiten.Monitor(); m != nil {
			if w, h := m.Size(); w > 0 && h > 0 {
				width, height = w, h
			}
		}
	}
	ebiten.SetWindowSize(width, height)

	if g.bgBlurRadius != 0 && g.enableTransparent {
		ebiten.SetWindowBackgroundBlur(g.bgBlurRadius)
	}
	ebiten.SetWindowDecorations(ebiten.DecorationsButtonsOnly)
	if g.closingHandled {
		ebiten.SetWindowClosingHandled(true)
	}

	gameOpts := g.buildRunGameOptions()
	return ebiten.RunGameWithOptions(g, &gameOpts)
}

// Close releases GUI-owned resources and restores process-global color state.
// It is safe to call more than once.
func (g *GUI) Close() error {
	g.closeOnce.Do(func() {
		g.cancelCtx()
		if g.renderer != nil {
			g.renderer.deallocate()
			g.renderer = nil
		}
		tcell.SetColorValues(g.originalColorValues)
	})
	return nil
}

// x11WMClass is the ICCCM WM_CLASS instance/class reported by the window.
// It must match StartupWMClass in deploy/rune-linux/rune.desktop so X11
// desktop environments (e.g. KDE Plasma) group the running window under the
// pinned launcher instead of showing a second, generic-icon task.
const x11WMClass = "rune"

func (g *GUI) buildRunGameOptions() ebiten.RunGameOptions {
	return ebiten.RunGameOptions{
		SingleThread:      true,
		ScreenTransparent: g.enableTransparent,
		X11ClassName:      x11WMClass,
		X11InstanceName:   x11WMClass,
	}
}

// Draw satisfies ebiten.Game. It renders the terminal GUI to the ebtien window.
func (g *GUI) Draw(screen *ebiten.Image) {
	if !g.needsRender {
		return
	}
	screen.Clear()
	cells := g.writer.RawCells()
	if g.links.armed() {
		cells = g.links.overlay(cells)
	}
	g.renderer.Draw(screen, cells, g.writer.Images(), g.cursor.show,
		g.cursor.pos, g.cursor.style, float64(g.renderOffset.X),
		float64(g.renderOffset.Y))
	g.needsRender = false
	if g.printFPS {
		ebitenutil.DebugPrint(screen, fmt.Sprintf("FPS: %0.2f", ebiten.ActualFPS()))
	}
}

// NeedsRender reports whether the next Draw will repaint the screen,
// i.e. the handler produced new content since the last render. It is
// false on an idle frame that Draw would early-out. Draw clears the flag
// after repainting.
func (g *GUI) NeedsRender() bool {
	return g.needsRender
}

// SetForceFullRepaint selects whether Draw repaints the entire retained frame.
// Changing the mode schedules a render so callers can compare strategies for
// the current cell buffer without dispatching another handler event.
func (g *GUI) SetForceFullRepaint(force bool) {
	g.forceFullRepaint = force
	if g.renderer != nil {
		g.renderer.forceFullRepaint = force
	}
	g.needsRender = true
}

// PublishEvent enqueues ev for the next frame and wakes the run loop.
// A client interrupt (a bare EventInterrupt that only asks for a
// redraw of asynchronously refreshed content) is collapsed onto an
// atomic flag instead of the channel so bursts of them cost no channel
// traffic; Update folds it into a single repaint. All other events,
// including interrupts carrying a Raw payload or UserFunc, keep their
// ordered delivery through the channel.
func (g *GUI) PublishEvent(ev term.Event) bool {
	if ev.Type == term.EventInterrupt && ev.Raw == nil && ev.UserFunc == nil {
		g.interruptPending.Store(true)
		g.scheduleFrame()
		return true
	}
	select {
	case g.updateChan <- ev:
		g.scheduleFrame()
		return true
	default:
		return false
	}
}

// scheduleFrame wakes the run loop. Before ebiten's first Game callback
// the UI is still being set up on the main thread and the wake-up traps
// inside GLFW; the first frame picks up whatever was published.
func (g *GUI) scheduleFrame() {
	if !g.started.Load() {
		return
	}
	ebiten.ScheduleFrame()
}

// awaitEchoInterrupt reports whether an interrupt was published within
// the echo budget. It sleeps in short slices because interrupts arrive
// on an atomic flag with no signalling channel; with vsync the sleep
// shifts Draw later within the same frame rather than delaying the
// present.
func (g *GUI) awaitEchoInterrupt() bool {
	deadline := time.Now().Add(echoWaitBudget)
	for !g.interruptPending.Load() {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(echoPollInterval)
	}
	return true
}

// Update satisfies ebiten.Game. It's called every time a new frame is to be scheduled.
func (g *GUI) Update() error {
	g.started.Store(true)
	g.pendingEvents = append(g.pendingEvents, g.mouse.processMouse()...)
	g.pendingEvents = g.input.processEvents(g.pendingEvents)
	g.pendingEvents = append(g.pendingEvents, g.processWindowClosed()...)
	needsDraw := g.needsDraw

	// Underlines must appear and disappear on the modifier alone, with no
	// mouse movement and no handler event, so a transition renders by itself.
	if g.links.setMeta(g.input.metaHeld()) {
		g.needsRender = true
	}
	if g.links.armed() {
		g.links.pointAt(g.mouse.clampedCoordinates(), g.writer.RawCells())
	}

	interruptPending := g.interruptPending.Swap(false)
	for len(g.updateChan) > 0 {
		g.pendingEvents = append(g.pendingEvents, <-g.updateChan)
	}
	if interruptPending {
		g.pendingEvents = append(g.pendingEvents, term.Event{Type: term.EventInterrupt})
		if g.prevTickKey {
			g.echoLikely = true
		}
	}

	// The loop wakes on every vsync whether or not anything happened.
	// Taking the UI lock to do nothing would contend, for no benefit,
	// with the extension RPC goroutines that need it, and the loop holds
	// it for the whole tick. An empty queue means no interrupt was
	// pending either, so no draw and no echo wait can be owed.
	if len(g.pendingEvents) == 0 && !needsDraw && g.drag.idle() {
		g.prevTickKey = false
		g.iteration++
		return nil
	}

	// set context with default iteration
	ctx := tui.ContextWithIteration(g.ctx, g.iteration)

	g.mu.Lock()
	defer g.mu.Unlock()

	// protect drag callback handler with mutex
	// so it can safely update UI state
	needsDraw = g.drag.poll() || needsDraw

	keyEvents := 0
	sawInterrupt := interruptPending
	for _, ev := range g.pendingEvents {
		switch ev.Type {
		case term.EventInterrupt:
			sawInterrupt = true
			if ev.UserFunc != nil {
				ev.UserFunc()
				needsDraw = true
				continue
			}
			var payloadCtx context.Context
			if ev.Raw == nil {
				payloadCtx = context.Background() // no iterationID
			} else {
				if id, ok := tui.IterationFromRawBytes(ev.Raw); ok {
					// override writer context with a specific iteration ID
					// that might not be this tick's iteration ID
					payloadCtx = tui.ContextWithIteration(g.ctx, id)
				} else {
					// override writer context with a user-payload
					payloadCtx = term.ContextWithPayload(g.ctx, ev.Raw)
				}
			}
			needsDraw = false
			g.drawHandler(payloadCtx)
		case term.EventError, term.EventResize:
			/* not dispatched by GUI */
		case term.EventMouse:
			if g.links.handleMouse(ev, g.writer.RawCells()) {
				continue
			}
			fallthrough
		default:
			if ev.Type == term.EventKey {
				keyEvents++
			}
			needsDraw = true
			exit, _ := g.handler.Handle(ev)
			if exit {
				if ebiten.IsFocused() {
					g.lastPositionX, g.lastPositionY = ebiten.WindowPosition()
				}
				return ErrHandlerExited
			}
		}
	}
	g.prevTickKey = keyEvents == 1
	// A lone keystroke on an echoing handler misses its own frame by
	// microseconds: the echo arrives right after this loop. Waiting is
	// bounded per tick, never per event, so backlogged repeat bursts
	// cannot compound it (the per-event wait removed in ff378cf3af froze
	// under key repeat).
	if keyEvents == 1 && !sawInterrupt && g.echoLikely && len(g.updateChan) == 0 {
		if g.awaitEchoInterrupt() && g.interruptPending.Swap(false) {
			needsDraw = false
			g.drawHandler(context.Background())
		} else {
			g.echoLikely = false
		}
	}
	if needsDraw {
		g.drawHandler(ctx)
	}

	// Zero the slots before truncating: term.Event contains heap pointers
	// (Err, Raw, UserFunc, Context) and a bare [:0] reslice would otherwise
	// keep payloads from previously-buffered events reachable until overwritten.
	clear(g.pendingEvents)
	g.pendingEvents = g.pendingEvents[:0]
	g.iteration++
	return nil
}

// Layout satisfies ebiten.Game. It provides the terminal gui size in pixels.
func (g *GUI) Layout(width, height int) (int, int) {
	g.started.Store(true)
	s := g.fontManager.DeviceScale()

	// resize handler only if effective size has changed
	if g.width != width || g.height != height || g.deviceScale != s {
		if g.deviceScale != s {
			g.log(log.DebugLevel, "reloading font due to "+
				"device scale change: %f vs %f", g.deviceScale, s)
			// reloading font shouldn't really fail
			_ = g.fontManager.ReloadFont()
		}
		g.mu.Lock()
		g.resize(width, height, s)
		g.mu.Unlock()
	}

	return int(float64(width) * s), int(float64(height) * s)
}

// MinimizeWindow minimizes the window.
func (g *GUI) MinimizeWindow() {
	ebiten.MinimizeWindow()
}

// MaximizeWindow maximizes the window.
func (g *GUI) MaximizeWindow() {
	ebiten.MaximizeWindow()
}

// RestoreWindow restores the window from its maximized or minimized state.
func (g *GUI) RestoreWindow() {
	ebiten.RestoreWindow()
}

// SetFullscreen changes the current mode to fullscreen or not.
//
// When on, the GUI screen is automatically enlarged
// to fit with the monitor. The current scale value is ignored.
//
// On desktops, Ebitengine uses 'windowed' fullscreen mode, which doesn't change
// your monitor's resolution.
//
// SetFullscreen does nothing on macOS when the window is fullscreened
// natively by the macOS desktop instead of SetFullscreen(true).
func (g *GUI) SetFullscreen(fullscreen bool) {
	ebiten.SetFullscreen(fullscreen)
}

// SetWindowPosition sets the window position.
// The position is an offset from the upper-left corner of the current monitor,
// in device-independent pixels.
// It sets the original window position in fullscreen mode.
func (g *GUI) SetWindowPosition(x, y int) {
	ebiten.SetWindowPosition(x, y)
}

// SetWindowSize sets the window size,
// even if the application is in fullscreen mode,
// it will set the original window size.
//
// SetWindowSize panics if width or height is not a positive number.
func (g *GUI) SetWindowSize(width, height int) {
	ebiten.SetWindowSize(width, height)
}

// IncreaseFontSize increases the size of the rendered font,
// making the interface appear bigger.
func (g *GUI) IncreaseFontSize() error {
	err := g.fontManager.IncreaseSize()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseFontSize increases the size of the rendered font,
// making the interface appear bigger.
func (g *GUI) DecreaseFontSize() error {
	err := g.fontManager.DecreaseSize()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// IncreaseCellWidth increases the width of the rendered font cells.
func (g *GUI) IncreaseCellWidth() error {
	err := g.fontManager.IncreaseCellWidth()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseCellWidth decreases the width of the rendered font cells.
func (g *GUI) DecreaseCellWidth() error {
	err := g.fontManager.DecreaseCellWidth()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// IncreaseLineHeight increases the size of the rendered font,
// making the interface appear bigger.
func (g *GUI) IncreaseLineHeight() error {
	err := g.fontManager.IncreaseLineHeight()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseLineHeight increases the size of the rendered font,
// making the interface appear bigger.
func (g *GUI) DecreaseLineHeight() error {
	err := g.fontManager.DecreaseLineHeight()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// SetFont sets the font collection identified by the given family name.
// If family is set to an empty string, the default builtin font is used.
func (g *GUI) SetFont(family string) error {
	err := g.fontManager.SetFontByFamilyName(family)
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// SetTheme sets the color theme to be used in the next render iteration.
// The theme must have been passed via WithColorThemes option before, otherwise
// this function returns an error.
func (g *GUI) SetTheme(name string) (Theme, error) {
	defer g.resize(g.width, g.height, g.fontManager.DeviceScale())

	g.resetTheme()
	if name == "" {
		return Theme{}, nil
	}

	theme, ok := g.colorThemes[name]
	if !ok {
		return Theme{}, fmt.Errorf("unkown theme '%s'", name)
	}
	g.setTheme(name, theme)
	return theme, nil
}

// Theme returns the current theme. An empty string
// indicates that no theme is set, so the default color scheme,
// is used.
func (g *GUI) Theme() string {
	return g.theme
}

// Themes returns the list of themes configured via WithColorThemes.
func (g *GUI) Themes() []string {
	var themes []string
	for name := range g.colorThemes {
		themes = append(themes, name)
	}
	return themes
}

// SetOpacity sets the background and foreground opacity.
// It has no effect if WithTransparentBackground has not
// been passed as an option to this GUI.
func (g *GUI) SetOpacity(background, foreground float64) {
	g.bgOpacity = background
	g.fgOpacity = foreground
	g.resize(g.width, g.height, g.fontManager.DeviceScale())
}

// SetBackgroundBlur sets the background blur of the window.
// It has no effect until the opacity is changed to be < 1.
// It has no effect if WithTransparentBackground has not
// been passed as an option to this GUI.
func (g *GUI) SetBackgroundBlur(radius int) {
	// if user is trying to remove blur, it will naturally
	// try to set it to 0, but radius 0 is interpreted as no-op
	// by ebiten.
	if radius == 0 {
		radius = 1
	}
	ebiten.SetWindowBackgroundBlur(radius)
	g.resize(g.width, g.height, g.fontManager.DeviceScale())
}

// AvailableFontFamilies returns an iterator with the available
// font families on the system.
func (g *GUI) AvailableFontFamilies() (iterator.Iterator[string], error) {
	return g.fontManager.AvailableFontFamilies()
}

// Size returns the current window width and height in pixels.
func (g *GUI) Size() (width, height int) {
	return g.width, g.height
}

// LastPosition returns the last window position in pixels offset, before
// GUI exits. If GUI is still running, this method returns 0, 0.
func (g *GUI) LastPosition() (x, y int) {
	return g.lastPositionX, g.lastPositionY
}

func (g *GUI) drawHandler(ctx context.Context) {
	g.writer.SetContext(ctx)
	_ = g.writer.Clear(g.defaultAttr)
	g.handler.Draw(g.writer)
	g.cursor.pos, g.cursor.style, g.cursor.show = g.handler.Cursor()
	g.needsDraw = false
	g.needsRender = true
	g.links.invalidate()
}

func (g *GUI) resize(width, height int, deviceScale float64) {
	g.width = width
	g.height = height
	g.deviceScale = deviceScale

	cellsWidth := g.fontManager.CellsWidth(g.width)
	cellsHeight := g.fontManager.CellsHeight(g.height)
	g.log(log.DebugLevel, "resize: pixels width: %d, height: %d, "+
		"device scale %f; cells width: %d, height: %d",
		width, height, deviceScale, cellsWidth, cellsHeight)

	g.handler.Resize(cellsWidth, cellsHeight)
	g.mouse.resize(cellsWidth, cellsHeight)
	g.cellPixelSize.Store(uint64(math.Round(g.fontManager.PixelX(1)))<<32 |
		uint64(math.Round(g.fontManager.PixelY(1))))
	g.writer = newFrameWriter(g.ctx, cellsWidth, cellsHeight)
	if g.renderer != nil {
		g.renderer.deallocate()
	}
	g.renderer = newRenderer(g.width, g.height, g.deviceScale,
		g.fontManager, g.bgOpacity, g.fgOpacity, g.enableLigatures,
		g.cursorAttributes, g.defaultAttr)
	g.renderer.forceFullRepaint = g.forceFullRepaint
	g.needsDraw = true
}

// CellPixelSize reports the cell pitch in device pixels, which is what
// image placements are scaled by and what the kitty graphics protocol
// advertises to clients. It is zero before the first resize and safe
// to call from any goroutine.
func (g *GUI) CellPixelSize() (width, height int) {
	packed := g.cellPixelSize.Load()
	return int(packed >> 32), int(packed & 0xffffffff)
}

// CellRect returns the origin and size, in points relative to the
// window's content area, of the cell grid region starting at cell
// (x, y) and spanning width by height cells. Native overlays use it to
// align themselves with the grid.
func (g *GUI) CellRect(x, y, width, height int) (px, py, pw, ph float64) {
	scale := g.deviceScale
	if scale <= 0 {
		scale = 1
	}
	return g.fontManager.PixelX(x) / scale, g.fontManager.PixelY(y) / scale,
		g.fontManager.PixelX(width) / scale, g.fontManager.PixelY(height) / scale
}

func (g *GUI) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "gui",
	}).Logf(level, msg, args...)
}

func (g *GUI) resetTheme() {
	tcell.SetColorValues(g.originalColorValues)

	g.defaultAttr.Fg = term.FromTcellColor(tcell.ColorWhite)
	g.defaultAttr.Bg = term.FromTcellColor(tcell.ColorBlack)
	g.cursorAttributes = term.Attributes{Bg: term.FromTcellColor(tcell.ColorRed)}
}

func (g *GUI) setTheme(name string, theme Theme) {
	m := make(map[tcell.Color]int32, len(theme.Colors))
	for color, value := range theme.Colors {
		m[color] = value.Hex()
	}
	tcell.MergeColorValues(m)

	g.theme = name
	g.defaultAttr.Fg = term.FromTcellColor(theme.Foreground)
	g.defaultAttr.Bg = term.FromTcellColor(theme.Background)
	g.cursorAttributes = term.Attributes{Bg: term.FromTcellColor(theme.Cursor)}
}
