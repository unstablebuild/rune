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

package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	thandler "unstable.build/rune/internal/handler"
)

var _ browserapi.Handler = (*Component)(nil)
var _ thandler.FloatingBarHandler = (*Component)(nil)
var _ DragTarget = (*Component)(nil)

// offsetTabs is the tabs bar wrapped in a virtual writer offset so the
// bar visually starts at TabBarOffset. The underlying component.Tabs is
// resized to (width - offset) so the resize algorithm operates on the
// actually visible viewport — otherwise the focused tab would render
// past the right edge of the viewport.
type offsetTabs struct {
	handler.Virtual[*thandler.ShadedTabs]
	offset int
}

func newOffsetTabs(tabs *thandler.ShadedTabs, offset int) *offsetTabs {
	v := &offsetTabs{offset: offset}
	v.C = tabs
	v.Move(term.Coordinates{X: offset})
	return v
}

// Resize : tui.Component
func (v *offsetTabs) Resize(width, height int) {
	inner := width - v.offset
	if inner < 0 {
		inner = 0
	}
	v.Virtual.Resize(width, height)
	v.C.Resize(inner, height)
}

// Component renders a browser-like tui.Compontent and exposes an API
// to open new windows, add new tabs, and switch between tabs.
//
// All tui.Handlers installed other than via NewTab are considered ephemeral,
// and will be destroyed either when windows close or when they return exit=true
// to a call to Handle. Conversely, tui.Handlers installed via NewTab
// will remain as a tab and can be managed independently from windows.
type Component struct {
	tabs thandler.Tabs
	// shadedTabs wraps tabs to animate the labels of active tabs. It is
	// what gets installed in the frame union.
	shadedTabs *thandler.ShadedTabs
	wm         thandler.WindowManager
	union      thandler.FrameUnion
	width      int
	height     int
	nextSplit  browserapi.Orientation

	dirtyTabs   bool
	focusWindow thandler.Window
	config      Config
	buffers     []*Tab
	windows     map[uint64]*browserWindow
	prompts     map[string]Window
	drag        dragState
	winDrop     winDropState
	interrupter term.Interrupter

	// rightInset is the live width of the reserved right column. It
	// starts at Config.RightInset and SetRightInset changes it.
	rightInset int
	// bars records every Bar call so the union can be rebuilt, which
	// is the only way to change a member's width: the union appends
	// members and never resizes or removes them.
	bars []barEntry
}

type barEntry struct {
	orientation browserapi.Orientation
	size        int
	frame       bool
	h           tui.Handler
}

// NewComponent allocates storage for a new Component and initializes it.
func NewComponent(config Config) *Component {
	ret := new(Component)
	ret.Init(config)
	return ret
}

// Init initializes this Component with config.
func (c *Component) Init(config Config) {
	c.config = config
	c.config.WindowManagerConfig.FloatingBar = c
	c.windows = make(map[uint64]*browserWindow)

	c.nextSplit = browserapi.OrientationRight
	c.prompts = make(map[string]Window)

	c.tabs.Init()
	c.tabs.OnClick = func(id int) (ret bool) {
		if config.OnTabsClick != nil {
			ret = config.OnTabsClick(id)
		}
		if id < 0 || id >= len(c.buffers) {
			return ret
		}
		t := c.buffers[id]
		// If the tab is already bound to a window, switch focus to that
		// window rather than trying to move the tab into the focused
		// window. Clicking the tab of an already-focused window is a no-op.
		if win, ok := t.Window(); ok {
			bwin := win.(*browserWindow)
			if bwin != c.Focus().(*browserWindow) {
				c.SetFocus(bwin)
			}
			return true
		}
		bwin := c.Focus().(*browserWindow)
		err := c.tryUpdateWindowContent(bwin, t, bwin.win.Content().(browserapi.Handler))
		if err != nil {
			c.setError(err)
		}
		return true
	}
	if config.OnTabIconClick != nil {
		c.tabs.OnIconClick = func(id int) {
			config.OnTabIconClick(c.buffers[id])
		}
	}

	c.wm.Init(c.wallpaper(), c.config.WindowManagerConfig)
	c.focusWindow = c.wm.Focus()
	_ = c.newWindow(c.focusWindow) // init handler with initial window
	c.wm.Subscribe((*wmSubscriber)(c))
	c.buffers = make([]*Tab, 0)

	frameAttr := config.WindowManagerConfig.FrameAttr
	highlightAttr := config.FocusTabHighlightAttr
	frameAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	bgAttr := term.Attributes{Bg: frameAttr.Bg}
	focusTabAttr := config.FocusTabAttr
	focusTabAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	nonFocusTabAttr := config.NonFocusTabAttr
	nonFocusTabAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	c.tabs.SetAttr(focusTabAttr, nonFocusTabAttr,
		c.focusTabIconAttr(), c.nonFocusTabIconAttr(),
		highlightAttr, frameAttr, bgAttr)
	c.tabs.SetFrameCharSet(config.WindowManagerConfig.FrameCharSet)
	c.tabs.SetFocusFrameChar(config.FocusTabHighlightChar)
	c.shadedTabs = thandler.NewShadedTabs(&c.tabs, thandler.ShadedTabsConfig{
		Shader:      config.ActiveTabShader,
		FPS:         config.ActiveTabShaderFPS,
		Loop:        config.ActiveTabShaderLoop,
		DefAttr:     config.NonFocusTabAttr,
		Interrupter: c.interrupter,
		Active:      c.activeTabIndices,
	})

	c.rightInset = config.RightInset
	c.initUnion()
	if config.TabNameSeparator != "" {
		c.tabs.SetNameSeparator(config.TabNameSeparator)
	}
}

// initUnion builds the frame union from scratch. The union only ever
// appends members, so changing the reserved column's width means
// rebuilding and replaying everything stacked around the window
// manager.
func (c *Component) initUnion() {
	c.union = thandler.FrameUnion{}
	c.union.Init(&c.wm)

	// make sure that frame union attrs are same as window manager attrs
	c.union.Attributes = c.config.WindowManagerConfig.FrameAttr
	c.union.Right = c.config.FrameUnionCharSet.Right
	c.union.Left = c.config.FrameUnionCharSet.Left
	c.union.Top = c.config.FrameUnionCharSet.Top
	c.union.Bottom = c.config.FrameUnionCharSet.Bottom
	c.union.Frame = c.config.Frame && c.config.FrameUnion

	// if tab bar offset is set, the remove frame from tabs
	// and install via union and no frame unioning.
	if c.config.TabBarOffset > 0 {
		vtabs := newOffsetTabs(c.shadedTabs, c.config.TabBarOffset)
		c.tabs.SetBorder(false)
		c.union.UnionTopFrame(vtabs, c.tabsSize(), false)
		c.union.CaptureDrags(vtabs)
	} else {
		c.tabs.SetBorder(c.config.Frame)
		c.union.UnionTop(c.shadedTabs, c.tabsSize())
		c.union.CaptureDrags(c.shadedTabs)
	}
	// The union sizes top members before left/right ones, so an empty
	// right member starts below the tab bar and narrows only the window
	// manager. It also swallows clicks landing on whatever floats there.
	if c.rightInset > 0 {
		c.union.UnionRightFrame(handler.Nop(), c.rightInset, false)
	}
	for _, bar := range c.bars {
		c.unionBar(bar)
	}
}

// SetRightInset changes the width of the reserved right column and
// relays the browser out around it.
func (c *Component) SetRightInset(cells int) {
	if cells < 0 {
		cells = 0
	}
	if cells == c.rightInset {
		return
	}
	c.rightInset = cells
	c.initUnion()
	c.Resize(c.width, c.height)
}

// NewTab adds a new tab to the list of tabs on this Component.
func (c *Component) NewTab(
	resource workspaceapi.URI, icon rune, name string,
	h browserapi.Handler, f io.Closer,
) *Tab {
	if c.config.TabOverrideIcon != 0 {
		icon = c.config.TabOverrideIcon
	}
	t := newTab(c, resource, h, f)
	c.buffers = append(c.buffers, t)
	c.tabs.Add(icon, name)
	return t
}

// NewTabFromContent converts the given window's content into a tab, if it's not a tab
// already. This method always returns a valid tab; whether it created one
// or returned false because the window's content is already a tab.
func (c *Component) NewTabFromContent(
	icon rune, name string, win Window,
) (*Tab, bool) {
	bwin := win.(*browserWindow)
	content := bwin.win.Content().(browserapi.Handler)
	tab, ok := content.(*Tab)
	if ok {
		return tab, false
	}
	content = c.unwrapContent(content)
	var uri workspaceapi.URI
	// best effort
	if urier, ok := content.(interface{ URI() workspaceapi.URI }); ok {
		uri = urier.URI()
	} else {
		var err error
		path := url.PathEscape(fmt.Sprintf("%p", content))
		uri, err = workspaceapi.ParseURI(fmt.Sprintf("internal:///%s", path))
		if err != nil {
			panic("parse internal uri")
		}
	}
	subscriber, ok := content.(TabSubscriber)
	tab = c.NewTab(uri, icon, name, content, nil)
	bwin.win.SetContent(tab)
	tab.setWindow(nil, bwin)
	if ok {
		tab.Subscribe(subscriber)
	}
	c.tabs.SetFocus(c.mustFindTabID(tab))
	c.dirtyTabs = true
	return tab, true
}

