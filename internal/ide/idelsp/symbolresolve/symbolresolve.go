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

// Package symbolresolve resolves package-qualified symbol names
// (e.g. "iterator.Iterator") to their declaration locations using
// tree-sitter queries against the workspace. Language-specific
// queries and rules live behind a per-language Spec.
package symbolresolve

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// Resolve resolves a package-qualified symbol name (e.g.
// "iterator.Iterator") to one or more declaration locations. It tries each
// language spec yielded by specs in turn — beginning as soon as the first spec
// arrives — and returns the first spec that produces matches. Results are
// deduplicated by file URI and collapsed by import path; when multiple distinct
// packages remain, each Match Display name is prefixed to disambiguate.
// progress, if non-nil, receives per-phase updates. Returns ErrNoDot when name
// contains no ".".
func Resolve(
	ctx context.Context, parser Searcher, qc QualifierContext,
	specs iterator.Iterator[Spec], name string, progress syntaxapi.Progress,
) ([]syntaxapi.Match, error) {
	if !strings.Contains(name, ".") {
		return nil, syntaxapi.ErrNoDot
	}
	defer func() { _ = specs.Close() }()

	var lastErr error
	for {
		spec, ok := specs.Next(ctx)
		if !ok {
			break
		}
		matches, err := resolveSpec(ctx, parser, &spec, qc, name, progress)
		if err != nil {
			lastErr = err
			continue
		}
		if len(matches) > 0 {
			return matches, nil
		}
	}
	if err := specs.Err(); err != nil {
		return nil, err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no symbols found for %q", name)
}

// resolveSpec runs the resolution phases for a single language spec.
// Host specs yield nothing: their symbols live in injected regions that
// only the per-file extraction batch can address.
func resolveSpec(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	name string, progress syntaxapi.Progress,
) ([]syntaxapi.Match, error) {
	if spec.isHost() {
		return nil, nil
	}
	report := func(msg string, found int, step, total int64) {
		if progress != nil {
			progress.Report(msg, found, step, total)
		}
	}

	pkg, typeName, sym, isMethod, ok := splitQualifiedName(spec, name)
	if !ok {
		return nil, nil
	}
	if isMethod {
		return resolveMethod(ctx, parser, spec, qc, pkg, typeName, sym, name, report)
	}

	// The reference queries capture single-identifier qualifiers, so a
	// dotted module path can only match through the definitions phase.
	var collected passResult
	if !strings.Contains(pkg, ".") {
		report("Searching references…", 0, 0, 4)
		var err error
		collected, err = runResolvePass(ctx, parser, spec, pkg, sym, name)
		if err != nil {
			return nil, err
		}
	}
	matches := collected.matches

	if len(matches) == 0 {
		report("Searching definitions…", 0, 2, 4)
		seen := make(map[string]bool)
		add := func(uri string, pos term.Coordinates) {
			if seen[uri] {
				return
			}
			seen[uri] = true
			matches = append(matches, syntaxapi.Match{URI: uri, Pos: pos, Display: name})
		}
		if err := collectDefinitions(
			ctx, parser, spec, qc, collected.packages, pkg, sym, add,
		); err != nil {
			return nil, err
		}
	}

	if len(matches) == 0 {
		// Nested-module names carry no marker distinguishing a symbol
		// from a method, so fall back to the pkg.Type.method reading.
		if mpkg, typeName, method, ok := splitNestedMethodName(spec, name); ok {
			return resolveMethod(
				ctx, parser, spec, qc, mpkg, typeName, method, name, report)
		}
		return nil, nil
	}
	if len(matches) > 1 && spec.ImportPathQuery != "" {
		report("Resolving imports…", len(matches), 3, 4)
		importPaths := resolveAliases(collected.imports, collected.explicitAliases)
		matches = deduplicateMatchesByImport(matches, pkg, importPaths)
	}
	if len(matches) > 1 {
		disambiguateDisplayNames(spec, matches, name)
	}
	return matches, nil
}

// splitQualifiedName splits a dotted name into its segments. For most
// languages a 2-part name (pkg.sym) is a plain symbol, a 3-part name
// (pkg.Type.method) is a method, and longer names are not resolvable.
// Nested-module specs instead read every name as modpath.sym — the
// method interpretation is tried separately when that yields nothing
// (see splitNestedMethodName).
func splitQualifiedName(
	spec *Spec, name string,
) (pkg, typeName, sym string, isMethod, ok bool) {
	parts := strings.Split(name, ".")
	if spec.NestedModules {
		if len(parts) < 2 {
			return "", "", "", false, false
		}
		last := len(parts) - 1
		return strings.Join(parts[:last], "."), "", parts[last], false, true
	}
	switch len(parts) {
	case 2:
		return parts[0], "", parts[1], false, true
	case 3:
		return parts[0], parts[1], parts[2], true, true
	default:
		return "", "", "", false, false
	}
}

// splitNestedMethodName reinterprets a nested-module name as
// modpath.Type.method, where modpath may be empty for a bare
// Type.method name (matched in any module). It reports ok=false for
// specs without nested modules or names with fewer than two segments.
func splitNestedMethodName(
	spec *Spec, name string,
) (pkg, typeName, method string, ok bool) {
	if !spec.NestedModules {
		return "", "", "", false
	}
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return "", "", "", false
	}
	last := len(parts) - 1
	return strings.Join(parts[:last-1], "."), parts[last-1], parts[last], true
}

