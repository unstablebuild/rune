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

package ide

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/llmrouter"
	"unstable.build/rune/internal/llm/llmsearch"
)

// regexpSearcher searches a workspace's files for a regular expression,
// with workspace-relative roots; *codesearch.Index is one.
type regexpSearcher interface {
	RegexpSearch(ctx context.Context, req searchapi.RegexpRequest) (
		iterator.Iterator[searchapi.RegexpResult], error)
}

// noRegexpSearch is the regexp search of a workspace without an index,
// such as the home workspace: it supports none.
type noRegexpSearch struct{}

func (noRegexpSearch) RegexpSearch(context.Context, searchapi.RegexpRequest) (
	iterator.Iterator[searchapi.RegexpResult], error,
) {
	return nil, fmt.Errorf("this workspace has no search index: %w", searchapi.ErrUnsupported)
}

// workspaceSearcher implements searchapi.Searcher for one workspace:
// regexp searches through its trigram index, semantic ones through the
// router's index of it.
type workspaceSearcher struct {
	// root is the workspace, as its indexes know it.
	root workspaceapi.URI
	// dir is the workspace's folder on its host, which absolute roots
	// must be in.
	dir    string
	regexp regexpSearcher
	router *llmrouter.Router
}

var _ searchapi.Searcher = (*workspaceSearcher)(nil)

// newWorkspaceSearcher returns the searcher of the workspace root, whose
// folder on its host is dir. Every argument is required.
func newWorkspaceSearcher(
	root workspaceapi.URI, dir string, regexp regexpSearcher, router *llmrouter.Router,
) *workspaceSearcher {
	if dir == "" || regexp == nil || router == nil {
		panic("newWorkspaceSearcher: dir, regexp and router are required")
	}
	return &workspaceSearcher{root: root, dir: dir, regexp: regexp, router: router}
}

// RegexpSearch implements searchapi.Searcher.
func (s *workspaceSearcher) RegexpSearch(
	ctx context.Context, req searchapi.RegexpRequest,
) (iterator.Iterator[searchapi.RegexpResult], error) {
	roots, err := s.relRoots(req.Roots)
	if err != nil {
		return nil, err
	}
	req.Roots = roots
	return s.regexp.RegexpSearch(ctx, req)
}

// SemanticSearch implements searchapi.Searcher. A workspace without an
// open index supports none: the error wraps both searchapi.ErrUnsupported
// and llmsearch.ErrNoIndex.
func (s *workspaceSearcher) SemanticSearch(
	ctx context.Context, req searchapi.SemanticRequest,
) (iterator.Iterator[searchapi.SemanticResult], error) {
	roots, err := s.relRoots(req.Roots)
	if err != nil {
		return nil, err
	}
	req.Roots = roots
	it, err := s.router.SemanticSearch(ctx, s.root, req)
	if errors.Is(err, llmsearch.ErrNoIndex) {
		return nil, fmt.Errorf("%w: %w", searchapi.ErrUnsupported, err)
	}
	return it, err
}

// relRoots returns roots relative to the workspace, slash-separated. A
// root outside the workspace is searchapi.ErrUnsupported: its files are
// in no index.
func (s *workspaceSearcher) relRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return roots, nil
	}
	out := make([]string, len(roots))
	for i, root := range roots {
		rel := root
		if filepath.IsAbs(root) {
			var err error
			if rel, err = filepath.Rel(s.dir, root); err != nil {
				return nil, fmt.Errorf("search: root %s: %w", root, err)
			}
		}
		rel = path.Clean(filepath.ToSlash(rel))
		if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
			return nil, fmt.Errorf("root %s is outside the workspace %s: %w",
				root, s.dir, searchapi.ErrUnsupported)
		}
		out[i] = rel
	}
	return out, nil
}