// Tab returns the tab with name and true if there's a tab with such name
// or nil and false otherwise.
func (c *Component) Tab(uri workspaceapi.URI) (*Tab, bool) {
	for _, t := range c.buffers {
		if t.uri.String() == uri.String() {
			return t, true
		}
	}
	return nil, false
}

// TabName returns the name and default name of the tab with the given uri or false
// if there's no tab with the given uri.
func (c *Component) TabName(uri workspaceapi.URI) (string, string, bool) {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			return c.tabs.TabName(i), c.tabs.DefaultTabName(i), true
		}
	}
	return "", "", false
}

// TabIcon returns the icon and default icon of the tab with the given uri
// or false if there's no tab with the given uri.
func (c *Component) TabIcon(uri workspaceapi.URI) (icon, defaultIcon rune, ok bool) {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			return c.tabs.TabIcon(i), c.tabs.DefaultTabIcon(i), true
		}
	}
	return 0, 0, false
}

// TabAttrs returns the attributes of the tab with the given uri or false
// if there's no tab with the given uri.
func (c *Component) TabAttrs(uri workspaceapi.URI) (term.Attributes, bool) {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			return c.tabs.TabAttr(i), true
		}
	}
	return term.Attributes{}, false
}

// SetTabNameAndAttrs overrides the name and attributes of the tab with the given uri.
// It returns false if there's no tab with id.
func (c *Component) SetTabNameAndAttrs(
	uri workspaceapi.URI, name string, attr term.Attributes,
) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.SetTabName(i, name)
			c.tabs.SetTabAttr(i, attr)
			return true
		}
	}
	return false
}

// SetTabAttrs overrides the name and attributes of the tab with the given uri.
// It returns false if there's no tab with id.
func (c *Component) SetTabAttrs(
	uri workspaceapi.URI, attr term.Attributes,
) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.SetTabAttr(i, attr)
			return true
		}
	}
	return false
}

// SetTabActivity marks the tab with the given uri as having work in
// progress or as idle. The active-tab shader runs over the labels of
// active tabs for as long as there is at least one. Activity is dropped
// when the tab is removed. It returns false if there's no tab with the
// given uri; setting the current state again is a no-op.
func (c *Component) SetTabActivity(uri workspaceapi.URI, active bool) bool {
	t, ok := c.Tab(uri)
	if !ok {
		return false
	}
	if t.active != active {
		t.active = active
		c.tabActivityChanged()
	}
	return true
}

// HasActiveTabs reports whether any tab is marked active.
func (c *Component) HasActiveTabs() bool {
	return slices.ContainsFunc(c.buffers, func(t *Tab) bool { return t.active })
}

func (c *Component) activeTabIndices() []int {
	var ret []int
	for i, t := range c.buffers {
		if t.active {
			ret = append(ret, i)
		}
	}
	return ret
}

func (c *Component) tabActivityChanged() {
	c.shadedTabs.SetRunning(c.HasActiveTabs())
	if c.config.OnTabActivity != nil {
		c.config.OnTabActivity()
	}
}

// SetTabIcon overrides the icon of the tab with the given uri.
// It returns false if there's no tab with the given uri. The tab icon
// can be restored to its default via ResetTabIcon.
func (c *Component) SetTabIcon(uri workspaceapi.URI, icon rune) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.SetTabIcon(i, icon)
			return true
		}
	}
	return false
}

// ResetTabIcon resets the icon of the tab with the given uri to the
// last icon set via SetTabDefaultIcon or, if none, the icon the tab
// was created with (after any Config.TabOverrideIcon override).
// It returns false if there's no tab with the given uri.
func (c *Component) ResetTabIcon(uri workspaceapi.URI) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.ResetTabIcon(i)
			return true
		}
	}
	return false
}

// ResetTabNameAndAttrs resets the name and attributes of the tab with the given uri.
// Moving forward, the name and attributes and name set via
// SetTabDefaultNameAndAttrs will be used. It returns false if there's no tab with id.
func (c *Component) ResetTabNameAndAttrs(uri workspaceapi.URI) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.ResetTabName(i)
			c.tabs.ResetTabAttr(i)
			return true
		}
	}
	return false
}

// SetTabDefaultNameAndAttrs resets the attributes and name of the
// tab with the given uri. It returns false if there's no tab with id.
func (c *Component) SetTabDefaultNameAndAttrs(
	uri workspaceapi.URI, name string, attr term.Attributes,
) bool {
	for i, t := range c.buffers {
		if t.uri.String() == uri.String() {
			c.tabs.SetTabDefaultAttr(i, attr)
			c.tabs.SetTabDefaultName(i, name)
			return true
		}
	}
	return false
}

// Tabs returns the tabs open in this browser.Component.
func (c *Component) Tabs() (ret []*Tab) {
	ret = make([]*Tab, len(c.buffers))
	copy(ret, c.buffers)
	return
}

// MoveTabLeft moves the tab in the given window to the left
// of the tabs list.
func (c *Component) MoveTabLeft(win Window) error {
	bWin := win.(*browserWindow)
	t, ok := browserTabAtWindow(bWin)
	if !ok {
		return errors.New("window content is not a tab")
	}
	idx := c.mustFindTabID(t)
	if idx == 0 {
		return errors.New("tab is already at the start of the list")
	}
	if c.tabs.MoveLeft(idx) {
		c.doRemoveTab(idx)
		c.doInsertTab(idx-1, t)
	}
	return nil
}

// MoveTabRight moves the tab in the given window to the right
// of the tabs list.
func (c *Component) MoveTabRight(win Window) error {
	bWin := win.(*browserWindow)
	t, ok := browserTabAtWindow(bWin)
	if !ok {
		return errors.New("window content is not a tab")
	}
	idx := c.mustFindTabID(t)
	if idx == c.tabs.Size()-1 {
		return errors.New("tab is already at the end of the list")
	}
	if c.tabs.MoveRight(idx) {
		c.doRemoveTab(idx)
		c.doInsertTab(idx+1, t)
	}
	return nil
}

// MoveTabTo moves the tab in the given window to the given position.
func (c *Component) MoveTabTo(win Window, idx int) error {
	bWin := win.(*browserWindow)
	t, ok := browserTabAtWindow(bWin)
	if !ok {
		return errors.New("window content is not a tab")
	}
	if idx > c.tabs.Size()-1 {
		idx = c.tabs.Size() - 1
	}
	curridx := c.mustFindTabID(t)
	if c.tabs.MoveTo(curridx, idx) {
		c.doRemoveTab(curridx)
		c.doInsertTab(idx, t)
		return nil
	}
	return errors.New("")
}

// PreviousTab updates win with the tab before the current tab.
func (c *Component) PreviousTab(win Window) bool {
	bWin := win.(*browserWindow)
	// already closed
	if bWin.parent == nil {
		return false
	}
	prev := bWin.win.Content().(browserapi.Handler)
	t, id := c.browserTabID(bWin)
	if t == nil {
		return c.updateWithNextFreeTab(win, prev)
	}
	for i := 0; i < len(c.buffers); i++ {
		if id == 0 {
			id = len(c.buffers) - 1
		} else {
			id--
		}
		if c.updateWindowTab(bWin, id, prev) {
			return true
		}
	}
	return false
}

// NextTab updates win with the tab after the current tab.
func (c *Component) NextTab(win Window) bool {
	bWin := win.(*browserWindow)
	// already closed
	if bWin.parent == nil {
		return false
	}
	prev := bWin.win.Content().(browserapi.Handler)
	t, id := c.browserTabID(bWin)
	if t == nil {
		return c.updateWithNextFreeTab(win, prev)
	}
	for i := 0; i < len(c.buffers); i++ {
		id++
		if id == len(c.buffers) {
			id = 0
		}
		if c.updateWindowTab(bWin, id, prev) {
			return true
		}
	}
	return false
}

// SetContentToTab updates win with the tab at the given index.
func (c *Component) SetContentToTab(win Window, tabIdx int) bool {
	bWin := win.(*browserWindow)
	if tabIdx < 0 {
		panic("negative tab index")
	}
	if tabIdx >= len(c.buffers) ||
		bWin.parent == nil {
		return false
	}
	prev := bWin.win.Content().(browserapi.Handler)
	return c.updateWindowTab(bWin, tabIdx, prev)
}

// RemoveAllTabs removes all tabs but the last one.
func (c *Component) RemoveAllTabs() {
	c.wm.Iterate(func(w thandler.Window) {
		win, ok := c.findWindow(w.ID())
		if !ok {
			panic("corrupted browser: could not find WindowManager window")
		}
		for c.RemoveWindowContent(win) {
		}
	})
}

