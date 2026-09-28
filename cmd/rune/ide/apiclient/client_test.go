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

package apiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"golang.org/x/oauth2"
	"unstable.build/rune/auth"
)

func TestNewDoesNotStartTelemetryWhenDisabled(t *testing.T) {
	requests := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// New always fetches the oauth2 config in the background for
		// the package trust keyring; only telemetry traffic matters.
		if r.URL.Path == auth.ServeConfigPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	select {
	case <-requests:
		t.Fatal("telemetry request sent with disabled telemetry")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNewStartsTelemetryWhenEnabled(t *testing.T) {
	requests := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	config.EnableTelemetry = true
	config.InstallBackupDir = t.TempDir()

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	for {
		select {
		case path := <-requests:
			if path == telemetryPath {
				return
			}
		case <-timer.C:
			t.Fatal("expected telemetry request when telemetry is enabled")
		}
	}
}

func TestNewPanicsOnZeroPeriodWithTelemetryEnabled(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero TelemetryPeriod with EnableTelemetry=true")
		}
	}()
	config := DefaultConfig()
	config.EnableTelemetry = true
	config.TelemetryPeriod = 0
	config.HTTPEndpointAddress = "http://localhost"
	config.InstallBackupDir = t.TempDir()
	_ = New(storagestub.NewInMemoryService(), config, t.TempDir())
}

func TestNewPanicsOnEmptyInstallBackupDirWithTelemetryEnabled(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty InstallBackupDir with EnableTelemetry=true")
		}
	}()
	config := DefaultConfig()
	config.EnableTelemetry = true
	config.HTTPEndpointAddress = "http://localhost"
	_ = New(storagestub.NewInMemoryService(), config, t.TempDir())
}

func TestInstallTampered(t *testing.T) {
	newClient := func(t *testing.T, enableTelemetry bool, backupDir string) *Client {
		t.Helper()
		config := DefaultConfig()
		config.HTTPEndpointAddress = "http://localhost"
		config.EnableTelemetry = enableTelemetry
		config.InstallBackupDir = backupDir
		client := New(storagestub.NewInMemoryService(), config, t.TempDir())
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		return client
	}

	t.Run("fresh install is not tampered", func(t *testing.T) {
		dir, _ := testInstallIDBackup(t)
		assert.False(t, newClient(t, true, dir).InstallTampered())
	})

	t.Run("wiped store with surviving backup is tampered", func(t *testing.T) {
		dir, tempPath := testInstallIDBackup(t)
		require.NoError(t, os.WriteFile(tempPath, []byte("backup-id"), 0o600))
		assert.True(t, newClient(t, true, dir).InstallTampered())
	})

	t.Run("disabled telemetry never reports tampering", func(t *testing.T) {
		dir, tempPath := testInstallIDBackup(t)
		require.NoError(t, os.WriteFile(tempPath, []byte("backup-id"), 0o600))
		assert.False(t, newClient(t, false, dir).InstallTampered())
	})
}

// fakeOAuthServer responds with a minimal /config endpoint and a 400
// invalid_grant on /token so the test cannot accidentally complete a flow.
func fakeOAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	return srv
}

func TestClient_TokenSource_NoImplicitBrowserFlow(t *testing.T) {
	srv := fakeOAuthServer(t)
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	var browserCalls atomic.Int32
	config.OpenBrowser = func(*url.URL) error {
		browserCalls.Add(1)
		return nil
	}

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	_, err := client.OAuthTokenSource().Token()
	require.Error(t, err)
	assert.True(t, errors.Is(err, auth.ErrNotAuthenticated),
		"expected ErrNotAuthenticated, got %v", err)
	assert.Equal(t, int32(0), browserCalls.Load(),
		"browser must not be opened when :login was not invoked")
}

