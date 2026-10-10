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

package debugshell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-dap"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	tuimarkdown "unstable.build/rune/internal/component/markdown"
)

// markdown renders content via the local markdown component.
// On parse error, falls back to a plain responsive string.
func markdown(content string) iterator.Iterator[component.Responsive] {
	cfg := tuimarkdown.DefaultConfig()
	cfg.HeaderPrefix = false
	md, err := tuimarkdown.NewWithConfig(content, cfg)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}

// line wraps a single plain string as a responsive component.
func line(s string) component.Responsive {
	return component.NewResponsiveString(s, component.StringResponsiveConfig{})
}

// markdownComponent renders content as a single markdown
// component, falling back to a plain responsive string on
// parse error. Useful when a code path returns a single
// `component.Responsive` rather than an iterator (for
// example, the per-event render in the session iterator).
func markdownComponent(content string) component.Responsive {
	md, err := tuimarkdown.New(content)
	if err != nil {
		return component.NewResponsiveString(
			content, component.StringResponsiveConfig{})
	}
	return md
}

// lines wraps strings as responsive components.
func lines(ss ...string) iterator.Iterator[component.Responsive] {
	out := make([]component.Responsive, len(ss))
	for i, s := range ss {
		out[i] = line(s)
	}
	return iterator.FromSlice(out)
}

func renderThreads(threads []dap.Thread) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(threads) == 0 {
		b.WriteString("No threads.\n")
		return markdown(b.String())
	}
	b.WriteString("Threads:\n\n")
	for _, t := range threads {
		fmt.Fprintf(&b, "- **%d** %s\n", t.Id, t.Name)
	}
	return markdown(b.String())
}

func renderStackFrames(frames []dap.StackFrame) iterator.Iterator[component.Responsive] {
	return markdown(formatStackTraceMarkdown(frames))
}

// formatStackTraceMarkdown renders frames as a markdown
// document identical to what `debugger stack-trace` returns.
// Exposed so the handler can push the same body into the
// session transcript automatically after every Stopped.
func formatStackTraceMarkdown(frames []dap.StackFrame) string {
	var b strings.Builder
	if len(frames) == 0 {
		b.WriteString("No stack frames.\n")
		return b.String()
	}
	b.WriteString("Stack trace:\n\n")
	for i, f := range frames {
		src := ""
		if f.Source != nil {
			src = f.Source.Path
			if src == "" {
				src = f.Source.Name
			}
		}
		if src != "" {
			fmt.Fprintf(&b, "%d. **%s** `%s:%d`\n", i, f.Name, src, f.Line)
		} else {
			fmt.Fprintf(&b, "%d. **%s** (line %d)\n", i, f.Name, f.Line)
		}
		// The instruction pointer is the canonical address to
		// pass to `disassemble`. Surface it inline so the user
		// can copy/paste without having to call evaluate.
		if f.InstructionPointerReference != "" {
			fmt.Fprintf(&b, "   - ip: `%s`\n",
				f.InstructionPointerReference)
		}
	}
	return b.String()
}

func renderScopes(scopes []dap.Scope) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(scopes) == 0 {
		b.WriteString("No scopes.\n")
		return markdown(b.String())
	}
	b.WriteString("Scopes:\n\n")
	for _, s := range scopes {
		fmt.Fprintf(&b, "- **%s** (ref %d)\n", s.Name, s.VariablesReference)
	}
	return markdown(b.String())
}

func renderVariables(vars []dap.Variable) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(vars) == 0 {
		b.WriteString("No variables.\n")
		return markdown(b.String())
	}
	b.WriteString("Variables:\n\n")
	for _, v := range vars {
		if v.Type != "" {
			fmt.Fprintf(&b, "- **%s** `%s` = `%s`\n", v.Name, v.Type, v.Value)
		} else {
			fmt.Fprintf(&b, "- **%s** = `%s`\n", v.Name, v.Value)
		}
	}
	return markdown(b.String())
}