// RemoveInactiveTabs removes all tabs that aren't used by any window.
// If no tabs were closed then false is returned.
func (c *Component) RemoveInactiveTabs() (removed bool) {
	for _, tab := range c.Tabs() {
		if !tab.free {
			// used by a window
			continue
		}
		removed = c.removeTab(tab) || removed
	}
	return
}

// Window returns the window with the given ID or false if there's
// no window with the given ID.
func (c *Component) Window(id uint64) (Window, bool) {
	ret, ok := c.findWindow(id)
	c.log(log.TraceLevel, "find window: %d, %v, %v", id, ret, ok)
	return ret, ok
}

// IterateWindows applies fn to each open browser window.
func (c *Component) IterateWindows(fn func(Window)) {
	c.wm.Iterate(func(w thandler.Window) {
		fn(&browserWindow{parent: c, win: w})
	})
}

// TileLayout returns the current tiled browser window tree layout.
func (c *Component) TileLayout() tcomponent.TileLayout {
	return c.wm.TileLayout()
}

// RestoreTileLayout replaces the current tiled layout and returns a map from
// old layout window IDs to newly allocated browser windows.
func (c *Component) RestoreTileLayout(
	layout tcomponent.TileLayout,
	content func(windowID uint64) browserapi.Handler,
) map[uint64]Window {
	c.windows = make(map[uint64]*browserWindow)
	c.prompts = make(map[string]Window)
	windows := c.wm.RestoreTileLayout(layout, func(windowID uint64) tui.Handler {
		h := content(windowID)
		wrapped, _ := c.newWindowContent(h)
		return wrapped
	})
	ret := make(map[uint64]Window, len(windows))
	for id, win := range windows {
		bwin := c.newWindow(win)
		ret[id] = bwin
		if tab, ok := bwin.win.Content().(*Tab); ok {
			tab.setWindow(nil, bwin)
			tab.callOnFocus()
		}
	}
	// Make sure every live wm window is tracked in c.windows. This
	// covers leaf nodes that wm.RestoreTileLayout omitted from its
	// returned map (e.g. layouts with WindowID == 0), so that
	// c.focus() can always resolve wm.Focus() to a *browserWindow.
	c.wm.Iterate(func(w thandler.Window) {
		if _, ok := c.findWindow(w.ID()); ok {
			return
		}
		c.newWindow(w)
	})
	c.dirtyTabs = true
	return ret
}

// RemoveWindowContent removes the content at win. It returns false
// if content was replaced with start handler because the content at win
// was the last content in this Component.
func (c *Component) RemoveWindowContent(win Window) bool {
	bwin := win.(*browserWindow)
	oldComponent := bwin.win.Content().(browserapi.Handler)
	t, isNotStartHandler := c.getFreeTab(oldComponent)
	oldComponent = c.updateWindowContent(bwin, t, nil /* don't store prev here */)
	oldTab, ok := oldComponent.(*Tab)
	if ok {
		c.removeTab(oldTab)
	}
	return isNotStartHandler
}

// RemoveTab removes the given tab and reports whether it removed a tab.
// It returns false if the given handler is not a tab or if the tab has
// already been removed.
func (c *Component) RemoveTab(h browserapi.Handler) bool {
	t, ok := h.(*Tab)
	if !ok {
		return false
	}
	if !t.free {
		_ = c.RemoveWindowContent(t.win)
		return true
	}
	return c.removeTab(t)
}

// SetDefaultSplit sets the default split to be used when Split
// is invoked with OrientationDefault.
func (c *Component) SetDefaultSplit(o browserapi.Orientation) browserapi.Orientation {
	if o == browserapi.OrientationDefault {
		panic("cannot set OrientationDefault as default orientation")
	}
	ret := c.nextSplit
	c.nextSplit = o
	return ret
}

// Split splits the current window in two and installs h to the orientation
// of the original content.
//
// Note that if h is not a handler created with NewTab
// the handler is cleaned as soon as the window's content is swapped.
func (c *Component) Split(
	o browserapi.Orientation, win Window, h browserapi.Handler,
) (Window, bool) {
	if win.(*browserWindow).parent == nil {
		panic("trying to split over a closed window")
	}
	// A tab is bound to at most one window
	if existing, ok := boundTabWindow(h); ok {
		return c.SetFocus(existing), true
	}
	if o == browserapi.OrientationDefault {
		o = c.nextSplit
	}
	switch o {
	case browserapi.OrientationRight:
		return c.splitRegular((*thandler.WindowManager).SplitVertical, win, h)
	case browserapi.OrientationLeft:
		return c.splitInverted((*thandler.WindowManager).SplitVertical, win, h)
	case browserapi.OrientationTop:
		return c.splitInverted((*thandler.WindowManager).SplitHorizontal, win, h)
	case browserapi.OrientationBottom:
		return c.splitRegular((*thandler.WindowManager).SplitHorizontal, win, h)
	default:
		panic("not a valid orientation")
	}
}

// SplitRoot creates a new top-level split in the root tiled layout.
func (c *Component) SplitRoot(alignment component.Alignment, h browserapi.Handler) (Window, bool) {
	if existing, ok := boundTabWindow(h); ok {
		return c.SetFocus(existing), true
	}
	h, isTab := c.newWindowContent(h)
	win, ok := c.wm.SplitRoot(alignment, h)
	if !ok {
		return nil, false
	}
	ret := c.newWindow(win)
	if isTab {
		c.dirtyTabs = true
		h.(*Tab).setWindow(nil, ret)
		h.(*Tab).callOnFocus()
	}
	return ret, true
}

// Floating opens a new floating window at the given coordinates,
// with the given height and width.
func (c *Component) Floating(
	h Floating, cfg browserapi.FloatingConfig,
) Window {
	if h == nil {
		panic("nil Floating handler")
	}
	h = c.newBrowserContent(h).(Floating)
	win := c.newWindow(c.wm.FloatingWindow(h, tcomponent.FloatingConfig{
		Alignment: cfg.Alignment,
		Offset:    cfg.Offset,
		NoBar:     cfg.NoWindowBar,
		Title:     cfg.Title,
	}))
	c.wm.SetFocus(win.win)
	return win
}

// Bar adds a bar to the orientation of the main window.
func (c *Component) Bar(cfg browserapi.BarConfig, h tui.Handler) {
	if cfg.Size <= 0 {
		panic("invalid bar size")
	}
	frame := (cfg.Frame == browserapi.BarFrameDefault && c.config.Frame) ||
		cfg.Frame == browserapi.BarFrameAlways

	if frame {
		f := handler.NewFrame(h)
		f.FrameCharSet = c.config.FrameCharSet
		f.Attributes = c.config.FrameAttr
		h = f
		cfg.Size += 2
	}

	if cfg.Orientation == browserapi.OrientationDefault {
		cfg.Orientation = c.nextSplit
	}
	// The entry is fully resolved so replaying it after a union rebuild
	// neither re-wraps the handler nor grows the size again.
	entry := barEntry{orientation: cfg.Orientation, size: cfg.Size, frame: frame, h: h}
	c.bars = append(c.bars, entry)
	c.unionBar(entry)
}

func (c *Component) unionBar(entry barEntry) {
	switch entry.orientation {
	case browserapi.OrientationTop:
		c.union.UnionTopFrame(entry.h, entry.size, entry.frame)
	case browserapi.OrientationBottom:
		c.union.UnionBottomFrame(entry.h, entry.size, entry.frame)
	case browserapi.OrientationLeft:
		c.union.UnionLeftFrame(entry.h, entry.size, entry.frame)
	case browserapi.OrientationRight:
		c.union.UnionRightFrame(entry.h, entry.size, entry.frame)
	}
}

func (c *Component) notify(level browserapi.NotificationLevel, msg string, args ...any) {
	_, _ = c.config.Notifications.Notify(level, msg, args...)
}

// Resize satisfies tui.Component
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
	c.union.Resize(width, height)
}

// Draw satisfies tui.Component
func (c *Component) Draw(w term.Writer) {
	if c.dirtyTabs {
		c.tabs.ResetFocus()
		for id, t := range c.buffers {
			c.tabs.SetIconAttr(id, c.nonFocusTabIconAttr())
			if !t.free {
				c.tabs.SetFocus(id)
			}
		}
		// SetFocus above also enables the highlight as a side effect;
		// reset it so the highlight is only drawn for the tab bound to
		// the focused window (mirroring the icon-attr logic below).
		c.tabs.ResetHighlight()

		if c.focusWindow != (thandler.Window{}) {
			win, ok := c.findWindow(c.focusWindow.ID())
			if ok {
				t, ok := browserTabAtWindow(win)
				if ok {
					// reset tab override attributes
					id := c.mustFindTabID(t)
					c.tabs.SetIconAttr(id, c.focusTabIconAttr())
					c.tabs.SetFocus(id)
				}
			}
		}
		c.dirtyTabs = false
	}

	if c.drawDragVeil(w) {
		return
	}
	c.drawContent(w)
}

// drawContent renders the window manager and its frame decorations.
func (c *Component) drawContent(w term.Writer) {
	c.union.Draw(w)

	// set correct attributes for focus window union charset
	c.overwriteFocusWindowUnion(w)
}

