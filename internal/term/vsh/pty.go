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
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// ErrPtyClosed reports an operation on a pty whose master was closed.
var ErrPtyClosed = errors.New("vsh: pty closed")

// ptyOutputBound is how much unread program output a pty holds before
// writers block, the way a kernel tty queue throttles a program whose
// terminal is not reading.
const ptyOutputBound = 64 << 10

var ptyCounter atomic.Int64

// pty is an in-process pseudoterminal. The master side is what the
// terminal emulator reads output from and writes keys to; the slave
// side is what programs read cooked input from and write output to.
// One mutex and condition guard every queue so that waiters on
// input, output, size and hangup all wake on a single broadcast.
type pty struct {
	mu   sync.Mutex
	cond *sync.Cond

	name              string
	masterFd, slaveFd uintptr

	// out is program output waiting for master reads.
	out []byte
	// atCol0 tracks whether the last output left the cursor at the
	// first column; lastCR, esc and csiParams are the output
	// translation state.
	atCol0 bool
	lastCR bool
	esc    outputEscape
	// csiParams are the parameter bytes of the CSI being written.
	csiParams []byte

	// slaveRefs counts the parent's handle and one per attached
	// process; the master reads EOF once it drops to zero.
	slaveRefs    int
	masterClosed bool
	hungUp       bool

	size  workspaceapi.PtySize
	winch bool

	ld lineDiscipline

	onHangup []func()
}

type outputEscape int

const (
	escNone outputEscape = iota
	escStart
	escCSI
	escOSC
	escOSCEsc
)

func newPty(masterFd, slaveFd uintptr) *pty {
	p := &pty{
		name:      fmt.Sprintf("/dev/vpts/%d", ptyCounter.Add(1)),
		masterFd:  masterFd,
		slaveFd:   slaveFd,
		atCol0:    true,
		slaveRefs: 1,
		size:      workspaceapi.PtySize{Columns: 80, Rows: 24},
	}
	p.cond = sync.NewCond(&p.mu)
	p.ld.p = p
	return p
}

