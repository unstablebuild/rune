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

package text

import (
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/template"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/vctrl"
)

// StatusBarComponentType is one of the many supported
// components by the status bar.
type StatusBarComponentType uint8

// StatusBarComponent represents a component to be
// rendered by the status bar.
type StatusBarComponent struct {
	Type       StatusBarComponentType
	Template   string
	Attributes term.Attributes
	// Image is set for StatusBarImage components only.
	Image template.Image
}

const (
	// StatusBarVoid is used to separate left-aligned components
	// from right-aligned thus it indicates that after this component
	// the rest of components will be aligned towards the right.
	StatusBarVoid StatusBarComponentType = iota
	// StatusBarStatus is the status message printed by the editor.
	StatusBarStatus
	// StatusBarFilePath is the relative file name of the file.
	StatusBarFilePath
	// StatusBarGitShortRef is the git short reference pointed at by HEAD.
	StatusBarGitShortRef
	// StatusBarGitDiffAdded renders the lines added.
	StatusBarGitDiffAdded
	// StatusBarGitDiffDeleted renders the lines deleted.
	StatusBarGitDiffDeleted
	// StatusBarDiagnosticsError renders the LSP errors.
	StatusBarDiagnosticsError
	// StatusBarDiagnosticsWarning renders the LSP warnings.
	StatusBarDiagnosticsWarning
	// StatusBarDiagnosticsInfo renders the LSP informational diagnostics.
	StatusBarDiagnosticsInfo
	// StatusBarLanguage is a language icon that indicates the status of the syntax.
	StatusBarLanguage
	// StatusBarCoordinatesCursorX is the cursor x coordinates.
	StatusBarCoordinatesCursorX
	// StatusBarCoordinatesCursorY is the cursor y coordinates.
	StatusBarCoordinatesCursorY
	// StatusBarTotalLines is the total lines in the file.
	StatusBarTotalLines
	// StatusBarImage is an image drawn over the bar. Its Template
	// surrounds the cells it reserves.
	StatusBarImage
)

// StatusBarConfig holds configuration for the status bar created
// by WithStatusBar.
type StatusBarConfig struct {
	ScheduleNextTick func(func()) bool
	Workspace        workspaceapi.URI
	Publisher        EventPublisher

	Layout          []StatusBarComponent
	BackgroundColor term.Color
	ErrorColor      term.Color
	GitService      vctrl.Service
	// Storage caches the images the layout downloads. It is required
	// when Layout has a StatusBarImage component, and is used as-is: the
	// caller owns its partitioning and lifetime.
	Storage storageapi.Service
	// Interrupter is signalled whenever an image of the layout changes
	// what it draws. It is required when Layout has a StatusBarImage
	// component.
	Interrupter term.Interrupter
	// Styles, when set, overrides Layout, BackgroundColor and ErrorColor
	// with its current style, and restyles the bar whenever it is
	// restyled, until the bar is closed.
	Styles *StatusBarStyles
}

// StatusBarStyle is the part of a StatusBarConfig that StatusBarStyles
// changes in the bars it styles.
type StatusBarStyle struct {
	// Layout is nil for the default layout.
	Layout          []StatusBarComponent
	BackgroundColor term.Color
	ErrorColor      term.Color
}

// StatusBarStyles is the style of the status bars created with it as
// their StatusBarConfig.Styles. Its methods must be called from the
// event loop.
type StatusBarStyles struct {
	style  StatusBarStyle
	images map[string]image.Image
	bars   map[*StatusBar]struct{}
}

// NewStatusBarStyles returns the styles that create bars with style.
func NewStatusBarStyles(style StatusBarStyle) *StatusBarStyles {
	return &StatusBarStyles{style: style, bars: make(map[*StatusBar]struct{})}
}

// Current returns the style bars are created with.
func (s *StatusBarStyles) Current() StatusBarStyle {
	return s.style
}