// SetDim sets whether next call to draw should use
// non-focus window diming feature.
func (c *Component) SetDim(to bool) bool {
	return c.wm.SetDim(to)
}

// SetDefaultAttr forwards the live theme default attributes to the window
// manager so grayscale dimming resolves a ColorDefault foreground.
func (c *Component) SetDefaultAttr(attr term.Attributes) {
	c.wm.SetDefaultAttr(attr)
}

// WindowManagerPosition returns the offset from the top left corner
// where the underlying window manager starts.
func (c *Component) WindowManagerPosition() term.Coordinates {
	return c.union.FrameUnion.MainPosition()
}

// WindowManagerSize returns the size of the window manager.
func (c *Component) WindowManagerSize() (width, height int) {
	return c.union.FrameUnion.MainWidth(), c.union.FrameUnion.MainHeight()
}

// DrawWindow draws target with the given term.Writer.
func (c *Component) DrawWindow(target Window, w term.Writer) {
	// emulate union draw
	pos := c.WindowManagerPosition()
	width, height := c.WindowManagerSize()
	vw := component.VirtualWriter{
		Writer: w,
		Offset: pos,
		Height: height,
		Width:  width,
	}
	c.wm.DrawWindow(target.(*browserWindow).win, &vw)
}

// ShiftFocus calls the underlying WindowManager.ShiftFocus.
func (c *Component) ShiftFocus() bool {
	return c.wm.ShiftFocus()
}

// Focus calls the underlying WindowManager.Focus.
func (c *Component) Focus() Window {
	return c.focus()
}

// FocusTab returns the Tab corresponding to the current window in focus,
// or false if the current window in focus is not drawing a Tab.
func (c *Component) FocusTab() (*Tab, bool) {
	w := c.focus()
	h, _ := w.Content()
	t, ok := h.(*Tab)
	return t, ok
}

// Shiftable calls the underlying WindowManager.Shiftable.
// It returns the Window that would become in focus if ShiftFocus
// is called.
func (c *Component) Shiftable() (Window, bool) {
	win, ok := c.wm.Shiftable()
	if !ok {
		return nil, false
	}

	w, ok := c.findWindow(win.ID())
	if !ok {
		panic("corrupted browser: cannot find focus window")
	}
	return w, true
}

// FocusDown calls the underlying WindowManager.FocusDown.
func (c *Component) FocusDown() bool {
	return c.wm.FocusDown()
}

// FocusLeft calls the underlying WindowManager.FocusLeft.
func (c *Component) FocusLeft() bool {
	return c.wm.FocusLeft()
}

// FocusRight calls the underlying WindowManager.FocusRight.
func (c *Component) FocusRight() bool {
	return c.wm.FocusRight()
}

// FocusUp calls the underlying WindowManager.FocusUp.
func (c *Component) FocusUp() bool {
	return c.wm.FocusUp()
}

// SwapContentDown calls the underlying WindowManager.SwapContentDown.
func (c *Component) SwapContentDown() bool {
	return c.swapContent((*thandler.WindowManager).SwapContentDown, thandler.Window.TileDown)
}

// SwapContentLeft calls the underlying WindowManager.SwapContentLeft.
func (c *Component) SwapContentLeft() bool {
	return c.swapContent((*thandler.WindowManager).SwapContentLeft, thandler.Window.TileLeft)
}

// SwapContentRight calls the underlying WindowManager.SwapContentRight.
func (c *Component) SwapContentRight() bool {
	return c.swapContent((*thandler.WindowManager).SwapContentRight, thandler.Window.TileRight)
}

// SwapContentUp calls the underlying WindowManager.SwapContentUp.
func (c *Component) SwapContentUp() bool {
	return c.swapContent((*thandler.WindowManager).SwapContentUp, thandler.Window.TileUp)
}

// swapContent rebinds the tabs the window manager moved, as it swaps
// content without knowing about tabs. A tab left bound to the window it
// moved out of is not freed when its new window closes, and focusing it
// then targets the closed window.
func (c *Component) swapContent(
	swap func(*thandler.WindowManager) bool,
	tile func(thandler.Window) (thandler.Window, bool),
) bool {
	if !swap(&c.wm) {
		return false
	}
	focus := c.wm.Focus()
	other, ok := tile(focus)
	if !ok {
		panic("corrupted browser: cannot find swapped window")
	}
	for _, w := range []thandler.Window{focus, other} {
		win, ok := c.findWindow(w.ID())
		if !ok {
			panic("corrupted browser: cannot find swapped window")
		}
		if t, ok := browserTabAtWindow(win); ok {
			t.setWindow(nil, win)
			t.callOnFocus()
		}
	}
	c.dirtyTabs = true
	return true
}

// ResetWindowSize resets width and height to be automatically calculated.
func (c *Component) ResetWindowSize() bool {
	w := c.wm.Focus()
	okh := c.wm.SetHeight(w, 0)
	okw := c.wm.SetWidth(w, 0)
	return okh || okw
}

// SetMaxWindowHeight maximizes the focus window's height.
func (c *Component) SetMaxWindowHeight() bool {
	w := c.wm.Focus()
	return c.wm.SetHeight(w, w.MaxHeight())
}

// SetMaxWindowWidth maximizes the focus window's width.
func (c *Component) SetMaxWindowWidth() bool {
	w := c.wm.Focus()
	return c.wm.SetWidth(w, w.MaxWidth())
}

// SetMinWindowHeight minimizes the focus window's height.
func (c *Component) SetMinWindowHeight() bool {
	w := c.wm.Focus()
	return c.wm.SetHeight(w, w.MinHeight())
}

// SetMinWindowWidth minimizes the focus window's width.
func (c *Component) SetMinWindowWidth() bool {
	w := c.wm.Focus()
	return c.wm.SetWidth(w, w.MinWidth())
}

// IncreaseWindowHeight increases the focus window's height.
func (c *Component) IncreaseWindowHeight() bool {
	w := c.wm.Focus()
	return c.wm.SetHeight(w, w.Height()+1)
}

// DecreaseWindowHeight decreases the focus window's height.
func (c *Component) DecreaseWindowHeight() bool {
	w := c.wm.Focus()
	return c.wm.SetHeight(w, w.Height()-1)
}

// IncreaseWindowWidth increases the focus window's width.
func (c *Component) IncreaseWindowWidth() bool {
	w := c.wm.Focus()
	return c.wm.SetWidth(w, w.Width()+1)
}

// DecreaseWindowWidth decreases the focus window's width.
func (c *Component) DecreaseWindowWidth() bool {
	w := c.wm.Focus()
	return c.wm.SetWidth(w, w.Width()-1)
}

// SetWindowWidth sets the given window's width exactly.
func (c *Component) SetWindowWidth(win Window, width int) bool {
	bwin := win.(*browserWindow)
	if bwin.parent == nil {
		return false
	}
	return c.wm.SetWidth(bwin.win, width)
}

// SetFocus sets the underlying WindowManager's focus to win.
func (c *Component) SetFocus(win Window) Window {
	bwin := win.(*browserWindow)
	if bwin.parent == nil {
		panic("trying to set focus to a closed window")
	}
	prev := c.wm.SetFocus(bwin.win)
	ret, ok := c.findWindow(prev.ID())
	if !ok {
		panic("could not find previously set focus")
	}
	return ret
}

// Handle proxies events to either the underlying Tabs or WindowManager.
func (c *Component) Handle(ev term.Event) (exit, handled bool) {
	return c.union.Handle(ev)
}

// uriHandler is the identity OnTabExit matches window content by.
type uriHandler interface {
	URI() workspaceapi.URI
}

// OnTabExit drops the terminal with the given uri.
func (c *Component) OnTabExit(uri workspaceapi.URI) bool {
	for _, t := range c.buffers {
		if t.uri.String() == uri.String() {
			return c.RemoveTab(t)
		}
	}
	for _, bw := range c.windows {
		h, err := bw.Content()
		if err != nil {
			continue
		}
		if u, ok := h.(uriHandler); ok && u.URI().String() == uri.String() {
			c.RemoveWindowContent(bw)
			return true
		}
	}
	return false
}

// Cursor calls the underlying FrameUnion's Cursor.
func (c *Component) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	return c.union.Cursor()
}

// Selection returns the underlying FrameUnion's selection.
func (c *Component) Selection() (string, bool) {
	return c.union.Selection()
}