// wakeOnDone broadcasts the condition when ctx ends so that a waiter
// polling ctx.Err under the lock notices. The returned stop releases
// the registration.
func (p *pty) wakeOnDone(ctx context.Context) func() bool {
	if ctx == nil || ctx.Done() == nil {
		return func() bool { return true }
	}
	return context.AfterFunc(ctx, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
}

// writeOutput queues program output for the master, translating "\n"
// to "\r\n" as the ONLCR output flag does. It blocks while the
// unread output exceeds the bound and fails with EIO once the master
// hung up. ctx aborts a blocked write.
func (p *pty) writeOutput(ctx context.Context, b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var stop func() bool
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	written := 0
	for len(b) > 0 {
		for len(p.out) >= ptyOutputBound && !p.hungUp && (ctx == nil || ctx.Err() == nil) {
			if stop == nil {
				stop = p.wakeOnDone(ctx)
			}
			p.cond.Wait()
		}
		if p.hungUp {
			return written, syscall.EIO
		}
		if ctx != nil && ctx.Err() != nil {
			return written, ctx.Err()
		}
		n := min(len(b), ptyOutputBound)
		p.translateLocked(b[:n])
		b = b[n:]
		written += n
		p.cond.Broadcast()
	}
	return written, nil
}

// translateLocked appends b to the output queue applying ONLCR and
// tracking the column. An escape sequence does not move the column
// except for the cursor-positioning CSIs that home it.
func (p *pty) translateLocked(b []byte) {
	for _, c := range b {
		switch p.esc {
		case escStart:
			switch c {
			case '[':
				p.esc = escCSI
				p.csiParams = p.csiParams[:0]
			case ']':
				p.esc = escOSC
			default:
				p.esc = escNone
			}
			p.out = append(p.out, c)
			continue
		case escCSI:
			p.out = append(p.out, c)
			if c >= 0x40 && c <= 0x7e {
				p.esc = escNone
				p.trackCSILocked(c)
			} else {
				p.csiParams = append(p.csiParams, c)
			}
			continue
		case escOSC:
			p.out = append(p.out, c)
			switch c {
			case 0x07:
				p.esc = escNone
			case 0x1b:
				p.esc = escOSCEsc
			}
			continue
		case escOSCEsc:
			p.out = append(p.out, c)
			p.esc = escNone
			continue
		}
		switch c {
		case '\n':
			if !p.lastCR {
				p.out = append(p.out, '\r')
			}
			p.out = append(p.out, '\n')
			p.atCol0 = true
		case '\r':
			p.out = append(p.out, '\r')
			p.atCol0 = true
		case 0x1b:
			p.out = append(p.out, c)
			p.esc = escStart
		case '\b':
			p.out = append(p.out, c)
		case '\t':
			p.out = append(p.out, c)
			p.atCol0 = false
		default:
			p.out = append(p.out, c)
			if c >= 0x20 {
				p.atCol0 = false
			}
		}
		p.lastCR = c == '\r'
	}
}

// trackCSILocked follows the cursor-positioning CSI ending in final:
// one naming a column says whether it is the first, and a move
// forward leaves it. Any other leaves the column as it was.
func (p *pty) trackCSILocked(final byte) {
	switch final {
	case 'H', 'f':
		p.atCol0 = csiParam(p.csiParams, 1) == 1
	case 'G':
		p.atCol0 = csiParam(p.csiParams, 0) == 1
	case 'C':
		p.atCol0 = false
	}
}

// csiParam is the i-th ";"-separated parameter of a CSI, 1 when it is
// omitted, zero or not a number, as cursor positions default.
func csiParam(params []byte, i int) int {
	fields := strings.Split(string(params), ";")
	if i >= len(fields) {
		return 1
	}
	n, err := strconv.Atoi(fields[i])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// echoLocked queues terminal-generated bytes (echo, BEL) for the
// master. They bypass the output bound so that the master's own
// writes never block.
func (p *pty) echoLocked(b []byte) {
	if p.hungUp {
		return
	}
	p.out = append(p.out, b...)
	for _, c := range b {
		if c == '\n' || c == '\r' {
			p.atCol0 = true
		} else if c >= 0x20 {
			p.atCol0 = false
		}
	}
	p.cond.Broadcast()
}

// readMaster delivers program output. It returns io.EOF once the last
// slave reference is released and the output is drained, and
// os.ErrClosed after the master was closed.
func (p *pty) readMaster(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if p.masterClosed {
			return 0, os.ErrClosed
		}
		if len(p.out) > 0 {
			n := copy(b, p.out)
			p.out = p.out[n:]
			if len(p.out) == 0 {
				p.out = nil
			}
			p.cond.Broadcast()
			return n, nil
		}
		if p.slaveRefs == 0 {
			return 0, io.EOF
		}
		p.cond.Wait()
	}
}

// writeMaster feeds keys typed at the terminal to the line
// discipline. It never blocks.
func (p *pty) writeMaster(b []byte) (int, error) {
	p.mu.Lock()
	if p.masterClosed {
		p.mu.Unlock()
		return 0, os.ErrClosed
	}
	interrupt := p.ld.receiveLocked(b)
	onInterrupt := p.ld.onInterrupt
	p.mu.Unlock()
	if interrupt && onInterrupt != nil {
		onInterrupt()
	}
	return len(b), nil
}

// closeMaster hangs the pty up: readers and writers on either side
// wake and fail, and the hangup hooks run with no lock held.
func (p *pty) closeMaster() {
	p.mu.Lock()
	if p.masterClosed {
		p.mu.Unlock()
		return
	}
	p.masterClosed = true
	p.hungUp = true
	hooks := p.onHangup
	p.onHangup = nil
	p.cond.Broadcast()
	p.mu.Unlock()
	for _, hook := range hooks {
		hook()
	}
}

// addHangupHook registers fn to run once when the master closes, or
// immediately when it already has.
func (p *pty) addHangupHook(fn func()) {
	p.mu.Lock()
	if p.hungUp {
		p.mu.Unlock()
		fn()
		return
	}
	p.onHangup = append(p.onHangup, fn)
	p.mu.Unlock()
}

func (p *pty) addSlaveRef() {
	p.mu.Lock()
	p.slaveRefs++
	p.mu.Unlock()
}

func (p *pty) releaseSlaveRef() {
	p.mu.Lock()
	p.slaveRefs--
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *pty) isHungUp() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hungUp
}

