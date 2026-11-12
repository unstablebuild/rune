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

package agentools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keenableServer serves handler and returns a provider pointed at it.
func keenableServer(t *testing.T, apiKey string, handler http.HandlerFunc) *KeenableSearch {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	k := NewKeenableSearch(apiKey)
	k.baseURL = srv.URL
	k.client = srv.Client()
	return k
}

func TestKeenableSearchRequest(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		wantPath   string
		wantAPIKey string
	}{
		{name: "without a key uses the public endpoint", wantPath: "/v1/search/public"},
		{name: "with a key uses the keyed endpoint", apiKey: "kn-test", wantPath: "/v1/search", wantAPIKey: "kn-test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotTitle, gotKey, gotType string
			var gotBody map[string]any
			k := keenableServer(t, tt.apiKey, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotTitle = r.Header.Get("X-Keenable-Title")
				gotKey = r.Header.Get("X-API-Key")
				gotType = r.Header.Get("Content-Type")
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &gotBody)
				_, _ = io.WriteString(w, `{"results":[]}`)
			})
			_, err := k.Search(t.Context(), WebSearchQuery{Query: "go generics", MaxResults: 3, Site: "go.dev"})
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, gotPath)
			assert.Equal(t, "Rune", gotTitle)
			assert.Equal(t, tt.wantAPIKey, gotKey)
			assert.Equal(t, "application/json", gotType)
			assert.Equal(t, map[string]any{
				"query":              "go generics",
				"max_results":        float64(3),
				"site":               "go.dev",
				"snippet_max_length": float64(webSearchMaxSnippetBytes),
			}, gotBody)
		})
	}
}

func TestKeenableSearchOmitsUnsetSite(t *testing.T) {
	var gotBody map[string]any
	k := keenableServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = io.WriteString(w, `{"results":[]}`)
	})
	_, err := k.Search(t.Context(), WebSearchQuery{Query: "q", MaxResults: 5})
	require.NoError(t, err)
	assert.NotContains(t, gotBody, "site")
}

func TestKeenableSearchMapsResults(t *testing.T) {
	k := keenableServer(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"query":"q","results":[
			{"title":"Go","url":"https://go.dev","description":"","snippet":"Build simple,\nsecure software.","acquired_at":"2026-10-01T00:00:00Z"},
			{"title":"No URL","url":"","description":"","snippet":"dropped"}
		]}`)
	})
	results, err := k.Search(t.Context(), WebSearchQuery{Query: "q", MaxResults: 5})
	require.NoError(t, err)
	assert.Equal(t, []WebSearchResult{
		{Title: "Go", URL: "https://go.dev", Snippet: "Build simple,\nsecure software."},
	}, results)
}

func TestKeenableSearchErrors(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "bad request carries the API's message",
			status:  http.StatusBadRequest,
			body:    `{"error":"Invalid parameter","message":"\"max_results\": must be an integer between 1 and 50"}`,
			wantErr: `HTTP 400: "max_results": must be an integer between 1 and 50`,
		},
		{
			name:    "rate limit without a key suggests one",
			status:  http.StatusTooManyRequests,
			body:    `{"error":"Too many requests"}`,
			wantErr: "HTTP 429: Too many requests (set KEENABLE_API_KEY for higher rate limits)",
		},
		{
			name:    "rate limit with a key",
			apiKey:  "kn-test",
			status:  http.StatusTooManyRequests,
			body:    `{"error":"Too many requests"}`,
			wantErr: "HTTP 429: Too many requests",
		},
		{
			name:    "rejected key",
			apiKey:  "kn-bad",
			status:  http.StatusUnauthorized,
			body:    `{"error":"Unauthorized","message":"Invalid API key"}`,
			wantErr: "HTTP 401: Invalid API key (check KEENABLE_API_KEY)",
		},
		{
			name:    "body that is not JSON",
			status:  http.StatusBadGateway,
			body:    `<html>bad gateway</html>`,
			wantErr: "HTTP 502",
		},
		{
			name:    "malformed success",
			status:  http.StatusOK,
			body:    `{"results":`,
			wantErr: "parse response",
		},
		{
			name:    "response over the size limit",
			status:  http.StatusOK,
			body:    `{"results":[` + strings.Repeat(" ", keenableMaxResponseBytes) + `]}`,
			wantErr: "response is larger than 1048576 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := keenableServer(t, tt.apiKey, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := k.Search(t.Context(), WebSearchQuery{Query: "q", MaxResults: 5})
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestKeenableSearchHonorsContext(t *testing.T) {
	started := make(chan struct{})
	k := keenableServer(t, "", func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := k.Search(ctx, WebSearchQuery{Query: "q", MaxResults: 5})
		errc <- err
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the server")
	}
	cancel()
	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Search did not return after the context was canceled")
	}
}
