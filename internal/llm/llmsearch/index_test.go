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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/workspace"
)

func (ti *testIndex) event(t *testing.T, typ textapi.EventType, rel string) bool {
	t.Helper()
	uri, err := ti.fs.URI(rel)
	require.NoError(t, err)
	return ti.idx.Handle(context.Background(), textapi.Event{Type: typ, URI: uri})
}

func embedWith(e Embedder) EmbedderFactory { return func(llmapi.ModelEntry) Embedder { return e } }

func meaningOf(hits []searchapi.SemanticResult) bool {
	for _, h := range hits {
		if h.Z != 0 {
			return true
		}
	}
	return false
}

func TestIndexScansTheWorkspace(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	st := ti.idx.Status()
	assert.True(t, st.Loaded)
	assert.True(t, st.Scanned)
	assert.Equal(t, map[string]int{stateDone: len(fixtureCode) - 1, stateSkipped: 1}, st.Files)
	assert.Positive(t, st.Vectors)
	assert.Greater(t, st.Segments, len(fixtureCode))
	assert.Equal(t, "fake@0000000 fake fp32", st.VectorSource)
	assert.Equal(t, "fake@0000000 fake fp32 | code1 lines1800", st.Fingerprint)
	assert.Empty(t, st.Stalled)
	assert.Equal(t, 1, st.Skipped[skipSecret])
	assert.Equal(t, 2, st.SkippedFolders[skipVendoredFolder])

	notified, updates := ti.notify.snapshot()
	assert.Equal(t, []string{"Indexing the workspace for semantic search"}, notified)
	require.NotEmpty(t, updates)
	assert.Equal(t, "Indexed 13 files for semantic search 13/13", updates[len(updates)-1])
}

func TestIndexServesFromStorageAfterReopen(t *testing.T) {
	embed := newCountingEmbedder()
	ti := openTestIndexWith(t, newRegistry(t, true), embedWith(embed), DefaultOptions())
	before, embedded := ti.idx.Status(), embed.count()
	notified, _ := ti.notify.snapshot()
	require.NoError(t, ti.idx.Close())

	ti.open(t, DefaultOptions())
	assert.Equal(t, embedded, embed.count(), "nothing is embedded again")
	again, _ := ti.notify.snapshot()
	assert.Equal(t, notified, again, "an index that is up to date posts no progress")
	after := ti.idx.Status()
	assert.Equal(t, before.Files, after.Files)
	assert.Equal(t, before.Vectors, after.Vectors)
	hits := ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "great circle radius earth", Meaning: true})
	require.NotEmpty(t, hits)
	assert.Equal(t, "geo/distance.py", hits[0].Path)
	assert.GreaterOrEqual(t, hits[0].Z, zGate)
}

func TestIndexProgressNotification(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantNotified []string
		wantUpdates  []string
	}{
		{name: "empty workspace", files: nil},
		{
			name:         "one file",
			files:        map[string]string{"main.go": "package main\n\nfunc main() {}\n"},
			wantNotified: []string{"Indexing the workspace for semantic search"},
			wantUpdates:  []string{"Indexed 1 file for semantic search 1/1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				writeFile(t, dir, name, content)
			}
			root, err := workspaceapi.CurrentUserHostURI(dir)
			require.NoError(t, err)
			fsys, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
			require.NoError(t, err)
			t.Cleanup(func() { _ = fsys.Close() })
			ti := &testIndex{
				dir: dir, root: root, fs: fsys, db: storagestub.NewInMemoryService(),
				notify: &recordingNotifications{}, svc: newService(t, newRegistry(t, true), NewFakeEmbedderFactory()),
			}
			ti.open(t, DefaultOptions())

			notified, updates := ti.notify.snapshot()
			assert.Equal(t, tt.wantNotified, notified)
			assert.Equal(t, tt.wantUpdates, updates)
			assert.True(t, ti.idx.Status().Scanned)
		})
	}
}

