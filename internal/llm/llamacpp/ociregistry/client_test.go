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

package ociregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"oras.land/oras-go/v2/registry/remote/retry"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

// newTestClient returns a Client wired to talk to the given httptest server
// over plaintext HTTP with short retry delays so test assertions fire
// quickly on failure.
func newTestClient(_ *testing.T, srv *httptest.Server) *ociregistry.Client {
	c := ociregistry.NewClient()
	c.PlainHTTP = true
	// Wrap the test server's transport in oras-go's retry transport so
	// transient 5xx responses are retried the same way they would be
	// against a real registry.
	base := srv.Client().Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c.HTTPClient = &http.Client{Transport: retry.NewTransport(base)}
	return c
}

func hostOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u.Host
}

// fakeRegistry implements the surface of an OCI v2 registry we care about.
// Handlers are safe for concurrent use.
type fakeRegistry struct {
	manifest   []byte
	manifestCT string
	blobs      map[string][]byte
	tags       *ociregistry.TagList

	// auth, if non-empty, requires callers to send this Authorization
	// header. If absent, the first challengeLeft requests receive a 401
	// with a Bearer challenge pointing at authRealm.
	auth          string
	authRealm     string
	challengeLeft int32
	lastAuth      string

	// failMode="flaky" makes the first blob request fail with 500.
	failMode  string
	flakyHits int32
}

