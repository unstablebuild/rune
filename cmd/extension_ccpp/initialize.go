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

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// clangdOptions is everything a clangd bring-up is parameterized by.
type clangdOptions struct {
	bin string
	// args are the user's extra clangd flags, appended last so they
	// override the defaults.
	args []string
	// compileCommandsDir is the directory holding the compile_commands.json
	// clangd must use, "" to let clangd find one per file.
	compileCommandsDir string
	// fallbackFlags are the compiler flags for files no compilation
	// database covers.
	fallbackFlags []string
	logLevel      string
}

// clangdDefaultArgs are the flags every clangd gets. Background indexing
// is what makes workspace symbols and cross-file references work;
// clang-tidy adds its checks to the diagnostics; iwyu header insertion
// keeps completion from adding an include for a symbol a header already
// included provides.
var clangdDefaultArgs = []string{
	"--background-index", "--clang-tidy", "--header-insertion=iwyu",
}

// clangdCommand renders the command line the host runs. The host splits
// it on spaces, so the compilation database directory, which may contain
// one, travels in initializationOptions rather than as a flag.
func clangdCommand(o clangdOptions) string {
	argv := append([]string{o.bin}, clangdDefaultArgs...)
	argv = append(argv, "--log="+o.logLevel)
	argv = append(argv, o.args...)
	return strings.Join(argv, " ")
}

// clangdInitializeParams builds the InitializeParams for clangd. The
// langID/command keys steer the host LSP shim; the rest of
// initializationOptions is clangd's own.
func clangdInitializeParams(
	rootURI, rootName string, o clangdOptions,
) (semanticapi.InitializeParams, error) {
	if o.logLevel == "" {
		o.logLevel = defaultClangdLogLevel
	}
	initOptions := map[string]any{
		"langID":  ccppLanguageID,
		"command": clangdCommand(o),
		// File status notifications need a status bar the extension does
		// not have.
		"clangdFileStatus": false,
	}
	if o.compileCommandsDir != "" {
		initOptions["compilationDatabasePath"] = o.compileCommandsDir
	}
	if len(o.fallbackFlags) > 0 {
		initOptions["fallbackFlags"] = o.fallbackFlags
	}
	initOptionsData, err := json.Marshal(initOptions)
	if err != nil {
		return semanticapi.InitializeParams{}, fmt.Errorf("marshal initialize options: %w", err)
	}

	// Completion advertises no snippet support on purpose: Rune's editor
	// inserts completion text verbatim, so placeholders must never reach
	// the buffer. clangd then completes plain identifiers.
	capabilities := map[string]any{
		"textDocument": map[string]any{
			"hover": map[string]any{
				"contentFormat": []string{"markdown", "plaintext"},
			},
			"definition":        map[string]any{},
			"declaration":       map[string]any{},
			"typeDefinition":    map[string]any{},
			"implementation":    map[string]any{},
			"references":        map[string]any{},
			"documentSymbol":    map[string]any{},
			"completion":        map[string]any{},
			"signatureHelp":     map[string]any{},
			"formatting":        map[string]any{},
			"rangeFormatting":   map[string]any{},
			"rename":            map[string]any{"prepareSupport": true},
			"documentHighlight": map[string]any{},
			"inlayHint":         map[string]any{},
			"publishDiagnostics": map[string]any{
				"relatedInformation": true,
			},
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{
						"valueSet": []string{
							"quickfix",
							"refactor",
							"refactor.extract",
							"refactor.inline",
							"refactor.rewrite",
							"source",
						},
					},
				},
			},
			"foldingRange": map[string]any{
				"lineFoldingOnly": false,
			},
			"selectionRange": map[string]any{},
			"callHierarchy":  map[string]any{},
			"typeHierarchy":  map[string]any{},
			"semanticTokens": map[string]any{
				"requests": map[string]any{
					"full":  true,
					"range": true,
				},
				"tokenTypes": []string{
					"namespace", "type", "class",
					"enum", "interface", "struct",
					"typeParameter", "parameter",
					"variable", "property",
					"enumMember", "event",
					"function", "method", "macro",
					"keyword", "modifier",
					"comment", "string", "number",
					"regexp", "operator",
					"decorator", "label",
				},
				"tokenModifiers": []string{
					"declaration", "definition",
					"readonly", "static",
					"deprecated", "abstract",
					"async", "modification",
					"documentation",
					"defaultLibrary",
				},
				"formats": []string{"relative"},
			},
		},
		"workspace": map[string]any{
			"symbol": map[string]any{
				"symbolKind": map[string]any{
					"valueSet": []int{
						int(semanticapi.SymbolKindFile),
						int(semanticapi.SymbolKindModule),
						int(semanticapi.SymbolKindNamespace),
						int(semanticapi.SymbolKindPackage),
						int(semanticapi.SymbolKindClass),
						int(semanticapi.SymbolKindMethod),
						int(semanticapi.SymbolKindProperty),
						int(semanticapi.SymbolKindField),
						int(semanticapi.SymbolKindConstructor),
						int(semanticapi.SymbolKindEnum),
						int(semanticapi.SymbolKindInterface),
						int(semanticapi.SymbolKindFunction),
						int(semanticapi.SymbolKindVariable),
						int(semanticapi.SymbolKindConstant),
						int(semanticapi.SymbolKindString),
						int(semanticapi.SymbolKindNumber),
						int(semanticapi.SymbolKindBoolean),
						int(semanticapi.SymbolKindArray),
						int(semanticapi.SymbolKindObject),
						int(semanticapi.SymbolKindKey),
						int(semanticapi.SymbolKindNull),
						int(semanticapi.SymbolKindEnumMember),
						int(semanticapi.SymbolKindStruct),
						int(semanticapi.SymbolKindEvent),
						int(semanticapi.SymbolKindOperator),
						int(semanticapi.SymbolKindTypeParameter),
					},
				},
			},
			"workspaceEdit": map[string]any{
				"documentChanges": true,
			},
			"configuration": true,
		},
		"window": map[string]any{
			"workDoneProgress": true,
			"showDocument": map[string]any{
				"support": true,
			},
		},
	}
	capabilitiesData, err := json.Marshal(capabilities)
	if err != nil {
		return semanticapi.InitializeParams{}, fmt.Errorf("marshal capabilities: %w", err)
	}

	return semanticapi.InitializeParams{
		RootURI:      rootURI,
		Capabilities: json.RawMessage(capabilitiesData),
		WorkspaceFolders: []semanticapi.WorkspaceFolder{
			{URI: rootURI, Name: rootName},
		},
		InitializeOptions: json.RawMessage(initOptionsData),
		Trace:             semanticapi.TraceValueOff,
	}, nil
}