// Restyle draws the open bars with style from their next Draw on, and
// creates the later ones with it. images holds, by URI, the images of
// style's StatusBarImage components that are drawn from the first frame
// as given, without being fetched; the others load as they do in a new
// bar. The open bars keep their status message, and what they last
// learned about their file.
func (s *StatusBarStyles) Restyle(style StatusBarStyle, images map[string]image.Image) {
	s.style, s.images = style, images
	for bar := range s.bars {
		bar.restyle(style, images)
	}
}

// WithStatusBar wraps the given editor with an git bar. The given buffer,
// and scroll should correspond to the buffer and scroll used by the given editor.
func WithStatusBar(
	handler Handler, buf *cell.Buffer, scroll *tcomponent.Scroll,
	readOnly, recovered bool, cfg StatusBarConfig,
) *StatusBar {
	if cfg.Publisher == nil || cfg.ScheduleNextTick == nil {
		panic("statusbar configuration is missing key dependencies")
	}
	var images map[string]image.Image
	if cfg.Styles != nil {
		style := cfg.Styles.style
		cfg.Layout, cfg.BackgroundColor, cfg.ErrorColor = style.Layout,
			style.BackgroundColor, style.ErrorColor
		images = cfg.Styles.images
	}
	ret := new(StatusBar)
	ret.buf = buf
	ret.scroll = scroll
	ret.Handler = handler
	ret.vhandler.C = handler
	ret.scheduleNextTick = cfg.ScheduleNextTick
	ret.svc = cfg.GitService
	ret.pub = cfg.Publisher
	ret.config = cfg
	ret.cancelBuild = func() {}
	ret.ctx, ret.cancelAll = context.WithCancel(context.Background())
	ret.readOnly = readOnly
	ret.recovered = recovered

	b := new(cell.Buffer)
	b.InitPerformance(1, 200, ' ')

	ret.status = component.NewFloatingReference(nil)

	ret.file = handler.Resource()
	ret.relpath = component.NewFloatingReference(nil)
	ret.rebuildFilename(workspaceapi.RelPath(cfg.Workspace, ret.file))
	ret.status = component.NewFloatingReference(nil)
	ret.gitShortRef = component.NewFloatingReference(nil)
	ret.gitAdd = component.NewFloatingReference(nil)
	ret.gitDel = component.NewFloatingReference(nil)
	ret.diagnosticsError = component.NewFloatingReference(nil)
	ret.diagnosticsInfo = component.NewFloatingReference(nil)
	ret.diagnosticsWarning = component.NewFloatingReference(nil)
	ret.syntaxState = component.NewFloatingReference(nil)
	ret.cursorX = component.NewFloatingReference(nil)
	ret.cursorY = component.NewFloatingReference(nil)
	ret.totalLines = component.NewFloatingReference(nil)

	ret.initLayout(cfg, images)
	ret.SetStatus("", term.Attributes{})

	buf.Subscribe((*statusBarSubscriber)(ret))
	evs := []textapi.EventType{textapi.EventTypeFlush}
	ret.dirty = true
	ret.lastFlush = buf.Version()
	_ = ret.pub.SubscribeEvents(evs, (*statusBarSubscriber)(ret))

	if ret.dirty {
		ret.rebuildBarAll()
	}
	if cfg.Styles != nil {
		cfg.Styles.bars[ret] = struct{}{}
	}

	return ret
}

