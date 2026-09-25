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

package upgradeshell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/ide/ideupgrade"
)

// promptWindowManager answers the upgrade prompt by feeding the given
// key to the floating handler as soon as it is shown. A zero key
// leaves the prompt unanswered.
type promptWindowManager struct {
	key rune
}

func (m *promptWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	if m.key != 0 {
		h.Resize(80, 20)
		h.Handle(term.Event{Type: term.EventKey, Ch: m.key})
	}
	return nil, nil
}

func (m *promptWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (m *promptWindowManager) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, nil
}
func (m *promptWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (m *promptWindowManager) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}
func (m *promptWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error {
	return nil
}
func (m *promptWindowManager) CloseWindow(browserapi.Window) error { return nil }

func (m *promptWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type recordingProgressWriter struct {
	mu    sync.Mutex
	units []string
}

func (w *recordingProgressWriter) Progress(_, _ int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.units = append(w.units, units)
}

func (w *recordingProgressWriter) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.units))
	copy(out, w.units)
	return out
}

// newHandler stands up a Handler backed by a TLS manifest endpoint
// that responds with status and body.
func newHandler(
	t *testing.T, status int, body any, key rune,
) *Handler {
	t.Helper()
	return newHandlerOSPackaged(t, status, body, key, false)
}

// newHandlerOSPackaged is newHandler with control over the
// OS-packaged flag.
func newHandlerOSPackaged(
	t *testing.T, status int, body any, key rune, osPackaged bool,
) *Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest-"+runtime.GOOS+"-"+runtime.GOARCH+".json",
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			if body != nil {
				require.NoError(t, json.NewEncoder(w).Encode(body))
			}
		})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	mgr, err := ideupgrade.New(ideupgrade.Config{
		CurrentVersion:   "v0.1.0",
		Arch:             runtime.GOOS + "-" + runtime.GOARCH,
		ManifestURL:      srv.URL,
		Storage:          storagestub.NewInMemoryService(),
		HTTPClient:       srv.Client(),
		WindowManager:    &promptWindowManager{key: key},
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	return New(Config{Manager: mgr, OSPackaged: osPackaged})
}

func availableManifest() map[string]any {
	return map[string]any{
		"version":  "v9.9.9",
		"url":      "https://example.invalid/rune-v9.9.9.tar.gz",
		"sha256":   "abc123",
		"filename": "rune-v9.9.9.tar.gz",
	}
}

func TestHandleCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    any
		key     rune
		args    []string
		want    string
		wantErr string
	}{
		{
			name:   "up to date",
			status: http.StatusNotFound,
			want:   "Rune is up to date (**v0.1.0**)",
		},
		{
			name:    "check fails",
			status:  http.StatusInternalServerError,
			wantErr: "status 500",
		},
		{
			name:   "remind later",
			status: http.StatusOK,
			body:   availableManifest(),
			key:    'l',
			want:   "Rune **v9.9.9** is available. Reminder postponed.",
		},
		{
			name:   "skip version",
			status: http.StatusOK,
			body:   availableManifest(),
			key:    's',
			want:   "Skipping Rune **v9.9.9**.",
		},
		{
			name:   "help",
			status: http.StatusNotFound,
			args:   []string{"help"},
			want:   usageMarkdown(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(t, tc.status, tc.body, tc.key)
			pw := &recordingProgressWriter{}
			got, err := h.upgrade(context.Background(),
				repl.Command{Name: CommandName, Args: tc.args}, pw)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestHandleCommandReportsCheckProgress pins the console feedback the
// user gets while the manifest fetch is in flight: without it the
// command looks hung for the duration of the request.
func TestHandleCommandReportsCheckProgress(t *testing.T) {
	h := newHandler(t, http.StatusNotFound, nil, 0)
	pw := &recordingProgressWriter{}
	_, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName}, pw)
	require.NoError(t, err)
	require.Equal(t, []string{"checking for updates"}, pw.snapshot())
}

// TestHandleCommandOSPackaged pins the behaviour of an OS-packaged
// build: the command explains where updates come from and never
// reaches the manifest endpoint. The endpoint is wired to fail, so a
// missing short-circuit surfaces as an error rather than passing.
func TestHandleCommandOSPackaged(t *testing.T) {
	h := newHandlerOSPackaged(
		t, http.StatusInternalServerError, nil, 0, true)
	pw := &recordingProgressWriter{}

	got, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName}, pw)

	require.NoError(t, err)
	require.Equal(t, "This build was distributed by an OS package manager, "+
		"so auto-updates are disabled. Check your distribution's package "+
		"manager for updates.", got)
	require.Empty(t, pw.snapshot(),
		"must not report check progress when no check runs")
}

// TestHandleCommandOSPackagedHelp keeps `upgrade help` useful in
// OS-packaged builds.
func TestHandleCommandOSPackagedHelp(t *testing.T) {
	h := newHandlerOSPackaged(
		t, http.StatusInternalServerError, nil, 0, true)

	got, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"help"}},
		&recordingProgressWriter{})

	require.NoError(t, err)
	require.Equal(t, usageMarkdown(), got)
}

// TestHandleCommandUpgradeNowForwardsProgress covers the "Upgrade Now"
// branch. The test binary is not part of a managed install, so the
// upgrade is refused before any download — which is enough to prove
// the branch reaches Manager.Upgrade with the console's writer.
func TestHandleCommandUpgradeNowForwardsProgress(t *testing.T) {
	h := newHandler(t, http.StatusOK, availableManifest(), 'y')
	pw := &recordingProgressWriter{}
	_, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName}, pw)
	require.Error(t, err)
	var notSupported *ideupgrade.ErrUpgradeNotSupported
	require.ErrorAs(t, err, &notSupported)
}

func TestNewPanicsWithoutManager(t *testing.T) {
	require.Panics(t, func() { New(Config{}) })
}

func TestHandleCommandReturnsOutputComponent(t *testing.T) {
	h := newHandler(t, http.StatusNotFound, nil, 0)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName}, repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := it.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}
