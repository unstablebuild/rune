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

package llmsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
	"unstable.build/rune/internal/workspace"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const testModel = "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0"

var waitTimeout = 30 * time.Second

// syncTick runs scheduled callbacks inline.
var syncTick = func(fn func()) bool { fn(); return true }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// recordingNotifications captures notifications.
type recordingNotifications struct {
	mu       sync.Mutex
	notified []string
	once     map[string]bool
	updates  []string
}

var _ browserapi.Notifications = (*recordingNotifications)(nil)

func (n *recordingNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = append(n.notified, fmt.Sprintf(msg, args...))
	return fmt.Sprintf("n%d", len(n.notified)), nil
}

func (n *recordingNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	text := fmt.Sprintf(msg, args...)
	if n.once == nil {
		n.once = map[string]bool{}
	}
	if n.once[text] {
		n.mu.Unlock()
		return "", nil
	}
	n.once[text] = true
	n.mu.Unlock()
	return n.Notify(level, "%s", text)
}

func (n *recordingNotifications) UpdateNotificationProgress(id, message string, progress, total int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if total <= 0 || progress > total {
		return fmt.Errorf("invalid progress %d/%d", progress, total)
	}
	n.updates = append(n.updates, fmt.Sprintf("%s %d/%d", message, progress, total))
	return nil
}

func (n *recordingNotifications) snapshot() (notified, updates []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notified...), append([]string(nil), n.updates...)
}

// newWorkspace copies testdata/project into a temporary workspace, with
// what a repository would not commit: an ignored folder, a build output
// and a secret.
func newWorkspace(t *testing.T) (string, workspaceapi.URI, workspaceapi.FileSystem) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sample")
	require.NoError(t, os.CopyFS(dir, os.DirFS("testdata/project")))
	for name, content := range map[string]string{
		"tmp/scratch.go": "package tmp\n\n// Ignored by git: palindrome.\n",
		"build/out.go":   "package build\n\n// Built: palindrome.\n",
		".env":           "TOKEN=palindrome\n",
	} {
		writeFile(t, dir, name, content)
	}
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return dir, root, scheme
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// newRegistry returns a model registry, with the test model downloaded
// when downloaded is set.
func newRegistry(t *testing.T, downloaded bool) *llamacpp.Registry {
	t.Helper()
	reg, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	if downloaded {
		seedModel(t, reg, testModel)
	}
	return reg
}

// seedModel writes a manifest and a weight blob for name, as a download
// would.
func seedModel(t *testing.T, reg *llamacpp.Registry, name string) {
	t.Helper()
	ref, err := llamacpp.ParseReference(name)
	require.NoError(t, err)
	body := []byte("gguf " + name)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	blobs := filepath.Join(reg.Root(), "blobs")
	require.NoError(t, os.MkdirAll(blobs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blobs, "sha256-"+hex.EncodeToString(sum[:])), body, 0o644))
	manifest, err := json.Marshal(ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Layers: []ociregistry.Descriptor{
			{MediaType: ociregistry.MediaTypeOllamaModel, Digest: digest, Size: int64(len(body))},
		},
	})
	require.NoError(t, err)
	dir := filepath.Join(reg.Root(), "manifests", ref.Host, filepath.FromSlash(ref.Repository))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ref.Tag), manifest, 0o644))
	_, ok := reg.Get(context.Background(), name)
	require.True(t, ok, "the seeded model must be downloaded")
}

func newService(t *testing.T, reg *llamacpp.Registry, embed EmbedderFactory) *Service {
	t.Helper()
	svc := New(Config{Enabled: true, Model: testModel, Options: DefaultOptions()}, embed, reg, nopCloser{})
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// testIndex is an open index over a copy of the fixture.
type testIndex struct {
	dir    string
	root   workspaceapi.URI
	fs     workspaceapi.FileSystem
	db     storageapi.Service
	notify *recordingNotifications
	svc    *Service
	reg    *llamacpp.Registry
	idx    *Index
}

func openTestIndex(t *testing.T, downloaded bool, opts Options) *testIndex {
	t.Helper()
	return openTestIndexWith(t, newRegistry(t, downloaded), NewFakeEmbedderFactory(), opts)
}

func openTestIndexWith(t *testing.T, reg *llamacpp.Registry, embed EmbedderFactory, opts Options) *testIndex {
	t.Helper()
	dir, root, fsys := newWorkspace(t)
	ti := &testIndex{
		dir: dir, root: root, fs: fsys, db: storagestub.NewInMemoryService(),
		notify: &recordingNotifications{}, svc: newService(t, reg, embed), reg: reg,
	}
	ti.open(t, opts)
	return ti
}

func (ti *testIndex) open(t *testing.T, opts Options) {
	t.Helper()
	idx, err := ti.svc.OpenIndex(IndexConfig{
		Root: ti.root, FS: ti.fs, DB: ti.db, Notify: ti.notify, Schedule: syncTick, Options: opts,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	ti.idx = idx
	ti.wait(t)
}

func (ti *testIndex) wait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	require.NoError(t, ti.idx.Wait(ctx))
}

func (ti *testIndex) search(t *testing.T, model llmapi.ModelEntry, req searchapi.SemanticRequest) []searchapi.SemanticResult {
	t.Helper()
	hits, _ := ti.searchStatus(t, model, req)
	return hits
}

func (ti *testIndex) searchStatus(
	t *testing.T, model llmapi.ModelEntry, req searchapi.SemanticRequest,
) ([]searchapi.SemanticResult, searchapi.SemanticStatus) {
	t.Helper()
	req.Model = model
	it, st, err := ti.svc.Search(context.Background(), ti.root, req)
	require.NoError(t, err)
	hits, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return hits, st
}

func localModel(name string) llmapi.ModelEntry {
	return llmapi.ModelEntry{Name: name, Provider: llamacpp.LLMProvider}
}

func paths(hits []searchapi.SemanticResult) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}

func TestNewPanicsWithoutDependencies(t *testing.T) {
	reg := newRegistry(t, false)
	tests := []struct {
		name   string
		embed  EmbedderFactory
		reg    *llamacpp.Registry
		closer interface{ Close() error }
	}{
		{"no embedder", nil, reg, nopCloser{}},
		{"no registry", NewFakeEmbedderFactory(), nil, nopCloser{}},
		{"no closer", NewFakeEmbedderFactory(), reg, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { New(Config{Enabled: true}, tt.embed, tt.reg, tt.closer) })
		})
	}
}

