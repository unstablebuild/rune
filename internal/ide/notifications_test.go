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
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/release/docrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/workspace"
)

func newTestNotifications(uri workspaceapi.URI, t *testing.T) (*testNotifier, *notis) {
	mock := newTestNotify()
	b := &notis{
		root:    mock,
		storage: storagestub.NewInMemoryService(),
		parent:  &workspaceManagerMock{workspace: new(ex), wantFocusURI: uri},
		uri:     uri,
		cfg: notifications.Config{
			ColorError: term.Attributes{Fg: term.ColorRed},
		},
	}
	return mock, b
}

func TestNotifyAcrossWorkspaces(t *testing.T) {
	t.Run("notifications are paused and attention attrs set", func(t *testing.T) {
		workspace, err := workspaceapi.ParseURI("file:///b")
		require.NoError(t, err)
		mock, b := newTestNotifications(workspace, t)
		workspaceMock := b.parent.(*workspaceManagerMock)
		workspaceMock.wantFocusURI, err = workspaceapi.ParseURI("file:///a")
		require.NoError(t, err)

		_, err = b.Notify(browserapi.LevelError, "abc")
		require.NoError(t, err)

		assert.Equal(t, 1, mock.messages["abc_paused"])
		expectedAttrs := map[workspaceapi.URI]term.Attributes{
			workspace: {Fg: term.ColorRed},
		}
		assert.Equal(t, expectedAttrs, workspaceMock.attrs)
	})
}

func TestSetWorkspaceRequiresAttentionRace(t *testing.T) {
	homeURI, err := workspaceapi.ParseURI("memory:///home")
	require.NoError(t, err)

	mu := new(sync.Mutex)
	sched, drain := newTestScheduler(t, mu)
	cfg := defaultCfg()
	cfg.scheduleNextTick = sched

	manager := workspace.NewManager(cfg.workspace(), sched)
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
	h.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	err = h.init(nil, homeURI, manager,
		notificationsConfig(), cfg, storage, dir,
		func(term.Event) bool { return true },
		FuncExtensionsRunner(testRunnerFn), pkgtrust.NewStore(dir, nil), mu, nil,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, 0, 0, '1', 0, 0, true, nil, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry())
	require.NoError(t, err)

	wsURI, err := workspaceapi.ParseURI("memory:///ws")
	require.NoError(t, err)
	// Slot 1 is not the focused slot (focus defaults to 0), so Resize
	// never dereferences this handler's embedded *ex.
	h.workspaces[1] = &workspaceHandler{uri: wsURI}
	h.width, h.height = 80, 24

	attr := term.Attributes{Fg: term.ColorRed}
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			h.setWorkspaceRequiresAttention(wsURI, attr)
		}()
	}
	wg.Wait()
	drain()

	assert.Equal(t, attr, h.workspaces[1].attentionAttr)
}

func TestNotificationsInheritRightInset(t *testing.T) {
	const rightInset = 5

	homeURI, err := workspaceapi.ParseURI("memory:///home")
	require.NoError(t, err)

	mu := new(sync.Mutex)
	sched, drainSched := newTestScheduler(t, mu)
	cfg := defaultCfg()
	cfg.scheduleNextTick = sched

	manager := workspace.NewManager(cfg.workspace(), sched)
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
	h.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	require.NoError(t, h.init(nil, homeURI, manager,
		notificationsConfig(), cfg, storage, dir,
		func(term.Event) bool { return true },
		FuncExtensionsRunner(testRunnerFn), pkgtrust.NewStore(dir, nil), mu, nil,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, rightInset, 0, '1', 0, 0, true, nil, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry()))
	drainSched()
	// Close runs under the IDE locker, as the event loop holds it.
	defer func() {
		mu.Lock()
		assert.NoError(t, h.Close())
	}()

	assert.Equal(t, rightInset, h.notifications.cfg.RightInset)
}

