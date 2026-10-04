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

package ide

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/release/docrelease"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/workspace"
)

func TestScheduleNextTickDoesNotReacquireHostLock(t *testing.T) {
	t.Parallel()

	// Mirror the production wiring: a single non-reentrant
	// sync.Mutex is shared between the host event loop (gui /
	// tui) and the IDE handler.
	mu := new(sync.Mutex)

	// rawScheduleNextTick is the contract a real host (gui /
	// tui) presents to the IDE: the host buffers fn until its
	// next Update tick, then invokes it while holding mu.
	var pending func()
	cfg := defaultCfg()
	cfg.scheduleNextTick = func(fn func()) bool {
		pending = fn
		return true
	}

	homeURI, err := workspaceapi.ParseURI("memory:///home")
	require.NoError(t, err)

	manager := workspace.NewManager(cfg.workspace(), cfg.scheduleNextTick)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	dir := t.TempDir()
	storage := localstorage.New(context.Background(), dir, docbson.Marshaler())
	releaseManager := docrelease.NewManager(document.NewInMemoryService())

	shRunner := new(shaderRunner)
	shRunner.init(handler.Nop(), term.NopInterrupter(), term.Attributes{},
		nopShutdownShaderConfig(), loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())

	h := new(workspaceManagerHandler)
	h.tutorialsInstalled = func([]string) (bool, error) { return false, nil }
	err = h.init(nil, homeURI, manager,
		notificationsConfig(), cfg, storage, dir,
		func(term.Event) bool { return true },
		FuncExtensionsRunner(testRunnerFn), pkgtrust.NewStore(dir, nil), mu, nil,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, 0, 0, '1', 0, 0, true, nil, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry())
	require.NoError(t, err)

	// Schedule a callback through the IDE's installed scheduler
	// (which is what every IDE caller — idelsp, lspcmd, autosaver,
	// addWorkspace's Phase B closure, etc. — invokes). The
	// callback records that it ran so we can distinguish
	// "deadlocked" from "ran but did the wrong thing".
	var ran bool
	require.True(t, h.scheduleNextTick(func() {
		ran = true
	}))
	require.NotNil(t, pending,
		"scheduleNextTick must forward to the host scheduler")

	// Simulate the host's next tick. (*GUI).Update locks first,
	// then dispatches every queued UserFunc inline. If init wrapped
	// scheduleNextTick with another mu.Lock(), pending() would
	// deadlock here and the watchdog below would fire.
	done := make(chan struct{})
	go func() {
		mu.Lock()
		defer mu.Unlock()
		pending()
		close(done)
	}()

	select {
	case <-done:
		assert.True(t, ran, "scheduled callback never ran")
	case <-time.After(2 * time.Second):
		t.Fatal("scheduleNextTick callback deadlocked: " +
			"init wrapped the host scheduler with h.mu.Lock()")
	}
}