func TestClient_Login_AttemptsBrowserFlow(t *testing.T) {
	srv := fakeOAuthServer(t)
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	browserCalls := make(chan struct{}, 1)
	config.OpenBrowser = func(*url.URL) error {
		select {
		case browserCalls <- struct{}{}:
		default:
		}
		return errors.New("test: browser not actually opened")
	}

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	ctx, cancel := context.WithCancel(t.Context())
	session := client.Login(ctx)

	select {
	case <-browserCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("expected browser to be opened by :login flow")
	}

	// The URL is still published so the wait prompt can show it.
	select {
	case u, ok := <-session.URL:
		require.True(t, ok, "URL channel must emit before close")
		require.NotNil(t, u, "URL channel must emit a non-nil URL")
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login to publish the OAuth URL")
	}

	// A failed browser open must NOT abort the flow: Done stays open so
	// the user can copy the URL and finish sign-in in any browser.
	select {
	case err := <-session.Done:
		t.Fatalf("Login must stay alive after browser-open failure, got Done=%v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Cancelling the context tears the flow down and resolves Done.
	cancel()
	select {
	case <-session.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login to resolve after context cancellation")
	}
}

// TestClient_Login_SecondAttemptAfterCancel reproduces the bug where
// cancelling an in-flight Login leaves the OAuth goroutine blocked
// inside blueauth.NewClientWithPorts holding the CachedTokenSource
// lock, so a subsequent Login never reaches openBrowser and never
// resolves.
func TestClient_Login_SecondAttemptAfterCancel(t *testing.T) {
	srv := fakeOAuthServer(t)
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	browserCalls := make(chan struct{}, 4)
	var browserBehavior atomic.Value
	browserBehavior.Store(func() error { return nil })
	config.OpenBrowser = func(*url.URL) error {
		browserCalls <- struct{}{}
		return browserBehavior.Load().(func() error)()
	}

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	ctx1, cancel1 := context.WithCancel(t.Context())
	session1 := client.Login(ctx1)

	select {
	case <-browserCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("expected first Login to open browser")
	}

	cancel1()

	select {
	case <-session1.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected first Login to publish completion after cancel")
	}

	browserBehavior.Store(func() error {
		return errors.New("test: browser not actually opened")
	})

	ctx2, cancel2 := context.WithCancel(t.Context())
	session2 := client.Login(ctx2)

	select {
	case <-browserCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("expected second Login to open browser after cancel of first")
	}

	// A failed browser open keeps the flow alive; cancelling resolves it.
	cancel2()
	select {
	case <-session2.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected second Login to publish completion after cancel")
	}
}

// TestClient_Login_PublishesOAuthURL verifies that the LoginSession
// surfaces the OAuth authorize URL — the same URL handed to the
// browser — so the bootstrap UI can inline it in the wait prompt.
// The URL channel is the only signal that drives the pre-swap login
// flow's "click here" link; if a refactor breaks the ctx plumbing
// that carries it from Login down into tokenSourceRefresh, the
// channel silently never resolves and the user is stuck staring at
// an empty prompt while the browser opens in the background.
func TestClient_Login_PublishesOAuthURL(t *testing.T) {
	srv := fakeOAuthServer(t)
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	browserURLCh := make(chan *url.URL, 1)
	config.OpenBrowser = func(u *url.URL) error {
		select {
		case browserURLCh <- u:
		default:
		}
		return errors.New("test: browser not actually opened")
	}

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	ctx, cancel := context.WithCancel(t.Context())
	session := client.Login(ctx)

	var published *url.URL
	select {
	case u, ok := <-session.URL:
		require.True(t, ok, "URL channel must emit before close")
		require.NotNil(t, u, "URL channel must emit a non-nil URL")
		assert.NotEmpty(t, u.String())
		assert.Contains(t, u.Query(), "state",
			"published URL must be an OAuth authorize URL (has state)")
		assert.Contains(t, u.Query(), "redirect_uri",
			"published URL must be an OAuth authorize URL (has redirect_uri)")
		published = u
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login to publish OAuth URL")
	}

	select {
	case browserURL := <-browserURLCh:
		assert.Equal(t, published.String(), browserURL.String(),
			"URL handed to openBrowser must match the URL published on session.URL")
	case <-time.After(5 * time.Second):
		t.Fatal("expected openBrowser to be invoked with the same URL")
	}

	// A failed browser open must not abort the flow; cancelling resolves it.
	cancel()
	select {
	case <-session.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login Done to resolve after context cancellation")
	}

	_, ok := <-session.URL
	assert.False(t, ok, "URL channel must be closed after Login completes")
}

// TestClient_Login_CompletesWhileBrowserOpenerBlocks reproduces the
// Linux login deadlock: the underlying OAuth client only starts
// reading the callback result after the visit-URL callback returns, so
// an opener that blocks for the lifetime of the browser process wedges
// the loopback redirect handler. The browser tab then spins forever on
// the redirect and login never completes.
func TestClient_Login_CompletesWhileBrowserOpenerBlocks(t *testing.T) {
	// Hold the callback after it delivers the code but before net/http
	// flushes the response, so token exchange wins the shutdown race.
	redirectRelease := make(chan struct{})
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetLevel(log.InfoLevel)
	logger.AddHook(&oauthRedirectCompletionHook{release: redirectRelease})
	t.Cleanup(func() {
		close(redirectRelease)
		logger.ReplaceHooks(oldHooks)
		logger.SetLevel(oldLevel)
	})

	srv := completingOAuthServer(t)
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	openerRelease := make(chan struct{})
	t.Cleanup(func() { close(openerRelease) })
	config.OpenBrowser = func(*url.URL) error {
		<-openerRelease
		return nil
	}

	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := client.Login(ctx)

	var authorizeURL *url.URL
	select {
	case u, ok := <-session.URL:
		require.True(t, ok, "URL channel must emit before close")
		require.NotNil(t, u)
		authorizeURL = u
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login to publish OAuth URL")
	}

	redirectURI := authorizeURL.Query().Get("redirect_uri")
	require.NotEmpty(t, redirectURI)
	state := authorizeURL.Query().Get("state")
	require.NotEmpty(t, state)

	callbackURL, err := url.Parse(redirectURI)
	require.NoError(t, err)
	q := callbackURL.Query()
	q.Set("code", "fake-auth-code")
	q.Set("state", state)
	callbackURL.RawQuery = q.Encode()

	// The browser tab hitting the loopback redirect must get a response
	// back even though the opener is still running.
	browserTab := &http.Client{Timeout: 5 * time.Second}
	resp, err := browserTab.Get(callbackURL.String())
	require.NoError(t, err, "loopback redirect must respond while the opener blocks")
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err, "loopback redirect must deliver the complete response")
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, callbackPageHTML, string(body))

	select {
	case err := <-session.Done:
		assert.NoError(t, err, "Login must complete after the redirect")
	case <-time.After(5 * time.Second):
		t.Fatal("expected Login to complete after the OAuth redirect")
	}
}