func TestIndexHandleReindexesOneFile(t *testing.T) {
	embed := newCountingEmbedder()
	ti := openTestIndexWith(t, newRegistry(t, true), embedWith(embed), DefaultOptions())
	before := embed.count()
	req := searchapi.SemanticRequest{Query: "zanzibar"}
	require.Empty(t, ti.search(t, localModel(testModel), req))

	writeFile(t, ti.dir, "geo/distance.py", "def zanzibar():\n    return 'spice'\n")
	writeFile(t, ti.dir, "data.csv", "zanzibar,1\n")
	assert.False(t, ti.event(t, textapi.EventTypeFlush, "data.csv"))
	assert.False(t, ti.event(t, textapi.EventTypeEdit, "geo/distance.py"))
	ti.idx.mu.Lock()
	assert.Empty(t, ti.idx.dirty, "data and buffer edits are not indexed")
	ti.idx.mu.Unlock()

	assert.False(t, ti.event(t, textapi.EventTypeFlush, "geo/distance.py"))
	ti.wait(t)
	assert.Equal(t, before+1, embed.count(), "only the saved file is embedded again")
	assert.Equal(t, []string{"geo/distance.py"}, paths(ti.search(t, localModel(testModel), req)))
}

func TestIndexHandleFolders(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	require.NoError(t, os.RemoveAll(filepath.Join(ti.dir, "billing")))
	ti.event(t, textapi.EventTypeRemove, "billing")
	writeFile(t, ti.dir, "shipping/parcel.go", "package shipping\n\n// Parcel weighs a zanzibar parcel.\n")
	ti.event(t, textapi.EventTypeCreate, "shipping")
	ti.wait(t)

	assert.Equal(t, map[string]int{stateDone: len(fixtureCode) - 2, stateSkipped: 1}, ti.idx.Status().Files)
	assert.Empty(t, ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "INV7Q3X42"}))
	assert.Equal(t, []string{"shipping/parcel.go"},
		paths(ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "zanzibar"})))
}

func TestIndexHandleRename(t *testing.T) {
	embed := newCountingEmbedder()
	ti := openTestIndexWith(t, newRegistry(t, true), embedWith(embed), DefaultOptions())
	before := embed.count()
	require.NoError(t, os.Rename(filepath.Join(ti.dir, "billing/tax.py"), filepath.Join(ti.dir, "billing/rates.py")))
	ti.event(t, textapi.EventTypeRename, "billing/tax.py")
	ti.event(t, textapi.EventTypeCreate, "billing/rates.py")
	ti.wait(t)

	assert.Equal(t, before, embed.count(), "a rename embeds nothing")
	got := paths(ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "sales tax rate"}))
	assert.Contains(t, got, "billing/rates.py")
	assert.NotContains(t, got, "billing/tax.py")
}

func TestIndexIgnoresEventsOutsideTheWorkspace(t *testing.T) {
	ti := openTestIndex(t, false, DefaultOptions())
	other, _, _ := newWorkspace(t)
	uri, err := ti.fs.URI(filepath.Join(other, "geo/distance.py"))
	require.NoError(t, err)
	ti.idx.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeFlush, URI: uri})
	ti.idx.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeFlush, URI: ti.root})
	ti.idx.mu.Lock()
	defer ti.idx.mu.Unlock()
	assert.Empty(t, ti.idx.dirty)
}

func TestIndexKeywordOnlyUntilTheModelIsDownloaded(t *testing.T) {
	ti := openTestIndex(t, false, DefaultOptions())
	st := ti.idx.Status()
	assert.Equal(t, map[string]int{stateDone: len(fixtureCode) - 1, stateSkipped: 1}, st.Files)
	assert.Zero(t, st.Vectors)
	assert.Empty(t, st.VectorSource)

	req := searchapi.SemanticRequest{Query: "great circle radius earth", Meaning: true}
	for range 2 {
		hits := ti.search(t, localModel(testModel), req)
		require.NotEmpty(t, hits, "keyword search works without the model")
		assert.False(t, meaningOf(hits))
	}
	notified, _ := ti.notify.snapshot()
	assert.Equal(t, 1, strings.Count(strings.Join(notified, "\n"),
		"models local download ggml-org/embeddinggemma-2-GGUF:Q8_0"), "%q", notified)
	assert.Equal(t, encoderNotDownloaded, ti.idx.Status().Encoder)

	seedModel(t, ti.reg, testModel)
	entry, err := ti.svc.EmbeddingModel(context.Background())
	require.NoError(t, err)
	assert.False(t, meaningOf(ti.search(t, entry, req)), "the index has no vectors yet")
	ti.wait(t)
	st = ti.idx.Status()
	assert.Equal(t, "fake@0000000 fake fp32", st.VectorSource, "the search woke the indexer for the model")
	assert.Positive(t, st.Vectors)
	hits := ti.search(t, entry, req)
	require.NotEmpty(t, hits)
	assert.GreaterOrEqual(t, hits[0].Z, zGate)
	assert.Equal(t, encoderReady, ti.idx.Status().Encoder)
}

