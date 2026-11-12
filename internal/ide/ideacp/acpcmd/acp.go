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

// Package acpcmd implements the acp command, which chats with the
// workspace's Agent Client Protocol agents in tabs.
package acpcmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	petname "github.com/dustinkirkland/golang-petname"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text"
)

// Name is the name of the command.
const Name = "acp"

const (
	subNew   = "new"
	subOpen  = "open"
	subClose = "close"
	flagNew  = "--new"
	tabIcon  = '󰚩'
)

var subcommands = []string{subClose, subNew, subOpen}

// Manual returns the manual of the command.
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name: Name,
		Summary: "Chat with the Agent Client Protocol agents configured under acp.agents. " +
			"Each agent runs once per workspace. Closing the last tab of a session ends it, " +
			"unless it is running a turn or waiting for a permission: it then runs on and " +
			"reports in the status inbox.",
		Synopsis: "<subcommand> [<args>]",
		Commands: []textapi.CommandManual{
			{
				Name:     subNew,
				Summary:  "Start a session with an agent and open it in a tab.",
				Synopsis: "<agent> [<session>]",
			},
			{
				Name:     subOpen,
				Summary:  "Open a running session in a tab, or focus its tab. --new opens another tab on it.",
				Synopsis: "[--new] <session>",
			},
			{
				Name:     subClose,
				Summary:  "End a session, even with its tabs open. Its agent keeps running.",
				Synopsis: "<session>",
			},
		},
	}
}

// Manager is the workspace's agents and their sessions.
type Manager = acpapi.Service

// Config is what the command works with. Parser, OnLinkClick and Status
// are optional.
type Config struct {
	WindowManager browserapi.WindowManager
	Interrupter   term.Interrupter
	Notifications browserapi.Notifications
	FileSystem    workspaceapi.FileSystem
	// Root is the workspace root, which chats resolve relative paths in.
	Root        workspaceapi.URI
	Parser      syntaxapi.Parser
	OnLinkClick func(*url.URL) bool
	// Chat is how chats look and edit what they send, as
	// dialoguetui.ReadConfig reads it. Their OnLinkClick and where their
	// markdown reads files and highlights code come from the fields above.
	Chat dialoguetui.ComponentConfig
	// Status takes what a session whose last tab closed mid-turn does,
	// until a tab shows it again. Without it, such a session reports
	// nothing.
	Status StatusReporter
}

var _ text.CommandHandler = (*Handler)(nil)

// Handler implements the command. It must be called on the event loop.
type Handler struct {
	m   Manager
	cfg Config

	// mu guards cfg.Chat, which Restyle changes, and the fields below.
	mu sync.Mutex
	// restyles counts the calls to Restyle, so a restyle applies only
	// while no later one started.
	restyles uint64
	// tabs are the open chats.
	tabs map[view]openChat
	// away are the sessions followed since their last chat closed, by
	// name.
	away map[string]*away
}

// view identifies a chat on a session. Chats on a session are numbered
// from 1, each taking the lowest number free when it opens.
type view struct {
	session string
	n       int
}

type openChat struct {
	chat    *chat
	content browserapi.Handler
	agent   string
	uri     workspaceapi.URI
	label   string
	// status is where the session reports once its last chat closed.
	status workspaceapi.URI
}

// New returns the command's handler.
func New(m Manager, cfg Config) *Handler {
	return &Handler{m: m, cfg: cfg, tabs: make(map[view]openChat), away: make(map[string]*away)}
}

// Restyle gives the open chats, and those opened from now on, the status
// bar, compose box frame and compose box colours dialoguetui.ReadStyle
// reads from own and host. It reads their images in the background; a
// Restyle still reading when another is called applies nothing.
func (h *Handler) Restyle(own, host config.Config) {
	h.mu.Lock()
	h.restyles++
	n, base := h.restyles, h.cfg.Chat
	h.mu.Unlock()
	go debug.CapturePanicReport(func() {
		cfg, frameImages := dialoguetui.ReadStyle(context.Background(), base, own, host, h.cfg.Notifications)
		h.mu.Lock()
		if n != h.restyles {
			h.mu.Unlock()
			return
		}
		h.cfg.Chat.StatusBar = cfg.StatusBar
		h.cfg.Chat.InputBox.FrameCharSet = cfg.InputBox.FrameCharSet
		h.cfg.Chat.InputBox.FrameAttr = cfg.InputBox.FrameAttr
		h.cfg.Chat.InputBox.ContentAttr = cfg.InputBox.ContentAttr
		h.cfg.Chat.InputBackgroundColor = cfg.InputBackgroundColor
		chats := make([]*chat, 0, len(h.tabs))
		for _, t := range h.tabs {
			chats = append(chats, t.chat)
		}
		h.mu.Unlock()
		for _, c := range chats {
			c.restyle(cfg, frameImages)
		}
	})
}

