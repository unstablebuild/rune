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

// Package workspaceshell provides a workspaceapi.Executor wrapper
// that tracks running processes and exposes a "process" shell
// command via ideconsole.CommandHandler.
package workspaceshell

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/console/ideconsole"
	"unstable.build/rune/internal/workspace/processctx"
)

var (
	_ workspaceapi.Executor     = (*Executor)(nil)
	_ schemeapi.Executor        = (*Executor)(nil)
	_ ideconsole.CommandHandler = (*Executor)(nil)
)

// Executor wraps a workspaceapi.Executor, tracking every
// started process so it can be listed, signalled, or stopped
// from a repl.Handler. It implements both
// workspaceapi.Executor and ideconsole.CommandHandler.
type Executor struct {
	mu         sync.RWMutex
	underlying workspaceapi.Executor
	processes  map[workspaceapi.Pid]processInfo
	history    map[workspaceapi.Pid]processInfo
	stats      map[cmdKey]*cmdStats
	extensions map[string]workspaceapi.Pid
	stdio      *stdioStore
	now        func() time.Time // for testing
	stopGrace  time.Duration    // grace period for stop
}

type processInfo struct {
	pid     workspaceapi.Pid
	path    string
	args    []string
	dir     string
	env     []string
	started time.Time
	ended   time.Time
	key     cmdKey
	parent  workspaceapi.Pid // 0 means no parent
	done    chan struct{}    // closed when process exits
	lastErr error            // set on exit for audit history
	stdio   *stdioSink
}

// cmdKey identifies a command by its path and arguments,
// used to aggregate stats across process lifetimes.
type cmdKey string

func makeCmdKey(path string, args []string) cmdKey {
	return cmdKey(path + "\x00" + strings.Join(args, "\x00"))
}

// cmdStats tracks aggregate statistics for a command
// across process lifetimes.
type cmdStats struct {
	lastErr error
}

const defaultStopGrace = 3 * time.Second

// NewExecutor returns an Executor that delegates to
// underlying while tracking running processes.
func NewExecutor(underlying workspaceapi.Executor) *Executor {
	return &Executor{
		underlying: underlying,
		processes:  make(map[workspaceapi.Pid]processInfo),
		history:    make(map[workspaceapi.Pid]processInfo),
		stats:      make(map[cmdKey]*cmdStats),
		extensions: make(map[string]workspaceapi.Pid),
		stdio:      newStdioStore(),
		now:        time.Now,
		stopGrace:  defaultStopGrace,
	}
}

// Start delegates to the underlying executor and tracks the
// started process. The cmd's Watcher is piped so that
// process exit automatically removes the entry.
func (e *Executor) Start(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	ch := make(chan error, 1)
	ours := workspaceapi.ChanProcessWatcher(ch)
	if cmd.Watcher != nil {
		cmd.Watcher = workspaceapi.MultiProcessWatcher(
			cmd.Watcher, ours,
		)
	} else {
		cmd.Watcher = ours
	}

	sink := newStdioSink(e.stdio)
	cmd.Stdout = sink.classify("stdout", cmd.Stdout)
	cmd.Stderr = sink.classify("stderr", cmd.Stderr)

	pid, err := e.underlying.Start(ctx, cmd)
	if err != nil {
		sink.close()
		return pid, err
	}

	key := makeCmdKey(cmd.Path, cmd.Args)
	info := processInfo{
		pid:     pid,
		path:    cmd.Path,
		args:    append([]string{}, cmd.Args...),
		dir:     cmd.Dir,
		env:     append([]string{}, cmd.Env...),
		started: e.now(),
		key:     key,
		done:    make(chan struct{}),
		stdio:   sink,
	}
	sink.identify(pid, info.started)

	if parent, ok := ParentPidFromContext(ctx); ok {
		info.parent = parent
	}

	e.mu.Lock()
	if info.parent == 0 {
		if extensionID, ok := processctx.ExtensionIDFromContext(ctx); ok {
			info.parent = e.extensions[extensionID]
		}
	}
	e.processes[pid] = info
	e.history[pid] = info
	if extensionID, ok := processctx.ExtensionIDFromContext(ctx); ok && info.parent == 0 {
		e.extensions[extensionID] = pid
	}
	e.mu.Unlock()

	go debug.CapturePanicReport(func() {

		exitErr := <-ch
		sink.close()
		e.mu.Lock()
		s := e.stats[key]
		if s == nil {
			s = &cmdStats{}
			e.stats[key] = s
		}
		s.lastErr = exitErr
		if p, ok := e.processes[pid]; ok {
			close(p.done)
		}
		delete(e.processes, pid)
		for extensionID, extensionPid := range e.extensions {
			if extensionPid == pid {
				delete(e.extensions, extensionID)
			}
		}
		if h, ok := e.history[pid]; ok {
			h.ended = e.now()
			h.lastErr = exitErr
			e.history[pid] = h
		}
		e.mu.Unlock()

	})

	return pid, nil
}

