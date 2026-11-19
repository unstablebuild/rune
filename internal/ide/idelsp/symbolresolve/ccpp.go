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

// C is the symbol-resolution spec for the C language. C has neither
// packages nor qualified references: every name lives in one global
// namespace and `a.b` / `a->b` are field accesses, which stay
// unresolved. Qualifiers are derived from the file stem, so `greeter.greet`
// names the function declared in greeter.c. Visibility (`static`) is not
// observable from tree-sitter captures, so every name is treated as
// visible. Headers parse as C++ (see languages.go), so they belong to
// Cpp.
var C = &Spec{
	LangID:             "c",
	IsExported:         nil,
	Qualifier:          ccppModuleFromURI,
	DisplayPathFromURI: ccppFileFromURI,
	Extensions:         []string{".c"},
}

// Cpp is the symbol-resolution spec for the C++ language. A file's
// package is the first namespace it declares, the innermost segment for
// `namespace a::b`; files outside any namespace fall back to their file
// stem. `ns::f`, `ns::Type` and `Type::member` are qualified references;
// `obj.m()` and `ptr->m()` stay unresolved. `Type::method` resolves to
// in-class declarations and out-of-line definitions alike. Visibility
// (`private`, anonymous namespaces) is not observable from tree-sitter
// captures, so every name is treated as visible.
var Cpp = &Spec{
	LangID: "cpp",
	RefQueries: []RefQuery{
		{
			Query: `(qualified_identifier scope: (namespace_identifier) @pkg ` +
				`name: (identifier) @symbol)`,
			Captures: []string{"pkg", "symbol"},
		},
		{
			Query: `(qualified_identifier scope: (namespace_identifier) @pkg ` +
				`name: (type_identifier) @type)`,
			Captures: []string{"pkg", "type"},
		},
		{
			Query: `(qualified_identifier scope: (namespace_identifier) @pkg ` +
				`name: (template_type name: (type_identifier) @type))`,
			Captures: []string{"pkg", "type"},
		},
		{
			Query: `(qualified_identifier scope: (namespace_identifier) @pkg ` +
				`name: (template_function name: (identifier) @symbol))`,
			Captures: []string{"pkg", "symbol"},
		},
	},
	PackageClauseQuery: `(namespace_definition name: [` +
		`(namespace_identifier) @pkg ` +
		`(nested_namespace_specifier (namespace_identifier) @pkg .)])`,
	PackageClauseCaptures: []string{"pkg"},
	IsExported:            nil,
	Qualifier:             ccppModuleFromURI,
	DisplayPathFromURI:    ccppFileFromURI,
	Extensions: []string{
		".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx", ".h++",
		".inl", ".ipp", ".tpp", ".h",
	},
	// A method is declared in its class body, defined out of line as
	// `Type::method`, or both; the grammar reads the `Type::` scope as a
	// namespace_identifier. Constructors and member templates appear in
	// the body as plain declarations whose declarator is an identifier.
	MethodDefQuery: `[` +
		`(class_specifier name: (type_identifier) @recv ` +
		`body: (field_declaration_list ` + ccppMemberPatterns + `)) ` +
		`(struct_specifier name: (type_identifier) @recv ` +
		`body: (field_declaration_list ` + ccppMemberPatterns + `)) ` +
		`(function_definition declarator: (function_declarator ` +
		`declarator: (qualified_identifier scope: (namespace_identifier) @recv ` +
		`name: (identifier) @method)))` +
		`]`,
	MethodDefCaptures: []string{"recv", "method"},
}

// ccppMemberPatterns matches the members of a class body that declare
// or define a method, capturing its name as @method.
const ccppMemberPatterns = `[` +
	`(field_declaration declarator: (function_declarator ` +
	`declarator: (field_identifier) @method)) ` +
	`(function_definition declarator: (function_declarator ` +
	`declarator: [(field_identifier) (identifier)] @method)) ` +
	`(declaration declarator: (function_declarator ` +
	`declarator: (identifier) @method)) ` +
	`(template_declaration (declaration declarator: (function_declarator ` +
	`declarator: (identifier) @method)))` +
	`]`

// ccppModuleFromURI derives a C or C++ qualifier from a file URI: the
// base file name without its extension. The qualifier context is
// unused: a translation unit has no module path.
func ccppModuleFromURI(_ QualifierContext, uri string) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	base := path.Base(parsed.Path())
	return strings.TrimSuffix(base, path.Ext(base))
}

// ccppFileFromURI returns the file path of a URI. A header and its
// source share a stem and usually a directory, so the directory alone
// could not tell a declaration apart from its definition.
func ccppFileFromURI(uri string) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	return parsed.Path()
}
