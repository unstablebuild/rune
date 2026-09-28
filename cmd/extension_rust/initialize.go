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

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// rustInitializeParams builds the InitializeParams for rust-analyzer.
// The langID/command/env keys steer the host LSP shim; every other key is
// forwarded verbatim as rust-analyzer's initializationOptions, so the
// cargo/procMacro/check/inlayHints settings configure the server. A
// non-empty sysroot points rust-analyzer at the standard library.
func rustInitializeParams(
	rootURI, command, sysroot, logFilter string, experimental bool,
) (semanticapi.InitializeParams, error) {
	// rust-analyzer sizes its main loop and its cache-priming pool from
	// the core count, and priming a cold project is the burst that
	// saturates the machine; the editor's render loop is then left
	// without a core to run on. Half the cores keeps the server useful
	// while leaving the editor somewhere to run.
	workers := max(1, runtime.NumCPU()/2)
	initOptions := map[string]any{
		"langID":  "rust",
		"command": command,
		"env": map[string]string{
			"RA_LOG": logFilter,
		},
		"numThreads": workers,
		"cachePriming": map[string]any{
			"numThreads": workers,
		},
		"cargo": map[string]any{
			"buildScripts": map[string]any{
				"enable": true,
			},
		},
		"procMacro": map[string]any{
			"enable": true,
		},
		"check": map[string]any{
			"command": "clippy",
		},
		"checkOnSave": true,
		// Assists that add a call whose result must be used annotate the
		// generated code with #[must_use] so the follow-up diagnostic is
		// surfaced instead of silently dropped.
		"assist": map[string]any{
			"emitMustUse": true,
		},
		// Auto-import assists (the `rust auto-import` quick fix) and import
		// normalization produce crate-relative, module-granular, merged use
		// trees so `organize-imports` output matches idiomatic Rust.
		"imports": map[string]any{
			"granularity": map[string]any{
				"group": "module",
			},
			"prefix": "crate",
			"merge": map[string]any{
				"glob": false,
			},
		},
		"completion": map[string]any{
			"autoimport": map[string]any{
				"enable": true,
			},
		},
		// Rune does not render rust-analyzer code lenses, so suppress them
		// to avoid the server computing run/debug/reference lenses that
		// never reach the UI.
		"lens": map[string]any{
			"enable": false,
		},
		"inlayHints": map[string]any{
			"bindingModeHints":  map[string]any{"enable": false},
			"chainingHints":     map[string]any{"enable": true},
			"closingBraceHints": map[string]any{"enable": true},
			"parameterHints":    map[string]any{"enable": true},
			"typeHints":         map[string]any{"enable": true},
		},
	}
	if sysroot != "" {
		initOptions["sysroot"] = sysroot
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
			"callHierarchy":     map[string]any{},
			"inlayHint":         map[string]any{},
			"codeLens":          map[string]any{},
			// Once build scripts and proc macros are enabled,
			// rust-analyzer computes native semantic diagnostics only
			// for pull-capable clients; without this Rune sees clippy
			// flycheck output only, which is a different diagnostic set
			// and only runs on save (RUNE-332).
			"diagnostic": map[string]any{},
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
							"source.organizeImports",
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
		// experimental advertises support for the client-serviced
		// rust-analyzer extensions:
		//   - codeActionGroup: the "group" field is carried through and
		//     used to collapse related actions.
		//   - serverStatusNotification: routed to HandleNotification.
		//   - colorDiagnosticOutput: ANSI-rendered compiler diagnostics.
		//
		// Only flags rust-analyzer actually reads from the client belong
		// here. The experimental/* request extensions (joinLines,
		// matchingBrace, moveItem, onEnter, ssr, parentModule, openCargoToml,
		// hoverRange, ...) are server capabilities that rust-analyzer answers
		// unconditionally, so advertising them client-side is a no-op.
		// The response-shaping flags rust-analyzer *does* read (localDocs,
		// hoverActions, commands) are added by experimentalCaps under the
		// experimental config flag.
		//
		// snippetTextEdit stays off: lspcmd.ApplyWorkspaceEdit writes edits
		// verbatim, so a snippet edit leaks literal $0/${1:_} tab stops into
		// the buffer (proven by TestE2E/ExtractVariable). Leaving it unset
		// keeps rust-analyzer's assists in plain TextEdit form. The
		// moveItem/onEnter subcommands strip snippet markers themselves
		// before applying.
		"experimental": experimentalCaps(experimental),
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

// experimentalCaps builds the experimental client-capability map. The
// always-on flags are the ones rust-analyzer reads from the client
// regardless of feature opt-in.
//
// The experimental-gated flags shape rust-analyzer's responses for the
// opt-in subcommands:
//   - localDocs lets `external-docs` receive a {web, local} response.
//   - hoverActions + commands are what unlock hover actions: rust-analyzer
//     only attaches a HoverAction link when the client both advertises
//     hoverActions and lists the corresponding client command in
//     experimental.commands.commands (see ClientCommandsConfig in
//     rust-analyzer's config.rs). Without the command names, every action
//     link is suppressed and hover comes back with actions == null.
func experimentalCaps(experimental bool) map[string]any {
	caps := map[string]any{
		"codeActionGroup":          true,
		"serverStatusNotification": true,
		"colorDiagnosticOutput":    true,
	}
	if experimental {
		caps["localDocs"] = true
		caps["hoverActions"] = true
		caps["commands"] = map[string]any{
			"commands": clientCommands,
		}
	}
	return caps
}

// clientCommands are the client-side command identifiers this extension
// advertises under experimental.commands.commands. rust-analyzer reads this
// list to decide which commands it may embed in responses, and treats each
// advertised name as a promise that the client services it *locally* (not via
// workspace/executeCommand). Hover actions are gated on it: prepare_hover_actions
// in rust-analyzer's handlers/request.rs only attaches a HoverAction when the
// matching command is advertised — show_reference for Implementation and
// Reference, run_single/debug_single for Runnable, goto_location for GoToType.
//
// The official VS Code client (editors/code/src/client.ts) also advertises
// rust-analyzer.rename and rust-analyzer.triggerParameterHints because it
// implements those client-side (inline rename, completion retrigger). This
// extension does not, and advertising them is actively harmful: rust-analyzer
// then embeds rust-analyzer.rename as the trailing command on assists such as
// "Extract into variable", which codeaction.go forwards to
// workspace/executeCommand — where the server rejects it as an unknown
// request (proven by TestE2E/ExtractVariable). So we advertise only the four
// commands whose actions this client can actually honor.
var clientCommands = []string{
	"rust-analyzer.runSingle",
	"rust-analyzer.debugSingle",
	"rust-analyzer.showReferences",
	"rust-analyzer.gotoLocation",
}
