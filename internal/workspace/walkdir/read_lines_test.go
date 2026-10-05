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

package walkdir

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// localFS implements Reader using OS calls for testing.
type localFS struct {
	root string
}

func (f localFS) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(f.root, path)
}

func (f localFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(path))
}

func (f localFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(f.resolve(path), flag, mode)
}

func (f localFS) Stat(path string) (os.FileInfo, error) {
	return os.Stat(f.resolve(path))
}

func (f localFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(f.resolve(name))
}

func TestReadLines_close_does_not_hang(t *testing.T) {
	dir := t.TempDir()

	// Create enough files with many lines so that ReadLines workers
	// are actively sending when we call Close.
	for i := range 20 {
		subdir := filepath.Join(dir, fmt.Sprintf("pkg%d", i))
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := range 5 {
			var content strings.Builder
			for k := range 50 {
				fmt.Fprintf(&content, "line %d content\n", k)
			}
			if err := os.WriteFile(
				filepath.Join(subdir, fmt.Sprintf("file%d.txt", j)),
				[]byte(content.String()), 0o644,
			); err != nil {
				t.Fatal(err)
			}
		}
	}

	fs := localFS{root: dir}
	ctx := context.Background()

	paths, err := ListFiles(ctx, fs, dir)
	if err != nil {
		t.Fatal(err)
	}

	lines, err := ReadLines(ctx, fs, paths)
	if err != nil {
		t.Fatal(err)
	}

	// Read a handful of lines, then stop early.
	for range 10 {
		_, ok := lines.Next(ctx)
		if !ok {
			t.Fatal("expected more lines")
		}
	}

	// Close must return promptly. Before the fix, this would deadlock
	// because readFile workers were stuck on unbuffered channel sends
	// with no context check.
	done := make(chan struct{})
	go func() {
		_ = lines.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() hung — likely deadlock in readFile workers")
	}
}

func TestReadLines_skipsOverlongLines(t *testing.T) {
	dir := t.TempDir()

	// Write a file with a short first line, a single line longer than the
	// scanner's max token size (which would normally surface as
	// bufio.ErrTooLong), and a short trailing line.
	overlong := bytes.Repeat([]byte("a"), bufio.MaxScanTokenSize+1)
	var content bytes.Buffer
	content.WriteString("first line\n")
	content.Write(overlong)
	content.WriteByte('\n')
	content.WriteString("trailing line\n")

	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, content.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := localFS{root: dir}
	ctx := context.Background()

	paths, err := ListFiles(ctx, fs, dir)
	if err != nil {
		t.Fatal(err)
	}

	lines, err := ReadLines(ctx, fs, paths)
	if err != nil {
		t.Fatal(err)
	}
	defer lines.Close() //nolint:errcheck

	var got []string
	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		got = append(got, line)
	}

	if err := lines.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after overlong line", err)
	}

	// The overlong line halts that scanner, so we only require the lines that
	// fit the buffer to be reported. Validate that we received the first short
	// line without surfacing a bufio.ErrTooLong error.
	if len(got) == 0 {
		t.Fatalf("expected at least one line, got none")
	}
	if !strings.Contains(got[0], "first line") {
		t.Fatalf("expected first line content, got %q", got[0])
	}
}

func TestWorkerCountFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{
			name: "no context value",
			ctx:  context.Background(),
			want: defaultWorkers,
		},
		{
			name: "positive context value",
			ctx:  ContextWithWorkerCount(context.Background(), 2),
			want: 2,
		},
		{
			name: "zero falls back to default",
			ctx:  ContextWithWorkerCount(context.Background(), 0),
			want: defaultWorkers,
		},
		{
			name: "negative falls back to default",
			ctx:  ContextWithWorkerCount(context.Background(), -1),
			want: defaultWorkers,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workerCountFromContext(tt.ctx); got != tt.want {
				t.Fatalf("workerCountFromContext() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadLines_contextWorkerCountLimitsConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			paths := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
			fs := &concurrencyReader{
				files: map[string]string{
					"a.txt": "a\n",
					"b.txt": "b\n",
					"c.txt": "c\n",
					"d.txt": "d\n",
				},
				started: make(chan struct{}, len(paths)),
				release: make(chan struct{}),
			}

			ctx := ContextWithWorkerCount(context.Background(), workers)
			lines, err := ReadLines(ctx, fs, newStringIterator(paths))
			if err != nil {
				t.Fatal(err)
			}

			for range workers {
				select {
				case <-fs.started:
				case <-time.After(time.Second):
					t.Fatalf("timed out waiting for %d workers to start", workers)
				}
			}

			select {
			case <-fs.started:
				t.Fatalf("OpenFile concurrency exceeded %d", workers)
			case <-time.After(20 * time.Millisecond):
			}

			close(fs.release)
			for {
				_, ok := lines.Next(context.Background())
				if !ok {
					break
				}
			}

			if err := lines.Err(); err != nil {
				t.Fatal(err)
			}
			if err := lines.Close(); err != nil {
				t.Fatal(err)
			}
			if got := fs.maxConcurrent.Load(); got > int64(workers) {
				t.Fatalf("max concurrent OpenFile calls = %d, want <= %d", got, workers)
			}
		})
	}
}

