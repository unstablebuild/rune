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
	"runtime"
	"strconv"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// pyCommand builds an LSP command string from a resolved binary path
// and a subcommand. When bin is empty the fallback name is used and the
// executor resolves it through $PATH.
func pyCommand(bin, fallback, subcmd string) string {
	name := bin
	if name == "" {
		name = fallback
	}
	if subcmd == "" {
		return name
	}
	return name + " " + subcmd
}

func pyRuffCommand(bin, logLevel string) string {
	command := pyCommand(bin, "ruff", "server")
	if logLevel == "debug" || logLevel == "trace" {
		return command + " -v"
	}
	return command
}

func pyInitializeParams(
	rootURI, command string, alternates map[string]string,
	diagnosticMode, logLevel string,
) (semanticapi.InitializeParams, error) {
	initOptions := map[string]any{
		"langID":  "python",
		"command": command,
		// ty and ruff size their rayon pool from the core count, so an
		// indexing burst saturates the machine and the editor's render
		// loop is left waiting for a thread to run on. Half the cores
		// keeps the servers useful while leaving the editor somewhere
		// to run. Alternate-command children inherit this env, so the
		// cap reaches ruff as well as ty.
		"env": map[string]any{
			"RAYON_NUM_THREADS": strconv.Itoa(max(1, runtime.NumCPU()/2)),
		},
	}
	if len(alternates) > 0 {
		initOptions["alternate_commands"] = alternates
	}
	// ty reads diagnosticMode from its (flattened) initialization
	// options. "workspace" makes ty type-check every project file and
	// answer workspace/diagnostic pulls for files that were never
	// opened in the editor, at the cost of a full-project scan per
	// pull. The Manager strips langID/command/alternate_commands and
	// forwards the rest verbatim, so this key reaches ty unchanged.
	if diagnosticMode != "" {
		initOptions["diagnosticMode"] = diagnosticMode
	}
	if logLevel != "" {
		initOptions["logLevel"] = logLevel
	}

	initOptionsData, err := json.Marshal(initOptions)
	if err != nil {
		return semanticapi.InitializeParams{}, fmt.Errorf("marshal initialize options: %w", err)
	}

	capabilities := map[string]any{
		"textDocument": map[string]any{
			"hover": map[string]any{
				"contentFormat": []string{"markdown", "plaintext"},
			},
			"declaration": map[string]any{
				"linkSupport": true,
			},
			"definition": map[string]any{
				"linkSupport": true,
			},
			"typeDefinition": map[string]any{
				"linkSupport": true,
			},
			"references":     map[string]any{},
			"documentSymbol": map[string]any{},
			"completion": map[string]any{
				"completionItem": map[string]any{
					"documentationFormat": []string{"markdown", "plaintext"},
				},
			},
			"signatureHelp": map[string]any{
				"signatureInformation": map[string]any{
					"activeParameterSupport": true,
					"parameterInformation": map[string]any{
						"labelOffsetSupport": true,
					},
				},
			},
			"formatting":      map[string]any{},
			"rangeFormatting": map[string]any{},
			"rename": map[string]any{
				"prepareSupport": true,
			},
			"publishDiagnostics": map[string]any{
				"relatedInformation": true,
			},
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{
						"valueSet": []string{
							"quickfix",
							"source",
							"source.organizeImports",
							"source.fixAll",
						},
					},
				},
			},
			"foldingRange": map[string]any{
				"lineFoldingOnly": false,
			},
			"selectionRange":    map[string]any{},
			"documentHighlight": map[string]any{},
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
			"diagnostics": map[string]any{},
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
		RootURI:           rootURI,
		Capabilities:      json.RawMessage(capabilitiesData),
		InitializeOptions: json.RawMessage(initOptionsData),
		Trace:             semanticapi.TraceValueOff,
	}, nil
}
