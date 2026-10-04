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

package workspacetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func TestSchemeConstructorDoesNotDeadlockEventLoop(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	// Real container that requires password auth.
	c := StartContainer(t, SSHDScenario{
		PasswordAccess: true, UserPassword: "hunter2",
	})

	loop := make(chan func(), 4)
	ui := newEventLoopUI(loop, "hunter2")

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	// No private keys configured: the only auth route is password,
	// which will trigger ui.PromptSecret (and therefore exercise the
	// deadlock condition).
	cfg := config.MapConfig(map[string]any{
		"insecure": true,
		"timeout":  "20s",
	})

	// Watchdog: detect deadlock by panicking if the constructor never
	// returns. We must run the constructor *inline* on the test
	// goroutine — the "event loop" equivalent — to reproduce the real
	// deadlock: the dial path needs to post work onto `loop` and wait
	// for this goroutine to pick it up.
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-doneCh:
		case <-time.After(10 * time.Second):
			panic("scheme constructor deadlocked: did not return within 10s")
		}
	}()

	schemeFn := workspacessh.New(ui)
	s, _ := schemeFn(context.Background(), cfg, uri)
	if s != nil {
		_ = s.Close()
	}
	close(doneCh)

	// Pump pending loop work so background goroutines can proceed (the
	// dial may still be waiting to display a password prompt).
	for pumped := 0; pumped < 4; pumped++ {
		select {
		case fn := <-loop:
			if fn != nil {
				fn()
			}
		case <-time.After(500 * time.Millisecond):
			return
		}
	}
}

// eventLoopUI mimics the IDE adapter's threading model: prompts are
// rendered on a single "event loop" goroutine, and PromptSecret blocks
// the calling goroutine until the loop processes the work.
//
// If workspacessh.New is invoked from the event-loop goroutine itself
// and synchronously triggers a prompt, the only way to avoid deadlock
// is for the dial to happen on a different goroutine — which is what
// remoteScheme.maintainConnection guarantees.
type eventLoopUI struct {
	loop      chan func()
	pwAnswers []string
	mu        sync.Mutex
}

func newEventLoopUI(loop chan func(), passwords ...string) *eventLoopUI {
	return &eventLoopUI{loop: loop, pwAnswers: passwords}
}

func (u *eventLoopUI) PromptSecret(_ context.Context, _ string) (string, error) {
	res := make(chan string, 1)
	// Post the work onto the event loop. The loop goroutine renders the
	// prompt and sends the answer back through res.
	u.loop <- func() {
		u.mu.Lock()
		var ans string
		if len(u.pwAnswers) > 0 {
			ans = u.pwAnswers[0]
			u.pwAnswers = u.pwAnswers[1:]
		}
		u.mu.Unlock()
		res <- ans
	}
	return <-res, nil
}

func (u *eventLoopUI) PromptText(context.Context, string, string) (string, error) {
	return "", context.Canceled
}

func (u *eventLoopUI) PromptChoice(context.Context, string, []string) (int, error) {
	return -1, context.Canceled
}

func (u *eventLoopUI) Notify(workspacessh.NotificationLevel, string) {}
