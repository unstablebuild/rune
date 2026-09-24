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

package handler

import (
	"fmt"
	"strings"

	compapi "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	componenttemplate "unstable.build/rune/internal/component/template"
)

// LessConfig holds configuration values for a Less instance.
type LessConfig struct {
	Wrap bool
	// SuperimposeMessage changes the behaviour to instead of drawing
	// a bottom bar permanently on which messages are written,
	// messages are superimposed on the last row of the scroll content.
	SuperimposeMessage bool
	ResAttr            term.Attributes
	BarAttr            term.Attributes
	MessageLayout      LessMessageLayout
	Attributes         term.Attributes
	Handler            func(LessEvent)
	// NoBar disables SetMessage and search functionality.
	// It takes prevalence over SuperimposeMessage.
	NoBar bool
}

// DefaultLessConfig is a sane configuration defaults for Less.
func DefaultLessConfig() LessConfig {
	return LessConfig{
		Wrap: false,
		ResAttr: term.Attributes{
			Attrs: term.AttrReverse,
		},
		NoBar: false,
	}
}

// Less is a clone of Unix' less program which implements
// the Handler and Component interfaces.
type Less struct {
	scroll            *component.Scroll
	searchScrollVirt  compapi.Virtual[*component.Scroll]
	msgStr            string
	msgWidth          int
	msgHeight         int
	msgFloating       bool
	msgVirt           compapi.Virtual[compapi.Responsive]
	mode              LessMode
	moveMode          LessMoveMode
	usedMsgBarAttr    term.Attributes
	usedSearchBarAttr term.Attributes
	delEOF            bool
	cursorOffset      int
	promptLen         int
	height            int
	width             int
	search            string
	config            LessConfig
}

type lessMessage struct {
	compapi.Floating
}

func (m lessMessage) Height(int) int {
	_, height := m.Dimensions()
	return height
}

// LessEventType represents a less event
type LessEventType uint8

const (
	// EOF is dispatched when user has reached end of buffer
	EOF LessEventType = iota
	// Search is dispatched when user has performed a text search
	// `Data` field in `Event` struct will be set to the search text
	Search
)

// LessMode represents one of the two modes of a less Handler.
// See Manual for more information on how to switch between modes.
type LessMode uint8

// List of less modes.
const (
	LessNormalMode LessMode = iota
	LessSearchMode
)

// LessMoveMode represents the moving direction of the search.
type LessMoveMode uint8

// List of less moving modes.
const (
	LessMoveForward LessMoveMode = iota
	LessMoveBackward
)

// LessEvent type represents a less event.
type LessEvent struct {
	Type LessEventType
	Data []byte
	Err  error
}

// NewLess allocates storage and returns a new instance of Less.
func NewLess(cfg LessConfig) *Less {
	l := new(Less)
	l.Init(cfg)
	return l
}

// Init initializes this instance or resets it if already initialized.
func (l *Less) Init(cfg LessConfig) {
	l.InitWithBuffer(cell.NewBuffer(), cfg)
}

// InitWithBuffer initialzes this instance with the given Buffer and configuration.
// If config is nil, the default one is used.
func (l *Less) InitWithBuffer(buf *cell.Buffer, cfg LessConfig) {
	l.scroll = component.NewScroll(buf)
	l.initWithBuffer(buf, cfg)
}

// InitWithScroll initialzes this instance with the given main Scroll and configuration.
// If config is nil, the default one is used.
func (l *Less) InitWithScroll(scroll *component.Scroll, cfg LessConfig) {
	l.scroll = scroll
	l.initWithBuffer(scroll.Buffer(), cfg)
}

// SetNormalMode sets the mode to normal.
func (l *Less) SetNormalMode() {
	l.cursorOffset = 1
	l.promptLen = 1
	l.searchScrollVirt.C.Buffer().Reset()
	l.mode = LessNormalMode
}

// SetSearchMode sets the mode to search mode.
func (l *Less) SetSearchMode(moveMode LessMoveMode) {
	switch moveMode {
	case LessMoveForward:
		l.SetPromptMode("/")
	case LessMoveBackward:
		l.SetPromptMode("?")
	}
	l.moveMode = moveMode
}