// resolveMethod resolves a pkg.Type.method name through the definitions
// phase alone: call-site references are out of scope. It yields nothing
// when the spec cannot resolve methods.
func resolveMethod(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	pkg, typeName, method, name string,
	report func(msg string, found int, step, total int64),
) ([]syntaxapi.Match, error) {
	if !spec.hasMethods() {
		return nil, nil
	}
	report("Searching definitions…", 0, 0, 1)

	packages, err := FilePackages(ctx, parser, spec)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var matches []syntaxapi.Match
	add := func(uri string, pos term.Coordinates) {
		if seen[uri] {
			return
		}
		seen[uri] = true
		matches = append(matches, syntaxapi.Match{URI: uri, Pos: pos, Display: name})
	}
	if err := collectMethodDefinitions(
		ctx, parser, spec, qc, packages, pkg, typeName, method, add,
	); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		disambiguateDisplayNames(spec, matches, name)
	}
	return matches, nil
}

// passResult holds everything a single SearchMulti pass collects for a spec:
// the deduplicated reference matches plus the file→package, file→imports and
// file→path→alias maps consumed from the other queries in the same pass.
type passResult struct {
	matches         []syntaxapi.Match
	packages        map[workspaceapi.URI]string
	imports         map[workspaceapi.URI][]string
	explicitAliases map[workspaceapi.URI]map[string]string
}

