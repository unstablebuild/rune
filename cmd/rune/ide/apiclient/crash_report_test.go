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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"golang.org/x/oauth2"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/crashreport"
)

// newValidTestTokenSource creates a CachedTokenSource backed by a static
// oauth2 token, so that Token() returns a valid token for test assertions.
func newValidTestTokenSource() *auth.CachedTokenSource {
	ts := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "test-token",
		TokenType:   "Bearer",
	})
	return auth.NewCachedTokenSource(
		auth.FuncTokenSourcer(func(_ context.Context, _ *oauth2.Token) (oauth2.TokenSource, error) {
			return ts, nil
		}),
		storagestub.NewInMemoryService(),
	)
}

func TestPostReport_Success(t *testing.T) {
	var receivedBody []byte
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reports" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != reportContentType {
			t.Errorf("unexpected content-type: %s", ct)
		}
		receivedAuth = r.Header.Get("Authorization")
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := &Client{
		httpEndpointURL: u,
		tokenSource:     newValidTestTokenSource(),
	}

	payload := crashreport.Payload{YAML: []byte("package: testpkg\nversion: v1.0.0\n")}

	err := client.PostReport(context.Background(), payload)
	if err != nil {
		t.Fatalf("PostReport: %v", err)
	}

	if string(receivedBody) != string(payload.YAML) {
		t.Errorf("body = %q, want %q", string(receivedBody), string(payload.YAML))
	}
	if receivedAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer test-token")
	}
}

func TestPostReport_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := &Client{
		httpEndpointURL: u,
		tokenSource:     newValidTestTokenSource(),
	}

	err := client.PostReport(context.Background(), crashreport.Payload{YAML: []byte("package: test\n")})
	if err == nil {
		t.Fatal("expected error for non-success status")
	}
}

func TestPostReport_SendsAnonymouslyWithoutToken(t *testing.T) {
	cases := []struct {
		name    string
		sourcer auth.TokenSourcer
	}{
		{
			name: "not logged in",
			sourcer: auth.FuncTokenSourcer(func(context.Context, *oauth2.Token) (oauth2.TokenSource, error) {
				return nil, auth.ErrNotAuthenticated
			}),
		},
		{
			name: "token refresh fails",
			sourcer: auth.FuncTokenSourcer(func(context.Context, *oauth2.Token) (oauth2.TokenSource, error) {
				return nil, errors.New("auth server unreachable")
			}),
		},
		{name: "no token sourcer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var receivedAuth []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuth = r.Header.Values("Authorization")
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()

			u, _ := url.Parse(srv.URL)
			client := &Client{
				httpEndpointURL: u,
				tokenSource: auth.NewCachedTokenSource(
					tc.sourcer, storagestub.NewInMemoryService()),
			}

			err := client.PostReport(context.Background(),
				crashreport.Payload{YAML: []byte("package: test\n")})
			if err != nil {
				t.Fatalf("PostReport: %v", err)
			}
			if len(receivedAuth) != 0 {
				t.Errorf("Authorization = %q, want none", receivedAuth)
			}
		})
	}
}

func TestPostReport_FallsBackToUnauthenticatedOn403(t *testing.T) {
	var authCount, unauthCount int
	var unauthBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reports" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		ct := r.Header.Get("Content-Type")
		if ct != reportContentType {
			t.Errorf("unexpected content-type: %s", ct)
		}
		if r.Header.Get("Authorization") != "" {
			authCount++
		} else {
			unauthCount++
			var err error
			unauthBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
		}
		// First request (with auth) gets 403 to trigger fallback
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusForbidden)
		} else {
			// Second request (unauthenticated) gets 201 to succeed
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := &Client{
		httpEndpointURL: u,
		tokenSource:     newValidTestTokenSource(),
	}

	payload := crashreport.Payload{YAML: []byte("package: testpkg\n")}

	err := client.PostReport(context.Background(), payload)
	if err != nil {
		t.Fatalf("PostReport: expected retry to succeed on 403->2xx, got: %v", err)
	}

	if authCount != 1 {
		t.Errorf("auth request count = %d, want 1", authCount)
	}
	if unauthCount != 1 {
		t.Errorf("unauth request count = %d, want 1", unauthCount)
	}
	if string(unauthBody) != string(payload.YAML) {
		t.Errorf("unauth body = %q, want %q", string(unauthBody), string(payload.YAML))
	}
}

func TestPostReport_FallbackAlsoFails(t *testing.T) {
	var authCount, unauthCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authCount++
		} else {
			unauthCount++
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := &Client{
		httpEndpointURL: u,
		tokenSource:     newValidTestTokenSource(),
	}

	err := client.PostReport(context.Background(), crashreport.Payload{YAML: []byte("package: testpkg\n")})
	if err == nil {
		t.Fatal("expected error when both authenticated and unauthenticated requests fail")
	}

	if authCount != 1 {
		t.Errorf("auth request count = %d, want 1", authCount)
	}
	if unauthCount != 1 {
		t.Errorf("unauth request count = %d, want 1", unauthCount)
	}

	expected := "report upload (unauth): status 403"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}
