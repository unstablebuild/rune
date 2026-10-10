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

package llamacpp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

func TestWrapPullError(t *testing.T) {
	t.Parallel()
	ref := Reference{Host: "hf.co", Repository: "owner/repo", Tag: "latest"}
	other := errors.New("network blew up")
	cases := []struct {
		name    string
		in      error
		want    error  // exact identity for pass-through and nil
		wantSub string // substring for wrapped errors
	}{
		{name: "nil passes through", in: nil, want: nil},
		{name: "passes through unknown errors", in: other, want: other},
		{
			name:    "ErrNotGGUFRepo wrapped with -GGUF hint",
			in:      ociregistry.ErrNotGGUFRepo,
			wantSub: "-GGUF",
		},
		{
			name:    "ErrNotFound wrapped with host",
			in:      ociregistry.ErrNotFound,
			wantSub: "hf.co",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapPullError(tc.in, ref)
			if tc.want != nil || tc.in == nil {
				if got != tc.want {
					t.Fatalf("wrapPullError(%v) = %v, want %v", tc.in, got, tc.want)
				}
				return
			}
			if got == nil {
				t.Fatalf("wrapPullError(%v) returned nil, want wrapped", tc.in)
			}
			if !strings.Contains(got.Error(), tc.wantSub) {
				t.Fatalf("wrapPullError(%v) = %q, want substring %q", tc.in, got, tc.wantSub)
			}
		})
	}
}

func TestModelDigest(t *testing.T) {
	t.Parallel()
	t.Run("nil PullResult", func(t *testing.T) {
		if got := modelDigest(nil); got != "" {
			t.Fatalf("modelDigest(nil) = %q, want empty", got)
		}
	})
	t.Run("nil Manifest", func(t *testing.T) {
		pr := &ociregistry.PullResult{}
		if got := modelDigest(pr); got != "" {
			t.Fatalf("modelDigest(empty) = %q, want empty", got)
		}
	})
	t.Run("manifest without model layer", func(t *testing.T) {
		pr := &ociregistry.PullResult{Manifest: &ociregistry.Manifest{
			Layers: []ociregistry.Descriptor{
				{MediaType: ociregistry.MediaTypeOllamaProjector, Digest: "sha256:proj"},
			},
		}}
		if got := modelDigest(pr); got != "" {
			t.Fatalf("modelDigest(no model) = %q, want empty", got)
		}
	})
	t.Run("returns model digest", func(t *testing.T) {
		pr := &ociregistry.PullResult{Manifest: &ociregistry.Manifest{
			Layers: []ociregistry.Descriptor{
				{MediaType: ociregistry.MediaTypeOllamaModel, Digest: "sha256:abc"},
			},
		}}
		if got := modelDigest(pr); got != "sha256:abc" {
			t.Fatalf("modelDigest = %q, want sha256:abc", got)
		}
	})
}

// buildMiniGGUF returns a path to a minimal but well-formed GGUF v3 header
// that exposes general.architecture=llama and llama.context_length=ctx so
// readGGUFContextLength succeeds. Used to drive the registry's
// real-blob metadata path (refreshContextWindow → storeContextWindow →
// loadContextWindow) end to end without depending on a multi-GiB model.
func buildMiniGGUF(t *testing.T, ctx uint32) []byte {
	t.Helper()
	b := newGGUFBuilder(t)
	b.setNKV(2)
	b.writeKVString("general.architecture", "llama")
	b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
		binary.Write(buf, binary.LittleEndian, ctx)
	})
	return b.buf.Bytes()
}

func TestRegistry_ContextWindow_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	storage := storagestub.NewInMemoryService()
	t.Cleanup(func() { _ = storage.Close() })

	r1, err := NewRegistry(dir, WithStorage(storage))
	if err != nil {
		t.Fatalf("NewRegistry 1: %v", err)
	}

	// Hand-craft an Ollama-style cache layout with a real GGUF blob.
	body := buildMiniGGUF(t, 12345)
	digest := digestSHA256(body)
	blobDir := filepath.Join(r1.Root(), "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "sha256-"+digest[len("sha256:"):]), body, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	mDir := filepath.Join(r1.Root(), "manifests", "hf.co", "owner", "real-GGUF")
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatalf("mkdir manifests: %v", err)
	}
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",` +
		`"layers":[{"mediaType":"application/vnd.ollama.image.model","digest":"` + digest +
		`","size":` + itoa(len(body)) + `}]}`)
	if err := os.WriteFile(filepath.Join(mDir, "latest"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	e, ok := r1.Get(context.Background(), "hf.co/owner/real-GGUF:latest")
	if !ok {
		t.Fatal("Get: not found")
	}
	if e.ContextWindow != 12345 {
		t.Fatalf("ContextWindow = %d, want 12345 from mini GGUF", e.ContextWindow)
	}

	// Persistent record is now in storage; a fresh Registry on the same
	// root must see the cached value without re-reading the file.
	r2, err := NewRegistry(dir, WithStorage(storage))
	if err != nil {
		t.Fatalf("NewRegistry 2: %v", err)
	}
	if got, found := r2.loadContextWindow(digest); !found || got != 12345 {
		t.Fatalf("loadContextWindow(%s) = (%d, %v), want (12345, true)", digest, got, found)
	}
}

// digestSHA256 hashes b with sha256 and returns the canonical "sha256:<hex>" form.
func digestSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func itoa(n int) string { return strconv.Itoa(n) }
