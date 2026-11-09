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

package upgradeconsole

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/ideupgrade"
)

// keyPrompter answers the upgrade prompt with the option bound to key
// as soon as it is shown. A zero key leaves the prompt unanswered.
type keyPrompter struct {
	key rune
}

func (p *keyPrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	if p.key == 0 {
		return nil, nil
	}
	for i, b := range cfg.Bindings {
		if b == (term.KeyComb{Ch: p.key}) {
			h.OnSelect(i, cfg.Options[i])
			break
		}
	}
	return nil, h.OnClose()
}

// nopStarter is the ideupgrade.Starter of a manager whose prompt is
// only ever shown by the console command, which runs the upgrade
// itself.
type nopStarter struct{}

func (nopStarter) StartUpgrade() error { return nil }

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
		Prompter:         &keyPrompter{key: key},
		Starter:          nopStarter{},
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

func TestHandleCommandReportsCheckProgress(t *testing.T) {
	h := newHandler(t, http.StatusNotFound, nil, 0)
	pw := &recordingProgressWriter{}
	_, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName}, pw)
	require.NoError(t, err)
	require.Equal(t, []string{"checking for updates"}, pw.snapshot())
}

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

func TestHandleCommandOSPackagedHelp(t *testing.T) {
	h := newHandlerOSPackaged(
		t, http.StatusInternalServerError, nil, 0, true)

	got, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"help"}},
		&recordingProgressWriter{})

	require.NoError(t, err)
	require.Equal(t, usageMarkdown(), got)
}

func TestHandleCommandUpgradeNowForwardsProgress(t *testing.T) {
	h := newHandler(t, http.StatusOK, availableManifest(), 'y')
	pw := &recordingProgressWriter{}
	_, err := h.upgrade(context.Background(),
		repl.Command{Name: CommandName}, pw)
	require.Error(t, err)
	var notSupported *ideupgrade.ErrUpgradeNotSupported
	require.ErrorAs(t, err, &notSupported)
}

func TestHandleCommandYesSkipsPrompt(t *testing.T) {
	// Nobody answers the prompt, so only skipping it reaches the
	// install, which refuses the test binary's layout.
	h := newHandler(t, http.StatusOK, availableManifest(), 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.upgrade(ctx,
		repl.Command{Name: CommandName, Args: []string{YesFlag}},
		&recordingProgressWriter{})
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