// Prompt creates a new prompt to be drawn as an overlay on the next call to Draw
// and it also takes over event control until user either exits prompt or selects
// an option. The passed options and bindings must be equal in length, or bindings
// must be zero in length, meaning no key bindings are provided to user.
// Callers must ensure that there's coherence between options and bindings, otherwise
// this method panics.
//
// Prompt uses message to de-duplicate prompts. If a prompt is already open
// then this method returns the window used to display that prompt.
func (c *Component) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) Window {
	if len(options) == 0 || (len(bindings) != 0 && len(options) != len(bindings)) {
		panic("Prompt given invalid options and/or bindings")
	}
	if win, ok := c.prompts[message]; ok {
		return win
	}
	clearHandler := &clearOnClosePromptHandler{
		root:    promptHandler,
		c:       c,
		message: message,
	}
	promptConfig := handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message:              message,
			Options:              options,
			BackgroundAttributes: c.config.PromptConfig.BackgroundAttr,
			MinWidth:             c.config.PromptConfig.MinWidth,
			NewMessage: func(str string) component.Floating {
				mcfg := markdown.DefaultConfig()
				mcfg.HeaderPrefix = false
				mkd, err := markdown.NewWithConfig(str, mcfg)
				if err == nil {
					return component.NewAspectRatioFloatingResponsive(
						component.NewSpan(mkd, component.SpanConfig{
							PadHorizontal:    4,
							PadVertical:      2,
							ContentAlignment: component.AlignmentCentered,
						}), component.DefaultAspectRatio)
				}
				cfg := component.StringResponsiveConfig{
					NoSplitWords: true,
					StringConfig: component.StringConfig{
						PaddingVertical:   4,
						PaddingHorizontal: 4,
						Alignment:         component.AlignmentCentered,
					},
				}
				messageResponsive := component.NewResponsiveString(str, cfg)

				return component.NewAspectRatioFloatingResponsive(
					messageResponsive, component.DefaultAspectRatio)
			},
		},
		PromptHandler:  clearHandler,
		OptionBindings: bindings,
		OptionAttr:     c.config.PromptConfig.TextAttr,
		HighlightAttr:  c.config.PromptConfig.HighlightAttr,
	}

	prompt := handler.NewPrompt(promptConfig)
	floatingConfig := browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}

	win := c.Floating(prompt, floatingConfig)
	clearHandler.win = win
	c.prompts[message] = win
	return win
}

// Subscribe subscribes sub to window focus events.
func (c *Component) Subscribe(sub thandler.WindowSubscriber) {
	c.wm.Subscribe(sub)
}

// Tiles returns the number of tiled windows in this Component.
func (c *Component) Tiles() int {
	return c.wm.SizeTiles()
}

// FloatingWindows returns the number of floating windows in this Component.
func (c *Component) FloatingWindows() int {
	return c.wm.SizeFloating()
}

// CloseOtherWindows closes all the windows except the given window.
func (c *Component) CloseOtherWindows(win Window) (retErr error) {
	if win.IsFloating() {
		// Program output and prompts are floating windows that take
		// focus, and a workspace cannot be left holding only floating
		// windows. Keep a tile instead so the focused float is closed
		// along with everything else and the layout really is cleared.
		tile, ok := c.firstTile()
		if !ok {
			return errors.New("cannot close all tiled windows")
		}
		win = tile
	}
	var ok bool
	c.wm.Iterate(func(w thandler.Window) {
		if w.ID() == win.WindowID() {
			return
		}
		// Close through the browser window-close path so the tab binding
		// is released and c.windows is updated. Closing the raw handler
		// window would leave the tab marked bound to a removed window; a
		// later click on that stuck tab focuses a detached tile and
		// panics in TilePosition. Reuse the tracked browserWindow so the
		// double-close guard in browserWindow.Close stays effective: a fresh
		// browserWindow would overwrite c.windows and let a handler whose
		// Close callback closes its own captured window re-enter closeWindow
		// for an already-deleted window, panicking.
		bw, found := c.findWindow(w.ID())
		if !found {
			bw = c.newWindow(w)
		}
		if err := bw.Close(); err != nil {
			retErr = multierror.Append(retErr, err)
			return
		}
		ok = true
	})
	if retErr != nil {
		return retErr
	}
	if !ok {
		retErr = errors.New("no windows to close")
	}
	return
}

// firstTile returns the first tiled window in tree order, reusing the
// tracked browserWindow so the double-close guard in
// browserWindow.Close stays effective.
func (c *Component) firstTile() (*browserWindow, bool) {
	var found *browserWindow
	c.wm.Iterate(func(w thandler.Window) {
		if found != nil || w.IsFloating() {
			return
		}
		bw, ok := c.findWindow(w.ID())
		if !ok {
			bw = c.newWindow(w)
		}
		found = bw
	})
	return found, found != nil
}

// Close closes the resources associated with this browser.
func (c *Component) Close() (ret error) {
	for _, f := range c.buffers {
		err := f.Close()
		if err != nil {
			ret = err
		}
	}
	// Drop *Tab references so closed tabs can be GC'd immediately rather
	// than waiting for subsequent appends to overwrite the slots.
	clear(c.buffers)
	c.buffers = c.buffers[:0]
	c.shadedTabs.SetRunning(false)
	c.wm.UnsubscribeAll()

	c.wm.Iterate(func(w thandler.Window) {
		// Reuse the tracked browserWindow so the double-close guard in
		// browserWindow.Close (which nils parent) stays effective. Creating a
		// fresh browserWindow here would overwrite c.windows and let a handler
		// whose Close callback closes its own captured window re-enter
		// closeWindow for an already-deleted window, panicking.
		bw, ok := c.findWindow(w.ID())
		if !ok {
			bw = c.newWindow(w)
		}
		_ = bw.Close()
	})
	return ret
}

func (c *Component) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "browser.Component").Logf(level, msg, args...)
}

func (c *Component) setError(err error) {
	c.notify(browserapi.LevelError, "%s", err)
}

func (c *Component) focus() *browserWindow {
	win, ok := c.findWindow(c.wm.Focus().ID())
	if !ok {
		panic("corrupted browser: cannot find focus window")
	}
	return win
}

func (c *Component) closeTab(t *Tab) {
	err := t.Close()
	if err != nil {
		c.log(log.WarnLevel, "tab Close error: %v", err)
	}
}

func (c *Component) removeTab(t *Tab) bool {
	if !t.free {
		panic("trying to remove tab that is still attached to a window")
	}
	id, found := c.findTabID(t)
	if !found {
		return false
	}
	c.closeTab(t)
	c.doRemoveTab(id)
	ok := c.tabs.Remove(id)
	if !ok {
		panic(fmt.Sprintf("corrupted tabs: could not find tab with id %v", id))
	}
	if t.active {
		t.active = false
		c.tabActivityChanged()
	}
	return true
}

func (c *Component) doRemoveTab(idx int) {
	// Use slices.Delete so the now-unused tail slot is cleared. A bare
	// append(s[:idx], s[idx+1:]...) leaves the dropped *Tab pointer (which
	// transitively roots the file's cell.Buffer) in the backing array.
	c.buffers = slices.Delete(c.buffers, idx, idx+1)
}

func (c *Component) doInsertTab(idx int, t *Tab) {
	c.buffers = append(c.buffers, nil)
	copy(c.buffers[idx+1:], c.buffers[idx:])
	c.buffers[idx] = t
}

func (c *Component) findTabID(t *Tab) (int, bool) {
	for i, f := range c.buffers {
		if f == t {
			return i, true
		}
	}
	return -1, false
}

// mustFindTabID resolves a tab known to be present in the component. Use
// findTabID directly when the tab may have already been removed.
func (c *Component) mustFindTabID(t *Tab) int {
	id, ok := c.findTabID(t)
	if !ok {
		panic("could not find tab")
	}
	return id
}

func (c *Component) browserTabID(win *browserWindow) (
	*Tab, int,
) {
	t, ok := browserTabAtWindow(win)
	if !ok {
		return nil, 0
	}
	return t, c.mustFindTabID(t)
}

func (c *Component) updateWindowTab(
	win *browserWindow, tabID int, prev browserapi.Handler,
) bool {
	if tabID >= len(c.buffers) {
		panic(fmt.Sprintf("invalid tab at index: %d", tabID))
	}
	t := c.buffers[tabID]
	if t.free {
		c.updateWindowContent(win, t, prev)
		return true
	}
	return false
}

func (c *Component) updateWithNextFreeTab(win Window, prev browserapi.Handler) bool {
	freeBufs := c.freeTabs()
	if len(freeBufs) != 0 {
		c.updateWindowContent(win.(*browserWindow), c.buffers[freeBufs[0]], prev)
		return true
	}

	return false
}

func (c *Component) closeHandler(h browserapi.Handler) {
	err := h.Close()
	if err != nil {
		c.log(log.WarnLevel, "Close error: %v", err)
	}
	c.log(log.DebugLevel, "Component.closeHandler(%p)", h)
}

func (c *Component) tryUpdateWindowContent(
	win *browserWindow, content browserapi.Handler, prev browserapi.Handler,
) error {
	if b, ok := content.(*Tab); ok {
		if !b.free {
			return browserapi.ErrTabNotFree
		}
	}
	c.updateWindowContent(win, content, prev)
	return nil
}