// runResolvePass runs every query a spec needs (references, and when present
// the package clause and import path/alias queries) in a single SearchMulti
// walk, then demultiplexes the shared stream and consumes each sub-stream
// concurrently. Reference matches are filtered to pkg.sym; the remaining
// sub-streams are reduced into the maps used by the definitions and import
// dedup phases.
func runResolvePass(
	ctx context.Context, parser Searcher, spec *Spec, pkg, sym, name string,
) (passResult, error) {
	var queries []MultiQuery
	id := 0
	nextID := func() int { n := id; id++; return n }

	refIDs := make([]int, len(spec.RefQueries))
	for i, rq := range spec.RefQueries {
		refIDs[i] = nextID()
		queries = append(queries, MultiQuery{ID: refIDs[i], Query: rq.Query, Captures: rq.Captures})
	}
	pkgID := -1
	if spec.hasPackages() {
		pkgID = nextID()
		queries = append(queries, MultiQuery{
			ID: pkgID, Query: spec.PackageClauseQuery, Captures: spec.PackageClauseCaptures,
		})
	}
	pathID, aliasID := -1, -1
	if spec.ImportPathQuery != "" {
		pathID = nextID()
		queries = append(queries, MultiQuery{
			ID: pathID, Query: spec.ImportPathQuery, Captures: spec.ImportPathCaptures,
		})
		aliasID = nextID()
		queries = append(queries, MultiQuery{
			ID: aliasID, Query: spec.ImportAliasQuery, Captures: spec.ImportAliasCaptures,
		})
	}
	// A language without qualified references, such as C, has nothing
	// to collect here; skip the workspace walk the pass would cost.
	if len(queries) == 0 {
		return passResult{}, nil
	}

	it, err := parser.SearchMulti(queries, spec.LangID)
	if err != nil {
		return passResult{}, err
	}
	ids := make([]int, 0, len(queries))
	for _, q := range queries {
		ids = append(ids, q.ID)
	}
	subs := splitByQuery(it, ids)

	var (
		mu   sync.Mutex
		seen = make(map[string]bool)
		res  passResult
		wg   sync.WaitGroup
		errs = make([]error, len(ids))
	)
	add := func(uri string, pos term.Coordinates) {
		mu.Lock()
		defer mu.Unlock()
		if seen[uri] {
			return
		}
		seen[uri] = true
		res.matches = append(res.matches, syntaxapi.Match{URI: uri, Pos: pos, Display: name})
	}

	// Each query ID is dense in [0, len(ids)) so it doubles as the index into
	// errs, letting consumers record failures without shared bookkeeping.
	consume := func(id int, fn func() error) {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			errs[id] = fn()
		})
	}

	for _, refID := range refIDs {
		sub := subs[refID]
		consume(refID, func() error {
			return collectRefPairs(ctx, sub, pkg, sym, add)
		})
	}
	if pkgID >= 0 {
		sub := subs[pkgID]
		consume(pkgID, func() error {
			packages, perr := iterator.Reduce(ctx, firsts(sub), reducePackages)
			mu.Lock()
			res.packages = packages
			mu.Unlock()
			return perr
		})
	}
	if pathID >= 0 {
		sub := subs[pathID]
		consume(pathID, func() error {
			imports, perr := iterator.Reduce(ctx, firsts(sub), reduceImportPaths)
			mu.Lock()
			res.imports = imports
			mu.Unlock()
			return perr
		})
	}
	if aliasID >= 0 {
		sub := subs[aliasID]
		consume(aliasID, func() error {
			aliases, perr := iterator.Reduce(ctx, pairs(sub), reduceImportAliases)
			mu.Lock()
			res.explicitAliases = aliases
			mu.Unlock()
			return perr
		})
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return passResult{}, e
		}
	}
	return res, nil
}

// collectRefPairs consumes one demultiplexed reference sub-stream and
// feeds matches for pkg.sym to add.
func collectRefPairs(
	ctx context.Context, sub iterator.Iterator[[]syntaxapi.Result],
	pkg, sym string, add func(uri string, pos term.Coordinates),
) error {
	return iterator.ForEach(ctx, pairs(sub), func(p [2]syntaxapi.Result) error {
		if p[0].Text != pkg || p[1].Text != sym {
			return nil
		}
		add(p[0].File.String(), p[1].From)
		return nil
	})
}

// collectDefinitions streams workspace definitions matching sym in the
// target package and feeds them to add. Files are qualified via their
// package clause when the spec has one, otherwise via spec.Qualifier.
// For specs with re-exports it also surfaces import bindings in
// re-export files whose module path matches pkg.
func collectDefinitions(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	packages map[workspaceapi.URI]string, pkg, sym string,
	add func(uri string, pos term.Coordinates),
) error {
	files, err := definitionFiles(ctx, parser, spec, qc, packages, pkg)
	if err != nil {
		return err
	}
	for _, fileURI := range files {
		it, err := parser.QueryNode(fileURI,
			syntaxapi.NodeCaptureDefinitionFunc|syntaxapi.NodeCaptureDefinitionType,
		)
		if err != nil {
			return err
		}
		for {
			r, ok := it.Next(ctx)
			if !ok {
				break
			}
			if r.Text != sym {
				continue
			}
			add(fileURI.String(), r.From)
		}
		if err := it.Err(); err != nil {
			_ = it.Close()
			return err
		}
		_ = it.Close()
	}
	if !spec.hasReexports() {
		return nil
	}
	return collectReexportDefs(ctx, parser, spec, qc, pkg, sym, add)
}

