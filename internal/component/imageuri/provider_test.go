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

package imageuri

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

var (
	testRed  = color.RGBA{R: 255, A: 255}
	testBlue = color.RGBA{B: 255, A: 255}
)

// failingStorage fails Get and/or Set and otherwise delegates.
type failingStorage struct {
	storageapi.Service
	getErr, setErr error
}

func (s failingStorage) Get(ctx context.Context, id string, doc any) error {
	if s.getErr != nil {
		return s.getErr
	}
	return s.Service.Get(ctx, id, doc)
}

func (s failingStorage) Set(ctx context.Context, id string, doc any) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.Service.Set(ctx, id, doc)
}

func pixel(t *testing.T, img image.Image) color.RGBA {
	t.Helper()
	rgba, ok := img.(*image.RGBA)
	require.True(t, ok, "got %T, want *image.RGBA", img)
	return rgba.RGBAAt(0, 0)
}

func TestCachedProviderHTTP(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const ttl = time.Hour
	redPNG := encodePNG(t, solid(1, 1, testRed))
	bluePNG := encodePNG(t, solid(1, 1, testBlue))
	fresh := &cacheDoc{Data: redPNG, FetchedAt: now, ExpiresAt: now.Add(time.Minute)}
	expired := &cacheDoc{Data: redPNG, FetchedAt: now.Add(-2 * ttl), ExpiresAt: now}
	storageErr := errors.New("storage down")

	tests := []struct {
		name     string
		cached   *cacheDoc
		wrap     func(storageapi.Service) storageapi.Service
		status   int
		body     []byte
		maxBytes int64

		wantErr      string
		wantColor    color.RGBA
		wantRequests int32
		wantStored   []byte
	}{
		{
			name:         "miss fetches and stores",
			status:       http.StatusOK,
			body:         bluePNG,
			wantColor:    testBlue,
			wantRequests: 1,
			wantStored:   bluePNG,
		},
		{
			name:         "fresh hit skips the server",
			cached:       fresh,
			status:       http.StatusOK,
			body:         bluePNG,
			wantColor:    testRed,
			wantRequests: 0,
			wantStored:   redPNG,
		},
		{
			name:         "expired entry is refetched",
			cached:       expired,
			status:       http.StatusOK,
			body:         bluePNG,
			wantColor:    testBlue,
			wantRequests: 1,
			wantStored:   bluePNG,
		},
		{
			name:         "expired entry is served when the server fails",
			cached:       expired,
			status:       http.StatusInternalServerError,
			wantColor:    testRed,
			wantRequests: 1,
			wantStored:   redPNG,
		},
		{
			name:         "expired entry is served when the body is not an image",
			cached:       expired,
			status:       http.StatusOK,
			body:         []byte("<html>"),
			wantColor:    testRed,
			wantRequests: 1,
			wantStored:   redPNG,
		},
		{
			name:         "corrupt fresh entry is a miss",
			cached:       &cacheDoc{Data: []byte("junk"), ExpiresAt: now.Add(ttl)},
			status:       http.StatusOK,
			body:         bluePNG,
			wantColor:    testBlue,
			wantRequests: 1,
			wantStored:   bluePNG,
		},
		{
			name:         "corrupt entry is not served when the server fails",
			cached:       &cacheDoc{Data: []byte("junk"), ExpiresAt: now.Add(ttl)},
			status:       http.StatusInternalServerError,
			wantErr:      "500 Internal Server Error",
			wantRequests: 1,
		},
		{
			name:         "no entry and not found",
			status:       http.StatusNotFound,
			wantErr:      "404 Not Found",
			wantRequests: 1,
		},
		{
			name:         "body over the limit",
			status:       http.StatusOK,
			body:         bluePNG,
			maxBytes:     int64(len(bluePNG) - 1),
			wantErr:      "larger than",
			wantRequests: 1,
		},
		{
			name:   "storage failures fall through to fetching",
			cached: fresh,
			wrap: func(s storageapi.Service) storageapi.Service {
				return failingStorage{Service: s, getErr: storageErr, setErr: storageErr}
			},
			status:       http.StatusOK,
			body:         bluePNG,
			wantColor:    testBlue,
			wantRequests: 1,
			wantStored:   redPNG,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.WriteHeader(tt.status)
					_, _ = w.Write(tt.body)
				}))
			defer srv.Close()
			uri, err := url.Parse(srv.URL + "/img.png")
			require.NoError(t, err)

			ctx := t.Context()
			mem := storagestub.NewInMemoryService()
			id := cacheID(uri.String())
			if tt.cached != nil {
				doc := *tt.cached
				doc.URI = uri.String()
				require.NoError(t, mem.Set(ctx, id, doc))
			}
			var storage storageapi.Service = mem
			if tt.wrap != nil {
				storage = tt.wrap(mem)
			}
			p := newCachedProvider(storage, ttl)
			p.client = srv.Client()
			p.now = func() time.Time { return now }
			if tt.maxBytes > 0 {
				p.maxBytes = tt.maxBytes
			}

			img, err := p.image(ctx, uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantColor, pixel(t, img))
			}
			assert.Equal(t, tt.wantRequests, requests.Load())

			var stored cacheDoc
			err = mem.Get(ctx, id, &stored)
			if tt.wantStored == nil {
				if tt.cached == nil {
					assert.ErrorIs(t, err, storageapi.ErrNotFound)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, uri.String(), stored.URI)
			assert.Equal(t, tt.wantStored, stored.Data)
		})
	}
}

func TestCachedProviderStoresExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const ttl = 90 * time.Minute
	path := filepath.Join(t.TempDir(), "img.png")
	require.NoError(t, os.WriteFile(path, encodePNG(t, solid(1, 1, testRed)), 0o600))
	uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}

	mem := storagestub.NewInMemoryService()
	p := newCachedProvider(mem, ttl)
	p.now = func() time.Time { return now }
	_, err := p.image(t.Context(), uri)
	require.NoError(t, err)

	var stored cacheDoc
	require.NoError(t, mem.Get(t.Context(), cacheID(uri.String()), &stored))
	assert.True(t, now.Equal(stored.FetchedAt), "FetchedAt = %s", stored.FetchedAt)
	assert.True(t, now.Add(ttl).Equal(stored.ExpiresAt), "ExpiresAt = %s", stored.ExpiresAt)

	// The entry is still fresh a minute before it expires, so removing
	// the file must not matter.
	require.NoError(t, os.Remove(path))
	p.now = func() time.Time { return now.Add(ttl - time.Minute) }
	img, err := p.image(t.Context(), uri)
	require.NoError(t, err)
	assert.Equal(t, testRed, pixel(t, img))
}

func TestCachedProviderFile(t *testing.T) {
	dir := t.TempDir()
	data := encodePNG(t, solid(1, 1, testBlue))
	path := filepath.Join(dir, "img.png")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	tests := []struct {
		name     string
		path     string
		maxBytes int64
		wantErr  string
	}{
		{name: "existing file", path: path},
		{name: "missing file", path: filepath.Join(dir, "missing.png"), wantErr: "missing.png"},
		{name: "file over the limit", path: path, maxBytes: int64(len(data) - 1), wantErr: "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newCachedProvider(storagestub.NewInMemoryService(), time.Hour)
			if tt.maxBytes > 0 {
				p.maxBytes = tt.maxBytes
			}
			uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(tt.path)}
			img, err := p.image(t.Context(), uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, testBlue, pixel(t, img))
		})
	}
}
