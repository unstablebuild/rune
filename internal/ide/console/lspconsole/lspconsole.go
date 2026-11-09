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

// Package lspconsole exposes the `lsp` REPL command, which inspects and
// controls the language servers of a workspace.
package lspconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/idelsp"
)

// CommandName is the top-level REPL command exposed by this console.
const CommandName = "lsp"

var commandManual = textapi.CommandManual{
	Name: CommandName,
	Summary: "Inspect and control the language servers of this workspace. " +
		"A <server> is a language id such as `go`, or a backend of a language " +
		"served by several such as `ruff` or `python/ruff`, optionally followed " +
		"by `@<root>`, the server's project root relative to the workspace. " +
		"The backends of a language at a root are started and stopped together.",
	Synopsis: "<command> [<server>]",
	Commands: []textapi.CommandManual{
		{
			Name:    "status",
			Summary: "Lists the language servers with their state, process and last error.",
		},
		{
			Name:     "info",
			Synopsis: "<server>",
			Summary: "Shows how a server runs: its command, environment, process, " +
				"version and last error.",
		},
		{
			Name:     "params",
			Synopsis: "<server>",
			Summary:  "Shows the initialize request sent to a server, as JSON.",
		},
		{
			Name:     "capabilities",
			Synopsis: "<server>",
			Summary:  "Shows the initialize response of a server, with its capabilities, as JSON.",
		},
		{
			Name:     "restart",
			Synopsis: "<server>",
			Summary:  "Stops a server if it is running and starts it again with the same parameters.",
		},
		{
			Name:     "start",
			Synopsis: "<server>",
			Summary:  "Starts a stopped or failed server with the parameters it was initialized with.",
		},
		{
			Name:     "stop",
			Synopsis: "<server>",
			Summary:  "Shuts down a server until it is started again.",
		},
	},
}

// Manual returns the REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

// Servers is the language server manager the console inspects and
// controls.
type Servers interface {
	RootURI() string
	Servers() []idelsp.ServerStatus
	Start(ctx context.Context, languageID, rootURI string) error
	Stop(ctx context.Context, languageID, rootURI string) error
	Restart(ctx context.Context, languageID, rootURI string) error
}

var _ Servers = (*idelsp.Manager)(nil)

// Handler is the dispatcher for the `lsp` command tree.
type Handler struct {
	servers Servers
	now     func() time.Time
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler for servers.
func New(servers Servers) *Handler {
	return &Handler{servers: servers, now: time.Now}
}

// HandleCommand satisfies repl.CommandHandler.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return h.Help(ctx, nil)
	}
	sub, args := cmd.Args[0], cmd.Args[1:]
	switch sub {
	case "help":
		return h.Help(ctx, args)
	case "status":
		return markdownOutput(h.statusMarkdown()), nil
	case "restart", "start", "stop":
		return h.control(ctx, sub, args)
	}
	render, ok := map[string]func([]idelsp.ServerStatus) string{
		"info":         h.infoMarkdown,
		"params":       h.paramsMarkdown,
		"capabilities": h.capabilitiesMarkdown,
	}[sub]
	if !ok {
		return nil, fmt.Errorf("unknown lsp command: %s", sub)
	}
	sts, err := h.selectServers(sub, args)
	if err != nil {
		return nil, err
	}
	return markdownOutput(render(sts)), nil
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	switch {
	case len(args) <= 1:
		var prefix string
		if len(args) == 1 {
			prefix = args[0]
		}
		names := make([]string, 0, len(commandManual.Commands))
		for _, c := range commandManual.Commands {
			names = append(names, c.Name)
		}
		return iterator.FromSlice(withPrefix(names, prefix)), nil
	case len(args) == 2 && args[0] != "status":
		return iterator.FromSlice(withPrefix(h.selectors(), args[1])), nil
	}
	return iterator.Empty[string](), nil
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := commandManual
	if len(args) > 0 {
		for _, c := range commandManual.Commands {
			if c.Name == args[0] {
				man = c
				man.Name = CommandName + " " + c.Name
			}
		}
	}
	return markdownOutput(usageMarkdown(man)), nil
}

func (h *Handler) selectServers(sub string, args []string) ([]idelsp.ServerStatus, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: %s %s <server>", CommandName, sub)
	}
	return h.resolve(args[0])
}

