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

//go:build !windows

package vte

import (
	"context"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/sys/unix"
	"unstable.build/rune/internal/debug"
)

// The gather stage drains the pty on a dedicated thread so the kernel
// queue is never left full while output is parsed or drawn. The macOS
// kernel tty queue holds about 1KiB and hands the master at most that
// much per read, so any pause on the reader immediately blocks the
// child; decoupling reading from parsing is what keeps bulk output
// (cat-ing a large file) flowing at the kernel's rate. The design and
// tuning follow ghostty's two-stage pty pipeline.
const (
	// gatherBatchCount bounds how many batches the gather stage can
	// run ahead of the parse stage before it blocks, which (via the
	// kernel pty queue) preserves flow control to the child.
	gatherBatchCount = 4

	// gatherBatchSize is also the unit of work the parse stage does
	// initially; occupancy feedback adjusts it for sustained output.
	gatherBatchSize = 64 * 1024

	gatherMinBatchSize = gatherBatchSize
	gatherMaxBatchSize = 1024 * 1024

	// gatherSaturatedRead marks a stream as saturated: the kernel
	// hands the master at most about 1KiB per read, so a full read
	// means the writer filled the queue (a bulk stream worth briefly
	// waiting on), while anything smaller is an interactive trickle
	// that must be delivered with no added latency.
	// Saturation is sticky for the lifetime of a batch: a short read
	// only means the reader caught the writer mid-refill, and demoting
	// the stream back to trickle handling on every such gap degrades
	// bulk output to per-KiB deliveries whose channel wake-ups dwarf
	// the read syscalls themselves.
	gatherSaturatedRead = 1024

	// gatherSpinMax is how many EAGAINs on a saturated stream are
	// retried with an immediate read before sleeping in poll. The
	// writer refills the drained queue within microseconds, while a
	// sleep and wakeup through poll costs several more; sleeping on
	// every refill gap degenerates to lockstep with the writer at
	// about 1KiB per wakeup. Only saturated streams spin, so an idle
	// or interactive terminal never does.
	gatherSpinMax = 16

	// gatherPollTimeoutMs bounds one poll for the writer's next
	// refill once spinning failed; when it expires the batch is
	// delivered with what it has.
	gatherPollTimeoutMs = 1

	// A payload target learned during continuous output should not
	// delay the first parse after the terminal has gone idle.
	gatherIdleThreshold = 3 * time.Millisecond
	gatherBudget        = gatherIdleThreshold
)

type ptyGather struct {
	ctx       context.Context
	fd        int
	quitR     int
	quitW     int
	free      chan []byte
	ready     chan []byte
	batchSize int
	// err is set by the gather goroutine before ready is closed and
	// must only be read after ready is drained.
	err error
}

// newPtyGather starts the gather stage for a local pty master. It
// reports false when master does not expose a local tty descriptor (a
// remote workspace or a test fake), in which case the caller must read
// the master directly.
func newPtyGather(ctx context.Context, master workspaceapi.File) (*ptyGather, bool) {
	fd := int(master.Fd())
	if fd <= 2 {
		return nil, false
	}
	if _, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err != nil {
		return nil, false
	}
	// A dup keeps raw reads valid while Component.Close closes the
	// master out from under the gather thread; the quit pipe wakes it.
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, false
	}
	unix.CloseOnExec(dup)
	_ = unix.SetNonblock(dup, true)
	var pipe [2]int
	if err := unix.Pipe(pipe[:]); err != nil {
		_ = unix.Close(dup)
		return nil, false
	}
	unix.CloseOnExec(pipe[0])
	unix.CloseOnExec(pipe[1])
	_ = unix.SetNonblock(pipe[0], true)
	_ = unix.SetNonblock(pipe[1], true)

	g := &ptyGather{
		ctx:       ctx,
		fd:        dup,
		quitR:     pipe[0],
		quitW:     pipe[1],
		free:      make(chan []byte, gatherBatchCount),
		ready:     make(chan []byte, gatherBatchCount),
		batchSize: gatherBatchSize,
	}
	for range gatherBatchCount {
		g.free <- make([]byte, gatherBatchSize)
	}
	go debug.CapturePanicReport(func() {
		<-ctx.Done()
		var quit [1]byte
		_, _ = unix.Write(g.quitW, quit[:])
		_ = unix.Close(g.quitW)
	})
	go debug.CapturePanicReport(g.gather)
	return g, true
}