func TestNotifyOnce(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	t.Run("delivers notifications only the first time it's invoked", func(t *testing.T) {
		t.Parallel()

		mock, b := newTestNotifications(uri, t)

		_, err := b.NotifyOnce(browserapi.LevelError, "a")
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a"])

		_, err = b.NotifyOnce(browserapi.LevelError, "a")
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a"])
	})

	t.Run("delivers notifications with different args multiple times, if args are different", func(t *testing.T) {
		t.Parallel()

		mock, b := newTestNotifications(uri, t)

		_, err := b.NotifyOnce(browserapi.LevelError, "a %d", 0)
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a 0"])

		_, err = b.NotifyOnce(browserapi.LevelError, "a %d", 1)
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a 1"])
		assert.Equal(t, 1, mock.messages["a 0"])
	})

	t.Run("delivers notifications with different args only once if args are the same", func(t *testing.T) {
		t.Parallel()

		mock, b := newTestNotifications(uri, t)

		_, err := b.NotifyOnce(browserapi.LevelError, "a %d", 0)
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a 0"])

		_, err = b.NotifyOnce(browserapi.LevelError, "a %d", 0)
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a 0"])
	})

	t.Run("delivers notifications multiple times if subsequent uses Notify rather than NotifyOnce", func(t *testing.T) {
		t.Parallel()

		mock, b := newTestNotifications(uri, t)

		_, err := b.NotifyOnce(browserapi.LevelError, "a")
		require.NoError(t, err)
		assert.Equal(t, 1, mock.messages["a"])

		b.Notify(browserapi.LevelError, "a")
		assert.Equal(t, 2, mock.messages["a"])

		_, err = b.NotifyOnce(browserapi.LevelError, "a")
		require.NoError(t, err)
		assert.Equal(t, 2, mock.messages["a"])
	})
}

func TestUpdateProgressRoutesToOriginWorkspace(t *testing.T) {
	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)

	newContainer := func(uri workspaceapi.URI, parent workspaceManagerIfc) *ex {
		e := new(ex)
		e.notifications = &notis{
			root:    notifications.New(handler.Nop(), notificationsConfig()),
			storage: storagestub.NewInMemoryService(),
			parent:  parent,
			uri:     uri,
			cfg:     notificationsConfig(),
		}
		return e
	}

	t.Run("terminal update reaches origin after focus switch", func(t *testing.T) {
		mock := &workspaceManagerMock{}
		a := newContainer(uriA, mock)
		b := newContainer(uriB, mock)
		mock.byURIHash = map[string]browserapi.Notifications{
			uriHash(uriA): a.notifications,
			uriHash(uriB): b.notifications,
		}
		router := &notisRouter{parent: mock}

		// A is focused when the notification is created.
		mock.workspace, mock.wantFocusURI = a, uriA
		id, err := router.Notify(browserapi.LevelInfo, "Indexing")
		require.NoError(t, err)
		require.NoError(t, router.UpdateNotificationProgress(id, "Indexing", 1, 2))

		// Focus moves to B; the terminal update must still reach A.
		mock.workspace, mock.wantFocusURI = b, uriB
		require.NoError(t, router.UpdateNotificationProgress(id, "done", 2, 2))

		hash, realID, ok := splitURIHash(id)
		require.True(t, ok)
		require.Equal(t, uriHash(uriA), hash)
		assert.False(t, a.notifications.(*notis).root.UpdateProgress(realID, "", 2, 2),
			"origin notification should be closed and removed")
	})

	t.Run("update for uninstalled origin falls back to focus", func(t *testing.T) {
		mock := &workspaceManagerMock{workspace: newContainer(uriB, nil), wantFocusURI: uriB}
		mock.byURIHash = map[string]browserapi.Notifications{}
		router := &notisRouter{parent: mock}

		// realID is unknown to the focused container, so it errors.
		assert.Error(t, router.UpdateNotificationProgress(
			withURIHash(uriA, "123"), "x", 1, 2))
	})

	t.Run("unprefixed id routes to focus", func(t *testing.T) {
		mock := &workspaceManagerMock{workspace: newContainer(uriB, nil), wantFocusURI: uriB}
		router := &notisRouter{parent: mock}

		// No hash prefix and unknown to focus, so it errors rather than panicking.
		assert.Error(t, router.UpdateNotificationProgress("456", "x", 1, 2))
	})
}