type oauthRedirectCompletionHook struct {
	release <-chan struct{}
}

func (*oauthRedirectCompletionHook) Levels() []log.Level {
	return []log.Level{log.InfoLevel}
}

func (h *oauthRedirectCompletionHook) Fire(entry *log.Entry) error {
	if entry.Data["class"] == "redirectHandler" && entry.Data["step"] == "success" {
		<-h.release
	}
	return nil
}

// fakeTokenJSON is a successful token response whose access token
// carries account claims, as ox-api's minted tokens do, so
// AccountStatus can read it back.
func fakeTokenJSON(t *testing.T) string {
	t.Helper()
	return `{"access_token":"` +
		makeAccountJWT(t, auth.RPCUser{Email: "fake@rune.test"}) + `",` +
		`"token_type":"Bearer","expires_in":3600,` +
		`"refresh_token":"fake-refresh-token"}`
}

// tokenReply scripts one response of the fake token endpoint.
type tokenReply struct {
	status int
	body   string
}

type fakeOAuthOptions struct {
	// withoutDeviceAuth leaves DeviceAuthURL out of the served config,
	// as an ox-api built before the device grant existed would.
	withoutDeviceAuth bool
	// tokenReplies are served in order by the token endpoint; once they
	// run out every request succeeds with fakeTokenJSON.
	tokenReplies []tokenReply
}