// StatusBar provides information about the current file, editing mode,
// and other useful file-level details.
type StatusBar struct {
	Handler
	ctx              context.Context
	cancelAll        func()
	svc              vctrl.Service
	scheduleNextTick func(func()) bool
	config           StatusBarConfig
	pub              EventPublisher
	readOnly         bool
	recovered        bool

	file        workspaceapi.URI
	buf         *cell.Buffer
	scroll      *tcomponent.Scroll
	cancelBuild func()
	closed      bool
	dirty       bool
	vhandler    handler.Virtual[Handler]

	lastFlush     int
	hidden        bool
	prevCursor    term.Coordinates
	prevOffset    term.Coordinates
	width         int
	height        int
	barLeft       component.Virtual[component.Floating]
	barRight      component.Virtual[component.Floating]
	barLeftItems  []component.Floating
	barRightItems []component.Floating
	// images are in the order they are drawn: by z_index, then layout.
	images             []*template.ImageElement
	status             *component.FloatingReference
	relpath            *component.FloatingReference
	gitShortRef        *component.FloatingReference
	gitAdd             *component.FloatingReference
	gitDel             *component.FloatingReference
	diagnosticsError   *component.FloatingReference
	diagnosticsWarning *component.FloatingReference
	diagnosticsInfo    *component.FloatingReference
	syntaxState        *component.FloatingReference
	cursorX            *component.FloatingReference
	cursorY            *component.FloatingReference
	totalLines         *component.FloatingReference
	statusBarTemplates
	statusBarBuilt

	// What the bar last learned, which a restyle draws again.
	statusMessage   string
	statusAttrs     term.Attributes
	lastSyntax      syntax.State
	lastGit         statusBarGit
	syntaxStreaming bool
}

// statusBarTemplates are the components of the layout, by type.
type statusBarTemplates struct {
	statusTemplate             StatusBarComponent
	relpathTemplate            StatusBarComponent
	gitShortRefTemplate        StatusBarComponent
	gitAddTemplate             StatusBarComponent
	gitDelTemplate             StatusBarComponent
	diagnosticsErrorTemplate   StatusBarComponent
	diagnosticsWarningTemplate StatusBarComponent
	diagnosticsInfoTemplate    StatusBarComponent
	syntaxTemplate             StatusBarComponent
	cursorXTemplate            StatusBarComponent
	cursorYTemplate            StatusBarComponent
	totalLinesTemplate         StatusBarComponent
}

// statusBarBuilt is the built state of hideable components, used by
// doRebuildBar to restore components before re-evaluating overflow.
type statusBarBuilt struct {
	gitShortRefBuilt component.Floating
	gitAddBuilt      component.Floating
	gitDelBuilt      component.Floating
	syntaxStateBuilt component.Floating
	totalLinesBuilt  component.Floating
}

// statusBarGit is what the bar last learned from git.
type statusBarGit struct {
	known          bool
	err            bool
	shortRef       string
	added, deleted int
}

// ShowCommandBar satisfies text.Handler.
func (b *StatusBar) ShowCommandBar(show bool) {
	b.hidden = !show
	b.doRebuildBar()
}

// SetStatus sets the status message.
func (b *StatusBar) SetStatus(message string, attrs term.Attributes) {
	b.statusMessage, b.statusAttrs = message, attrs
	components := template.Build(b.statusTemplate.Template, message,
		attrs, b.statusTemplate.Attributes, b.config.BackgroundColor)
	status := component.Inline(components, component.AlignmentLeft)
	b.status.Init(status)
	b.doRebuildBar()
}

// Close unsubscribes this bar from all the file events and
// closes the underlying handler.
func (b *StatusBar) Close() (ret error) {
	if b.closed {
		return nil
	}
	b.cancelAll()
	b.closed = true
	if b.config.Styles != nil {
		delete(b.config.Styles.bars, b)
	}
	for _, img := range b.images {
		if err := img.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	ok, err := b.pub.UnsubscribeEvents((*statusBarSubscriber)(b))
	if err != nil {
		ret = multierror.Append(ret, err)
	} else if !ok {
		ret = multierror.Append(ret, errors.New("could not unsubscribe status bar"))
	}
	if err := b.vhandler.C.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	return
}

// SetCursorAtScroll wraps the underlying text.Handler.
func (b *StatusBar) SetCursorAtScroll(pos term.Coordinates) bool {
	defer b.rebuildBarCursor()
	return b.Handler.SetCursorAtScroll(pos)
}

// MoveToNextLocation wraps the underlying text.Handler.
func (b *StatusBar) MoveToNextLocation(ID string) bool {
	defer b.rebuildBarCursor()
	return b.Handler.MoveToNextLocation(ID)
}

// MoveToPrevLocation wraps the underlying text.Handler.
func (b *StatusBar) MoveToPrevLocation(ID string) bool {
	defer b.rebuildBarCursor()
	return b.Handler.MoveToPrevLocation(ID)
}

// Resize satisfies tui.Component.
func (b *StatusBar) Resize(width, height int) {
	b.width = width
	b.height = height
	barHeight := b.doRebuildBar()
	b.vhandler.Resize(width, height-barHeight)
}

// Selection satisfies tui.Handler.
func (b *StatusBar) Selection() (string, bool) {
	return b.vhandler.Selection()
}

// Cursor satisfies tui.Handler.
func (b *StatusBar) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return b.vhandler.Cursor()
}

