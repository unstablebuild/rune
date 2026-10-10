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

package llamacpp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

// digestBytes returns the sha256: digest used by the OCI cache layout.
func digestBytes(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// seedCachedModel writes a manifest + weight blob into the registry's
// underlying OCI cache so Models()/Get() treat the entry as fully
// pulled. `body` is the raw blob bytes; its digest becomes the
// manifest's model layer digest so callers can compose tests that
// check context-window-by-digest behaviour.
func seedCachedModel(
	t *testing.T, reg *llamacpp.Registry, host, repo, tag string, body []byte,
) string {
	t.Helper()
	digest := digestBytes(body)
	// Cache is hidden behind Registry, so write through the disk
	// layout directly: blobs live under <root>/blobs/sha256-<hex> and
	// manifests under <root>/manifests/<host>/<repo...>/<target>.
	root := reg.Root()
	blobDir := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	// Invert the Cache.BlobPath mapping (sha256:… -> sha256-…).
	blobName := "sha256-" + digest[len("sha256:"):]
	if err := os.WriteFile(filepath.Join(blobDir, blobName), body, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	mDir := filepath.Join(root, "manifests", host, repo)
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatalf("mkdir manifests: %v", err)
	}
	manifest := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: digest, Size: int64(len(body))},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mDir, tag), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return digest
}

func seedCachedModelWithProjector(
	t *testing.T,
	reg *llamacpp.Registry,
	host, repo, tag string,
	modelBody, projectorBody []byte,
) (string, string) {
	t.Helper()
	modelDigest := digestBytes(modelBody)
	projectorDigest := digestBytes(projectorBody)
	root := reg.Root()
	blobDir := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	modelBlobName := "sha256-" + modelDigest[len("sha256:"):]
	if err := os.WriteFile(filepath.Join(blobDir, modelBlobName), modelBody, 0o644); err != nil {
		t.Fatalf("write model blob: %v", err)
	}
	projectorBlobName := "sha256-" + projectorDigest[len("sha256:"):]
	if err := os.WriteFile(filepath.Join(blobDir, projectorBlobName), projectorBody, 0o644); err != nil {
		t.Fatalf("write projector blob: %v", err)
	}
	mDir := filepath.Join(root, "manifests", host, repo)
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatalf("mkdir manifests: %v", err)
	}
	manifest := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaProjector, Digest: projectorDigest, Size: int64(len(projectorBody))},
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: modelDigest, Size: int64(len(modelBody))},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mDir, tag), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return modelDigest, projectorDigest
}

type fakeDownloader struct {
	pull func(context.Context, *ociregistry.Cache, ociregistry.Reference, ociregistry.ProgressFunc) (*ociregistry.PullResult, error)
}

func (f fakeDownloader) Pull(
	ctx context.Context,
	cache *ociregistry.Cache,
	ref ociregistry.Reference,
	progress ociregistry.ProgressFunc,
) (*ociregistry.PullResult, error) {
	return f.pull(ctx, cache, ref, progress)
}

func collect(t *testing.T, it iterator.Iterator[llmapi.ModelEntry]) []llmapi.ModelEntry {
	t.Helper()
	out, err := iterator.ToSlice(context.Background(), it)
	if err != nil {
		t.Fatalf("to slice: %v", err)
	}
	return out
}

func TestRegistry_EmptyCacheReturnsNoModels(t *testing.T) {
	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := collect(t, r.Models()); len(got) != 0 {
		t.Fatalf("expected 0, got %d: %+v", len(got), got)
	}
}

func TestRegistry_EmptyRootRejected(t *testing.T) {
	if _, err := llamacpp.NewRegistry(""); err == nil {
		t.Fatal("expected an error for empty root")
	}
}