// collectReexportDefs streams the re-export bindings across the
// workspace and feeds those bound as sym in a re-export file whose
// module path matches pkg to add.
func collectReexportDefs(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	pkg, sym string, add func(uri string, pos term.Coordinates),
) error {
	it, err := parser.Search(spec.ReexportQuery, spec.ReexportCaptures, spec.LangID)
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	for {
		r, ok := it.Next(ctx)
		if !ok {
			return it.Err()
		}
		uri := r.File.String()
		if r.Text != sym || !spec.isReexportFile(uri) ||
			!modulePathHasSuffix(spec.qualifier(qc, uri), pkg) {
			continue
		}
		add(uri, r.From)
	}
}

// collectMethodDefinitions streams method definitions in the target
// package whose receiver type equals typeName and whose name equals
// method, feeding their locations to add. It runs the spec's
// MethodDefQuery per candidate file so receiver/method captures pair
// within a single file's match stream.
func collectMethodDefinitions(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	packages map[workspaceapi.URI]string, pkg, typeName, method string,
	add func(uri string, pos term.Coordinates),
) error {
	files, err := definitionFiles(ctx, parser, spec, qc, packages, pkg)
	if err != nil {
		return err
	}
	for _, fileURI := range files {
		if err := collectFileMethods(
			ctx, parser, spec, fileURI, typeName, method, add,
		); err != nil {
			return err
		}
	}
	return nil
}

// collectFileMethods runs the method-definition query against one file
// and feeds matches whose receiver type equals typeName and method name
// equals method to add.
func collectFileMethods(
	ctx context.Context, parser Searcher, spec *Spec,
	fileURI workspaceapi.URI, typeName, method string,
	add func(uri string, pos term.Coordinates),
) error {
	it, err := parser.Query2(
		fileURI, spec.MethodDefQuery, captures2(spec.MethodDefCaptures))
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	for {
		p, ok := it.Next(ctx)
		if !ok {
			break
		}
		if p[0].Text == typeName && p[1].Text == method {
			add(fileURI.String(), p[1].From)
		}
	}
	return it.Err()
}

