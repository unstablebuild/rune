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
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax/treesitter"
)

// uses lazyly initialized syntaxapi.Parser to circumvent
// cosmetic circular dependency between pkgmanager and treesitter.NewParser
type lazyParser struct {
	root *workspaceManagerHandler
	once sync.Once
	p    syntaxapi.Parser
}

var _ syntaxapi.Parser = (*lazyParser)(nil)

func (w *lazyParser) parser() syntaxapi.Parser {
	w.once.Do(func() {
		w.p = treesitter.NewParser(w.root.homeWorkspace, w.root.pkgmanager, w.root.homeURI)
	})
	return w.p
}

func (w *lazyParser) Search(query string, captureNames []string, languages ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return nil, errors.New("parser not supported on home workspace")
}

// SearchNode is implemented with Search by using an internally provided
// query that is able to capture a known set of tree nodes across programming
// languages. Multiple node types can be combined using bitwise OR.
func (w *lazyParser) SearchNode(
	nodeTypes syntaxapi.NodeCaptureName, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return nil, errors.New("parser not supported on home workspace")
}

// Query searches for matches in a specific file using the given tree-sitter
// literal query and a list of capture names that should be returned.
func (w *lazyParser) Query(file workspaceapi.URI, query string, captureNames []string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return nil, errors.New("parser not supported on home workspace")
}

// QueryNode is implemented with Query by using an internally provided
// query that is able to capture a known set of tree nodes across
// programming languages. Multiple node types can be combined using bitwise OR.
func (w *lazyParser) QueryNode(
	file workspaceapi.URI, nodeTypes syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	return nil, errors.New("parser not supported on home workspace")
}

// Highlight returns syntax highlighting locations for the given content,
// interpreted as belonging to the file identified by uri.
func (w *lazyParser) Highlight(uri workspaceapi.URI, content string) (
	iterator.Iterator[textapi.Location], error,
) {
	return w.parser().Highlight(uri, content)
}

// ResolveSymbol resolves a dotted symbol name to its declaration and
// reference locations.
func (w *lazyParser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	return nil, errors.New("parser not supported on home workspace")
}

// ListReferencedSymbols returns an empty iterator; the home workspace has no
// symbol index.
func (w *lazyParser) ListReferencedSymbols(
	ctx context.Context,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

type currentParser struct {
	root *workspaceManagerHandler
}

var _ syntaxapi.Parser = currentParser{}

func (c currentParser) parser() syntaxapi.Parser {
	if c.root == nil {
		return nil
	}
	ex := c.root.focusEx()
	if ex == nil {
		return nil
	}
	return ex.parser
}

var errNoParser = errors.New("no focused parser")

func (c currentParser) Search(
	query string, captureNames []string, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.Search(query, captureNames, languages...)
}

func (c currentParser) SearchNode(
	node syntaxapi.NodeCaptureName, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.SearchNode(node, languages...)
}

func (c currentParser) Query(
	file workspaceapi.URI, query string, captureNames []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.Query(file, query, captureNames)
}

func (c currentParser) QueryNode(
	file workspaceapi.URI, node syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.QueryNode(file, node)
}

func (c currentParser) Highlight(
	uri workspaceapi.URI, content string,
) (iterator.Iterator[textapi.Location], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.Highlight(uri, content)
}

func (c currentParser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.ResolveSymbol(ctx, name, progress)
}

func (c currentParser) ListReferencedSymbols(
	ctx context.Context,
) (iterator.Iterator[string], error) {
	p := c.parser()
	if p == nil {
		return nil, errNoParser
	}
	return p.ListReferencedSymbols(ctx)
}
