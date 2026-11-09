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

// sampleext is a self-contained Rune workspace extension compiled by
// the xsandbox e2e test from its own Go module. Its behavior is fully
// deterministic so a spec can assert the exact RPCs it makes:
//
//   - reads config key "prefix" (default "sample")
//   - fetches the workspace config over RPC
//   - reads VERSION from the workspace and notifies its contents
//   - registers command "greet <name>" -> notifies "<prefix>: hi <name>"
//   - registers command "count <args...>" -> notifies "<prefix>: n=<len>",
//     and completes its last argument from "alpha" and "beta"
//   - registers REPL command "status" -> prints "<prefix>: ok", completes
//     "verbose" and prints "usage: status [verbose]" as its help
//   - subscribes to editor open events -> notifies "<prefix>: opened <base>"
//   - registers command "api" -> exercises every workspace service
//     exhaustively: EVERY method of every interface reachable through
//     extensionapi.Workspace (filesystem, executor, terminal, storage,
//     window manager, notifications, resource opener, interrupter,
//     editor, parser, all 64 LSP methods, LLM, all 34 debugger
//     methods, config) plus the Commands/RawConn accessors, notifying
//     "<prefix>: <service> ok" for each service that succeeds. The
//     per-method probes live in probes.go.
//   - registers command "wm" -> installs tui.Handlers through the
//     window manager (Split, Tab, Bar) and notifies
//     "<prefix>: wm <op> ok" for each one.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func main() {
	meta := extensionapi.Metadata{
		DeveloperID:      "Unstable Build",
		DeveloperEmail:   "it@unstable.build",
		DeveloperKey:     "xsandbox-sample",
		ExtensionID:      "xsandbox_sample",
		ExtensionName:    "xsandbox sample",
		ExtensionVersion: "test",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionCommands,
			extensionapi.PermissionNotifications,
			extensionapi.PermissionFileSystem,
			extensionapi.PermissionConfig,
			extensionapi.PermissionEditor,
			extensionapi.PermissionExecute,
			extensionapi.PermissionTerminal,
			extensionapi.PermissionStorage,
			extensionapi.PermissionBrowserWindowManager,
			extensionapi.PermissionBrowserResourceOpener,
			extensionapi.PermissionInterrupt,
			extensionapi.PermissionSyntaxTree,
			extensionapi.PermissionLSP,
			extensionapi.PermissionDebugger,
			extensionapi.PermissionLLM,
		),
	}
	if err := extensionapi.ServeWorkspaceExtension(
		extensionapi.FuncWorkspaceExtension(extend), meta); err != nil {
		log.Fatal(err)
	}
}

func extend(ctx context.Context, w *extensionapi.Workspace, cfg config.Config) error {
	prefix, err := cfg.GetString("prefix")
	if err != nil || prefix == "" {
		prefix = "sample"
	}

	// Fetch the workspace config over RPC (config.Config/Get).
	w.Config(ctx).Iterate(func(string, any) {})

	notifications := w.Notifications(ctx)

	fs := w.FileSystem(ctx)
	version, err := readFile(fs, "VERSION")
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	if _, err := notifications.Notify(browserapi.LevelInfo,
		"%s: version %s", prefix, strings.TrimSpace(version)); err != nil {
		return fmt.Errorf("notify version: %w", err)
	}

	if err := w.Editor(ctx).SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeOpen},
		openNotifier{prefix: prefix, notifications: notifications},
	); err != nil {
		return fmt.Errorf("subscribe editor events: %w", err)
	}

	if err := w.RegisterCommand(textapi.CommandManual{
		Name:     "greet",
		Summary:  "Greet a name",
		Synopsis: "<name>",
	}, greetHandler{prefix: prefix, notifications: notifications}); err != nil {
		return fmt.Errorf("register greet: %w", err)
	}

	if err := w.RegisterCommand(textapi.CommandManual{
		Name:     "count",
		Summary:  "Count arguments",
		Synopsis: "[<arg>...]",
	}, countHandler{prefix: prefix, notifications: notifications}); err != nil {
		return fmt.Errorf("register count: %w", err)
	}

	if err := w.RegisterREPLCommand(textapi.CommandManual{
		Name:    "status",
		Summary: "Report status",
	}, statusHandler{prefix: prefix}); err != nil {
		return fmt.Errorf("register status: %w", err)
	}

	if err := w.RegisterCommand(textapi.CommandManual{
		Name:     "api",
		Summary:  "Exercise every workspace service",
		Synopsis: "",
	}, apiHandler{prefix: prefix, w: w, notifications: notifications}); err != nil {
		return fmt.Errorf("register api: %w", err)
	}

	if err := w.RegisterCommand(textapi.CommandManual{
		Name:     "wm",
		Summary:  "Install tui.Handlers via the window manager",
		Synopsis: "",
	}, wmHandler{prefix: prefix, w: w, notifications: notifications}); err != nil {
		return fmt.Errorf("register wm: %w", err)
	}

	if err := w.RegisterCommand(textapi.CommandManual{
		Name:     "counter",
		Summary:  "Split a panel that renders count=N and increments on <space>",
		Synopsis: "",
	}, counterHandler{w: w}); err != nil {
		return fmt.Errorf("register counter: %w", err)
	}
	return nil
}

