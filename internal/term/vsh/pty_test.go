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

package vsh

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

type readResult struct {
	data string
	err  error
}

// readMasterAsync reads the master in the background and reports the
// outcome of the first read.
func readMasterAsync(p *pty) <-chan readResult {
	ch := make(chan readResult, 1)
	go debug.CapturePanicReport(func() {
		buf := make([]byte, 4096)
		n, err := p.readMaster(buf)
		ch <- readResult{data: string(buf[:n]), err: err}
	})
	return ch
}

func awaitRead(t *testing.T, ch <-chan readResult) readResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("read did not return")
		return readResult{}
	}
}

// assertBlocked checks that nothing arrives on ch for a while. A
// blocked operation has no observable signal, so this waits for a
// short time.
func assertBlocked[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("expected the operation to block, got %v", v)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPtyOutputTranslation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		writes []string
		out    string
		atCol0 bool
	}{
		{name: "nothing written", out: "", atCol0: true},
		{name: "text", writes: []string{"abc"}, out: "abc"},
		{name: "LF becomes CRLF", writes: []string{"a\nb\n"}, out: "a\r\nb\r\n", atCol0: true},
		{name: "CRLF is kept", writes: []string{"a\r\nb"}, out: "a\r\nb"},
		{name: "CRLF split across writes is kept", writes: []string{"a\r", "\nb"}, out: "a\r\nb"},
		{name: "blank lines", writes: []string{"\n\n"}, out: "\r\n\r\n", atCol0: true},
		{name: "CR CR LF adds no CR", writes: []string{"\r\r\n"}, out: "\r\r\n", atCol0: true},
		{name: "LF after an escape that follows CR gets its CR", writes: []string{"\r\x1b[K\n"}, out: "\r\x1b[K\r\n", atCol0: true},
		{name: "text then carriage return", writes: []string{"abc\r"}, out: "abc\r", atCol0: true},
		{name: "a tab leaves the first column", writes: []string{"\t"}, out: "\t"},
		{name: "a bell does not move", writes: []string{"\x07"}, out: "\x07", atCol0: true},
		{name: "SGR after newline", writes: []string{"abc\n\x1b[31m"}, out: "abc\r\n\x1b[31m", atCol0: true},
		{name: "SGR after text", writes: []string{"abc\x1b[0m"}, out: "abc\x1b[0m"},
		{name: "clear homes the cursor", writes: []string{"abc\x1b[H\x1b[2J"}, out: "abc\x1b[H\x1b[2J", atCol0: true},
		{name: "f homes the cursor", writes: []string{"abc\x1b[f"}, out: "abc\x1b[f", atCol0: true},
		{name: "a position in the first column", writes: []string{"abc\x1b[5;1H"}, out: "abc\x1b[5;1H", atCol0: true},
		{name: "a position past the first column", writes: []string{"\x1b[5;7H"}, out: "\x1b[5;7H"},
		{name: "a position with only a row", writes: []string{"abc\x1b[5H"}, out: "abc\x1b[5H", atCol0: true},
		{name: "column one", writes: []string{"abc\x1b[G"}, out: "abc\x1b[G", atCol0: true},
		{name: "an explicit column one", writes: []string{"abc\x1b[1G"}, out: "abc\x1b[1G", atCol0: true},
		{name: "a later column", writes: []string{"\x1b[5G"}, out: "\x1b[5G"},
		{name: "cursor forward", writes: []string{"\x1b[3C"}, out: "\x1b[3C"},
		{name: "cursor up keeps the column", writes: []string{"\x1b[A"}, out: "\x1b[A", atCol0: true},
		{name: "CSI split across writes", writes: []string{"\x1b[", "31", "m\n"}, out: "\x1b[31m\r\n", atCol0: true},
		{name: "a position split across writes", writes: []string{"x\x1b[2;", "1H"}, out: "x\x1b[2;1H", atCol0: true},
		{name: "osc after text", writes: []string{"abc\x1b]133;A\x1b\\"}, out: "abc\x1b]133;A\x1b\\"},
		{name: "osc then newline", writes: []string{"\x1b]133;A\x1b\\\n"}, out: "\x1b]133;A\x1b\\\r\n", atCol0: true},
		{name: "osc ended by BEL", writes: []string{"\x1b]0;title\x07x"}, out: "\x1b]0;title\x07x"},
		{name: "newlines inside an osc are not translated", writes: []string{"\x1b]0;a\nb\x07"}, out: "\x1b]0;a\nb\x07", atCol0: true},
		{name: "osc split across writes", writes: []string{"\x1b]133;", "A\x1b", "\\x"}, out: "\x1b]133;A\x1b\\x"},
		{name: "a two-byte escape", writes: []string{"\x1b7\n"}, out: "\x1b7\r\n", atCol0: true},
		{name: "utf-8 text", writes: []string{"日本\n"}, out: "日本\r\n", atCol0: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			for _, w := range tc.writes {
				n, err := p.writeOutput(context.Background(), []byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n, "the count is of bytes taken, not of bytes queued")
			}
			assert.Equal(t, tc.out, drainMaster(p))
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.Equal(t, tc.atCol0, p.atCol0, "atCol0")
		})
	}
}

