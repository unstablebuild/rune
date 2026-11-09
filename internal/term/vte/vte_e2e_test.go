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

//go:build e2e

package vte

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"
	"unstable.build/rune/internal/workspace"
)

// e2eTimeout bounds every wait on a real program; it is generous
// because the kernel hands a program about 1KiB of input per wakeup.
const e2eTimeout = 30 * time.Second

// e2eTerminal is a Handler over a pty from the real file scheme that
// runs a real program, driven as the host drives it: input goes
// through Handle, and what the user would be told goes to
// notifications. It is the fixture every e2e test of the package
// builds on.
type e2eTerminal struct {
	*Handler
	notifications *recordingNotifications
}

// e2eOptions configures an e2eTerminal. The zero value runs sh in a
// cooked 80x24 tty.
type e2eOptions struct {
	// command is the terminal.command line to run.
	command string
	// width and height size the terminal before the program starts.
	width, height int
	// rawTTY hands the program a raw tty from the start, the state a
	// full-screen program raises itself before the user interacts with
	// it. A cooked tty would instead line-edit the input.
	rawTTY bool
	// config adjusts the Config the terminal is built with.
	config func(*Config)
}

func newE2ETerminal(t *testing.T, opts e2eOptions) *e2eTerminal {
	t.Helper()
	if opts.command == "" {
		opts.command = "sh"
	}
	if opts.width == 0 {
		opts.width = 80
	}
	if opts.height == 0 {
		opts.height = 24
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	uri, err := workspaceapi.CurrentUserHostURI(os.TempDir())
	require.NoError(t, err)
	dataDir := t.TempDir()
	shellRCDir, err := workspace.InstallShellRC(dataDir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileSchemeFunc(dataDir, shellRCDir)(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { scheme.Close() })

	var terminal schemeapi.Terminal = scheme
	if opts.rawTTY {
		terminal = rawTerminal{scheme}
	}
	cfg := DefaultConfig()
	cfg.WidthHint, cfg.HeightHint = opts.width, opts.height
	cfg.CommandAndArgs = []string{opts.command}
	if opts.config != nil {
		opts.config(&cfg)
	}
	h := &e2eTerminal{notifications: new(recordingNotifications)}
	h.Handler, err = NewHandler(nopEventPublisher{}, h.notifications,
		terminal, scheme, nopTabManager{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { h.Close() })

	// The read stage starts asynchronously in Run and is what makes
	// the master non-blocking. Until then the terminal is not running
	// as it does in production: input written before would go through
	// a blocking master that no test expectation covers.
	fd := h.comp.pty.Master.Fd()
	require.Eventually(t, func() bool {
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		return err == nil && flags&unix.O_NONBLOCK != 0
	}, e2eTimeout, time.Millisecond, "the read stage did not start")
	return h
}

// rawTerminal puts the tty in raw mode before the program starts.
type rawTerminal struct{ schemeapi.Terminal }

func (t rawTerminal) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	pty, err := t.Terminal.NewPty(ctx)
	if err != nil {
		return pty, err
	}
	if _, err := xterm.MakeRaw(int(pty.Slave.Fd())); err != nil {
		return pty, fmt.Errorf("raw tty: %w", err)
	}
	return pty, nil
}

// send drives events through Handle in order, requiring each to be
// handled without the terminal exiting.
func (h *e2eTerminal) send(t *testing.T, evs ...term.Event) {
	t.Helper()
	for _, ev := range evs {
		exit, handled := h.Handle(ev)
		require.False(t, exit, "terminal exited on %+v", ev)
		require.True(t, handled, "%+v was not handled; notifications: %v",
			ev, h.notifications.Messages())
	}
}

// paste delivers payload the way the host does: a start marker, the
// raw bytes, and an end marker that flushes the whole paste to the pty
// in one write.
func (h *e2eTerminal) paste(t *testing.T, payload []byte) {
	t.Helper()
	h.send(t,
		term.Event{Type: term.EventPasteStart},
		term.Event{Type: term.EventRaw, Raw: payload},
		term.Event{Type: term.EventPasteEnd})
}

// waitFile waits until path holds at least n bytes and returns them,
// reporting what the user was told when they never arrive.
func (h *e2eTerminal) waitFile(t *testing.T, path string, n int) []byte {
	t.Helper()
	var size int64
	ok := assert.Eventually(t, func() bool {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		size = info.Size()
		return size >= int64(n)
	}, e2eTimeout, 10*time.Millisecond)
	if !ok {
		t.Fatalf("%s holds %d of %d bytes; notifications: %v",
			path, size, n, h.notifications.Messages())
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	return got
}

// e2eCatToFile is a program that writes everything it is given to a
// file, so that what reached it can be read back byte for byte. The
// path is quoted once for the terminal's field split and once for sh.
func e2eCatToFile(t *testing.T) (command, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "received")
	return fmt.Sprintf(`sh -c 'exec cat > "$0"' '%s'`, path), path
}

// requireSameBytes reports a mismatch by offset rather than by dumping
// both payloads.
func requireSameBytes(t *testing.T, want, got []byte) {
	t.Helper()
	if bytes.Equal(want, got) {
		return
	}
	for i := range min(len(want), len(got)) {
		if want[i] != got[i] {
			t.Fatalf("byte %d differs: want %q, got %q (lengths %d and %d)",
				i, want[i], got[i], len(want), len(got))
		}
	}
	t.Fatalf("got %d bytes, want %d", len(got), len(want))
}

// e2ePasteSize is a paste far past any kernel's tty input queue, as a
// multi-screen block pasted into an editor is.
const e2ePasteSize = 4 << 20

func TestE2EPasteLargerThanTtyQueueArrivesWhole(t *testing.T) {
	command, received := e2eCatToFile(t)
	h := newE2ETerminal(t, e2eOptions{command: command, rawTTY: true})

	payload := alphaPayload(e2ePasteSize)
	h.paste(t, payload)

	requireSameBytes(t, payload, h.waitFile(t, received, len(payload)))
	assert.Empty(t, h.notifications.Messages())
}
