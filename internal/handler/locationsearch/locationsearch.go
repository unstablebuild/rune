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

// Package locationsearch provides a floating-window handler that
// composes a fuzzy-search list driven by an external command with a
// syntax-highlighted preview pane on top. Each list entry must be a
// stdout line in the form `path[:line[:col]]`; the focused entry's
// file is loaded and rendered above the list with the target line
// highlighted.
package locationsearch

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/search"
)

// Config configures the appearance and behavior of the locationsearch handler.
type Config struct {
	// ListConfig controls the embedded fuzzy-search list (colors, algo,
	// case sensitivity, interrupter, sync vs async). Required fields
	// (Interrupter) match search.ListConfig requirements.
	ListConfig search.ListConfig

	// PreviewAttr is the cell attributes overlaid on the line that
	// matches the focused entry's reference range.
	PreviewAttr term.Attributes

	// PreviewContextLines is the height of the preview pane above the
	// search list. Zero uses defaultPreviewContextLines.
	PreviewContextLines int

	// MinPreviewWidth is the minimum width the floating window will
	// request via Dimensions. Zero uses defaultMinPreviewWidth.
	MinPreviewWidth int

	// SeparatorHeight is the number of rows reserved for the separator
	// between preview and list. Zero uses defaultSeparatorHeight.
	SeparatorHeight int

	// MaxListHeight caps the list portion in Dimensions. Zero uses
	// defaultMaxListHeight.
	MaxListHeight int

	// HistoryDocumentID and HistoryKey control history persistence
	// inside the inner finder. Empty HistoryDocumentID disables
	// history.
	HistoryDocumentID string
	HistoryKey        term.KeyComb

	// MaxHistory caps the number of entries persisted in history when
	// HistoryDocumentID is set. Zero uses the finder default.
	MaxHistory int

	// Source, when set, lists the entries instead of a command.
	Source func(ctx context.Context) (iterator.Iterator[string], error)

	// Annotate, when set, describes an entry beyond its location; see
	// Annotation. It is called on every draw with the focused entry, or
	// "" when no entry is focused, so it must be cheap. Setting it adds
	// a status bar under the list.
	Annotate func(entry string) Annotation

	// Label leads the status bar, as an editor's status bar leads with
	// its mode.
	Label string

	// TermAttr is overlaid on an entry's terms in the preview.
	TermAttr term.Attributes
}

// Annotation is what Config.Annotate knows about an entry beyond its
// location.
type Annotation struct {
	// Status follows the label in the status bar, and Detail is set
	// against the bar's right end. Only their first lines are shown,
	// and Status is cut short where they would meet.
	Status, Detail string
	// Error replaces the label with an ERROR one, in red.
	Error bool
	// First and Last are the lines, from 1, that the entry spans in its
	// file; zero when it spans none. The entry's own line is then
	// highlighted in the preview whether its location has a column or
	// not.
	First, Last int
	// Terms are emphasized, regardless of case, in the lines the entry
	// spans.
	Terms []string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		PreviewAttr:         term.Attributes{Attrs: term.AttrReverse},
		PreviewContextLines: defaultPreviewContextLines,
		MinPreviewWidth:     defaultMinPreviewWidth,
		SeparatorHeight:     defaultSeparatorHeight,
		MaxListHeight:       defaultMaxListHeight,
		TermAttr:            term.Attributes{Attrs: term.AttrBold | term.AttrUnderline},
	}
}

const (
	defaultPreviewContextLines = 21
	defaultMinPreviewWidth     = 80
	defaultMaxListHeight       = 15
	defaultSeparatorHeight     = 1
)