func TestPtyEcho(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		echo   string
		atCol0 bool
	}{
		{name: "echoed text leaves the first column", echo: "ab"},
		{name: "an echoed line end returns to it", output: "x", echo: "ab\r\n", atCol0: true},
		{name: "the interrupt echo", echo: "^C"},
		{name: "a bell does not move", echo: "\x07", atCol0: true},
		{name: "an erase does not move", output: "x", echo: "\b \b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			_, err := p.writeOutput(context.Background(), []byte(tc.output))
			require.NoError(t, err)
			p.mu.Lock()
			p.echoLocked([]byte(tc.echo))
			atCol0 := p.atCol0
			p.mu.Unlock()
			assert.Equal(t, tc.output+tc.echo, drainMaster(p))
			assert.Equal(t, tc.atCol0, atCol0)
		})
	}
}

func TestPtyEchoAfterHangupIsDropped(t *testing.T) {
	t.Parallel()
	p := newPty(1, 2)
	p.closeMaster()
	p.mu.Lock()
	p.echoLocked([]byte("x"))
	p.mu.Unlock()
	assert.Equal(t, "", drainMaster(p))
}

func TestPtyMasterReadsInPieces(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	_, err := (&ptySlave{p: p}).Write([]byte("hello"))
	require.NoError(t, err)
	var got []string
	buf := make([]byte, 2)
	for range 3 {
		n, err := p.readMaster(buf)
		require.NoError(t, err)
		got = append(got, string(buf[:n]))
	}
	assert.Equal(t, []string{"he", "ll", "o"}, got)
}

func TestPtyMasterEOFOnlyAfterLastSlaveReferenceAndDrain(t *testing.T) {
	t.Parallel()
	for _, procs := range []int{0, 1, 3} {
		t.Run(strings.Repeat("p", procs), func(t *testing.T) {
			t.Parallel()
			p := newPty(10, 11)
			parent := &ptySlave{p: p}
			for range procs {
				p.addSlaveRef()
			}
			_, err := parent.Write([]byte("out"))
			require.NoError(t, err)
			require.NoError(t, parent.Close())
			require.NoError(t, parent.Close(), "closing the parent handle twice releases one reference")

			r := awaitRead(t, readMasterAsync(p))
			require.NoError(t, r.err)
			assert.Equal(t, "out", r.data, "output is drained before EOF")

			ch := readMasterAsync(p)
			for range procs {
				assertBlocked(t, ch)
				p.releaseSlaveRef()
			}
			r = awaitRead(t, ch)
			assert.ErrorIs(t, r.err, io.EOF)
		})
	}
}

func TestPtyHangupHooks(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	var calls []string
	for _, name := range []string{"first", "second", "third"} {
		p.addHangupHook(func() {
			// Taking the lock proves the hook runs with it released.
			assert.ErrorIs(t, p.setSize(workspaceapi.PtySize{Columns: 1, Rows: 1}), ErrPtyClosed)
			calls = append(calls, name)
		})
	}
	assert.Empty(t, calls, "no hook runs before the hangup")
	master := &ptyMaster{p: p}
	require.NoError(t, master.Close())
	require.NoError(t, master.Close(), "closing the master twice is harmless")
	assert.Equal(t, []string{"first", "second", "third"}, calls, "hooks run once, in order")

	p.addHangupHook(func() { calls = append(calls, "late") })
	assert.Equal(t, []string{"first", "second", "third", "late"}, calls, "a hook added after the hangup runs at once")
}

