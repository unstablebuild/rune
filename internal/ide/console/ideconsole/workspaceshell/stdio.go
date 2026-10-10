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

package workspaceshell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// If *os.File ever stops satisfying workspaceapi.File, pty slaves
// and socketpairs would fall into the tee branch of classify and a
// pipe would be interposed, breaking isatty() and fd passthrough.
var _ workspaceapi.File = (*os.File)(nil)

const (
	// Only tee'd bytes are counted; capping a stream handed to the
	// child as a raw fd would require interposing a pipe.
	stdioSinkCap = 4 << 20

	stdioTailBytes = 32 << 10

	stdioDirPrefix = "rune-stdio-"
)

var errStdioStoreClosed = errors.New("stdio store closed")

// stdioStore owns the per-Executor directory holding captured
// process output, created on first use so that executors whose
// processes all write to files never touch disk.
type stdioStore struct {
	// Empty means the OS temp dir. Overridden in tests.
	root string

	mu  sync.Mutex
	dir string
	err error
	seq atomic.Uint64
}

func newStdioStore() *stdioStore { return &stdioStore{} }

func (s *stdioStore) ensureDir() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		return s.dir, nil
	}
	if s.err != nil {
		return "", s.err
	}
	dir, err := os.MkdirTemp(s.root, stdioDirPrefix+"*")
	if err != nil {
		s.err = err
		return "", err
	}
	s.dir = dir
	return dir, nil
}

// remove deletes the store directory and blocks further capture.
func (s *stdioStore) remove() error {
	s.mu.Lock()
	dir := s.dir
	s.dir = ""
	s.err = errStdioStoreClosed
	s.mu.Unlock()
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

type stdioStream struct {
	name string
	// Empty when the stream was captured into the sink; otherwise
	// the name of the file the stream was passed through to.
	dest string
}

// stdioSink captures one process's stdout and stderr into a single
// append-only file, readable while it runs and after it exits.
type stdioSink struct {
	store *stdioStore

	mu        sync.Mutex
	file      *os.File
	path      string
	written   int64
	truncated bool
	broken    bool
	streams   []stdioStream
}

func newStdioSink(store *stdioStore) *stdioSink {
	return &stdioSink{store: store}
}

// classify returns the writer to hand the child in place of w.
// File-backed streams are left untouched: pty slaves, LSP
// socketpairs and extension logs reach the child as real file
// descriptors, and wrapping them would force a pipe in between.
func (s *stdioSink) classify(name string, w io.Writer) io.Writer {
	if w == nil {
		f, err := s.open()
		if err != nil {
			s.record(name, "unavailable: "+err.Error())
			return nil
		}
		s.record(name, "")
		return f
	}
	if f, ok := w.(workspaceapi.File); ok {
		s.record(name, f.Name())
		return w
	}
	if _, err := s.open(); err != nil {
		s.record(name, "unavailable: "+err.Error())
		return w
	}
	s.record(name, "")
	return &stdioTee{w: w, sink: s}
}

func (s *stdioSink) record(name, dest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams = append(s.streams, stdioStream{name: name, dest: dest})
}

func (s *stdioSink) open() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return s.file, nil
	}
	dir, err := s.store.ensureDir()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("pending-%d.log", s.store.seq.Add(1))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(
		path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600,
	)
	if err != nil {
		return nil, err
	}
	s.file = f
	s.path = path
	return f, nil
}

// identify names the sink file after the process. The sink is
// opened before Start returns a pid, hence the rename; the
// timestamp disambiguates pids reused within one session.
func (s *stdioSink) identify(pid workspaceapi.Pid, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return
	}
	next := filepath.Join(
		filepath.Dir(s.path),
		fmt.Sprintf("%d-%d.log", pid, started.UnixNano()),
	)
	if err := os.Rename(s.path, next); err != nil {
		return
	}
	s.path = next
}

// append never reports failure: a broken sink must not break the
// process's real output stream.
func (s *stdioSink) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.broken || s.truncated {
		return
	}
	remaining := int64(stdioSinkCap) - s.written
	if remaining <= 0 {
		s.truncated = true
		return
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		s.truncated = true
	}
	n, err := s.file.Write(p)
	s.written += int64(n)
	if err != nil {
		s.broken = true
	}
}

func (s *stdioSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return
	}
	_ = s.file.Close()
	s.file = nil
}

func (s *stdioSink) report() (path string, streams []stdioStream, truncated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, append([]stdioStream{}, s.streams...), s.truncated
}

// tail returns at most max trailing bytes of captured output,
// starting at a line boundary.
func (s *stdioSink) tail(max int64) ([]byte, error) {
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := int64(0)
	if fi.Size() > max {
		off = fi.Size() - max
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := io.ReadFull(io.NewSectionReader(f, off, int64(len(buf))), buf); err != nil {
		return nil, err
	}
	if off > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}

type stdioTee struct {
	w    io.Writer
	sink *stdioSink
}

func (t *stdioTee) Write(p []byte) (int, error) {
	t.sink.append(p)
	return t.w.Write(p)
}

func (e *Executor) handleStdio(
	args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: process stdio <pid>")
	}
	pidVal, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("invalid pid: %s", args[0])
	}
	pid := workspaceapi.Pid(pidVal)

	e.mu.RLock()
	info, ok := e.processes[pid]
	if !ok {
		info, ok = e.history[pid]
	}
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown pid: %d", pid)
	}
	if info.stdio == nil {
		return nil, fmt.Errorf("no captured output for pid %d", pid)
	}

	path, streams, truncated := info.stdio.report()
	lines := []string{fmt.Sprintf("process %d output:", pid)}
	for _, stream := range streams {
		dest := stream.dest
		if dest == "" {
			dest = path
		}
		lines = append(lines,
			fmt.Sprintf("  %s → %s", stream.name, dest))
	}
	if truncated {
		lines = append(lines,
			fmt.Sprintf("  (capture stopped after %d bytes)", stdioSinkCap))
	}
	lines = append(lines, "")

	tail, err := info.stdio.tail(stdioTailBytes)
	if err != nil {
		return nil, err
	}
	if len(tail) == 0 {
		return toLines(append(lines, "  (no output captured)")...), nil
	}
	return toLines(append(
		lines,
		strings.Split(strings.TrimSuffix(string(tail), "\n"), "\n")...,
	)...), nil
}