func TestServiceSearchErrors(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	other, err := workspaceapi.ParseURI(ti.root.String() + "-other")
	require.NoError(t, err)

	tests := []struct {
		name  string
		root  workspaceapi.URI
		model llmapi.ModelEntry
		want  error
	}{
		{"hosted model", ti.root, llmapi.ModelEntry{Name: "gpt-4o", Provider: "openai"}, searchapi.ErrUnsupported},
		{"no index", other, localModel(testModel), ErrNoIndex},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ti.svc.Search(context.Background(), tt.root, searchapi.SemanticRequest{Query: "invoice", Model: tt.model})
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestServiceOpenIndex(t *testing.T) {
	ti := openTestIndex(t, false, DefaultOptions())
	got, ok := ti.svc.Index(ti.root)
	require.True(t, ok)
	assert.Same(t, ti.idx, got)

	cfg := IndexConfig{Root: ti.root, FS: ti.fs, DB: ti.db, Notify: ti.notify, Schedule: syncTick,
		Options: DefaultOptions()}
	_, err := ti.svc.OpenIndex(cfg)
	assert.ErrorContains(t, err, "already open", "a workspace has one index at a time")

	bad := cfg
	bad.Options.CodeStretch = 0
	_, err = ti.svc.OpenIndex(bad)
	assert.ErrorContains(t, err, "invalid options")

	require.NoError(t, ti.idx.Close())
	require.NoError(t, ti.idx.Close(), "Close is idempotent")
	_, ok = ti.svc.Index(ti.root)
	assert.False(t, ok, "a closed index is unregistered")
	_, _, err = ti.svc.Search(context.Background(), ti.root, searchapi.SemanticRequest{Query: "x", Model: localModel(testModel)})
	assert.ErrorIs(t, err, ErrNoIndex)
}

func TestServiceOpenIndexDisabled(t *testing.T) {
	_, root, fsys := newWorkspace(t)
	svc := New(Config{Model: testModel, Options: DefaultOptions()}, NewFakeEmbedderFactory(),
		newRegistry(t, false), nopCloser{})
	defer func() { _ = svc.Close() }()
	_, err := svc.OpenIndex(IndexConfig{Root: root, FS: fsys, DB: storagestub.NewInMemoryService(),
		Notify: &recordingNotifications{}, Schedule: syncTick, Options: DefaultOptions()})
	assert.ErrorContains(t, err, "disabled")
}

type countingCloser struct{ closed int }

func (c *countingCloser) Close() error {
	c.closed++
	return nil
}

func TestServiceCloseClosesIndexesThenCloser(t *testing.T) {
	_, root, fsys := newWorkspace(t)
	closer := &countingCloser{}
	svc := New(Config{Enabled: true, Model: testModel, Options: DefaultOptions()}, NewFakeEmbedderFactory(),
		newRegistry(t, false), closer)
	idx, err := svc.OpenIndex(IndexConfig{Root: root, FS: fsys, DB: storagestub.NewInMemoryService(),
		Notify: &recordingNotifications{}, Schedule: syncTick, Options: DefaultOptions()})
	require.NoError(t, err)

	require.NoError(t, svc.Close())
	require.NoError(t, svc.Close())
	assert.Equal(t, 1, closer.closed)
	assert.ErrorIs(t, idx.Reindex(), errClosed, "the service closed the index")
	_, err = svc.OpenIndex(IndexConfig{Root: root, FS: fsys, DB: storagestub.NewInMemoryService(),
		Notify: &recordingNotifications{}, Schedule: syncTick, Options: DefaultOptions()})
	assert.ErrorIs(t, err, errClosed)
}

func TestServiceEmbeddingModel(t *testing.T) {
	svc := newService(t, newRegistry(t, false), NewFakeEmbedderFactory())
	_, err := svc.EmbeddingModel(context.Background())
	require.ErrorIs(t, err, llmapi.ErrModelNotFound)
	assert.Contains(t, err.Error(), "models local download ggml-org/embeddinggemma-2-GGUF:Q8_0")

	svc = newService(t, newRegistry(t, true), NewFakeEmbedderFactory())
	entry, err := svc.EmbeddingModel(context.Background())
	require.NoError(t, err)
	assert.Equal(t, testModel, entry.Name)
	assert.Equal(t, llamacpp.LLMProvider, entry.Provider)
	assert.True(t, strings.HasPrefix(filepath.Base(entry.BaseURL), "sha256-"), entry.BaseURL)
}

func TestServiceSearchThroughIterator(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	hits := ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "INV7Q3X42", Limit: 5, Meaning: true})
	require.NotEmpty(t, hits)
	assert.Equal(t, "billing/invoice.py", hits[0].Path)
}