func TestRegistry_ListsSeededManifests(t *testing.T) {
	r, err := llamacpp.NewRegistry(
		t.TempDir(),
		llamacpp.WithContextWindow(4096),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModel(t, r, "hf.co", "owner/a-GGUF", "latest", []byte("weights-a"))
	seedCachedModel(t, r, "hf.co", "owner/b-GGUF", "latest", []byte("weights-b"))

	got := collect(t, r.Models())
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %+v", len(got), got)
	}
	wantNames := []string{
		"hf.co/owner/a-GGUF:latest",
		"hf.co/owner/b-GGUF:latest",
	}
	for i, e := range got {
		if e.Name != wantNames[i] {
			t.Errorf("entry[%d].Name = %q, want %q", i, e.Name, wantNames[i])
		}
		if e.Provider != llamacpp.LLMProvider {
			t.Errorf("entry[%d].Provider = %q, want %q", i, e.Provider, llamacpp.LLMProvider)
		}
		if e.ContextWindow != 4096 {
			t.Errorf("entry[%d].ContextWindow = %d, want 4096", i, e.ContextWindow)
		}
		if e.BaseURL == "" {
			t.Errorf("entry[%d] missing BaseURL", i)
		}
	}
}

func TestRegistry_GetByRegistryName(t *testing.T) {
	r, err := llamacpp.NewRegistry(
		t.TempDir(),
		llamacpp.WithContextWindow(4096),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModel(t, r, "hf.co", "owner/foo", "latest", []byte("weights"))
	e, ok := r.Get(context.Background(), "hf.co/owner/foo:latest")
	if !ok {
		t.Fatal("Get: not found")
	}
	if e.ContextWindow != 4096 {
		t.Fatalf("ContextWindow = %d, want 4096", e.ContextWindow)
	}
}

func TestRegistry_SurfacesProjectorPath(t *testing.T) {
	r, err := llamacpp.NewRegistry(
		t.TempDir(),
		llamacpp.WithContextWindow(4096),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModelWithProjector(t, r, "hf.co", "owner/mm", "latest", []byte("weights"), []byte("projector"))

	e, ok := r.Get(context.Background(), "hf.co/owner/mm:latest")
	if !ok {
		t.Fatal("Get: not found")
	}
	if e.ProjectorPath == "" {
		t.Fatal("expected ProjectorPath to be populated")
	}
	if e.BaseURL == "" {
		t.Fatal("expected BaseURL model path to be populated")
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, ok := r.Get(context.Background(), "nope"); ok {
		t.Fatal("expected Get to return false for missing model")
	}
}

func TestRegistry_FallsBackToDefaultWhenMetadataReadFails(t *testing.T) {
	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModel(t, r, "hf.co", "owner/broken", "latest", []byte("not-a-gguf"))

	e, ok := r.Get(context.Background(), "hf.co/owner/broken:latest")
	if !ok {
		t.Fatal("expected Get to succeed")
	}
	if e.ContextWindow != 8192 {
		t.Fatalf("ContextWindow = %d, want default 8192", e.ContextWindow)
	}
}

func TestRegistry_WithContextWindowOverridesMetadata(t *testing.T) {
	r, err := llamacpp.NewRegistry(
		t.TempDir(),
		llamacpp.WithContextWindow(1234),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModel(t, r, "hf.co", "owner/m", "latest", []byte("weights"))
	e, ok := r.Get(context.Background(), "hf.co/owner/m:latest")
	if !ok {
		t.Fatal("Get: not found")
	}
	if e.ContextWindow != 1234 {
		t.Fatalf("ContextWindow = %d, want override 1234", e.ContextWindow)
	}
}

func TestRegistry_DeleteRemovesManifestBlobAndStorageEntry(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	t.Cleanup(func() { _ = storage.Close() })
	r, err := llamacpp.NewRegistry(
		t.TempDir(),
		llamacpp.WithStorage(storage),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	digest := seedCachedModel(t, r, "hf.co", "owner/keep", "latest", []byte("gguf-weights"))

	// Force the registry to populate the persistent store by asking
	// for a listing — it will fall back to the GGUF parser (fails on
	// our fake blob) and then to the default, which is not cached.
	// Instead we drive the cache via a Get, then overwrite the
	// entry with a sentinel directly so the subsequent Delete has
	// something to clear. The storage key shape is an
	// implementation detail of the registry but this test exists
	// precisely to pin the invariant.
	storageID := "sha256-" + digest[len("sha256:"):]
	type rec struct {
		ContextWindow int `json:"context_window"`
	}
	if err := storage.Set(context.Background(), storageID,
		rec{ContextWindow: 16384}); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
	// Sanity: the seeded record is retrievable.
	var probe rec
	if err := storage.Get(context.Background(), storageID, &probe); err != nil {
		t.Fatalf("probe seeded storage: %v", err)
	}
	if probe.ContextWindow != 16384 {
		t.Fatalf("seeded record lost field: %+v", probe)
	}

	ref, err := llamacpp.ParseReference("hf.co/owner/keep:latest")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if err := r.Delete(context.Background(), ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Model disappears from the listing.
	if got := collect(t, r.Models()); len(got) != 0 {
		t.Fatalf("expected empty after delete, got %+v", got)
	}

	// Persistent cache entry is gone.
	var gone rec
	if err := storage.Get(context.Background(), storageID, &gone); err == nil {
		t.Fatalf("expected storage entry removed, got %+v", gone)
	}
}

func TestRegistry_DeleteKeepsBlobReferencedByAnotherManifest(t *testing.T) {
	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	body := []byte("shared-weights")
	seedCachedModel(t, r, "hf.co", "owner/a", "latest", body)
	seedCachedModel(t, r, "hf.co", "owner/b", "latest", body)

	refA, _ := llamacpp.ParseReference("hf.co/owner/a:latest")
	if err := r.Delete(context.Background(), refA); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	// B must still list because it shares the blob.
	got := collect(t, r.Models())
	if len(got) != 1 || got[0].Name != "hf.co/owner/b:latest" {
		t.Fatalf("expected only B after deleting A, got %+v", got)
	}
	// And its weights must still open.
	if _, err := os.Stat(got[0].BaseURL); err != nil {
		t.Fatalf("shared blob missing after sibling delete: %v", err)
	}
}

func TestRegistry_DeleteMissingReferenceReturnsNotExist(t *testing.T) {
	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ref, _ := llamacpp.ParseReference("hf.co/owner/nope:latest")
	err = r.Delete(context.Background(), ref)
	if !os.IsNotExist(err) {
		t.Fatalf("Delete missing: got %v, want os.ErrNotExist", err)
	}
}

func TestRegistry_PersistentContextWindowSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	storage := storagestub.NewInMemoryService()
	t.Cleanup(func() { _ = storage.Close() })

	r1, err := llamacpp.NewRegistry(dir, llamacpp.WithStorage(storage))
	if err != nil {
		t.Fatalf("NewRegistry 1: %v", err)
	}
	digest := seedCachedModel(t, r1, "hf.co", "owner/m", "latest", []byte("fake-gguf"))
	// Seed the storage entry to simulate a prior successful metadata
	// read (the blob isn't a real GGUF here).
	storageID := "sha256-" + digest[len("sha256:"):]
	type rec struct {
		ContextWindow int
	}
	if err := storage.Set(context.Background(), storageID,
		rec{ContextWindow: 32768}); err != nil {
		t.Fatalf("seed storage: %v", err)
	}

	r2, err := llamacpp.NewRegistry(dir, llamacpp.WithStorage(storage))
	if err != nil {
		t.Fatalf("NewRegistry 2: %v", err)
	}
	e, ok := r2.Get(context.Background(), "hf.co/owner/m:latest")
	if !ok {
		t.Fatal("Get: not found")
	}
	if e.ContextWindow != 32768 {
		t.Fatalf("ContextWindow = %d, want 32768 (persistent)", e.ContextWindow)
	}
}

func TestRegistry_ParseReference_DefaultModelsDir_AndRoot(t *testing.T) {
	t.Run("ParseReference defaults host", func(t *testing.T) {
		ref, err := llamacpp.ParseReference("unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M")
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		if ref.Host != llamacpp.DefaultRegistryHost {
			t.Fatalf("Host = %q, want %q", ref.Host, llamacpp.DefaultRegistryHost)
		}
		if ref.Repository != "unsloth/gemma-3n-E2B-it-GGUF" {
			t.Fatalf("Repository = %q", ref.Repository)
		}
		if ref.Tag != "Q4_K_M" {
			t.Fatalf("Tag = %q, want Q4_K_M", ref.Tag)
		}
	})

	t.Run("ParseReference preserves explicit host", func(t *testing.T) {
		ref, err := llamacpp.ParseReference("hf.co/owner/repo:latest")
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		if ref.Host != "hf.co" {
			t.Fatalf("Host = %q, want hf.co", ref.Host)
		}
	})

	t.Run("ParseReference rejects empty", func(t *testing.T) {
		if _, err := llamacpp.ParseReference(""); err == nil {
			t.Fatal("ParseReference(empty): want error")
		}
	})

	t.Run("DefaultModelsDir respects XDG", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "/tmp/rune-xdg")
		got := llamacpp.DefaultModelsDir()
		want := filepath.Join("/tmp/rune-xdg", "llama", "models")
		if got != want {
			t.Fatalf("DefaultModelsDir = %q, want %q", got, want)
		}
	})

	t.Run("DefaultModelsDir falls back to home", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got := llamacpp.DefaultModelsDir()
		want := filepath.Join(home, ".local", "share", "llama", "models")
		if got != want {
			t.Fatalf("DefaultModelsDir = %q, want %q", got, want)
		}
	})

	t.Run("Root returns constructor root", func(t *testing.T) {
		root := t.TempDir()
		r, err := llamacpp.NewRegistry(root)
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		if got := r.Root(); got != root {
			t.Fatalf("Root = %q, want %q", got, root)
		}
	})
}

func TestRegistry_Download_TableDriven(t *testing.T) {
	t.Parallel()

	mkManifest := func(modelDigest string, extra ...ociregistry.Descriptor) *ociregistry.Manifest {
		layers := append([]ociregistry.Descriptor{{MediaType: ociregistry.MediaTypeOllamaModel, Digest: modelDigest, Size: 123}}, extra...)
		return &ociregistry.Manifest{SchemaVersion: 2, MediaType: ociregistry.MediaTypeDockerManifestV2, Layers: layers}
	}

	tests := []struct {
		name       string
		ref        llamacpp.Reference
		pull       func(context.Context, *ociregistry.Cache, ociregistry.Reference, ociregistry.ProgressFunc) (*ociregistry.PullResult, error)
		wantErr    string
		assertions func(t *testing.T, got *llamacpp.DownloadResult, progressCalls [][2]int64)
	}{
		{
			name: "hostless ref defaults host and returns model path",
			ref:  llamacpp.Reference{Repository: "u/r", Tag: "latest"},
			pull: func(_ context.Context, cache *ociregistry.Cache, ref ociregistry.Reference, _ ociregistry.ProgressFunc) (*ociregistry.PullResult, error) {
				if ref.Host != llamacpp.DefaultRegistryHost {
					return nil, errors.New("default host not applied")
				}
				return &ociregistry.PullResult{Reference: ref, Manifest: mkManifest("sha256:" + strings.Repeat("a", 64)), ModelPath: "/tmp/model.gguf", Cache: cache}, nil
			},
			assertions: func(t *testing.T, got *llamacpp.DownloadResult, _ [][2]int64) {
				if got.ModelPath != "/tmp/model.gguf" {
					t.Fatalf("ModelPath = %q", got.ModelPath)
				}
				if got.Reference.Host != llamacpp.DefaultRegistryHost {
					t.Fatalf("Reference.Host = %q, want %q", got.Reference.Host, llamacpp.DefaultRegistryHost)
				}
			},
		},
		{
			name: "progress callback is forwarded",
			ref:  llamacpp.Reference{Host: "hf.co", Repository: "u/r", Tag: "latest"},
			pull: func(_ context.Context, cache *ociregistry.Cache, ref ociregistry.Reference, progress ociregistry.ProgressFunc) (*ociregistry.PullResult, error) {
				if progress == nil {
					return nil, errors.New("progress callback was nil")
				}
				progress("sha256:any", 7, 9)
				return &ociregistry.PullResult{Reference: ref, Manifest: mkManifest("sha256:" + strings.Repeat("d", 64)), ModelPath: "/tmp/model.gguf", Cache: cache}, nil
			},
			assertions: func(t *testing.T, got *llamacpp.DownloadResult, progressCalls [][2]int64) {
				if got.ModelPath != "/tmp/model.gguf" {
					t.Fatalf("ModelPath = %q", got.ModelPath)
				}
				if len(progressCalls) != 1 || progressCalls[0] != [2]int64{7, 9} {
					t.Fatalf("progressCalls = %#v, want [[7 9]]", progressCalls)
				}
			},
		},
		{
			name: "projector and extra layers are surfaced",
			ref:  llamacpp.Reference{Host: "hf.co", Repository: "u/r", Tag: "latest"},
			pull: func(_ context.Context, cache *ociregistry.Cache, ref ociregistry.Reference, _ ociregistry.ProgressFunc) (*ociregistry.PullResult, error) {
				root := cache.Root()
				blobDir := filepath.Join(root, "blobs")
				if err := os.MkdirAll(blobDir, 0o755); err != nil {
					return nil, err
				}
				templateDigest := "sha256:" + strings.Repeat("b", 64)
				templateBlob := filepath.Join(blobDir, "sha256-"+strings.Repeat("b", 64))
				if err := os.WriteFile(templateBlob, []byte("template"), 0o644); err != nil {
					return nil, err
				}
				return &ociregistry.PullResult{
					Reference: ref,
					Manifest: mkManifest(
						"sha256:"+strings.Repeat("a", 64),
						ociregistry.Descriptor{MediaType: ociregistry.MediaTypeOllamaProjector, Digest: "sha256:" + strings.Repeat("c", 64), Size: 55},
						ociregistry.Descriptor{MediaType: ociregistry.MediaTypeOllamaTemplate, Digest: templateDigest, Size: 8},
					),
					ModelPath:  "/tmp/model.gguf",
					MMProjPath: "/tmp/mmproj.gguf",
					Cache:      cache,
				}, nil
			},
			assertions: func(t *testing.T, got *llamacpp.DownloadResult, _ [][2]int64) {
				if got.ProjectorPath != "/tmp/mmproj.gguf" {
					t.Fatalf("ProjectorPath = %q", got.ProjectorPath)
				}
				if len(got.LayerSizes) != 2 {
					t.Fatalf("len(LayerSizes) = %d, want 2", len(got.LayerSizes))
				}
				if got.LayerSizes[0].MediaType != ociregistry.MediaTypeOllamaProjector {
					t.Fatalf("LayerSizes[0].MediaType = %q", got.LayerSizes[0].MediaType)
				}
				if got.LayerSizes[1].MediaType != ociregistry.MediaTypeOllamaTemplate {
					t.Fatalf("LayerSizes[1].MediaType = %q", got.LayerSizes[1].MediaType)
				}
				if got.LayerSizes[1].Path == "" {
					t.Fatal("expected non-empty path for template layer")
				}
			},
		},
		{
			name: "not found is wrapped",
			ref:  llamacpp.Reference{Host: "hf.co", Repository: "missing/repo", Tag: "latest"},
			pull: func(_ context.Context, _ *ociregistry.Cache, _ ociregistry.Reference, _ ociregistry.ProgressFunc) (*ociregistry.PullResult, error) {
				return nil, ociregistry.ErrNotFound
			},
			wantErr: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := llamacpp.NewRegistry(t.TempDir(), llamacpp.WithDownloader(fakeDownloader{pull: tc.pull}))
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			var progressCalls [][2]int64
			got, err := r.Download(context.Background(), tc.ref, func(downloaded, total int64) {
				progressCalls = append(progressCalls, [2]int64{downloaded, total})
			})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Download() error = nil, want contains %q", tc.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
					t.Fatalf("Download() error = %q, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Download(): %v", err)
			}
			if tc.assertions != nil {
				tc.assertions(t, got, progressCalls)
			}
		})
	}
}

func TestRegistry_CachedReferences_SortedAndCompleteOnly(t *testing.T) {
	t.Parallel()

	r, err := llamacpp.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	seedCachedModel(t, r, "hf.co", "owner/z-GGUF", "latest", []byte("weights-z"))
	seedCachedModel(t, r, "hf.co", "owner/a-GGUF", "Q4_K_M", []byte("weights-a"))

	got, err := r.CachedReferences()
	if err != nil {
		t.Fatalf("CachedReferences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(CachedReferences) = %d, want 2", len(got))
	}
	if got[0].String() != "hf.co/owner/a-GGUF:Q4_K_M" {
		t.Fatalf("got[0] = %q", got[0].String())
	}
	if got[1].String() != "hf.co/owner/z-GGUF:latest" {
		t.Fatalf("got[1] = %q", got[1].String())
	}
}