// Handler wraps a fuzzy-search list with a syntax-highlighted preview
// pane and implements browserapi.Floating so it can be installed
// directly via WindowManager.Floating.
type Handler struct {
	inner finder.RedispatchHandler
	// innerSpan wraps inner with horizontal padding so the
	// preview, separator and search bar all share the same
	// horizontal margin, and so the rightmost cell of the search
	// bar row is free for the scan-progress animation.
	innerSpan *sdkhandler.Span
	fs        workspaceapi.FileSystem
	parser    syntaxapi.Parser
	schedule  func(func()) bool
	log       *slog.Logger

	cfg Config

	// layout state
	innerW, innerH int
	previewH       int
	listH          int
	// statusH is the height of the status bar: none without Annotate.
	statusH int

	// preview state
	previewCells [][]term.Cell
	// previewW is the widest line of every preview shown so far: the
	// picker grows to fit a wider file but never narrows back, which
	// would jerk it about as the focus moves through the list.
	previewW     int
	prevLine     string
	targetLine   int
	targetStartX int
	targetEndX   int
	// highlightTargetLine indicates whether the focused entry's
	// location included a column. When false the preview pane shows
	// the file but does not visually highlight the target line, since
	// without a column the target defaults to the first line and a
	// reverse-video bar would be misleading.
	highlightTargetLine bool

	// scanRunning is true while the inner finder's scanData
	// goroutine is still streaming results. While true, Draw
	// renders a scan-progress component.Animation in the
	// rightmost cell of the search-bar row, which sits inside
	// the horizontal padding reserved by innerSpan and so does
	// not collide with the inner finder's match counter. Once
	// the inner ScanDone channel closes the watcher goroutine
	// flips scanRunning to false and closes the animation.
	scanRunning   atomic.Bool
	scanAnim      *component.Animation
	scanWatchStop func()
	scanWatchDone chan struct{}
}

var _ browserapi.Floating = (*Handler)(nil)

// New constructs a locationsearch handler that drives an inner
// fuzzy-search list with `command` and overlays a syntax-highlighted
// preview pane above it. Each stdout line of `command` is parsed as
// `path[:line[:col]]` for both the list entries and the preview
// target. `parser` is optional; when nil the preview pane shows
// uncolored cells. `command` must be empty when cfg.Source lists the
// entries instead.
func New(
	ctx context.Context,
	clients finder.Clients,
	invokeWindow browserapi.Window,
	cfg Config,
	parser syntaxapi.Parser,
	scheduleNextTick func(func()) bool,
	command string,
	log *slog.Logger,
) (*Handler, error) {
	cfg = applyDefaults(cfg)
	if log == nil {
		log = slog.Default()
	}
	var source func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error)
	if cfg.Source != nil {
		if command != "" {
			return nil, errors.New("locationsearch: a command and a Source are exclusive")
		}
		source = func(_ workspaceapi.FileSystem, ctx context.Context) (iterator.Iterator[string], error) {
			return cfg.Source(ctx)
		}
	}

	h := &Handler{
		fs:       clients.FileSystem,
		parser:   parser,
		schedule: scheduleNextTick,
		log:      log,
		cfg:      cfg,
	}
	if cfg.Annotate != nil {
		h.statusH = statusBarHeight
	}

	getResource := func(fs workspaceapi.FileSystem, line string) (
		workspaceapi.URI, term.Coordinates, bool,
	) {
		return parseLocationLine(fs, line)
	}

	maxHistory := cfg.MaxHistory
	if maxHistory == 0 {
		maxHistory = defaultMaxHistory
	}
	listCfg := cfg.ListConfig
	if listCfg.Interrupter == nil {
		listCfg.Interrupter = clients.Interrupter
	}
	rh, err := finder.NewWithListConfig(ctx, clients, invokeWindow,
		cfg.HistoryKey, cfg.HistoryDocumentID, command, maxHistory,
		listCfg, source, getResource)
	if err != nil {
		return nil, err
	}
	h.inner = rh
	h.innerSpan = sdkhandler.NewSpan(rh, component.SpanConfig{
		PadHorizontal:    spanHorizontalPad,
		ContentAlignment: component.AlignmentCentered,
	})
	h.startScanSpinner(clients.Interrupter)
	return h, nil
}