// HandleCommand satisfies text.CommandHandler. A new session opens in a
// tab right away and reports in it whether its agent started.
func (h *Handler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if len(cmd.Args) == 0 {
		return usage("")
	}
	sub, args := cmd.Args[0], cmd.Args[1:]
	switch sub {
	case subNew:
		if len(args) < 1 || len(args) > 2 {
			return usage(subNew)
		}
		return h.newSession(ctx, cmd.Window, args)
	case subOpen:
		another := len(args) > 0 && args[0] == flagNew
		if another {
			args = args[1:]
		}
		if len(args) != 1 {
			return usage(subOpen)
		}
		return h.openSession(ctx, cmd.Window, args[0], another)
	case subClose:
		if len(args) != 1 {
			return usage(subClose)
		}
		return h.closeSession(ctx, args[0])
	}
	return fmt.Errorf("%s: unknown subcommand %q", Name, sub)
}

// ShowStatus opens the session reporting at uri since its last chat
// closed, as acp open does, in the focused window. It reports false when
// no such session reports at uri.
func (h *Handler) ShowStatus(uri workspaceapi.URI) (bool, error) {
	name, ok := h.awayAt(uri)
	if !ok {
		return false, nil
	}
	return true, h.openSession(context.Background(), nil, name, false)
}

// CloseStatus ends the session reporting at uri since its last chat
// closed, as acp close does. It reports false when no such session
// reports at uri.
func (h *Handler) CloseStatus(uri workspaceapi.URI) (bool, error) {
	name, ok := h.awayAt(uri)
	if !ok {
		return false, nil
	}
	return true, h.closeSession(context.Background(), name)
}