// completingOAuthServer serves an oauth2 config pointing back at itself
// and a token endpoint that completes the PKCE code exchange.
func completingOAuthServer(t *testing.T) *httptest.Server {
	return newFakeOAuthServer(t, fakeOAuthOptions{})
}

func newFakeOAuthServer(t *testing.T, opts fakeOAuthOptions) *httptest.Server {
	t.Helper()
	var base atomic.Value
	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(auth.ServeConfigPath, func(w http.ResponseWriter, _ *http.Request) {
		u, _ := base.Load().(string)
		cfg := auth.Config{
			APIURL:    u + "/api/v2/",
			JWKSURL:   u + "/.well-known/jwks.json",
			SignupURL: u + "/signup",
			Config: oauth2.Config{
				// An empty ClientSecret selects the PKCE flow.
				ClientID: "test-client-id",
				Scopes:   []string{"offline_access", "openid"},
				Endpoint: oauth2.Endpoint{
					AuthStyle: oauth2.AuthStyleInParams,
					AuthURL:   u + "/authorize",
					TokenURL:  u + auth.ServeTokenPath,
				},
			},
		}
		if !opts.withoutDeviceAuth {
			cfg.Endpoint.DeviceAuthURL = u + "/oauth/device/code"
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(cfg))
	})
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "test-client-id", r.Form.Get("client_id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"fake-device-code",` +
			`"user_code":"ABCD-EFGH",` +
			`"verification_uri":"https://auth.rune.test/activate",` +
			`"verification_uri_complete":` +
			`"https://auth.rune.test/activate?user_code=ABCD-EFGH",` +
			`"expires_in":900,"interval":1}`))
	})
	mux.HandleFunc(auth.ServeTokenPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := int(tokenCalls.Add(1)) - 1
		if n < len(opts.tokenReplies) {
			w.WriteHeader(opts.tokenReplies[n].status)
			_, _ = w.Write([]byte(opts.tokenReplies[n].body))
			return
		}
		_, _ = w.Write([]byte(fakeTokenJSON(t)))
	})
	srv := httptest.NewServer(mux)
	base.Store(srv.URL)
	return srv
}