func (c *Component) updateWindowContent(
	win *browserWindow, content browserapi.Handler, prev browserapi.Handler,
) browserapi.Handler {
	tab, ok := content.(*Tab)
	if ok {
		id := c.mustFindTabID(tab)
		c.tabs.SetFocus(id)
		c.dirtyTabs = true
		prevt, _ := prev.(*Tab)
		tab.setWindow(prevt, win)
	} else {
		_, sok := content.(*browserScrollableContent)
		_, bok := content.(*browserContent)
		_, fok := content.(*browserFloatingContent)
		_, fsok := content.(*browserFloatingScrollableContent)
		if !sok && !bok && !fok && !fsok {
			content = c.newBrowserContent(content)
		}
	}
	oldComponent := win.win.SetContent(content).(browserapi.Handler)
	// first call OnFree
	c.releaseHandler(oldComponent)
	// then call OnFocus if applicable
	if ok {
		tab.callOnFocus()
	}
	return oldComponent
}

// only tabs are able to be re-installed after content is updated.
func (c *Component) releaseHandler(h browserapi.Handler) {
	if t, ok := h.(*Tab); ok {
		c.dirtyTabs = true
		t.setFree()
	} else {
		c.closeHandler(h)
	}
}

func browserTabAtWindow(win *browserWindow) (*Tab, bool) {
	t, ok := win.win.Content().(*Tab)
	return t, ok
}

// boundTabWindow reports the window currently showing h when h is a tab
// already bound to a window, so callers can refuse to bind one tab to a
// second window.
func boundTabWindow(h browserapi.Handler) (Window, bool) {
	t, ok := h.(*Tab)
	if !ok || t.free {
		return nil, false
	}
	return t.Window()
}

func (c *Component) freeTabs() []int {
	freeBufs := make([]int, 0)
	for i, b := range c.buffers {
		if b.free {
			freeBufs = append(freeBufs, i)
		}
	}
	return freeBufs
}

// returns the next free tab or an empty Handler
func (c *Component) getFreeTab(hint tui.Handler) (browserapi.Handler, bool) {
	t, ok := hint.(*Tab)
	if ok && t.prev != nil && t.prev.free && slices.Contains(c.buffers, t.prev) {
		return t.prev, true
	}
	freeBufs := c.freeTabs()
	if len(freeBufs) == 0 {
		return c.wallpaper(), false
	}

	id := freeBufs[0]
	return c.buffers[id], true
}

func (c *Component) splitRegular(
	split func(*thandler.WindowManager, thandler.Window, tui.Handler) (thandler.Window, bool),
	splitWindow Window,
	newHandler browserapi.Handler,
) (*browserWindow, bool) {
	win := c.split(split, splitWindow, newHandler)
	if win == nil {
		return nil, false
	}
	c.wm.SetFocus(win.win)
	return win, true
}

func (c *Component) splitInverted(
	split func(*thandler.WindowManager, thandler.Window, tui.Handler) (thandler.Window, bool),
	splitWindow Window,
	newHandler browserapi.Handler,
) (*browserWindow, bool) {
	splitBrowserWin := splitWindow.(*browserWindow)
	splitHandlerWin := splitBrowserWin.win
	splitHandler := splitHandlerWin.Content()

	// perform a regular split
	newBrowserWin := c.split(split, splitWindow, newHandler)
	if newBrowserWin == nil {
		return nil, false
	}
	newHandlerWin := newBrowserWin.win
	newBrowserHandler := newHandlerWin.Content()

	// switch underlying handler.Window
	// so the new *browserWindow refers to the
	// original split target's handler.Window
	splitBrowserWin.win = newHandlerWin
	newBrowserWin.win = splitHandlerWin

	// switch content
	newBrowserWin.win.SetContent(newBrowserHandler)
	splitBrowserWin.win.SetContent(splitHandler)

	// ammend id mapping
	c.windows[newBrowserWin.WindowID()] = newBrowserWin
	c.windows[splitBrowserWin.WindowID()] = splitBrowserWin

	// return new instance of browser window
	// pointing to old instance of the split target window
	return newBrowserWin, true
}

func (c *Component) newWindowContent(h browserapi.Handler) (browserapi.Handler, bool) {
	if h == nil {
		h = c.wallpaper()
	}
	_, ok := h.(*Tab)
	if !ok {
		h = c.newBrowserContent(h)
	}
	return h, ok
}

func (c *Component) split(
	split func(*thandler.WindowManager, thandler.Window, tui.Handler) (thandler.Window, bool),
	splitWin Window, h browserapi.Handler,
) *browserWindow {
	h, isTab := c.newWindowContent(h)
	win, ok := split(&c.wm, splitWin.(*browserWindow).win, h)
	if !ok {
		return nil
	}
	ret := c.newWindow(win)
	if isTab {
		c.dirtyTabs = true
		h.(*Tab).setWindow(nil, ret)
		h.(*Tab).callOnFocus()
	}
	return ret
}

func (c *Component) tabsSize() int {
	if c.config.TabBarHeight != 0 {
		return c.config.TabBarHeight
	}
	if c.config.Frame {
		return 3
	}
	return 1
}

func (c *Component) newWindow(win thandler.Window) *browserWindow {
	browserWin := &browserWindow{
		parent: c,
		win:    win,
	}
	c.windows[browserWin.WindowID()] = browserWin
	c.log(log.TraceLevel, "new window: %d", win.ID())
	return browserWin
}

// closeWindow closes win or returns an error if win is the last Window.
func (c *Component) closeWindow(win *browserWindow) error {
	_, ok := c.findWindow(win.WindowID())
	if !ok {
		panic("window not found")
	}

	content := win.win.Content().(browserapi.Handler)
	err := win.win.Close()
	c.log(log.TraceLevel, "closing window: %d, %v", win.WindowID(), err)
	if err != nil {
		return err
	}

	delete(c.windows, win.WindowID())

	// ensure that if handler calls Closed to ensure that
	// window is closed, the answer will be correct.
	win.parent = nil
	c.releaseHandler(content)
	return nil
}

func (c *Component) findWindow(winID uint64) (*browserWindow, bool) {
	w, ok := c.windows[winID]
	return w, ok
}

func (c *Component) overwriteFocusWindowUnion(w term.Writer) {
	if !c.config.Frame || c.focusWindow == (thandler.Window{}) || c.wm.SizeTiles() == 1 {
		return
	}
	topleft := c.focusWindow.Position()
	mainPos := c.union.MainPosition()
	mainWidth := c.union.MainWidth()
	mainHeight := c.union.MainHeight()
	attr := c.config.WindowManagerConfig.FocusFrameAttr
	cs := c.config.WindowManagerConfig.FocusFrameCharSet

	if topleft == (term.Coordinates{}) {
		cell := term.NewCell(cs.TopLeft, 1, attr)
		w.SetCell(mainPos, cell)
	}

	topright := term.Coordinates{X: topleft.X + c.focusWindow.Width(), Y: topleft.Y}
	if topright == (term.Coordinates{X: mainWidth, Y: 0}) {
		cell := term.NewCell(cs.TopRight, 1, attr)
		pos := term.Coordinates{X: mainPos.X + mainWidth - 1, Y: mainPos.Y}
		w.SetCell(pos, cell)
	}

	bottomleft := term.Coordinates{X: topleft.X, Y: topleft.Y + c.focusWindow.Height()}
	if bottomleft == (term.Coordinates{Y: mainHeight, X: 0}) {
		cell := term.NewCell(cs.BottomLeft, 1, attr)
		pos := term.Coordinates{X: mainPos.X, Y: mainPos.Y + mainHeight - 1}
		w.SetCell(pos, cell)
	}

	bottomright := term.Coordinates{
		X: topleft.X + c.focusWindow.Width(),
		Y: topleft.Y + c.focusWindow.Height(),
	}
	if bottomright == (term.Coordinates{Y: mainHeight, X: mainWidth}) {
		cell := term.NewCell(cs.BottomRight, 1, attr)
		pos := term.Coordinates{
			X: mainPos.X + mainWidth - 1,
			Y: mainPos.Y + mainHeight - 1,
		}
		w.SetCell(pos, cell)
	}
}

func (c *Component) wallpaper() browserapi.Handler {
	wallpaper := c.config.Wallpaper
	if wallpaper.NewComponent == nil {
		wallpaper.NewComponent = component.Nop
	}
	instance := wallpaper.NewComponent()
	// if background attrs were passed try to re-construct string wallpaper
	// or set a background via component.Background.
	if wallpaper.BackgroundAttr != (term.Attributes{}) {
		if str, ok := instance.(component.String); ok {
			cfg := str.Config()
			cfg.BackgroundAttributes = wallpaper.BackgroundAttr
			cfg.Attributes.Bg = wallpaper.BackgroundAttr.Bg
			instance = component.NewStringWithConfig(str.String(), cfg)
		} else {
			// activate override behaviour
			nonZeroCh := ' '
			instance = component.NewBackground(instance,
				term.NewCell(nonZeroCh, 0, wallpaper.BackgroundAttr))
		}
	}
	// make wallpaper satisfy Floating to avoid browserContent panic
	// if wallpaper is being set as a default on a floating window
	floating := component.StaticFloating(instance, 80, 40)
	return &browserContent{
		Handler: NopFloatingHandler(handler.NopFloatingFromComponent(floating)),
		c:       c,
	}
}

