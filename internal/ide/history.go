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
	"fmt"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idetask"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/workspace"
)

// exSnapshotter satisfies idehistory.Snapshotter by reading from an
// ex's components.
type exSnapshotter struct {
	ex *ex
	// wh is the workspace the ex belongs to, when there is one. It is
	// read live rather than captured because the name changes under
	// the subscription, via workspacerename.
	wh *workspaceHandler
}

func (s exSnapshotter) Name() string {
	if s.wh == nil {
		return ""
	}
	return s.wh.tabname
}

// Terminals snapshots every open terminal in the ex into
// idehistory.TerminalSession records.
func (s exSnapshotter) Terminals() []idehistory.TerminalSession {
	e := s.ex
	tabs, terminals := openTerminalSessionWindowsForState(e)
	var ret []idehistory.TerminalSession
	for _, tab := range e.comp.Tabs() {
		terminal, ok := tab.Handler().(vtereservoir.VTE)
		if !ok {
			continue
		}
		window := tabs[tab]
		if !window.visible {
			window.tab = true
		}
		snap, err := terminal.Snapshot()
		if err != nil {
			continue
		}
		if snap.Title == "" {
			snap.Title = openTerminalSessionTitle(e, tab, terminal, "")
		}
		ret = append(ret, idehistory.TerminalSession{
			Name:     "", // auto-generated at restore time
			Snapshot: snap,
			Tab:      window.tab,
			Visible:  window.visible,
			Focus:    window.focus,
			WindowID: window.windowID,
		})
	}
	for _, win := range terminals {
		snap, err := win.terminal.Snapshot()
		if err != nil {
			continue
		}
		if snap.Title == "" {
			snap.Title = openTerminalSessionTitle(e, nil, win.terminal, "")
		}
		ret = append(ret, idehistory.TerminalSession{
			Snapshot: snap,
			Tab:      win.window.tab,
			Visible:  win.window.visible,
			Focus:    win.window.focus,
			WindowID: win.window.windowID,
		})
	}
	return ret
}

// Tasks snapshots every running task in the ex.
func (s exSnapshotter) Tasks() []idehistory.TaskSession {
	e := s.ex
	if e.tasks == nil {
		return nil
	}
	var ret []idehistory.TaskSession
	for _, info := range e.tasks.ListTasks() {
		if len(info.CmdAndArgs) == 0 {
			continue
		}
		ret = append(ret, idehistory.TaskSession{
			TaskName:                 info.Name,
			Filter:                   info.Filter,
			Cmd:                      info.CmdAndArgs[0],
			Args:                     append([]string(nil), info.CmdAndArgs[1:]...),
			MinimizeAlignment:        info.MinimizeAlignment,
			WindowID:                 info.WindowID,
			WindowMinimized:          info.WindowMinimized,
			WindowMinimizedAlignment: info.WindowMinimizedAlignment,
		})
	}
	return ret
}

func (s exSnapshotter) Layout() (tcomponent.TileLayout, bool) {
	layout := s.ex.comp.Browser().TileLayout()
	return layout, true
}

func (s exSnapshotter) FileWindowIDs() map[string]uint64 {
	ret := make(map[string]uint64)
	s.ex.comp.Browser().IterateWindows(func(win browser.Window) {
		content, err := win.Content()
		if err != nil {
			return
		}
		tab, ok := content.(*browser.Tab)
		if !ok {
			return
		}
		if _, ok := tab.Closer().(workspace.FlusherCloser); !ok {
			return
		}
		ret[tab.URI().String()] = win.WindowID()
	})
	return ret
}

// ExtensionTabs snapshots the tiled windows showing a tab that the resource
// opener of its URI's scheme can reopen, or a placeholder still waiting
// for that opener. Tabs that are only in the tab bar are not captured, and
// neither are workspace files, which are restored as files even if an
// extension registered an opener for their scheme.
func (s exSnapshotter) ExtensionTabs() []idehistory.ExtensionTab {
	var ret []idehistory.ExtensionTab
	b := s.ex.comp.Browser()
	b.IterateWindows(func(win browser.Window) {
		if win.IsFloating() {
			return
		}
		content, err := win.Content()
		if err != nil {
			return
		}
		tab, ok := content.(*browser.Tab)
		if !ok {
			return
		}
		if _, ok := tab.Closer().(workspace.FlusherCloser); ok {
			return
		}
		uri := tab.URI()
		_, hasOpener := s.ex.comp.ResourceOpener(uri.Scheme())
		if !hasOpener && !s.ex.comp.PendingTabs().Contains(uri) {
			return
		}
		_, icon, _ := b.TabIcon(uri)
		_, name, _ := b.TabName(uri)
		focus, _ := win.Focus()
		ret = append(ret, idehistory.ExtensionTab{
			URI:      uri,
			Icon:     icon,
			Name:     name,
			WindowID: win.WindowID(),
			Focus:    focus,
		})
	})
	return ret
}

