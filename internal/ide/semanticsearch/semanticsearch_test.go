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

package semanticsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
	"unstable.build/rune/internal/llm/llmsearch"
	"unstable.build/rune/internal/workspace"
)

const testModel = "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0"

const waitFor = 10 * time.Second

func TestParseArgs(t *testing.T) {
	root, err := workspaceapi.CurrentUserHostURI("/work/repo")
	require.NoError(t, err)
	tests := []struct {
		name    string
		args    []string
		want    searchapi.SemanticRequest
		wantErr string
	}{
		{
			name: "query",
			args: []string{"retry", "backoff"},
			want: searchapi.SemanticRequest{Query: "retry backoff", Meaning: true},
		},
		{
			name: "quoted query",
			args: []string{"retry backoff"},
			want: searchapi.SemanticRequest{Query: "retry backoff", Meaning: true},
		},
		{
			name: "every flag",
			args: []string{"-r", "internal", "--root=/work/repo/cmd", "-n", "5", "--meaning=false", "--prefix-last", "ret"},
			want: searchapi.SemanticRequest{
				Query: "ret", Roots: []string{"internal", "cmd"}, Limit: 5, PrefixLast: true,
			},
		},
		{
			name: "flags between words",
			args: []string{"retry", "-n", "3", "backoff"},
			want: searchapi.SemanticRequest{Query: "retry backoff", Limit: 3, Meaning: true},
		},
		{
			name: "dash dash",
			args: []string{"--", "-n", "flag"},
			want: searchapi.SemanticRequest{Query: "-n flag", Meaning: true},
		},
		{name: "no query", args: nil, wantErr: "usage: searchsemantic"},
		{name: "blank query", args: []string{" "}, wantErr: "usage: searchsemantic"},
		{name: "help", args: []string{"-h"}, wantErr: "usage: searchsemantic"},
		{name: "unknown flag", args: []string{"--nope", "q"}, wantErr: "searchsemantic: unknown flag: --nope"},
		{name: "negative limit", args: []string{"-n", "-1", "q"}, wantErr: "--limit must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args, root)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSetupMessage(t *testing.T) {
	tests := []struct {
		name  string
		need  needs
		model string
		want  string
	}{
		{
			name: "both", need: needs{server: true, model: true}, model: testModel,
			want: "This command requires installing the **llama-server** package and downloading the " +
				"**ggml-org/embeddinggemma-2-GGUF:Q8_0** model from Hugging Face. Do you want to do that?",
		},
		{
			name: "server", need: needs{server: true}, model: testModel,
			want: "This command requires installing the **llama-server** package. Do you want to do that?",
		},
		{
			name: "upgrade and model", need: needs{upgrade: true, model: true}, model: testModel,
			want: "This command requires upgrading the **llama-server** package and downloading the " +
				"**ggml-org/embeddinggemma-2-GGUF:Q8_0** model from Hugging Face. Do you want to do that?",
		},
		{
			name: "model from elsewhere", need: needs{model: true}, model: "registry.example.com/x/y:Q4",
			want: "This command requires downloading the **x/y:Q4** model from registry.example.com. " +
				"Do you want to do that?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, setupMessage(tt.need, tt.model))
		})
	}
}

func TestComplete(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{nil, nil},
		{[]string{"retry"}, nil},
		{[]string{"-"}, flagNames},
		{[]string{"retry", "--pre"}, []string{"--prefix-last"}},
	}
	h := New(Config{})
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			it, _, err := h.Complete(context.Background(), textapi.Command{Name: Name, Args: tt.args})
			require.NoError(t, err)
			got, err := blueiterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.Equal(t, tt.want, nilIfEmpty(got))
		})
	}
}