// Signal delegates to the underlying executor.
func (e *Executor) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	return e.underlying.Signal(pid, sig)
}

// Close discards captured process output and delegates to the
// underlying executor.
func (e *Executor) Close() error {
	rmErr := e.stdio.remove()
	if err := e.underlying.Close(); err != nil {
		return err
	}
	return rmErr
}

// StartCommand implements schemeapi.Executor by delegating to Start.
func (e *Executor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return e.Start(ctx, cmd)
}

// RegisterProcessCommand registers the "process" REPL command in
// the given registry. Use this for the executor that tracks
// workspace processes (ad-hoc commands, plugins, vte ptys).
func (e *Executor) RegisterProcessCommand(r *ideconsole.CommandRegistry) {
	r.Register("process", "Process management", e)
}

// isProcessCommandName reports whether name is the REPL command
// dispatched to this Executor. Both the workspace-scope "process"
// command and the extensions tracker reuse the same Executor type;
// the latter is now folded under the "extensions" REPL command so
// the Executor is only invoked with cmd.Name == "process" in either
// case.
func isProcessCommandName(name string) bool {
	return name == "process"
}

// HandleCommand dispatches process subcommands.
func (e *Executor) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if !isProcessCommandName(cmd.Name) {
		return nil, repl.ErrNotFound
	}
	if len(cmd.Args) == 0 {
		return e.Help(ctx, nil)
	}
	switch cmd.Args[0] {
	case "status":
		return e.handleStatus(), nil
	case "tree":
		return e.handleTree(), nil
	case "audit":
		return e.handleAudit(), nil
	case "info":
		return e.handleInfo(cmd.Args[1:])
	case "signal":
		return e.handleSignal(cmd.Args[1:])
	case "stop":
		return e.handleStop(cmd.Args[1:])
	case "stdio":
		return e.handleStdio(cmd.Args[1:])
	default:
		return nil, fmt.Errorf("unknown process subcommand: %s", cmd.Args[0])
	}
}

// Complete returns process subcommand and PID completions.
func (e *Executor) Complete(
	_ context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if !isProcessCommandName(cmd) {
		return iterator.Empty[string](), nil
	}
	if len(args) == 0 {
		return iterator.FromSlice(processSubcommands()), nil
	}
	if len(args) == 1 {
		return completeStrings(processSubcommands(), args[0]), nil
	}
	if completesPID(args[0]) {
		return e.completePids(args[len(args)-1]), nil
	}
	return iterator.Empty[string](), nil
}

// Help returns usage information for process subcommands.
func (e *Executor) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return toLines(
		"Process management commands:",
		"",
		"  process status             List running processes",
		"  process audit              List all processes (including exited)",
		"  process tree               Show process tree (parent→child)",
		"  process info <pid>         Show detailed process information",
		"  process signal <pid> [N]   Send signal N to a process (default: SIGTERM)",
		"  process signal -N <pid>    Send signal N to a process",
		"  process stop <pid>         Gracefully stop a process (SIGTERM, then SIGKILL)",
		"  process stdio <pid>        Show captured process output",
	), nil
}

type psEntry struct {
	pid     workspaceapi.Pid
	uptime  time.Duration
	lastErr string
	command string
}