// SetPromptMode enters search mode behind an arbitrary prompt label,
// for a caller that reads the input back with SearchText and acts on
// it itself. Enter still runs the less search; intercept it when the
// input is not a search. The label must be ASCII.
func (l *Less) SetPromptMode(prompt string) {
	buf := l.searchScrollVirt.C.Buffer()
	buf.Reset()
	buf.WriteString(prompt)
	l.promptLen = len(prompt)
	l.cursorOffset = l.promptLen
	l.mode = LessSearchMode
	l.moveMode = LessMoveForward

	if l.config.SuperimposeMessage && l.scroll.Attributes != l.usedSearchBarAttr {
		l.updateSearchBarAttr()
	}
	l.resize()
}

// SearchText returns the contents of the search buffer.
func (l *Less) SearchText() string {
	str := l.searchScrollVirt.C.Buffer().String()
	bytes := []byte(str)[min(l.promptLen, len(str)):]
	return string(bytes)
}

// SetMessage sets a message to be displayed on the bottom right corner.
func (l *Less) SetMessage(text string, args ...any) {
	l.setMessage(fmt.Sprintf(text, args...))
	l.resize()
}

// Mode returns the current LessMode.
func (l *Less) Mode() LessMode {
	return l.mode
}

// Cursor satisfies tui.Handler
func (l *Less) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	ret := term.Coordinates{X: l.cursorOffset, Y: l.height - 1}
	return ret, term.CursorStyleDefault, true
}

// Selection satisfies tui.Handler
func (l *Less) Selection() (string, bool) {
	return "", false
}

// Draw satisfies tui.Component
func (l *Less) Draw(w term.Writer) {
	l.scroll.Draw(w)

	if !l.scroll.CanSeekDown() && !l.delEOF {
		l.delEOF = true
		l.sendEvent(LessEvent{Type: EOF})
	}

	// if attrs were changed dynamically, ensure attributes of superimposed message
	// match those of the Scroll.
	if l.config.SuperimposeMessage &&
		l.config.BarAttr.Bg == 0 && l.config.MessageLayout.Attributes.Bg == 0 &&
		l.usedMsgBarAttr.Bg != l.scroll.Attributes.Bg {
		l.setMessage(l.msgStr)
		cmdBarWidth, cmdBarHeight := l.cmdBarHeight()
		l.resizeMoveMessage(cmdBarWidth, cmdBarHeight)
	}

	l.msgVirt.Draw(w)
	if l.mode == LessSearchMode {
		l.searchScrollVirt.Draw(w)
	}
}

// Resize satisfies tui.Component.
func (l *Less) Resize(width, height int) {
	l.width, l.height = width, height
	l.resize()
}

// Handle satisfies tui.Handler.
func (l *Less) Handle(ev term.Event) (exit bool, handled bool) {
	switch ev.Type {
	case term.EventKey:
		switch l.mode {
		case LessNormalMode:
			exit, handled = l.normalHandleEvent(ev)
		case LessSearchMode:
			exit, handled = l.searchHandleEvent(ev)
		}
	}

	return
}

// Buffer returns the internal scroll's Buffer.
func (l *Less) Buffer() *cell.Buffer {
	return l.scroll.Buffer()
}

// Scroll returns the internal scroll. Scroll's public properties
// should not be updated. Use LessConfig instead.
func (l *Less) Scroll() *component.Scroll {
	return l.scroll
}

// ShowCommandBar determines whether the command bar should be
// displayed or not.
func (l *Less) ShowCommandBar(show bool) {
	l.config.NoBar = !show
}