func readFile(fs workspaceapi.FileSystem, name string) (string, error) {
	f, err := fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type greetHandler struct {
	prefix        string
	notifications browserapi.Notifications
}

func (h greetHandler) HandleCommand(_ context.Context, cmd textapi.Command) error {
	name := "world"
	if len(cmd.Args) > 0 {
		name = cmd.Args[0]
	}
	_, err := h.notifications.Notify(browserapi.LevelInfo, "%s: hi %s", h.prefix, name)
	return err
}

func (h greetHandler) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

type countHandler struct {
	prefix        string
	notifications browserapi.Notifications
}

func (h countHandler) HandleCommand(_ context.Context, cmd textapi.Command) error {
	_, err := h.notifications.Notify(browserapi.LevelWarn,
		"%s: n=%d", h.prefix, len(cmd.Args))
	return err
}

func (h countHandler) Complete(_ context.Context, _ string, args []string) (iterator.Iterator[string], error) {
	last := ""
	if len(args) > 0 {
		last = args[len(args)-1]
	}
	var ret []string
	for _, c := range []string{"alpha", "beta"} {
		if strings.HasPrefix(c, last) {
			ret = append(ret, c)
		}
	}
	return iterator.FromSlice(ret), nil
}

type statusHandler struct {
	prefix string
}

func (h statusHandler) HandleCommand(
	_ context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.FromSlice([]component.Responsive{
		component.NewResponsiveString(h.prefix+": ok", component.StringResponsiveConfig{}),
	}), nil
}

func (h statusHandler) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{"verbose"}), nil
}

func (h statusHandler) Help(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
	return iterator.FromSlice([]component.Responsive{
		component.NewResponsiveString("usage: status [verbose]", component.StringResponsiveConfig{}),
	}), nil
}

type openNotifier struct {
	prefix        string
	notifications browserapi.Notifications
}

func (n openNotifier) Handle(_ context.Context, ev textapi.Event) bool {
	_, _ = n.notifications.Notify(browserapi.LevelInfo,
		"%s: opened %s", n.prefix, path.Base(ev.URI.Path()))
	return false
}

// apiHandler drives every workspace service in one command invocation
// so a spec can assert the full RPC surface. Each service that returns
// without error emits a "<prefix>: <service> ok" notification. The
// order is fixed so the spec can rely on it.
type apiHandler struct {
	prefix        string
	w             *extensionapi.Workspace
	notifications browserapi.Notifications
}

func (h apiHandler) HandleCommand(ctx context.Context, _ textapi.Command) error {
	h.probe(ctx, "filesystem", h.probeFilesystemAll)
	h.probe(ctx, "executor", h.probeExecutorAll)
	h.probe(ctx, "terminal", h.probeTerminalAll)
	h.probe(ctx, "storage", h.probeStorageAll)
	h.probe(ctx, "windowmanager", h.probeWindowManagerAll)
	h.probe(ctx, "notifications", h.probeNotificationsAll)
	h.probe(ctx, "resourceopener", h.probeResourceOpenerAll)
	h.probe(ctx, "interrupter", h.probeInterrupterAll)
	h.probe(ctx, "editor", h.probeEditorAll)
	h.probe(ctx, "parser", h.probeParserAll)
	h.probe(ctx, "lsp", h.probeLSPAll)
	h.probe(ctx, "llm", h.probeLLMAll)
	h.probe(ctx, "debugger", h.probeDebuggerAll)
	h.probe(ctx, "config", h.probeConfigAll)
	h.probe(ctx, "accessors", h.probeAccessors)
	return nil
}

func (h apiHandler) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (h apiHandler) probe(ctx context.Context, name string, fn func(context.Context) error) {
	if err := fn(ctx); err != nil {
		_, _ = h.notifications.Notify(browserapi.LevelError,
			"%s: %s err %v", h.prefix, name, err)
		return
	}
	_, _ = h.notifications.Notify(browserapi.LevelInfo, "%s: %s ok", h.prefix, name)
}