func (g *ptyGather) adjustBatchSize(free int) {
	switch free {
	case gatherBatchCount - 1:
		g.batchSize = min(g.batchSize*9/8, gatherMaxBatchSize)
	case 0:
		g.batchSize = max(g.batchSize*8/9, gatherMinBatchSize)
	}
}

func gatherFillPolicy(
	target int,
	wait time.Duration,
) (int, time.Duration, bool) {
	if wait >= gatherIdleThreshold {
		target = gatherMinBatchSize
	}
	if target == gatherMinBatchSize {
		return target, gatherBudget, wait >= gatherIdleThreshold
	}
	return target, 0, false
}

// release returns a consumed batch to the gather stage. The ring has
// exactly gatherBatchCount buffers so the send can never block.
func (g *ptyGather) release(batch []byte) {
	g.free <- batch[:cap(batch)]
}

func (g *ptyGather) gather() {
	// The gather loop bypasses the runtime poller with raw reads and
	// short polls; pin it to its own thread like any dedicated IO
	// thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(g.ready)
	defer func() {
		_ = unix.Close(g.fd)
		_ = unix.Close(g.quitR)
	}()

	for {
		var buf []byte
		select {
		case buf = <-g.free:
		case <-g.ctx.Done():
			g.err = g.ctx.Err()
			return
		}
		if cap(buf) < g.batchSize {
			buf = make([]byte, gatherMaxBatchSize)
		} else {
			buf = buf[:g.batchSize]
		}
		n, saturated, idle, err := g.fill(buf, g.batchSize)
		if idle {
			g.batchSize = gatherMinBatchSize
		}
		if n > 0 {
			if saturated {
				g.adjustBatchSize(len(g.free))
			}
			select {
			case g.ready <- buf[:n]:
			case <-g.ctx.Done():
				g.err = g.ctx.Err()
				return
			}
		}
		if err != nil {
			g.err = err
			return
		}
	}
}

// fill gathers one batch. target is the current parser work unit for
// saturated output; interactive trickles return as soon as input pauses.
func (g *ptyGather) fill(
	buf []byte,
	target int,
) (int, bool, bool, error) {
	var n, spins int
	var saturated, idle bool
	_, budget, _ := gatherFillPolicy(target, 0)
	var deadline time.Time
	for n < target {
		r, err := unix.Read(g.fd, buf[n:target])
		if r > 0 {
			if n == 0 && budget > 0 {
				deadline = time.Now().Add(budget)
			}
			n += r
			saturated = saturated || r >= gatherSaturatedRead
			spins = 0
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return n, saturated, idle, nil
			}
			continue
		}
		if r == 0 && err == nil {
			return n, saturated, idle, io.EOF
		}
		switch err {
		case unix.EINTR:
			continue
		case unix.EAGAIN:
		default:
			return n, saturated, idle, &os.SyscallError{Syscall: "read", Err: err}
		}
		if n > 0 {
			if !saturated {
				return n, false, idle, nil
			}
			if spins < gatherSpinMax {
				spins++
				continue
			}
		}
		waitStart := time.Now()
		ptyReady, quit, err := g.poll(n > 0)
		if err != nil {
			return n, saturated, idle, err
		}
		if quit {
			if err := g.ctx.Err(); err != nil {
				return n, saturated, idle, err
			}
			return n, saturated, idle, context.Canceled
		}
		if !ptyReady && n > 0 {
			return n, saturated, idle, nil
		}
		if n == 0 {
			var becameIdle bool
			target, budget, becameIdle = gatherFillPolicy(target, time.Since(waitStart))
			idle = idle || becameIdle
		}
	}
	return n, saturated, idle, nil
}

func (g *ptyGather) poll(bounded bool) (ptyReady, quit bool, err error) {
	timeout := -1
	if bounded {
		timeout = gatherPollTimeoutMs
	}
	fds := [2]unix.PollFd{
		{Fd: int32(g.fd), Events: unix.POLLIN},
		{Fd: int32(g.quitR), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(fds[:], timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, false, &os.SyscallError{Syscall: "poll", Err: err}
		}
		if fds[1].Revents != 0 {
			return false, true, nil
		}
		return fds[0].Revents != 0, false, nil
	}
}