// otherEmbedder makes vectors no index here was built with.
type otherEmbedder struct{ fakeEmbedder }

func (otherEmbedder) Info() EmbedderInfo {
	return EmbedderInfo{Model: "other", Revision: "1234567", Runtime: "fake", Dtype: "fp32", Dim: dims}
}

func TestIndexKeywordOnlyWithAnotherModel(t *testing.T) {
	const otherModel = "huggingface.co/x/other-GGUF:Q4_0"
	reg := newRegistry(t, true)
	seedModel(t, reg, otherModel)
	ti := openTestIndexWith(t, reg, func(entry llmapi.ModelEntry) Embedder {
		if entry.Name == otherModel {
			return otherEmbedder{}
		}
		return fakeEmbedder{}
	}, DefaultOptions())

	hits := ti.search(t, localModel(otherModel), searchapi.SemanticRequest{Query: "great circle radius earth", Meaning: true})
	require.NotEmpty(t, hits)
	assert.False(t, meaningOf(hits), "vectors of different models never meet")
	notified, _ := ti.notify.snapshot()
	assert.Contains(t, notified,
		"Meaning search is off: the search index was built with fake@0000000 fake fp32, not other@1234567 fake fp32")
	ti.wait(t)
	assert.Equal(t, "fake@0000000 fake fp32", ti.idx.Status().VectorSource, "the index keeps its model")
}

func TestIndexStallsWhenTheModelGoes(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	ref, err := llamacpp.ParseReference(testModel)
	require.NoError(t, err)
	require.NoError(t, ti.reg.Delete(context.Background(), ref))

	writeFile(t, ti.dir, "geo/distance.py", "def zanzibar():\n    return 'spice'\n")
	ti.event(t, textapi.EventTypeFlush, "geo/distance.py")
	ti.wait(t)
	st := ti.idx.Status()
	assert.Contains(t, st.Stalled, "not downloaded")
	assert.Equal(t, 1, st.Files[statePending], "files wait for the model; vectors are not mixed")
	assert.Equal(t, "fake@0000000 fake fp32", st.VectorSource)

	seedModel(t, ti.reg, testModel)
	ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "zanzibar", Meaning: true})
	ti.wait(t)
	st = ti.idx.Status()
	assert.Empty(t, st.Stalled)
	assert.Zero(t, st.Files[statePending])
}

func TestIndexStallFinishesTheProgressNotification(t *testing.T) {
	embed := newCountingEmbedder()
	embed.err = errors.New("llama-server exited")
	ti := openTestIndexWith(t, newRegistry(t, true), embedWith(embed), DefaultOptions())
	assert.Contains(t, ti.idx.Status().Stalled, "llama-server exited")
	assert.Contains(t, ti.idx.Status().Unavailable(), "llama-server exited")

	notified, updates := ti.notify.snapshot()
	assert.Equal(t, []string{"Indexing the workspace for semantic search"}, notified)
	require.NotEmpty(t, updates)
	last := updates[len(updates)-1]
	assert.Contains(t, last, "Indexing for semantic search stopped: llmsearch: embedder: llama-server exited")
	assert.True(t, strings.HasSuffix(last, " 1/1"), "the notification is finished: %q", last)
}

