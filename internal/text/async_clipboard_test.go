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

package text

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/debug"
)

// gatedRegister holds its first default-register Copy on a gate so a burst of
// copies overlaps one in-flight OS write, the way wl-copy latency stretches a
// Vim `.` repeat. It records what reaches the wrapped layer.
type gatedRegister struct {
	clipboard.Register
	entered chan struct{}
	release chan struct{}

	mu     sync.Mutex
	gated  bool
	texts  []string
	pastes int
}

func (r *gatedRegister) Copy(registerID string, data clipboard.Data) error {
	var first bool
	r.mu.Lock()
	r.texts = append(r.texts, data.Text)
	if registerID == clipboard.DefaultRegisterID && !r.gated && r.entered != nil {
		r.gated = true
		first = true
	}
	r.mu.Unlock()
	if first {
		close(r.entered)
		<-r.release
	}
	return r.Register.Copy(registerID, data)
}

func (r *gatedRegister) Paste(registerID string) (clipboard.Data, error) {
	r.mu.Lock()
	r.pastes++
	r.mu.Unlock()
	return r.Register.Paste(registerID)
}

func (r *gatedRegister) copied() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

// TestAsyncClipboardCopyNeverBlocksOnOSWrite reproduces the hang from holding
// `.` to repeat a Vim dw: every repeated delete re-copied the deleted word
// and each copy shelled out a fresh wl-copy on the event loop goroutine.
// Copies must return while an OS write is in flight, and the burst must cost
// one in-flight write plus one write for the newest payload.
func TestAsyncClipboardCopyNeverBlocksOnOSWrite(t *testing.T) {
	sys := &gatedRegister{
		Register: clipboard.NewInMemory(),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	clip := newAsyncRegister(sys)

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "word-0"}))
	<-sys.entered

	const burst = 50
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		for i := 1; i <= burst; i++ {
			_ = clip.Copy(clipboard.DefaultRegisterID,
				clipboard.Data{Text: fmt.Sprintf("word-%d", i)})
		}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("copies blocked behind an in-flight OS clipboard write")
	}
	close(sys.release)

	require.Eventually(t, func() bool {
		return len(sys.copied()) == 2
	}, 5*time.Second, time.Millisecond,
		"a burst must cost the in-flight write plus one write for the newest payload")
	require.Equal(t, []string{"word-0", fmt.Sprintf("word-%d", burst)}, sys.copied(),
		"the OS clipboard must end up holding the newest payload, not an intermediate")

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "next"}))
	require.Eventually(t, func() bool {
		return len(sys.copied()) == 3
	}, 5*time.Second, time.Millisecond,
		"a copy that arrives after the burst settles must still write through")
}

// TestAsyncClipboardForwardsNamedRegistersSynchronously checks the wrapper's
// contract that only the default register is queued: named registers are
// in-memory writes and must reach the wrapped clipboard before Copy returns.
func TestAsyncClipboardForwardsNamedRegistersSynchronously(t *testing.T) {
	sys := &gatedRegister{Register: clipboard.NewInMemory()}
	clip := newAsyncRegister(sys)

	require.NoError(t, clip.Copy("a", clipboard.Data{Text: "named"}))
	require.Equal(t, []string{"named"}, sys.copied(),
		"a named register write must be forwarded synchronously, not queued")

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "os"}))
	require.Eventually(t, func() bool {
		return len(sys.copied()) == 2
	}, 5*time.Second, time.Millisecond,
		"the default register write must reach the OS layer through the worker")
	require.Equal(t, []string{"named", "os"}, sys.copied())
}

// TestAsyncClipboardPasteReadsNewestCopyWhileWriteInFlight covers the copy
// worker racing the event loop: while a write is queued or in flight the OS
// clipboard is not authoritative, so Paste must answer with the newest copy
// instead of reading stale OS content.
func TestAsyncClipboardPasteReadsNewestCopyWhileWriteInFlight(t *testing.T) {
	sys := &gatedRegister{
		Register: clipboard.NewInMemory(),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	clip := newAsyncRegister(sys)

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "fresh"}))
	<-sys.entered

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	require.Equal(t, "fresh", data.Text,
		"paste must return the newest copy while its OS write is in flight")
	sys.mu.Lock()
	require.Zero(t, sys.pastes,
		"paste must not read the OS clipboard while a write is in flight")
	sys.mu.Unlock()

	_, err = clip.Paste("a")
	require.NoError(t, err)
	sys.mu.Lock()
	require.Equal(t, 1, sys.pastes,
		"named register pastes must still reach the wrapped clipboard")
	sys.mu.Unlock()

	close(sys.release)
	require.Eventually(t, func() bool {
		return clip.pending.Load() == 0
	}, 5*time.Second, time.Millisecond,
		"the worker must finish the in-flight write")

	data, err = clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	require.Equal(t, "fresh", data.Text)
	sys.mu.Lock()
	require.Equal(t, 2, sys.pastes,
		"once the write settles, paste must read the OS clipboard again")
	sys.mu.Unlock()
}