// definitionFiles returns the file URIs whose qualifier equals pkg. When
// the spec has a package clause it consults the packages map, and files
// absent from it fall back to spec.Qualifier when the spec has one;
// otherwise it derives the qualifier from each file's URI via
// spec.Qualifier, matching pkg against any dotted suffix of the module
// path.
func definitionFiles(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	packages map[workspaceapi.URI]string, pkg string,
) ([]workspaceapi.URI, error) {
	seen := make(map[workspaceapi.URI]bool)
	var files []workspaceapi.URI
	if spec.hasPackages() {
		for fileURI, pkgName := range packages {
			if pkgName == pkg {
				seen[fileURI] = true
				files = append(files, fileURI)
			}
		}
		if spec.Qualifier == nil {
			return files, nil
		}
	}
	iter, err := parser.SearchNode(
		syntaxapi.NodeCaptureDefinitionFunc|syntaxapi.NodeCaptureDefinitionType,
		spec.LangID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()
	for {
		r, ok := iter.Next(ctx)
		if !ok {
			return files, iter.Err()
		}
		uri := r.File.String()
		if seen[r.File] || !spec.matchesFile(uri) {
			continue
		}
		if _, declared := packages[r.File]; declared ||
			!modulePathHasSuffix(spec.qualifier(qc, uri), pkg) {
			continue
		}
		seen[r.File] = true
		files = append(files, r.File)
	}
}

// filePackage returns the qualifier of a file: its declared package when
// the spec has a package clause and the file declares one, otherwise
// whatever spec.Qualifier derives from the URI, or "".
func filePackage(
	spec *Spec, qc QualifierContext, packages map[workspaceapi.URI]string,
	file workspaceapi.URI,
) string {
	if spec.hasPackages() {
		if pkgName, ok := packages[file]; ok {
			return pkgName
		}
	}
	return spec.qualifier(qc, file.String())
}

// FilePackages returns a map from file URI to the package name declared
// by its package clause, according to spec. Files without a parseable
// package clause are omitted. When the spec has no package clause it
// returns nil.
func FilePackages(
	ctx context.Context, parser Searcher, spec *Spec,
) (map[workspaceapi.URI]string, error) {
	if !spec.hasPackages() {
		return nil, nil
	}
	iter, err := parser.Search(
		spec.PackageClauseQuery, spec.PackageClauseCaptures, spec.LangID,
	)
	if err != nil {
		return nil, err
	}
	return iterator.Reduce(ctx, iter, reducePackages)
}

// reducePackages folds package-clause results into a file→package map,
// keeping the first package name seen per file.
func reducePackages(
	m map[workspaceapi.URI]string, r syntaxapi.Result,
) (map[workspaceapi.URI]string, error) {
	if m == nil {
		m = make(map[workspaceapi.URI]string)
	}
	if _, exists := m[r.File]; !exists {
		m[r.File] = r.Text
	}
	return m, nil
}

// SearchDefinitions streams package-qualified definition names
// ("pkg.Name") for every function and type defined in the workspace,
// according to spec. When the spec has a package clause, packages
// provides the file→package mapping and files absent from it fall
// back to the spec's Qualifier, if any; otherwise the qualifier is
// derived from each file's URI.
// Nested-module qualifiers stream one name per dotted suffix of the
// module path, and re-export bindings are streamed as definitions.
// Names rejected by keep are skipped. keep may be nil.
func SearchDefinitions(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	packages map[workspaceapi.URI]string,
	ch chan<- string, keep func(string) bool,
) error {
	iter, err := parser.SearchNode(
		syntaxapi.NodeCaptureDefinitionFunc|syntaxapi.NodeCaptureDefinitionType,
		spec.LangID,
	)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()
	for {
		r, ok := iter.Next(ctx)
		if !ok {
			if err := iter.Err(); err != nil {
				return err
			}
			break
		}
		if !spec.matchesFile(r.File.String()) {
			continue
		}
		pkgName := filePackage(spec, qc, packages, r.File)
		if pkgName == "" {
			continue
		}
		if keep != nil && !keep(r.Text) {
			continue
		}
		for _, q := range moduleSuffixes(pkgName) {
			select {
			case ch <- q + "." + r.Text:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if !spec.hasReexports() {
		return nil
	}
	return searchReexportDefinitions(ctx, parser, spec, qc, ch, keep)
}

// SearchMethodDefinitions streams package-qualified method-definition
// names ("pkg.Type.Method") for every method defined in the workspace,
// according to spec. Package qualification mirrors SearchDefinitions:
// with a package clause the file→package mapping comes from packages,
// otherwise the qualifier is derived from each file's URI, and one name
// is streamed per dotted suffix of the module path. Specs without a
// method-definition query stream nothing.
func SearchMethodDefinitions(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	packages map[workspaceapi.URI]string, ch chan<- string,
) error {
	if !spec.hasMethods() {
		return nil
	}
	iter, err := parser.Search2(
		spec.MethodDefQuery, captures2(spec.MethodDefCaptures), spec.LangID)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()
	for {
		p, ok := iter.Next(ctx)
		if !ok {
			return iter.Err()
		}
		recv, method := p[0], p[1]
		if !spec.matchesFile(method.File.String()) {
			continue
		}
		pkgName := filePackage(spec, qc, packages, method.File)
		if pkgName == "" {
			continue
		}
		for _, q := range moduleSuffixes(pkgName) {
			select {
			case ch <- q + "." + recv.Text + "." + method.Text:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// searchReexportDefinitions streams the qualified names of re-export
// bindings, one per dotted suffix of the binding file's module path.
func searchReexportDefinitions(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	ch chan<- string, keep func(string) bool,
) error {
	iter, err := parser.Search(
		spec.ReexportQuery, spec.ReexportCaptures, spec.LangID,
	)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()
	for {
		r, ok := iter.Next(ctx)
		if !ok {
			return iter.Err()
		}
		uri := r.File.String()
		if !spec.isReexportFile(uri) {
			continue
		}
		if keep != nil && !keep(r.Text) {
			continue
		}
		pkgName := spec.qualifier(qc, uri)
		if pkgName == "" {
			continue
		}
		for _, q := range moduleSuffixes(pkgName) {
			select {
			case ch <- q + "." + r.Text:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// ImportedAliases returns, per file, the set of import aliases in scope
// according to spec. The completer uses it to keep only references whose
// package alias is actually imported. Returns an empty map when the spec
// declares no import queries.
func ImportedAliases(
	ctx context.Context, parser Searcher, spec *Spec,
) (map[workspaceapi.URI]map[string]bool, error) {
	result := make(map[workspaceapi.URI]map[string]bool)
	if spec.ImportPathQuery == "" {
		return result, nil
	}
	importPaths, err := resolveImportPaths(ctx, parser, spec)
	if err != nil {
		return nil, err
	}
	for file, aliases := range importPaths {
		set := make(map[string]bool, len(aliases))
		for alias := range aliases {
			set[alias] = true
		}
		result[file] = set
	}
	return result, nil
}

func disambiguateDisplayNames(spec *Spec, matches []syntaxapi.Match, name string) {
	for i, m := range matches {
		prefix := m.ImportPath
		if prefix == "" {
			prefix = spec.displayPath(m.URI)
		}
		matches[i].Display = prefix + ": " + name
	}
}

func resolveImportPaths(
	ctx context.Context, parser Searcher, spec *Spec,
) (map[workspaceapi.URI]map[string]string, error) {
	pathIter, err := parser.Search(
		spec.ImportPathQuery, spec.ImportPathCaptures, spec.LangID,
	)
	if err != nil {
		return nil, err
	}
	imports, err := iterator.Reduce(ctx, pathIter, reduceImportPaths)
	if err != nil {
		return nil, err
	}

	aliasIter, err := parser.Search2(
		spec.ImportAliasQuery, captures2(spec.ImportAliasCaptures), spec.LangID,
	)
	if err != nil {
		return nil, err
	}
	explicitAliases, err := iterator.Reduce(ctx, aliasIter, reduceImportAliases)
	if err != nil {
		return nil, err
	}
	return resolveAliases(imports, explicitAliases), nil
}

// reduceImportPaths folds import-path results into a file→import-paths map.
func reduceImportPaths(
	m map[workspaceapi.URI][]string, r syntaxapi.Result,
) (map[workspaceapi.URI][]string, error) {
	if m == nil {
		m = make(map[workspaceapi.URI][]string)
	}
	m[r.File] = append(m[r.File], strings.Trim(r.Text, `"`))
	return m, nil
}

// reduceImportAliases folds explicit import-alias pairs (alias, import path)
// into a file→import-path→alias map, dropping blank ("." / "_") aliases.
func reduceImportAliases(
	m map[workspaceapi.URI]map[string]string, p [2]syntaxapi.Result,
) (map[workspaceapi.URI]map[string]string, error) {
	alias := p[0].Text
	if alias == "." || alias == "_" {
		return m, nil
	}
	importPath := strings.Trim(p[1].Text, `"`)
	if m == nil {
		m = make(map[workspaceapi.URI]map[string]string)
	}
	if m[p[0].File] == nil {
		m[p[0].File] = make(map[string]string)
	}
	m[p[0].File][importPath] = alias
	return m, nil
}

// resolveAliases combines the file→import-paths map with the explicit
// file→import-path→alias overrides into a file→alias→import-path map. The
// default alias is the import path's base segment; "." and "_" imports are
// excluded.
func resolveAliases(
	imports map[workspaceapi.URI][]string,
	explicitAliases map[workspaceapi.URI]map[string]string,
) map[workspaceapi.URI]map[string]string {
	resolved := make(map[workspaceapi.URI]map[string]string, len(imports))
	for file, paths := range imports {
		aliases := make(map[string]string)
		for _, importPath := range paths {
			alias := path.Base(importPath)
			if fileAliases := explicitAliases[file]; fileAliases != nil {
				if explicitAlias, ok := fileAliases[importPath]; ok {
					alias = explicitAlias
				}
			}
			if alias == "." || alias == "_" {
				continue
			}
			aliases[alias] = importPath
		}
		if len(aliases) > 0 {
			resolved[file] = aliases
		}
	}
	return resolved
}

// deduplicateMatchesByImport collapses matches that resolve to the same
// import path for alias, annotating each surviving match with its import
// path. Files absent from importPaths are keyed by their URI.
func deduplicateMatchesByImport(
	matches []syntaxapi.Match, alias string,
	importPaths map[workspaceapi.URI]map[string]string,
) []syntaxapi.Match {
	lookup := make(map[string]map[string]string, len(importPaths))
	for uri, aliases := range importPaths {
		lookup[uri.String()] = aliases
	}
	seen := make(map[string]bool)
	result := matches[:0]
	for _, m := range matches {
		key := m.URI
		if fileImports := lookup[m.URI]; fileImports != nil {
			if importPath, ok := fileImports[alias]; ok {
				key = importPath
				m.ImportPath = importPath
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, m)
	}
	return result
}

// firsts adapts a demultiplexed single-capture sub-stream to its sole
// capture.
func firsts(
	it iterator.Iterator[[]syntaxapi.Result],
) iterator.Iterator[syntaxapi.Result] {
	return iterator.Map(
		iterator.Filter(it, func(m []syntaxapi.Result) bool {
			return len(m) > 0
		}),
		func(m []syntaxapi.Result) syntaxapi.Result { return m[0] },
	)
}

// pairs adapts a demultiplexed two-capture sub-stream to its capture
// pairs.
func pairs(
	it iterator.Iterator[[]syntaxapi.Result],
) iterator.Iterator[[2]syntaxapi.Result] {
	return iterator.Map(
		iterator.Filter(it, func(m []syntaxapi.Result) bool {
			return len(m) == 2
		}),
		func(m []syntaxapi.Result) [2]syntaxapi.Result {
			return [2]syntaxapi.Result{m[0], m[1]}
		},
	)
}

// captures2 narrows a spec's two-element capture list to the fixed-size
// form the paired query entry points take.
func captures2(captures []string) [2]string {
	return [2]string{captures[0], captures[1]}
}

// ListReferences streams package-qualified symbol names ("pkg.Name")
// referenced or defined across the workspace for every spec yielded by specs.
// Names may repeat across specs; callers that need uniqueness deduplicate the
// stream. Returns ErrNoDot is never produced here since no name is parsed.
func ListReferences(
	ctx context.Context, parser Searcher, qc QualifierContext,
	specs iterator.Iterator[Spec], ch chan<- string,
) error {
	defer func() { _ = specs.Close() }()
	for {
		spec, ok := specs.Next(ctx)
		if !ok {
			break
		}
		if err := listSpecReferences(ctx, parser, &spec, qc, ch); err != nil {
			return err
		}
	}
	return specs.Err()
}

func listSpecReferences(
	ctx context.Context, parser Searcher, spec *Spec, qc QualifierContext,
	ch chan<- string,
) error {
	if spec.isHost() {
		return nil
	}
	imports, err := ImportedAliases(ctx, parser, spec)
	if err != nil {
		return err
	}
	for _, rq := range spec.RefQueries {
		keep := func(p [2]syntaxapi.Result) bool {
			if !spec.exported(p[1].Text) {
				return false
			}
			if rq.RequireImport {
				m := imports[p[0].File]
				return m != nil && m[p[0].Text]
			}
			return true
		}
		if err := listRefPairs(ctx, parser, rq.Query, rq.Captures, spec.LangID, keep, ch); err != nil {
			return err
		}
	}
	packages, err := FilePackages(ctx, parser, spec)
	if err != nil {
		return err
	}
	if err := SearchDefinitions(ctx, parser, spec, qc, packages, ch, nil); err != nil {
		return err
	}
	return SearchMethodDefinitions(ctx, parser, spec, qc, packages, ch)
}

func listRefPairs(
	ctx context.Context, parser Searcher,
	query string, captures []string, lang string,
	keep func([2]syntaxapi.Result) bool,
	ch chan<- string,
) error {
	iter, err := parser.Search2(query, captures2(captures), lang)
	if err != nil {
		return err
	}
	results := iterator.Map(
		iterator.Filter(iter, keep),
		func(p [2]syntaxapi.Result) string {
			return p[0].Text + "." + p[1].Text
		},
	)
	defer func() { _ = results.Close() }()
	for {
		s, ok := results.Next(ctx)
		if !ok {
			return results.Err()
		}
		select {
		case ch <- s:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
