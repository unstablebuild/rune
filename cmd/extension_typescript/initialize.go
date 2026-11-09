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

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// tsServerCommand runs the native compiler as a stdio language server.
func tsServerCommand(bin string) string {
	return bin + " --lsp --stdio"
}

// tsInitializeParams builds the InitializeParams for tsgo. The
// langID/command keys steer the host LSP shim; the rest is forwarded
// verbatim as tsgo's initializationOptions.
//
// Two tsgo behaviors shape the params:
//   - tsgo only pushes tsconfig diagnostics. Per-file diagnostics are
//     answered to textDocument/diagnostic pulls, so the client must
//     advertise textDocument.diagnostic for the Manager to pull them.
//   - tsgo reads its preferences from userPreferences only when the
//     client does not advertise workspace.configuration; otherwise it
//     asks for the VS Code settings sections, which Rune does not have.
func tsInitializeParams(rootURI, server string) (semanticapi.InitializeParams, error) {
	initOptions := map[string]any{
		"langID":  tsLanguageID,
		"command": tsServerCommand(server),
		"userPreferences": map[string]any{
			"includeInlayParameterNameHints":                        "all",
			"includeInlayParameterNameHintsWhenArgumentMatchesName": false,
			"includeInlayVariableTypeHints":                         true,
			"includeInlayVariableTypeHintsWhenTypeMatchesName":      false,
			"includeInlayFunctionLikeReturnTypeHints":               true,
			"includeInlayEnumMemberValueHints":                      true,
		},
	}
	initOptionsData, err := json.Marshal(initOptions)
	if err != nil {
		return semanticapi.InitializeParams{}, fmt.Errorf("marshal initialize options: %w", err)
	}

	// Only the surface tsgo 7.0 implements is advertised: no
	// declaration, and completion items without snippetSupport, because
	// Rune inserts completion text verbatim.
	capabilities := map[string]any{
		"textDocument": map[string]any{
			"hover": map[string]any{
				"contentFormat": []string{"markdown", "plaintext"},
			},
			"definition":        map[string]any{"linkSupport": true},
			"typeDefinition":    map[string]any{"linkSupport": true},
			"implementation":    map[string]any{"linkSupport": true},
			"references":        map[string]any{},
			"documentSymbol":    map[string]any{"hierarchicalDocumentSymbolSupport": true},
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
			"diagnostic": map[string]any{},
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{
						"valueSet": []string{
							"quickfix",
							"source",
							"source.organizeImports",
							"source.removeUnusedImports",
							"source.sortImports",
							"source.fixAll",
						},
					},
				},
			},
			"foldingRange": map[string]any{
				"lineFoldingOnly": false,
			},
			"selectionRange": map[string]any{},
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
			"symbol": map[string]any{},
			"workspaceEdit": map[string]any{
				"documentChanges": true,
			},
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
		RootURI:           rootURI,
		Capabilities:      json.RawMessage(capabilitiesData),
		InitializeOptions: json.RawMessage(initOptionsData),
		Trace:             semanticapi.TraceValueOff,
	}, nil
}
