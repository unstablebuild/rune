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

//go:build e2e

package ociregistry_test

// End-to-end tests that hit the real Hugging Face endpoint. These run by
// default because the small-footprint assertions (manifest fetch + 4 KiB
// range read) only transfer a few kilobytes. Set OCIREGISTRY_SKIP_E2E=1 to
// opt out — e.g. when running offline or in CI without outbound network.
//
// An optional OCIREGISTRY_E2E_FULL_PULL=1 flag enables a further test that
// downloads the entire model weight blob (~800 MiB for the default Llama
// 3.2 1B reference); this is off by default because it is slow.

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

// hfTestReference is a small, community-maintained GGUF repo on HF. The
// repository is public, the tag is stable, and the Q4_K_M blob is ~800 MiB —
// small enough to pull in a reasonable amount of time when we opt into the
// full pull test, while exercising every interesting bit of the client
// (HTTPS, Bearer-less CDN redirect, Range, 206, large stream).
const (
	hfTestReference = "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M"

	// GGUF_MAGIC is the magic number at the start of every GGUF file.
	// We read just the first 4 bytes of the weight blob to verify the
	// Range request round-tripped correctly.
	ggufMagic = "GGUF"
)

func skipIfE2EDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv("OCIREGISTRY_SKIP_E2E") != "" {
		t.Skip("OCIREGISTRY_SKIP_E2E set; skipping Hugging Face E2E test")
	}
}

// hfClient returns an ociregistry.Client wired for Hugging Face, honoring
// $HF_TOKEN when present so rate limits are lifted and so the test can cover
// private-repo flows on CI machines that have a token configured.
func hfClient(t *testing.T) *ociregistry.Client {
	t.Helper()
	c := ociregistry.NewClient()
	c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	if token := strings.TrimSpace(os.Getenv("HF_TOKEN")); token != "" {
		c.WithHFToken("hf.co", token)
		t.Logf("HF_TOKEN detected — using authenticated requests")
	}
	return c
}

func TestHuggingFace_FetchManifest(t *testing.T) {
	skipIfE2EDisabled(t)
	c := hfClient(t)

	ref, err := ociregistry.ParseReference(hfTestReference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m, err := c.FetchManifest(ctx, ref)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.SchemaVersion != 2 {
		t.Fatalf("schemaVersion: got %d want 2", m.SchemaVersion)
	}
	model := m.FindLayer(ociregistry.MediaTypeOllamaModel)
	if model == nil {
		t.Fatalf("manifest has no %s layer: %+v", ociregistry.MediaTypeOllamaModel, m.Layers)
	}
	if !strings.HasPrefix(model.Digest, "sha256:") || len(model.Digest) != len("sha256:")+64 {
		t.Fatalf("malformed model digest %q", model.Digest)
	}
	if model.Size <= 0 {
		t.Fatalf("model layer has non-positive size %d", model.Size)
	}
	t.Logf("manifest: model digest=%s size=%d", model.Digest, model.Size)
}

func TestHuggingFace_RangeReadVerifiesGGUFMagic(t *testing.T) {
	skipIfE2EDisabled(t)
	c := hfClient(t)

	ref, err := ociregistry.ParseReference(hfTestReference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := c.FetchManifest(ctx, ref)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	model := m.FindLayer(ociregistry.MediaTypeOllamaModel)
	if model == nil {
		t.Fatalf("no model layer in manifest")
	}

	// Read just the first few KiB — enough to see the GGUF magic + version
	// without pulling the whole 800 MiB blob.
	//
	// Note: BlobReader only requests `bytes=start-`; the server decides the
	// end. This still exercises the 206 path because HF's CDN answers with
	// a 206 for any Range request. We cap the read below with io.LimitReader.
	rc, total, err := c.BlobReader(ctx, ref, model.Digest, 0)
	if err != nil {
		t.Fatalf("BlobReader: %v", err)
	}
	defer func() { _ = rc.Close() }()

	if total != 0 && total != model.Size {
		t.Fatalf("BlobReader total %d does not match manifest Size %d", total, model.Size)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if string(buf) != ggufMagic {
		t.Fatalf("GGUF magic: got %q (%s) want %q", buf, hex.EncodeToString(buf), ggufMagic)
	}
	t.Logf("verified %s on %d-byte blob", ggufMagic, model.Size)
}

func TestHuggingFace_FullPull(t *testing.T) {
	skipIfE2EDisabled(t)
	if os.Getenv("OCIREGISTRY_E2E_FULL_PULL") == "" {
		t.Skip("OCIREGISTRY_E2E_FULL_PULL not set; skipping full-pull E2E test")
	}

	c := hfClient(t)
	c.HTTPClient = &http.Client{Timeout: 0} // unlimited per-request for the big blob

	ref, err := ociregistry.ParseReference(hfTestReference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var lastReport time.Time
	res, err := c.Pull(ctx, cache, ref, func(digest string, downloaded, total int64) {
		if time.Since(lastReport) < 2*time.Second {
			return
		}
		lastReport = time.Now()
		if total > 0 {
			t.Logf("pull %s: %d / %d bytes (%.1f%%)",
				shortDigest(digest),
				downloaded, total,
				float64(downloaded)/float64(total)*100)
		} else {
			t.Logf("pull %s: %d bytes", shortDigest(digest), downloaded)
		}
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.ModelPath == "" {
		t.Fatal("expected ModelPath to be populated")
	}
	// Verify the file starts with the GGUF magic.
	f, err := os.Open(res.ModelPath)
	if err != nil {
		t.Fatalf("open pulled file: %v", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(f, buf); err != nil {
		t.Fatalf("read pulled magic: %v", err)
	}
	if string(buf) != ggufMagic {
		t.Fatalf("pulled file is not GGUF (magic=%q)", buf)
	}
	t.Logf("full pull succeeded: %s", res.ModelPath)
}

func shortDigest(d string) string {
	return fmt.Sprintf("%s…", d[:min(len(d), 19)])
}