func renderModules(modules []dap.Module) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(modules) == 0 {
		b.WriteString("No modules.\n")
		return markdown(b.String())
	}
	b.WriteString("Modules:\n\n")
	for _, m := range modules {
		fmt.Fprintf(&b, "- **%v** %s (%s)\n", m.Id, m.Name, m.Path)
	}
	return markdown(b.String())
}

func renderSources(sources []dap.Source) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(sources) == 0 {
		b.WriteString("No sources.\n")
		return markdown(b.String())
	}
	b.WriteString("Loaded sources:\n\n")
	for _, s := range sources {
		p := s.Path
		if p == "" {
			p = s.Name
		}
		fmt.Fprintf(&b, "- `%s`\n", p)
	}
	return markdown(b.String())
}

func renderValue(label, value string) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n\n```go\n%s\n```\n", label, value)
	return markdown(b.String())
}

func renderDisassembly(
	insts []dap.DisassembledInstruction,
) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	if len(insts) == 0 {
		b.WriteString("No instructions.\n")
		return markdown(b.String())
	}
	b.WriteString("Disassembly:\n\n```dissassembly\n")
	for _, ins := range insts {
		fmt.Fprintf(&b, "%-18s %s\n", ins.Address, ins.Instruction)
	}
	b.WriteString("```\n")
	return markdown(b.String())
}

// newSessionIterator builds an iterator that turns session events
// (DAP events and the final close notification) into responsive
// markdown components. The initial element announces the session
// ID and language; subsequent elements are the streamed events.
// The iterator ends when the subscriber channel closes.
//
// onClose, when non-nil, is invoked exactly once when the
// iterator observes the close signal — it is the caller's
// hook to clear any IDE state (location lists, gutter
// markers) that should not outlive the session even if
// OnClose was not driven through the adapter (e.g. when the
// REPL drained the iterator after a user-initiated terminate
// that bypassed the adapter's close path).
func newSessionIterator(
	langID, sessionID string, ch <-chan sessionEvent,
	progress repl.ProgressWriter, onClose func(),
) iterator.Iterator[component.Responsive] {
	return &sessionIterator{
		langID:    langID,
		sessionID: sessionID,
		ch:        ch,
		progress:  progress,
		onClose:   onClose,
	}
}

type sessionIterator struct {
	langID    string
	sessionID string
	ch        <-chan sessionEvent
	progress  repl.ProgressWriter
	onClose   func()
	announced bool
	closed    bool
}

func (s *sessionIterator) Next(ctx context.Context) (component.Responsive, bool) {
	if !s.announced {
		s.announced = true
		return markdownComponent(fmt.Sprintf(
			"Debug session started (lang=%s, id=%s).",
			s.langID, s.sessionID)), true
	}
	if s.closed {
		return nil, false
	}
	// NOTE: this iterator is intentionally tied to the
	// session's lifetime — *not* to the caller's context.
	// The REPL cancels its per-command context on Ctrl-C
	// (handler.dispatchCommand → cmdCancel), but the debug
	// session keeps running and the user expects DAP events
	// to keep streaming until they explicitly terminate it.
	// Honoring ctx.Done here would silently stop rendering
	// every event after the first Ctrl-C.
	//
	// The iterator ends only when the events channel closes,
	// which happens from OnClose / resetSession (i.e. an
	// explicit `debugger terminate`, an adapter-side
	// disconnect, or the debuggee exiting).
	ev, ok := <-s.ch
	if !ok {
		s.runOnClose()
		return nil, false
	}
	if ev.closed {
		s.runOnClose()
		reason := ev.reason
		if reason == "" {
			reason = "terminated"
		}
		return markdownComponent(fmt.Sprintf(
			"Debug session ended (%s).", reason)), true
	}
	// Pre-rendered handler-side entries (e.g. the auto
	// stack-trace pushed after every Stopped) are emitted
	// verbatim.
	if ev.rendered != "" {
		return markdownComponent(ev.rendered), true
	}
	// Progress events are routed to the REPL's progress
	// reporter and not rendered as transcript entries.
	// Returning nil from Next would end iteration, so recurse
	// to fetch the next non-progress event.
	if s.handleProgressEvent(ev.ev) {
		return s.Next(ctx)
	}
	// Hide adapter chatter in the `console` category (e.g.
	// delve's "Type 'dlv help' ..." banner). The content is
	// still tee'd to the on-disk log via appendOutput;
	// suppressing it here keeps the REPL transcript focused on
	// the user's session.
	if isConsoleNoise(ev.ev) {
		return s.Next(ctx)
	}
	return markdownComponent(formatDAPEvent(ev.ev)), true
}

