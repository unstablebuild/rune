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

// Package llmsearch is workspace code search: a port of DigUp's code
// index to Go, answering searchapi.Searcher.SemanticSearch.
//
// Each workspace gets an Index: its code files cut into stretches of whole
// lines, each kept for keyword search and, with a local EmbeddingGemma
// model, for meaning search. A query ranks files by both, as DigUp does.
// Keyword search reproduces SQLite FTS5 exactly (tokenizer, stemmer, bm25
// and snippets), so rankings match DigUp's without SQLite.
//
// The files mirror DigUp's sources:
//
//   - tokenizer.go, unicode61_tables.go, porter.go: FTS5's unicode61 and
//     porter tokenizers (fts5_unicode2.c, fts5_tokenize.c).
//   - keyword.go: the fts table with FTS5's bm25() and snippet()
//     (fts5_aux.c).
//   - searchtext.go: SearchText.swift and Searcher's terms and patterns.
//   - code.go: CodeExtractor.swift.
//   - rules.go, crawler.go: Rules.swift, Crawler.swift.
//   - prompts.go: Model.swift's prompts.
//   - vectors.go: Vectors.swift and Searcher.meaning.
//   - store.go: IndexStore.swift, over storageapi instead of SQLite.
//   - indexer.go: Indexer.swift, for code.
//   - searcher.go: Searcher.swift.
//   - embedder.go: Embedder and WorkerInfo, over llama-server.
//   - index.go, service.go: SearchService.swift.
package llmsearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/llamacpp"
)

// ErrNoIndex is returned by Service.Search for a workspace that has no
// open Index.
var ErrNoIndex = errors.New("llmsearch: no search index is open for the workspace")

var errClosed = errors.New("llmsearch: closed")

// Config configures code search.
type Config struct {
	// Enabled turns indexing on: without it no Index opens.
	Enabled bool
	// Model is the registry name of the local embedding model, e.g.
	// huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0.
	Model   string
	Options Options
}

// DefaultConfig leaves semantic indexing off until enabled, using
// EmbeddingGemma 2 when it is.
func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Model:   "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0",
		Options: DefaultOptions(),
	}
}

// Service owns the embedder factory shared by every workspace index and
// the registry of open indexes, keyed by workspace root.
type Service struct {
	cfg      Config
	embed    EmbedderFactory
	registry *llamacpp.Registry
	closer   io.Closer

	mu      sync.Mutex
	indexes map[string]*Index
	closed  bool
}

// New returns a Service that embeds with the models of registry through
// embed. closer is closed last by Close: it owns what embed runs on (the
// embedding servers). Every argument is required.
func New(cfg Config, embed EmbedderFactory, registry *llamacpp.Registry, closer io.Closer) *Service {
	if embed == nil || registry == nil || closer == nil {
		panic("llmsearch: New needs an embedder factory, a model registry and a closer")
	}
	return &Service{cfg: cfg, embed: embed, registry: registry, closer: closer, indexes: map[string]*Index{}}
}

// OpenIndex opens the index of cfg.Root and starts keeping it in step with
// the workspace in the background. A workspace has one Index at a time.
func (s *Service) OpenIndex(cfg IndexConfig) (*Index, error) {
	if !s.cfg.Enabled {
		return nil, errors.New("llmsearch: search is disabled")
	}
	if cfg.FS == nil || cfg.DB == nil || cfg.Notify == nil || cfg.Schedule == nil {
		return nil, errors.New("llmsearch: an index needs a filesystem, a database, notifications and a scheduler")
	}
	if cfg.Options.CodeStretch <= 0 || cfg.Options.MaxCodeBytes <= 0 || cfg.Options.MaxNotebookBytes <= 0 {
		return nil, fmt.Errorf("llmsearch: invalid options %+v", cfg.Options)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	key := cfg.Root.String()
	if _, ok := s.indexes[key]; ok {
		return nil, fmt.Errorf("llmsearch: an index of %s is already open", key)
	}
	idx := newIndex(s, cfg)
	s.indexes[key] = idx
	go debug.CapturePanicReport(idx.run)
	return idx, nil
}

// Index returns the open index of root.
func (s *Service) Index(root workspaceapi.URI) (*Index, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.indexes[root.String()]
	return idx, ok
}

func (s *Service) unregister(idx *Index) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexes[idx.cfg.Root.String()] == idx {
		delete(s.indexes, idx.cfg.Root.String())
	}
}

// Search answers req from root's index, by meaning with req.Model's
// vectors when it is a downloaded local model the index was built with,
// and by words always; the status says when the index was still indexing
// or the answer was ranked by words alone. req.Model must be resolved: an
// alias is not local. It returns searchapi.ErrUnsupported for a model that
// is not local, and ErrNoIndex when root has no open index.
func (s *Service) Search(
	ctx context.Context, root workspaceapi.URI, req searchapi.SemanticRequest,
) (iterator.Iterator[searchapi.SemanticResult], searchapi.SemanticStatus, error) {
	if req.Model.Provider != llamacpp.LLMProvider {
		return nil, searchapi.SemanticStatus{}, searchapi.ErrUnsupported
	}
	idx, ok := s.Index(root)
	if !ok {
		return nil, searchapi.SemanticStatus{}, fmt.Errorf("%w: %s", ErrNoIndex, root)
	}
	hits, st, err := idx.search(ctx, req)
	if err != nil {
		return nil, searchapi.SemanticStatus{}, err
	}
	return iterator.FromSlice(hits), st, nil
}

// EmbeddingModel returns the configured embedding model, or an error
// wrapping llmapi.ErrModelNotFound that says how to download it.
func (s *Service) EmbeddingModel(ctx context.Context) (llmapi.ModelEntry, error) {
	entry, ok := s.registry.Get(ctx, s.cfg.Model)
	if !ok {
		return llmapi.ModelEntry{}, fmt.Errorf("%w: %s is not downloaded: %s",
			llmapi.ErrModelNotFound, s.cfg.Model, s.downloadHint())
	}
	return entry, nil
}

// ConfiguredModel returns the configured embedding model as a local model
// entry, downloaded or not. Search answers it by words alone until it is
// downloaded, and tells the user how to download it.
func (s *Service) ConfiguredModel() llmapi.ModelEntry {
	return llmapi.ModelEntry{Name: s.cfg.Model, Provider: llamacpp.LLMProvider}
}

// indexEmbedder is the embedder indexes are built with, or nil when the
// model is not downloaded.
func (s *Service) indexEmbedder(ctx context.Context) Embedder {
	entry, err := s.EmbeddingModel(ctx)
	if err != nil {
		return nil
	}
	return s.embed(entry)
}

// downloadHint is the command that downloads the embedding model.
func (s *Service) downloadHint() string {
	return "run `models local download " + strings.TrimPrefix(s.cfg.Model, llamacpp.DefaultRegistryHost+"/") + "`"
}

// Close closes every open index, then the closer given to New.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	indexes := make([]*Index, 0, len(s.indexes))
	for _, idx := range s.indexes {
		indexes = append(indexes, idx)
	}
	s.mu.Unlock()
	var errs []error
	for _, idx := range indexes {
		errs = append(errs, idx.Close())
	}
	return errors.Join(append(errs, s.closer.Close())...)
}