func (l *Less) searchHandleEvent(ev term.Event) (bool, bool) {
	if ev.Type != term.EventKey || ev.Mod != 0 {
		return false, false
	}

	switch ev.Key {
	case term.KeyBackspace:
		if l.cursorOffset > l.promptLen {
			l.cursorOffset--
			l.searchScrollVirt.C.Buffer().
				DeleteCell(term.Coordinates{X: l.cursorOffset, Y: 0})
		}
	case term.KeyEnter:
		l.search = l.SearchText()
		l.scroll.Search(l.search)
		l.SetNormalMode()
		switch l.moveMode {
		case LessMoveForward:
			l.scroll.SeekNextResult()
		case LessMoveBackward:
			l.scroll.SeekPrevResult()
		}
		l.sendEvent(LessEvent{Type: Search, Data: []byte(l.search)})

	case term.KeyEsc:
		l.SetNormalMode()

	case term.KeySpace:
		ev.Ch = ' '
		fallthrough

	default:
		l.cursorOffset++
		l.searchScrollVirt.C.Buffer().WriteString(string(ev.Ch))
	}

	l.resize()
	if l.config.SuperimposeMessage && l.scroll.Attributes != l.usedSearchBarAttr {
		l.updateSearchBarAttr()
	}
	return false, true
}

func (l *Less) updateSearchBarAttr() {
	l.usedSearchBarAttr = l.scroll.Attributes
	l.searchScrollVirt.C.Attributes = l.usedSearchBarAttr
}

func (l *Less) normalHandleEvent(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return
	}
	if ev.Mod == 0 {
		switch ev.Key {
		case term.KeyArrowUp:
			return false, l.scroll.SeekUp()
		case term.KeyArrowDown:
			return false, l.scroll.SeekDown()
		case term.KeyPgup:
			return false, l.scroll.SeekUpPage()
		case term.KeyPgdn:
			return false, l.scroll.SeekDownPage()
		}
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'p':
			return false, l.scroll.SeekUp()
		case 'n':
			return false, l.scroll.SeekDown()
		case 'b':
			return false, l.scroll.SeekUpPage()
		case 'f':
			return false, l.scroll.SeekDownPage()
		}
		return
	}
	if ev.Mod != 0 {
		return
	}

	switch ev.Ch {
	case 'q':
		exit = true
		handled = true
	case 'N':
		switch l.moveMode {
		case LessMoveForward:
			handled = l.scroll.SeekPrevResult()
		case LessMoveBackward:
			handled = l.scroll.SeekNextResult()
		}
	case 'n':
		switch l.moveMode {
		case LessMoveForward:
			handled = l.scroll.SeekNextResult()
		case LessMoveBackward:
			handled = l.scroll.SeekPrevResult()
		}
	case '0':
		handled = l.scroll.SeekStartLine()
	case '$':
		handled = l.scroll.SeekEndLine()
	case 'g':
		handled = l.scroll.SeekStartFile()
	case 'G':
		handled = l.scroll.SeekEndFile()
	case 'j':
		handled = l.scroll.SeekDown()
	case 'k':
		handled = l.scroll.SeekUp()
	case 'h':
		handled = l.scroll.SeekLeft()
	case 'l':
		handled = l.scroll.SeekRight()
	case '/':
		l.SetSearchMode(LessMoveForward)
		handled = true
	case '?':
		l.SetSearchMode(LessMoveBackward)
		handled = true

	default:
		switch ev.Key {
		case term.KeyEsc:
			exit = true
			handled = true
		default:
			handled = false
		}
	}

	return
}

func (l *Less) setMessage(msg string) {
	layout := l.config.MessageLayout
	if layout.Template == "" {
		layout.Template = "%s"
	}
	// The layout's own styling wins over the bar's base attributes.
	attr := term.AttributesUnion(l.config.BarAttr, layout.Attributes)
	if l.config.SuperimposeMessage && attr.Bg == 0 {
		attr.Bg = l.scroll.Attributes.Bg
	}
	// Layout decorations are laid out inline with the message, so on a
	// multi-row message they end up detached beside the middle row.
	decorate := layout.Template != "%s" && !strings.Contains(msg, "\n")
	formatted := ""
	if msg != "" {
		formatted = msg
		if decorate {
			formatted = fmt.Sprintf(layout.Template, msg)
		}
	}
	var newMsg compapi.Responsive = compapi.NewResponsiveString(formatted,
		compapi.StringResponsiveConfig{
			StringConfig: compapi.StringConfig{
				Alignment:            compapi.AlignmentRight,
				Attributes:           attr,
				BackgroundAttributes: attr,
			},
		})
	l.msgWidth = term.CalculateOptimalWidth(term.StringToCells(formatted))
	l.msgFloating = false
	if msg != "" && decorate {
		built := componenttemplate.Build(layout.Template, msg, term.Attributes{},
			attr, l.scroll.Attributes.Bg)
		inline := compapi.Inline(built, compapi.AlignmentRight)
		l.msgWidth, l.msgHeight = inline.Dimensions()
		l.msgFloating = true
		newMsg = lessMessage{Floating: inline}
	}
	l.usedMsgBarAttr = attr
	l.msgVirt.C = newMsg
	l.msgStr = msg
}

