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
	"os"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// QualifierContext carries the workspace information a Spec's Qualifier
// may consult when deriving a file's qualifier, such as whether package
// marker files exist in parent directories.
type QualifierContext struct {
	// Root is the workspace root URI.
	Root workspaceapi.URI
	// Exists reports whether a workspace-relative path exists. It must
	// be safe for concurrent use and is non-nil in every context built
	// by NewQualifierContext. Qualifiers only consult it for files
	// under Root, so a zero context (whose Root contains nothing)
	// never invokes it.
	Exists func(rel string) bool
}

// StatFS is the filesystem slice NewQualifierContext consults.
// workspaceapi.FileSystem satisfies it.
type StatFS interface {
	Stat(path string) (os.FileInfo, error)
}

// NewQualifierContext returns a QualifierContext over fs rooted at
// root, memoizing existence checks for the context's lifetime. The
// memo is goroutine-safe but never invalidated, so build one context
// per scan or resolution pass rather than caching it long-term.
// Panics when fs is nil: callers without a filesystem must not build a
// context.
func NewQualifierContext(fs StatFS, root workspaceapi.URI) QualifierContext {
	if fs == nil {
		panic("symbolresolve: NewQualifierContext requires a filesystem")
	}
	var cache sync.Map
	exists := func(rel string) bool {
		if v, ok := cache.Load(rel); ok {
			return v.(bool)
		}
		_, err := fs.Stat(rel)
		exists := err == nil
		cache.Store(rel, exists)
		return exists
	}
	return QualifierContext{Root: root, Exists: exists}
}