func TestStatusUnavailable(t *testing.T) {
	tests := []struct {
		name string
		st   Status
		want string
	}{
		{"ready", Status{Encoder: encoderReady}, ""},
		{"not searched yet", Status{}, ""},
		{"not downloaded", Status{Encoder: encoderNotDownloaded}, ""},
		{"query not encoded", Status{Encoder: encoderUnavailable + ": server exited"}, "server exited"},
		{"stalled", Status{Stalled: "embedder: boom", Encoder: encoderReady}, "embedder: boom"},
		{"stalled before the query failed", Status{Stalled: "embedder: boom", Encoder: encoderUnavailable + ": server exited"}, "server exited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.st.Unavailable())
		})
	}
}

func TestIndexRescanAdoptsTheModel(t *testing.T) {
	const changed = "The embedding model changed: rebuilding the search index"
	embed := newCountingEmbedder()
	reg := newRegistry(t, false)
	ti := openTestIndexWith(t, reg, embedWith(embed), DefaultOptions())
	require.Empty(t, ti.idx.Status().VectorSource)

	seedModel(t, reg, testModel)
	ti.idx.Rescan()
	ti.wait(t)
	st := ti.idx.Status()
	assert.Equal(t, "fake@0000000 fake fp32", st.VectorSource)
	assert.Positive(t, st.Vectors)
	notified, _ := ti.notify.snapshot()
	assert.NotContains(t, notified, changed, "a first model is no change")

	embed.info = &EmbedderInfo{Model: "other", Revision: "1234567", Runtime: "fake", Dtype: "fp32", Dim: dims}
	ti.idx.Rescan()
	ti.wait(t)
	assert.Equal(t, "other@1234567 fake fp32", ti.idx.Status().VectorSource)
	notified, _ = ti.notify.snapshot()
	assert.Contains(t, notified, changed)
}

func TestIndexReindex(t *testing.T) {
	embed := newCountingEmbedder()
	ti := openTestIndexWith(t, newRegistry(t, true), embedWith(embed), DefaultOptions())
	before, embedded := ti.idx.Status(), embed.count()
	require.NoError(t, ti.idx.Reindex())
	ti.wait(t)
	assert.Equal(t, 2*embedded, embed.count(), "everything is embedded again")
	after := ti.idx.Status()
	assert.Equal(t, before.Files, after.Files)
	assert.Equal(t, before.Vectors, after.Vectors)
}

func TestIndexReleasesVectorsWhenIdle(t *testing.T) {
	opts := DefaultOptions()
	opts.IdleTimeout = 20 * time.Millisecond
	ti := openTestIndex(t, true, opts)
	hits := ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "great circle radius earth", Meaning: true})
	require.NotEmpty(t, hits)
	assert.Eventually(t, func() bool {
		ti.idx.cat.mu.Lock()
		defer ti.idx.cat.mu.Unlock()
		return ti.idx.cat.vec == nil
	}, waitTimeout, 5*time.Millisecond)

	hits = ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "great circle radius earth", Meaning: true})
	require.NotEmpty(t, hits)
	assert.GreaterOrEqual(t, hits[0].Z, zGate, "the next meaning search loads them again")
}

func TestIndexDropsHitsOfMissingFiles(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	require.NoError(t, os.Remove(filepath.Join(ti.dir, "billing/invoice.py")))
	assert.Empty(t, ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "INV7Q3X42"}))
}

func TestIndexSearchWithinRoots(t *testing.T) {
	ti := openTestIndex(t, true, DefaultOptions())
	hits := ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "palindrome", Roots: []string{"./dup/"}})
	assert.Equal(t, []string{"dup/a.go", "dup/b.go"}, paths(hits))
	hits = ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "palindrome", Roots: []string{"retry"}})
	assert.Empty(t, hits)
	hits = ti.search(t, localModel(testModel), searchapi.SemanticRequest{Query: "palindrome", Roots: []string{"."}, Limit: 1})
	assert.Len(t, hits, 1)
}

func TestIndexWaitAfterClose(t *testing.T) {
	ti := openTestIndex(t, false, DefaultOptions())
	require.NoError(t, ti.idx.Close())
	assert.ErrorIs(t, ti.idx.Wait(context.Background()), errClosed)
	assert.False(t, ti.event(t, textapi.EventTypeFlush, "geo/distance.py"))
}