func TestHandleCommandSetsUpMeaningSearch(t *testing.T) {
	h := newHarness(t, false, false)
	require.NoError(t, h.run("retry", "backoff"))
	prompt := h.prompt.wait(t)
	assert.Equal(t, "This command requires installing the **llama-server** package and downloading the "+
		"**ggml-org/embeddinggemma-2-GGUF:Q8_0** model from Hugging Face. Do you want to do that?", prompt.message)
	assert.Equal(t, []string{yesOpt, noOpt}, prompt.options)

	prompt.handler.OnSelect(0, yesOpt)
	h.notify.waitFor(t, "You can now use the command `searchsemantic`")
	assert.True(t, h.server.isInstalled())
	_, err := h.search.EmbeddingModel(context.Background())
	assert.NoError(t, err, "the model is downloaded")
	assert.Equal(t, []string{
		"Downloading ggml-org/embeddinggemma-2-GGUF:Q8_0",
		"You can now use the command `searchsemantic`",
	}, h.notify.messages())
	assert.Contains(t, h.notify.progress(), "Downloaded ggml-org/embeddinggemma-2-GGUF:Q8_0 1/1",
		"the download's notification is finished")
	assert.Eventually(t, func() bool { return h.index.Status().VectorSource != "" }, waitFor, 10*time.Millisecond,
		"the index adopts the model")
	assert.Nil(t, h.wm.picker(), "the search waits for the user to run it again")

	require.NoError(t, h.run("retry"))
	assert.Eventually(t, func() bool { return h.wm.picker() != nil }, waitFor, 10*time.Millisecond)
	assert.Equal(t, 1, h.prompt.count(), "it is set up once")
}

func TestHandleCommandDeclinedSetup(t *testing.T) {
	h := newHarness(t, true, false)
	require.NoError(t, h.run("retry"))
	prompt := h.prompt.wait(t)
	assert.Equal(t, "This command requires downloading the **ggml-org/embeddinggemma-2-GGUF:Q8_0** model "+
		"from Hugging Face. Do you want to do that?", prompt.message)
	prompt.handler.OnSelect(1, noOpt)
	assert.False(t, h.handler.settingUp.Load())
	assert.Empty(t, h.notify.messages())
	assert.Zero(t, h.models.count())
}

func TestHandleCommandSetupFails(t *testing.T) {
	h := newHarness(t, true, false)
	h.models.err = errors.New("network down")
	require.NoError(t, h.run("retry"))
	h.prompt.wait(t).handler.OnSelect(0, yesOpt)
	h.notify.waitFor(t, "searchsemantic: download ggml-org/embeddinggemma-2-GGUF:Q8_0: network down")
	assert.Contains(t, h.notify.progress(), "Could not download ggml-org/embeddinggemma-2-GGUF:Q8_0 1/1")
	assert.Eventually(t, func() bool { return !h.handler.settingUp.Load() }, waitFor, 10*time.Millisecond)
}

func TestHandleCommandUpgradesAnOutdatedServer(t *testing.T) {
	tests := []struct {
		name       string
		stillOld   bool
		wantNotice string
	}{
		{name: "upgraded", wantNotice: "You can now use the command `searchsemantic`"},
		{
			name: "latest still too old", stillOld: true,
			wantNotice: "searchsemantic: the latest llama-server package is too old to run the embedding model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, true, true)
			h.server.outdated, h.server.stillOld = true, tt.stillOld
			require.NoError(t, h.run("retry"))
			prompt := h.prompt.wait(t)
			assert.Equal(t, "This command requires upgrading the **llama-server** package. "+
				"Do you want to do that?", prompt.message, "an old llama-server cannot run the model")

			prompt.handler.OnSelect(0, yesOpt)
			h.notify.waitFor(t, tt.wantNotice)
			assert.Equal(t, 1, h.server.installs())
			assert.Zero(t, h.models.count(), "the model is downloaded already")
		})
	}
}

