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

//go:build e2e

package claudetest

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acprpc"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/ideacp"
	"unstable.build/rune/internal/ide/ideacp/acpcmd"
	"unstable.build/rune/internal/workspace"
)

const (
	agent = "claude"
	// thinkingDisplay is the variable that decides whether Claude Code
	// shows its reasoning.
	thinkingDisplay = "CLAUDE_CODE_THINKING_DISPLAY"
	// startTimeout bounds starting a session, which may first have npx
	// download the agent.
	startTimeout = 3 * time.Minute
	// turnTimeout bounds a turn of the agent.
	turnTimeout = 3 * time.Minute
	// uiTimeout bounds the wait for a chat to show what its session did.
	uiTimeout = 15 * time.Second

	screenWidth  = 120
	screenHeight = 50
)

// agentConfig is the claude agent as the claude-code package configures
// it.
var agentConfig = ideacp.AgentConfig{
	Command: "npx",
	Args:    []string{"-y", "@agentclientprotocol/claude-agent-acp@0.100.0"},
	Env: map[string]string{
		"CLAUDE_CODE_EXECUTABLE": "claude",
		thinkingDisplay:          "summarized",
	},
}

// agents bounds the harnesses running at once, as each runs a Node
// process and a Claude Code process per session.
var agents = make(chan struct{}, 3)

var ctrlC = term.Event{Mod: term.ModCtrl, Ch: 'c'}

// statuses are the phases the chat's status bar shows.
var statuses = []string{"IDLE", "SENDING", "REASONING", "RECEIVING", "EXECUTING", "ASKING"}

// harness is a workspace with the claude agent, run by an ideacp.Manager,
// and the acp command that chats with its sessions.
type harness struct {
	t     *testing.T
	dir   string
	m     *ideacp.Manager
	acp   *acpcmd.Handler
	wm    *windowManager
	notes *notifications
}

// newHarness returns a harness whose agent runs with env over the
// claude-code package's.
func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	for _, bin := range []string{"claude", "npx"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found, skipping Claude Code e2e test", bin)
		}
	}
	agents <- struct{}{}
	t.Cleanup(func() { <-agents })
	// Claude Code reports paths with their symlinks resolved.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	cfg := agentConfig
	cfg.Env = maps.Clone(agentConfig.Env)
	maps.Copy(cfg.Env, env)
	m := ideacp.New(root, scheme, ideacp.Config{Agents: map[string]ideacp.AgentConfig{agent: cfg}})
	t.Cleanup(func() { _ = m.Close() })
	wm := &windowManager{tabs: make(map[string]browserapi.Handler)}
	t.Cleanup(wm.closeAll)
	notes := &notifications{}
	chat := dialoguetui.DefaultComponentConfig()
	chat.StatusBar = dialoguetui.DefaultStatusBarConfig()
	return &harness{
		t: t, dir: dir, m: m, wm: wm, notes: notes,
		acp: acpcmd.New(m, acpcmd.Config{
			WindowManager: wm,
			Interrupter:   term.NopInterrupter(),
			Notifications: notes,
			Root:          root,
			Chat:          chat,
		}),
	}
}

// run runs the command acp with args, as the command prompt does.
func (h *harness) run(args ...string) error {
	return h.acp.HandleCommand(context.Background(), textapi.Command{Name: acpcmd.Name, Args: args})
}

// newChat runs acp new claude name, and returns its chat for t once the
// session started.
func (h *harness) newChat(t *testing.T, name string) *chat {
	t.Helper()
	require.NoError(t, h.run("new", agent, name))
	return h.chat(t, name)
}

// openChat runs acp open name, and returns its chat for t.
func (h *harness) openChat(t *testing.T, name string) *chat {
	t.Helper()
	require.NoError(t, h.run("open", name))
	return h.chat(t, name)
}

func (h *harness) chat(t *testing.T, name string) *chat {
	t.Helper()
	c := &chat{t: t, name: name, tab: h.wm.tab(t, name)}
	var s acpapi.Session
	waitUntil(t, startTimeout, "session "+name+" to start", func() bool {
		var err error
		s, err = h.m.OpenSession(context.Background(), name)
		return err == nil
	}, c.screen)
	c.log = follow(t, s)
	c.log.waitFor(0, "session "+name+" to start", func(ev acpapi.Event) bool { return ev.Started != nil })
	return c
}