func TestReadLinesClosesOpenedFiles(t *testing.T) {
	fs := &closeCountingReader{
		files: map[string]string{
			"a.txt": "a\n",
			"b.txt": "b\n",
		},
	}

	ctx := ContextWithWorkerCount(context.Background(), 1)
	lines, err := ReadLines(ctx, fs, newStringIterator([]string{"a.txt", "b.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, ok := lines.Next(context.Background())
		if !ok {
			break
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if err := lines.Close(); err != nil {
		t.Fatal(err)
	}

	if got := fs.opened.Load(); got != 2 {
		t.Fatalf("opened files = %d, want 2", got)
	}
	if got := fs.closed.Load(); got != fs.opened.Load() {
		t.Fatalf("closed files = %d, want %d", got, fs.opened.Load())
	}
}

func TestReadLinesGrowsScanBufferOnDemand(t *testing.T) {
	longLine := strings.Repeat("x", 3*bufio.MaxScanTokenSize)
	fs := &readSizeRecordingReader{
		files: map[string]string{
			"small.txt": "one\ntwo\n",
			"large.txt": longLine + "\n",
		},
	}

	ctx := ContextWithWorkerCount(context.Background(), 1)
	ctx = ContextWithScanBufferSize(ctx, 64<<20)
	lines, err := ReadLines(ctx, fs, newStringIterator(
		[]string{"small.txt", "large.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		got = append(got, line)
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if err := lines.Close(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("lines = %d, want 3 (long line must fit the raised cap)", len(got))
	}
	found := false
	for _, line := range got {
		if strings.Contains(line, longLine) {
			found = true
		}
	}
	if !found {
		t.Fatal("long line was not returned; buffer growth is broken")
	}

	small := fs.maxReadSize["small.txt"]
	if small > bufio.MaxScanTokenSize {
		t.Fatalf("small file read size = %d, want <= %d: reads must not "+
			"be sized to the scan cap", small, bufio.MaxScanTokenSize)
	}
	large := fs.maxReadSize["large.txt"]
	if large > 8*bufio.MaxScanTokenSize {
		t.Fatalf("large file read size = %d, want proportional to its "+
			"line (~%d), not the %d cap", large, len(longLine), 64<<20)
	}
}

type readSizeRecordingReader struct {
	files       map[string]string
	maxReadSize map[string]int
}

func (r *readSizeRecordingReader) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file:///" + path)
}

func (r *readSizeRecordingReader) OpenFile(
	path string, _ int, _ os.FileMode,
) (workspaceapi.File, error) {
	content, ok := r.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	if r.maxReadSize == nil {
		r.maxReadSize = make(map[string]int)
	}
	return &readSizeRecordingFile{
		nopFile: nopFile{Reader: bytes.NewBufferString(content)},
		record: func(n int) {
			if n > r.maxReadSize[path] {
				r.maxReadSize[path] = n
			}
		},
	}, nil
}

func (r *readSizeRecordingReader) Stat(string) (os.FileInfo, error) {
	return nil, os.ErrNotExist
}

func (r *readSizeRecordingReader) ReadDir(string) ([]os.DirEntry, error) {
	return nil, os.ErrNotExist
}

type readSizeRecordingFile struct {
	nopFile
	record func(int)
}

func (f *readSizeRecordingFile) Read(p []byte) (int, error) {
	f.record(len(p))
	return f.nopFile.Reader.Read(p)
}

type stringIterator struct {
	values []string
	idx    int
}

func newStringIterator(values []string) iterator.Iterator[string] {
	return &stringIterator{values: values}
}

func (s *stringIterator) Next(context.Context) (string, bool) {
	if s.idx >= len(s.values) {
		return "", false
	}
	value := s.values[s.idx]
	s.idx++
	return value, true
}

func (s *stringIterator) Close() error { return nil }

func (s *stringIterator) Err() error { return nil }

type concurrencyReader struct {
	files map[string]string

	active        atomic.Int64
	maxConcurrent atomic.Int64
	started       chan struct{}
	release       chan struct{}
}

func (r *concurrencyReader) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file:///" + path)
}

func (r *concurrencyReader) OpenFile(path string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	content, ok := r.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}

	active := r.active.Add(1)
	for {
		maxConcurrent := r.maxConcurrent.Load()
		if active <= maxConcurrent || r.maxConcurrent.CompareAndSwap(maxConcurrent, active) {
			break
		}
	}
	r.started <- struct{}{}
	<-r.release
	r.active.Add(-1)

	return nopFile{Reader: bytes.NewBufferString(content)}, nil
}

func (r *concurrencyReader) Stat(string) (os.FileInfo, error) {
	return nil, os.ErrNotExist
}

func (r *concurrencyReader) ReadDir(string) ([]os.DirEntry, error) {
	return nil, os.ErrNotExist
}

type nopFile struct {
	io.Reader
	io.Seeker
	io.ReaderAt
	io.Writer
	io.Closer
}

func (n nopFile) Close() error { return nil }

func (n nopFile) Fd() uintptr { return 0 }

func (n nopFile) Name() string { return "" }

func (n nopFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

func (n nopFile) Seek(int64, int) (int64, error) { return 0, nil }

func (n nopFile) Stat() (os.FileInfo, error) { return nil, os.ErrInvalid }

func (n nopFile) Sync() error { return nil }

func (n nopFile) Truncate(int64) error { return nil }

func (n nopFile) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type closeCountingReader struct {
	files  map[string]string
	opened atomic.Int64
	closed atomic.Int64
}

func (r *closeCountingReader) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file:///" + path)
}

func (r *closeCountingReader) OpenFile(path string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	content, ok := r.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	r.opened.Add(1)
	return closeCountingFile{
		Reader: bytes.NewBufferString(content),
		close:  func() { r.closed.Add(1) },
	}, nil
}

func (r *closeCountingReader) Stat(string) (os.FileInfo, error) {
	return nil, os.ErrNotExist
}

func (r *closeCountingReader) ReadDir(string) ([]os.DirEntry, error) {
	return nil, os.ErrNotExist
}

type closeCountingFile struct {
	io.Reader
	close func()
}

func (f closeCountingFile) Close() error {
	f.close()
	return nil
}

func (f closeCountingFile) Fd() uintptr { return 0 }

func (f closeCountingFile) Name() string { return "" }

func (f closeCountingFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

func (f closeCountingFile) Seek(int64, int) (int64, error) { return 0, nil }

func (f closeCountingFile) Stat() (os.FileInfo, error) { return nil, os.ErrInvalid }

func (f closeCountingFile) Sync() error { return nil }

func (f closeCountingFile) Truncate(int64) error { return nil }

func (f closeCountingFile) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