func TestClient_LoginWithDeviceCode(t *testing.T) {
	pending := tokenReply{
		status: http.StatusBadRequest,
		body:   `{"error":"authorization_pending","error_description":"not yet"}`,
	}
	denied := tokenReply{
		status: http.StatusForbidden,
		body:   `{"error":"access_denied","error_description":"user said no"}`,
	}

	for _, tc := range []struct {
		name         string
		opts         fakeOAuthOptions
		wantPrompt   bool
		wantErr      error
		wantErrStr   string
		wantSignedIn bool
	}{
		{
			name:         "publishes the prompt then caches the token",
			wantPrompt:   true,
			wantSignedIn: true,
		},
		{
			// x/oauth2 keeps polling only when the relayed error body
			// names authorization_pending; anything else aborts.
			name:         "keeps polling while authorization is pending",
			opts:         fakeOAuthOptions{tokenReplies: []tokenReply{pending}},
			wantPrompt:   true,
			wantSignedIn: true,
		},
		{
			name:       "access denied resolves Done with the error",
			opts:       fakeOAuthOptions{tokenReplies: []tokenReply{denied}},
			wantPrompt: true,
			wantErrStr: "access_denied",
		},
		{
			name:    "server without a device endpoint is unsupported",
			opts:    fakeOAuthOptions{withoutDeviceAuth: true},
			wantErr: ErrDeviceLoginUnsupported,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeOAuthServer(t, tc.opts)
			defer srv.Close()

			config := DefaultConfig()
			config.HTTPEndpointAddress = srv.URL
			var browserCalls atomic.Int32
			config.OpenBrowser = func(*url.URL) error {
				browserCalls.Add(1)
				return nil
			}
			client := New(storagestub.NewInMemoryService(), config, t.TempDir())
			defer client.Close()

			session := client.LoginWithDeviceCode(t.Context())

			select {
			case prompt, ok := <-session.Prompt:
				require.Equal(t, tc.wantPrompt, ok,
					"prompt emitted=%v, want %v", ok, tc.wantPrompt)
				if ok {
					assert.Equal(t, "ABCD-EFGH", prompt.UserCode)
					assert.Equal(t, "https://auth.rune.test/activate",
						prompt.VerificationURI)
					assert.Equal(t,
						"https://auth.rune.test/activate?user_code=ABCD-EFGH",
						prompt.VerificationURIComplete)
					assert.WithinDuration(t, time.Now().Add(15*time.Minute),
						prompt.Expiry, time.Minute)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("expected the prompt channel to emit or close")
			}

			select {
			case err := <-session.Done:
				switch {
				case tc.wantErr != nil:
					assert.True(t, errors.Is(err, tc.wantErr), "got %v", err)
				case tc.wantErrStr != "":
					require.Error(t, err)
					assert.Contains(t, err.Error(), tc.wantErrStr)
				default:
					require.NoError(t, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("expected Done to resolve")
			}

			_, signedIn, err := client.AccountStatus(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tc.wantSignedIn, signedIn)
			assert.Equal(t, int32(0), browserCalls.Load(),
				"sign-in by code must never open a browser")
		})
	}
}

func TestClient_LoginWithDeviceCode_CancelWhilePolling(t *testing.T) {
	pending := tokenReply{
		status: http.StatusBadRequest,
		body:   `{"error":"authorization_pending"}`,
	}
	// Enough pending replies that the poll outlives the test's cancel.
	srv := newFakeOAuthServer(t, fakeOAuthOptions{
		tokenReplies: []tokenReply{pending, pending, pending, pending, pending,
			pending, pending, pending, pending, pending, pending, pending},
	})
	defer srv.Close()

	config := DefaultConfig()
	config.HTTPEndpointAddress = srv.URL
	client := New(storagestub.NewInMemoryService(), config, t.TempDir())
	defer client.Close()

	ctx, cancel := context.WithCancel(t.Context())
	session := client.LoginWithDeviceCode(ctx)

	select {
	case _, ok := <-session.Prompt:
		require.True(t, ok, "expected a prompt before cancelling")
	case <-time.After(5 * time.Second):
		t.Fatal("expected the prompt to be published")
	}

	cancel()
	select {
	case err := <-session.Done:
		assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("expected Done to resolve after cancellation")
	}
	_, ok := <-session.Prompt
	assert.False(t, ok, "Prompt must be closed once the session resolves")

	_, signedIn, err := client.AccountStatus(t.Context())
	require.NoError(t, err)
	assert.False(t, signedIn)
}

func TestParseAccountClaims(t *testing.T) {
	planEnds := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	token := makeAccountJWT(t, auth.RPCUser{
		ID:       "auth0|abc",
		Email:    "user@example.com",
		Role:     auth.RolePaid,
		Account:  "acc-1",
		PlanEnds: planEnds,
	})

	user, err := parseAccountClaims(token)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", user.Email)
	assert.Equal(t, auth.RolePaid, user.Role)
	assert.Equal(t, "acc-1", user.Account)
	assert.True(t, planEnds.Equal(user.PlanEnds))
}

func TestParseAccountClaimsRejectsMalformedToken(t *testing.T) {
	_, err := parseAccountClaims("not-a-jwt")
	require.Error(t, err)
}

func makeAccountJWT(t *testing.T, user auth.RPCUser) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(struct {
		Extra auth.RPCUser `json:"extra"`
	}{Extra: user})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestNetworkCredentials(t *testing.T) {
	tsuite := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantURL    string
		wantKey    string
		wantErrStr string
	}{
		{
			name:    "entitled account",
			status:  http.StatusOK,
			body:    `{"control_url":"https://control.example.com","auth_key":"tskey-1"}`,
			wantURL: "https://control.example.com",
			wantKey: "tskey-1",
		},
		{
			name:    "signed out",
			status:  http.StatusUnauthorized,
			wantErr: auth.ErrNotAuthenticated,
		},
		{
			name:    "no plan",
			status:  http.StatusForbidden,
			body:    auth.SubscriptionRequiredMessage + "\n",
			wantErr: ErrSubscriptionRequired,
		},
		{
			// The account may use the network; it just has as many
			// machines on it as its plan covers.
			name:    "machine allowance spent",
			status:  http.StatusForbidden,
			body:    auth.MachineLimitMessage + "\n",
			wantErr: ErrMachineLimit,
		},
		{
			name:    "payment required",
			status:  http.StatusPaymentRequired,
			wantErr: ErrSubscriptionRequired,
		},
		{
			// blueauth answers 403 for every auth failure and prod
			// 403s the whole /api/ subtree when the endpoint is not
			// deployed, so a bare 403 must never read as "buy a
			// plan" (an entitled admin was prompted to upgrade).
			name:       "forbidden without the paid-gate message",
			status:     http.StatusForbidden,
			body:       "<!doctype html><title>403</title>403 Forbidden",
			wantErrStr: "status 403",
		},
		{
			name:       "server error",
			status:     http.StatusInternalServerError,
			wantErrStr: "status 500",
		},
		{
			name:       "incomplete response",
			status:     http.StatusOK,
			body:       `{"control_url":"https://control.example.com"}`,
			wantErrStr: "incomplete response",
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			var gotPath, gotMethod, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					gotPath, gotMethod = r.URL.Path, r.Method
					gotAuth = r.Header.Get("Authorization")
					w.WriteHeader(tcase.status)
					_, _ = w.Write([]byte(tcase.body))
				}))
			defer srv.Close()

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)
			client := &Client{
				httpEndpointURL: u,
				tokenSource:     newValidTestTokenSource(),
			}

			controlURL, authKey, err := client.NetworkCredentials(context.Background())
			assert.Equal(t, "/api/network/credentials", gotPath)
			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, "Bearer test-token", gotAuth)
			switch {
			case tcase.wantErr != nil:
				assert.True(t, errors.Is(err, tcase.wantErr), "got %v", err)
			case tcase.wantErrStr != "":
				require.Error(t, err)
				assert.Contains(t, err.Error(), tcase.wantErrStr)
			default:
				require.NoError(t, err)
				assert.Equal(t, tcase.wantURL, controlURL)
				assert.Equal(t, tcase.wantKey, authKey)
			}
		})
	}
}