// Draw satisfies tui.Component.
func (b *StatusBar) Draw(w term.Writer) {
	b.vhandler.Draw(w)
	shifted := b.barRight.Height() != 0
	if b.config.BackgroundColor != term.ColorDefault && shifted {
		attrs := term.Attributes{Bg: b.config.BackgroundColor}
		for x := range b.width {
			w.UnionAttributes(term.Coordinates{Y: b.height - 1, X: x}, attrs)
		}
	}
	b.barLeft.Draw(w)
	b.barRight.Draw(w)
	// Images go over the bar's text, which would cover them if it were
	// drawn after them. They may be placed partly outside the editor and
	// its bar, where they are cut off unless they overflow.
	if len(b.images) > 0 {
		iw := term.BoundsCheckWriter(b.width, b.height, w)
		for _, img := range b.images {
			img.DrawImage(iw, shifted)
		}
	}
	if shifted {
		attrs := term.Attributes{Attrs: term.AttrVerticalRenderOffset}
		for x := range b.width {
			w.UnionAttributes(term.Coordinates{Y: b.height - 1, X: x}, attrs)
		}
	}
}

// Buffer is a helper for tests.
func (b *StatusBar) Buffer() *cell.Buffer {
	return b.buf
}

// Handle satisfies tui.Handler.
func (b *StatusBar) Handle(ev term.Event) (quit, handled bool) {
	mouse := ev.Type == term.EventMouse && ev.Key == term.MouseLeft &&
		ev.MouseY >= b.vhandler.Height() && ev.MouseY < b.vhandler.Height()+1
	if !mouse {
		quit, handled = b.vhandler.Handle(ev)
		prevCursor := b.prevCursor
		prevOffset := b.prevOffset
		b.prevOffset = b.scroll.Offset()
		b.prevCursor, _, _ = b.vhandler.Cursor()
		if prevCursor != b.prevCursor || prevOffset != b.prevOffset {
			b.rebuildBarCursor()
		}
		return
	}
	return
}

func (b *StatusBar) rebuildBarAll() {
	b.dirty = false
	b.rebuildBarEdit()
	b.rebuildBarCursor()
	b.rebuildBarSyntax(b.lastSyntax)
	b.rebuildBarFlush(context.Background())
	b.doRebuildBar()
}

// restyle rebuilds the bar with style, keeping its subscriptions and
// what it last learned.
func (b *StatusBar) restyle(style StatusBarStyle, images map[string]image.Image) {
	for _, img := range b.images {
		if err := img.Close(); err != nil {
			b.log(log.WarnLevel, "close status bar image: %v", err)
		}
	}
	b.images = nil
	b.statusBarTemplates = statusBarTemplates{}
	b.statusBarBuilt = statusBarBuilt{}
	b.config.Layout = style.Layout
	b.config.BackgroundColor = style.BackgroundColor
	b.config.ErrorColor = style.ErrorColor
	b.initLayout(b.config, images)
	b.SetStatus(b.statusMessage, b.statusAttrs)
	if b.lastGit.known {
		if b.lastGit.err {
			b.buildGitError()
		} else {
			b.buildGit(b.lastGit.shortRef, b.lastGit.added, b.lastGit.deleted)
		}
	}
	b.rebuildBarAll()
}