func TestURIHashRoundTrip(t *testing.T) {
	uris := []string{
		"file:///a",
		"file:///deep/nested/workspace",
		"memory:///home",
		"file:///with:colon/in/path",
		"file:///unicode/📁/wörk",
	}
	realIDs := []string{
		"",
		"123",
		"18446744073709551615", // max uint64 decimal
		":",
		"a:b:c",
		":leading",
		"trailing:",
		"has spaces",
		"emoji-😀-id",
		"new\nline",
		"\x00null\x00",
		strings.Repeat("x", 4096),
	}

	for _, us := range uris {
		uri, err := workspaceapi.ParseURI(us)
		require.NoError(t, err)
		for _, realID := range realIDs {
			name := fmt.Sprintf("%s|%q", us, realID)
			t.Run(name, func(t *testing.T) {
				encoded := withURIHash(uri, realID)
				hash, decoded, ok := splitURIHash(encoded)
				require.True(t, ok, "encoded id must carry a hash prefix")
				assert.Equal(t, uriHash(uri), hash)
				assert.Equal(t, realID, decoded,
					"realID must survive the round trip verbatim")
			})
		}
	}
}

func TestUpdateProgressRoutesAdversarialMessages(t *testing.T) {
	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)

	messages := []string{
		"",
		"Indexing",
		"progress: 50%",
		"colon:separated:message",
		"emoji 🚀 status",
		"multi\nline\nmessage",
		"tab\tseparated",
		"very " + strings.Repeat("long ", 1000) + "message",
		"<html>&entities;</html>",
		"null\x00byte",
	}

	for i, msg := range messages {
		t.Run(fmt.Sprintf("%d/%q", i, msg), func(t *testing.T) {
			mock := &workspaceManagerMock{}
			a := &ex{notifications: &notis{
				root:    notifications.New(handler.Nop(), notificationsConfig()),
				storage: storagestub.NewInMemoryService(),
				parent:  mock,
				uri:     uriA,
				cfg:     notificationsConfig(),
			}}
			b := &ex{notifications: &notis{
				root:    notifications.New(handler.Nop(), notificationsConfig()),
				storage: storagestub.NewInMemoryService(),
				parent:  mock,
				uri:     uriB,
				cfg:     notificationsConfig(),
			}}
			mock.byURIHash = map[string]browserapi.Notifications{
				uriHash(uriA): a.notifications,
				uriHash(uriB): b.notifications,
			}
			router := &notisRouter{parent: mock}

			mock.workspace, mock.wantFocusURI = a, uriA
			id, err := router.Notify(browserapi.LevelInfo, "%s", msg)
			require.NoError(t, err)
			require.NoError(t,
				router.UpdateNotificationProgress(id, msg, 1, 2))

			mock.workspace, mock.wantFocusURI = b, uriB
			require.NoError(t,
				router.UpdateNotificationProgress(id, "done", 2, 2))

			_, realID, ok := splitURIHash(id)
			require.True(t, ok)
			assert.False(t,
				a.notifications.(*notis).root.UpdateProgress(realID, "", 2, 2),
				"origin notification must be closed by the terminal update")
		})
	}
}

type testNotifier struct {
	messages map[string]int
}

func newTestNotify() *testNotifier {
	ret := new(testNotifier)
	ret.messages = make(map[string]int)
	return ret
}

func (t *testNotifier) Notify(
	level notifications.Level, msg string,
) string {
	t.messages[msg] = t.messages[msg] + 1
	return t.ID(level, msg)
}

func (t *testNotifier) ID(
	level notifications.Level, msg string,
) string {
	h := fnv.New64a()
	h.Write([]byte(msg))
	return strconv.FormatUint(h.Sum64(), 10)
}

func (t *testNotifier) UpdateProgress(
	id, message string, progress, total int64,
) bool {
	return false
}

func (t *testNotifier) CloseAll() {
	clear(t.messages)
}

func (t *testNotifier) ResumeAll() {
}

func (t *testNotifier) PauseAll() {
	m := make(map[string]int)
	for k, v := range t.messages {
		m[k+"_paused"] = v
	}
	t.messages = m
}
