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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

func TestPromptChoiceCompletion(t *testing.T) {
	tests := []struct {
		name     string
		complete func(*promptChoiceCompletion)
		cancel   bool
		want     int
		wantErr  error
	}{
		{
			name: "selection before close",
			complete: func(completion *promptChoiceCompletion) {
				completion.selectIndex(2)
				completion.dismiss()
			},
			want: 2,
		},
		{
			name: "close without selection",
			complete: func(completion *promptChoiceCompletion) {
				completion.dismiss()
			},
			want:    -1,
			wantErr: context.Canceled,
		},
		{
			name: "first terminal outcome wins",
			complete: func(completion *promptChoiceCompletion) {
				completion.selectIndex(1)
				completion.dismiss()
				completion.selectIndex(0)
				completion.dismiss()
			},
			want: 1,
		},
		{
			name:    "caller cancellation",
			cancel:  true,
			want:    -1,
			wantErr: context.Canceled,
		},
		{
			name: "completed outcome beats observed cancellation",
			complete: func(completion *promptChoiceCompletion) {
				completion.selectIndex(3)
			},
			cancel: true,
			want:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completion := newPromptChoiceCompletion()
			if tt.complete != nil {
				tt.complete(completion)
			}

			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancel {
				cancel()
			} else {
				defer cancel()
			}

			got, err := completion.wait(ctx)
			assert.Equal(t, tt.want, got)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}

	t.Run("caller cancellation remains observable", func(t *testing.T) {
		completion := newPromptChoiceCompletion()
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()

		got, err := completion.wait(ctx)
		assert.Equal(t, -1, got)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestPromptChoiceBrowserLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "seed.txt"), nil, 0o666))

	configFile, _ := makeTestFiles(t)
	mu := new(sync.Mutex)
	scheduleNextTick, drain := newTestScheduler(t, mu)
	i, err := New(
		workspaceDir,
		configFile.Name(),
		dataDir,
		pkgtrust.NewStore(), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, i.Close()) })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	// Start scheduler dispatch before waiting: the install lands
	// through a scheduled callback.
	drain()
	i.WaitWorkspaces()
	drain()

	type promptResult struct {
		index int
		err   error
	}
	openerScheduled := make(chan struct{})
	var openerScheduledOnce sync.Once
	i.scheduleFn = func(fn func()) bool {
		scheduled := scheduleNextTick(fn)
		openerScheduledOnce.Do(func() { close(openerScheduled) })
		return scheduled
	}
	result := make(chan promptResult, 1)
	go debug.CapturePanicReport(func() {
		index, promptErr := newWorkspaceWindowManagerUI(i).PromptChoice(
			context.Background(), "Trust this host key?", []string{"Trust once"},
		)
		result <- promptResult{index: index, err: promptErr}
	})

	<-openerScheduled
	drain()
	mu.Lock()
	floatingBefore := i.workspaceHandler.focusEx().comp.Browser().FloatingWindows()
	mu.Unlock()
	require.Equal(t, 1, floatingBefore)

	mu.Lock()
	root.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	floatingAfter := i.workspaceHandler.focusEx().comp.Browser().FloatingWindows()
	mu.Unlock()
	assert.Zero(t, floatingAfter)

	got := <-result
	assert.Equal(t, 0, got.index)
	assert.NoError(t, got.err)
}

func TestPromptFields(t *testing.T) {
	password := []browserapi.Field{{Question: "Password", Secret: true}}
	for _, tt := range []struct {
		name   string
		fields []browserapi.Field
		keys   string
		want   []string
		// wantOpen is whether the prompt stays open, answering nothing
		// until the caller gives up.
		wantOpen bool
	}{
		{name: "submit", fields: password, keys: "hunter2<enter>", want: []string{"hunter2"}},
		{name: "submit by option", fields: password, keys: "hunter2<tab><enter>", want: []string{"hunter2"}},
		{
			name:   "each field",
			fields: []browserapi.Field{{Question: "User"}, {Question: "OTP", Secret: true}},
			keys:   "me<tab>123<enter>",
			want:   []string{"me", "123"},
		},
		{name: "esc dismisses", fields: password, keys: "hun<esc>"},
		{name: "ctrl-c dismisses rather than submitting", fields: password, keys: "hun<c-c>"},
		{name: "ctrl-d on empty does not submit", fields: password, keys: "<c-d>", wantOpen: true},
		{name: "caller gives up", fields: password, keys: "hun", wantOpen: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			i, root, mu, drain, scheduled := newSSHPromptTestIDE(t)
			type result struct {
				answers []string
				err     error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go debug.CapturePanicReport(func() {
				answers, err := newWorkspaceWindowManagerUI(i).PromptFields(ctx, "SSH login", tt.fields)
				done <- result{answers, err}
			})
			<-scheduled
			drain()
			floating := func() int {
				mu.Lock()
				defer mu.Unlock()
				return i.workspaceHandler.focusEx().comp.Browser().FloatingWindows()
			}
			require.Equal(t, 1, floating())

			keyEvents, err := term.ParseKeys(tt.keys)
			require.NoError(t, err)
			mu.Lock()
			for _, k := range keyEvents {
				root.Handle(term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod})
			}
			mu.Unlock()

			wantErr := context.Canceled
			if tt.wantOpen {
				require.Equal(t, 1, floating(), "the prompt answered")
				cancel()
			} else if tt.want != nil {
				wantErr = nil
			}
			select {
			case got := <-done:
				assert.Equal(t, tt.want, got.answers)
				assert.ErrorIs(t, got.err, wantErr)
			case <-time.After(5 * time.Second):
				t.Fatal("the prompt did not answer")
			}
			drain()
			assert.Zero(t, floating(), "the prompt is still open")
		})
	}

	t.Run("same prompt already open", func(t *testing.T) {
		i, _, _, drain, scheduled := newSSHPromptTestIDE(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go debug.CapturePanicReport(func() {
			_, err := newWorkspaceWindowManagerUI(i).PromptFields(ctx, "SSH login", password)
			done <- err
		})
		<-scheduled
		drain()

		_, err := newWorkspaceWindowManagerUI(i).PromptFields(ctx, "SSH login", password)
		assert.ErrorContains(t, err, "already open")
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
}

// newSSHPromptTestIDE is an IDE whose event loop runs the callbacks
// scheduled when drain is called. scheduled is closed once the first
// callback after it returns is scheduled.
func newSSHPromptTestIDE(t *testing.T) (
	i *IDE, root tui.Handler, mu *sync.Mutex, drain func() uint64, scheduled chan struct{},
) {
	t.Helper()
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "seed.txt"), nil, 0o666))

	configFile, _ := makeTestFiles(t)
	mu = new(sync.Mutex)
	scheduleNextTick, drain := newTestScheduler(t, mu)
	i, err := New(
		workspaceDir,
		configFile.Name(),
		dataDir,
		pkgtrust.NewStore(), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, i.Close()) })

	root = i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drain()
	i.WaitWorkspaces()
	drain()

	scheduled = make(chan struct{})
	var once sync.Once
	i.scheduleFn = func(fn func()) bool {
		ok := scheduleNextTick(fn)
		once.Do(func() { close(scheduled) })
		return ok
	}
	return i, root, mu, drain, scheduled
}