func (fr *fakeRegistry) manifestHandler(w http.ResponseWriter, r *http.Request) {
	fr.lastAuth = r.Header.Get("Authorization")
	if fr.auth != "" && r.Header.Get("Authorization") != fr.auth {
		if atomic.AddInt32(&fr.challengeLeft, -1) >= 0 {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+fr.authRealm+`",service="reg",scope="repository:foo:pull"`)
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ct := fr.manifestCT
	if ct == "" {
		ct = ociregistry.MediaTypeDockerManifestV2
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(fr.manifest)
}

func (fr *fakeRegistry) blobHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	digest := parts[len(parts)-1]
	data, ok := fr.blobs[digest]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if fr.failMode == "flaky" && atomic.AddInt32(&fr.flakyHits, 1) == 1 {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}

	rangeHdr := r.Header.Get("Range")
	if rangeHdr == "" {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(data)
		return
	}

	// Accept only "bytes=N-" or "bytes=N-M".
	spec := strings.TrimPrefix(rangeHdr, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	start, err := strconv.ParseInt(spec[:dash], 10, 64)
	if err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	end := int64(len(data)) - 1
	if rest := spec[dash+1:]; rest != "" {
		e, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		end = e
	}
	if start >= int64(len(data)) {
		http.Error(w, "oob", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[start : end+1])
}

func (fr *fakeRegistry) tagsHandler(w http.ResponseWriter, _ *http.Request) {
	if fr.tags == nil {
		http.Error(w, "no tags", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(fr.tags)
}

func runFakeRegistry(t *testing.T, fr *fakeRegistry) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			fr.manifestHandler(w, r)
		case strings.Contains(r.URL.Path, "/blobs/"):
			fr.blobHandler(w, r)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			fr.tagsHandler(w, r)
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// buildManifest constructs an OCI manifest whose layers point at the given
// digest→content map. The returned JSON is what the fake registry should
// serve in response to a manifest GET.
func buildManifest(t *testing.T, blobs map[string][]byte) []byte {
	t.Helper()
	var layers []ociregistry.Descriptor
	for digest, data := range blobs {
		layers = append(layers, ociregistry.Descriptor{
			MediaType: ociregistry.MediaTypeOllamaModel,
			Digest:    digest,
			Size:      int64(len(data)),
		})
	}
	m := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Config: ociregistry.Descriptor{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Digest:    digestOf([]byte("cfg")),
			Size:      3,
		},
		Layers: layers,
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

// ---- actual tests ---------------------------------------------------------

func TestClient_Ping(t *testing.T) {
	srv := runFakeRegistry(t, &fakeRegistry{})
	c := newTestClient(t, srv)
	if err := c.Ping(context.Background(), hostOf(t, srv)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestClient_FetchManifest(t *testing.T) {
	data := []byte("hello world")
	blobs := map[string][]byte{digestOf(data): data}
	mData := buildManifest(t, blobs)

	srv := runFakeRegistry(t, &fakeRegistry{manifest: mData, blobs: blobs})
	c := newTestClient(t, srv)

	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	m, err := c.FetchManifest(context.Background(), ref)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.SchemaVersion != 2 || len(m.Layers) != 1 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if len(m.Raw) == 0 {
		t.Fatal("expected Raw to be populated")
	}
}

func TestClient_PullAndResolve_SurfaceMMProjPath(t *testing.T) {
	model := []byte("weights")
	mmproj := []byte("projector")
	modelDigest := digestOf(model)
	mmprojDigest := digestOf(mmproj)
	manifest := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Config: ociregistry.Descriptor{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Digest:    digestOf([]byte("cfg")),
			Size:      3,
		},
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaProjector, Digest: mmprojDigest, Size: int64(len(mmproj))},
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: modelDigest, Size: int64(len(model))},
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	fr := &fakeRegistry{
		manifest: manifestJSON,
		blobs: map[string][]byte{
			modelDigest:  model,
			mmprojDigest: mmproj,
		},
	}
	srv := runFakeRegistry(t, fr)
	c := newTestClient(t, srv)
	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	ref, err := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	if err != nil {
		t.Fatalf("ParseReferenceWithDefault: %v", err)
	}

	pr, err := c.Pull(context.Background(), cache, ref, nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if pr.ModelPath == "" {
		t.Fatal("expected ModelPath")
	}
	if pr.MMProjPath == "" {
		t.Fatal("expected MMProjPath")
	}
	resolved, err := ociregistry.Resolve(cache, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.MMProjPath == "" {
		t.Fatal("expected MMProjPath on resolved result")
	}
	if resolved.ModelPath == "" {
		t.Fatal("expected ModelPath on resolved result")
	}
}

func TestClient_ListTags_NotImplemented(t *testing.T) {
	srv := runFakeRegistry(t, &fakeRegistry{})
	c := newTestClient(t, srv)
	_, err := c.ListTags(context.Background(), hostOf(t, srv), "foo/bar")
	if err != ociregistry.ErrNotImplemented {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}

func TestClient_ListTags_OK(t *testing.T) {
	fr := &fakeRegistry{tags: &ociregistry.TagList{Name: "foo/bar", Tags: []string{"a", "b"}}}
	srv := runFakeRegistry(t, fr)
	c := newTestClient(t, srv)
	tags, err := c.ListTags(context.Background(), hostOf(t, srv), "foo/bar")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags.Tags) != 2 || tags.Tags[1] != "b" {
		t.Fatalf("unexpected tags: %+v", tags)
	}
}

func TestClient_PullAndResolve(t *testing.T) {
	body := bytes.Repeat([]byte("gguf-data-"), 200)
	digest := digestOf(body)
	blobs := map[string][]byte{digest: body}
	mData := buildManifest(t, blobs)

	srv := runFakeRegistry(t, &fakeRegistry{manifest: mData, blobs: blobs})
	c := newTestClient(t, srv)

	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}

	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))

	var progressCalls int32
	res, err := c.Pull(context.Background(), cache, ref, func(_ string, downloaded, total int64) {
		atomic.AddInt32(&progressCalls, 1)
		if total > 0 && downloaded > total {
			t.Errorf("downloaded=%d > total=%d", downloaded, total)
		}
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.ModelPath == "" {
		t.Fatal("expected ModelPath to be populated for MediaTypeOllamaModel layer")
	}
	if atomic.LoadInt32(&progressCalls) == 0 {
		t.Fatal("expected progress callback to fire")
	}
	got, err := os.ReadFile(res.ModelPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if digestOf(got) != digest {
		t.Fatal("stored blob digest mismatch")
	}

	res2, err := ociregistry.Resolve(cache, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res2.ModelPath != res.ModelPath {
		t.Fatalf("Resolve mismatch: %q vs %q", res2.ModelPath, res.ModelPath)
	}
}

func TestClient_DownloadResumesFromPartial(t *testing.T) {
	body := []byte(strings.Repeat("A", 1024))
	digest := digestOf(body)
	blobs := map[string][]byte{digest: body}
	mData := buildManifest(t, blobs)

	srv := runFakeRegistry(t, &fakeRegistry{manifest: mData, blobs: blobs})
	c := newTestClient(t, srv)

	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	// Simulate a previous, interrupted download.
	writePartialBlob(t, cache, digest, body[:300])

	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	desc := ociregistry.Descriptor{
		MediaType: ociregistry.MediaTypeOllamaModel,
		Digest:    digest,
		Size:      int64(len(body)),
	}
	if err := c.DownloadBlob(context.Background(), cache, ref, desc, nil); err != nil {
		t.Fatalf("DownloadBlob: %v", err)
	}

	path, err := cache.BlobPath(digest)
	if err != nil {
		t.Fatalf("BlobPath: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("content mismatch (got %d bytes)", len(got))
	}
}

func TestClient_RetriesTransientFailure(t *testing.T) {
	body := []byte("short")
	digest := digestOf(body)
	blobs := map[string][]byte{digest: body}
	mData := buildManifest(t, blobs)

	fr := &fakeRegistry{manifest: mData, blobs: blobs, failMode: "flaky"}
	srv := runFakeRegistry(t, fr)
	c := newTestClient(t, srv)

	cache, _ := ociregistry.OpenCache(t.TempDir())
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	desc := ociregistry.Descriptor{
		MediaType: ociregistry.MediaTypeOllamaModel,
		Digest:    digest,
		Size:      int64(len(body)),
	}

	start := time.Now()
	if err := c.DownloadBlob(context.Background(), cache, ref, desc, nil); err != nil {
		t.Fatalf("DownloadBlob: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("retry took too long: %s", time.Since(start))
	}
	if got := atomic.LoadInt32(&fr.flakyHits); got < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", got)
	}
}

func TestClient_BearerChallengeFlow(t *testing.T) {
	body := []byte("protected-blob")
	digest := digestOf(body)
	blobs := map[string][]byte{digest: body}
	mData := buildManifest(t, blobs)

	var tokenHits int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		if r.URL.Query().Get("service") != "reg" {
			http.Error(w, "bad service", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "T0K3N",
			"expires_in": 60,
		})
	}))
	t.Cleanup(tokenSrv.Close)

	fr := &fakeRegistry{
		manifest:      mData,
		blobs:         blobs,
		auth:          "Bearer T0K3N",
		authRealm:     tokenSrv.URL + "/token",
		challengeLeft: 1,
	}
	srv := runFakeRegistry(t, fr)
	c := newTestClient(t, srv)

	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	m, err := c.FetchManifest(context.Background(), ref)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.SchemaVersion != 2 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if atomic.LoadInt32(&tokenHits) == 0 {
		t.Fatal("expected token endpoint to be hit")
	}
}

func TestClient_StaticBearer(t *testing.T) {
	fr := &fakeRegistry{
		manifest:      buildManifest(t, map[string][]byte{digestOf([]byte("x")): []byte("x")}),
		blobs:         map[string][]byte{},
		auth:          "Bearer STATIC",
		challengeLeft: 0,
	}
	srv := runFakeRegistry(t, fr)
	c := newTestClient(t, srv)
	c.WithHFToken(hostOf(t, srv), "STATIC")

	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	if _, err := c.FetchManifest(context.Background(), ref); err != nil {
		t.Fatalf("FetchManifest: %v (last Authorization header: %q)", err, fr.lastAuth)
	}
}

func TestClient_FetchManifest_NotGGUF_MapsToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_INVALID","message":"repo is not GGUF"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	_, err := c.FetchManifest(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error on 400 manifest response")
	}
	if !errors.Is(err, ociregistry.ErrNotGGUFRepo) {
		t.Fatalf("expected ErrNotGGUFRepo, got %v", err)
	}
}

func TestClient_FetchManifest_400_ShardedGGUF(t *testing.T) {
	const hfMsg = "The specified repository contains sharded GGUF. " +
		"Ollama does not support this yet."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("x-error-message", hfMsg)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"` + hfMsg + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	_, err := c.FetchManifest(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ociregistry.ErrShardedGGUF) {
		t.Fatalf("expected ErrShardedGGUF, got %v", err)
	}
	// The guidance must not leak HF's Ollama-centric wording to the user.
	if strings.Contains(strings.ToLower(err.Error()), "ollama") {
		t.Fatalf("error should not mention Ollama, got %q", err)
	}
	if !strings.Contains(err.Error(), "single-file quantization") {
		t.Fatalf("error %q is missing actionable guidance", err)
	}
}

func TestClient_FetchManifest_404_MapsToNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/manifests/"):
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	_, err := c.FetchManifest(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error on 404 manifest response")
	}
	if !errors.Is(err, ociregistry.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestClient_Pull_ReportsCumulativeProgressAcrossLayers(t *testing.T) {
	big := bytes.Repeat([]byte("M"), 4096)
	tpl := []byte("tpl")
	params := []byte("{}")
	bigDig, tplDig, paramsDig := digestOf(big), digestOf(tpl), digestOf(params)

	m := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Config: ociregistry.Descriptor{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Digest:    digestOf([]byte("cfg")), Size: 3,
		},
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: bigDig, Size: int64(len(big))},
			{MediaType: ociregistry.MediaTypeOllamaTemplate, Digest: tplDig, Size: int64(len(tpl))},
			{MediaType: ociregistry.MediaTypeOllamaParams, Digest: paramsDig, Size: int64(len(params))},
		},
	}
	mData, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	totalBytes := int64(len(big) + len(tpl) + len(params))

	srv := runFakeRegistry(t, &fakeRegistry{
		manifest: mData,
		blobs:    map[string][]byte{bigDig: big, tplDig: tpl, paramsDig: params},
	})
	c := newTestClient(t, srv)
	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))

	type sample struct{ downloaded, total int64 }
	var samples []sample
	_, err = c.Pull(context.Background(), cache, ref,
		func(_ string, downloaded, total int64) {
			samples = append(samples, sample{downloaded, total})
		},
	)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("no progress samples")
	}

	// Every sample must report the whole-manifest total — not a
	// per-layer total — and `downloaded` must be monotonically
	// non-decreasing across samples.
	var prev int64
	for i, s := range samples {
		if s.total != totalBytes {
			t.Fatalf("sample[%d].total=%d, want manifest total %d",
				i, s.total, totalBytes)
		}
		if s.downloaded < prev {
			t.Fatalf("sample[%d].downloaded=%d regressed from %d",
				i, s.downloaded, prev)
		}
		prev = s.downloaded
	}
	last := samples[len(samples)-1]
	if last.downloaded != totalBytes {
		t.Fatalf("final sample downloaded=%d, want %d", last.downloaded, totalBytes)
	}
}