// Complete satisfies text.CommandHandler.
func (h *Handler) Complete(ctx context.Context, cmd textapi.Command) (
	blueiterator.Iterator[string], string, error,
) {
	args := cmd.Args
	var names []string
	switch {
	case len(args) <= 1:
		names = subcommands
	case len(args) == 2 && args[0] == subNew:
		agents, err := h.agents(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, a := range agents {
			names = append(names, a.Name)
		}
	case len(args) == 2 && args[0] == subOpen && strings.HasPrefix(args[1], "-"):
		names = []string{flagNew}
	case len(args) == 2 && (args[0] == subOpen || args[0] == subClose),
		len(args) == 3 && args[0] == subOpen && args[1] == flagNew:
		sessions, err := h.sessions(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, s := range sessions {
			names = append(names, s.Name)
		}
	}
	prefix := ""
	if len(args) > 0 {
		prefix = args[len(args)-1]
	}
	return blueiterator.FromSlice(slices.DeleteFunc(slices.Clone(names), func(n string) bool {
		return !strings.HasPrefix(n, prefix)
	})), "", nil
}

func (h *Handler) newSession(ctx context.Context, win browserapi.Window, args []string) error {
	agent := args[0]
	agents, err := h.agents(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(agents, func(a acpapi.AgentInfo) bool { return a.Name == agent }) {
		if len(agents) == 0 {
			return fmt.Errorf("unknown agent %q: configure agents under acp.agents", agent)
		}
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = a.Name
		}
		return fmt.Errorf("unknown agent %q, choose one of: %s", agent, strings.Join(names, ", "))
	}
	sessions, err := h.sessions(ctx)
	if err != nil {
		return err
	}
	taken := func(name string) bool {
		return slices.ContainsFunc(sessions, func(s acpapi.SessionInfo) bool { return s.Name == name })
	}
	var name string
	if len(args) == 2 {
		name = args[1]
		if taken(name) {
			return fmt.Errorf("session %q already exists", name)
		}
	} else {
		name = freeName(taken)
	}
	return h.show(win, agent, name, func(ctx context.Context) (acpapi.Session, error) {
		return h.m.NewSession(ctx, acpapi.NewSessionRequest{Agent: agent, Name: name})
	})
}

// openSession focuses the first tab of session name, or opens one when
// it has none or another is set.
func (h *Handler) openSession(ctx context.Context, win browserapi.Window, name string, another bool) error {
	s, err := h.m.OpenSession(ctx, name)
	if err != nil {
		return err
	}
	h.stopAway(name)
	if !another {
		h.mu.Lock()
		open, ok := h.tabs[h.firstViewLocked(name)]
		h.mu.Unlock()
		if ok {
			return h.focus(win, open.uri, open.label, open.content)
		}
	}
	return h.show(win, s.Info().Agent, name, func(context.Context) (acpapi.Session, error) { return s, nil })
}

// closeSession closes the session in the background, as the agent may
// take a while to answer.
func (h *Handler) closeSession(ctx context.Context, name string) error {
	s, err := h.m.OpenSession(ctx, name)
	if err != nil {
		return err
	}
	h.stopAway(name)
	go debug.CapturePanicReport(func() { h.endSession(name, s) })
	return nil
}

func (h *Handler) endSession(name string, s acpapi.Session) {
	if err := s.Close(); err != nil && !errors.Is(err, acpapi.ErrClosed) {
		_, _ = h.cfg.Notifications.Notify(browserapi.LevelError, "close %s: %v", name, err)
	}
}

func (h *Handler) newChat(name string, open func(context.Context) (acpapi.Session, error)) *chat {
	h.mu.Lock()
	cfg := h.cfg.Chat
	h.mu.Unlock()
	cfg.OnLinkClick = h.cfg.OnLinkClick
	md := dialoguetui.DefaultMarkdownConfig()
	if cfg.MarkdownConfig != nil {
		shared := *cfg.MarkdownConfig
		md = &shared
	}
	md.FS = h.cfg.FileSystem
	md.Dir = h.cfg.Root.Path()
	md.Parser = h.cfg.Parser
	cfg.MarkdownConfig = md
	return newChat(chatConfig{
		Name:          name,
		Open:          open,
		Component:     cfg,
		WindowManager: h.cfg.WindowManager,
		Interrupter:   h.cfg.Interrupter,
		Parser:        h.cfg.Parser,
		Root:          h.cfg.Root.Path(),
	})
}

// show opens a chat on what open returns, session name of agent, in a
// new tab of win, or of the focused window when win is nil.
func (h *Handler) show(
	win browserapi.Window, agent, name string, open func(context.Context) (acpapi.Session, error),
) error {
	h.mu.Lock()
	v := view{session: name, n: 1}
	for _, taken := h.tabs[v]; taken; _, taken = h.tabs[v] {
		v.n++
	}
	h.mu.Unlock()
	uri, err := tabURI(agent, name, v.n)
	if err != nil {
		return err
	}
	status, err := statusURI(agent, name)
	if err != nil {
		return err
	}
	label := name
	if v.n > 1 {
		label = fmt.Sprintf("%s (%d)", name, v.n)
	}
	c := h.newChat(name, open)
	content := c.content(func() { h.closed(v, c) })
	h.mu.Lock()
	h.tabs[v] = openChat{chat: c, content: content, agent: agent, uri: uri, label: label, status: status}
	h.mu.Unlock()
	if err := h.focus(win, uri, label, content); err != nil {
		// Forgotten first, so closing a chat that never showed leaves its
		// session be.
		h.mu.Lock()
		delete(h.tabs, v)
		h.mu.Unlock()
		_ = content.Close()
		return err
	}
	return nil
}

// closed forgets chat c of v, whose tab closed. When it was the last chat
// on its session, the session ends unless it works on, in which case it
// is followed until a chat shows it again or it ends.
func (h *Handler) closed(v view, c *chat) {
	h.mu.Lock()
	open, ok := h.tabs[v]
	if !ok || open.chat != c {
		h.mu.Unlock()
		return
	}
	delete(h.tabs, v)
	if _, ok := h.tabs[h.firstViewLocked(v.session)]; ok {
		h.mu.Unlock()
		return
	}
	prev := h.away[v.session]
	a := newAway(v.session, open.agent, open.status, h.cfg.Status)
	h.away[v.session] = a
	h.mu.Unlock()
	if prev != nil {
		prev.stop()
	}
	go debug.CapturePanicReport(func() { h.follow(a, c) })
}

// follow ends the session of c, whose last chat closed, unless it works
// on. Then it reports what the session does through a until a stops or
// the session ends.
func (h *Handler) follow(a *away, c *chat) {
	s, err := c.waitSession(a.ctx)
	if err != nil {
		h.dropAway(a)
		return
	}
	it := s.Events()
	defer func() { _ = it.Close() }()
	// Next's context bounds the wait only, so a done one folds the events
	// logged so far: what the session is doing now, which the closed
	// chat may not have caught up with.
	now, cancel := context.WithCancel(a.ctx)
	cancel()
	var st acpapi.SessionState
	for {
		ev, ok := it.Next(now)
		if !ok {
			break
		}
		acpapi.Apply(&st, ev)
	}
	switch {
	case a.ctx.Err() != nil:
		return
	case !errors.Is(it.Err(), context.Canceled):
		h.dropAway(a)
		return
	case !working(st):
		if h.dropAway(a) {
			h.endSession(a.name, s)
		}
		return
	}
	a.report(st, nil)
	for {
		ev, ok := it.Next(a.ctx)
		if !ok {
			break
		}
		was := working(st)
		acpapi.Apply(&st, ev)
		if was || working(st) {
			a.report(st, ev.Done)
		}
	}
	if a.ctx.Err() == nil {
		h.dropAway(a)
	}
}

// stopAway stops following session name, if it is.
func (h *Handler) stopAway(name string) {
	h.mu.Lock()
	a := h.away[name]
	delete(h.away, name)
	h.mu.Unlock()
	if a != nil {
		a.stop()
	}
}

// dropAway stops following a's session, reporting false when something
// else stopped it first.
func (h *Handler) dropAway(a *away) bool {
	h.mu.Lock()
	ok := h.away[a.name] == a
	if ok {
		delete(h.away, a.name)
	}
	h.mu.Unlock()
	if ok {
		a.stop()
	}
	return ok
}

// awayAt names the session followed since its last chat closed that
// reports at uri.
func (h *Handler) awayAt(uri workspaceapi.URI) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, a := range h.away {
		if a.uri.Equal(uri) {
			return name, true
		}
	}
	return "", false
}

// firstViewLocked is the open chat on session name with the lowest
// number, or a view no chat has when none is open.
func (h *Handler) firstViewLocked(name string) view {
	first := view{session: name}
	for v := range h.tabs {
		if v.session == name && (first.n == 0 || v.n < first.n) {
			first = v
		}
	}
	return first
}

func (h *Handler) focus(win browserapi.Window, uri workspaceapi.URI, name string, content browserapi.Handler) error {
	wm := h.cfg.WindowManager
	tab, err := wm.Tab(uri, tabIcon, name, content)
	if err != nil {
		return fmt.Errorf("create tab: %w", err)
	}
	if win == nil {
		if win, err = wm.Focus(); err != nil {
			return err
		}
	}
	if err := wm.SetWindowContent(win, tab); err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		return fmt.Errorf("window set content: %w", err)
	}
	return nil
}

