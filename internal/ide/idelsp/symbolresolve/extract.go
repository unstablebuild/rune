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
	"context"
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// FileQueryer runs batched per-file queries for symbol extraction:
// every query in a batch runs against a single parse of the file.
// Consumers such as ExtractFile borrow the queryer and do not own its
// lifecycle; see QuerySession for the owned form.
type FileQueryer interface {
	// QueryMulti runs every query against a single parse of file,
	// returning each match's captures grouped per the query's contract.
	QueryMulti(ctx context.Context, file workspaceapi.URI, queries []MultiQuery) (
		[]MultiResult, error,
	)
}

// QuerySession is an owned FileQueryer: implementations cache loaded
// languages and compiled query batches across calls, so the creator
// must Close it to release them. Sessions need not be safe for
// concurrent use; create one per goroutine.
type QuerySession interface {
	FileQueryer
	io.Closer
}

// SymbolKind classifies one extracted symbol occurrence.
type SymbolKind int

const (
	// SymbolRef is a package-qualified reference site.
	SymbolRef SymbolKind = iota
	// SymbolDef is a function or type definition.
	SymbolDef
	// SymbolMethodDef is a method definition, named pkg.Type.Method.
	SymbolMethodDef
)

// FileSymbol is one symbol occurrence extracted from a file.
type FileSymbol struct {
	// Name is the qualified symbol name: pkg.Sym for references and
	// definitions, pkg.Type.Method for method definitions.
	Name string
	Pos  term.Coordinates
	Kind SymbolKind
}

// FileExtraction is one file's contribution to a symbol index.
type FileExtraction struct {
	Symbols []FileSymbol
	// Imports maps package aliases in scope to their import paths.
	Imports map[string]string
}

// queryRole identifies what a query in an extraction batch captures.
type queryRole int

const (
	roleRef queryRole = iota
	rolePkg
	rolePath
	roleAlias
	roleDefs
	roleMethod
	roleReexport
)

// queryTag records the source and role of one batch query; a batch's
// tags are indexed by query ID.
type queryTag struct {
	src  querySource
	role queryRole
	// ref indexes src.spec.RefQueries for roleRef queries.
	ref int
}

