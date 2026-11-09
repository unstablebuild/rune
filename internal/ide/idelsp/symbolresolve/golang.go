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

// Go is the symbol-resolution spec for the Go language.
var Go = &Spec{
	LangID: "go",
	RefQueries: []RefQuery{
		{
			Query: `(qualified_type package: (package_identifier) @pkg ` +
				`name: (type_identifier) @type)`,
			Captures: []string{"pkg", "type"},
		},
		{
			Query: `(selector_expression operand: (identifier) @pkg ` +
				`field: (field_identifier) @symbol)`,
			Captures:      []string{"pkg", "symbol"},
			RequireImport: true,
		},
	},
	PackageClauseQuery:    `(package_clause (package_identifier) @pkg)`,
	PackageClauseCaptures: []string{"pkg"},
	ImportPathQuery: `(import_spec ` +
		`path: (interpreted_string_literal) @path)`,
	ImportPathCaptures: []string{"path"},
	ImportAliasQuery: `(import_spec name: (package_identifier) @alias ` +
		`path: (interpreted_string_literal) @path)`,
	ImportAliasCaptures: []string{"alias", "path"},
	IsExported:          isExported,
	DisplayPathFromURI:  goPackagePathFromURI,
	Extensions:          []string{".go"},
	MethodDefQuery: `[(method_declaration ` +
		`receiver: (parameter_list (parameter_declaration ` +
		`type: [(type_identifier) @recv ` +
		`(pointer_type (type_identifier) @recv)])) ` +
		`name: (field_identifier) @method) ` +
		`(type_spec name: (type_identifier) @recv ` +
		`type: (interface_type (method_elem ` +
		`name: (field_identifier) @method)))]`,
	MethodDefCaptures: []string{"recv", "method"},
}

// goPackagePathFromURI derives a Go-style display prefix from a file
// URI. For Go module cache paths it recovers the import path by
// stripping "@version" segments; otherwise it returns the file's
// directory path. The URI scheme is ignored.
func goPackagePathFromURI(uri string) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	dir := path.Dir(parsed.Path())
	if idx := strings.Index(dir, "/pkg/mod/"); idx != -1 {
		modPath := dir[idx+len("/pkg/mod/"):]
		parts := strings.Split(modPath, "/")
		for i, part := range parts {
			if atIdx := strings.Index(part, "@"); atIdx != -1 {
				parts[i] = part[:atIdx]
			}
		}
		return strings.Join(parts, "/")
	}
	return dir
}

func isExported(name string) bool {
	return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
}

// IsExported reports whether name starts with an uppercase ASCII
// letter, matching Go's export rules.
func IsExported(name string) bool { return isExported(name) }