// Resize positions the preview pane and forwards the residual list
// area to the inner fuzzy-search handler.
func (h *Handler) Resize(w, height int) {
	h.innerW = w
	h.innerH = height
	h.previewH = clampPreviewHeight(h.cfg.PreviewContextLines,
		height-1-h.cfg.SeparatorHeight-h.statusH)
	h.listH = max(1, height-h.previewH-h.cfg.SeparatorHeight-h.statusH)
	h.innerSpan.Resize(w, h.listH)
}

// Draw refreshes the preview from the focused selection then renders
// the preview, separator, inner list and status bar into w.
func (h *Handler) Draw(w term.Writer) {
	// The list keeps at least one row even when the preview, the
	// separator and the status bar already fill the handler: truncate
	// instead of drawing past it.
	w = &component.VirtualWriter{Writer: w, Width: h.innerW, Height: h.innerH}
	sel, _ := h.inner.Selection()
	if sel != h.prevLine {
		h.loadPreview(sel)
	}
	var ann Annotation
	if h.cfg.Annotate != nil {
		ann = h.cfg.Annotate(sel)
	}
	h.drawPreview(w, ann)
	h.drawSeparator(w)
	innerOffset := term.Coordinates{Y: h.previewH + h.cfg.SeparatorHeight}
	innerWriter := &component.VirtualWriter{
		Writer: w,
		Offset: innerOffset,
		Width:  h.innerW,
		Height: h.listH,
	}
	h.innerSpan.Draw(innerWriter)
	if h.scanRunning.Load() && h.scanAnim != nil &&
		h.innerW > scanSpinnerWidth {
		spinnerWriter := &component.VirtualWriter{
			Writer: w,
			Offset: term.Coordinates{
				X: h.innerW - scanSpinnerWidth,
				Y: innerOffset.Y,
			},
			Width:  scanSpinnerWidth,
			Height: 1,
		}
		h.scanAnim.Resize(scanSpinnerWidth, 1)
		h.scanAnim.Draw(spinnerWriter)
	}
	h.drawStatusBar(w, ann)
}

// Handle delegates events to the inner fuzzy-search handler.
func (h *Handler) Handle(ev term.Event) (exit, handled bool) {
	return h.innerSpan.Handle(ev)
}

// Cursor returns the cursor position from the inner search input.
func (h *Handler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	c, style, show := h.innerSpan.Cursor()
	c.Y += h.previewH + h.cfg.SeparatorHeight
	return c, style, show
}

// Selection returns the currently focused list entry.
func (h *Handler) Selection() (string, bool) { return h.innerSpan.Selection() }

// Close releases the inner finder. The window is left to its owner,
// since it may already show other content.
func (h *Handler) Close() error {
	h.stopScanSpinner()
	return h.inner.Close()
}

// Dimensions returns the preferred floating-window size.
func (h *Handler) Dimensions() (int, int) {
	return max(h.cfg.MinPreviewWidth, h.previewW),
		h.cfg.MaxListHeight + h.cfg.PreviewContextLines + h.cfg.SeparatorHeight + h.statusH
}

func applyDefaults(cfg Config) Config {
	if cfg.PreviewContextLines == 0 {
		cfg.PreviewContextLines = defaultPreviewContextLines
	}
	if cfg.MinPreviewWidth == 0 {
		cfg.MinPreviewWidth = defaultMinPreviewWidth
	}
	if cfg.SeparatorHeight == 0 {
		cfg.SeparatorHeight = defaultSeparatorHeight
	}
	if cfg.MaxListHeight == 0 {
		cfg.MaxListHeight = defaultMaxListHeight
	}
	return cfg
}

func clampPreviewHeight(want, max int) int {
	if max < 0 {
		return 0
	}
	if want > max {
		return max
	}
	if want < 0 {
		return 0
	}
	return want
}

const defaultMaxHistory = 2000