type openTerminalWindow struct {
	tab      bool
	visible  bool
	focus    bool
	windowID uint64
}

type openTerminalWindowTerminal struct {
	terminal vtereservoir.VTE
	window   openTerminalWindow
}

func openTerminalSessionWindowsForState(e *ex) (
	map[*browser.Tab]openTerminalWindow, []openTerminalWindowTerminal,
) {
	tabs := make(map[*browser.Tab]openTerminalWindow)
	var terminals []openTerminalWindowTerminal
	e.comp.Browser().IterateWindows(func(win browser.Window) {
		if win.IsFloating() {
			return
		}
		content, err := win.Content()
		if err != nil {
			return
		}
		focus, _ := win.Focus()
		window := openTerminalWindow{
			visible:  true,
			focus:    focus,
			windowID: win.WindowID(),
		}
		if tab, ok := content.(*browser.Tab); ok {
			if _, ok := tab.Handler().(vtereservoir.VTE); ok {
				window.tab = true
				tabs[tab] = window
			}
			return
		}
		terminal, ok := content.(vtereservoir.VTE)
		if !ok {
			return
		}
		terminals = append(terminals, openTerminalWindowTerminal{
			terminal: terminal,
			window:   window,
		})
	})
	return tabs, terminals
}

func openTerminalSessionTitle(
	e *ex,
	tab *browser.Tab,
	terminal vtereservoir.VTE,
	fallback string,
) string {
	if tab != nil {
		name, _, ok := e.comp.Browser().TabName(tab.URI())
		if ok && name != "" {
			return name
		}
	}
	if title := terminal.Title(); title != "" {
		return title
	}
	return fallback
}

// restoreOpenTerminalSessions restores the terminal sessions from state
// into the given ex, mirroring the legacy two-pass restore: first into
// any pre-existing windows mapped by WindowID, then sequentially split
// the remaining visible ones, and finally hydrate any hidden tab
// snapshots.
func restoreOpenTerminalSessions(
	e *ex, sessions []idehistory.TerminalSession,
	windows map[uint64]browser.Window,
) error {
	ret := new(multierror.Error)
	if len(sessions) == 0 {
		return nil
	}
	// auto-assign deterministic names for sessions missing one.
	for i := range sessions {
		if sessions[i].Name == "" {
			sessions[i].Name = fmt.Sprintf("__terminal-%06d", i)
		}
	}
	restored := make(map[string]bool)
	var focus browser.Window
	for _, doc := range sessions {
		if len(windows) == 0 || !doc.Visible || doc.WindowID == 0 {
			continue
		}
		win, ok := windows[doc.WindowID]
		if !ok {
			continue
		}
		content, err := newTerminalSessionContentFromState(e, doc)
		if err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("restore open terminal session %q: %w", doc.Name, err))
			continue
		}
		if err := win.SetContent(content); err != nil {
			_ = content.Close()
			ret = multierror.Append(ret,
				fmt.Errorf("restore open terminal session %q: %w", doc.Name, err))
			continue
		}
		restored[doc.Name] = true
		if doc.Focus {
			focus = win
		}
	}
	if focus != nil {
		e.comp.Browser().SetFocus(focus)
	}
	visibleDocs := make([]idehistory.TerminalSession, 0, len(sessions))
	for _, doc := range sessions {
		if restored[doc.Name] || !doc.Visible {
			continue
		}
		visibleDocs = append(visibleDocs, doc)
	}
	ret = multierror.Append(ret, restoreOpenTerminalSessionsSequential(e, visibleDocs))
	ret = multierror.Append(ret, restoreHiddenOpenTerminalSessionTabs(e, sessions))
	return ret.ErrorOrNil()
}

func restoreHiddenOpenTerminalSessionTabs(
	e *ex, sessions []idehistory.TerminalSession,
) error {
	ret := new(multierror.Error)
	for _, doc := range sessions {
		if doc.Visible || !doc.Tab {
			continue
		}
		if _, err := newTerminalSessionContentFromState(e, doc); err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("restore open terminal session %q: %w", doc.Name, err))
		}
	}
	return ret.ErrorOrNil()
}

