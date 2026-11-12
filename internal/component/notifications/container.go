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

package notifications

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

// Level represents the notification level.
type Level uint8

const (
	// LevelError signals an unexpected error.
	LevelError Level = iota
	// LevelWarn signals an expected error.
	LevelWarn
	// LevelInfo signals an informational message.
	LevelInfo
	// LevelSuccess signals a message of success.
	LevelSuccess
)

// Config configures Container.
type Config struct {
	// Style is what Container.Restyle changes.
	Style

	// AutoClose the minimum time to automatically close a notification.
	// If the message is longer than usual, the time will be increased proportionately.
	AutoClose time.Duration
	// Width determines the width of each notification in columns.
	Width int
	// Interrupt is needed to asynchronously update the UI. This is optional.
	Interrupter term.Interrupter

	// RightInset is a column of this width, at the right edge, that
	// notifications must not be drawn over. It matches the column the
	// inner component reserves for another UI element floating over it,
	// which the inner component cannot express by being narrower: it
	// still occupies the full width.
	RightInset int

	// Graphics returns what draws the images that FrameCharSet.Graphics
	// and ProgressRunes.Graphics name. It is called while drawing, and
	// only when the style names an image. When it is nil, or returns
	// nil, the frame and progress bar show their runes alone.
	Graphics func() *tcomponent.FrameGraphics

	// Observer, when set, is invoked with every notification posted
	// via Notify before it is rendered. Tests use it to surface
	// notification text that would otherwise only exist as styled
	// cells in a rendered frame.
	Observer func(level Level, msg string)
}

// Style is how a Container draws notifications.
type Style struct {
	// ProgressBar switches an auto-close progress bar on or off. The
	// progress bar takes the bottom row of the frame, so the images of
	// its bottom slots are not drawn.
	ProgressBar bool
	// Attributes of the notification text.
	Attributes term.Attributes
	// BackgroundAttributes of the notification text.
	BackgroundAttributes term.Attributes
	// FrameCharSet of the notification frame if Frame is enabled.
	// This is optional. If not set, component.FrameCharSetDefault is used.
	FrameCharSet component.FrameCharSet

	// Padding adds top and right padding in number of cells from the margins
	// and between notifications if there's more than one being displayed.
	Padding int

	// ProgressRunes determines the runes used to render progress.
	// If not set, the corresponding characters in FrameCharSet are used.
	ProgressRunes ProgressRunes

	// ColorInfo determines the style of the progress bar for info notifications.
	// By default, the default color along with a bold foreground is used.
	ColorInfo term.Attributes
	// ColorSuccess determines the style of the progress bar for success notifications.
	// By default, term.ColorGreen is used.
	ColorSuccess term.Attributes
	// ColorWarning determines the style of the progress bar for warning notifications.
	// By default, term.ColorYellow is used.
	ColorWarning term.Attributes
	// ColorError determines the style of the progress bar for error notifications.
	// By default, term.ColorRed is used.
	ColorError term.Attributes
}

// withDefaults returns s with the defaults of what it leaves unset.
func (s Style) withDefaults() Style {
	if s.FrameCharSet == (component.FrameCharSet{}) {
		s.FrameCharSet = component.FrameCharSetDefault()
	}
	if s.ColorInfo == (term.Attributes{}) {
		s.ColorInfo.Attrs = s.BackgroundAttributes.Attrs
		s.ColorInfo.Attrs |= term.AttrBold
	}
	if s.ColorSuccess == (term.Attributes{}) {
		s.ColorSuccess.Attrs = s.BackgroundAttributes.Attrs
		s.ColorSuccess.Attrs |= term.AttrBold
		s.ColorSuccess.Fg = term.ColorGreen
	}
	if s.ColorWarning == (term.Attributes{}) {
		s.ColorWarning.Attrs = s.BackgroundAttributes.Attrs
		s.ColorWarning.Attrs |= term.AttrBold
		s.ColorWarning.Fg = term.ColorYellow
	}
	if s.ColorError == (term.Attributes{}) {
		s.ColorError.Attrs = s.BackgroundAttributes.Attrs
		s.ColorError.Attrs |= term.AttrBold
		s.ColorError.Fg = term.ColorRed
	}
	if s.ProgressRunes == (ProgressRunes{Graphics: s.ProgressRunes.Graphics}) {
		s.ProgressRunes.Start = s.FrameCharSet.BottomLeft
		s.ProgressRunes.Current = s.FrameCharSet.HorizontalBottom
		s.ProgressRunes.CurrentTip = s.FrameCharSet.HorizontalBottom
		s.ProgressRunes.Remain = s.FrameCharSet.HorizontalBottom
		s.ProgressRunes.End = s.FrameCharSet.BottomRight
	}
	return s
}