func TestNetworkMachines(t *testing.T) {
	lastSeen := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotMethod = r.URL.Path, r.Method
			_, _ = w.Write([]byte(`{"machines":[` +
				`{"id":"1","hostname":"laptop","last_seen":"2026-03-01T12:00:00Z",` +
				`"online":true}]}`))
		}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	client := &Client{httpEndpointURL: u, tokenSource: newValidTestTokenSource()}

	machines, err := client.NetworkMachines(context.Background())
	require.NoError(t, err)
	assert.Equal(t, auth.NetworkNodesPath, gotPath)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, []Machine{
		{ID: "1", Hostname: "laptop", LastSeen: lastSeen, Online: true},
	}, machines)
}

func TestNetworkMachineRemove(t *testing.T) {
	tsuite := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"removed", http.StatusNoContent, nil},
		{"machine of another account", http.StatusNotFound, ErrMachineNotFound},
		{"signed out", http.StatusUnauthorized, auth.ErrNotAuthenticated},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			var gotPath, gotMethod, gotBody string
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					gotPath, gotMethod = r.URL.Path, r.Method
					body, _ := io.ReadAll(r.Body)
					gotBody = string(body)
					w.WriteHeader(tcase.status)
				}))
			defer srv.Close()

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)
			client := &Client{
				httpEndpointURL: u,
				tokenSource:     newValidTestTokenSource(),
			}

			err = client.NetworkMachineRemove(context.Background(), "7")
			assert.Equal(t, auth.NetworkNodeRemovePath, gotPath)
			assert.Equal(t, http.MethodPost, gotMethod)
			assert.JSONEq(t, `{"node_id":"7"}`, gotBody)
			if tcase.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tcase.wantErr), "got %v", err)
		})
	}
}