// isConsoleNoise reports whether ev is an OutputEvent whose
// category is `console` — adapter banners and similar UX
// chrome that should not pollute the REPL transcript.
func isConsoleNoise(ev dap.EventMessage) bool {
	out, ok := ev.(*dap.OutputEvent)
	if !ok {
		return false
	}
	return out.Body.Category == "console"
}

// handleProgressEvent maps DAP progress events onto the
// REPL's ProgressWriter. Returns true when ev was a progress
// event (and the caller should *not* emit a transcript entry
// for it).
func (s *sessionIterator) handleProgressEvent(ev dap.EventMessage) bool {
	if s.progress == nil || ev == nil {
		// Without a writer there is nothing to update; fall
		// through and let the event be rendered as a normal
		// transcript line so the user still sees it.
		return false
	}
	switch e := ev.(type) {
	case *dap.ProgressStartEvent:
		// Pct values from DAP are in 0..100 with no
		// guaranteed total. Use 100 as the synthetic total
		// so the writer can render a familiar percentage.
		s.progress.Progress(int64(e.Body.Percentage), 100,
			progressUnits(e.Body.Title, e.Body.Message))
		return true
	case *dap.ProgressUpdateEvent:
		s.progress.Progress(int64(e.Body.Percentage), 100,
			e.Body.Message)
		return true
	case *dap.ProgressEndEvent:
		s.progress.Progress(100, 100, e.Body.Message)
		return true
	}
	return false
}

// progressUnits picks the most descriptive non-empty label
// available for a Start event: title falls back to message.
func progressUnits(title, msg string) string {
	if title != "" && msg != "" {
		return title + ": " + msg
	}
	if title != "" {
		return title
	}
	return msg
}

func (s *sessionIterator) runOnClose() {
	if s.onClose == nil {
		return
	}
	fn := s.onClose
	s.onClose = nil
	fn()
}

func (s *sessionIterator) Err() error   { return nil }
func (s *sessionIterator) Close() error { return nil }

// newLaunchIterator returns an iterator that yields the
// `static` strings first, then a "Waiting for debuggee to
// initialize…" progress line, then blocks until either init
// is closed (signalled when *dap.InitializedEvent arrives)
// or ctx is cancelled. The iterator never delivers a
// terminal value beyond those lines — its purpose is to keep
// the prompt visibly busy while the adapter is bringing the
// debuggee up.
//
// init may be nil, in which case the iterator ends after the
// static lines have been emitted (used as a fallback when
// arming the channel was not possible).
func newLaunchIterator(
	static []string, init <-chan struct{},
) iterator.Iterator[component.Responsive] {
	return &launchIterator{
		static: static,
		init:   init,
	}
}

type launchIterator struct {
	static     []string
	init       <-chan struct{}
	idx        int
	progressed bool
	waited     bool
	cancelled  bool
	done       bool
}