func TestHandleCommandSearches(t *testing.T) {
	h := newHarness(t, true, true)
	h.searcher.hits = cannedHits[:2]
	require.NoError(t, h.run("-r", "billing", "-n", "2", "invoice", "total"))
	picker := h.wm.waitPicker(t)
	assert.Zero(t, h.prompt.count(), "nothing to set up")
	assert.Equal(t, searchapi.SemanticRequest{Query: "invoice total", Roots: []string{"billing"}, Limit: 2, Meaning: true},
		h.searcher.request(t))

	out := draw(t, picker)
	lines := strings.Split(out, "\n")
	assert.Equal(t, "  SEMANTIC █▓▒░ score 3.14 · meaning z 2.50 cos 0.61 · words #2"+
		strings.Repeat(" ", 25)+"lines 40–47 ", lines[len(lines)-1], "the status bar says how the focused result ranked")
	assert.NotContains(t, out, "of 2 ", "the list counts the results, not the status bar")
	assert.Contains(t, out, "billing/invoice.go:42: return roundInvoiceTotal(lines)")
	assert.Contains(t, out, "geo/route.go:7:        func shortestRoute(a, b Place) []Place {")
	assert.Contains(t, out, " func total(lines []Line) int {", "the preview shows the stretch that matched")
	assert.Nil(t, h.searcher.regexpRequest(), "meaning search answered")
}

func TestHandleCommandWithoutMeaning(t *testing.T) {
	h := newHarness(t, false, false)
	h.searcher.hits = cannedHits[1:2]
	require.NoError(t, h.run("--meaning=false", "shortest"))
	picker := h.wm.picker()
	require.NotNil(t, picker, "a search by words needs no setup")
	assert.Zero(t, h.prompt.count())
	assert.False(t, h.searcher.request(t).Meaning)
	draw(t, picker)
}

func TestHandleCommandFallsBackToWords(t *testing.T) {
	const fallback = "  ERROR █▓▒░ fallback to word search. see logs for more info · "
	words := []searchapi.RegexpResult{
		{Path: "geo/route.go", Line: 7, Text: "func shortestRoute(a, b Place) []Place {", Spans: [][2]int{{5, 13}, {13, 18}}},
	}
	tests := []struct {
		name      string
		embed     llmsearch.EmbedderFactory
		hits      []searchapi.SemanticResult
		status    searchapi.SemanticStatus
		err       error
		regexpErr error
		args      []string
		wantWords bool
		wantBar   string
	}{
		{
			name: "meaning search fails", err: errors.New("boom"), args: []string{"shortest", "route"},
			wantWords: true, wantBar: fallback + "score 1.00 · words #1",
		},
		{
			name: "the model cannot run", embed: failingEmbedderFactory, hits: cannedHits[1:2],
			args: []string{"shortest", "route"}, wantWords: true, wantBar: fallback + "score 1.00 · words #1",
		},
		{
			name: "the model cannot run but meaning answered", embed: failingEmbedderFactory, hits: cannedHits[:1],
			status:  searchapi.SemanticStatus{Indexing: true, Message: "indexing stalled: llama-server exited"},
			args:    []string{"invoice", "total"},
			wantBar: "  SEMANTIC █▓▒░ score 3.14 · meaning z 2.50 cos 0.61 · words #2 · indexing stalled: ll… lines 40–47 ",
		},
		{
			name: "word search fails too", err: errors.New("boom"), regexpErr: errors.New("bad index"),
			args: []string{"shortest"}, wantWords: true, wantBar: "  ERROR █▓▒░ Search failed: bad index",
		},
		{
			name: "words were asked for", err: errors.New("boom"), args: []string{"--meaning=false", "shortest"},
			wantBar: "  ERROR █▓▒░ Search failed: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			embed := tt.embed
			if embed == nil {
				embed = llmsearch.NewFakeEmbedderFactory()
			}
			h := newHarnessWith(t, true, true, embed)
			h.searcher.hits, h.searcher.status, h.searcher.err = tt.hits, tt.status, tt.err
			h.searcher.lines, h.searcher.regexpErr = words, tt.regexpErr
			require.NoError(t, h.run(tt.args...))
			out := draw(t, h.wm.waitPicker(t))
			lines := strings.Split(out, "\n")
			assert.True(t, strings.HasPrefix(lines[len(lines)-1], tt.wantBar), "%q", lines[len(lines)-1])
			if !tt.wantWords {
				assert.Nil(t, h.searcher.regexpRequest())
				return
			}
			assert.Equal(t, &searchapi.RegexpRequest{
				Pattern: strings.Join(llmsearch.Terms(strings.Join(tt.args, " ")), "|"), IgnoreCase: true,
				Limit: wordLines, MaxPerFile: wordLinesPerFile,
			}, h.searcher.regexpRequest())
			if tt.regexpErr == nil {
				assert.Contains(t, out, "geo/route.go:7: func shortestRoute(a, b Place) []Place {")
			}
		})
	}
}