// wmHandler installs tui.Handlers through the window manager so the
// sandbox records the handler-carrying streams (Split, Tab, Bar). The
// host drives the installed handler over a bidi stream; the sandbox
// records the stream open and its first client message.
type wmHandler struct {
	prefix        string
	w             *extensionapi.Workspace
	notifications browserapi.Notifications
}

func (h wmHandler) HandleCommand(ctx context.Context, _ textapi.Command) error {
	wm := h.w.WindowManager(ctx)
	focused, err := wm.Focus()
	if err != nil {
		return fmt.Errorf("focus: %w", err)
	}
	if _, err := wm.Split(browserapi.OrientationLeft, focused, newPanel()); err != nil {
		return fmt.Errorf("split: %w", err)
	}
	h.notify("split")

	uri, _ := workspaceapi.ParseURI("file://" + h.w.DataDir(ctx) + "/panel")
	if _, err := wm.Tab(uri, '*', "panel", newPanel()); err != nil {
		return fmt.Errorf("tab: %w", err)
	}
	h.notify("tab")

	if err := wm.Bar(browserapi.BarConfig{
		Orientation: browserapi.OrientationBottom,
		Size:        1,
	}, newPanel()); err != nil {
		return fmt.Errorf("bar: %w", err)
	}
	h.notify("bar")
	return nil
}

func (h wmHandler) notify(what string) {
	_, _ = h.notifications.Notify(browserapi.LevelInfo, "%s: wm %s ok", h.prefix, what)
}

func (h wmHandler) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// counterHandler installs a stateful panel that renders "count=N" and
// increments N on each <space> key. It lets a spec assert the exact
// rendered output before and after sending key events.
type counterHandler struct {
	w *extensionapi.Workspace
}

func (h counterHandler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	wm := h.w.WindowManager(ctx)
	if _, err := wm.Split(browserapi.OrientationRight, cmd.Window, newCounterPanel()); err != nil {
		return fmt.Errorf("counter split: %w", err)
	}
	return nil
}

func (h counterHandler) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// counterPanel is a deterministic browserapi.Handler: it draws
// "count=N" at the top-left of its viewport and increments N whenever
// it handles a <space> key press. Below, it draws the last mouse press,
// paste or focus change it handled.
type counterPanel struct {
	count int
	last  string
	// paste is the text of the paste in progress, while pasting.
	paste   []rune
	pasting bool
}

func newCounterPanel() *counterPanel { return &counterPanel{} }

func (*counterPanel) Resize(int, int) {}

func (p *counterPanel) Draw(w term.Writer) {
	for y, text := range []string{fmt.Sprintf("count=%d", p.count), p.last} {
		for x, ch := range text {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: ch})
		}
	}
}

func (p *counterPanel) Handle(ev term.Event) (exit, handled bool) {
	switch {
	case ev.Type == term.EventPasteStart:
		p.pasting, p.paste = true, nil
	case ev.Type == term.EventPasteEnd:
		p.pasting, p.last = false, "paste "+string(p.paste)
	case ev.Type == term.EventKey && p.pasting:
		p.paste = append(p.paste, ev.Ch)
	case ev.Type == term.EventKey && ev.Key == term.KeySpace:
		p.count++
	case ev.Type == term.EventMouse && ev.Key == term.MouseLeft:
		p.last = fmt.Sprintf("click %d,%d", ev.MouseX, ev.MouseY)
	case ev.Type == term.EventFocus:
		p.last = "focus"
	default:
		return false, false
	}
	return false, true
}

func (*counterPanel) Close() error              { return nil }
func (*counterPanel) Selection() (string, bool) { return "", false }

func (*counterPanel) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

func (*counterPanel) Dimensions() (int, int) { return 10, 5 }

// panel is a minimal browserapi.Handler installed into the window
// manager. It renders nothing and consumes no events; the sandbox
// only needs the handler stream to be opened, not driven.
type panel struct{}

func newPanel() *panel { return &panel{} }

func (*panel) Resize(int, int)           {}
func (*panel) Draw(term.Writer)          {}
func (*panel) Close() error              { return nil }
func (*panel) Selection() (string, bool) { return "", false }

func (*panel) Handle(term.Event) (bool, bool) { return false, false }

func (*panel) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

// Dimensions lets panel satisfy browserapi.Floating so it can be
// installed as a floating window.
func (*panel) Dimensions() (int, int) { return 10, 5 }