// hasGraphics reports whether s names any image.
func (s Style) hasGraphics() bool {
	return !s.FrameCharSet.Graphics.Empty() || !s.ProgressRunes.Graphics.Empty()
}

// ProgressRunes contains the runes used by a Container to render progress.
type ProgressRunes struct {
	Start      rune
	Current    rune
	CurrentTip rune
	Remain     rune
	End        rune

	// Graphics names an image for each rune, drawn over the cells of
	// that rune. Images are not tinted with the color of the level.
	Graphics ProgressGraphics
}

// ProgressGraphics names the image of each rune of ProgressRunes.
type ProgressGraphics struct {
	Start, Current, CurrentTip, Remain, End string

	// Overlay draws each image over its rune, which shows through the
	// image's transparent pixels in the color of the level. Otherwise
	// each image replaces its rune.
	Overlay bool
}

// Empty reports whether g names no image, whatever its mode.
func (g ProgressGraphics) Empty() bool {
	return g.Start == "" && g.Current == "" && g.CurrentTip == "" &&
		g.Remain == "" && g.End == ""
}

// Container renders an inner tui.Component and overlays any notifications that were
// posted via Notify.
type Container struct {
	cfg   Config
	inner tui.Component

	ctx       context.Context
	cancelCtx func()

	mu    sync.RWMutex
	vlist component.Virtual[*component.ResponsiveList]
	// width and height are the last size, replayed when the reserved
	// column changes.
	width, height int

	notifications map[string]*notificationTicket
}

// New allocates storage for a new instance of Container and initializes it with
// inner. See Containerfor more details
func New(inner tui.Component, cfg Config) *Container {
	ret := new(Container)
	ret.Init(inner, cfg)
	return ret
}

// Init initializes this Container with inner and cfg. It panics if cfg.Width <= 0.
func (n *Container) Init(inner tui.Component, cfg Config) {
	if cfg.Width < 0 {
		panic(fmt.Sprintf("notifications.Container with invalid width: %v", cfg.Width))
	}
	if cfg.AutoClose == 0 {
		panic(fmt.Sprintf("notifications.Container with invalid close timeout: %v", cfg.AutoClose))
	}
	cfg.Style = cfg.Style.withDefaults()
	n.inner = inner
	n.cfg = cfg
	n.vlist.C = component.NewResponsiveList()
	n.ctx, n.cancelCtx = context.WithCancel(context.Background())
	n.notifications = make(map[string]*notificationTicket)
}

// Draw satisfies tui.Component.
func (n *Container) Draw(w term.Writer) {
	n.inner.Draw(w)

	n.mu.RLock()
	defer n.mu.RUnlock()

	n.vlist.Draw(w)
}

// Resize satisfies tui.Component.
func (n *Container) Resize(width, height int) {
	n.inner.Resize(width, height)

	n.mu.Lock()
	defer n.mu.Unlock()

	n.width, n.height = width, height
	n.layout()
}

// layout places the notifications within the last size. It must be
// called with mu held.
func (n *Container) layout() {
	width, height := n.width, n.height
	padding := n.cfg.Padding

	anchor := width - n.cfg.RightInset
	if anchor <= 0 {
		anchor = width
	}
	effectiveWidth := n.cfg.Width
	offset := anchor - n.cfg.Width - padding
	if offset < 0 {
		offset = 0
		effectiveWidth = anchor
	}
	if height <= padding {
		padding = 0
	}

	n.vlist.Move(term.Coordinates{X: offset, Y: padding})
	n.vlist.Resize(effectiveWidth, height-padding)
}