func TestHandleCommandErrors(t *testing.T) {
	h := newHarness(t, true, true)
	assert.ErrorIs(t, h.run(), errUsage)
	require.NoError(t, h.index.Close())
	assert.ErrorIs(t, h.run("retry"), errNoIndex)
}

func TestHandleCommandShowsTheSearchStatus(t *testing.T) {
	h := newHarness(t, true, true)
	h.searcher.hits = cannedHits[1:2]
	h.searcher.status = searchapi.SemanticStatus{Indexing: true, Message: "12 files left to index"}
	require.NoError(t, h.run("shortest"))
	lines := strings.Split(draw(t, h.wm.waitPicker(t)), "\n")
	assert.True(t, strings.HasPrefix(lines[len(lines)-1],
		"  SEMANTIC █▓▒░ score 2.00 · words #1 · 12 files left to index"), "%q", lines[len(lines)-1])
}

// harness runs the command over a workspace with an open search index.
type harness struct {
	root     workspaceapi.URI
	reg      *llamacpp.Registry
	search   *llmsearch.Service
	index    *llmsearch.Index
	server   *stubServer
	models   *stubDownloader
	searcher *stubSearcher
	prompt   *stubPrompt
	wm       *stubWindowManager
	notify   *recordingNotifications
	handler  *Handler
}

func newHarness(t *testing.T, serverInstalled, modelDownloaded bool) *harness {
	t.Helper()
	return newHarnessWith(t, serverInstalled, modelDownloaded, llmsearch.NewFakeEmbedderFactory())
}