// scanSpinnerFPS is the redraw cadence of the scan-progress
// component.Animation drawn in the freed top-right cell of the
// inner finder's search-bar row.
const scanSpinnerFPS = 10

// scanSpinnerWidth is the number of columns reserved on the right
// edge of the inner finder for the scan-progress animation.
// component.ProgressAnimationFrames are 1 cell wide.
const scanSpinnerWidth = 1

// spanHorizontalPad is the total horizontal padding added by
// innerSpan around the inner finder. With ContentAlignment
// AlignmentCentered this is split evenly so the inner finder
// is offset by spanHorizontalPad/2 cells from the left and right
// edges. The right-edge padding is also where the scan-progress
// animation is drawn during scanning.
const spanHorizontalPad = 2

// statusBarHeight is the height of the status bar under the list.
const statusBarHeight = 1

// The status bar looks like an editor's: a gray bar led by a label that
// shades into it.
var (
	statusBarAttr  = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorGray}
	labelAttr      = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlue, Attrs: term.AttrBold}
	errorLabelAttr = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorRed, Attrs: term.AttrBold}
)

const (
	errorLabel = "ERROR"
	labelShade = "█▓▒░"
)

// drawStatusBar draws ann on the last row, which a GUI shifts down half
// a cell onto the frame, as it does an editor's status bar.
func (h *Handler) drawStatusBar(w term.Writer, ann Annotation) {
	y := h.innerH - h.statusH
	if h.statusH == 0 || y < 0 {
		return
	}
	for x := range h.innerW {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(' ', 1, statusBarAttr))
	}
	label, attr := h.cfg.Label, labelAttr
	if ann.Error {
		label, attr = errorLabel, errorLabelAttr
	}
	x := 0
	if label != "" {
		x = writeLine(w, x, y, h.innerW, "  "+label+" ", attr)
		x = writeLine(w, x, y, h.innerW, labelShade, term.Attributes{Fg: attr.Bg, Bg: statusBarAttr.Bg})
	}
	detailX := h.innerW
	if detail := lineWidth(ann.Detail); detail > 0 {
		detailX = max(x, h.innerW-detail-1)
	}
	writeLine(w, x, y, detailX-1, " "+ann.Status, statusBarAttr)
	writeLine(w, detailX, y, h.innerW, ann.Detail, statusBarAttr)
	offset := term.Attributes{Attrs: term.AttrVerticalRenderOffset}
	for x := range h.innerW {
		w.UnionAttributes(term.Coordinates{X: x, Y: y}, offset)
	}
}

// writeLine writes the first line of s in attr from x on row y, cut
// short with an ellipsis where it would reach end, and returns the
// column after it.
func writeLine(w term.Writer, x, y, end int, s string, attr term.Attributes) int {
	rows := term.StringToCells(s)
	if len(rows) == 0 {
		return x
	}
	limit := end
	if x+lineWidth(s) > end {
		limit = end - 1
	}
	for _, c := range rows[0] {
		cw := max(1, int(c.Width))
		if x+cw > limit {
			break
		}
		c.SetAttributes(attr)
		w.SetCell(term.Coordinates{X: x, Y: y}, c)
		x += cw
	}
	if limit < end && x < end {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('…', 1, attr))
		x++
	}
	return x
}

// lineWidth is how many cells the first line of s takes.
func lineWidth(s string) int {
	rows := term.StringToCells(s)
	if len(rows) == 0 {
		return 0
	}
	n := 0
	for _, c := range rows[0] {
		n += max(1, int(c.Width))
	}
	return n
}