func (p *pty) setSize(size workspaceapi.PtySize) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.masterClosed {
		return ErrPtyClosed
	}
	p.size = size
	p.winch = true
	p.cond.Broadcast()
	return nil
}

// windowSize returns the current size and clears the pending resize
// flag, for a program that is about to lay itself out from scratch.
func (p *pty) windowSize() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.winch = false
	return p.size.Columns, p.size.Rows
}

func (p *pty) hasFd(fd uintptr) bool {
	return fd == p.masterFd || fd == p.slaveFd
}

// ptyMaster is the terminal emulator's handle on a pty.
type ptyMaster struct {
	p *pty
}

var _ workspaceapi.File = (*ptyMaster)(nil)

func (m *ptyMaster) Name() string                      { return "/dev/vptmx" }
func (m *ptyMaster) Fd() uintptr                       { return m.p.masterFd }
func (m *ptyMaster) Read(b []byte) (int, error)        { return m.p.readMaster(b) }
func (m *ptyMaster) Write(b []byte) (int, error)       { return m.p.writeMaster(b) }
func (m *ptyMaster) Close() error                      { m.p.closeMaster(); return nil }
func (m *ptyMaster) Stat() (os.FileInfo, error)        { return ptyFileInfo{name: m.Name()}, nil }
func (m *ptyMaster) Sync() error                       { return nil }
func (m *ptyMaster) Truncate(int64) error              { return syscall.EINVAL }
func (m *ptyMaster) Seek(int64, int) (int64, error)    { return 0, syscall.ESPIPE }
func (m *ptyMaster) ReadAt([]byte, int64) (int, error) { return 0, syscall.ESPIPE }

// ptySlave is the parent's handle on the program side of a pty. It
// is what callers pass as a command's standard streams; Close
// releases the parent's reference exactly once.
type ptySlave struct {
	p      *pty
	closed atomic.Bool
}

var _ workspaceapi.File = (*ptySlave)(nil)

func (s *ptySlave) Name() string { return s.p.name }
func (s *ptySlave) Fd() uintptr  { return s.p.slaveFd }

// Read is a cooked read of what the terminal typed.
func (s *ptySlave) Read(b []byte) (int, error) {
	return s.p.ld.read(context.Background(), b)
}

func (s *ptySlave) Write(b []byte) (int, error) {
	return s.p.writeOutput(context.Background(), b)
}

func (s *ptySlave) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		s.p.releaseSlaveRef()
	}
	return nil
}

func (s *ptySlave) Stat() (os.FileInfo, error)        { return ptyFileInfo{name: s.p.name}, nil }
func (s *ptySlave) Sync() error                       { return nil }
func (s *ptySlave) Truncate(int64) error              { return syscall.EINVAL }
func (s *ptySlave) Seek(int64, int) (int64, error)    { return 0, syscall.ESPIPE }
func (s *ptySlave) ReadAt([]byte, int64) (int, error) { return 0, syscall.ESPIPE }

// ptyFileInfo is the synthetic stat of a pty device.
type ptyFileInfo struct {
	name string
}

func (i ptyFileInfo) Name() string       { return i.name }
func (i ptyFileInfo) Size() int64        { return 0 }
func (i ptyFileInfo) Mode() os.FileMode  { return os.ModeDevice | os.ModeCharDevice | 0o620 }
func (i ptyFileInfo) ModTime() time.Time { return time.Time{} }
func (i ptyFileInfo) IsDir() bool        { return false }
func (i ptyFileInfo) Sys() any           { return nil }