// closeTab closes the tab of c, as the user would.
func (h *harness) closeTab(c *chat) {
	c.t.Helper()
	h.wm.remove(c.name)
	require.NoError(c.t, c.tab.Close())
}

// sessions names the running sessions.
func (h *harness) sessions() []string {
	h.t.Helper()
	var names []string
	for _, s := range collect(h.t, h.m.Sessions(context.Background())) {
		names = append(names, s.Name)
	}
	return names
}

// agents lists the configured agents.
func (h *harness) agents() []acpapi.AgentInfo {
	h.t.Helper()
	return collect(h.t, h.m.Agents(context.Background()))
}

// extension returns the ACP service as extensions reach it, over gRPC.
func (h *harness) extension() acpapi.Service {
	h.t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	acprpc.RegisterACPServer(srv, acprpc.NewServer(h.m))
	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })
	h.t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = cc.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	return acprpc.NewClient(ctx, cc)
}

// exists reports whether the workspace has a file called name.
func (h *harness) exists(name string) bool {
	_, err := os.Stat(filepath.Join(h.dir, name))
	return err == nil
}

// chat is a tab of the acp command, typed into and read off its screen,
// and the log of its session.
type chat struct {
	t    *testing.T
	name string
	tab  browserapi.Handler
	log  *transcript
	// mu serializes drawing the tab.
	mu sync.Mutex
}

