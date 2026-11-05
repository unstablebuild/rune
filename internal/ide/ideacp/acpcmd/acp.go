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
	tabIcon  = '󰚩'
)

var subcommands = []string{subClose, subNew, subOpen}

// Manual returns the manual of the command.
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name: Name,
		Summary: "Chat with the Agent Client Protocol agents configured under acp.agents. " +
			"Each agent runs once per workspace and its sessions outlive their tabs.",
		Synopsis: "<subcommand> [<args>]",
		Commands: []textapi.CommandManual{
			{
				Name:     subNew,
				Summary:  "Start a session with an agent and open it in a tab.",
				Synopsis: "<agent> [<session>]",
			},
			{
				Name:     subOpen,
				Summary:  "Open a running session in a tab.",
				Synopsis: "<session>",
			},
			{
				Name:     subClose,
				Summary:  "End a session. Its agent keeps running.",
				Synopsis: "<session>",
			},
		},
	}
}

// Manager is the workspace's agents and their sessions.
type Manager = acpapi.Service

// Config is what the command works with. Parser and OnLinkClick are
// optional.
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
	// tabs are the open chats by tab URI.
	tabs map[workspaceapi.URI]openChat
}

type openChat struct {
	chat    *chat
	content browserapi.Handler
}

// New returns the command's handler.
func New(m Manager, cfg Config) *Handler {
	return &Handler{m: m, cfg: cfg, tabs: make(map[workspaceapi.URI]openChat)}
}

// Restyle gives the open chats, and those opened from now on, the status
// bar and compose box frame dialoguetui.ReadStyle reads from own and host.
// It reads their images in the background; a Restyle still reading when
// another is called applies nothing.
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
		chats := make([]*chat, 0, len(h.tabs))
		for _, t := range h.tabs {
			chats = append(chats, t.chat)
		}
		h.mu.Unlock()
		for _, c := range chats {
			c.restyle(cfg.StatusBar, cfg.InputBox.FrameCharSet, frameImages)
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
		if len(args) != 1 {
			return usage(subOpen)
		}
		return h.openSession(ctx, cmd.Window, args[0])
	case subClose:
		if len(args) != 1 {
			return usage(subClose)
		}
		return h.closeSession(ctx, args[0])
	}
	return fmt.Errorf("%s: unknown subcommand %q", Name, sub)
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
	case len(args) == 2 && (args[0] == subOpen || args[0] == subClose):
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
	uri, err := tabURI(agent, name)
	if err != nil {
		return err
	}
	c := h.newChat(name, func(ctx context.Context) (acpapi.Session, error) {
		return h.m.NewSession(ctx, acpapi.NewSessionRequest{Agent: agent, Name: name})
	})
	return h.show(win, uri, name, c)
}

func (h *Handler) openSession(ctx context.Context, win browserapi.Window, name string) error {
	s, err := h.m.OpenSession(ctx, name)
	if err != nil {
		return err
	}
	uri, err := tabURI(s.Info().Agent, name)
	if err != nil {
		return err
	}
	h.mu.Lock()
	open, ok := h.tabs[uri]
	h.mu.Unlock()
	if ok {
		return h.focus(win, uri, name, open.content)
	}
	c := h.newChat(name, func(context.Context) (acpapi.Session, error) { return s, nil })
	return h.show(win, uri, name, c)
}

// closeSession closes the session in the background, as the agent may
// take a while to answer.
func (h *Handler) closeSession(ctx context.Context, name string) error {
	s, err := h.m.OpenSession(ctx, name)
	if err != nil {
		return err
	}
	go debug.CapturePanicReport(func() {
		if err := s.Close(); err != nil && !errors.Is(err, acpapi.ErrClosed) {
			_, _ = h.cfg.Notifications.Notify(browserapi.LevelError, "close %s: %v", name, err)
		}
	})
	return nil
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

// show opens c in a tab in win, or in the focused window when win is nil.
func (h *Handler) show(win browserapi.Window, uri workspaceapi.URI, name string, c *chat) error {
	content := c.content(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.tabs, uri)
	})
	h.mu.Lock()
	h.tabs[uri] = openChat{chat: c, content: content}
	h.mu.Unlock()
	if err := h.focus(win, uri, name, content); err != nil {
		_ = content.Close()
		return err
	}
	return nil
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

// tabURI identifies the tab of agent's session name.
func tabURI(agent, name string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI(fmt.Sprintf("%s://%s/%s", Name, url.PathEscape(agent), url.PathEscape(name)))
}

func usage(sub string) error {
	for _, m := range Manual().Commands {
		if m.Name == sub {
			return fmt.Errorf("usage: %s %s %s", Name, sub, m.Synopsis)
		}
	}
	return fmt.Errorf("usage: %s %s", Name, Manual().Synopsis)
}