// resolve returns the servers sel names: by language id, backend name
// or language/backend, optionally qualified by @root.
func (h *Handler) resolve(sel string) ([]idelsp.ServerStatus, error) {
	name, root, qualified := strings.Cut(sel, "@")
	var ret []idelsp.ServerStatus
	for _, st := range h.servers.Servers() {
		if name != st.Language && name != st.Name && name != serverName(st) {
			continue
		}
		if qualified && !h.rootMatches(st.RootURI, root) {
			continue
		}
		ret = append(ret, st)
	}
	if len(ret) == 0 {
		return nil, fmt.Errorf("no language server matches %q; see `%s status`", sel, CommandName)
	}
	return ret, nil
}

func (h *Handler) rootMatches(rootURI, sel string) bool {
	if sel == rootURI {
		return true
	}
	sel = path.Clean(sel)
	return sel == h.relRoot(rootURI) || sel == rootPath(rootURI)
}

// relRoot returns rootURI as a path relative to the workspace root, or
// as an absolute path when it is outside of it.
func (h *Handler) relRoot(rootURI string) string {
	root := rootPath(rootURI)
	ws := strings.TrimSuffix(rootPath(h.servers.RootURI()), "/")
	if root == ws {
		return "."
	}
	if rel, ok := strings.CutPrefix(root, ws+"/"); ok {
		return rel
	}
	return root
}

func rootPath(rootURI string) string {
	return strings.TrimPrefix(rootURI, "file://")
}

// serverName names a server in output: its language, or language/backend
// for a language served by several backends.
func serverName(st idelsp.ServerStatus) string {
	if st.Name == st.Language {
		return st.Language
	}
	return st.Language + "/" + st.Name
}

func (h *Handler) label(language, rootURI string) string {
	return fmt.Sprintf("`%s` at `%s`", language, h.relRoot(rootURI))
}

func (h *Handler) selectors() []string {
	var ret []string
	for _, st := range h.servers.Servers() {
		ret = append(ret, st.Language, st.Language+"@"+h.relRoot(st.RootURI))
		if st.Name != st.Language {
			ret = append(ret, st.Name, serverName(st))
		}
	}
	slices.Sort(ret)
	return slices.Compact(ret)
}

var controlDone = map[string]string{
	"restart": "Restarted", "start": "Started", "stop": "Stopped",
}

func (h *Handler) control(
	ctx context.Context, sub string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sts, err := h.selectServers(sub, args)
	if err != nil {
		return nil, err
	}
	op := map[string]func(context.Context, string, string) error{
		"restart": h.servers.Restart, "start": h.servers.Start, "stop": h.servers.Stop,
	}[sub]
	type key struct{ language, root string }
	seen := make(map[key]bool)
	var done []string
	var errs []error
	for _, st := range sts {
		k := key{st.Language, st.RootURI}
		if seen[k] {
			continue
		}
		seen[k] = true
		label := h.label(k.language, k.root)
		if err := op(ctx, k.language, k.root); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", sub, label, err))
			continue
		}
		done = append(done, fmt.Sprintf("- %s %s", controlDone[sub], label))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return markdownOutput(strings.Join(done, "\n") + "\n"), nil
}

func (h *Handler) statusMarkdown() string {
	sts := h.servers.Servers()
	if len(sts) == 0 {
		return "No language server has been initialized in this workspace.\n"
	}
	now := h.now()
	var b strings.Builder
	b.WriteString("| SERVER | ROOT | STATE | PID | UPTIME | RESTARTS | LAST ERROR |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, st := range sts {
		pid, uptime := "—", "—"
		if st.Pid != 0 {
			pid = fmt.Sprintf("`%d`", st.Pid)
		}
		if st.State == idelsp.ServerRunning {
			uptime = fmt.Sprintf("`%s`", formatDuration(now.Sub(st.Started)))
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %d | %s |\n",
			tableCell(serverName(st)), tableCell(h.relRoot(st.RootURI)), st.State,
			pid, uptime, st.Restarts, tableCell(errTail(st.LastError)))
	}
	return b.String()
}

func (h *Handler) heading(st idelsp.ServerStatus) string {
	return fmt.Sprintf("### %s\n\n", h.label(serverName(st), st.RootURI))
}