// ExtractFile computes one file's contribution to a symbol index by
// running every query the spec needs in one batch against a single
// parse of the file. It applies the same rules as the workspace-wide
// resolver: references are filtered by the spec's export predicate
// and, when a reference query requires it, by the file's resolved
// imports; definitions are qualified by the package clause (or the
// spec's path-derived qualifier) regardless of visibility; method
// definitions are emitted as pkg.Type.Method. Definitions of
// nested-module specs are emitted once per dotted suffix of the
// file's module path, so a symbol is addressable by any trailing run
// of module segments. Re-export bindings in the spec's re-export
// files are emitted as definitions under the same qualifiers. For a
// host spec the batch carries each injected spec's queries bound to
// the regions of its language, and the host's qualifier names the
// symbols they extract.
func ExtractFile(
	ctx context.Context, parser FileQueryer, spec *Spec, qc QualifierContext,
	file workspaceapi.URI,
) (FileExtraction, error) {
	var queries []MultiQuery
	var tags []queryTag
	add := func(src querySource, role queryRole, ref int, q MultiQuery) {
		q.ID, q.Lang = len(tags), src.lang
		queries = append(queries, q)
		tags = append(tags, queryTag{src: src, role: role, ref: ref})
	}
	for _, src := range spec.querySources() {
		s := src.spec
		for i, rq := range s.RefQueries {
			add(src, roleRef, i, MultiQuery{Query: rq.Query, Captures: rq.Captures})
		}
		// A package clause names the file's module, which an injected
		// region cannot do.
		if src.lang == "" && s.hasPackages() {
			add(src, rolePkg, 0, MultiQuery{
				Query: s.PackageClauseQuery, Captures: s.PackageClauseCaptures,
			})
		}
		if s.ImportPathQuery != "" {
			add(src, rolePath, 0, MultiQuery{
				Query: s.ImportPathQuery, Captures: s.ImportPathCaptures,
			})
			add(src, roleAlias, 0, MultiQuery{
				Query: s.ImportAliasQuery, Captures: s.ImportAliasCaptures,
			})
		}
		add(src, roleDefs, 0, MultiQuery{
			Nodes: syntaxapi.NodeCaptureDefinitionFunc | syntaxapi.NodeCaptureDefinitionType,
		})
		if s.hasMethods() {
			add(src, roleMethod, 0, MultiQuery{
				Query: s.MethodDefQuery, Captures: s.MethodDefCaptures,
			})
		}
		// The re-export query joins the batch for every file of the
		// language — not only re-export files — so sessions that cache
		// the compiled batch per language never recompile it; results
		// from other files are dropped below.
		if s.hasReexports() {
			add(src, roleReexport, 0, MultiQuery{
				Query: s.ReexportQuery, Captures: s.ReexportCaptures,
			})
		}
	}

	results, err := parser.QueryMulti(ctx, file, queries)
	if err != nil {
		return FileExtraction{}, err
	}

	// References need the resolved imports and definitions need the
	// package qualifier, so reduce those first. A declared package
	// clause overrides the path-derived qualifier.
	pkg := spec.qualifier(qc, file.String())
	var paths map[workspaceapi.URI][]string
	var explicit map[workspaceapi.URI]map[string]string
	for _, r := range results {
		switch tags[r.QueryID].role {
		case rolePkg:
			pkg = r.Match[0].Text
		case rolePath:
			paths, _ = reduceImportPaths(paths, r.Match[0])
		case roleAlias:
			explicit, _ = reduceImportAliases(
				explicit, [2]syntaxapi.Result{r.Match[0], r.Match[1]})
		}
	}
	var ext FileExtraction
	// Single-file batch: take the sole entry rather than keying by the
	// caller's URI, which may not compare equal to parser-built ones.
	for _, aliases := range resolveAliases(paths, explicit) {
		ext.Imports = aliases
	}

	var pkgs []string
	if pkg != "" {
		pkgs = moduleSuffixes(pkg)
	}
	for _, r := range results {
		tag := tags[r.QueryID]
		s := tag.src.spec
		switch tag.role {
		case roleRef:
			p0, p1 := r.Match[0], r.Match[1]
			if !s.exported(p1.Text) {
				continue
			}
			if s.RefQueries[tag.ref].RequireImport && ext.Imports[p0.Text] == "" {
				continue
			}
			ext.Symbols = append(ext.Symbols, FileSymbol{
				Name: p0.Text + "." + p1.Text, Pos: p1.From, Kind: SymbolRef,
			})
		case roleDefs:
			if pkg == "" {
				continue
			}
			for _, q := range pkgs {
				ext.Symbols = append(ext.Symbols, FileSymbol{
					Name: q + "." + r.Match[0].Text,
					Pos:  r.Match[0].From,
					Kind: SymbolDef,
				})
			}
		case roleMethod:
			if pkg == "" {
				continue
			}
			for _, q := range pkgs {
				ext.Symbols = append(ext.Symbols, FileSymbol{
					Name: q + "." + r.Match[0].Text + "." + r.Match[1].Text,
					Pos:  r.Match[1].From,
					Kind: SymbolMethodDef,
				})
			}
			// The class always stays attached to the method; only the
			// module prefix is dropped, so a bare Class.method still
			// addresses this definition.
			if spec.NestedModules {
				ext.Symbols = append(ext.Symbols, FileSymbol{
					Name: r.Match[0].Text + "." + r.Match[1].Text,
					Pos:  r.Match[1].From,
					Kind: SymbolMethodDef,
				})
			}
		case roleReexport:
			if pkg == "" || !s.isReexportFile(file.Path()) {
				continue
			}
			for _, q := range pkgs {
				ext.Symbols = append(ext.Symbols, FileSymbol{
					Name: q + "." + r.Match[0].Text,
					Pos:  r.Match[0].From,
					Kind: SymbolDef,
				})
			}
		}
	}
	return ext, nil
}