func (l *Less) cmdBarHeight() (cmdBarWidth, cmdBarHeight int) {
	if l.config.NoBar {
		return
	}
	cmdBarWidth = l.width
	if l.config.SuperimposeMessage {
		msgslen := l.msgWidth + len(l.searchScrollVirt.C.Buffer().String())
		cmdBarWidth = min(l.width, msgslen)
	}
	cmdBarHeight = max(l.msgVirt.C.Height(cmdBarWidth),
		l.searchScrollVirt.C.Height(cmdBarWidth))
	if l.height <= cmdBarHeight {
		cmdBarHeight = 1
	}
	return
}

func (l *Less) resize() {
	cmdBarWidth, cmdBarHeight := l.cmdBarHeight()
	contentHeight := l.height - cmdBarHeight
	if l.config.SuperimposeMessage && l.scroll.InvertOffset {
		// avoid content shifting up and down due to invert offset
		// search needs to be refactored into a browser-wide component
		// so although this is not ideal, it's fine for now.
		contentHeight = l.height
	}

	// allow content to be superimposed on bar
	if cmdBarWidth == 0 {
		contentHeight = l.height
	}

	if l.scroll.SizeHeight() != contentHeight || l.scroll.Width() != l.width {
		l.scroll.Resize(l.width, contentHeight)
	}

	l.searchScrollVirt.Move(term.Coordinates{X: 0, Y: l.height - cmdBarHeight})
	l.searchScrollVirt.Resize(l.width-l.msgWidth, cmdBarHeight)

	l.resizeMoveMessage(cmdBarWidth, cmdBarHeight)
}

func (l *Less) resizeMoveMessage(cmdBarWidth, cmdBarHeight int) {
	width, height := cmdBarWidth, cmdBarHeight
	if l.config.SuperimposeMessage || l.msgFloating {
		width = min(l.msgWidth, cmdBarWidth)
	}
	// Built layouts must remain on the top row when a search query wraps.
	if l.msgFloating {
		height = min(l.msgHeight, cmdBarHeight)
	}
	// don't occlude other content if bar background is empty
	l.msgVirt.Move(term.Coordinates{X: l.width - width, Y: l.height - cmdBarHeight})
	l.msgVirt.Resize(width, height)
}

func (l *Less) setupScroll(w *component.Scroll, attr term.Attributes) {
	w.ResultsAttr = l.config.ResAttr
	w.Wrap = l.config.Wrap
	w.Attributes = attr
}

func (l *Less) initWithBuffer(buf *cell.Buffer, cfg LessConfig) {
	l.delEOF = false
	if cfg.ResAttr == (term.Attributes{}) {
		cfg.ResAttr = DefaultLessConfig().ResAttr
	}

	l.config = cfg
	l.searchScrollVirt.C = component.NewScroll(cell.NewBuffer())

	// initialize message comps
	l.setMessage("")

	searchBarAttr := l.config.BarAttr
	l.setupScroll(l.searchScrollVirt.C, searchBarAttr)
	l.setupScroll(l.scroll, l.config.Attributes)
	if l.config.SuperimposeMessage {
		l.updateSearchBarAttr()
	}

	l.SetNormalMode()
}

func (l *Less) sendEvent(ev LessEvent) {
	if l.config.Handler != nil {
		l.config.Handler(ev)
	}
}
