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
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/cli/mcptools"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// parsePosition parses line and col strings as uint32.
func parsePosition(
	lineStr, colStr string,
) (semanticapi.Position, error) {
	line, err := strconv.ParseUint(lineStr, 10, 32)
	if err != nil {
		return semanticapi.Position{}, fmt.Errorf(
			"invalid line %q: %w", lineStr, err,
		)
	}
	col, err := strconv.ParseUint(colStr, 10, 32)
	if err != nil {
		return semanticapi.Position{}, fmt.Errorf(
			"invalid column %q: %w", colStr, err,
		)
	}
	return semanticapi.Position{
		Line:      uint32(line),
		Character: uint32(col),
	}, nil
}

func (a *app) getLSP(
	ctx context.Context,
) (semanticapi.LSP, error) {
	w, err := a.getWorkspace()
	if err != nil {
		return nil, err
	}
	return w.LSP(ctx), nil
}

// resolveSymbol returns the first site of the symbol name, resolved through the
// workspace's symbol index first; see mcptools.ResolveSymbol.
func (a *app) resolveSymbol(
	ctx context.Context,
	lsp semanticapi.LSP,
	name string,
	hint mcptools.SymbolResolveHint,
) (mcptools.SymbolPosition, error) {
	w, err := a.getWorkspace()
	if err != nil {
		return mcptools.SymbolPosition{}, err
	}
	return mcptools.ResolveSymbolBest(ctx, lsp, w.Parser(ctx), name, hint)
}

func printLocations(
	ctx context.Context,
	format string,
	result semanticapi.LocationResult,
) error {
	flat := mcptools.ToFlatLocationsFromResult(result)
	if format == "" {
		for _, f := range flat {
			fmt.Printf(
				"%s %d:%d-%d:%d\n",
				f.URI,
				f.StartLine, f.StartChar,
				f.EndLine, f.EndChar,
			)
		}
		return nil
	}
	it := iterator.FromSlice(flat)
	return printIterator(ctx, format, it, []string{
		"URI", "StartLine", "StartChar",
		"EndLine", "EndChar",
	})
}

func newLSPCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lsp",
		Short: "Language Server Protocol commands",
	}

	cmd.AddCommand(
		newLSPHoverCmd(a),
		newLSPDefinitionCmd(a),
		newLSPReferencesCmd(a),
		newLSPSymbolsCmd(a),
		newLSPWorkspaceSymbolsCmd(a),
		newLSPDiagnosticsCmd(a),
		newLSPWorkspaceDiagnosticsCmd(a),
		newLSPRenameCmd(a),
		newLSPCodeActionsCmd(a),
		newLSPCompletionCmd(a),
		newLSPSignatureHelpCmd(a),
		newLSPDeclarationCmd(a),
		newLSPTypeDefinitionCmd(a),
		newLSPImplementationCmd(a),
		newLSPFormattingCmd(a),
		newLSPPrepareRenameCmd(a),
		newLSPDocumentHighlightCmd(a),
		newLSPCodeLensCmd(a),
		newLSPRangeFormattingCmd(a),
		newLSPFoldingRangeCmd(a),
		newLSPSelectionRangeCmd(a),
		newLSPExecuteCommandCmd(a),
		newLSPInlayHintCmd(a),
		newLSPPrepareCallHierarchyCmd(a),
		newLSPIncomingCallsCmd(a),
		newLSPOutgoingCallsCmd(a),
		newLSPPrepareTypeHierarchyCmd(a),
		newLSPTypeSupertypesCmd(a),
		newLSPTypeSubtypesCmd(a),
		newLSPSemanticTokensFullCmd(a),
		newLSPSemanticTokensRangeCmd(a),
		newLSPDocumentColorCmd(a),
		newLSPColorPresentationCmd(a),
		newLSPDocumentLinkCmd(a),
		newLSPOnTypeFormattingCmd(a),
		newLSPLinkedEditingRangeCmd(a),
		newLSPMonikerCmd(a),
		newLSPInlineValueCmd(a),
	)

	return cmd
}

func newLSPHoverCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "hover (<file> <line> <col> | <symbol>)",
		Short: "Get hover info for a symbol",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			h, err := lsp.Hover(cmd.Context(), semanticapi.HoverParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: uri,
				},
				Position: pos,
			})
			if err != nil {
				return err
			}
			if h == nil {
				return printString(format, "", []string{"Contents"})
			}
			type hoverResult struct {
				Contents string `json:"contents"`
				Kind     string `json:"kind"`
			}
			r := hoverResult{
				Contents: h.Contents.Value,
				Kind:     string(h.Contents.Kind),
			}
			return printResult(
				cmd.Context(), format, r,
				func(v hoverResult) {
					fmt.Println(v.Contents)
				},
				[]string{"Contents", "Kind"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPDefinitionCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "definition (<file> <line> <col> | <symbol>)",
		Short: "Go to definition",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			locs, err := lsp.Definition(
				cmd.Context(), semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			return printLocations(cmd.Context(), format, locs)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPReferencesCmd(a *app) *cobra.Command {
	var format string
	var includeDecl bool

	cmd := &cobra.Command{
		Use:   "references (<file> <line> <col> | <symbol>)",
		Short: "Find all references",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			locs, err := lsp.References(
				cmd.Context(), semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
					Context: semanticapi.ReferenceContext{
						IncludeDeclaration: includeDecl,
					},
				},
			)
			if err != nil {
				return err
			}
			return printLocations(cmd.Context(), format,
				semanticapi.LocationResult{Locations: locs})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVarP(
		&includeDecl, "declaration", "d", false,
		"Include declaration",
	)

	return cmd
}

func newLSPSymbolsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "symbols <file>",
		Short: "List document symbols",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			syms, err := lsp.DocumentSymbol(
				cmd.Context(), semanticapi.DocumentSymbolParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := mcptools.FlattenSymbolResult(syms)
			if format == "" {
				for _, s := range flat {
					indent := strings.Repeat("  ", s.Depth)
					fmt.Printf(
						"%s%s [%s] %d:%d-%d:%d\n",
						indent, s.Name, s.Kind,
						s.StartLine, s.StartChar,
						s.EndLine, s.EndChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Detail", "Kind", "Depth",
				"StartLine", "StartChar",
				"EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPWorkspaceSymbolsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "workspace-symbols <query>",
		Short: "Search workspace symbols",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			syms, err := lsp.WorkspaceSymbol(
				cmd.Context(), semanticapi.WorkspaceSymbolParams{
					Query: args[0],
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatWorkspaceSymbol, len(syms))
			for i, s := range syms {
				flat[i] = mcptools.FlatWorkspaceSymbol{
					Name:      s.Name,
					Kind:      mcptools.SymbolKindString(s.Kind),
					URI:       s.Location.URI,
					StartLine: s.Location.Range.Start.Line,
					StartChar: s.Location.Range.Start.Character,
				}
			}
			if format == "" {
				for _, s := range flat {
					fmt.Printf(
						"%s %s %s:%d:%d\n",
						s.Name, s.Kind,
						s.URI,
						s.StartLine, s.StartChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Kind", "URI",
				"StartLine", "StartChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPDiagnosticsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "diagnostics <file>",
		Short: "Get diagnostics for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			report, err := lsp.Diagnostic(
				cmd.Context(), semanticapi.DocumentDiagnosticParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatDiagnostic, len(report.Items))
			for i, d := range report.Items {
				flat[i] = mcptools.FlatDiagnostic{
					Severity:  mcptools.SeverityString(d.Severity),
					StartLine: d.Range.Start.Line,
					StartChar: d.Range.Start.Character,
					EndLine:   d.Range.End.Line,
					EndChar:   d.Range.End.Character,
					Message:   d.Message,
					Source:    d.Source,
					Code:      d.Code,
				}
			}
			if format == "" {
				for _, d := range flat {
					fmt.Printf(
						"[%s] %d:%d: %s (%s, %s)\n",
						d.Severity,
						d.StartLine, d.StartChar,
						d.Message, d.Source, d.Code,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Severity", "StartLine", "StartChar",
				"EndLine", "EndChar",
				"Message", "Source", "Code",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPWorkspaceDiagnosticsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "workspace-diagnostics",
		Short: "Get diagnostics for the entire workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			report, err := lsp.WorkspaceDiagnostic(
				cmd.Context(), semanticapi.WorkspaceDiagnosticParams{},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatWorkspaceDiagnostic, 0, len(report.Items))
			for _, doc := range report.Items {
				for _, d := range doc.Items {
					flat = append(flat, mcptools.FlatWorkspaceDiagnostic{
						URI:       doc.URI,
						Severity:  mcptools.SeverityString(d.Severity),
						StartLine: d.Range.Start.Line,
						StartChar: d.Range.Start.Character,
						EndLine:   d.Range.End.Line,
						EndChar:   d.Range.End.Character,
						Message:   d.Message,
						Source:    d.Source,
						Code:      d.Code,
					})
				}
			}
			if format == "" {
				for _, d := range flat {
					fmt.Printf(
						"[%s] %s %d:%d: %s (%s, %s)\n",
						d.Severity,
						d.URI,
						d.StartLine, d.StartChar,
						d.Message, d.Source, d.Code,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"URI", "Severity", "StartLine", "StartChar",
				"EndLine", "EndChar",
				"Message", "Source", "Code",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatRenameEdit struct {
	URI   string `json:"uri"`
	Edits int    `json:"edits"`
}

func newLSPRenameCmd(a *app) *cobra.Command {
	var format string
	var dryRun bool
	var noColor bool

	cmd := &cobra.Command{
		Use:   "rename (<file> <line> <col> <new-name> | <symbol> <new-name>)",
		Short: "Rename a symbol",
		Args:  cobra.RangeArgs(2, 4),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			var newName string
			if len(args) == 4 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
				newName = args[3]
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
				newName = args[1]
			}
			edit, err := lsp.Rename(
				cmd.Context(), semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
					NewName:  newName,
				},
			)
			if err != nil {
				return err
			}
			slog.Debug("rename: LSP response", "uri", uri, "nil", edit == nil)
			if edit != nil {
				for u, es := range edit.Changes {
					for i, e := range es {
						slog.Debug("rename: edit", "file", u, "index", i,
							"start_line", e.Range.Start.Line, "start_char", e.Range.Start.Character,
							"end_line", e.Range.End.Line, "end_char", e.Range.End.Character,
							"new_text", e.NewText)
					}
				}
			}
			if edit == nil {
				return printString(
					format, "no edits",
					[]string{"Result"},
				)
			}
			fe := workspaceEditToFileEdits(edit)
			if dryRun {
				var flat []flatRenameEdit
				for u, edits := range edit.Changes {
					flat = append(flat, flatRenameEdit{
						URI:   u,
						Edits: len(edits),
					})
				}
				if format == "" {
					for _, f := range flat {
						fmt.Printf("%s %d\n", f.URI, f.Edits)
					}
					return nil
				}
				it := iterator.FromSlice(flat)
				return printIterator(
					cmd.Context(), format, it,
					[]string{"URI", "Edits"},
				)
			}
			return a.handleEdits(
				cmd.Context(), fe, dryRun, noColor,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVar(
		&dryRun, "dry-run", false,
		"Preview changes without applying",
	)
	cmd.Flags().BoolVar(
		&noColor, "no-color", false,
		"Disable colored diff output",
	)

	return cmd
}

type flatCodeActionEdit struct {
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	StartLine uint32 `json:"start_line"`
	StartChar uint32 `json:"start_char"`
	EndLine   uint32 `json:"end_line"`
	EndChar   uint32 `json:"end_char"`
	NewText   string `json:"new_text"`
}

type flatCodeActionCommand struct {
	Title     string      `json:"title"`
	Command   string      `json:"command"`
	Arguments commandArgs `json:"arguments"`
}

// commandArgs holds command arguments as raw JSON but
// prints as a comma-separated list for text/table output.
type commandArgs json.RawMessage

func (a commandArgs) MarshalJSON() ([]byte, error) {
	if len(a) == 0 {
		return []byte("[]"), nil
	}
	return json.RawMessage(a), nil
}

func (a commandArgs) String() string {
	if len(a) == 0 {
		return ""
	}
	var items []json.RawMessage
	if err := json.Unmarshal(a, &items); err != nil {
		return string(a)
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = string(item)
	}
	return strings.Join(parts, ", ")
}

func fetchCodeActions(
	a *app,
	cmd *cobra.Command,
	args []string,
) ([]semanticapi.CodeActionResult, error) {
	uri, err := a.resolveURIArg(cmd.Context(), args[0])
	if err != nil {
		return nil, err
	}
	pos, err := parsePosition(args[1], args[2])
	if err != nil {
		return nil, err
	}
	lsp, err := a.getLSP(cmd.Context())
	if err != nil {
		return nil, err
	}
	r := semanticapi.Range{Start: pos, End: pos}
	return lsp.CodeAction(
		cmd.Context(),
		semanticapi.CodeActionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{
				URI: uri.String(),
			},
			Range: r,
			Context: semanticapi.CodeActionContext{
				Diagnostics: []semanticapi.Diagnostic{},
			},
		},
	)
}

func newLSPCodeActionsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "code-actions <file> <line> <col>",
		Short: "List code actions at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(
			cmd *cobra.Command, args []string,
		) (retErr error) {
			defer func() {
				retErr = formatError(format, retErr)
			}()
			actions, err := fetchCodeActions(
				a, cmd, args,
			)
			if err != nil {
				return err
			}
			return printCodeActions(
				cmd.Context(), format, actions,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	cmd.AddCommand(
		newLSPCodeActionEditsCmd(a),
		newLSPCodeActionCommandsCmd(a),
	)

	return cmd
}

func newLSPCodeActionEditsCmd(
	a *app,
) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "edits <file> <line> <col>",
		Short: "List code action edits at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(
			cmd *cobra.Command, args []string,
		) (retErr error) {
			defer func() {
				retErr = formatError(format, retErr)
			}()
			actions, err := fetchCodeActions(
				a, cmd, args,
			)
			if err != nil {
				return err
			}
			return printCodeActionEdits(
				cmd.Context(), format, actions,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPCodeActionCommandsCmd(
	a *app,
) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "commands <file> <line> <col>",
		Short: "List code action commands at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(
			cmd *cobra.Command, args []string,
		) (retErr error) {
			defer func() {
				retErr = formatError(format, retErr)
			}()
			actions, err := fetchCodeActions(
				a, cmd, args,
			)
			if err != nil {
				return err
			}
			return printCodeActionCommands(
				cmd.Context(), format, actions,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func printCodeActions(
	ctx context.Context,
	format string,
	actions []semanticapi.CodeActionResult,
) error {
	flat := make([]mcptools.FlatCodeAction, 0, len(actions))
	for _, a := range actions {
		if a.CodeAction != nil {
			flat = append(flat, mcptools.FlatCodeAction{
				Title: a.CodeAction.Title,
				Kind:  string(a.CodeAction.Kind),
			})
		} else if a.Command != nil {
			flat = append(flat, mcptools.FlatCodeAction{
				Title: a.Command.Title,
				Kind:  "command",
			})
		}
	}
	if format == "" {
		for _, a := range flat {
			fmt.Printf("[%s] %q\n", a.Kind, a.Title)
		}
		return nil
	}
	it := iterator.FromSlice(flat)
	return printIterator(ctx, format, it, []string{
		"Title", "Kind",
	})
}

func printCodeActionEdits(
	ctx context.Context,
	format string,
	actions []semanticapi.CodeActionResult,
) error {
	var flat []flatCodeActionEdit
	for _, a := range actions {
		ca := a.CodeAction
		if ca == nil || ca.Edit == nil {
			continue
		}
		flat = append(
			flat,
			flattenWorkspaceEdit(
				ca.Title, string(ca.Kind), ca.Edit,
			)...,
		)
	}
	if format == "" {
		for _, e := range flat {
			fmt.Printf(
				"%q [%s] %s %d:%d-%d:%d %q\n",
				e.Title, e.Kind, e.URI,
				e.StartLine, e.StartChar,
				e.EndLine, e.EndChar,
				e.NewText,
			)
		}
		return nil
	}
	it := iterator.FromSlice(flat)
	return printIterator(ctx, format, it, []string{
		"Title", "Kind", "URI",
		"StartLine", "StartChar",
		"EndLine", "EndChar", "NewText",
	})
}

func flattenWorkspaceEdit(
	title, kind string,
	edit *semanticapi.WorkspaceEdit,
) []flatCodeActionEdit {
	var out []flatCodeActionEdit
	if len(edit.Changes) > 0 {
		uris := make([]string, 0, len(edit.Changes))
		for u := range edit.Changes {
			uris = append(uris, u)
		}
		sort.Strings(uris)
		for _, u := range uris {
			for _, te := range edit.Changes[u] {
				out = append(out, flatCodeActionEdit{
					Title:     title,
					Kind:      kind,
					URI:       u,
					StartLine: te.Range.Start.Line,
					StartChar: te.Range.Start.Character,
					EndLine:   te.Range.End.Line,
					EndChar:   te.Range.End.Character,
					NewText:   te.NewText,
				})
			}
		}
	}
	for _, dc := range edit.DocumentChanges {
		tde := dc.TextDocumentEdit
		if tde == nil {
			continue
		}
		u := tde.TextDocument.URI
		for _, te := range tde.Edits {
			out = append(out, flatCodeActionEdit{
				Title:     title,
				Kind:      kind,
				URI:       u,
				StartLine: te.Range.Start.Line,
				StartChar: te.Range.Start.Character,
				EndLine:   te.Range.End.Line,
				EndChar:   te.Range.End.Character,
				NewText:   te.NewText,
			})
		}
	}
	return out
}

func printCodeActionCommands(
	ctx context.Context,
	format string,
	actions []semanticapi.CodeActionResult,
) error {
	var flat []flatCodeActionCommand
	for _, a := range actions {
		if c := a.Command; c != nil {
			flat = append(flat, flattenCommand(c))
		}
		if a.CodeAction != nil && a.CodeAction.Command != nil {
			flat = append(
				flat,
				flattenCommand(a.CodeAction.Command),
			)
		}
	}
	if format == "" {
		for _, c := range flat {
			fmt.Printf(
				"%q %s %s\n",
				c.Title, c.Command, c.Arguments,
			)
		}
		return nil
	}
	it := iterator.FromSlice(flat)
	return printIterator(ctx, format, it, []string{
		"Title", "Command", "Arguments",
	})
}

func flattenCommand(
	c *semanticapi.Command,
) flatCodeActionCommand {
	args := commandArgs("[]")
	if len(c.Arguments) > 0 {
		b, err := json.Marshal(c.Arguments)
		if err == nil {
			args = commandArgs(b)
		}
	}
	return flatCodeActionCommand{
		Title:     c.Title,
		Command:   c.Command,
		Arguments: args,
	}
}

func newLSPCompletionCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "completion <file> <line> <col>",
		Short: "Get completion suggestions at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			result, err := lsp.Completion(
				cmd.Context(), semanticapi.CompletionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatCompletion, len(result.Items))
			for i, item := range result.Items {
				flat[i] = mcptools.FlatCompletion{
					Label:  item.Label,
					Kind:   mcptools.CompletionItemKindString(item.Kind),
					Detail: item.Detail,
				}
			}
			if format == "" {
				for _, c := range flat {
					fmt.Printf("[%s] %s", c.Kind, c.Label)
					if c.Detail != "" {
						fmt.Printf(" - %s", c.Detail)
					}
					fmt.Println()
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Label", "Kind", "Detail",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPSignatureHelpCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "signature-help <file> <line> <col>",
		Short: "Get signature help at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			result, err := lsp.SignatureHelp(
				cmd.Context(), semanticapi.SignatureHelpParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			if result == nil || len(result.Signatures) == 0 {
				return printString(format, "", []string{"Label"})
			}
			var params []string
			for _, sig := range result.Signatures {
				for _, p := range sig.Parameters {
					params = append(params, p.Label)
				}
			}
			r := mcptools.FlatSignatureHelp{
				Label:           result.Signatures[0].Label,
				ActiveSignature: result.ActiveSignature,
				ActiveParameter: result.ActiveParameter,
				Parameters:      strings.Join(params, ", "),
			}
			return printResult(
				cmd.Context(), format, r,
				func(v mcptools.FlatSignatureHelp) {
					fmt.Println(v.Label)
					if v.Parameters != "" {
						fmt.Printf("  Parameters: %s\n", v.Parameters)
					}
				},
				[]string{"Label", "ActiveSignature", "ActiveParameter", "Parameters"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPDeclarationCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "declaration (<file> <line> <col> | <symbol>)",
		Short: "Go to declaration",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			locs, err := lsp.Declaration(
				cmd.Context(), semanticapi.DeclarationParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			return printLocations(cmd.Context(), format, locs)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPTypeDefinitionCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "type-definition (<file> <line> <col> | <symbol>)",
		Short: "Go to type definition",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintTypeDefinition,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			locs, err := lsp.TypeDefinition(
				cmd.Context(), semanticapi.TypeDefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			return printLocations(cmd.Context(), format, locs)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPImplementationCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "implementation (<file> <line> <col> | <symbol>)",
		Short: "Find implementations",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintImplementation,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			locs, err := lsp.Implementation(
				cmd.Context(), semanticapi.ImplementationParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			return printLocations(cmd.Context(), format, locs)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPFormattingCmd(a *app) *cobra.Command {
	var format string
	var dryRun bool
	var noColor bool
	var tabSize uint32
	var insertSpaces bool

	cmd := &cobra.Command{
		Use:   "formatting <file>",
		Short: "Format a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			edits, err := lsp.Formatting(
				cmd.Context(), semanticapi.DocumentFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Options: semanticapi.FormattingOptions{
						TabSize:      tabSize,
						InsertSpaces: insertSpaces,
					},
				},
			)
			if err != nil {
				return err
			}
			slog.Debug("formatting: LSP response", "uri", uri.String(), "num_edits", len(edits))
			for i, e := range edits {
				slog.Debug("formatting: edit", "index", i,
					"start_line", e.Range.Start.Line, "start_char", e.Range.Start.Character,
					"end_line", e.Range.End.Line, "end_char", e.Range.End.Character,
					"new_text", e.NewText)
			}
			fe := textEditsToFileEdits(uri.String(), edits)
			if dryRun && format != "" {
				flat := make([]mcptools.FlatTextEdit, len(edits))
				for i, e := range edits {
					flat[i] = mcptools.FlatTextEdit{
						StartLine: e.Range.Start.Line,
						StartChar: e.Range.Start.Character,
						EndLine:   e.Range.End.Line,
						EndChar:   e.Range.End.Character,
						NewText:   e.NewText,
					}
				}
				it := iterator.FromSlice(flat)
				return printIterator(
					cmd.Context(), format, it,
					[]string{
						"StartLine", "StartChar",
						"EndLine", "EndChar", "NewText",
					},
				)
			}
			return a.handleEdits(
				cmd.Context(), fe, dryRun, noColor,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVar(
		&dryRun, "dry-run", false,
		"Preview changes without applying",
	)
	cmd.Flags().BoolVar(
		&noColor, "no-color", false,
		"Disable colored diff output",
	)
	cmd.Flags().Uint32Var(
		&tabSize, "tab-size", 4,
		"Tab size for formatting",
	)
	cmd.Flags().BoolVar(
		&insertSpaces, "insert-spaces", true,
		"Use spaces instead of tabs",
	)

	return cmd
}

func newLSPPrepareRenameCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "prepare-rename (<file> <line> <col> | <symbol>)",
		Short: "Prepare for rename operation",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintDefault,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			result, err := lsp.PrepareRename(
				cmd.Context(), semanticapi.PrepareRenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			if result == nil {
				return printString(format, "rename not supported", []string{"Result"})
			}
			r := mcptools.FlatPrepareRename{
				StartLine:   result.Range.Start.Line,
				StartChar:   result.Range.Start.Character,
				EndLine:     result.Range.End.Line,
				EndChar:     result.Range.End.Character,
				Placeholder: result.Placeholder,
			}
			return printResult(
				cmd.Context(), format, r,
				func(v mcptools.FlatPrepareRename) {
					fmt.Printf(
						"%d:%d-%d:%d",
						v.StartLine, v.StartChar,
						v.EndLine, v.EndChar,
					)
					if v.Placeholder != "" {
						fmt.Printf(" %q", v.Placeholder)
					}
					fmt.Println()
				},
				[]string{
					"StartLine", "StartChar", "EndLine", "EndChar", "Placeholder",
				},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPDocumentHighlightCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "document-highlight <file> <line> <col>",
		Short: "Get document highlights at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			highlights, err := lsp.DocumentHighlight(
				cmd.Context(), semanticapi.DocumentHighlightParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatDocumentHighlight, len(highlights))
			for i, h := range highlights {
				flat[i] = mcptools.FlatDocumentHighlight{
					Kind:      mcptools.DocumentHighlightKindString(h.Kind),
					StartLine: h.Range.Start.Line,
					StartChar: h.Range.Start.Character,
					EndLine:   h.Range.End.Line,
					EndChar:   h.Range.End.Character,
				}
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"[%s] %d:%d-%d:%d\n",
						h.Kind,
						h.StartLine, h.StartChar,
						h.EndLine, h.EndChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Kind", "StartLine", "StartChar", "EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPCodeLensCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "code-lens <file>",
		Short: "Get code lenses for a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			lenses, err := lsp.CodeLens(
				cmd.Context(), semanticapi.CodeLensParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatCodeLens, len(lenses))
			for i, l := range lenses {
				var title, command string
				if l.Command != nil {
					title = l.Command.Title
					command = l.Command.Command
				}
				flat[i] = mcptools.FlatCodeLens{
					StartLine: l.Range.Start.Line,
					StartChar: l.Range.Start.Character,
					EndLine:   l.Range.End.Line,
					EndChar:   l.Range.End.Character,
					Title:     title,
					Command:   command,
				}
			}
			if format == "" {
				for _, l := range flat {
					fmt.Printf(
						"%d:%d-%d:%d %s (%s)\n",
						l.StartLine, l.StartChar,
						l.EndLine, l.EndChar,
						l.Title, l.Command,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar", "Title", "Command",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func parseRange(
	startLineStr, startColStr, endLineStr, endColStr string,
) (semanticapi.Range, error) {
	start, err := parsePosition(startLineStr, startColStr)
	if err != nil {
		return semanticapi.Range{}, err
	}
	end, err := parsePosition(endLineStr, endColStr)
	if err != nil {
		return semanticapi.Range{}, err
	}
	return semanticapi.Range{Start: start, End: end}, nil
}

func newLSPRangeFormattingCmd(a *app) *cobra.Command {
	var format string
	var dryRun bool
	var noColor bool
	var tabSize uint32
	var insertSpaces bool

	cmd := &cobra.Command{
		Use:   "range-formatting <file> <start-line> <start-col> <end-line> <end-col>",
		Short: "Format a range in a document",
		Args:  cobra.ExactArgs(5),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			r, err := parseRange(args[1], args[2], args[3], args[4])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			edits, err := lsp.RangeFormatting(
				cmd.Context(), semanticapi.DocumentRangeFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Range: r,
					Options: semanticapi.FormattingOptions{
						TabSize:      tabSize,
						InsertSpaces: insertSpaces,
					},
				},
			)
			if err != nil {
				return err
			}
			slog.Debug("range-formatting: LSP response",
				"uri", uri.String(), "num_edits", len(edits))
			for i, e := range edits {
				slog.Debug("range-formatting: edit", "index", i,
					"start_line", e.Range.Start.Line, "start_char", e.Range.Start.Character,
					"end_line", e.Range.End.Line, "end_char", e.Range.End.Character,
					"new_text", e.NewText)
			}
			fe := textEditsToFileEdits(uri.String(), edits)
			if dryRun && format != "" {
				flat := make([]mcptools.FlatTextEdit, len(edits))
				for i, e := range edits {
					flat[i] = mcptools.FlatTextEdit{
						StartLine: e.Range.Start.Line,
						StartChar: e.Range.Start.Character,
						EndLine:   e.Range.End.Line,
						EndChar:   e.Range.End.Character,
						NewText:   e.NewText,
					}
				}
				it := iterator.FromSlice(flat)
				return printIterator(
					cmd.Context(), format, it,
					[]string{
						"StartLine", "StartChar",
						"EndLine", "EndChar", "NewText",
					},
				)
			}
			return a.handleEdits(
				cmd.Context(), fe, dryRun, noColor,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVar(
		&dryRun, "dry-run", false,
		"Preview changes without applying",
	)
	cmd.Flags().BoolVar(
		&noColor, "no-color", false,
		"Disable colored diff output",
	)
	cmd.Flags().Uint32Var(
		&tabSize, "tab-size", 4,
		"Tab size for formatting",
	)
	cmd.Flags().BoolVar(
		&insertSpaces, "insert-spaces", true,
		"Use spaces instead of tabs",
	)

	return cmd
}

func newLSPFoldingRangeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "folding-range <file>",
		Short: "Get folding ranges for a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			ranges, err := lsp.FoldingRange(
				cmd.Context(), semanticapi.FoldingRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatFoldingRange, len(ranges))
			for i, r := range ranges {
				flat[i] = mcptools.FlatFoldingRange{
					StartLine: r.StartLine,
					StartChar: r.StartCharacter,
					EndLine:   r.EndLine,
					EndChar:   r.EndCharacter,
					Kind:      mcptools.FoldingRangeKindString(r.Kind),
				}
			}
			if format == "" {
				for _, r := range flat {
					fmt.Printf(
						"%d:%d-%d:%d [%s]\n",
						r.StartLine, r.StartChar,
						r.EndLine, r.EndChar,
						r.Kind,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar", "Kind",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPSelectionRangeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "selection-range <file> <line> <col>",
		Short: "Get selection ranges at positions",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			ranges, err := lsp.SelectionRange(
				cmd.Context(), semanticapi.SelectionRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Positions: []semanticapi.Position{pos},
				},
			)
			if err != nil {
				return err
			}
			flat := mcptools.FlattenSelectionRanges(ranges)
			if format == "" {
				for _, r := range flat {
					indent := strings.Repeat("  ", r.Depth)
					fmt.Printf(
						"%s%d:%d-%d:%d\n",
						indent,
						r.StartLine, r.StartChar,
						r.EndLine, r.EndChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar", "Depth",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPExecuteCommandCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "execute-command <command> [args...]",
		Short: "Execute a workspace command",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()

			var arguments []json.RawMessage
			for _, a := range args[1:] {
				if !json.Valid([]byte(a)) {
					return fmt.Errorf("invalid argument %q:"+
						" expected a JSON value (string, number, bool,"+
						" object, array, or null)", a)
				}
				arguments = append(arguments, json.RawMessage(a))
			}

			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			result, err := lsp.ExecuteCommand(
				cmd.Context(), semanticapi.ExecuteCommandParams{
					Command:   args[0],
					Arguments: arguments,
				},
			)
			if err != nil {
				return err
			}
			type execResult struct {
				Result string `json:"result"`
			}
			return printResult(
				cmd.Context(), format, execResult{Result: result},
				func(v execResult) {
					if v.Result != "" {
						fmt.Println(v.Result)
					} else {
						fmt.Println("OK")
					}
				},
				[]string{"Result"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func inlayHintKindString(k semanticapi.InlayHintKind) string {
	switch k {
	case semanticapi.InlayHintKindType:
		return "Type"
	case semanticapi.InlayHintKindParameter:
		return "Parameter"
	default:
		return fmt.Sprintf("Unknown(%d)", k)
	}
}

type flatInlayHint struct {
	Line  uint32 `json:"line"`
	Char  uint32 `json:"char"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

func newLSPInlayHintCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "inlay-hint <file> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get inlay hints for a range",
		Args:  cobra.ExactArgs(5),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			r, err := parseRange(args[1], args[2], args[3], args[4])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			hints, err := lsp.InlayHint(
				cmd.Context(), semanticapi.InlayHintParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Range: r,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatInlayHint, len(hints))
			for i, h := range hints {
				flat[i] = flatInlayHint{
					Line:  h.Position.Line,
					Char:  h.Position.Character,
					Kind:  inlayHintKindString(h.Kind),
					Label: h.Label,
				}
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"%d:%d [%s] %s\n",
						h.Line, h.Char,
						h.Kind, h.Label,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Line", "Char", "Kind", "Label",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPPrepareCallHierarchyCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "prepare-call-hierarchy (<file> <line> <col> | <symbol>)",
		Short: "Prepare call hierarchy at a position",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintCallHierarchy,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			items, err := lsp.PrepareCallHierarchy(
				cmd.Context(), semanticapi.CallHierarchyPrepareParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatCallHierarchyItem, len(items))
			for i, item := range items {
				flat[i] = mcptools.ToFlatCallHierarchyItem(item)
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"%s [%s] %s %d:%d-%d:%d\n",
						h.Name, h.Kind, h.URI,
						h.StartLine, h.StartChar, h.EndLine, h.EndChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Kind", "URI", "StartLine", "StartChar", "EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPIncomingCallsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "incoming-calls <name> <kind> <uri> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get incoming calls for a call hierarchy item",
		Args:  cobra.ExactArgs(7),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			r, err := parseRange(args[3], args[4], args[5], args[6])
			if err != nil {
				return err
			}
			kind := mcptools.ParseSymbolKind(args[1])
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			calls, err := lsp.CallHierarchyIncomingCalls(
				cmd.Context(), semanticapi.CallHierarchyIncomingCallsParams{
					Item: semanticapi.CallHierarchyItem{
						Name:           args[0],
						Kind:           kind,
						URI:            args[2],
						Range:          r,
						SelectionRange: r,
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatIncomingCall, len(calls))
			for i, c := range calls {
				flat[i] = mcptools.FlatIncomingCall{
					FromName:      c.From.Name,
					FromKind:      mcptools.SymbolKindString(c.From.Kind),
					FromURI:       c.From.URI,
					FromStartLine: c.From.Range.Start.Line,
					FromStartChar: c.From.Range.Start.Character,
				}
			}
			if format == "" {
				for _, c := range flat {
					fmt.Printf(
						"%s [%s] %s:%d:%d\n",
						c.FromName, c.FromKind, c.FromURI,
						c.FromStartLine, c.FromStartChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"FromName", "FromKind", "FromURI", "FromStartLine", "FromStartChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPOutgoingCallsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "outgoing-calls <name> <kind> <uri> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get outgoing calls for a call hierarchy item",
		Args:  cobra.ExactArgs(7),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			r, err := parseRange(args[3], args[4], args[5], args[6])
			if err != nil {
				return err
			}
			kind := mcptools.ParseSymbolKind(args[1])
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			calls, err := lsp.CallHierarchyOutgoingCalls(
				cmd.Context(), semanticapi.CallHierarchyOutgoingCallsParams{
					Item: semanticapi.CallHierarchyItem{
						Name:           args[0],
						Kind:           kind,
						URI:            args[2],
						Range:          r,
						SelectionRange: r,
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatOutgoingCall, len(calls))
			for i, c := range calls {
				flat[i] = mcptools.FlatOutgoingCall{
					ToName:      c.To.Name,
					ToKind:      mcptools.SymbolKindString(c.To.Kind),
					ToURI:       c.To.URI,
					ToStartLine: c.To.Range.Start.Line,
					ToStartChar: c.To.Range.Start.Character,
				}
			}
			if format == "" {
				for _, c := range flat {
					fmt.Printf(
						"%s [%s] %s:%d:%d\n",
						c.ToName, c.ToKind, c.ToURI,
						c.ToStartLine, c.ToStartChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"ToName", "ToKind", "ToURI", "ToStartLine", "ToStartChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPPrepareTypeHierarchyCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "prepare-type-hierarchy (<file> <line> <col> | <symbol>)",
		Short: "Prepare type hierarchy at a position",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			var uri string
			var pos semanticapi.Position
			if len(args) == 3 {
				u, err := a.resolveURIArg(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				pos, err = parsePosition(args[1], args[2])
				if err != nil {
					return err
				}
				uri = u.String()
			} else {
				sp, err := a.resolveSymbol(
					cmd.Context(), lsp, args[0], mcptools.HintTypeHierarchy,
				)
				if err != nil {
					return err
				}
				uri = sp.URI
				pos = sp.Position
			}
			items, err := lsp.PrepareTypeHierarchy(
				cmd.Context(), semanticapi.TypeHierarchyPrepareParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatTypeHierarchyItem, len(items))
			for i, item := range items {
				flat[i] = mcptools.ToFlatTypeHierarchyItem(item)
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"%s [%s] %s %d:%d-%d:%d\n",
						h.Name, h.Kind, h.URI,
						h.StartLine, h.StartChar, h.EndLine, h.EndChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Kind", "URI", "Detail",
				"StartLine", "StartChar", "EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPTypeSupertypesCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "type-supertypes <name> <kind> <uri> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get supertypes for a type hierarchy item",
		Args:  cobra.ExactArgs(7),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			r, err := parseRange(args[3], args[4], args[5], args[6])
			if err != nil {
				return err
			}
			kind := mcptools.ParseSymbolKind(args[1])
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			items, err := lsp.TypeHierarchySupertypes(
				cmd.Context(), semanticapi.TypeHierarchySupertypesParams{
					Item: semanticapi.TypeHierarchyItem{
						Name:           args[0],
						Kind:           kind,
						URI:            args[2],
						Range:          r,
						SelectionRange: r,
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatTypeHierarchyItem, len(items))
			for i, item := range items {
				flat[i] = mcptools.ToFlatTypeHierarchyItem(item)
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"%s [%s] %s:%d:%d\n",
						h.Name, h.Kind, h.URI,
						h.StartLine, h.StartChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Kind", "URI", "Detail",
				"StartLine", "StartChar", "EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPTypeSubtypesCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "type-subtypes <name> <kind> <uri> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get subtypes for a type hierarchy item",
		Args:  cobra.ExactArgs(7),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			r, err := parseRange(args[3], args[4], args[5], args[6])
			if err != nil {
				return err
			}
			kind := mcptools.ParseSymbolKind(args[1])
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			items, err := lsp.TypeHierarchySubtypes(
				cmd.Context(), semanticapi.TypeHierarchySubtypesParams{
					Item: semanticapi.TypeHierarchyItem{
						Name:           args[0],
						Kind:           kind,
						URI:            args[2],
						Range:          r,
						SelectionRange: r,
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]mcptools.FlatTypeHierarchyItem, len(items))
			for i, item := range items {
				flat[i] = mcptools.ToFlatTypeHierarchyItem(item)
			}
			if format == "" {
				for _, h := range flat {
					fmt.Printf(
						"%s [%s] %s:%d:%d\n",
						h.Name, h.Kind, h.URI,
						h.StartLine, h.StartChar,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Name", "Kind", "URI", "Detail",
				"StartLine", "StartChar", "EndLine", "EndChar",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatSemanticTokens struct {
	ResultID string `json:"result_id"`
	Data     string `json:"data"`
}

func newLSPSemanticTokensFullCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "semantic-tokens-full <file>",
		Short: "Get full semantic tokens for a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			tokens, err := lsp.SemanticTokensFull(
				cmd.Context(), semanticapi.SemanticTokensParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			if tokens == nil {
				fmt.Println("No semantic tokens")
				return nil
			}
			dataStr := fmt.Sprintf("%v", tokens.Data)
			flat := flatSemanticTokens{
				ResultID: tokens.ResultID,
				Data:     dataStr,
			}
			if format == "" {
				fmt.Printf("ResultID: %s\n", flat.ResultID)
				fmt.Printf("Data (%d tokens): %s\n", len(tokens.Data)/5, dataStr)
				return nil
			}
			return printResult(
				cmd.Context(), format, flat,
				func(v flatSemanticTokens) {
					fmt.Printf("ResultID: %s\n", v.ResultID)
					fmt.Printf("Data: %s\n", v.Data)
				},
				[]string{"ResultID", "Data"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPSemanticTokensRangeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "semantic-tokens-range <file> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get semantic tokens for a range in a document",
		Args:  cobra.ExactArgs(5),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			r, err := parseRange(args[1], args[2], args[3], args[4])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			tokens, err := lsp.SemanticTokensRange(
				cmd.Context(), semanticapi.SemanticTokensRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Range: r,
				},
			)
			if err != nil {
				return err
			}
			if tokens == nil {
				fmt.Println("No semantic tokens")
				return nil
			}
			dataStr := fmt.Sprintf("%v", tokens.Data)
			flat := flatSemanticTokens{
				ResultID: tokens.ResultID,
				Data:     dataStr,
			}
			if format == "" {
				fmt.Printf("ResultID: %s\n", flat.ResultID)
				fmt.Printf("Data (%d tokens): %s\n", len(tokens.Data)/5, dataStr)
				return nil
			}
			return printResult(
				cmd.Context(), format, flat,
				func(v flatSemanticTokens) {
					fmt.Printf("ResultID: %s\n", v.ResultID)
					fmt.Printf("Data: %s\n", v.Data)
				},
				[]string{"ResultID", "Data"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatColorInformation struct {
	StartLine uint32  `json:"start_line"`
	StartChar uint32  `json:"start_char"`
	EndLine   uint32  `json:"end_line"`
	EndChar   uint32  `json:"end_char"`
	Red       float64 `json:"red"`
	Green     float64 `json:"green"`
	Blue      float64 `json:"blue"`
	Alpha     float64 `json:"alpha"`
}

func newLSPDocumentColorCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "document-color <file>",
		Short: "Get colors in a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			colors, err := lsp.DocumentColor(
				cmd.Context(), semanticapi.DocumentColorParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatColorInformation, len(colors))
			for i, c := range colors {
				flat[i] = flatColorInformation{
					StartLine: c.Range.Start.Line,
					StartChar: c.Range.Start.Character,
					EndLine:   c.Range.End.Line,
					EndChar:   c.Range.End.Character,
					Red:       c.Color.Red,
					Green:     c.Color.Green,
					Blue:      c.Color.Blue,
					Alpha:     c.Color.Alpha,
				}
			}
			if format == "" {
				for _, c := range flat {
					fmt.Printf(
						"%d:%d-%d:%d rgba(%.2f, %.2f, %.2f, %.2f)\n",
						c.StartLine, c.StartChar, c.EndLine, c.EndChar,
						c.Red, c.Green, c.Blue, c.Alpha,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar",
				"Red", "Green", "Blue", "Alpha",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatColorPresentation struct {
	Label   string `json:"label"`
	NewText string `json:"new_text,omitempty"`
}

func newLSPColorPresentationCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "color-presentation <file> <start-line> <start-col> <end-line> <end-col> <red> <green> <blue> <alpha>",
		Short: "Get color presentations for a color",
		Args:  cobra.ExactArgs(9),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			r, err := parseRange(args[1], args[2], args[3], args[4])
			if err != nil {
				return err
			}
			red, err := strconv.ParseFloat(args[5], 64)
			if err != nil {
				return fmt.Errorf("invalid red: %w", err)
			}
			green, err := strconv.ParseFloat(args[6], 64)
			if err != nil {
				return fmt.Errorf("invalid green: %w", err)
			}
			blue, err := strconv.ParseFloat(args[7], 64)
			if err != nil {
				return fmt.Errorf("invalid blue: %w", err)
			}
			alpha, err := strconv.ParseFloat(args[8], 64)
			if err != nil {
				return fmt.Errorf("invalid alpha: %w", err)
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			pres, err := lsp.ColorPresentation(
				cmd.Context(), semanticapi.ColorPresentationParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Color: semanticapi.Color{
						Red: red, Green: green, Blue: blue, Alpha: alpha,
					},
					Range: r,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatColorPresentation, len(pres))
			for i, p := range pres {
				newText := ""
				if p.TextEdit != nil {
					newText = p.TextEdit.NewText
				}
				flat[i] = flatColorPresentation{
					Label:   p.Label,
					NewText: newText,
				}
			}
			if format == "" {
				for _, p := range flat {
					fmt.Printf("%s\n", p.Label)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Label", "NewText",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatDocumentLink struct {
	StartLine uint32 `json:"start_line"`
	StartChar uint32 `json:"start_char"`
	EndLine   uint32 `json:"end_line"`
	EndChar   uint32 `json:"end_char"`
	Target    string `json:"target"`
	Tooltip   string `json:"tooltip"`
}

func newLSPDocumentLinkCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "document-link <file>",
		Short: "Get links in a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			links, err := lsp.DocumentLink(
				cmd.Context(), semanticapi.DocumentLinkParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatDocumentLink, len(links))
			for i, l := range links {
				flat[i] = flatDocumentLink{
					StartLine: l.Range.Start.Line,
					StartChar: l.Range.Start.Character,
					EndLine:   l.Range.End.Line,
					EndChar:   l.Range.End.Character,
					Target:    l.Target,
					Tooltip:   l.Tooltip,
				}
			}
			if format == "" {
				for _, l := range flat {
					fmt.Printf(
						"%d:%d-%d:%d %s\n",
						l.StartLine, l.StartChar, l.EndLine, l.EndChar,
						l.Target,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar",
				"Target", "Tooltip",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newLSPOnTypeFormattingCmd(a *app) *cobra.Command {
	var format string
	var dryRun bool
	var noColor bool
	var tabSize uint32
	var insertSpaces bool

	cmd := &cobra.Command{
		Use:   "on-type-formatting <file> <line> <col> <character>",
		Short: "Get formatting edits triggered by typing a character",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			edits, err := lsp.OnTypeFormatting(
				cmd.Context(), semanticapi.DocumentOnTypeFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position:  pos,
					Character: args[3],
					Options: semanticapi.FormattingOptions{
						TabSize:      tabSize,
						InsertSpaces: insertSpaces,
					},
				},
			)
			if err != nil {
				return err
			}
			slog.Debug("on-type-formatting: LSP response",
				"uri", uri.String(), "num_edits", len(edits))
			for i, e := range edits {
				slog.Debug("on-type-formatting: edit", "index", i,
					"start_line", e.Range.Start.Line, "start_char", e.Range.Start.Character,
					"end_line", e.Range.End.Line, "end_char", e.Range.End.Character,
					"new_text", e.NewText)
			}
			fe := textEditsToFileEdits(uri.String(), edits)
			if dryRun && format != "" {
				flat := make([]mcptools.FlatTextEdit, len(edits))
				for i, e := range edits {
					flat[i] = mcptools.FlatTextEdit{
						StartLine: e.Range.Start.Line,
						StartChar: e.Range.Start.Character,
						EndLine:   e.Range.End.Line,
						EndChar:   e.Range.End.Character,
						NewText:   e.NewText,
					}
				}
				it := iterator.FromSlice(flat)
				return printIterator(
					cmd.Context(), format, it,
					[]string{
						"StartLine", "StartChar",
						"EndLine", "EndChar", "NewText",
					},
				)
			}
			return a.handleEdits(
				cmd.Context(), fe, dryRun, noColor,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVar(
		&dryRun, "dry-run", false,
		"Preview changes without applying",
	)
	cmd.Flags().BoolVar(
		&noColor, "no-color", false,
		"Disable colored diff output",
	)
	cmd.Flags().Uint32Var(
		&tabSize, "tab-size", 4, "Tab size",
	)
	cmd.Flags().BoolVar(
		&insertSpaces, "insert-spaces", true,
		"Use spaces",
	)

	return cmd
}

type flatLinkedEditingRange struct {
	StartLine   uint32 `json:"start_line"`
	StartChar   uint32 `json:"start_char"`
	EndLine     uint32 `json:"end_line"`
	EndChar     uint32 `json:"end_char"`
	WordPattern string `json:"word_pattern,omitempty"`
}

func newLSPLinkedEditingRangeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "linked-editing-range <file> <line> <col>",
		Short: "Get linked editing ranges at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			result, err := lsp.LinkedEditingRange(
				cmd.Context(), semanticapi.LinkedEditingRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			if result == nil {
				fmt.Println("No linked editing ranges")
				return nil
			}
			flat := make([]flatLinkedEditingRange, len(result.Ranges))
			for i, r := range result.Ranges {
				flat[i] = flatLinkedEditingRange{
					StartLine:   r.Start.Line,
					StartChar:   r.Start.Character,
					EndLine:     r.End.Line,
					EndChar:     r.End.Character,
					WordPattern: result.WordPattern,
				}
			}
			if format == "" {
				for _, r := range flat {
					fmt.Printf("%d:%d-%d:%d\n", r.StartLine, r.StartChar, r.EndLine, r.EndChar)
				}
				if result.WordPattern != "" {
					fmt.Printf("Word pattern: %s\n", result.WordPattern)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar", "WordPattern",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func monikerKindString(k semanticapi.MonikerKind) string {
	switch k {
	case semanticapi.MonikerKindImport:
		return "Import"
	case semanticapi.MonikerKindExport:
		return "Export"
	case semanticapi.MonikerKindLocal:
		return "Local"
	default:
		return "Unknown"
	}
}

func monikerUniquenessString(u semanticapi.MonikerUniquenessLevel) string {
	switch u {
	case semanticapi.MonikerUniquenessLevelDocument:
		return "Document"
	case semanticapi.MonikerUniquenessLevelProject:
		return "Project"
	case semanticapi.MonikerUniquenessLevelGroup:
		return "Group"
	case semanticapi.MonikerUniquenessLevelScheme:
		return "Scheme"
	case semanticapi.MonikerUniquenessLevelGlobal:
		return "Global"
	default:
		return "Unknown"
	}
}

type flatMoniker struct {
	Scheme     string `json:"scheme"`
	Identifier string `json:"identifier"`
	Unique     string `json:"unique"`
	Kind       string `json:"kind"`
}

func newLSPMonikerCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "moniker <file> <line> <col>",
		Short: "Get monikers at a position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			pos, err := parsePosition(args[1], args[2])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			monikers, err := lsp.Moniker(
				cmd.Context(), semanticapi.MonikerParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Position: pos,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatMoniker, len(monikers))
			for i, m := range monikers {
				flat[i] = flatMoniker{
					Scheme:     m.Scheme,
					Identifier: m.Identifier,
					Unique:     monikerUniquenessString(m.Unique),
					Kind:       monikerKindString(m.Kind),
				}
			}
			if format == "" {
				for _, m := range flat {
					fmt.Printf(
						"%s:%s [%s] %s\n",
						m.Scheme, m.Identifier, m.Kind, m.Unique,
					)
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"Scheme", "Identifier", "Unique", "Kind",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type flatInlineValue struct {
	StartLine    uint32 `json:"start_line"`
	StartChar    uint32 `json:"start_char"`
	EndLine      uint32 `json:"end_line"`
	EndChar      uint32 `json:"end_char"`
	Text         string `json:"text,omitempty"`
	VariableName string `json:"variable_name,omitempty"`
	Expression   string `json:"expression,omitempty"`
}

func newLSPInlineValueCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "inline-value <file> <start-line> <start-col> <end-line> <end-col>",
		Short: "Get inline values for a range",
		Args:  cobra.ExactArgs(5),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			r, err := parseRange(args[1], args[2], args[3], args[4])
			if err != nil {
				return err
			}
			lsp, err := a.getLSP(cmd.Context())
			if err != nil {
				return err
			}
			values, err := lsp.InlineValue(
				cmd.Context(), semanticapi.InlineValueParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri.String(),
					},
					Range: r,
				},
			)
			if err != nil {
				return err
			}
			flat := make([]flatInlineValue, len(values))
			for i, v := range values {
				flat[i] = flatInlineValue{
					StartLine:    v.Range.Start.Line,
					StartChar:    v.Range.Start.Character,
					EndLine:      v.Range.End.Line,
					EndChar:      v.Range.End.Character,
					Text:         v.Text,
					VariableName: v.VariableName,
					Expression:   v.Expression,
				}
			}
			if format == "" {
				for _, v := range flat {
					fmt.Printf(
						"%d:%d-%d:%d",
						v.StartLine, v.StartChar, v.EndLine, v.EndChar,
					)
					if v.Text != "" {
						fmt.Printf(" text=%q", v.Text)
					}
					if v.VariableName != "" {
						fmt.Printf(" var=%s", v.VariableName)
					}
					if v.Expression != "" {
						fmt.Printf(" expr=%s", v.Expression)
					}
					fmt.Println()
				}
				return nil
			}
			it := iterator.FromSlice(flat)
			return printIterator(cmd.Context(), format, it, []string{
				"StartLine", "StartChar", "EndLine", "EndChar",
				"Text", "VariableName", "Expression",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}