func (b *StatusBar) isCursorEnabled() bool {
	return b.cursorXTemplate.Type != 0 ||
		b.cursorYTemplate.Type != 0 ||
		b.totalLinesTemplate.Type != 0
}

func (b *StatusBar) rebuildBarCursor() {
	if !b.isCursorEnabled() {
		return
	}
	totalRows := b.buf.Rows()
	cursor := b.Handler.CursorAtScroll()

	components := template.Build(b.cursorXTemplate.Template,
		cursor.X+1, term.Attributes{}, b.cursorXTemplate.Attributes,
		b.config.BackgroundColor)
	b.cursorX.Init(component.Inline(components, component.AlignmentLeft))

	components = template.Build(b.cursorYTemplate.Template,
		cursor.Y+1, term.Attributes{}, b.cursorYTemplate.Attributes,
		b.config.BackgroundColor)
	b.cursorY.Init(component.Inline(components, component.AlignmentLeft))

	components = template.Build(b.totalLinesTemplate.Template,
		totalRows, term.Attributes{}, b.totalLinesTemplate.Attributes,
		b.config.BackgroundColor)
	b.totalLinesBuilt = component.Inline(components, component.AlignmentLeft)
	b.totalLines.Init(b.totalLinesBuilt)

	b.doRebuildBar()
}

func (b *StatusBar) diagnosticsEnabled() bool {
	return b.diagnosticsErrorTemplate.Type != 0 ||
		b.diagnosticsWarningTemplate.Type != 0 ||
		b.diagnosticsInfoTemplate.Type != 0
}

func (b *StatusBar) rebuildBarEdit() {
	b.rebuildFilename(workspaceapi.RelPath(b.config.Workspace, b.file))
	if !b.diagnosticsEnabled() {
		b.doRebuildBar()
		return
	}
	go debug.CapturePanicReport(func() {
		messages := calculateMessagesStats(b.Handler.LocationLists())
		b.scheduleNextTick(func() {
			b.buildMessages(messages)
			b.doRebuildBar()
		})
	})
}

func (b *StatusBar) rebuildBarSyntax(state syntax.State) {
	b.lastSyntax = state
	if state.LangID == "" {
		state.LangID = "unknown"
	}
	attrs := b.syntaxTemplate.Attributes
	if state.ParserError != "" {
		attrs.Fg = b.config.ErrorColor
		if b.syntaxTemplate.Attributes.Bg == b.config.ErrorColor {
			attrs.Fg = term.ColorRed
			if b.syntaxTemplate.Attributes.Bg == term.ColorRed {
				attrs.Fg = term.ColorYellow
			}
		}
	}
	components := template.Build(b.syntaxTemplate.Template, state.LangID,
		attrs, b.syntaxTemplate.Attributes,
		b.config.BackgroundColor)
	b.syntaxStateBuilt = component.Inline(components, component.AlignmentLeft)
	b.syntaxState.Init(b.syntaxStateBuilt)
	b.doRebuildBar()
}

func (b *StatusBar) gitEnabled() bool {
	return b.gitAddTemplate.Type != 0 ||
		b.gitDelTemplate.Type != 0 ||
		b.gitShortRefTemplate.Type != 0
}

func (b *StatusBar) rebuildBarFlush(ctx context.Context) {
	if !b.gitEnabled() {
		b.rebuildBarEdit()
		return
	}

	b.cancelBuild()
	ctx, b.cancelBuild = context.WithCancel(ctx)
	go debug.CapturePanicReport(func() {
		var err error
		diff, err := b.svc.Diff(ctx, b.file)
		if err != nil {
			b.log(log.DebugLevel, "compute diff: %v", err)
		}
		shortRef, err := b.svc.ShortRef(ctx, b.file)
		if err != nil {
			b.log(log.DebugLevel, "get short ref: %v", err)
		}
		added, deleted := calculateGitStats(diff)
		b.scheduleNextTick(func() {
			messages := calculateMessagesStats(b.Handler.LocationLists())
			select {
			case <-ctx.Done():
				return
			default:
			}
			b.rebuildFilename(workspaceapi.RelPath(b.config.Workspace, b.file))
			b.buildMessages(messages)
			if err != nil {
				b.buildGitError()
			} else {
				b.buildGit(shortRef, added, deleted)
			}
			b.lastGit = statusBarGit{
				known: true, err: err != nil, shortRef: shortRef,
				added: added, deleted: deleted,
			}
			b.doRebuildBar()
		})
	})
}