// Restyle draws the open notifications, and those posted later, with s.
func (n *Container) Restyle(s Style) {
	n.mu.Lock()
	defer n.mu.Unlock()

	progressBar := n.cfg.ProgressBar
	n.cfg.Style = s.withDefaults()
	for id, t := range n.notifications {
		comp := t.el.Value().(*component.Span).Content().(*notificationComp)
		comp.style(n.cfg)
		t.el.SetValue(n.newSpan(comp))
		if progressBar != n.cfg.ProgressBar && comp.pausedAt.IsZero() {
			// Resuming starts, or stops, the ticker that repaints
			// the progress bar.
			n.pauseNotification(t.el)
			n.resumeNotification(t, id)
		}
	}
	n.layout()
}

// ID returns the ID of a given notification, as it would be
// returned by Notify.
func (n *Container) ID(level Level, msg string) string {
	return nonCryptoHashString(fmt.Sprintf("noti_%q", msg))
}

// SetRightInset changes the reserved column notifications keep clear
// and re-anchors them against it.
func (n *Container) SetRightInset(cells int) {
	n.mu.Lock()
	if cells == n.cfg.RightInset {
		n.mu.Unlock()
		return
	}
	n.cfg.RightInset = cells
	width, height := n.width, n.height
	n.mu.Unlock()
	n.Resize(width, height)
}

// Notify creates a new notification with the given msg and level.
func (n *Container) Notify(level Level, msg string) string {
	if n.cfg.Observer != nil {
		n.cfg.Observer(level, msg)
	}
	const baselineChars = len("this is a simple notification.")
	duration := n.cfg.AutoClose
	if len(msg) > baselineChars {
		duration *= time.Duration(float64(len(msg)) / float64(baselineChars))
		duration = time.Duration(math.Min(float64(2*n.cfg.AutoClose), float64(duration)))
		duration = time.Duration(math.Max(float64(n.cfg.AutoClose), float64(duration)))
	}

	ctx, cancel := context.WithTimeout(n.ctx, duration)
	id := n.ID(level, msg)

	n.mu.Lock()
	defer n.mu.Unlock()

	comp := newNotification(level, msg, n.cfg, duration, cancel)

	// clear out current notification, based on message equality, if it exists
	if ticket, exists := n.notifications[id]; exists {
		ticket.cancelCtx()
		n.vlist.C.Remove(&ticket.el)
		delete(n.notifications, id)
	}

	// add a new notification
	el := n.vlist.C.PushFront(n.newSpan(comp))
	n.notifications[id] = &notificationTicket{el: el, cancelCtx: cancel}

	n.startAutoClose(ctx, cancel, el, id)

	return id
}

func (n *Container) newSpan(comp *notificationComp) *component.Span {
	return component.NewSpan(comp, component.SpanConfig{
		PadVertical:      n.cfg.Padding,
		ContentAlignment: component.AlignmentTop,
	})
}

// UpdateProgress updates the progress of a notification,
// overriding the default time-based progress.
//
// After calling this once, caller is responsible for updating it
// until progress == total.
//
// The message argument can be empty, in which case the previous
// message passed to UpdateProgress is used, or the original
// message when creating the notification.
func (c *Container) UpdateProgress(id, message string, progress, total int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if progress > total || total == 0 {
		panic("invalid arguments: progress must be smaller than " +
			"total and total must not be zero")
	}
	ticket, ok := c.notifications[id]
	if !ok {
		return false
	}

	// cancel auto progress
	c.pauseNotification(ticket.el)

	// close if progress == total
	if progress == total {
		c.closeNotification(ticket.el)
		delete(c.notifications, id)
		return true
	}

	span := ticket.el.Value().(*component.Span)
	comp := span.Content().(*notificationComp)
	comp.manualProgress = progress
	comp.manualProgressTotal = total
	if message != "" {
		comp.setMessage(message)
		// force responsive list to "dirty" state, so if message
		// size changed, we re-evaluate sizes.
		ticket.el.SetValue(span)
	}
	return true
}

// CloseAll closes all open notifications.
func (n *Container) CloseAll() {
	n.mu.Lock()
	defer n.mu.Unlock()

	// clear out current notification, based on message equality, if it exists
	for _, ticket := range n.notifications {
		n.closeNotification(ticket.el)
	}
	n.notifications = make(map[string]*notificationTicket)
}