// startScanSpinner constructs a scanSpinnerFPS component.Animation
// driven by clients.Interrupter, then spawns a watcher goroutine
// that flips scanRunning to false (and closes the animation) once
// the inner finder's ScanDone channel closes. If the inner finder
// does not implement ScanWaiter, or the scan is already complete
// when New returns (e.g. SyncSearch tests), no spinner is started.
func (h *Handler) startScanSpinner(interrupter term.Interrupter) {
	waiter, ok := h.inner.(finder.ScanWaiter)
	if !ok {
		return
	}
	scanDone := waiter.ScanDone()
	select {
	case <-scanDone:
		return
	default:
	}
	if interrupter == nil {
		interrupter = term.NopInterrupter()
	}
	frames, seq := component.ProgressAnimationFrames()
	h.scanAnim = component.NewAnimation(interrupter, frames, seq, scanSpinnerFPS)
	h.scanRunning.Store(true)

	stopCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.scanWatchStop = stop
	h.scanWatchDone = done
	go debug.CapturePanicReport(func() {
		defer close(done)
		select {
		case <-stopCtx.Done():
		case <-scanDone:
		}
		h.scanRunning.Store(false)
		if h.scanAnim != nil {
			_ = h.scanAnim.Close()
		}
	})
}

// stopScanSpinner cancels the watcher goroutine started by
// startScanSpinner and waits for it to exit. Safe to call multiple
// times.
func (h *Handler) stopScanSpinner() {
	if h.scanWatchStop == nil {
		return
	}
	h.scanWatchStop()
	<-h.scanWatchDone
	h.scanWatchStop = nil
	h.scanWatchDone = nil
}

func isPositiveInt(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// parseLocationLine parses lines emitted by grep-style tools in the
// form `path`, `path:LINE`, `path:LINE:COL`, or `path:LINE:COL:rest`,
// where `rest` is matched content that we ignore. Line/col are
// 1-indexed in input and converted to 0-indexed in term.Coordinates.
//
// The first ':'-separated segment that parses as a positive integer
// is treated as the line number; the segment to the left (joined by
// `:`) is the path. If the segment after the line is also a positive
// integer it is treated as the column. Everything after is ignored.
// This makes the parser tolerant of `git grep -n --column` style
// output like `path:line:col:matched line content` and of
// Windows-style paths like `C:\foo\bar:42:5`.
func parseLocationLine(fs workspaceapi.FileSystem, line string) (
	workspaceapi.URI, term.Coordinates, bool,
) {
	p := parseLocationLineDetailed(fs, line)
	return p.uri, p.coords, p.ok
}

// parsedLocationLine is the detailed result of parsing a location line.
// It distinguishes whether a column was present so callers can decide
// to visually highlight the target line only when the input was
// specific enough to point at a column.
type parsedLocationLine struct {
	uri       workspaceapi.URI
	coords    term.Coordinates
	hasColumn bool
	ok        bool
}

func parseLocationLineDetailed(fs workspaceapi.FileSystem, line string) parsedLocationLine {
	s := strings.TrimSpace(line)
	if s == "" {
		return parsedLocationLine{}
	}

	parts := strings.Split(s, ":")
	lineIdx := -1
	for i := 1; i < len(parts); i++ {
		if n, err := strconv.Atoi(parts[i]); err == nil && n > 0 {
			lineIdx = i
			break
		}
	}

	var path string
	lineNum, col := 0, 0
	hasColumn := false
	if lineIdx < 0 {
		path = s
	} else {
		path = strings.Join(parts[:lineIdx], ":")
		lineNum, _ = strconv.Atoi(parts[lineIdx])
		if lineIdx+1 < len(parts) && isPositiveInt(parts[lineIdx+1]) {
			col, _ = strconv.Atoi(parts[lineIdx+1])
			hasColumn = true
		}
	}
	if path == "" {
		return parsedLocationLine{}
	}

	uri, err := fs.URI(path)
	if err != nil {
		return parsedLocationLine{}
	}
	coords := term.Coordinates{}
	if lineNum > 0 {
		coords.Y = lineNum - 1
	}
	if col > 0 {
		coords.X = col - 1
	}
	return parsedLocationLine{
		uri:       uri,
		coords:    coords,
		hasColumn: hasColumn,
		ok:        true,
	}
}