func restoreOpenTerminalSessionsSequential(
	e *ex, sessions []idehistory.TerminalSession,
) error {
	ret := new(multierror.Error)
	var focus browser.Window
	for _, doc := range sessions {
		win, err := restoreOpenTerminalSessionSequential(e, doc, focus)
		if err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("restore open terminal session %q: %w", doc.Name, err))
			continue
		}
		if win != nil && doc.Focus {
			focus = win
		}
	}
	if focus != nil {
		e.comp.Browser().SetFocus(focus)
	}
	return ret.ErrorOrNil()
}

func restoreOpenTerminalSessionSequential(
	e *ex, doc idehistory.TerminalSession, prev browser.Window,
) (browser.Window, error) {
	content, err := newTerminalSessionContentFromState(e, doc)
	if err != nil {
		return nil, err
	}
	if !doc.Visible {
		return nil, nil
	}
	if prev == nil {
		win := e.invokeWindow()
		if err := win.SetContent(content); err != nil {
			return nil, err
		}
		return win, nil
	}
	win, err := e.comp.Split(browserapi.OrientationDefault, prev, content)
	if err != nil {
		return nil, err
	}
	return win, nil
}

func newTerminalSessionContentFromState(
	e *ex, doc idehistory.TerminalSession,
) (browserapi.Handler, error) {
	h, err := e.newTerminalSessionHandler(doc.Name, doc.Snapshot)
	if err != nil {
		return nil, err
	}
	if !doc.Tab {
		return h, nil
	}
	tab, err := newTerminalSessionTabFromState(e, doc, h)
	if err != nil {
		return nil, err
	}
	return tab, nil
}

func newTerminalSessionTabFromState(
	e *ex, doc idehistory.TerminalSession, h vtereservoir.VTE,
) (*browser.Tab, error) {
	uri, err := terminalSessionURI(doc.Name)
	if err != nil {
		return nil, err
	}
	title := doc.Name
	if doc.Snapshot.Title != "" {
		title = doc.Snapshot.Title
	}
	t, err := e.comp.Tab(uri, e.config.Icons.Terminal, title, h)
	if err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("wm.Tab: %w", err)
	}
	// Route dynamic title updates issued under the handler's own URI
	// to this session-keyed tab.
	e.tabAliases.addAlias(h.URI(), uri)
	tab := t.(*browser.Tab)
	tab.Subscribe((*tabSubscriber)(e))
	return tab, nil
}

// restoreOpenTaskSessions restores tasks from state into the given ex.
func restoreOpenTaskSessions(e *ex, sessions []idehistory.TaskSession) error {
	if e.tasks == nil {
		return nil
	}
	ret := new(multierror.Error)
	for _, doc := range sessions {
		name := doc.TaskName
		if name == "" {
			name = doc.Name
		}
		if name == "" || doc.Cmd == "" {
			continue
		}
		task := idetask.Task{
			Name:              name,
			Filter:            doc.Filter,
			Cmd:               doc.Cmd,
			Args:              append([]string(nil), doc.Args...),
			MinimizeAlignment: doc.MinimizeAlignment,
		}
		if err := e.tasks.RunTask(task); err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("restore task %q: %w", name, err))
			continue
		}
		if win := taskWindow(e, name); win != nil {
			if doc.WindowMinimized {
				minimizeWindow(win, doc.WindowMinimizedAlignment)
			} else {
				win.Unminimize()
			}
		}
	}
	return ret.ErrorOrNil()
}

func taskWindow(e *ex, name string) browser.Window {
	var ret browser.Window
	e.comp.Browser().IterateWindows(func(win browser.Window) {
		if ret != nil {
			return
		}
		content, err := win.Content()
		if err != nil {
			return
		}
		task, ok := content.(*idetask.Task)
		if !ok {
			return
		}
		if task.Info().Name == name {
			ret = win
		}
	})
	return ret
}

// minimizeWindow snaps win to the configured alignment.
//
// This helper lives in ide/ rather than idehistory/ because it touches
// the IDE's browser.Window — idehistory is concerned only with
// persistence.
func minimizeWindow(win browser.Window, alignment component.Alignment) {
	switch alignment {
	case component.AlignmentTop:
		win.MinimizeUp(0)
	case component.AlignmentBottom:
		win.MinimizeDown(0)
	case component.AlignmentLeft:
		win.MinimizeLeft(0)
	default:
		win.MinimizeRight(0)
	}
}
