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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"golang.org/x/oauth2"
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
	logouts        int
	logoutErr      error
	checkErr       error
	checks         int
}

func (s *stubHeadlessClient) AccountStatus(
	context.Context,
) (auth.RPCUser, bool, error) {
	s.calls++
	if s.calls == 1 && s.logouts == 0 {
		return s.user, s.signedIn, s.statusErr
	}
	return s.loginUser, s.loginOK, s.loginStatusErr
}

func (s *stubHeadlessClient) Logout(context.Context) error {
	s.logouts++
	return s.logoutErr
}

func (s *stubHeadlessClient) CheckSignIn(context.Context) error {
	s.checks++
	return s.checkErr
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
		name        string
		client      *stubHeadlessClient
		wantErr     string
		contains    []string
		absent      []string
		wantLogouts int
	}{
		{
			name: "already signed in skips the code prompt",
			client: &stubHeadlessClient{
				user: auth.RPCUser{
					Email: "a@rune.test", Role: auth.RolePaid, ServeOnly: true,
				},
				signedIn: true,
			},
			contains: []string{"Signed in as a@rune.test (Rune Pro)"},
			absent:   []string{"enter the code"},
		},
		{
			name: "a revoked sign-in is replaced",
			client: &stubHeadlessClient{
				user:      auth.RPCUser{Email: "a@rune.test", ServeOnly: true},
				signedIn:  true,
				checkErr:  auth.ErrNotAuthenticated,
				prompt:    prompt,
				loginUser: auth.RPCUser{Email: "a@rune.test", ServeOnly: true},
				loginOK:   true,
			},
			contains: []string{
				"revoked or has expired",
				"enter the code ABCD-EFGH",
				"Signed in as a@rune.test (Rune)",
			},
		},
		{
			// The sign-in may well be fine: only the server can say,
			// so it is not thrown away for a server that cannot answer.
			name: "a sign-in that cannot be checked is an error",
			client: &stubHeadlessClient{
				user:     auth.RPCUser{Email: "a@rune.test", ServeOnly: true},
				signedIn: true,
				checkErr: errors.New("connection refused"),
				prompt:   prompt,
			},
			wantErr: "connection refused",
			absent:  []string{"enter the code", "Signed in"},
		},
		{
			name: "a full-access sign-in is discarded and replaced",
			client: &stubHeadlessClient{
				user:      auth.RPCUser{Email: "a@rune.test", Role: auth.RolePaid},
				signedIn:  true,
				prompt:    prompt,
				loginUser: auth.RPCUser{Email: "a@rune.test", ServeOnly: true},
				loginOK:   true,
			},
			contains: []string{
				"full account access",
				"enter the code ABCD-EFGH",
				"Signed in as a@rune.test (Rune)",
			},
			wantLogouts: 1,
		},
		{
			name: "a full-access sign-in that cannot be discarded is an error",
			client: &stubHeadlessClient{
				user:      auth.RPCUser{Email: "a@rune.test"},
				signedIn:  true,
				logoutErr: errors.New("keychain locked"),
			},
			wantErr:     "keychain locked",
			absent:      []string{"enter the code", "Signed in"},
			wantLogouts: 1,
		},
		{
			name: "a login that yields full access is refused and discarded",
			client: &stubHeadlessClient{
				prompt:    prompt,
				loginUser: auth.RPCUser{Email: "b@rune.test"},
				loginOK:   true,
			},
			wantErr:     "serve-only",
			absent:      []string{"Signed in"},
			wantLogouts: 1,
		},
		{
			name: "signed out prints the code and verification page",
			client: &stubHeadlessClient{
				prompt:    prompt,
				loginUser: auth.RPCUser{Email: "b@rune.test", ServeOnly: true},
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
			assert.Equal(t, tc.wantLogouts, tc.client.logouts)
		})
	}
}