// send types text and presses enter.
func (c *chat) send(text string) {
	c.t.Helper()
	r := strings.NewReplacer(`\`, `\\`, "<", `\<`, ">", `\>`, " ", "<space>")
	keys, err := term.ParseKeys(r.Replace(text) + "<enter>")
	require.NoError(c.t, err)
	for _, k := range keys {
		c.press(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
	}
}

func (c *chat) press(ev term.Event) {
	if ev.Type == 0 {
		ev.Type = term.EventKey
	}
	c.tab.Handle(ev)
}

// prompt sends text and returns the turn it starts once it ends.
func (c *chat) prompt(text string) turn {
	c.t.Helper()
	from := c.log.len()
	c.send(text)
	return c.log.turn(from)
}

// set runs the chat command /cmd value and waits for the session to take
// the value of its config option of category.
func (c *chat) set(cmd string, category acp.SessionConfigOptionCategory, value string) {
	c.t.Helper()
	from := c.log.len()
	c.send("/" + cmd + " " + value)
	c.log.waitFor(from, "/"+cmd+" "+value, func(ev acpapi.Event) bool {
		if ev.Update == nil || ev.Update.ConfigOptionUpdate == nil {
			return false
		}
		opt := findOption(ev.Update.ConfigOptionUpdate.ConfigOptions, category)
		return opt != nil && string(opt.CurrentValue) == value
	})
}

// option is the session's config option of category.
func (c *chat) option(category acp.SessionConfigOptionCategory) *acp.SessionConfigOptionSelect {
	c.t.Helper()
	opt := findOption(c.log.state().ConfigOptions, category)
	require.NotNil(c.t, opt, "no %s option", category)
	return opt
}

// useHaiku switches the session to the fastest model at low effort.
func (c *chat) useHaiku() {
	c.t.Helper()
	c.set("model", acp.SessionConfigOptionCategoryModel, "haiku")
	c.set("effort", acp.SessionConfigOptionCategoryThoughtLevel, "low")
}

// answering waits for the turn to start from index from on to end,
// answering each permission it asks for on the chat's prompt: by picking
// option, or by dismissing the prompt when option is empty. It returns
// the turn and what the chat showed with each prompt up.
func (c *chat) answering(from int, option string) (turn, []string) {
	c.t.Helper()
	answered := make(map[acpapi.PermissionID]bool)
	var prompts []string
	timeout := time.After(turnTimeout)
	for {
		evs, ended, changed := c.log.since(from)
		if i := slices.IndexFunc(evs, func(ev acpapi.Event) bool {
			return ev.Permission != nil && !answered[ev.Permission.ID]
		}); i >= 0 {
			p := *evs[i].Permission
			prompts = append(prompts, c.answer(p, option))
			answered[p.ID] = true
			continue
		}
		if tn, ok := findTurn(evs); ok {
			return tn, prompts
		}
		if ended {
			c.t.Fatalf("session %s ended mid-turn: %v", c.name, c.log.err)
		}
		select {
		case <-changed:
		case <-timeout:
			c.t.Fatalf("turn of %s did not end:\n%s", c.name, c.screen())
		}
	}
}

// answer picks option on the chat's prompt for p, or dismisses it when
// option is empty, and returns what the chat showed with the prompt up.
func (c *chat) answer(p acpapi.Permission, option string) string {
	c.t.Helper()
	// A prompt leaves the chat once answered, so one up is p's. Its
	// cursor marks the option it starts on.
	require.NotEmpty(c.t, p.Request.Options)
	screen := c.waitScreen("> " + p.Request.Options[0].Name)
	if option == "" {
		c.press(term.Event{Key: term.KeyEsc})
	} else {
		i := slices.IndexFunc(p.Request.Options, func(o acp.PermissionOption) bool { return o.Name == option })
		require.GreaterOrEqual(c.t, i, 0, "permission %d offers no %q", p.ID, option)
		for range i {
			c.press(term.Event{Key: term.KeyArrowDown})
		}
		c.press(term.Event{Key: term.KeyEnter})
	}
	c.log.waitFor(0, fmt.Sprintf("permission %d to resolve", p.ID), func(ev acpapi.Event) bool {
		return ev.Resolved != nil && ev.Resolved.ID == p.ID
	})
	return screen
}

func (c *chat) screen() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tab.Resize(screenWidth, screenHeight)
	return handlertest.DrawHandler(c.tab, screenWidth, screenHeight)
}

// waitScreen waits for the chat to show each of want, ignoring how it
// wraps them, and returns the screen.
func (c *chat) waitScreen(want ...string) string {
	c.t.Helper()
	var screen string
	waitUntil(c.t, uiTimeout, fmt.Sprintf("chat %s to show %q", c.name, want), func() bool {
		screen = c.screen()
		return containsAll(screen, want...)
	}, c.screen)
	return screen
}

// status is the phase on the chat's status bar.
func (c *chat) status() string {
	lines := strings.Split(strings.TrimRight(c.screen(), " \n"), "\n")
	bar := lines[len(lines)-1]
	for _, s := range statuses {
		if strings.Contains(bar, s) {
			return s
		}
	}
	return ""
}

// waitStatus waits for the chat's status bar to show status.
func (c *chat) waitStatus(status string) {
	c.t.Helper()
	waitUntil(c.t, uiTimeout, fmt.Sprintf("chat %s to be %s", c.name, status), func() bool {
		return c.status() == status
	}, c.screen)
}

// watchStatus records the phases the chat's status bar goes through until
// the returned function, which returns them, is called. The last is the
// phase then.
func (c *chat) watchStatus() func() []string {
	stop := make(chan struct{})
	done := make(chan struct{})
	var seen []string
	go debug.CapturePanicReport(func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if s := c.status(); s != "" && (len(seen) == 0 || seen[len(seen)-1] != s) {
				seen = append(seen, s)
			}
			select {
			case <-stop:
				if s := c.status(); s != "" && seen[len(seen)-1] != s {
					seen = append(seen, s)
				}
				return
			case <-tick.C:
			}
		}
	})
	var once sync.Once
	finish := func() []string {
		once.Do(func() {
			close(stop)
			<-done
		})
		return seen
	}
	c.t.Cleanup(func() { finish() })
	return finish
}

// transcript follows the event log of a session.
type transcript struct {
	t *testing.T

	mu     sync.Mutex
	events []acpapi.Event
	ended  bool
	err    error
	// changed is closed and replaced on every change.
	changed chan struct{}
}

func follow(t *testing.T, s acpapi.Session) *transcript {
	tr := &transcript{t: t, changed: make(chan struct{})}
	it := s.Events()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		for {
			ev, ok := it.Next(ctx)
			tr.mu.Lock()
			if ok {
				tr.events = append(tr.events, ev)
			} else {
				tr.ended, tr.err = true, it.Err()
			}
			close(tr.changed)
			tr.changed = make(chan struct{})
			tr.mu.Unlock()
			if !ok {
				return
			}
		}
	})
	t.Cleanup(func() {
		cancel()
		<-done
		_ = it.Close()
	})
	return tr
}

func (tr *transcript) len() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.events)
}

// since returns the events from index from on, whether the log ended, and
// a channel closed on its next change.
func (tr *transcript) since(from int) ([]acpapi.Event, bool, <-chan struct{}) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.events[min(from, len(tr.events)):]), tr.ended, tr.changed
}

// waitFor returns the first event from index from on that match accepts.
func (tr *transcript) waitFor(from int, what string, match func(acpapi.Event) bool) acpapi.Event {
	tr.t.Helper()
	timeout := time.After(turnTimeout)
	for {
		evs, ended, changed := tr.since(from)
		if i := slices.IndexFunc(evs, match); i >= 0 {
			return evs[i]
		}
		if ended {
			tr.t.Fatalf("session ended before %s: %v", what, tr.err)
		}
		select {
		case <-changed:
		case <-timeout:
			tr.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// turn waits for the first turn to start from index from on to end, and
// returns it.
func (tr *transcript) turn(from int) turn {
	tr.t.Helper()
	start := tr.waitFor(from, "the turn to start", func(ev acpapi.Event) bool { return ev.Prompt != nil })
	tr.waitFor(start.Seq, "the turn to end", func(ev acpapi.Event) bool { return ev.Done != nil })
	evs, _, _ := tr.since(start.Seq)
	tn, _ := findTurn(evs)
	return tn
}

// state is the session's state as of its last event.
func (tr *transcript) state() acpapi.SessionState {
	evs, _, _ := tr.since(0)
	var s acpapi.SessionState
	for _, ev := range evs {
		acpapi.Apply(&s, ev)
	}
	return s
}

// turn is the events of a turn, from its Prompt to its Done.
type turn []acpapi.Event

// findTurn returns the first turn of evs that ended.
func findTurn(evs []acpapi.Event) (turn, bool) {
	start := slices.IndexFunc(evs, func(ev acpapi.Event) bool { return ev.Prompt != nil })
	if start < 0 {
		return nil, false
	}
	end := slices.IndexFunc(evs[start:], func(ev acpapi.Event) bool { return ev.Done != nil })
	if end < 0 {
		return nil, false
	}
	return turn(evs[start : start+end+1]), true
}

func (tn turn) done() acpapi.TurnEnd { return *tn[len(tn)-1].Done }

// text is what the session's own agent said.
func (tn turn) text() string {
	var b strings.Builder
	for _, ev := range tn {
		if ev.Session == "" && ev.Update != nil && ev.Update.AgentMessageChunk != nil {
			b.WriteString(blockText(ev.Update.AgentMessageChunk.Content))
		}
	}
	return b.String()
}

// thought is the reasoning the session's own agent showed.
func (tn turn) thought() string {
	var b strings.Builder
	for _, ev := range tn {
		if ev.Session == "" && ev.Update != nil && ev.Update.AgentThoughtChunk != nil {
			b.WriteString(blockText(ev.Update.AgentThoughtChunk.Content))
		}
	}
	return b.String()
}

func (tn turn) permissions() []acpapi.Permission {
	var out []acpapi.Permission
	for _, ev := range tn {
		if ev.Permission != nil {
			out = append(out, *ev.Permission)
		}
	}
	return out
}

// outcome is how permission id was answered.
func (tn turn) outcome(id acpapi.PermissionID) acp.RequestPermissionOutcome {
	for _, ev := range tn {
		if ev.Resolved != nil && ev.Resolved.ID == id {
			return ev.Resolved.Outcome
		}
	}
	return acp.RequestPermissionOutcome{}
}

// subagents are the subagents the turn announced, in order.
func (tn turn) subagents() []acp.SessionId {
	var out []acp.SessionId
	for _, ev := range tn {
		if ev.Subagent != nil && !slices.Contains(out, ev.Subagent.SessionID) {
			out = append(out, ev.Subagent.SessionID)
		}
	}
	return out
}

// toolCalls are the titles of the tool calls session made, the session's
// own agent when it is empty.
func (tn turn) toolCalls(session acp.SessionId) []string {
	titles := make(map[acp.ToolCallId]string)
	var ids []acp.ToolCallId
	for _, ev := range tn {
		if ev.Session != session || ev.Update == nil {
			continue
		}
		switch u := ev.Update; {
		case u.ToolCall != nil:
			ids = append(ids, u.ToolCall.ToolCallId)
			titles[u.ToolCall.ToolCallId] = u.ToolCall.Title
		case u.ToolCallUpdate != nil && u.ToolCallUpdate.Title != nil:
			titles[u.ToolCallUpdate.ToolCallId] = *u.ToolCallUpdate.Title
		}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = titles[id]
	}
	return out
}

// windowManager keeps the tabs the acp command opens, by name.
type windowManager struct {
	browserapi.WindowManager

	mu   sync.Mutex
	tabs map[string]browserapi.Handler
}

func (w *windowManager) Tab(_ workspaceapi.URI, _ rune, name string, h browserapi.Handler) (browserapi.Handler, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if tab, ok := w.tabs[name]; ok {
		return tab, nil
	}
	w.tabs[name] = h
	return h, nil
}

func (w *windowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }

func (w *windowManager) Focus() (browserapi.Window, error) { return window(1), nil }

func (w *windowManager) tab(t *testing.T, name string) browserapi.Handler {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	tab, ok := w.tabs[name]
	require.True(t, ok, "no tab %s", name)
	return tab
}

func (w *windowManager) remove(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.tabs, name)
}

func (w *windowManager) closeAll() {
	w.mu.Lock()
	tabs := slices.Collect(maps.Values(w.tabs))
	clear(w.tabs)
	w.mu.Unlock()
	for _, tab := range tabs {
		_ = tab.Close()
	}
}

type window uint64

func (w window) WindowID() uint64 { return uint64(w) }

// notifications keeps what the acp command notifies.
type notifications struct {
	mu   sync.Mutex
	msgs []string
}

func (n *notifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, fmt.Sprintf(msg, args...))
	return "", nil
}

func (n *notifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *notifications) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

func (n *notifications) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.msgs)
}

// waitUntil waits for cond, failing t with what dump shows when it does not
// hold within timeout.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool, dump func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s:\n%s", what, dump())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// containsAll reports whether screen shows each of want, ignoring how it
// wraps them and the markdown the chat renders.
func containsAll(screen string, want ...string) bool {
	flat := flatten(screen)
	for _, w := range want {
		if !strings.Contains(flat, flatten(w)) {
			return false
		}
	}
	return true
}

var markup = strings.NewReplacer("`", "", "**", "")

func flatten(s string) string {
	return strings.Join(strings.Fields(markup.Replace(s)), " ")
}

// column is where text starts on the first line of screen that shows it,
// or -1.
func column(screen, text string) int {
	for _, line := range strings.Split(screen, "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return utf8.RuneCountInString(line[:i])
		}
	}
	return -1
}

func findOption(opts []acp.SessionConfigOption, category acp.SessionConfigOptionCategory) *acp.SessionConfigOptionSelect {
	for _, o := range opts {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == category {
			return o.Select
		}
	}
	return nil
}

// valueName is the name opt gives its value.
func valueName(opt *acp.SessionConfigOptionSelect, value acp.SessionConfigValueId) string {
	var values []acp.SessionConfigSelectOption
	if u := opt.Options.Ungrouped; u != nil {
		values = *u
	}
	if g := opt.Options.Grouped; g != nil {
		for _, group := range *g {
			values = append(values, group.Options...)
		}
	}
	for _, v := range values {
		if v.Value == value {
			return v.Name
		}
	}
	return string(value)
}

func blockText(b acp.ContentBlock) string {
	if b.Text != nil {
		return b.Text.Text
	}
	return ""
}

func collect[T any](t *testing.T, it iterator.Iterator[T]) []T {
	t.Helper()
	defer func() { _ = it.Close() }()
	var out []T
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			require.NoError(t, it.Err())
			return out
		}
		out = append(out, v)
	}
}