func (e *Executor) handleStatus() iterator.Iterator[component.Responsive] {
	now := e.now()
	e.mu.RLock()
	entries := make([]psEntry, 0, len(e.processes))
	for _, info := range e.processes {
		cmd := formatCmd(info)
		lastErr := "—"
		if s := e.stats[info.key]; s != nil {
			lastErr = formatLastErr(s.lastErr)
		}
		entries = append(entries, psEntry{
			pid:     info.pid,
			uptime:  now.Sub(info.started),
			lastErr: lastErr,
			command: cmd,
		})
	}
	e.mu.RUnlock()
	return renderProcessTableMarkdown(entries)
}

func (e *Executor) handleAudit() iterator.Iterator[component.Responsive] {
	now := e.now()
	e.mu.RLock()
	entries := make([]psEntry, 0, len(e.history))
	for _, info := range e.history {
		cmd := formatCmd(info)
		uptime := now.Sub(info.started)
		if !info.ended.IsZero() {
			uptime = info.ended.Sub(info.started)
		}
		lastErr := "—"
		if info.lastErr != nil {
			lastErr = formatLastErr(info.lastErr)
		} else if s := e.stats[info.key]; s != nil {
			lastErr = formatLastErr(s.lastErr)
		}
		entries = append(entries, psEntry{
			pid:     info.pid,
			uptime:  uptime,
			lastErr: lastErr,
			command: cmd,
		})
	}
	e.mu.RUnlock()
	return renderProcessTableMarkdown(entries)
}

func (e *Executor) handleTree() iterator.Iterator[component.Responsive] {
	now := e.now()
	e.mu.RLock()
	infos := make([]processInfo, 0, len(e.processes))
	for _, info := range e.processes {
		infos = append(infos, info)
	}
	e.mu.RUnlock()

	// Build children map and find roots.
	children := make(map[workspaceapi.Pid][]processInfo)
	var roots []processInfo
	for _, info := range infos {
		if info.parent == 0 {
			roots = append(roots, info)
		} else {
			children[info.parent] = append(
				children[info.parent], info)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].pid < roots[j].pid
	})
	for k := range children {
		c := children[k]
		sort.Slice(c, func(i, j int) bool {
			return c[i].pid < c[j].pid
		})
	}

	var b strings.Builder
	b.WriteString("- **Process tree**\n")

	var walk func(info processInfo, depth int)
	walk = func(info processInfo, depth int) {
		cmd := formatCmd(info)
		indent := strings.Repeat("  ", depth)
		ppidStr := "—"
		if info.parent != 0 {
			ppidStr = strconv.Itoa(int(info.parent))
		}
		fmt.Fprintf(
			&b,
			"%s- **PID %d** (PPID: %s, uptime: %s) — `%s`\n",
			indent,
			info.pid,
			ppidStr,
			formatDuration(now.Sub(info.started)),
			cmd,
		)
		for _, child := range children[info.pid] {
			walk(child, depth+1)
		}
	}

	for _, root := range roots {
		walk(root, 0)
	}
	return markdownResponsive(b.String())
}

