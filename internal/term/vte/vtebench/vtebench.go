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

// Package vtebench measures what a way of opening a terminal costs: raw
// throughput, the CPU of paced output, keystroke echo latency under
// load, startup, and memory per terminal. It drives real shells in
// real ptys, so it belongs to e2e tests, which print its reports.
package vtebench

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace"
)

// The size every terminal is opened at.
const (
	Width  = 120
	Height = 36
)

// Host stands in for the browser of one terminal. Every batch of
// output its emulator applies wakes whoever measures it.
type Host struct{ Wake chan struct{} }

// NewHost returns a Host with room for one pending wake.
func NewHost() *Host { return &Host{Wake: make(chan struct{}, 1)} }

// PublishEvent satisfies browser.EventPublisher.
func (h *Host) PublishEvent(term.Event) error {
	select {
	case h.Wake <- struct{}{}:
	default:
	}
	return nil
}

// Notify satisfies browser.Notifications.
func (*Host) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

// NotifyOnce satisfies browser.Notifications.
func (*Host) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

// UpdateNotificationProgress satisfies browser.Notifications.
func (*Host) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

// Tab satisfies browser.TabManager.
func (*Host) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}

// SetTabName satisfies browser.TabManager.
func (*Host) SetTabName(workspaceapi.URI, string, term.Attributes) error { return nil }

// OnTabExit satisfies browser.TabManager.
func (*Host) OnTabExit(workspaceapi.URI) bool { return false }

// SetTabStatus satisfies browser.TabManager.
func (*Host) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }

// EndTabStatus satisfies browser.TabManager.
func (*Host) EndTabStatus(workspaceapi.URI) error { return nil }

// Loop is the host event loop: what terminals schedule runs on one
// goroutine, in order.
type Loop struct {
	mu     sync.Mutex
	queue  []func()
	notify chan struct{}
}

// NewLoop starts a Loop that stops with the test.
func NewLoop(t *testing.T) *Loop {
	l := &Loop{notify: make(chan struct{}, 1)}
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-l.notify:
			case <-done:
				return
			}
			l.mu.Lock()
			fns := l.queue
			l.queue = nil
			l.mu.Unlock()
			for _, f := range fns {
				f()
			}
		}
	})
	t.Cleanup(func() { close(done) })
	return l
}

// Schedule satisfies vte.Config.ScheduleNextTick.
func (l *Loop) Schedule(f func()) bool {
	l.mu.Lock()
	l.queue = append(l.queue, f)
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
	return true
}

// Config is the emulator configuration every path opens terminals
// with: the bench size, cmd, and the loop as scheduler.
func Config(loop *Loop, cmd []string) vte.Config {
	cfg := vte.DefaultConfig()
	cfg.WidthHint = Width
	cfg.HeightHint = Height
	cfg.CommandAndArgs = cmd
	cfg.ScheduleNextTick = loop.Schedule
	// As terminal.dynamic_tab_name, on by default: the end marker is a
	// title.
	cfg.DynamicTabName = true
	return cfg
}