// PauseAll pauses auto-close progress on all open notifications.
func (n *Container) PauseAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	// clear out current notification, based on message equality, if it exists
	for _, ticket := range n.notifications {
		n.pauseNotification(ticket.el)
	}
}

// ResumeAll resumes auto-close progress on all open notifications.
func (n *Container) ResumeAll() {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.resumeAll()
}
func (n *Container) resumeAll() {
	for id, ticket := range n.notifications {
		n.resumeNotification(ticket, id)
	}
}

// Handle handles the given term.Event. It ignores anything
// other than mouse events. It handles mouse events by pausing
// notifications on hover.
func (n *Container) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventMouse || ev.Mod != 0 {
		return
	}

	pos := n.vlist.Position()
	height := n.vlist.Height()
	width := n.vlist.Width()
	if ev.MouseX < pos.X || ev.MouseY < pos.Y ||
		ev.MouseX >= pos.X+width || ev.MouseY >= pos.Y+height {
		n.ResumeAll() // hover outside should resume all
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	el, ok := n.vlist.C.ElementAt(term.Coordinates{X: ev.MouseX - pos.X, Y: ev.MouseY - pos.Y})
	if !ok {
		n.resumeAll()
		return
	}

	switch ev.Key {
	case term.MouseLeft:
		n.closeNotification(el)
	default:
		n.pauseNotification(el)
	}

	handled = true
	return
}

// Close cancels all pending notifications and closes this
// notification container for good. Future calls to Notify will
// not produce new notifications.
func (n *Container) Close() error {
	n.cancelCtx()
	return nil
}

func (n *Container) closeNotification(el component.ListNode) {
	el.Value().(*component.Span).Content().(*notificationComp).cancel()
	n.vlist.C.Remove(&el)
	// Closed by a click, it must not be found by id anymore, or
	// UpdateProgress would update what nobody sees.
	for id, t := range n.notifications {
		if t.el.Value() == el.Value() {
			delete(n.notifications, id)
			break
		}
	}
}

func (n *Container) pauseNotification(el component.ListNode) {
	comp := el.Value().(*component.Span).Content().(*notificationComp)
	if comp.pausedAt != (time.Time{}) {
		return // pause is idempotent
	}
	// this prevents element from being removed
	// and cancels fps interrupt goroutine.
	comp.pausedAt = time.Now()
	comp.cancel()

}

func (n *Container) resumeNotification(t *notificationTicket, id string) {
	comp := t.el.Value().(*component.Span).Content().(*notificationComp)
	if comp.manualProgressTotal != 0 {
		return // pinned by UpdateProgress until progress == total
	}
	if comp.pausedAt.Equal(time.Time{}) {
		return // resume is idempotent
	}
	remaining := comp.end.Sub(comp.pausedAt)
	newEnd := time.Now().Add(remaining)

	ctx, cancelCtx := context.WithTimeout(n.ctx, remaining)

	comp.end = newEnd
	comp.pausedAt = time.Time{}
	comp.cancel = cancelCtx
	t.cancelCtx = cancelCtx

	n.startAutoClose(ctx, cancelCtx, t.el, id)
}

func (n *Container) startAutoClose(
	ctx context.Context, cancel func(),
	el component.ListNode, id string,
) {
	go debug.CapturePanicReport(func() {
		defer cancel()

		<-ctx.Done()
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}

		n.mu.Lock()
		// no need to ensure that while we were trying to acquire a lock, no one
		// removed it already as Remove is itempotent. See Go std's list.List.
		n.vlist.C.Remove(&el)
		delete(n.notifications, id)
		n.mu.Unlock()

		if n.cfg.Interrupter != nil {
			_ = n.cfg.Interrupter.Interrupt(ctx)
		}
	})

	if !n.cfg.ProgressBar {
		return
	}

	if n.cfg.Interrupter != nil {
		go debug.CapturePanicReport(func() {
			term.InterruptAt(ctx, n.cfg.Interrupter, 10)
		})
	}
}

type notificationTicket struct {
	cancelCtx func()
	el        component.ListNode
}

func nonCryptoHashString(s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return strconv.FormatUint(h.Sum64(), 10)
}