func (e *Executor) handleInfo(
	args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: process info <pid>")
	}
	pidVal, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("invalid pid: %s", args[0])
	}
	pid := workspaceapi.Pid(pidVal)

	now := e.now()
	e.mu.RLock()
	info, ok := e.processes[pid]
	fromHistory := false
	if !ok {
		info, ok = e.history[pid]
		fromHistory = ok
	}
	var stats *cmdStats
	if ok {
		stats = e.stats[info.key]
	}
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("process %d not found", pid)
	}

	cmd := formatCmd(info)

	uptime := now.Sub(info.started)
	if !info.ended.IsZero() {
		uptime = info.ended.Sub(info.started)
	}

	lastErr := "—"
	if info.lastErr != nil {
		lastErr = formatLastErr(info.lastErr)
	} else if stats != nil {
		lastErr = formatLastErr(stats.lastErr)
	}

	ppidStr := "—"
	if info.parent != 0 {
		ppidStr = strconv.Itoa(int(info.parent))
	}

	infoLines := []string{
		fmt.Sprintf("- **PID:** `%d`", info.pid),
		fmt.Sprintf("- **Parent PID:** `%s`", ppidStr),
		fmt.Sprintf("- **Command:** `%s`", cmd),
		fmt.Sprintf("- **Directory:** `%s`", info.dir),
		fmt.Sprintf("- **Started:** %s", info.started.Format(time.RFC3339)),
		fmt.Sprintf("- **Uptime:** %s", formatDuration(uptime)),
	}
	if fromHistory {
		infoLines = append(infoLines,
			fmt.Sprintf("- **Ended:** %s",
				info.ended.Format(time.RFC3339)))
	}
	infoLines = append(infoLines,
		fmt.Sprintf("- **Last Error:** %s", lastErr))

	if len(info.env) > 0 {
		infoLines = append(infoLines, "- **Environment:**")
		for _, envVar := range info.env {
			infoLines = append(infoLines,
				fmt.Sprintf("  - `%s`", redactEnv(envVar)))
		}
	}

	return markdownResponsive(strings.Join(infoLines, "\n")), nil
}

func formatLastErr(err error) string {
	if err == nil {
		return "—"
	}
	const maxLen = 30
	s := err.Error()
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// formatCmd renders a tracked process's command for display.
// Empty info.path means the process was started via the
// workspaceapi "use the user's login shell" protocol contract
// (Cmd.Path == ""). The actual binary is resolved inside the
// file scheme — possibly on a remote host for SSH workspaces —
// so the tracker never sees a concrete path. Render a stable
// placeholder so process status/tree/info don't show an empty
// cell for these entries.
func formatCmd(info processInfo) string {
	path := info.path
	if path == "" {
		path = "(login shell)"
	}
	if len(info.args) > 0 {
		return path + " " + strings.Join(info.args, " ")
	}
	return path
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm%ds", m, s)
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

func (e *Executor) handleSignal(
	args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: process signal [-signal] <pid>")
	}

	sig := syscall.SIGTERM
	pidStr := args[0]

	if strings.HasPrefix(pidStr, "-") && len(args) > 1 {
		n, err := strconv.Atoi(pidStr[1:])
		if err != nil {
			return nil, fmt.Errorf("invalid signal: %s", pidStr[1:])
		}
		sig = syscall.Signal(n)
		pidStr = args[1]
	} else if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, fmt.Errorf("invalid signal: %s", args[1])
		}
		sig = syscall.Signal(n)
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid pid: %s", pidStr)
	}

	if err := e.Signal(workspaceapi.Pid(pid), sig); err != nil {
		return nil, err
	}

	return iterator.Empty[component.Responsive](), nil
}

func (e *Executor) handleStop(
	args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: process stop <pid>")
	}
	pidVal, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("invalid pid: %s", args[0])
	}
	pid := workspaceapi.Pid(pidVal)

	// Look up the done channel while holding the lock.
	e.mu.RLock()
	info, tracked := e.processes[pid]
	e.mu.RUnlock()

	// Send SIGTERM first.
	if err := e.Signal(pid, syscall.SIGTERM); err != nil {
		return nil, err
	}

	if !tracked {
		// Process not tracked; fall through to SIGKILL.
		if err := e.Signal(pid, syscall.SIGKILL); err != nil {
			return nil, err
		}
		return toLines("  sent SIGKILL (untracked process)"), nil
	}

	// Wait for graceful exit or timeout.
	select {
	case <-info.done:
		return toLines(
			fmt.Sprintf("  process %d stopped", pid),
		), nil
	case <-time.After(e.stopGrace):
		if err := e.Signal(pid, syscall.SIGKILL); err != nil {
			return nil, err
		}
		return toLines(
			fmt.Sprintf("  process %d killed (SIGTERM timed out)", pid),
		), nil
	}
}

func processSubcommands() []string {
	return []string{
		"status", "audit", "tree", "info", "signal", "stop", "stdio",
	}
}