func (h *Handler) infoMarkdown(sts []idelsp.ServerStatus) string {
	now := h.now()
	var b strings.Builder
	for i, st := range sts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(h.heading(st))
		fmt.Fprintf(&b, "- **State:** %s\n", st.State)
		fmt.Fprintf(&b, "- **Command:** %s\n",
			inlineCode(strings.Join(append([]string{st.Path}, st.Args...), " ")))
		fmt.Fprintf(&b, "- **Root:** %s\n", inlineCode(rootPath(st.RootURI)))
		if st.Pid != 0 {
			fmt.Fprintf(&b, "- **PID:** `%d`, output in `process stdio %d`\n", st.Pid, st.Pid)
		} else {
			b.WriteString("- **PID:** —\n")
		}
		if !st.Started.IsZero() {
			fmt.Fprintf(&b, "- **Started:** %s\n", st.Started.Format(time.RFC3339))
		}
		if st.State == idelsp.ServerRunning {
			fmt.Fprintf(&b, "- **Uptime:** %s\n", formatDuration(now.Sub(st.Started)))
		}
		fmt.Fprintf(&b, "- **Restarts:** %d\n", st.Restarts)
		if info := serverInfo(st.Result); info != "" {
			fmt.Fprintf(&b, "- **Server:** %s\n", info)
		}
		if len(st.Env) > 0 {
			b.WriteString("- **Environment:**\n")
			for _, kv := range st.Env {
				fmt.Fprintf(&b, "  - %s\n", inlineCode(kv))
			}
		}
		if st.LastError == nil {
			b.WriteString("- **Last Error:** —\n")
		} else {
			b.WriteString("- **Last Error:**\n\n")
			b.WriteString(codeBlock("text", st.LastError.Error()))
		}
	}
	return b.String()
}

func (h *Handler) paramsMarkdown(sts []idelsp.ServerStatus) string {
	var b strings.Builder
	for i, st := range sts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(h.heading(st))
		if len(st.Params) == 0 {
			b.WriteString("No initialize request was sent to this server.\n")
			continue
		}
		b.WriteString(codeBlock("json", indentJSON(st.Params)))
	}
	return b.String()
}

func (h *Handler) capabilitiesMarkdown(sts []idelsp.ServerStatus) string {
	var b strings.Builder
	for i, st := range sts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(h.heading(st))
		if len(st.Result) == 0 {
			fmt.Fprintf(&b, "The server has not completed the initialize handshake "+
				"(state: %s). See `%s info %s`.\n", st.State, CommandName, serverName(st))
			continue
		}
		b.WriteString(codeBlock("json", indentJSON(st.Result)))
	}
	return b.String()
}

// serverInfo returns the name and version a server reported in its
// initialize response, or "" if it reported none.
func serverInfo(result json.RawMessage) string {
	var res struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if len(result) == 0 || json.Unmarshal(result, &res) != nil {
		return ""
	}
	return strings.TrimSpace(res.ServerInfo.Name + " " + res.ServerInfo.Version)
}

func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// codeBlock fences content with more backticks than any run in it, so
// the content cannot close the block early.
func codeBlock(lang, content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimSuffix(content, "\n") + "\n" + fence + "\n"
}

// inlineCode renders s as inline code. Backticks cannot be escaped in
// a code span, so they are replaced.
func inlineCode(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// tableCell keeps s from breaking out of the table cell it is embedded
// in, wrapped in backticks or not.
func tableCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "␤")
	s = strings.ReplaceAll(s, "\n", "␤")
	s = strings.ReplaceAll(s, "\r", "␤")
	s = strings.ReplaceAll(s, "`", "\\`")
	return strings.ReplaceAll(s, "|", "\\|")
}

// errCellWidth is how much of an error a table cell shows, in runes.
const errCellWidth = 60

// errTail keeps the tail of err: wrapped errors end in their root cause,
// while their head is often a prefix shared by every failure.
func errTail(err error) string {
	if err == nil {
		return "—"
	}
	r := []rune(err.Error())
	if len(r) <= errCellWidth {
		return string(r)
	}
	return "…" + string(r[len(r)-(errCellWidth-1):])
}

func formatDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

func withPrefix(names []string, prefix string) []string {
	var ret []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			ret = append(ret, n)
		}
	}
	slices.Sort(ret)
	return ret
}

func usageMarkdown(man textapi.CommandManual) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s`\n\n", man.Name)
	if man.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", man.Summary)
	}
	if man.Synopsis != "" {
		fmt.Fprintf(&b, "**Usage:** `%s %s`\n\n", man.Name, man.Synopsis)
	}
	if len(man.Commands) > 0 {
		b.WriteString("### Subcommands\n\n")
		for _, c := range man.Commands {
			invocation := c.Name
			if c.Synopsis != "" {
				invocation = c.Name + " " + c.Synopsis
			}
			fmt.Fprintf(&b, "- `%s`\n  %s\n", invocation, c.Summary)
		}
	}
	return b.String()
}

func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}
