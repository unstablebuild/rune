// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gui

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/gui/font"
)

var (
	_ ebiten.Game = (*GUI)(nil)

	// ErrHandlerExited reports that the TUI handler goroutine returned.
	ErrHandlerExited = errors.New("tui handler exited")
)

const (
	defaultWidth, defaultHeight = 800, 600
	echoPollInterval            = 50 * time.Microsecond
)

var echoWaitBudget = 2 * time.Millisecond

// GUI is the ebiten-backed graphical front end for a Rune terminal.
type GUI struct {
	ctx               context.Context
	cancelCtx         func()
	mu                sync.Locker
	fontManager       *font.Manager
	updateChan        chan term.Event
	handler           tui.Handler
	writer            *cell.BufferWriter
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

	links linkScanner

	echoLikely  bool
	prevTickKey bool

	interruptPending    atomic.Bool
	processWindowClosed func() []term.Event
	closingHandled      bool
	closeOnce           sync.Once
}