func TestPtyAfterHangup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   func(m *ptyMaster, s *ptySlave) error
		want error
	}{
		{name: "master read with output queued", op: func(m *ptyMaster, _ *ptySlave) error {
			_, err := m.Read(make([]byte, 8))
			return err
		}, want: os.ErrClosed},
		{name: "master write", op: func(m *ptyMaster, _ *ptySlave) error {
			_, err := m.Write([]byte("key"))
			return err
		}, want: os.ErrClosed},
		{name: "slave write", op: func(_ *ptyMaster, s *ptySlave) error {
			_, err := s.Write([]byte("late"))
			return err
		}, want: syscall.EIO},
		{name: "slave read with a line queued", op: func(_ *ptyMaster, s *ptySlave) error {
			_, err := s.Read(make([]byte, 8))
			return err
		}},
		{name: "slave read once drained", op: func(_ *ptyMaster, s *ptySlave) error {
			_, _ = s.Read(make([]byte, 8))
			_, err := s.Read(make([]byte, 8))
			return err
		}, want: io.EOF},
		{name: "resize", op: func(m *ptyMaster, _ *ptySlave) error {
			return m.p.setSize(workspaceapi.PtySize{Columns: 1, Rows: 1})
		}, want: ErrPtyClosed},
		{name: "master close", op: func(m *ptyMaster, _ *ptySlave) error { return m.Close() }},
		{name: "slave close", op: func(_ *ptyMaster, s *ptySlave) error { return s.Close() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(10, 11)
			m, s := &ptyMaster{p: p}, &ptySlave{p: p}
			_, err := m.Write([]byte("line\r"))
			require.NoError(t, err)
			_, err = s.Write([]byte("output"))
			require.NoError(t, err)
			require.NoError(t, m.Close())
			err = tc.op(m, s)
			if tc.want == nil {
				assert.NoError(t, err)
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestPtyHangupWakesBlockedOperations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		block func(p *pty) (int, error)
		wantN int
		want  error
	}{
		{name: "a master read", block: func(p *pty) (int, error) { return p.readMaster(make([]byte, 8)) }, want: os.ErrClosed},
		{name: "a slave read", block: func(p *pty) (int, error) { return (&ptySlave{p: p}).Read(make([]byte, 8)) }, want: io.EOF},
		{name: "a slave write past the output bound keeps what it queued",
			block: func(p *pty) (int, error) { return (&ptySlave{p: p}).Write(make([]byte, ptyOutputBound+10)) },
			wantN: ptyOutputBound, want: syscall.EIO},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(10, 11)
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go debug.CapturePanicReport(func() {
				n, err := tc.block(p)
				done <- result{n, err}
			})
			assertBlocked(t, done)
			p.closeMaster()
			select {
			case r := <-done:
				assert.Equal(t, tc.wantN, r.n)
				assert.ErrorIs(t, r.err, tc.want)
			case <-time.After(5 * time.Second):
				t.Fatal("the hangup did not wake the operation")
			}
		})
	}
}