func (h *Handler) agents(ctx context.Context) ([]acpapi.AgentInfo, error) {
	return collect(ctx, h.m.Agents(ctx))
}

func (h *Handler) sessions(ctx context.Context) ([]acpapi.SessionInfo, error) {
	return collect(ctx, h.m.Sessions(ctx))
}

func collect[T any](ctx context.Context, it iterator.Iterator[T]) ([]T, error) {
	defer func() { _ = it.Close() }()
	var out []T
	for {
		v, ok := it.Next(ctx)
		if !ok {
			return out, it.Err()
		}
		out = append(out, v)
	}
}

// freeName returns a petname that is not taken.
func freeName(taken func(string) bool) string {
	for range 32 {
		if n := petname.Generate(2, "-"); !taken(n) {
			return n
		}
	}
	base := petname.Generate(2, "-")
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); !taken(n) {
			return n
		}
	}
}

// tabURI identifies the tab of view n of agent's session name. View 1's
// is the session's own; the others add their number.
func tabURI(agent, name string, n int) (workspaceapi.URI, error) {
	uri := fmt.Sprintf("%s://%s/%s", Name, url.PathEscape(agent), url.PathEscape(name))
	if n > 1 {
		uri += "/" + strconv.Itoa(n)
	}
	return workspaceapi.ParseURI(uri)
}

// statusURI is where agent's session name reports while no tab shows it,
// which no tab has.
func statusURI(agent, name string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI(fmt.Sprintf("%s://%s/%s/session", Name, url.PathEscape(agent), url.PathEscape(name)))
}

func usage(sub string) error {
	for _, m := range Manual().Commands {
		if m.Name == sub {
			return fmt.Errorf("usage: %s %s %s", Name, sub, m.Synopsis)
		}
	}
	return fmt.Errorf("usage: %s %s", Name, Manual().Synopsis)
}