func completesPID(subcommand string) bool {
	return subcommand == "signal" || subcommand == "stop" ||
		subcommand == "info" || subcommand == "stdio"
}

func completeStrings(values []string, prefix string) iterator.Iterator[string] {
	matches := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			matches = append(matches, value)
		}
	}
	return iterator.FromSlice(matches)
}

func (e *Executor) completePids(prefix string) iterator.Iterator[string] {
	e.mu.RLock()
	pids := make([]string, 0, len(e.processes))
	for pid := range e.processes {
		s := strconv.Itoa(int(pid))
		if strings.HasPrefix(s, prefix) {
			pids = append(pids, s)
		}
	}
	e.mu.RUnlock()
	sort.Strings(pids)
	return iterator.FromSlice(pids)
}

func toLines(ss ...string) iterator.Iterator[component.Responsive] {
	out := make([]component.Responsive, len(ss))
	for i, s := range ss {
		out[i] = toResponsive(s)
	}
	return iterator.FromSlice(out)
}

func renderProcessTableMarkdown(entries []psEntry) iterator.Iterator[component.Responsive] {
	return markdownResponsive(buildProcessTableMarkdownSource(entries))
}

// buildProcessTableMarkdownSource produces the markdown source
// for the process status/audit table. Each row is sanitized so
// that newlines or backticks inside a command or lastErr cannot
// break out of their cell. Entries are sorted by PID.
func buildProcessTableMarkdownSource(entries []psEntry) string {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].pid < entries[j].pid
	})
	var b strings.Builder
	b.WriteString("| PID | UPTIME | LAST ERR | COMMAND |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, ent := range entries {
		fmt.Fprintf(
			&b,
			"| `%d` | `%s` | %s | `%s` |\n",
			ent.pid,
			formatDuration(ent.uptime),
			escapeMarkdownTableCell(ent.lastErr),
			escapeMarkdownTableCell(ent.command),
		)
	}
	return b.String()
}

func toResponsive(s string) component.Responsive {
	return component.NewResponsiveString(
		s, component.StringResponsiveConfig{},
	)
}

func markdownResponsive(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		return iterator.FromSlice([]component.Responsive{toResponsive(content)})
	}
	return iterator.FromSlice([]component.Responsive{md})
}

// escapeMarkdownTableCell sanitizes a string so it can safely be
// embedded inside a single markdown table cell wrapped in
// backticks. It collapses line breaks to a visible single-line
// marker, escapes backticks so they cannot terminate the
// surrounding `…`, and escapes pipes so they cannot terminate
// the cell.
func escapeMarkdownTableCell(s string) string {
	// Order matters: collapse CRLF to a single marker before
	// touching lone CR or LF.
	s = strings.ReplaceAll(s, "\r\n", "␤")
	s = strings.ReplaceAll(s, "\n", "␤")
	s = strings.ReplaceAll(s, "\r", "␤")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

// ContextWithParentPid returns a context carrying the given
// parent PID. When a process is started with this context, the
// executor records it as a child of the specified parent.
func ContextWithParentPid(
	ctx context.Context, pid workspaceapi.Pid,
) context.Context {
	return processctx.ContextWithParentPid(ctx, pid)
}

// ParentPidFromContext extracts a parent PID previously stored
// via ContextWithParentPid. Returns 0, false when absent.
func ParentPidFromContext(ctx context.Context) (workspaceapi.Pid, bool) {
	return processctx.ParentPidFromContext(ctx)
}

// secretEnvKeys lists environment variable names whose values
// must be redacted in process info output.
var secretEnvKeys = map[string]bool{
	"RUNE_CERT":  true,
	"RUNE_TOKEN": true,
	"IDE_CERT":   true,
	"IDE_TOKEN":  true,
}

// redactEnv replaces the value portion of sensitive environment
// variables with "****". Non-sensitive variables and entries
// without an "=" are returned unchanged.
func redactEnv(envVar string) string {
	key, _, ok := strings.Cut(envVar, "=")
	if !ok {
		return envVar
	}
	if secretEnvKeys[key] {
		return key + "=****"
	}
	return envVar
}