func TestPtyOutputBound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "a write of several bounds", payload: strings.Repeat("x", 3*ptyOutputBound+1), want: strings.Repeat("x", 3*ptyOutputBound+1)},
		{name: "translation may queue more than the bound", payload: strings.Repeat("\n", 2*ptyOutputBound), want: strings.Repeat("\r\n", 2*ptyOutputBound)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(10, 11)
			slave := &ptySlave{p: p}
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go debug.CapturePanicReport(func() {
				n, err := slave.Write([]byte(tc.payload))
				done <- result{n, err}
				_ = slave.Close()
			})
			got, err := io.ReadAll(&ptyMaster{p: p})
			require.NoError(t, err)
			r := <-done
			require.NoError(t, r.err)
			assert.Equal(t, len(tc.payload), r.n)
			assert.Equal(t, len(tc.want), len(got))
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestPtyOutputBoundBlocksProgramButNotMaster(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	slave := &ptySlave{p: p}
	master := &ptyMaster{p: p}

	_, err := slave.Write(make([]byte, ptyOutputBound))
	require.NoError(t, err)

	blocked := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		_, err := slave.Write([]byte("x"))
		blocked <- err
	})
	assertBlocked(t, blocked)

	// Keys and echo never wait on the output bound.
	n, err := master.Write([]byte("key"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	buf := make([]byte, ptyOutputBound)
	_, err = master.Read(buf)
	require.NoError(t, err)
	select {
	case err := <-blocked:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the writer did not resume once output was drained")
	}
}

func TestPtyBlockedWriteAbortsOnContext(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	_, err := p.writeOutput(context.Background(), make([]byte, ptyOutputBound))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		_, err := p.writeOutput(ctx, []byte("x"))
		done <- err
	})
	assertBlocked(t, done)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not abort the blocked write")
	}

	n, err := p.writeOutput(ctx, []byte("x"))
	assert.ErrorIs(t, err, context.Canceled, "an ended context fails a write that would block")
	assert.Zero(t, n)
}

func TestPtySize(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	cols, rows := p.windowSize()
	assert.Equal(t, []int{80, 24}, []int{cols, rows}, "the default size")

	for _, size := range []workspaceapi.PtySize{
		{Columns: 20, Rows: 10},
		{Columns: 1, Rows: 1},
		{Columns: 500, Rows: 200},
		// A zero size is what a kernel tty starts with; it is stored
		// as given for programs to fall back from.
		{Columns: 0, Rows: 0},
	} {
		require.NoError(t, p.setSize(size))
		p.mu.Lock()
		assert.True(t, p.winch, "a resize is pending")
		p.mu.Unlock()
		cols, rows = p.windowSize()
		assert.Equal(t, []int{size.Columns, size.Rows}, []int{cols, rows})
		p.mu.Lock()
		assert.False(t, p.winch, "reading the size consumes the pending resize")
		p.mu.Unlock()
	}
}

func TestPtyHandles(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	other := newPty(12, 13)
	assert.NotEqual(t, (&ptySlave{p: p}).Name(), (&ptySlave{p: other}).Name(), "each pty has its own name")

	tests := []struct {
		name string
		file workspaceapi.File
		path string
		fd   uintptr
	}{
		{name: "master", file: &ptyMaster{p: p}, path: `^/dev/vptmx$`, fd: 10},
		{name: "slave", file: &ptySlave{p: p}, path: `^/dev/vpts/\d+$`, fd: 11},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Regexp(t, tc.path, tc.file.Name())
			assert.Equal(t, tc.fd, tc.file.Fd())
			assert.True(t, p.hasFd(tc.fd))

			info, err := tc.file.Stat()
			require.NoError(t, err)
			assert.Equal(t, tc.file.Name(), info.Name())
			assert.Equal(t, os.ModeDevice|os.ModeCharDevice|0o620, info.Mode())
			assert.Zero(t, info.Size())
			assert.False(t, info.IsDir())
			assert.True(t, info.ModTime().IsZero())
			assert.Nil(t, info.Sys())

			assert.NoError(t, tc.file.Sync())
			_, err = tc.file.Seek(0, io.SeekStart)
			assert.ErrorIs(t, err, syscall.ESPIPE)
			_, err = tc.file.ReadAt(make([]byte, 1), 0)
			assert.ErrorIs(t, err, syscall.ESPIPE)
			assert.ErrorIs(t, tc.file.Truncate(0), syscall.EINVAL)
		})
	}
	assert.False(t, p.hasFd(12))
}

func TestPtyHandlesCarryDataBothWays(t *testing.T) {
	t.Parallel()
	p := newPty(10, 11)
	master, slave := &ptyMaster{p: p}, &ptySlave{p: p}

	_, err := master.Write([]byte("typed\r"))
	require.NoError(t, err)
	buf := make([]byte, 16)
	n, err := slave.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "typed\n", string(buf[:n]), "the slave reads the cooked line")

	_, err = slave.Write([]byte("out\n"))
	require.NoError(t, err)
	n, err = master.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "typed\r\nout\r\n", string(buf[:n]), "the master reads the echo, then the output")
}
