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

package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
)

type stubHeadlessClient struct {
	user      auth.RPCUser
	signedIn  bool
	statusErr error

	prompt         *apiclient.DevicePrompt
	loginErr       error
	loginUser      auth.RPCUser
	loginOK        bool
	loginStatusErr error
	calls          int
}

func (s *stubHeadlessClient) AccountStatus(
	context.Context,
) (auth.RPCUser, bool, error) {
	s.calls++
	if s.calls == 1 {
		return s.user, s.signedIn, s.statusErr
	}
	return s.loginUser, s.loginOK, s.loginStatusErr
}

func (s *stubHeadlessClient) LoginWithDeviceCode(
	context.Context,
) apiclient.DeviceLoginSession {
	promptCh := make(chan apiclient.DevicePrompt, 1)
	done := make(chan error, 1)
	if s.prompt != nil {
		promptCh <- *s.prompt
	}
	close(promptCh)
	done <- s.loginErr
	close(done)
	return apiclient.DeviceLoginSession{Prompt: promptCh, Done: done}
}

func TestHeadlessLogin(t *testing.T) {
	prompt := &apiclient.DevicePrompt{
		VerificationURI: "https://auth.rune.test/activate",
		UserCode:        "ABCD-EFGH",
		Expiry:          time.Date(2026, 9, 17, 14, 32, 0, 0, time.Local),
	}

	for _, tc := range []struct {
		name     string
		client   *stubHeadlessClient
		wantErr  string
		contains []string
		absent   []string
	}{
		{
			name: "already signed in skips the code prompt",
			client: &stubHeadlessClient{
				user:     auth.RPCUser{Email: "a@rune.test", Role: auth.RolePaid},
				signedIn: true,
			},
			contains: []string{"Signed in as a@rune.test (Rune Pro)"},
			absent:   []string{"enter the code"},
		},
		{
			name: "signed out prints the code and verification page",
			client: &stubHeadlessClient{
				prompt:    prompt,
				loginUser: auth.RPCUser{Email: "b@rune.test"},
				loginOK:   true,
			},
			contains: []string{
				"To sign this machine in, open\n\n    https://auth.rune.test/activate\n\n" +
					"in any browser and enter the code ABCD-EFGH (expires 14:32).",
				"Signed in as b@rune.test (Rune)",
			},
		},
		{
			name: "login failure is returned",
			client: &stubHeadlessClient{
				prompt:   prompt,
				loginErr: errors.New("oauth timed out"),
			},
			wantErr:  "oauth timed out",
			contains: []string{"enter the code ABCD-EFGH"},
		},
		{
			name: "server without sign-in by code is reported",
			client: &stubHeadlessClient{
				loginErr: apiclient.ErrDeviceLoginUnsupported,
			},
			wantErr: "does not offer sign-in by code",
			absent:  []string{"enter the code"},
		},
		{
			name: "unreadable cached token is returned",
			client: &stubHeadlessClient{
				statusErr: errors.New("jwt: decode payload"),
			},
			wantErr: "jwt: decode payload",
		},
		{
			name: "unreadable token after login is returned",
			client: &stubHeadlessClient{
				prompt:         prompt,
				loginStatusErr: errors.New("jwt: decode payload"),
			},
			wantErr: "jwt: decode payload",
			absent:  []string{"Login successful", "Signed in"},
		},
		{
			name: "login that stores no token is an error",
			client: &stubHeadlessClient{
				prompt: prompt,
			},
			wantErr: "no account token",
			absent:  []string{"Login successful", "Signed in"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := headlessLogin(context.Background(), tc.client, &out)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			for _, want := range tc.contains {
				assert.Contains(t, out.String(), want)
			}
			for _, absent := range tc.absent {
				assert.NotContains(t, out.String(), absent)
			}
		})
	}
}

func TestStartHeadlessLogging(t *testing.T) {
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetLevel(log.InfoLevel)
	})

	t.Run("tees the log file to the extra writer", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "debug.log")
		cfg := config.MapConfig(map[string]any{
			"log_path":  logPath,
			"log_level": "info",
		})

		var out bytes.Buffer
		closeLog, err := startHeadlessLogging(cfg, &out)
		require.NoError(t, err)

		log.Info("headless node up")
		closeLog()

		onDisk, err := os.ReadFile(logPath)
		require.NoError(t, err)
		assert.Contains(t, string(onDisk), "headless node up")
		assert.Contains(t, out.String(), "headless node up")
	})

	t.Run("without log_path only the extra writer is used", func(t *testing.T) {
		var out bytes.Buffer
		closeLog, err := startHeadlessLogging(
			config.MapConfig(map[string]any{}), &out)
		require.NoError(t, err)
		defer closeLog()

		log.Info("no log file configured")
		assert.Contains(t, out.String(), "no log file configured")
	})

	t.Run("an unopenable log path is an error", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.MapConfig(map[string]any{"log_path": dir})
		_, err := startHeadlessLogging(cfg, &bytes.Buffer{})
		require.Error(t, err)
	})
}

func TestHeadlessLogLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  map[string]any
		want log.Level
	}{
		{"configured level wins", map[string]any{"log_level": "debug"}, log.DebugLevel},
		{"missing level defaults to info", map[string]any{}, log.InfoLevel},
		{"unparseable level defaults to info", map[string]any{"log_level": "loud"}, log.InfoLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, headlessLogLevel(config.MapConfig(tc.cfg)))
		})
	}
}

func TestFormatNodeStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   runenet.Status
		want string
	}{
		{
			name: "running node lists its addresses",
			st: runenet.Status{
				Hostname: "builder",
				State:    "Running",
				Addrs: []netip.Addr{
					netip.MustParseAddr("100.64.0.1"),
				},
			},
			want: "Network node builder is Running\n  address: 100.64.0.1\n",
		},
		{
			name: "a stalled node reports its last error",
			st: runenet.Status{
				Hostname:  "builder",
				State:     "NeedsLogin",
				LastError: "control server unreachable",
			},
			want: "Network node builder is NeedsLogin\n" +
				"  last error: control server unreachable\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatNodeStatus(tc.st))
		})
	}
}

func TestWaitForShutdownSignalReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		waitForShutdownSignal(ctx)
		close(done)
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForShutdownSignal did not return after the context was cancelled")
	}
}