func (l *launchIterator) Next(ctx context.Context) (component.Responsive, bool) {
	if l.done {
		return nil, false
	}
	if l.idx < len(l.static) {
		s := l.static[l.idx]
		l.idx++
		return line(s), true
	}
	if l.init == nil {
		l.done = true
		return nil, false
	}
	if !l.progressed {
		l.progressed = true
		return line("Waiting for debuggee to initialize…"), true
	}
	if l.waited {
		l.done = true
		if l.cancelled {
			return line("Wait cancelled."), true
		}
		return nil, false
	}
	l.waited = true
	select {
	case <-l.init:
		l.done = true
		return nil, false
	case <-ctx.Done():
		l.cancelled = true
		l.done = true
		return line("Wait cancelled."), true
	}
}

func (l *launchIterator) Err() error   { return nil }
func (l *launchIterator) Close() error { return nil }

// formatDAPEvent renders a DAP event as a short markdown
// snippet. The convention is to wrap the event payload in a
// fenced ` ```event ` block so async adapter messages are
// visually distinct from synchronous command output (which
// uses regular markdown formatting). Unknown events fall back
// to the event name followed by the JSON-marshalled body.
func formatDAPEvent(ev dap.EventMessage) string {
	if ev == nil {
		return codeBlock("event", "<nil>")
	}
	base := ev.GetEvent()
	name := base.Event
	switch e := ev.(type) {
	case *dap.InitializedEvent:
		return "**Debuggee initialized.**"
	case *dap.OutputEvent:
		out := strings.TrimRight(e.Body.Output, "\n")
		cat := e.Body.Category
		if cat == "" {
			cat = "output"
		}
		return codeBlock(cat, out)
	case *dap.StoppedEvent:
		return codeBlock("stopped", fmt.Sprintf(
			"reason: %s\nthread: %d\ndescription: %s",
			e.Body.Reason, e.Body.ThreadId, e.Body.Description))
	case *dap.ContinuedEvent:
		return codeBlock("continued", fmt.Sprintf(
			"thread: %d\nall threads: %v",
			e.Body.ThreadId, e.Body.AllThreadsContinued))
	case *dap.TerminatedEvent:
		return "**Debuggee terminated.**"
	case *dap.ExitedEvent:
		return codeBlock("exited", fmt.Sprintf(
			"exit code: %d", e.Body.ExitCode))
	case *dap.ThreadEvent:
		return codeBlock("thread", fmt.Sprintf(
			"reason: %s\nid: %d",
			e.Body.Reason, e.Body.ThreadId))
	case *dap.BreakpointEvent:
		return codeBlock("breakpoint", fmt.Sprintf(
			"reason: %s", e.Body.Reason))
	case *dap.ProcessEvent:
		return codeBlock("process", formatProcessEvent(e))
	}
	// Generic fallback: encode the full event as JSON.
	raw, err := json.Marshal(ev)
	if err != nil {
		return codeBlock("event", name)
	}
	return codeBlock(name, string(raw))
}

// codeBlock renders body inside a fenced markdown block
// labelled lang. Empty bodies still produce a single-line
// fenced block so the renderer's styling is consistent.
func codeBlock(lang, body string) string {
	return fmt.Sprintf("```%s\n%s\n```", lang, body)
}

// formatProcessEvent pretty-prints a ProcessEvent body. The
// event marks the launch/attach succeeded and identifies the
// debuggee process so the user can correlate it with their
// system. Empty fields are omitted.
func formatProcessEvent(e *dap.ProcessEvent) string {
	var b strings.Builder
	if e.Body.Name != "" {
		fmt.Fprintf(&b, "name: %s\n", e.Body.Name)
	}
	if e.Body.SystemProcessId != 0 {
		fmt.Fprintf(&b, "pid: %d\n", e.Body.SystemProcessId)
	}
	if e.Body.StartMethod != "" {
		fmt.Fprintf(&b, "method: %s\n", e.Body.StartMethod)
	}
	fmt.Fprintf(&b, "local: %v\n", e.Body.IsLocalProcess)
	if e.Body.PointerSize != 0 {
		fmt.Fprintf(&b, "pointer size: %d\n", e.Body.PointerSize)
	}
	return strings.TrimRight(b.String(), "\n")
}