func (b *StatusBar) buildMessages(messages map[term.Attributes]int) {
	/* TODO with LSP */
}

func (b *StatusBar) buildGit(shortRef string, added, deleted int) {
	components := template.Build(b.gitShortRefTemplate.Template, shortRef,
		term.Attributes{}, b.gitShortRefTemplate.Attributes,
		b.config.BackgroundColor)
	b.gitShortRefBuilt = component.Inline(components, component.AlignmentLeft)
	b.gitShortRef.Init(b.gitShortRefBuilt)

	components = template.Build(b.gitAddTemplate.Template, added,
		term.Attributes{}, b.gitAddTemplate.Attributes,
		b.config.BackgroundColor)
	b.gitAddBuilt = component.Inline(components, component.AlignmentLeft)
	b.gitAdd.Init(b.gitAddBuilt)

	components = template.Build(b.gitDelTemplate.Template, deleted,
		term.Attributes{}, b.gitDelTemplate.Attributes,
		b.config.BackgroundColor)
	b.gitDelBuilt = component.Inline(components, component.AlignmentLeft)
	b.gitDel.Init(b.gitDelBuilt)
}

func (b *StatusBar) buildGitError() {
	components := template.Build(b.gitShortRefTemplate.Template, "untracked",
		term.Attributes{Fg: term.ColorYellow},
		b.gitShortRefTemplate.Attributes, b.config.BackgroundColor)
	b.gitShortRefBuilt = component.Inline(components, component.AlignmentLeft)
	b.gitShortRef.Init(b.gitShortRefBuilt)
	b.gitAddBuilt = nil
	b.gitAdd.Init(nil)
	b.gitDelBuilt = nil
	b.gitDel.Init(nil)
}

func (b *StatusBar) rebuildFilename(filename string) {
	attrs := b.relpathTemplate.Attributes
	var path string
	flushed := b.buf.Version() == b.lastFlush
	switch {
	case !b.recovered && flushed && !b.readOnly:
		path = fmt.Sprintf("%s   ", filename)
	case b.recovered && flushed && !b.readOnly:
		path = fmt.Sprintf("%s 󱄋", filename)
	case flushed && b.readOnly:
		path = fmt.Sprintf("%s 󰌾", filename)
	case !flushed && !b.readOnly:
		path = fmt.Sprintf("%s ", filename)
		attrs.Fg = term.ColorOlive
		if attrs.Bg == term.ColorOlive {
			attrs.Fg = term.ColorYellow
		}
	case !flushed && b.readOnly:
		path = fmt.Sprintf("%s 󰗻", filename)
		attrs.Fg = term.ColorRed
		if attrs.Bg == term.ColorRed {
			attrs.Fg = term.ColorOlive
		}
	}
	components := template.Build(b.relpathTemplate.Template,
		path, attrs, b.relpathTemplate.Attributes, b.config.BackgroundColor)
	b.relpath.Init(component.Inline(components, component.AlignmentLeft))
}