// LocalScheme is this machine's file scheme rooted at the temporary
// directory: the Terminal and Executor of a locally owned terminal.
func LocalScheme(t *testing.T) schemeapi.Scheme {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + os.TempDir())
	require.NoError(t, err)
	s, err := workspace.NewFileScheme(context.Background(), nil, uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// VTE is the emulator a user sees: the one whose grid the editor
// draws.
type VTE struct {
	Comp  *vte.Component
	Host  *Host
	Close func()
}

// Path is one way of opening a terminal.
type Path struct {
	Name string
	Open func(t *testing.T, cmd []string) *VTE
	// Remote reports the CPU spent so far by the processes other than
	// this one, and when gc is set the heap they hold after a
	// collection. It is nil when there are none.
	Remote func(t *testing.T, gc bool) (time.Duration, uint64)
}

func (p Path) cpu(t *testing.T) time.Duration {
	cpu := selfCPU()
	if p.Remote != nil {
		r, _ := p.Remote(t, false)
		cpu += r
	}
	return cpu
}

func (p Path) heap(t *testing.T) (local, remote uint64) {
	local = selfHeap()
	if p.Remote != nil {
		_, remote = p.Remote(t, true)
	}
	return local, remote
}

func selfCPU() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func selfHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// Screen renders v's active screen.
func Screen(t *testing.T, v *VTE) string {
	t.Helper()
	snap, err := v.Comp.Snapshot()
	require.NoError(t, err)
	var b strings.Builder
	for _, row := range snap.ActiveCells() {
		for _, c := range row {
			if c.Ch == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.Ch)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// WaitFor reports whether cond held before timeout, checking it after
// every batch the emulator applies.
func WaitFor(v *VTE, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-v.Host.Wake:
		case <-poll.C:
		}
	}
}

// WaitScreen reports whether want showed on v's screen before timeout.
func WaitScreen(t *testing.T, v *VTE, want string, timeout time.Duration) bool {
	t.Helper()
	return WaitFor(v, timeout, func() bool { return strings.Contains(Screen(t, v), want) })
}

// endMarker is the title a script sets once it printed everything, so
// checking for it does not cost a copy of the screen per batch.
const endMarker = "printf '\\033]2;ZQEND\\007'"

// waitEnd reports whether the end marker arrived. It gives up early
// when the emulator stops reading: a server dropped it.
func waitEnd(v *VTE, timeout time.Duration) bool {
	WaitFor(v, timeout, func() bool { return v.Comp.Title() == "ZQEND" || v.Comp.IsComplete() })
	return v.Comp.Title() == "ZQEND"
}

type payload struct {
	name string
	path string
	size int64
}

// bench holds the payloads and the scripts that print them.
type bench struct{ dir string }

// script returns the command that runs body: the emulator splits a
// command line into fields itself, so the body travels as a file.
func (b *bench) script(t *testing.T, body string) []string {
	t.Helper()
	f, err := os.CreateTemp(b.dir, "script*.sh")
	require.NoError(t, err)
	_, err = f.WriteString(body + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return []string{"/bin/sh", f.Name()}
}

func (b *bench) writePayloads(t *testing.T) []payload {
	t.Helper()
	gen := []struct {
		name  string
		write func(w *bufio.Writer)
	}{
		{"plain", func(w *bufio.Writer) {
			const n = 250_000
			for i := range n {
				fmt.Fprintf(w, "%06d 2026-11-17T10:00:00.%03dZ INFO compiling "+
					"unstable.build/rune/internal/pkg%03d/file%04d.go (%d/%d)\n",
					i, i%1000, i%997, i%9973, i, n)
			}
		}},
		{"color", func(w *bufio.Writer) {
			const n = 200_000
			for i := range n {
				fmt.Fprintf(w, "\x1b[2m%06d\x1b[0m \x1b[36m2026-11-17T10:00:00.%03dZ\x1b[0m "+
					"\x1b[1;32mINFO\x1b[0m compiling \x1b[34munstable.build/rune/internal/"+
					"pkg%03d/file%04d.go\x1b[0m (\x1b[33m%d\x1b[0m/%d)\n",
					i, i%1000, i%997, i%9973, i, n)
			}
		}},
		{"fullscreen", func(w *bufio.Writer) {
			const frames = 4000
			_, _ = w.WriteString("\x1b[?1049h\x1b[?25l")
			text := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789 ", 8)
			for f := range frames {
				for r := range Height {
					off := (f + r) % 37
					fmt.Fprintf(w, "\x1b[%d;1H\x1b[38;5;%dm\x1b[48;5;%dm%s\x1b[0m",
						r+1, (f+r)%256, (f*7+r)%256, text[off:off+Width])
				}
			}
			_, _ = w.WriteString("\x1b[?1049l\x1b[?25h")
		}},
	}
	var ret []payload
	for _, g := range gen {
		path := filepath.Join(b.dir, g.name)
		f, err := os.Create(path)
		require.NoError(t, err)
		w := bufio.NewWriterSize(f, 1<<20)
		g.write(w)
		require.NoError(t, w.Flush())
		require.NoError(t, f.Close())
		st, err := os.Stat(path)
		require.NoError(t, err)
		ret = append(ret, payload{name: g.name, path: path, size: st.Size()})
	}
	// The plain payload again, cut in MiB chunks a script paces.
	plain, err := os.ReadFile(ret[0].path)
	require.NoError(t, err)
	chunks := filepath.Join(b.dir, "chunks")
	require.NoError(t, os.Mkdir(chunks, 0o755))
	for i := 0; i*(1<<20) < len(plain); i++ {
		chunk := plain[i*(1<<20) : min(len(plain), (i+1)*(1<<20))]
		require.NoError(t, os.WriteFile(filepath.Join(chunks, fmt.Sprintf("%03d", i)),
			chunk, 0o644))
	}
	return ret
}

// pacedLoop prints the plain payload a MiB every interval.
func (b *bench) pacedLoop(interval string) string {
	return "for f in " + shellQuote(filepath.Join(b.dir, "chunks")) + "/*; do cat \"$f\"; " +
		"sleep " + interval + "; done"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func report(format string, args ...any) {
	fmt.Printf("VTEBENCH "+format+"\n", args...)
}

// Run measures every path and prints a VTEBENCH line per measurement.
// VTEBENCH_ONLY limits the measurements to those whose name it
// contains: throughput, paced, latency, startup, memory.
func Run(t *testing.T, paths []Path) {
	dir, err := os.MkdirTemp("", "vtebench")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b := &bench{dir: dir}
	payloads := b.writePayloads(t)
	only := os.Getenv("VTEBENCH_ONLY")
	for _, p := range paths {
		t.Run(p.Name, func(t *testing.T) {
			if only == "" || strings.Contains(only, "throughput") {
				for _, pl := range payloads {
					b.throughput(t, p, pl)
				}
			}
			if only == "" || strings.Contains(only, "paced") {
				b.paced(t, p, payloads[0])
			}
			if only == "" || strings.Contains(only, "latency") {
				for _, load := range []string{"none", "paced", "flood"} {
					b.latency(t, p, payloads[0], load)
				}
			}
			if only == "" || strings.Contains(only, "startup") {
				b.startup(t, p)
			}
			if only == "" || strings.Contains(only, "memory") {
				b.memory(t, p, payloads[0])
			}
		})
	}
}

func (b *bench) throughput(t *testing.T, p Path, pl payload) {
	const runs = 3
	var elapsed, cpus []time.Duration
	for range runs {
		v := p.Open(t, b.script(t, "stty -echo; printf 'RDY\\n'; read _; cat "+
			shellQuote(pl.path)+"; "+endMarker+"; read _"))
		require.True(t, WaitScreen(t, v, "RDY", 10*time.Second), "%s never started", p.Name)
		cpu0 := p.cpu(t)
		t0 := time.Now()
		require.NoError(t, v.Comp.WriteToPty([]byte("\n")))
		done := waitEnd(v, time.Minute)
		d := time.Since(t0)
		cpu := p.cpu(t) - cpu0
		complete := v.Comp.IsComplete()
		v.Close()
		if !done {
			report("throughput path=%s payload=%s DROPPED=%v after_s=%.3f", p.Name, pl.name,
				complete, d.Seconds())
			return
		}
		elapsed = append(elapsed, d)
		cpus = append(cpus, cpu)
	}
	slices.Sort(elapsed)
	slices.Sort(cpus)
	mb := float64(pl.size) / (1 << 20)
	med := elapsed[len(elapsed)/2]
	report("throughput path=%s payload=%s size_mb=%.1f median_s=%.3f mb_per_s=%.1f "+
		"cpu_ms_per_mb=%.2f runs=%v", p.Name, pl.name, mb, med.Seconds(), mb/med.Seconds(),
		float64(cpus[len(cpus)/2].Milliseconds())/mb, elapsed)
}

// paced measures what showing output at a steady rate costs, a rate
// every path keeps up with.
func (b *bench) paced(t *testing.T, p Path, pl payload) {
	const runs = 3
	var cpus []time.Duration
	for range runs {
		v := p.Open(t, b.script(t, "stty -echo; printf 'RDY\\n'; read _; "+b.pacedLoop("0.05")+
			"; "+endMarker+"; read _"))
		require.True(t, WaitScreen(t, v, "RDY", 10*time.Second), "%s never started", p.Name)
		cpu0 := p.cpu(t)
		require.NoError(t, v.Comp.WriteToPty([]byte("\n")))
		done := waitEnd(v, time.Minute)
		cpu := p.cpu(t) - cpu0
		complete := v.Comp.IsComplete()
		v.Close()
		if !done {
			report("paced path=%s DROPPED=%v", p.Name, complete)
			return
		}
		cpus = append(cpus, cpu)
	}
	slices.Sort(cpus)
	mb := float64(pl.size) / (1 << 20)
	report("paced path=%s size_mb=%.1f cpu_ms_per_mb=%.2f cpu_runs=%v", p.Name, mb,
		float64(cpus[len(cpus)/2].Milliseconds())/mb, cpus)
}

func (b *bench) latency(t *testing.T, p Path, flood payload, load string) {
	const samples = 500
	const gap = 8 * time.Millisecond
	var loader *VTE
	switch load {
	case "paced":
		loader = p.Open(t, b.script(t, "while :; do "+b.pacedLoop("0.1")+"; done"))
		time.Sleep(time.Second)
	case "flood":
		loader = p.Open(t, b.script(t, "while :; do cat "+shellQuote(flood.path)+"; done"))
		time.Sleep(time.Second)
	}
	v := p.Open(t, b.script(t, "stty raw -echo; printf RDY; exec cat"))
	require.True(t, WaitScreen(t, v, "RDY", 10*time.Second), "%s never started", p.Name)
	col := v.Comp.CursorAtScreen().X
	lat := make([]time.Duration, 0, samples)
	for len(lat) < samples {
		if col >= 100 {
			require.NoError(t, v.Comp.WriteToPty([]byte("\r")))
			require.True(t, WaitFor(v, 10*time.Second, func() bool {
				return v.Comp.CursorAtScreen().X == 0
			}))
			col = 0
		}
		time.Sleep(gap)
		select {
		case <-v.Host.Wake:
		default:
		}
		want := col + 1
		t0 := time.Now()
		require.NoError(t, v.Comp.WriteToPty([]byte("x")))
		ok := WaitFor(v, 10*time.Second, func() bool { return v.Comp.CursorAtScreen().X == want })
		d := time.Since(t0)
		require.True(t, ok, "%s: keystroke %d never echoed", p.Name, len(lat))
		lat = append(lat, d)
		col = want
	}
	dropped := false
	if loader != nil {
		dropped = loader.Comp.IsComplete()
		loader.Close()
	}
	v.Close()
	slices.Sort(lat)
	pct := func(q float64) float64 {
		return float64(lat[min(len(lat)-1, int(q*float64(len(lat))))].Microseconds()) / 1000
	}
	report("latency path=%s load=%s samples=%d p50_ms=%.3f p90_ms=%.3f p99_ms=%.3f "+
		"max_ms=%.3f loader_dropped=%v", p.Name, load, len(lat), pct(0.5), pct(0.9),
		pct(0.99), float64(lat[len(lat)-1].Microseconds())/1000, dropped)
}

func (b *bench) startup(t *testing.T, p Path) {
	const n = 20
	var ds []time.Duration
	cmd := b.script(t, "printf RDY; read _")
	for range n {
		t0 := time.Now()
		v := p.Open(t, cmd)
		require.True(t, WaitScreen(t, v, "RDY", 10*time.Second), "%s never started", p.Name)
		ds = append(ds, time.Since(t0))
		v.Close()
	}
	slices.Sort(ds)
	report("startup path=%s n=%d p50_ms=%.2f p90_ms=%.2f max_ms=%.2f", p.Name, n,
		float64(ds[n/2].Microseconds())/1000, float64(ds[n*9/10].Microseconds())/1000,
		float64(ds[n-1].Microseconds())/1000)
}

func (b *bench) memory(t *testing.T, p Path, flood payload) {
	const terminals = 8
	const lines = 10_000
	local0, remote0 := p.heap(t)
	var vs []*VTE
	for range terminals {
		v := p.Open(t, b.script(t, "head -n "+fmt.Sprint(lines)+" "+
			shellQuote(flood.path)+"; "+endMarker+"; read _"))
		vs = append(vs, v)
	}
	for _, v := range vs {
		require.True(t, waitEnd(v, time.Minute), "%s never printed", p.Name)
	}
	local1, remote1 := p.heap(t)
	for _, v := range vs {
		v.Close()
	}
	mib := func(a, b uint64) float64 { return (float64(b) - float64(a)) / (1 << 20) }
	report("memory path=%s terminals=%d lines=%d editor_heap_mib=%.1f server_heap_mib=%.1f "+
		"per_terminal_mib=%.2f", p.Name, terminals, lines, mib(local0, local1),
		mib(remote0, remote1), (mib(local0, local1)+mib(remote0, remote1))/terminals)
}