// relPath returns uri's path relative to the context root, or "" when
// uri lies outside it.
func (qc QualifierContext) relPath(uri workspaceapi.URI) string {
	rel := workspaceapi.RelPath(qc.Root, uri)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// RefQuery is a tree-sitter query that captures a package-qualified
// reference. Captures[0] must capture the package alias and Captures[1]
// the referenced symbol name.
type RefQuery struct {
	Query    string
	Captures []string

	// RequireImport restricts matches to files that import the captured
	// package alias. Used by the completer to drop dot-imported or
	// same-package references that would otherwise look package-qualified.
	RequireImport bool
}

// Spec describes how to resolve and complete package-qualified symbol
// names for one language. The tree-sitter queries it carries are scoped
// to LangID by the parser, so applying a Spec to a workspace that
// contains no files of that language is a cheap no-op.
type Spec struct {
	// LangID is the tree-sitter language identifier (e.g. "go",
	// "python") used to scope parser.Search.
	LangID string

	// RefQueries are run in order to find package-qualified references
	// (qualified types, selector/attribute access, …).
	RefQueries []RefQuery

	// PackageClauseQuery, when non-empty, derives the package name a
	// file declares. Captures[0] of the query must capture the package
	// identifier. When empty, Qualifier is used to derive the
	// bare-definition prefix instead.
	PackageClauseQuery    string
	PackageClauseCaptures []string

	// ImportPathQuery and ImportAliasQuery support import-aware
	// deduplication. When ImportPathQuery is empty, dedup-by-import is
	// skipped.
	ImportPathQuery     string
	ImportPathCaptures  []string
	ImportAliasQuery    string
	ImportAliasCaptures []string

	// IsExported reports whether name can be the target of a qualified
	// reference from another package. It filters reference sites only;
	// definitions are indexed regardless of visibility. A nil predicate
	// accepts every name.
	IsExported func(string) bool

	// MethodDefQuery captures method definitions as (receiverType,
	// methodName) pairs for resolving pkg.Type.Method names. Captures[0]
	// is the receiver type, Captures[1] the method name. Empty disables
	// method resolution for this language.
	MethodDefQuery    string
	MethodDefCaptures []string

	// NestedModules marks languages whose qualifiers are dotted module
	// paths (e.g. Python's "pkg.sub.mod"). Resolution then accepts
	// names with any number of leading module segments, matching them
	// against dotted suffixes of each file's module path.
	NestedModules bool

	// ReexportQuery, when non-empty, captures names bound by import
	// statements in the files listed in ReexportFiles; those bindings
	// are indexed as definitions under the file's qualifier (e.g. a
	// Python package __init__.py re-exporting a private module's
	// class). Captures[0] must capture the bound name.
	ReexportQuery    string
	ReexportCaptures []string
	// ReexportFiles lists the base file names whose import bindings
	// count as re-exports.
	ReexportFiles []string

	// Qualifier returns the prefix applied to bare definition names
	// during the definitions phase when the language has no package
	// clause (e.g. a Python module name derived from the file path).
	// A language with a package clause may also set it to qualify the
	// files that declare none, such as C++ sources outside any
	// namespace; files with a clause keep the declared package.
	Qualifier func(qc QualifierContext, uri string) string

	// DisplayPathFromURI derives a human-readable display prefix from a
	// file URI, used when disambiguating matches.
	DisplayPathFromURI func(uri string) string

	// Extensions lists the file-name suffixes (including the dot) that
	// belong to this language. The definitions phase uses them to scope
	// the cross-language SearchNode/QueryNode results to this spec's
	// files. Empty means no extension filtering.
	Extensions []string

	// Injected lists the specs applied to the regions of other languages
	// that this language's grammar marks through its injections query,
	// each identified by its LangID.
	Injected []*Spec
}

// exported reports whether name passes the spec's export predicate. A
// nil predicate accepts every name.
func (s *Spec) exported(name string) bool {
	if s.IsExported == nil {
		return true
	}
	return s.IsExported(name)
}

// hasPackages reports whether the spec derives qualifiers from a package
// clause rather than from the file path.
func (s *Spec) hasPackages() bool {
	return s.PackageClauseQuery != ""
}

// hasMethods reports whether the spec can resolve pkg.Type.Method names.
func (s *Spec) hasMethods() bool {
	return s.MethodDefQuery != ""
}

// hasReexports reports whether the spec indexes import bindings in
// designated files as definitions.
func (s *Spec) hasReexports() bool {
	return s.ReexportQuery != "" && len(s.ReexportFiles) > 0
}

// isReexportFile reports whether uri's base name is one of the spec's
// re-export files.
func (s *Spec) isReexportFile(uri string) bool {
	return slices.Contains(s.ReexportFiles, path.Base(uri))
}

// isHost reports whether the spec extracts symbols through injected
// specs rather than its own queries.
func (s *Spec) isHost() bool {
	return len(s.Injected) > 0
}

// HasImports reports whether the spec, or any spec it injects, resolves
// import aliases, so that matches can be deduplicated by import path.
func (s *Spec) HasImports() bool {
	if s.ImportPathQuery != "" {
		return true
	}
	for _, sub := range s.Injected {
		if sub.HasImports() {
			return true
		}
	}
	return false
}

// querySource is one spec whose queries join a file's extraction batch:
// the file's own spec, or an injected spec applied to regions of lang.
type querySource struct {
	spec *Spec
	lang string
}

// querySources returns the specs whose queries extract symbols from a
// file of this spec: the spec itself, or for a host the injected specs,
// each bound to the regions of its own language.
func (s *Spec) querySources() []querySource {
	if !s.isHost() {
		return []querySource{{spec: s}}
	}
	sources := make([]querySource, len(s.Injected))
	for i, sub := range s.Injected {
		sources[i] = querySource{spec: sub, lang: sub.LangID}
	}
	return sources
}

// qualifier returns the bare-definition prefix for a file URI when the
// spec has no package clause; it returns "" when Qualifier is nil.
func (s *Spec) qualifier(qc QualifierContext, uri string) string {
	if s.Qualifier == nil {
		return ""
	}
	return s.Qualifier(qc, uri)
}

// displayPath derives the display prefix for a file URI, falling back to
// the raw URI when DisplayPathFromURI is nil.
func (s *Spec) displayPath(uri string) string {
	if s.DisplayPathFromURI == nil {
		return uri
	}
	return s.DisplayPathFromURI(uri)
}

// matchesFile reports whether uri belongs to this language according to
// its extensions. An empty Extensions list matches every file.
func (s *Spec) matchesFile(uri string) bool {
	if len(s.Extensions) == 0 {
		return true
	}
	for _, ext := range s.Extensions {
		if strings.HasSuffix(uri, ext) {
			return true
		}
	}
	return false
}

// moduleSuffixes returns every dotted suffix of module, from the full
// path to the last segment: "a.b.c" → ["a.b.c", "b.c", "c"].
func moduleSuffixes(module string) []string {
	suffixes := []string{module}
	for {
		i := strings.IndexByte(module, '.')
		if i < 0 {
			return suffixes
		}
		module = module[i+1:]
		suffixes = append(suffixes, module)
	}
}

// modulePathHasSuffix reports whether query equals full or a
// segment-boundary dotted suffix of it. An empty query matches any
// module path: an unqualified name is not scoped to a module.
func modulePathHasSuffix(full, query string) bool {
	if query == "" {
		return true
	}
	return full == query || strings.HasSuffix(full, "."+query)
}