func (b *StatusBar) doRebuildBar() int {
	barHeight := 1
	if b.height < 1 || b.hidden {
		barHeight = 0
	}

	// When the file path is in the layout, progressively hide
	// other components instead of truncating the path.
	// Priority: git branch → git stats → language → total lines.
	if b.relpathTemplate.Type != 0 {
		// Restore all hideable components from their built state so we
		// re-evaluate what needs to be hidden at the current width.
		b.gitShortRef.Init(b.gitShortRefBuilt)
		b.gitAdd.Init(b.gitAddBuilt)
		b.gitDel.Init(b.gitDelBuilt)
		b.syntaxState.Init(b.syntaxStateBuilt)
		b.totalLines.Init(b.totalLinesBuilt)
	}

	barLeftWidth, _ := b.barLeft.C.Dimensions()
	barRightWidth, _ := b.barRight.C.Dimensions()

	if b.relpathTemplate.Type != 0 && barLeftWidth+barRightWidth > b.width && b.width > 0 {
		if barLeftWidth+barRightWidth > b.width {
			b.gitShortRef.Init(nil)
			barLeftWidth, _ = b.barLeft.C.Dimensions()
			barRightWidth, _ = b.barRight.C.Dimensions()
		}
		if barLeftWidth+barRightWidth > b.width {
			b.gitAdd.Init(nil)
			b.gitDel.Init(nil)
			barLeftWidth, _ = b.barLeft.C.Dimensions()
			barRightWidth, _ = b.barRight.C.Dimensions()
		}
		if barLeftWidth+barRightWidth > b.width {
			b.syntaxState.Init(nil)
			barLeftWidth, _ = b.barLeft.C.Dimensions()
			barRightWidth, _ = b.barRight.C.Dimensions()
		}
		if barLeftWidth+barRightWidth > b.width {
			b.totalLines.Init(nil)
			barLeftWidth, _ = b.barLeft.C.Dimensions()
			barRightWidth, _ = b.barRight.C.Dimensions()
		}
	}

	if barLeftWidth > b.width {
		barLeftWidth = b.width
		barRightWidth = 0
	} else if barLeftWidth+barRightWidth > b.width {
		barRightWidth = b.width - barLeftWidth
	}

	offset := term.Coordinates{Y: b.height - barHeight}

	b.barLeft.Resize(barLeftWidth, barHeight)
	b.barLeft.Move(offset)

	offset.X = b.width - barRightWidth
	b.barRight.Move(offset)
	b.barRight.Resize(barRightWidth, barHeight)
	b.layoutImages(barLeftWidth, barRightWidth, barHeight)

	messageWidth := max(0, b.width-barLeftWidth-barRightWidth)
	offset.X = barLeftWidth + messageWidth/2
	return barHeight
}

// layoutImages places every image over the editor and its bar.
func (b *StatusBar) layoutImages(barLeftWidth, barRightWidth, barHeight int) {
	rows := b.height
	if barHeight == 0 {
		rows = 0
	}
	template.PlaceImages(b.barLeftItems, 0, barLeftWidth, b.width, rows)
	template.PlaceImages(b.barRightItems, b.width-barRightWidth, barRightWidth,
		b.width, rows)
}

func (b *StatusBar) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "text.StatusBar").Logf(level, msg, args...)
}

type statusBarSubscriber StatusBar

func (b *statusBarSubscriber) Handle(ctx context.Context, ev textapi.Event) bool {
	if ev.URI != b.file || ev.Type != textapi.EventTypeFlush {
		return false
	}
	b.lastFlush = b.buf.Version()
	(*StatusBar)(b).log(log.TraceLevel, "received event: %s", ev.Type.String())
	(*StatusBar)(b).rebuildBarFlush(ctx)
	return false
}

func (b *statusBarSubscriber) OnWillEdit(
	ctx context.Context, from, to term.Coordinates, str string,
) {
}

func (b *statusBarSubscriber) OnDidEdit(
	ctx context.Context, start, end term.Coordinates, old string,
) {
	(*StatusBar)(b).rebuildBarEdit()
}

type syntaxService interface {
	State() iterator.Iterator[syntax.State]
}

func (b *StatusBar) streamSyntaxState() {
	if b.syntaxStreaming {
		return
	}
	b.syntaxStreaming = true
	svc, ok := b.buf.View().(syntaxService)
	if !ok {
		b.log(log.DebugLevel, "no syntax will be reported: "+
			"file does not have a syntax tree installed")
		return
	}
	go debug.CapturePanicReport(func() {
		iter := svc.State()
		defer iter.Close()
		for {
			state, ok := iter.Next(b.ctx)
			if !ok {
				break
			}
			b.scheduleNextTick(func() {
				b.rebuildBarSyntax(state)
			})
		}
		if err := iter.Err(); err != nil && !errors.Is(err, context.Canceled) {
			b.log(log.WarnLevel, "syntax state iter error: %v", err)
		}
	})
}