func (c *Component) newBrowserContent(content browserapi.Handler) browserapi.Handler {
	bc := browserContent{c: c, Handler: content}
	_, fok := content.(component.Floating)
	_, sok := content.(component.Scrollable)
	if sok && fok {
		return &browserFloatingScrollableContent{browserContent: bc}
	}
	if fok {
		return &browserFloatingContent{browserContent: bc}
	}
	if sok {
		return &browserScrollableContent{browserContent: bc}
	}
	return &bc
}

func (c *Component) focusTabIconAttr() term.Attributes {
	focusIconAttr := c.config.FocusTabIconAttr
	focusIconAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	return focusIconAttr
}

func (c *Component) nonFocusTabIconAttr() term.Attributes {
	nonFocusIconAttr := c.config.NonFocusTabIconAttr
	nonFocusIconAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	return nonFocusIconAttr
}

func (c *Component) unwrapContent(content browserapi.Handler) browserapi.Handler {
	bsc, ok := content.(*browserScrollableContent)
	if ok {
		return bsc.Handler
	}
	bc, ok := content.(*browserContent)
	if ok {
		return bc.Handler
	}
	bfc, ok := content.(*browserFloatingContent)
	if ok {
		return bfc.Handler
	}
	bfsc, ok := content.(*browserFloatingScrollableContent)
	if ok {
		return bfsc.Handler
	}
	panic("extraneous content")
}

// clearOnClosePromptHandler drops the prompt-dedup entry before
// running the root callbacks: a callback may reopen the same prompt,
// and a stale entry would dedupe that reopen against the window
// being closed. The entry is only dropped while it still points at
// this handler's own window, so a dying window's OnClose cannot
// evict an entry that an earlier OnSelect reopen just registered.
type clearOnClosePromptHandler struct {
	c       *Component
	message string
	root    handler.PromptHandler
	win     Window
}

func (c *clearOnClosePromptHandler) clear() {
	if cur, ok := c.c.prompts[c.message]; ok && cur == c.win {
		delete(c.c.prompts, c.message)
	}
}

func (c *clearOnClosePromptHandler) OnSelect(idx int, option string) {
	c.clear()
	c.root.OnSelect(idx, option)
}

func (c *clearOnClosePromptHandler) OnClose() error {
	c.clear()
	return c.root.OnClose()
}

// component.WindowManager sinchronously removes tui.Handlers
// upon returning exit=true on calls to Handle. This
// structure is used to call Close when this occurs.
type browserContent struct {
	browserapi.Handler
	closed bool
	c      *Component
}

// allow for advanced use of content
func (c *browserContent) Content() browserapi.Handler {
	return c.Handler
}

func (c *browserContent) Handle(ev term.Event) (exit, handled bool) {
	prev := c.c.focusWindow.Content()
	win := c.c.focusWindow
	exit, handled = c.Handler.Handle(ev)
	if exit {
		c.c.closeHandler(c)
		content := win.Content()
		// if previous content is not the same as the new content, then it must
		// mean that the underlying Handler swapped the content before exiting.
		// This window is going away (see handler.WindowManager) so make sure that
		// a non-ephemeral handler (Tab), is set free.
		if prev != content {
			if t, ok := content.(*Tab); ok {
				c.c.dirtyTabs = true
				t.setFree()
			}
		}
	}
	return
}

func (c *browserContent) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.Handler.Close()
}

var _ Floating = (*browserFloatingContent)(nil)

type browserFloatingContent struct {
	browserContent
}

func (c *browserFloatingContent) Dimensions() (int, int) {
	return c.Handler.(Floating).Dimensions()
}

var _ Floating = (*browserFloatingScrollableContent)(nil)
var _ Scrollable = (*browserFloatingScrollableContent)(nil)

type browserFloatingScrollableContent struct {
	browserContent
}

func (c *browserFloatingScrollableContent) Dimensions() (int, int) {
	return c.Handler.(Floating).Dimensions()
}

func (c *browserFloatingScrollableContent) SeekUp() bool {
	return c.Handler.(component.Scrollable).SeekUp()
}

func (c *browserFloatingScrollableContent) SeekDown() bool {
	return c.Handler.(component.Scrollable).SeekDown()
}

func (c *browserFloatingScrollableContent) SeekOffset() int {
	return c.Handler.(component.Scrollable).SeekOffset()
}

func (c *browserFloatingScrollableContent) MaxSeekOffset() int {
	return c.Handler.(component.Scrollable).MaxSeekOffset()
}

var _ Scrollable = (*browserScrollableContent)(nil)

type browserScrollableContent struct {
	browserContent
}

func (c *browserScrollableContent) SeekUp() bool {
	return c.Handler.(component.Scrollable).SeekUp()
}

func (c *browserScrollableContent) SeekDown() bool {
	return c.Handler.(component.Scrollable).SeekDown()
}

func (c *browserScrollableContent) SeekOffset() int {
	return c.Handler.(component.Scrollable).SeekOffset()
}

func (c *browserScrollableContent) MaxSeekOffset() int {
	return c.Handler.(component.Scrollable).MaxSeekOffset()
}

// satisfies handler.WindowSubscriber to
// override union attrs of focus window
type wmSubscriber Component

func (s *wmSubscriber) OnFocus(prev, focus thandler.Window) {
	c := (*Component)(s)
	c.focusWindow = focus
	c.dirtyTabs = true
}

// OnBarClose routes window-bar close icon clicks through closeWindow so
// tab and handler release bookkeeping runs, mirroring tab close and
// :close. Returning false for unknown windows lets the window manager
// fall back to closing the window directly.
func (c *Component) OnBarClose(win thandler.Window) bool {
	bw, ok := c.findWindow(win.ID())
	if !ok {
		return false
	}
	if err := c.closeWindow(bw); err != nil {
		c.setError(err)
	}
	return true
}

// OnBarDrag satisfies handler.FloatingBarHandler by keeping the
// pre-split preview in sync with the cursor.
func (c *Component) OnBarDrag(win thandler.Window, pos term.Coordinates) {
	src, ok := c.findWindow(win.ID())
	if !ok {
		c.clearWinDropPreview()
		return
	}
	c.updateWinDropPreview(src, pos)
}

// OnBarDragCancel satisfies handler.FloatingBarHandler.
func (c *Component) OnBarDragCancel(thandler.Window) {
	c.clearWinDropPreview()
}

// OnBarDrop converts the dragged float's content into a tab and
// installs it in the previewed region. It reports false when the
// release lands outside any drop zone, leaving the drag a plain move.
func (c *Component) OnBarDrop(win thandler.Window, pos term.Coordinates) bool {
	src, ok := c.findWindow(win.ID())
	if !ok {
		c.clearWinDropPreview()
		return false
	}
	c.updateWinDropPreview(src, pos)

	st := c.winDrop
	dst := st.target
	if st.zone != winDropCenter {
		dst = st.preview
	}
	if st.zone == winDropNone || dst == nil || dst.Closed() {
		c.clearWinDropPreview()
		return false
	}

	tab, _ := c.NewTabFromContent(winDropTabIcon, c.winDropTabName(src), src)
	// Release the preview before closing the float so the teardown
	// below cannot reclaim the window we are about to fill.
	c.winDrop = winDropState{}
	c.stopVeil()

	if err := src.Close(); err != nil {
		c.setError(err)
		return false
	}
	if err := dst.SetContent(tab); err != nil {
		c.setError(err)
		return false
	}
	c.SetFocus(dst)
	c.interrupt()
	return true
}

// winDropTabName names the tab created from a dropped float, preferring
// the bar title and falling back to the content's resource basename.
func (c *Component) winDropTabName(src *browserWindow) string {
	if title := src.win.Title(); title != "" {
		return title
	}
	content, err := src.Content()
	if err != nil {
		return winDropDefaultTabName
	}
	if urier, ok := content.(interface{ URI() workspaceapi.URI }); ok {
		if base := path.Base(urier.URI().Path()); base != "" &&
			base != "." && base != "/" {
			return base
		}
	}
	return winDropDefaultTabName
}

// updateWinDropPreview resolves the drop zone under pos and installs
// the matching pre-split preview, tearing down any stale one.
func (c *Component) updateWinDropPreview(src *browserWindow, pos term.Coordinates) {
	if c.holdsWinDropPreview(pos) {
		return
	}
	target, region, local, ok := c.winDropTarget(src, pos)
	if !ok {
		c.clearWinDropPreview()
		return
	}
	zone := dropZoneAt(local, region.w, region.h)
	if target == c.winDrop.target && zone == c.winDrop.zone {
		return
	}

	c.clearWinDropPreview()
	// The teardown above closed the placeholder, which is itself a
	// tile the layout may have moved under the cursor since it was
	// created, so target is only known to be live before that call.
	if zone == winDropNone || target.Closed() {
		return
	}
	c.winDrop = winDropState{
		src:    src,
		target: target,
		region: region,
		zone:   zone,
	}
	if zone != winDropCenter {
		preview, ok := c.Split(zone.orientation(), target, nil)
		if !ok {
			c.winDrop = winDropState{}
			return
		}
		c.winDrop.preview = preview.(*browserWindow)
		// Split focuses the new window; the dragged float must keep
		// its focus frame for the duration of the drag.
		c.SetFocus(src)
	}
	c.startVeil()
	c.interrupt()
}

