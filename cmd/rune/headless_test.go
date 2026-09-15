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
	"net/url"
	"os"
	"path/filepath"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/runenet"
)

type stubHeadlessClient struct {
	user      auth.RPCUser
	signedIn  bool
	statusErr error

	loginURL  *url.URL
	loginErr  error
	loginUser auth.RPCUser
	loginOK   bool
	calls     int
}

func (s *stubHeadlessClient) AccountStatus(
	context.Context,
) (auth.RPCUser, bool, error) {
	s.calls++
	if s.calls == 1 {
		return s.user, s.signedIn, s.statusErr
	}
	return s.loginUser, s.loginOK, nil
}

func (s *stubHeadlessClient) Login(context.Context) apiclient.LoginSession {
	urlCh := make(chan *url.URL, 1)
	done := make(chan error, 1)
	if s.loginURL != nil {
		urlCh <- s.loginURL
	}
	close(urlCh)
	done <- s.loginErr
	close(done)
	return apiclient.LoginSession{URL: urlCh, Done: done}
}

func TestHeadlessLogin(t *testing.T) {
	authURL, err := url.Parse("https://rune.test/authorize?code=1")
	require.NoError(t, err)

	for _, tc := range []struct {
		name     string
		client   *stubHeadlessClient
		wantErr  string
		contains []string
		absent   []string
	}{
		{
			name: "already signed in skips the browser flow",
			client: &stubHeadlessClient{
				user:     auth.RPCUser{Email: "a@rune.test", Role: auth.RolePaid},
				signedIn: true,
			},
			contains: []string{"Signed in as a@rune.test (Rune Pro)"},
			absent:   []string{"Open this URL"},
		},
		{
			name: "signed out prints the authorization url",
			client: &stubHeadlessClient{
				loginURL:  authURL,
				loginUser: auth.RPCUser{Email: "b@rune.test"},
				loginOK:   true,
			},
			contains: []string{
				"Open this URL in your browser to complete login:",
				authURL.String(),
				"Signed in as b@rune.test (Rune)",
			},
		},
		{
			name: "login failure is returned",
			client: &stubHeadlessClient{
				loginURL: authURL,
				loginErr: errors.New("oauth timed out"),
			},
			wantErr:  "oauth timed out",
			contains: []string{authURL.String()},
		},
		{
			name: "unreadable cached token is returned",
			client: &stubHeadlessClient{
				statusErr: errors.New("jwt: decode payload"),
			},
			wantErr: "jwt: decode payload",
		},
		{
			name: "login without account details still reports success",
			client: &stubHeadlessClient{
				loginURL: authURL,
			},
			contains: []string{"Login successful."},
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