func TestClient_Pull_CumulativeWithSomeLayersCached(t *testing.T) {
	big := bytes.Repeat([]byte("M"), 4096)
	tpl := []byte("tpl")
	bigDig, tplDig := digestOf(big), digestOf(tpl)

	m := ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Config: ociregistry.Descriptor{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Digest:    digestOf([]byte("cfg")), Size: 3,
		},
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: bigDig, Size: int64(len(big))},
			{MediaType: ociregistry.MediaTypeOllamaTemplate, Digest: tplDig, Size: int64(len(tpl))},
		},
	}
	mData, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	totalBytes := int64(len(big) + len(tpl))

	srv := runFakeRegistry(t, &fakeRegistry{
		manifest: mData,
		blobs:    map[string][]byte{bigDig: big, tplDig: tpl},
	})
	c := newTestClient(t, srv)
	cache, err := ociregistry.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}

	// Pre-populate the big layer to simulate a post-ctrl-c resume.
	ref, _ := ociregistry.ParseReferenceWithDefault("foo/bar:latest", hostOf(t, srv))
	bigDesc := ociregistry.Descriptor{
		MediaType: ociregistry.MediaTypeOllamaModel,
		Digest:    bigDig, Size: int64(len(big)),
	}
	if err := c.DownloadBlob(context.Background(), cache, ref, bigDesc, nil); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var lastDownloaded, lastTotal int64
	_, err = c.Pull(context.Background(), cache, ref,
		func(_ string, downloaded, total int64) {
			lastDownloaded, lastTotal = downloaded, total
		},
	)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if lastTotal != totalBytes {
		t.Fatalf("progress total=%d, want whole-manifest %d", lastTotal, totalBytes)
	}
	if lastDownloaded != totalBytes {
		t.Fatalf("progress downloaded=%d, want %d", lastDownloaded, totalBytes)
	}
}