func newHarnessWith(t *testing.T, serverInstalled, modelDownloaded bool, embed llmsearch.EmbedderFactory) *harness {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"billing/invoice.go": "package billing\n\n" + strings.Repeat("// pad\n", 37) +
			"func total(lines []Line) int {\n\tsum := 0\n\treturn roundInvoiceTotal(lines)\n}\n",
		"geo/route.go": "package geo\n\n\n\n\n\nfunc shortestRoute(a, b Place) []Place {\n\treturn nil\n}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	fsys, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsys.Close() })

	reg, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	models := &stubDownloader{reg: reg}
	if modelDownloaded {
		models.seed(t, testModel)
	}
	search := llmsearch.New(llmsearch.Config{Enabled: true, Model: testModel, Options: llmsearch.DefaultOptions()},
		embed, reg, nopCloser{})
	t.Cleanup(func() { _ = search.Close() })
	notify := &recordingNotifications{}
	index, err := search.OpenIndex(llmsearch.IndexConfig{
		Root: root, FS: fsys, DB: storagestub.NewInMemoryService(), Notify: &recordingNotifications{},
		Schedule: syncTick, Options: llmsearch.DefaultOptions(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = index.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	require.NoError(t, index.Wait(ctx))

	h := &harness{
		root: root, reg: reg, search: search, index: index,
		server: &stubServer{installed: serverInstalled}, models: models, searcher: &stubSearcher{},
		prompt: &stubPrompt{}, wm: &stubWindowManager{}, notify: notify,
	}
	h.handler = New(Config{
		Root: root, Searcher: h.searcher, Search: search, Models: models, Server: h.server,
		Clients: finder.Clients{
			ResourceOpener: stubResourceOpener{}, WindowManager: h.wm, Interrupter: term.NopInterrupter(),
			Notifications: notify, FileSystem: fsys,
		},
		Prompt: h.prompt, ScheduleNextTick: syncTick,
	})
	t.Cleanup(func() {
		if p := h.wm.picker(); p != nil {
			_ = p.Close()
		}
	})
	return h
}

func (h *harness) run(args ...string) error {
	return h.handler.HandleCommand(context.Background(), textapi.Command{Name: Name, Args: args})
}

// draw renders the picker once its search is done.
func draw(t *testing.T, picker *locationsearch.Handler) string {
	t.Helper()
	const width, height = 100, 30
	var out string
	require.Eventually(t, func() bool {
		picker.Resize(width, height)
		w := term.NewStringWriter(width, height)
		picker.Draw(w)
		require.NoError(t, w.Flush())
		out = w.String()
		return !strings.Contains(out, "Searching for")
	}, waitFor, 10*time.Millisecond)
	if _, ok := picker.Selection(); ok {
		// The entries reach the list after the search's status does.
		require.Eventually(t, func() bool {
			w := term.NewStringWriter(width, height)
			picker.Draw(w)
			require.NoError(t, w.Flush())
			out = w.String()
			return strings.Contains(out, " score ")
		}, waitFor, 10*time.Millisecond)
	}
	return out
}

var syncTick = func(fn func()) bool { fn(); return true }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// failingEmbedderFactory makes embedders that cannot run, as when
// llama-server cannot load the model.
func failingEmbedderFactory(llmapi.ModelEntry) llmsearch.Embedder { return failingEmbedder{} }

type failingEmbedder struct{}

func (failingEmbedder) Info() llmsearch.EmbedderInfo {
	return llmsearch.NewFakeEmbedderFactory()(llmapi.ModelEntry{}).Info()
}

func (failingEmbedder) EmbedTexts(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("no such architecture")
}

type stubServer struct {
	mu sync.Mutex
	// stillOld keeps the server outdated when it is installed.
	installed, outdated, stillOld bool
	installCalls                  int
}

func (s *stubServer) Installed(context.Context) (installed, outdated bool, _ error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed, s.outdated, nil
}

func (s *stubServer) Install(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installed, s.outdated = true, s.stillOld
	s.installCalls++
	return nil
}

func (s *stubServer) isInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed
}

func (s *stubServer) installs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installCalls
}

// stubDownloader downloads a model by writing it into the registry as a
// download would.
type stubDownloader struct {
	reg *llamacpp.Registry
	err error

	mu        sync.Mutex
	downloads int
}

func (d *stubDownloader) Download(_ context.Context, ref llamacpp.Reference, progress llamacpp.ProgressFunc) (
	*llamacpp.DownloadResult, error,
) {
	d.mu.Lock()
	d.downloads++
	d.mu.Unlock()
	progress(0, 10)
	if d.err != nil {
		return nil, d.err
	}
	if err := d.write(ref); err != nil {
		return nil, err
	}
	progress(10, 10)
	return &llamacpp.DownloadResult{Reference: ref}, nil
}

func (d *stubDownloader) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.downloads
}

func (d *stubDownloader) seed(t *testing.T, model string) {
	t.Helper()
	ref, err := llamacpp.ParseReference(model)
	require.NoError(t, err)
	require.NoError(t, d.write(ref))
}

func (d *stubDownloader) write(ref llamacpp.Reference) error {
	body := []byte("gguf " + ref.String())
	sum := sha256.Sum256(body)
	blobs := filepath.Join(d.reg.Root(), "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(blobs, "sha256-"+hex.EncodeToString(sum[:])), body, 0o644); err != nil {
		return err
	}
	manifest, err := json.Marshal(ociregistry.Manifest{
		SchemaVersion: 2,
		MediaType:     ociregistry.MediaTypeDockerManifestV2,
		Layers: []ociregistry.Descriptor{{
			MediaType: ociregistry.MediaTypeOllamaModel,
			Digest:    "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(body)),
		}},
	})
	if err != nil {
		return err
	}
	dir := filepath.Join(d.reg.Root(), "manifests", ref.Host, filepath.FromSlash(ref.Repository))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ref.Tag), manifest, 0o644)
}