// TestHeadlessLoginReplacesFullAccessToken pins that a headless node
// never runs with a full-access token. One left in its data directory
// from before headless nodes were serve-only, or copied over from a
// desktop install, is discarded, and the node signs in again by code
// through the headless client.
func TestHeadlessLoginReplacesFullAccessToken(t *testing.T) {
	oauth := newHeadlessOAuthServer(t)
	storage := storagestub.NewInMemoryService()
	// Field names, not json tags, are what the stub storage matches on.
	require.NoError(t, storageapi.WithPartition(storage, "auth").Set(
		t.Context(), "tokenv2", struct {
			AccessToken  string
			TokenType    string
			RefreshToken string
			Expiry       time.Time
		}{
			AccessToken:  accountJWT(t, auth.RPCUser{Email: "desk@rune.test"}),
			TokenType:    "Bearer",
			RefreshToken: "desktop-refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}))

	cfg := apiclient.DefaultConfig()
	cfg.HTTPEndpointAddress = oauth.URL
	cfg.Headless = true
	client := apiclient.New(storage, cfg, t.TempDir())
	defer client.Close()

	var out bytes.Buffer
	require.NoError(t, headlessLogin(t.Context(), client, &out))
	assert.Contains(t, out.String(), "full account access")
	assert.Contains(t, out.String(), "enter the code ABCD-EFGH")

	user, ok, err := client.AccountStatus(t.Context())
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, user.ServeOnly)
	assert.Equal(t, []string{"test-headless-client", "test-headless-client"},
		oauth.clientIDs(), "device authorization and token grant")
}

// TestHeadlessLoginReplacesRevokedToken pins that a headless node whose
// sign-in was revoked, or has expired, signs in again by code at
// startup instead of announcing an account it can no longer act for.
func TestHeadlessLoginReplacesRevokedToken(t *testing.T) {
	oauth := newHeadlessOAuthServer(t)
	storage := storagestub.NewInMemoryService()
	require.NoError(t, storageapi.WithPartition(storage, "auth").Set(
		t.Context(), "tokenv2", struct {
			AccessToken  string
			TokenType    string
			RefreshToken string
			Expiry       time.Time
		}{
			AccessToken: accountJWT(t, auth.RPCUser{
				Email: "node@rune.test", ServeOnly: true}),
			TokenType:    "Bearer",
			RefreshToken: revokedRefreshToken,
			Expiry:       time.Now().Add(-time.Hour),
		}))

	cfg := apiclient.DefaultConfig()
	cfg.HTTPEndpointAddress = oauth.URL
	cfg.Headless = true
	client := apiclient.New(storage, cfg, t.TempDir())
	defer client.Close()

	var out bytes.Buffer
	require.NoError(t, headlessLogin(t.Context(), client, &out))
	assert.Contains(t, out.String(), "revoked or has expired")
	assert.Contains(t, out.String(), "enter the code ABCD-EFGH")

	tok := client.CachedTokenSource().Cached(t.Context())
	require.NotNil(t, tok)
	assert.True(t, tok.Valid())
	assert.Equal(t, "headless-refresh-token", tok.RefreshToken)
}

type headlessOAuthServer struct {
	*httptest.Server
	mu  sync.Mutex
	ids []string
}

func (s *headlessOAuthServer) clientIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ids...)
}

// revokedRefreshToken is refused by newHeadlessOAuthServer.
const revokedRefreshToken = "revoked-refresh-token"

// newHeadlessOAuthServer stands in for the API server's oauth2 surface:
// it advertises a headless client and, like the API server, marks only
// the tokens obtained through it serve-only. It refuses
// revokedRefreshToken with the provider error the API server relays.
func newHeadlessOAuthServer(t *testing.T) *headlessOAuthServer {
	t.Helper()
	s := &headlessOAuthServer{}
	record := func(r *http.Request) string {
		require.NoError(t, r.ParseForm())
		id := r.Form.Get("client_id")
		s.mu.Lock()
		s.ids = append(s.ids, id)
		s.mu.Unlock()
		return id
	}
	mux := http.NewServeMux()
	mux.HandleFunc(auth.ServeConfigPath, func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(auth.Config{
			HeadlessClientID: "test-headless-client",
			Config: oauth2.Config{
				ClientID: "test-client-id",
				Endpoint: oauth2.Endpoint{
					AuthStyle:     oauth2.AuthStyleInParams,
					AuthURL:       s.URL + "/authorize",
					DeviceAuthURL: s.URL + "/oauth/device/code",
					TokenURL:      s.URL + auth.ServeTokenPath,
				},
			},
		}))
	})
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-EFGH",` +
			`"verification_uri":"https://auth.rune.test/activate",` +
			`"expires_in":900,"interval":1}`))
	})
	mux.HandleFunc(auth.ServeTokenPath, func(w http.ResponseWriter, r *http.Request) {
		user := auth.RPCUser{
			Email: "node@rune.test", ServeOnly: record(r) == "test-headless-client",
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("refresh_token") == revokedRefreshToken {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"invalid_grant",` +
				`"error_description":"Unknown or invalid refresh token."}`))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accountJWT(t, user),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "headless-refresh-token",
		}))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// accountJWT is an unsigned access token carrying user the way the API
// server's tokens carry the account.
func accountJWT(t *testing.T, user auth.RPCUser) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"extra": user})
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
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

	t.Run("env vars in log_path are expanded", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("RUNE_TEST_HEADLESS_LOG_DIR", dir)
		cfg := config.MapConfig(map[string]any{
			"log_path": "$RUNE_TEST_HEADLESS_LOG_DIR/debug.log",
		})

		closeLog, err := startHeadlessLogging(cfg, &bytes.Buffer{})
		require.NoError(t, err)
		log.Info("expanded log path")
		closeLog()

		onDisk, err := os.ReadFile(filepath.Join(dir, "debug.log"))
		require.NoError(t, err)
		assert.Contains(t, string(onDisk), "expanded log path")
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
