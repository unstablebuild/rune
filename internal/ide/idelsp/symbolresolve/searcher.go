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

package symbolresolve

import (
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// MultiQuery is one tree-sitter query in a SearchMulti batch. ID tags the
// query so results from a single shared workspace walk can be demultiplexed
// back to the query that produced them.
type MultiQuery struct {
	ID       int
	Query    string
	Captures []string
	// Nodes selects the built-in node-capture query instead of Query.
	// Node patterns capture independent definitions, so each captured
	// node is emitted as its own single-capture match rather than being
	// grouped into a tuple.
	Nodes syntaxapi.NodeCaptureName
	// Lang, when non-empty, runs the query against the regions of that
	// language that the file's grammar marks through its injections
	// query (a <script> block, say) instead of against the file's own
	// syntax tree. Each region is parsed on its own with the named
	// language's grammar, in document coordinates. Files whose grammar
	// has no injections query, and languages that are not installed,
	// yield no matches for such queries.
	Lang string
}

// MultiResult is a single SearchMulti match tagged with the ID of the
// MultiQuery that produced it. Match holds the match's captures in the
// query's declared capture order, so consumers never re-pair captures
// downstream.
type MultiResult struct {
	QueryID int
	Match   []syntaxapi.Result
}

// Searcher is the subset of the workspace parser that symbol resolution
// needs. SearchMulti runs several queries against a single per-file parse
// so resolution issues one workspace walk instead of one per query.
type Searcher interface {
	// Search runs a single tree-sitter query across the workspace,
	// optionally restricted to the given languages.
	Search(query string, captureNames []string, languages ...string) (
		iterator.Iterator[syntaxapi.Result], error,
	)
	// Search2 runs a two-capture tree-sitter query across the workspace
	// and streams each match's captures as a pair in captureNames order.
	Search2(query string, captureNames [2]string, languages ...string) (
		iterator.Iterator[[2]syntaxapi.Result], error,
	)
	// SearchMulti runs every query against one shared parse per file and
	// streams tagged matches, optionally restricted to the given languages.
	SearchMulti(queries []MultiQuery, languages ...string) (
		iterator.Iterator[MultiResult], error,
	)
	// SearchNode runs the built-in node-capture query across the workspace,
	// optionally restricted to the given languages.
	SearchNode(nodeTypes syntaxapi.NodeCaptureName, languages ...string) (
		iterator.Iterator[syntaxapi.Result], error,
	)
	// QueryNode runs the built-in node-capture query against a single file.
	QueryNode(file workspaceapi.URI, nodeTypes syntaxapi.NodeCaptureName) (
		iterator.Iterator[syntaxapi.Result], error,
	)
	// Query2 runs a two-capture tree-sitter query against a single file
	// and streams each match's captures as a pair in captureNames order.
	Query2(file workspaceapi.URI, query string, captureNames [2]string) (
		iterator.Iterator[[2]syntaxapi.Result], error,
	)
}
