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

package syntax

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const (
	// LocalsFunctionsFilter filters local file functions.
	LocalsFunctionsFilter = "local.definition.function"
	// LocalsMethodsFilter filters local file functions.
	LocalsMethodsFilter = "local.definition.method"
	// LocalsVariablesFilter filters local file variables.
	LocalsVariablesFilter = "local.definition.var"
	// LocalsTypesFilter filters local file types.
	LocalsTypesFilter = "local.definition.type"
)

// Match represents an AST node that matches a query.
type Match struct {
	CaptureName string
	LineString  string
	Line        int
}

// Query runs the given query and returns an iterator with the results.
func (t *Tree) Query(queryFile string, captureNames ...string) (
	iterator.Iterator[Match], error,
) {
	it, err := t.query(queryFile, captureNames...)
	if err != nil {
		return nil, err
	}
	if len(captureNames) == 0 {
		return it, nil
	}
	return iterator.Filter(it, func(m Match) bool {
		return slices.Contains(captureNames, m.CaptureName)
	}), nil
}

func (t *Tree) runQuery(queryFile string, expectedCaptureNames []string) ([]Match, error) {
	ret := make([]Match, 0)
	root := t.tree.RootNode()

	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()
	var query *tree_sitter.Query
	switch queryFile {
	case "folds.scm":
		query = t.folds
	case "indents.scm":
		query = t.indents
	case "highlights.scm":
		query = t.highlights
	case "locals.scm":
		query = t.locals
	default:
		// custom query files are expected to be relative to a workspace's path
		// and in any case, in a workspace's host. For file:// workspaces,
		// this makes no difference, but for remote workspaces, keeping the query files
		// local to the repository makes more sense, allowing users to keep custom queries
		// under source control.
		file, err := t.opener.OpenFile(queryFile, os.O_RDONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("open query file at workspace's host: %w", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("read query file: %w", err)
		}
		var qerr *tree_sitter.QueryError
		query, qerr = tree_sitter.NewQuery(t.parser.Language(), string(data))
		if qerr != nil {
			return nil, fmt.Errorf("compile query: %w", *qerr)
		}
		defer query.Close()
	}

	if query == nil {
		return nil, fmt.Errorf("could not load %s file", queryFile)
	}

	// verify expected capture names
	var err error
	captureNames := query.CaptureNames()
	for _, captureName := range expectedCaptureNames {
		_, ok := query.CaptureIndexForName(captureName)
		if !ok {
			err = multierror.Append(err, fmt.Errorf("capture name '%s' does not exist in query", captureName))
		}
	}
	if err != nil {
		return nil, err
	}

	matches := cur.Matches(query, root, []byte(t.buf.String()))
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			captureName := captureNames[cap.Index]
			n := cap.Node
			rng, ok := t.treeSitterRangeToTerm(n.Range())
			if !ok {
				t.log(log.DebugLevel, "could not convert tree sitter range %+v to term",
					n.Range())
				break
			}
			if rng.Start.Y >= t.buf.Rows() {
				continue // guard against external parser bugs
			}
			rng.End.Y = rng.Start.Y
			rng.End.X = t.buf.Columns(rng.Start.Y)
			rng.Start.X = 0
			cells, _, _ := t.buf.Select(rng.Start, rng.End)
			ret = append(ret, Match{
				Line:        rng.Start.Y,
				CaptureName: captureName,
				LineString:  term.CellsToString(cells),
			})
		}
	}
	return ret, nil
}

type nodesIterator struct {
	ready              chan struct{}
	tree               *Tree
	err                error
	slice              iterator.Iterator[Match]
	queryFile          string
	expectedQueryNames []string
}

func newNodesIterator(
	t *Tree, ch chan struct{}, queryFile string, expectedQueryNames ...string,
) *nodesIterator {
	return &nodesIterator{
		tree:               t,
		ready:              ch,
		queryFile:          queryFile,
		expectedQueryNames: expectedQueryNames,
	}
}

func (f *nodesIterator) Next(ctx context.Context) (Match, bool) {
	if f.slice != nil {
		return f.slice.Next(ctx)
	}

	select {
	case <-f.ready:
	case <-ctx.Done():
		f.err = ctx.Err()
		return Match{}, false
	}
	// the Tree may have been closed between the ready signal and
	// this first Next; its native tree-sitter objects are freed
	// under mu, so re-check closed before touching them.
	f.tree.mu.Lock()
	if f.tree.closed {
		f.tree.mu.Unlock()
		f.err = errors.New("tree is closed")
		return Match{}, false
	}
	if f.tree.tree == nil {
		f.tree.mu.Unlock()
		f.err = errors.New("could not parse tree: could not find " +
			"parser in language package or there was a critical parser error")
		return Match{}, false
	}
	data, err := f.tree.runQuery(f.queryFile, f.expectedQueryNames)
	f.tree.mu.Unlock()
	if err != nil {
		f.err = err
		return Match{}, false
	}
	f.slice = iterator.FromSlice(data)

	return f.slice.Next(ctx)
}

func (f *nodesIterator) Err() error {
	if f.err != nil {
		return f.err
	}
	if f.slice == nil {
		return nil
	}
	return f.slice.Err()
}

func (f *nodesIterator) Close() error {
	return nil
}