type stubSearcher struct {
	hits      []searchapi.SemanticResult
	status    searchapi.SemanticStatus
	err       error
	lines     []searchapi.RegexpResult
	regexpErr error

	mu        sync.Mutex
	req       *searchapi.SemanticRequest
	regexpReq *searchapi.RegexpRequest
}

func (s *stubSearcher) RegexpSearch(_ context.Context, req searchapi.RegexpRequest) (
	iterator.Iterator[searchapi.RegexpResult], error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regexpReq = &req
	if s.regexpErr != nil {
		return nil, s.regexpErr
	}
	return iterator.FromSlice(s.lines), nil
}

func (s *stubSearcher) regexpRequest() *searchapi.RegexpRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regexpReq
}

func (s *stubSearcher) SemanticSearch(_ context.Context, req searchapi.SemanticRequest) (
	iterator.Iterator[searchapi.SemanticResult], searchapi.SemanticStatus, error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.req = &req
	if s.err != nil {
		return nil, searchapi.SemanticStatus{}, s.err
	}
	return iterator.FromSlice(s.hits), s.status, nil
}

func (s *stubSearcher) request(t *testing.T) searchapi.SemanticRequest {
	t.Helper()
	var req *searchapi.SemanticRequest
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		req = s.req
		return req != nil
	}, waitFor, 10*time.Millisecond)
	return *req
}

type prompted struct {
	message string
	options []string
	handler handler.PromptHandler
}

type stubPrompt struct {
	mu      sync.Mutex
	prompts []prompted
}

func (p *stubPrompt) Prompt(message string, options []string, _ []term.KeyComb, h handler.PromptHandler) browser.Window {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, prompted{message: message, options: options, handler: h})
	return nil
}

func (p *stubPrompt) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.prompts)
}

func (p *stubPrompt) wait(t *testing.T) prompted {
	t.Helper()
	require.Eventually(t, func() bool { return p.count() > 0 }, waitFor, 10*time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prompts[len(p.prompts)-1]
}

type recordingNotifications struct {
	mu       sync.Mutex
	notified []string
	updates  []string
}

func (n *recordingNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = append(n.notified, fmt.Sprintf(msg, args...))
	return fmt.Sprintf("n%d", len(n.notified)), nil
}

func (n *recordingNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(_, message string, progress, total int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if total <= 0 || progress > total {
		return fmt.Errorf("invalid progress %d/%d", progress, total)
	}
	n.updates = append(n.updates, fmt.Sprintf("%s %d/%d", message, progress, total))
	return nil
}

func (n *recordingNotifications) messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notified...)
}

func (n *recordingNotifications) progress() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.updates...)
}

func (n *recordingNotifications) waitFor(t *testing.T, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return slices.Contains(n.messages(), msg)
	}, waitFor, 10*time.Millisecond, "%q not in %q", msg, n.messages())
}

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

type stubWindowManager struct {
	mu       sync.Mutex
	floating *locationsearch.Handler
}

func (m *stubWindowManager) picker() *locationsearch.Handler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.floating
}

func (m *stubWindowManager) waitPicker(t *testing.T) *locationsearch.Handler {
	t.Helper()
	require.Eventually(t, func() bool { return m.picker() != nil }, waitFor, 10*time.Millisecond)
	return m.picker()
}

func (m *stubWindowManager) Focus() (browserapi.Window, error) { return stubWindow(1), nil }
func (m *stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return stubWindow(2), nil
}

func (m *stubWindowManager) Floating(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floating = h.(*locationsearch.Handler)
	return stubWindow(3), nil
}
func (m *stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (m *stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, errors.New("no tabs")
}
func (m *stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (m *stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }
func (m *stubWindowManager) SetTabStatus(workspaceapi.URI, browserapi.TabStatus) error    { return nil }

type stubResourceOpener struct{}

func (stubResourceOpener) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return nil, errors.New("not opened")
}
func (stubResourceOpener) Show(workspaceapi.URI) error { return nil }