// initLayout lays out cfg.Layout. images holds, by URI, the images its
// StatusBarImage components draw as loaded.
func (b *StatusBar) initLayout(cfg StatusBarConfig, images map[string]image.Image) {
	if cfg.Layout == nil {
		// the default layout is backwards compatible
		// with previous less-based bar.
		cfg.Layout = []StatusBarComponent{
			{Type: StatusBarVoid},
			{
				Template: "%s",
				Type:     StatusBarStatus,
			},
		}
	}

	var barLeft []component.Floating
	var barRight []component.Floating
	var right bool
	for _, comp := range cfg.Layout {
		var toappend component.Floating
		switch comp.Type {
		case StatusBarVoid:
			right = true
			continue
		case StatusBarStatus:
			b.statusTemplate = comp
			toappend = b.status
		case StatusBarFilePath:
			toappend = b.relpath
			b.relpathTemplate = comp
		case StatusBarGitShortRef:
			toappend = b.gitShortRef
			b.gitShortRefTemplate = comp
		case StatusBarGitDiffAdded:
			toappend = b.gitAdd
			b.gitAddTemplate = comp
		case StatusBarGitDiffDeleted:
			toappend = b.gitDel
			b.gitDelTemplate = comp
		case StatusBarDiagnosticsError:
			toappend = b.diagnosticsError
			b.diagnosticsErrorTemplate = comp
		case StatusBarDiagnosticsWarning:
			toappend = b.diagnosticsWarning
			b.diagnosticsWarningTemplate = comp
		case StatusBarDiagnosticsInfo:
			toappend = b.diagnosticsInfo
			b.diagnosticsInfoTemplate = comp
		case StatusBarLanguage:
			b.streamSyntaxState()
			toappend = b.syntaxState
			b.syntaxTemplate = comp
		case StatusBarCoordinatesCursorX:
			toappend = b.cursorX
			b.cursorXTemplate = comp
		case StatusBarCoordinatesCursorY:
			toappend = b.cursorY
			b.cursorYTemplate = comp
		case StatusBarTotalLines:
			toappend = b.totalLines
			b.totalLinesTemplate = comp
		case StatusBarImage:
			var img *template.ImageElement
			var err error
			if loaded, ok := images[comp.Image.URI]; ok {
				img, err = template.NewLoadedImageElement(comp.Image, loaded,
					comp.Template, comp.Attributes, cfg.BackgroundColor)
			} else {
				img, err = template.NewImageElement(comp.Image, comp.Template,
					comp.Attributes, cfg.BackgroundColor, cfg.Storage, cfg.Interrupter)
			}
			if err != nil {
				b.log(log.WarnLevel, "status bar image: %v", err)
				continue
			}
			b.images = append(b.images, img)
			toappend = img
		}
		if right {
			barRight = append(barRight, toappend)
		} else {
			barLeft = append(barLeft, toappend)
		}
	}

	b.barLeft.C = component.Inline(barLeft, component.AlignmentLeft)
	b.barRight.C = component.Inline(barRight, component.AlignmentRight)
	b.barLeftItems, b.barRightItems = barLeft, barRight
	template.SortImages(b.images)
}

func calculateGitStats(diff vctrl.FileDiff) (added, deleted int) {
	for _, hunk := range diff.Hunks {
		added += int(hunk.NewLines)
		deleted += int(hunk.OrigLines)
	}
	return
}

func calculateMessagesStats(sets []LocationSet) (categories map[term.Attributes]int) {
	categories = make(map[term.Attributes]int)
	for _, set := range sets {
		for _, loc := range set.Locations {
			if loc.Message == "" {
				continue
			}
			categories[loc.Attr] = categories[loc.Attr] + 1
		}
	}
	return
}
