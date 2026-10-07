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
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// TypeScript is the symbol-resolution spec for .ts, .mts and .cts files,
// including their .d.ts declaration forms. TSX and JavaScript share its
// rules, one spec per tree-sitter grammar. ECMAScript modules have no
// package clause, so a module is named after its file. Other modules
// reach its members through a binding of the whole module: a
// namespace import (`import * as geo from "./geometry"`), a TypeScript
// import-require (`import geo = require("./geometry")`), or a CommonJS
// `const geo = require("./geometry")`. Named imports bind bare
// identifiers and so produce no qualified references. An index file
// is named after its directory, and the names it re-exports with
// `export { … }` count as its definitions. `export` is not observable
// from the definition captures, so every name is treated as visible.
var TypeScript = &Spec{
	LangID: "typescript",
	RefQueries: []RefQuery{
		{Query: ecmaMemberRefQuery, Captures: []string{"pkg", "member"}, RequireImport: true},
		{Query: tsTypeRefQuery, Captures: []string{"pkg", "type"}, RequireImport: true},
	},
	ImportPathQuery:     tsImportQuery,
	ImportPathCaptures:  []string{"path"},
	ImportAliasQuery:    tsImportQuery,
	ImportAliasCaptures: []string{"alias", "path"},
	Qualifier:           ecmaModuleFromURI,
	DisplayPathFromURI:  ecmaDirFromURI,
	Extensions:          []string{".ts", ".mts", ".cts"},
	MethodDefQuery:      tsMethodDefQuery,
	MethodDefCaptures:   []string{"recv", "method"},
	ReexportQuery:       ecmaReexportQuery,
	ReexportCaptures:    []string{"name"},
	ReexportFiles:       []string{"index.ts", "index.mts", "index.cts", "index.d.ts"},
}

// TSX is the TypeScript spec for .tsx files, which tree-sitter parses
// with a separate grammar that shares TypeScript's node types.
var TSX = &Spec{
	LangID: "tsx",
	RefQueries: []RefQuery{
		{Query: ecmaMemberRefQuery, Captures: []string{"pkg", "member"}, RequireImport: true},
		{Query: tsTypeRefQuery, Captures: []string{"pkg", "type"}, RequireImport: true},
	},
	ImportPathQuery:     tsImportQuery,
	ImportPathCaptures:  []string{"path"},
	ImportAliasQuery:    tsImportQuery,
	ImportAliasCaptures: []string{"alias", "path"},
	Qualifier:           ecmaModuleFromURI,
	DisplayPathFromURI:  ecmaDirFromURI,
	Extensions:          []string{".tsx"},
	MethodDefQuery:      tsMethodDefQuery,
	MethodDefCaptures:   []string{"recv", "method"},
	ReexportQuery:       ecmaReexportQuery,
	ReexportCaptures:    []string{"name"},
	ReexportFiles:       []string{"index.tsx"},
}

// JavaScript is the spec for JavaScript and JSX files. The JavaScript
// grammar has no type references or import-require clause, and names
// classes with plain identifiers.
var JavaScript = &Spec{
	LangID: "javascript",
	RefQueries: []RefQuery{
		{Query: ecmaMemberRefQuery, Captures: []string{"pkg", "member"}, RequireImport: true},
	},
	ImportPathQuery:     jsImportQuery,
	ImportPathCaptures:  []string{"path"},
	ImportAliasQuery:    jsImportQuery,
	ImportAliasCaptures: []string{"alias", "path"},
	Qualifier:           ecmaModuleFromURI,
	DisplayPathFromURI:  ecmaDirFromURI,
	Extensions:          []string{".js", ".mjs", ".cjs", ".jsx"},
	MethodDefQuery: `(class_declaration name: (identifier) @recv ` +
		`body: (class_body (method_definition name: (property_identifier) @method)))`,
	MethodDefCaptures: []string{"recv", "method"},
	ReexportQuery:     ecmaReexportQuery,
	ReexportCaptures:  []string{"name"},
	ReexportFiles:     []string{"index.js", "index.mjs", "index.cjs", "index.jsx"},
}

const (
	ecmaMemberRefQuery = `(member_expression object: (identifier) @pkg ` +
		`property: (property_identifier) @member)`

	tsTypeRefQuery = `(nested_type_identifier module: (identifier) @pkg ` +
		`name: (type_identifier) @type)`

	ecmaNamespaceImport = `(import_statement ` +
		`(import_clause (namespace_import (identifier) @alias)) ` +
		`source: (string (string_fragment) @path))`

	// Only a require() whose sole argument is a string literal binds a
	// statically known module.
	ecmaRequireImport = `(variable_declarator name: (identifier) @alias ` +
		`value: (call_expression function: (identifier) @_require ` +
		`arguments: (arguments . (string (string_fragment) @path) .)) ` +
		`(#eq? @_require "require"))`

	jsImportQuery = `[` + ecmaNamespaceImport + ` ` + ecmaRequireImport + `]`

	tsImportQuery = `[` + ecmaNamespaceImport + ` ` +
		`(import_statement (import_require_clause (identifier) @alias ` +
		`source: (string (string_fragment) @path))) ` +
		ecmaRequireImport + `]`

	tsMethodDefQuery = `[(class_declaration name: (type_identifier) @recv ` +
		`body: (class_body (method_definition name: (property_identifier) @method))) ` +
		`(abstract_class_declaration name: (type_identifier) @recv ` +
		`body: (class_body (method_definition name: (property_identifier) @method)))]`

	// An export specifier binds its alias when it has one, so
	// `export { load as loadConfig }` re-exports loadConfig, not load.
	ecmaReexportQuery = `[(export_statement (export_clause ` +
		`(export_specifier name: (identifier) @name !alias))) ` +
		`(export_statement (export_clause ` +
		`(export_specifier alias: (identifier) @name)))]`
)

// ecmaModuleFromURI derives an ECMAScript module name from a file URI:
// the base file name up to its first dot, so a declaration file
// (geometry.d.ts) or a suffixed one (geometry.test.ts) shares the name
// of its module, or the parent directory name for an index file, which
// is what a bare directory import resolves to. The qualifier context
// is unused: module names keep a single segment.
func ecmaModuleFromURI(_ QualifierContext, uri string) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	stem, _, _ := strings.Cut(path.Base(parsed.Path()), ".")
	if stem == "index" {
		return path.Base(path.Dir(parsed.Path()))
	}
	return stem
}

// ecmaDirFromURI returns the directory path of a file URI.
func ecmaDirFromURI(uri string) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	return path.Dir(parsed.Path())
}
