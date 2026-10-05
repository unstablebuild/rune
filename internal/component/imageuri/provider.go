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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
)

const (
	// defaultMaxBytes bounds the encoded size of an image, both for
	// what is read from its source and for what is cached.
	defaultMaxBytes    = 16 << 22
	defaultHTTPTimeout = 30 * time.Second
)

// cacheDoc is the storage record for the encoded bytes of one URI.
type cacheDoc struct {
	URI       string
	Data      []byte
	FetchedAt time.Time
	ExpiresAt time.Time
}

// cachedProvider fetches images and caches their encoded bytes for ttl.
// Storage failures only cost a refetch, so they are logged and never
// surfaced.
type cachedProvider struct {
	storage  storageapi.Service
	ttl      time.Duration
	client   *http.Client
	now      func() time.Time
	maxBytes int64
}

func newCachedProvider(
	storage storageapi.Service, ttl time.Duration,
) *cachedProvider {
	return &cachedProvider{
		storage:  storage,
		ttl:      ttl,
		client:   &http.Client{Timeout: defaultHTTPTimeout},
		now:      time.Now,
		maxBytes: defaultMaxBytes,
	}
}

func cacheID(uri string) string {
	sum := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(sum[:])
}

func (p *cachedProvider) image(
	ctx context.Context, uri *url.URL,
) (image.Image, error) {
	key := uri.String()
	id := cacheID(key)

	stale := p.lookup(ctx, id, key)
	if stale != nil && p.now().Before(stale.ExpiresAt) {
		img, err := decode(stale.Data)
		if err == nil {
			return img, nil
		}
		slog.Debug("imageuri: discard corrupt cache entry",
			"uri", uri.Redacted(), "error", err)
		stale = nil
	}

	data, err := p.fetch(ctx, uri)
	var img image.Image
	if err == nil {
		img, err = decode(data)
	}
	if err == nil {
		p.store(ctx, id, key, data)
		return img, nil
	}
	if ctx.Err() != nil || stale == nil {
		return nil, err
	}

	img, derr := decode(stale.Data)
	if derr != nil {
		return nil, err
	}
	slog.Debug("imageuri: serve expired entry after refresh failed",
		"uri", uri.Redacted(), "error", err)
	return img, nil
}

// lookup returns the cached record for key, or nil if there is none.
func (p *cachedProvider) lookup(
	ctx context.Context, id, key string,
) *cacheDoc {
	var doc cacheDoc
	err := p.storage.Get(ctx, id, &doc)
	if err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			slog.Warn("imageuri: read cache", "error", err)
		}
		return nil
	}
	if doc.URI != key {
		return nil
	}
	return &doc
}

func (p *cachedProvider) store(
	ctx context.Context, id, key string, data []byte,
) {
	now := p.now()
	doc := cacheDoc{
		URI:       key,
		Data:      data,
		FetchedAt: now,
		ExpiresAt: now.Add(p.ttl),
	}
	if err := p.storage.Set(ctx, id, doc); err != nil {
		slog.Warn("imageuri: write cache", "error", err)
	}
}

func (p *cachedProvider) fetch(
	ctx context.Context, uri *url.URL,
) ([]byte, error) {
	switch uri.Scheme {
	case "file":
		return p.readFile(uri)
	case "http", "https":
		return p.get(ctx, uri)
	default:
		return nil, fmt.Errorf("unsupported scheme %q", uri.Scheme)
	}
}

func (p *cachedProvider) readFile(uri *url.URL) ([]byte, error) {
	f, err := os.Open(filepath.FromSlash(uri.Path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, p.maxBytes, uri.Redacted())
}

func (p *cachedProvider) get(
	ctx context.Context, uri *url.URL,
) ([]byte, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, uri.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %s", uri.Redacted(), resp.Status)
	}
	if resp.ContentLength > p.maxBytes {
		return nil, errTooLarge(uri.Redacted(), p.maxBytes)
	}
	return readLimited(resp.Body, p.maxBytes, uri.Redacted())
}

func readLimited(r io.Reader, maxBytes int64, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errTooLarge(name, maxBytes)
	}
	return data, nil
}

func errTooLarge(name string, maxBytes int64) error {
	return fmt.Errorf("%s is larger than %d bytes", name, maxBytes)
}