// holdsWinDropPreview reports whether pos sits on the placeholder but
// outside the region the current zone was measured against. Inserting
// the placeholder as a new sibling redistributes the others, and later
// relayouts move it again, so it can drift off that region while the
// cursor is still on what the veil highlights. Re-resolving there would
// pick the placeholder as the next target and split the very window the
// teardown is about to close.
func (c *Component) holdsWinDropPreview(pos term.Coordinates) bool {
	st := c.winDrop
	if st.zone == winDropNone || st.preview == nil || st.preview.Closed() {
		return false
	}
	return !st.region.contains(pos) && windowRect(st.preview).contains(pos)
}

// winDropTarget returns the tile the preview applies to, the region the
// zone is measured against, and pos as a coordinate local to it.
func (c *Component) winDropTarget(src *browserWindow, pos term.Coordinates) (
	target *browserWindow, region winDropRect, local term.Coordinates, ok bool,
) {
	if st := c.winDrop; st.zone != winDropNone &&
		st.target != nil && !st.target.Closed() && st.region.contains(pos) {
		return st.target, st.region, st.region.local(pos), true
	}
	win, ok := c.wm.TileAt(pos)
	if !ok {
		return nil, winDropRect{}, term.Coordinates{}, false
	}
	target, ok = c.findWindow(win.ID())
	if !ok || target == src {
		return nil, winDropRect{}, term.Coordinates{}, false
	}
	region = windowRect(target)
	return target, region, region.local(pos), true
}

// clearWinDropPreview removes the pre-split placeholder and restores
// the layout and focus the drag found.
func (c *Component) clearWinDropPreview() {
	st := c.winDrop
	if st.zone == winDropNone && st.preview == nil {
		return
	}
	c.winDrop = winDropState{}
	if st.preview != nil && !st.preview.Closed() {
		if err := st.preview.Close(); err != nil {
			c.setError(err)
		}
	}
	if st.src != nil && !st.src.Closed() {
		c.SetFocus(st.src)
	}
	c.stopVeil()
	c.interrupt()
}

// winDropVeil returns the rect and label of the window-drag preview.
func (c *Component) winDropVeil() (win *browserWindow, label string, ok bool) {
	st := c.winDrop
	switch st.zone {
	case winDropNone:
		return nil, "", false
	case winDropCenter:
		return st.target, winDropConvertLabel, st.target != nil
	default:
		return st.preview, winDropSplitLabel, st.preview != nil
	}
}

// SetInterrupter installs the interrupter used to animate the
// drop-target veil. Without one the veil is still drawn, but only
// repaints when the host redraws for another reason.
func (c *Component) SetInterrupter(i term.Interrupter) {
	c.interrupter = i
	c.shadedTabs.SetInterrupter(i)
	c.shadedTabs.SetRunning(c.HasActiveTabs())
}

// WindowAt returns the window rendered at pos, which is relative to
// this Component's top-left corner.
func (c *Component) WindowAt(pos term.Coordinates) (Window, bool) {
	off := c.WindowManagerPosition()
	pos.X -= off.X
	pos.Y -= off.Y
	if pos.X < 0 || pos.Y < 0 {
		return nil, false
	}
	win, ok := c.wm.WindowAt(pos)
	if !ok {
		return nil, false
	}
	bwin, ok := c.findWindow(win.ID())
	if !ok {
		return nil, false
	}
	return bwin, true
}

// DragHover marks the window under pos as the pending drop target and
// starts the veil animation. It reports whether a window was found;
// when none is, any previous target is cleared.
func (c *Component) DragHover(pos term.Coordinates) bool {
	win, ok := c.WindowAt(pos)
	if !ok {
		c.DragCancel()
		return false
	}
	bwin := win.(*browserWindow)
	if c.drag.win == bwin {
		return true
	}
	c.drag.win = bwin
	c.startVeil()
	c.interrupt()
	return true
}

// DragCancel clears the drop target and stops the veil animation.
func (c *Component) DragCancel() {
	if c.drag.win == nil && c.drag.cancel == nil {
		return
	}
	c.drag.win = nil
	c.stopVeil()
	c.interrupt()
}

// DragDrop delivers paths to the window under pos as a bracketed paste,
// focusing that window first so the paste reaches it even when another
// window holds the focus. It reports whether a window received the drop.
func (c *Component) DragDrop(pos term.Coordinates, paths []string) bool {
	c.DragCancel()
	if len(paths) == 0 {
		return false
	}
	win, ok := c.WindowAt(pos)
	if !ok {
		return false
	}
	c.SetFocus(win)
	c.Handle(term.Event{Type: term.EventPasteStart})
	for _, r := range strings.Join(paths, "\n") {
		c.Handle(term.Event{
			Type: term.EventKey,
			Ch:   r,
			Raw:  []byte(string(r)),
		})
	}
	c.Handle(term.Event{Type: term.EventPasteEnd})
	return true
}

// startVeil starts the shared pulse ticker used by both the file-drop
// veil and the window-drag preview veil.
func (c *Component) startVeil() {
	if c.interrupter == nil || c.drag.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.drag.cancel = cancel
	interrupter := c.interrupter
	frame := &c.drag.frame
	go debug.CapturePanicReport(func() {
		ticker := time.NewTicker(time.Second / dragVeilFPS)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				frame.Add(1)
				_ = interrupter.Interrupt(ctx)
			case <-ctx.Done():
				return
			}
		}
	})
}

// stopVeil stops the shared pulse ticker once neither veil needs it.
func (c *Component) stopVeil() {
	if c.drag.win != nil || c.winDrop.zone != winDropNone {
		return
	}
	if c.drag.cancel != nil {
		c.drag.cancel()
		c.drag.cancel = nil
	}
	c.drag.frame.Store(0)
}

func (c *Component) interrupt() {
	if c.interrupter == nil {
		return
	}
	_ = c.interrupter.Interrupt(context.Background())
}

// drawDragVeil renders the browser with a pulsing veil over the file
// drop target or the window-drag preview region. It reports false when
// neither is active, leaving the caller to draw normally.
func (c *Component) drawDragVeil(w term.Writer) bool {
	win, label, exclude := c.veilTarget()
	if win == nil || win.Closed() {
		return false
	}
	width, height := win.Width(), win.Height()
	if width <= 0 || height <= 0 || c.width <= 0 || c.height <= 0 {
		return false
	}
	if c.drag.buf == nil || c.drag.bufW != c.width || c.drag.bufH != c.height {
		c.drag.buf = cell.NewBufferWriter(context.Background(), c.width, c.height)
		c.drag.bufW, c.drag.bufH = c.width, c.height
	}
	c.drag.buf.SetContext(w.Context())
	_ = c.drag.buf.Clear(term.Attributes{})
	c.drawContent(c.drag.buf)

	pos := win.Position()
	off := c.WindowManagerPosition()
	pos.X += off.X
	pos.Y += off.Y

	var skip *veilRect
	if exclude != nil && !exclude.Closed() {
		epos := exclude.Position()
		epos.X += off.X
		epos.Y += off.Y
		skip = &veilRect{pos: epos, width: exclude.Width(), height: exclude.Height()}
	}

	cells := c.drag.buf.RawCells()
	veilCells(cells, pos, width, height, c.config.DropTargetAttr, skip)
	writeCenteredLabel(cells, pos, width, height, label,
		c.config.DropTargetLabelAttr)
	shader.Virtual(shader.Pulse(shader.PulseParams{
		Color:        c.config.DropTargetAttr.Fg,
		PeriodFrames: dragVeilPeriodFrame,
		Intensity:    dragVeilIntensity,
	}, c.config.DropTargetAttr), pos, width, height).
		Shade(int(c.drag.frame.Load()), 0, cells)

	for y, row := range cells {
		for x, cl := range row {
			w.SetCell(term.Coordinates{X: x, Y: y}, cl)
		}
	}
	return true
}

// veilTarget resolves which window the veil covers, its label, and the
// window whose cells must stay untinted where they overlap it.
func (c *Component) veilTarget() (win *browserWindow, label string, exclude *browserWindow) {
	if win, label, ok := c.winDropVeil(); ok {
		return win, label, c.winDrop.src
	}
	if c.drag.win == nil {
		return nil, "", nil
	}
	return c.drag.win, c.dropLabel(c.drag.win), nil
}

// dropLabel returns the veil message for the target window, keyed by
// the scheme of the tab it renders.
func (c *Component) dropLabel(win *browserWindow) string {
	if t, ok := browserTabAtWindow(win); ok {
		if label, ok := c.config.DropTargetLabels[t.URI().Scheme()]; ok {
			return label
		}
	}
	if label, ok := c.config.DropTargetLabels[""]; ok {
		return label
	}
	return defaultDropLabel
}
