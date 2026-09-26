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

package agentools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// maxSymbolMatches bounds fan-out of follow-up LSP requests when a
// fuzzy WorkspaceSymbol fallback returns many candidates.
const maxSymbolMatches = 8

// symbolNameFormsDoc documents the dot-qualified name forms accepted
// by the symbol-taking tools, per language family. Kept in one place
// so every tool (and the grep-guard message) teaches the model the
// same lookup grammar.
const symbolNameFormsDoc = `Qualify the name with its container path, dot-separated. Any trailing
part of the path works:
  go:      <package>.<Symbol> (e.g. "mypackage.MyFunc")
  python:  <module>.<symbol> or <package>.<module>.<symbol>
           (e.g. "_impl.Widget", "mypkg._impl.Widget"; names
           re-exported by a package also resolve as <package>.<name>)
  rust:    <module>.<Symbol> (e.g. "geometry.Shape")
  methods: <Type>.<method> or <container-path>.<Type>.<method>
           (e.g. "MyType.Method", "orders.Order.total")
Pass as much of the path as you know — the full path is never
required. A bare name with no dots falls back to a fuzzy workspace
search, which can be slower and ambiguous.`

// unqualifiedNameHint returns a one-line hint prompting the model to
// pass a dot-qualified name when it supplied a bare name. A name
// without a dot cannot resolve through the symbol index and always
// falls back to a fuzzy WorkspaceSymbol search, which is slower and
// can return unrelated matches. Returns "" for already-qualified
// names.
func unqualifiedNameHint(symbol string) string {
	if strings.Contains(symbol, ".") {
		return ""
	}
	return fmt.Sprintf("Hint: %q has no dot, so it resolved via a fuzzy "+
		"workspace search that may return unrelated matches. Prefer a "+
		"dot-qualified name (e.g. \"package.%s\") for a precise lookup.",
		symbol, symbol)
}

// withHint prepends a hint block to tool result content, leaving the
// content unchanged when the hint is empty.
func withHint(hint, content string) string {
	if hint == "" {
		return content
	}
	return hint + "\n\n" + content
}

// LSPTools returns all LSP-backed agent tools. The tracker should be
// the same instance returned by DefaultTools so that stale reads are
// shared across all file-aware tools.
func LSPTools(
	lsp semanticapi.LSP,
	fs workspaceapi.FileSystem,
	parser syntaxapi.Parser,
	cwd workspaceapi.URI,
	tracker *FileTracker,
) []agent.Tool {
	return []agent.Tool{
		&findDefinitionTool{lsp: lsp, fs: fs, parser: parser, cwd: cwd, tracker: tracker},
		&findImplementationsTool{lsp: lsp, fs: fs, parser: parser, cwd: cwd, tracker: tracker},
		&findReferencesTool{lsp: lsp, fs: fs, parser: parser, cwd: cwd, tracker: tracker},
		&outlineFileTool{lsp: lsp, fs: fs, cwd: cwd, tracker: tracker},
		&searchSymbolsTool{lsp: lsp, cwd: cwd, tracker: tracker},
		&describeSymbolTool{lsp: lsp, parser: parser, cwd: cwd},
		&checkFileErrorsTool{lsp: lsp, fs: fs, cwd: cwd, tracker: tracker},
		&formatFileTool{lsp: lsp, fs: fs, cwd: cwd, tracker: tracker},
	}
}

const symbolRedirectNote = "Note: %q resolved to a known symbol; showing find_references results\n(call find_references directly for symbol lookups)."

type dottedReferences struct {
	locations  []semanticapi.Location
	references [][]semanticapi.Location
	truncated  bool
	totalCount int
}

func resolveIndexedSymbol(
	ctx context.Context, parser syntaxapi.Parser, symbol string,
) ([]semanticapi.Location, bool, error) {
	if parser == nil {
		return nil, false, nil
	}
	it, err := parser.ResolveSymbol(ctx, symbol, nil)
	if err != nil {
		return nil, false, err
	}
	matches, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return nil, false, err
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	locs := make([]semanticapi.Location, 0, len(matches))
	for _, m := range matches {
		pos := semanticapi.Position{
			Line:      uint32(m.Pos.Y),
			Character: uint32(m.Pos.X),
		}
		locs = append(locs, semanticapi.Location{
			URI:   m.URI,
			Range: semanticapi.Range{Start: pos, End: pos},
		})
	}
	return capLocations(locs)
}

func resolveSymbol(
	ctx context.Context, lsp semanticapi.LSP, parser syntaxapi.Parser, symbol string,
) ([]semanticapi.Location, bool, error) {
	if locs, truncated, err := resolveIndexedSymbol(ctx, parser, symbol); err == nil && len(locs) > 0 {
		return locs, truncated, nil
	}
	syms, err := lsp.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
		Query: symbol,
	})
	if err != nil {
		return nil, false, fmt.Errorf("workspace symbol lookup: %w", err)
	}
	if len(syms) == 0 {
		if hint := unqualifiedNameHint(symbol); hint != "" {
			return nil, false, fmt.Errorf("symbol %q not found. %s", symbol, hint)
		}
		return nil, false, fmt.Errorf("symbol %q not found", symbol)
	}
	locs := make([]semanticapi.Location, 0, len(syms))
	for _, s := range syms {
		locs = append(locs, s.Location)
	}
	return capLocations(locs)
}

func resolveDottedReferences(
	ctx context.Context, parser syntaxapi.Parser, lsp semanticapi.LSP, pattern string,
) (*dottedReferences, bool) {
	if !strings.Contains(pattern, ".") || parser == nil || lsp == nil {
		return nil, false
	}
	locs, truncated, err := resolveIndexedSymbol(ctx, parser, pattern)
	if err != nil || len(locs) == 0 {
		return nil, false
	}
	allRefs := make([][]semanticapi.Location, 0, len(locs))
	total := 0
	for _, loc := range locs {
		refs, err := lsp.References(ctx, semanticapi.ReferenceParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
			Position:     loc.Range.Start,
			Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
		})
		if err != nil {
			return nil, false
		}
		allRefs = append(allRefs, refs)
		total += len(refs)
	}
	if total == 0 {
		return nil, false
	}
	return &dottedReferences{
		locations:  locs,
		references: allRefs,
		truncated:  truncated,
		totalCount: total,
	}, true
}

func capLocations(locs []semanticapi.Location) ([]semanticapi.Location, bool, error) {
	if len(locs) > maxSymbolMatches {
		return locs[:maxSymbolMatches], true, nil
	}
	return locs, false, nil
}

func renderMultiLocation(
	cwd workspaceapi.URI, locs []semanticapi.Location, blocks []string,
	truncated bool, totalBefore int,
) string {
	if len(blocks) == 1 {
		out := blocks[0]
		if truncated {
			out += fmt.Sprintf("\n\n(matches truncated to %d of %d)", len(blocks), totalBefore)
		}
		return out
	}
	var sb strings.Builder
	for i, b := range blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("# ")
		sb.WriteString(formatLocation(locs[i], cwd))
		sb.WriteByte('\n')
		sb.WriteString(b)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n\n(matches truncated to %d of %d)", len(blocks), totalBefore)
	}
	return sb.String()
}

// fileURI resolves a file path to a workspace URI.
func fileURI(
	fs workspaceapi.FileSystem, cwd workspaceapi.URI, filePath string,
) (workspaceapi.URI, error) {
	path := resolvePath(cwd, filePath)
	return fs.URI(path)
}

// uriToRelPath extracts a workspace-relative path from a file URI string.
func uriToRelPath(uri string, cwd workspaceapi.URI) string {
	parsed, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	return workspaceapi.RelPath(cwd, parsed)
}

// formatLocation formats a Location as "relative/path.go:line" (1-based).
func formatLocation(loc semanticapi.Location, cwd workspaceapi.URI) string {
	rel := uriToRelPath(loc.URI, cwd)
	line := loc.Range.Start.Line + 1
	return fmt.Sprintf("%s:%d", rel, line)
}

// formatLocations formats a LocationResult into a human-readable string.
func formatLocations(result semanticapi.LocationResult, cwd workspaceapi.URI) string {
	var locations []semanticapi.Location
	switch {
	case result.Location != nil:
		locations = []semanticapi.Location{*result.Location}
	case len(result.Locations) > 0:
		locations = result.Locations
	case len(result.LocationLinks) > 0:
		for _, link := range result.LocationLinks {
			locations = append(locations, semanticapi.Location{
				URI:   link.TargetURI,
				Range: link.TargetRange,
			})
		}
	}
	if len(locations) == 0 {
		return "no results found"
	}
	var sb strings.Builder
	for i, loc := range locations {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(formatLocation(loc, cwd))
	}
	return sb.String()
}

// pathsFromLocationResult extracts unique absolute file paths from a LocationResult.
func pathsFromLocationResult(result semanticapi.LocationResult) []string {
	var uris []string
	switch {
	case result.Location != nil:
		uris = []string{result.Location.URI}
	case len(result.Locations) > 0:
		for _, loc := range result.Locations {
			uris = append(uris, loc.URI)
		}
	case len(result.LocationLinks) > 0:
		for _, link := range result.LocationLinks {
			uris = append(uris, link.TargetURI)
		}
	}
	seen := make(map[string]struct{})
	var paths []string
	for _, uri := range uris {
		parsed, err := workspaceapi.ParseURI(uri)
		if err != nil {
			continue
		}
		p := parsed.Path()
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}
	return paths
}

// symbolKindName returns a human-readable name for a SymbolKind.
func symbolKindName(k semanticapi.SymbolKind) string {
	switch k {
	case semanticapi.SymbolKindFile:
		return "file"
	case semanticapi.SymbolKindModule:
		return "module"
	case semanticapi.SymbolKindNamespace:
		return "namespace"
	case semanticapi.SymbolKindPackage:
		return "package"
	case semanticapi.SymbolKindClass:
		return "class"
	case semanticapi.SymbolKindMethod:
		return "method"
	case semanticapi.SymbolKindProperty:
		return "property"
	case semanticapi.SymbolKindField:
		return "field"
	case semanticapi.SymbolKindConstructor:
		return "constructor"
	case semanticapi.SymbolKindEnum:
		return "enum"
	case semanticapi.SymbolKindInterface:
		return "interface"
	case semanticapi.SymbolKindFunction:
		return "function"
	case semanticapi.SymbolKindVariable:
		return "variable"
	case semanticapi.SymbolKindConstant:
		return "constant"
	case semanticapi.SymbolKindString:
		return "string"
	case semanticapi.SymbolKindNumber:
		return "number"
	case semanticapi.SymbolKindBoolean:
		return "boolean"
	case semanticapi.SymbolKindArray:
		return "array"
	case semanticapi.SymbolKindObject:
		return "object"
	case semanticapi.SymbolKindKey:
		return "key"
	case semanticapi.SymbolKindNull:
		return "null"
	case semanticapi.SymbolKindEnumMember:
		return "enum_member"
	case semanticapi.SymbolKindStruct:
		return "struct"
	case semanticapi.SymbolKindEvent:
		return "event"
	case semanticapi.SymbolKindOperator:
		return "operator"
	case semanticapi.SymbolKindTypeParameter:
		return "type_parameter"
	default:
		return fmt.Sprintf("kind(%d)", k)
	}
}

// severityName returns a human-readable name for a DiagnosticSeverity.
func severityName(s semanticapi.DiagnosticSeverity) string {
	switch s {
	case semanticapi.DiagnosticSeverityError:
		return "error"
	case semanticapi.DiagnosticSeverityWarning:
		return "warning"
	case semanticapi.DiagnosticSeverityInformation:
		return "info"
	case semanticapi.DiagnosticSeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// --- find_definition ---

type findDefinitionTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	parser  syntaxapi.Parser
	cwd     workspaceapi.URI
	tracker *FileTracker
}

type symbolArgs struct {
	Symbol string `json:"symbol"`
}

func (t *findDefinitionTool) NeedsDeterministicOrder() bool { return false }

func (t *findDefinitionTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "find_definition",
			Description: `Find where a symbol is defined. Returns the file path and line number
of the definition (e.g. "src/main.go:42").

The symbol is matched by name across the workspace. Use when you need
to navigate to the source of a function, type, or variable. Prefer over
search_content for navigating to definitions.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type": "string",
						"description": "The symbol name to find the definition of.\n" +
							symbolNameFormsDoc,
					},
				},
				"required":             []string{"symbol"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *findDefinitionTool) Summary(arguments string) string {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Symbol
}

func (t *findDefinitionTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	locs, truncated, err := resolveSymbol(ctx, t.lsp, t.parser, args.Symbol)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	totalBefore := len(locs)
	if truncated {
		totalBefore = -1
	}
	blocks := make([]string, 0, len(locs))
	var discovered []string
	for _, loc := range locs {
		result, err := t.lsp.Definition(ctx, semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
			Position:     loc.Range.Start,
		})
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("error: definition: %v", err), IsError: true}
		}
		discovered = append(discovered, pathsFromLocationResult(result)...)
		blocks = append(blocks, formatLocations(result, t.cwd))
	}
	t.tracker.TrackDiscovery(ctx, dedupPaths(discovered))
	return agent.ToolResult{
		Content: withHint(unqualifiedNameHint(args.Symbol),
			renderMultiLocation(t.cwd, locs, blocks, truncated, max(totalBefore, len(locs)))),
	}
}

// --- find_implementations ---

type findImplementationsTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	parser  syntaxapi.Parser
	cwd     workspaceapi.URI
	tracker *FileTracker
}

func (t *findImplementationsTool) NeedsDeterministicOrder() bool { return false }

func (t *findImplementationsTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "find_implementations",
			Description: `Find implementation relationships for a symbol. Returns file paths and
line numbers (e.g. "handler.go:15"). Works in both directions: given an
interface, it lists the concrete types that satisfy it; given a concrete
type, it lists the interfaces that the type satisfies.

Prefer over search_content for finding implementors or satisfied
interfaces.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type": "string",
						"description": "The interface name (to find implementing types) or the\n" +
							"concrete type name (to find satisfied interfaces).\n" +
							symbolNameFormsDoc,
					},
				},
				"required":             []string{"symbol"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *findImplementationsTool) Summary(arguments string) string {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Symbol
}

func (t *findImplementationsTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	locs, truncated, err := resolveSymbol(ctx, t.lsp, t.parser, args.Symbol)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	blocks := make([]string, 0, len(locs))
	var discovered []string
	for _, loc := range locs {
		result, err := t.lsp.Implementation(ctx, semanticapi.ImplementationParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
			Position:     loc.Range.Start,
		})
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("error: implementation: %v", err), IsError: true}
		}
		discovered = append(discovered, pathsFromLocationResult(result)...)
		blocks = append(blocks, formatLocations(result, t.cwd))
	}
	t.tracker.TrackDiscovery(ctx, dedupPaths(discovered))
	return agent.ToolResult{
		Content: withHint(unqualifiedNameHint(args.Symbol),
			renderMultiLocation(t.cwd, locs, blocks, truncated, len(locs))),
	}
}

// --- find_references ---

type findReferencesTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	parser  syntaxapi.Parser
	cwd     workspaceapi.URI
	tracker *FileTracker
}

func (t *findReferencesTool) NeedsDeterministicOrder() bool { return false }

func (t *findReferencesTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "find_references",
			Description: `Find all references to a symbol across the workspace. Returns file
paths, line numbers, and source text for each reference, formatted as
"path:line:content" (e.g. "handler.go:15:	srv.Handle(ctx)").

Use when you need to understand where a function, type, or variable is
used. Includes the declaration by default. Results are capped at 200
matches. Prefer over search_content for finding usages of a symbol.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type": "string",
						"description": "The symbol name to find references of.\n" +
							symbolNameFormsDoc,
					},
				},
				"required":             []string{"symbol"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *findReferencesTool) Summary(arguments string) string {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Symbol
}

func (t *findReferencesTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	locs, symTruncated, err := resolveSymbol(ctx, t.lsp, t.parser, args.Symbol)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	blocks := make([]string, 0, len(locs))
	var discovered []string
	fileCache := make(map[string][]string) // URI -> lines, shared across locations.
	for _, loc := range locs {
		locations, err := t.lsp.References(ctx, semanticapi.ReferenceParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
			Position:     loc.Range.Start,
			Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
		})
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("error: references: %v", err), IsError: true}
		}
		blocks = append(blocks, formatReferences(t.fs, t.cwd, locations, fileCache))
		limit := min(len(locations), maxSearchResults)
		discovered = append(discovered, pathsFromLocations(locations[:limit])...)
	}
	t.tracker.TrackDiscovery(ctx, dedupPaths(discovered))
	return agent.ToolResult{
		Content: withHint(unqualifiedNameHint(args.Symbol),
			renderMultiLocation(t.cwd, locs, blocks, symTruncated, len(locs))),
	}
}

// formatReferences renders one find_references block (matching the
// pre-RUNE-190 output shape) for a single resolved location's
// per-reference list. fileCache is shared across calls so reading the
// same source file twice is avoided.
func formatReferences(
	fs workspaceapi.FileSystem, cwd workspaceapi.URI,
	locations []semanticapi.Location, fileCache map[string][]string,
) string {
	if len(locations) == 0 {
		return "no references found"
	}
	limit := len(locations)
	truncated := false
	if limit > maxSearchResults {
		limit = maxSearchResults
		truncated = true
	}
	var sb strings.Builder
	for i := 0; i < limit; i++ {
		l := locations[i]
		if i > 0 {
			sb.WriteByte('\n')
		}
		rel := uriToRelPath(l.URI, cwd)
		lineNum := l.Range.Start.Line + 1
		lines, ok := fileCache[l.URI]
		if !ok {
			parsed, parseErr := workspaceapi.ParseURI(l.URI)
			if parseErr == nil {
				data, readErr := readFile(fs, parsed.Path())
				if readErr == nil {
					lines = strings.Split(string(data), "\n")
				}
			}
			fileCache[l.URI] = lines
		}
		content := ""
		if int(l.Range.Start.Line) < len(lines) {
			content = lines[l.Range.Start.Line]
		}
		fmt.Fprintf(&sb, "%s:%d:%s", rel, lineNum, content)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n\n(results truncated at %d matches)", maxSearchResults)
	}
	return sb.String()
}

// SymbolContext is semantic context resolved from one exact document position.
type SymbolContext struct {
	Definition    string
	References    string
	Documentation string
}

// SymbolContextAtPosition resolves a symbol from one exact document position.
func SymbolContextAtPosition(
	ctx context.Context, lsp semanticapi.LSP, fs workspaceapi.FileSystem,
	cwd, uri workspaceapi.URI, pos semanticapi.Position,
) (SymbolContext, error) {
	doc := semanticapi.TextDocumentIdentifier{URI: "file://" + uri.Path()}
	definition, err := lsp.Definition(ctx, semanticapi.DefinitionParams{
		TextDocument: doc,
		Position:     pos,
	})
	if err != nil {
		return SymbolContext{}, fmt.Errorf("definition: %w", err)
	}
	references, err := lsp.References(ctx, semanticapi.ReferenceParams{
		TextDocument: doc,
		Position:     pos,
		Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		return SymbolContext{}, fmt.Errorf("references: %w", err)
	}
	hover, err := lsp.Hover(ctx, semanticapi.HoverParams{
		TextDocument: doc,
		Position:     pos,
	})
	if err != nil {
		return SymbolContext{}, fmt.Errorf("hover: %w", err)
	}
	return SymbolContext{
		Definition:    formatLocations(definition, cwd),
		References:    formatReferences(fs, cwd, references, make(map[string][]string)),
		Documentation: hoverContent(hover),
	}, nil
}

// dedupPaths returns paths with duplicates removed, preserving order.
func dedupPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := paths[:0]
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// pathsFromLocations extracts unique absolute file paths from a Location slice.
func pathsFromLocations(locations []semanticapi.Location) []string {
	seen := make(map[string]struct{})
	var paths []string
	for _, l := range locations {
		parsed, err := workspaceapi.ParseURI(l.URI)
		if err != nil {
			continue
		}
		p := parsed.Path()
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}
	return paths
}

// --- outline_file ---

type outlineFileTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	cwd     workspaceapi.URI
	tracker *FileTracker
}

type filePathArgs struct {
	Path     string `json:"path"`
	FilePath string `json:"file_path"` // fallback: accept "file_path" for backward compatibility
}

// filePath returns the effective file path, preferring path over file_path.
// Returns an error if both are empty.
func (a filePathArgs) filePath() (string, error) {
	if a.Path != "" {
		return a.Path, nil
	}
	if a.FilePath != "" {
		return a.FilePath, nil
	}
	return "", fmt.Errorf("missing required parameter: path")
}

func (t *outlineFileTool) NeedsDeterministicOrder() bool { return false }

func (t *outlineFileTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "outline_file",
			Description: `Get a flat outline of a file showing all symbols (functions,
types, methods, variables) with their kinds, line numbers, and names.

Output is formatted as "path:line:kind:name" — for example:
  extension.go:10:struct:MyType
  extension.go:12:method:MyType.Handle
  extension.go:25:method:MyType.Close

Use to understand a file's structure without reading it. Prefer over
read_file when you only need to know what a file contains.

The path can be relative to the workspace root or absolute. Always read
a file before modifying it so you understand its current contents.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The file path to outline (relative to workspace root or absolute).",
					},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *outlineFileTool) Summary(arguments string) string {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	fp, _ := args.filePath()
	return summaryPath(t.cwd, fp)
}

func (t *outlineFileTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	fp, err := args.filePath()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	uri, err := fileURI(t.fs, t.cwd, fp)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: resolve path: %v", err), IsError: true}
	}
	result, err := t.lsp.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: uri.String()},
	})
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: document symbol: %v", err), IsError: true}
	}
	var sb strings.Builder
	if len(result.DocumentSymbols) > 0 {
		rel := workspaceapi.RelPath(t.cwd, uri)
		formatDocumentSymbols(&sb, result.DocumentSymbols, rel, "")
	} else if len(result.SymbolInformation) > 0 {
		for _, sym := range result.SymbolInformation {
			rel := uriToRelPath(sym.Location.URI, t.cwd)
			line := sym.Location.Range.Start.Line + 1
			fmt.Fprintf(&sb, "%s:%d:%s:%s\n", rel, line, symbolKindName(sym.Kind), sym.Name)
		}
	} else {
		return agent.ToolResult{Content: "no symbols found"}
	}
	absPath := resolvePath(t.cwd, fp)
	staleIDs := t.tracker.TrackRead(ctx, "outline_file", absPath, "")
	staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(absPath)...)
	return agent.ToolResult{
		Content:           sb.String(),
		DropToolResultIDs: staleIDs,
	}
}

func formatDocumentSymbols(sb *strings.Builder, symbols []semanticapi.DocumentSymbol, rel string, parent string) {
	for _, sym := range symbols {
		line := sym.Range.Start.Line + 1
		name := sym.Name
		if parent != "" {
			name = parent + "." + name
		}
		fmt.Fprintf(sb, "%s:%d:%s:%s\n", rel, line, symbolKindName(sym.Kind), name)
		if len(sym.Children) > 0 {
			formatDocumentSymbols(sb, sym.Children, rel, sym.Name)
		}
	}
}

// --- search_symbols ---

type searchSymbolsTool struct {
	lsp     semanticapi.LSP
	cwd     workspaceapi.URI
	tracker *FileTracker
}

type searchSymbolsArgs struct {
	Query string `json:"query"`
}

func (t *searchSymbolsTool) NeedsDeterministicOrder() bool { return false }

func (t *searchSymbolsTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "search_symbols",
			Description: `Search for symbols by name across the entire workspace. Returns matching
symbol names with their kind and location (e.g. "handler.go:42:function:HandleRequest"),
capped at 200 results.

Use to locate a function or type when you know its name but not its
file. Matches against the symbol's own name in any language, so query
a bare name ("Widget", "HandleRequest"), not a dotted path. Supports
partial name matching. Once the symbol is known, navigate with
find_definition / find_references using the qualified dotted form.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type": "string",
						"description": "Symbol name or partial name to search for " +
							"(bare, without package/module qualifiers).",
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *searchSymbolsTool) Summary(arguments string) string {
	var args searchSymbolsArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Query
}

func (t *searchSymbolsTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args searchSymbolsArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	syms, err := t.lsp.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
		Query: args.Query,
	})
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: workspace symbol: %v", err), IsError: true}
	}
	if len(syms) == 0 {
		return agent.ToolResult{Content: "no symbols found"}
	}
	var sb strings.Builder
	limit := len(syms)
	truncated := false
	if limit > maxSearchResults {
		limit = maxSearchResults
		truncated = true
	}
	for i := 0; i < limit; i++ {
		sym := syms[i]
		rel := uriToRelPath(sym.Location.URI, t.cwd)
		line := sym.Location.Range.Start.Line + 1
		fmt.Fprintf(&sb, "%s:%d:%s:%s\n", rel, line, symbolKindName(sym.Kind), sym.Name)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n(results truncated at %d matches)", maxSearchResults)
	}
	// Track discovered file paths for consumption by file-reading tools.
	seen := make(map[string]struct{})
	var discoveredPaths []string
	for i := 0; i < limit; i++ {
		parsed, err := workspaceapi.ParseURI(syms[i].Location.URI)
		if err != nil {
			continue
		}
		p := parsed.Path()
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			discoveredPaths = append(discoveredPaths, p)
		}
	}
	t.tracker.TrackDiscovery(ctx, discoveredPaths)
	return agent.ToolResult{Content: sb.String()}
}

// --- describe_symbol ---

type describeSymbolTool struct {
	lsp    semanticapi.LSP
	parser syntaxapi.Parser
	cwd    workspaceapi.URI
}

func (t *describeSymbolTool) NeedsDeterministicOrder() bool { return false }

func (t *describeSymbolTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "describe_symbol",
			Description: `Get type information and documentation for a symbol. Returns the
symbol's type signature and any doc comments from hover information.

Use to understand what a symbol is — its type, parameters, and
documentation — without reading its source file.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type": "string",
						"description": "The symbol name to describe.\n" +
							symbolNameFormsDoc,
					},
				},
				"required":             []string{"symbol"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *describeSymbolTool) Summary(arguments string) string {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Symbol
}

func (t *describeSymbolTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args symbolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	locs, truncated, err := resolveSymbol(ctx, t.lsp, t.parser, args.Symbol)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	blocks := make([]string, 0, len(locs))
	for _, loc := range locs {
		hover, err := t.lsp.Hover(ctx, semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
			Position:     loc.Range.Start,
		})
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("error: hover: %v", err), IsError: true}
		}
		blocks = append(blocks, hoverContent(hover))
	}
	return agent.ToolResult{
		Content: withHint(unqualifiedNameHint(args.Symbol),
			renderMultiLocation(t.cwd, locs, blocks, truncated, len(locs))),
	}
}

// hoverContent extracts the most useful content from a Hover result,
// matching the precedence used by the original describe_symbol tool.
func hoverContent(hover *semanticapi.Hover) string {
	if hover == nil {
		return "no information available"
	}
	switch {
	case hover.Contents.Value != "":
		return hover.Contents.Value
	case hover.ContentsMarked != nil:
		return hover.ContentsMarked.Value
	case len(hover.ContentsMarkedA) > 0:
		var sb strings.Builder
		for i, m := range hover.ContentsMarkedA {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(m.Value)
		}
		return sb.String()
	default:
		return "no information available"
	}
}

// --- check_file_errors ---

type checkFileErrorsTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	cwd     workspaceapi.URI
	tracker *FileTracker
}

func (t *checkFileErrorsTool) NeedsDeterministicOrder() bool { return false }

func (t *checkFileErrorsTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "check_file_errors",
			Description: `Get compilation errors, warnings, and linter diagnostics for a file.
Returns issues sorted by severity (errors first), each formatted as
"path:line:severity:message [source]".

Use after applying patches or making changes to verify the file still
compiles and passes lint checks. Returns "no errors or warnings" when
the file is clean.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The file path to check (relative to workspace root or absolute).",
					},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *checkFileErrorsTool) Summary(arguments string) string {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	fp, _ := args.filePath()
	return summaryPath(t.cwd, fp)
}

func (t *checkFileErrorsTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	fp, err := args.filePath()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	uri, err := fileURI(t.fs, t.cwd, fp)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: resolve path: %v", err), IsError: true}
	}
	report, err := t.lsp.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: uri.String()},
	})
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: diagnostic: %v", err), IsError: true}
	}
	if len(report.Items) == 0 {
		return agent.ToolResult{Content: "no errors or warnings"}
	}
	// Sort by severity (errors first), then by line.
	sort.Slice(report.Items, func(i, j int) bool {
		if report.Items[i].Severity != report.Items[j].Severity {
			return report.Items[i].Severity < report.Items[j].Severity
		}
		return report.Items[i].Range.Start.Line < report.Items[j].Range.Start.Line
	})
	var sb strings.Builder
	rel := workspaceapi.RelPath(t.cwd, uri)
	for _, d := range report.Items {
		line := d.Range.Start.Line + 1
		sev := severityName(d.Severity)
		fmt.Fprintf(&sb, "%s:%d:%s:%s", rel, line, sev, d.Message)
		if d.Source != "" {
			fmt.Fprintf(&sb, " [%s]", d.Source)
		}
		sb.WriteByte('\n')
	}
	absPath := resolvePath(t.cwd, fp)
	staleIDs := t.tracker.TrackRead(ctx, "check_file_errors", absPath, "")
	staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(absPath)...)
	return agent.ToolResult{
		Content:           sb.String(),
		DropToolResultIDs: staleIDs,
	}
}

// --- format_file ---

type formatFileTool struct {
	lsp     semanticapi.LSP
	fs      workspaceapi.FileSystem
	cwd     workspaceapi.URI
	tracker *FileTracker
}

func (t *formatFileTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "format_file",
			Description: `Format a file using the language server's formatter (e.g. gofmt for Go).
Reads the file, applies formatting edits, and writes the result back.

Returns the number of edits applied, or "no changes needed" if the file
is already formatted. Use instead of running formatter commands via
bash.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The file path to format (relative to workspace root or absolute).",
					},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *formatFileTool) Summary(arguments string) string {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	fp, _ := args.filePath()
	return summaryPath(t.cwd, fp)
}

func (t *formatFileTool) NeedsDeterministicOrder() bool { return true }

func (t *formatFileTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args filePathArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	fp, err := args.filePath()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}
	absPath := resolvePath(t.cwd, fp)
	uri, err := t.fs.URI(absPath)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: resolve path: %v", err), IsError: true}
	}
	edits, err := t.lsp.Formatting(ctx, semanticapi.DocumentFormattingParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: uri.String()},
		Options: semanticapi.FormattingOptions{
			TabSize:      4,
			InsertSpaces: false,
		},
	})
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: formatting: %v", err), IsError: true}
	}
	if len(edits) == 0 {
		return agent.ToolResult{Content: "no changes needed"}
	}
	// Read the file, apply edits in reverse order, write back.
	data, err := readFile(t.fs, absPath)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: read file: %v", err), IsError: true}
	}
	content := applyTextEdits(string(data), edits)
	if err := writeFile(t.fs, absPath, []byte(content), 0o644); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: write file: %v", err), IsError: true}
	}
	// format_file modifies the file — drop stale reads and consumed
	// discoveries, and forget the hash so the agent re-reads before
	// the next modification.
	staleIDs := t.tracker.StaleReads(absPath)
	staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(absPath)...)
	t.tracker.Forget(absPath)
	return agent.ToolResult{
		Content:           fmt.Sprintf("formatted %s (%d edits applied)", fp, len(edits)),
		DropToolResultIDs: staleIDs,
	}
}

// applyTextEdits applies LSP text edits to content. Edits are applied in
// reverse order by position to preserve earlier offsets.
func applyTextEdits(content string, edits []semanticapi.TextEdit) string {
	// Sort edits in reverse order (later positions first).
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Range.Start.Line != edits[j].Range.Start.Line {
			return edits[i].Range.Start.Line > edits[j].Range.Start.Line
		}
		return edits[i].Range.Start.Character > edits[j].Range.Start.Character
	})
	lines := strings.Split(content, "\n")
	for _, edit := range edits {
		startLine := int(edit.Range.Start.Line)
		startChar := int(edit.Range.Start.Character)
		endLine := int(edit.Range.End.Line)
		endChar := int(edit.Range.End.Character)

		// Clamp to valid ranges.
		if startLine >= len(lines) {
			startLine = len(lines) - 1
		}
		if startLine < 0 {
			startLine = 0
		}
		if endLine >= len(lines) {
			endLine = len(lines) - 1
		}
		if endLine < 0 {
			endLine = 0
		}

		prefix := ""
		if startChar <= len(lines[startLine]) {
			prefix = lines[startLine][:startChar]
		} else {
			prefix = lines[startLine]
		}
		suffix := ""
		if endChar <= len(lines[endLine]) {
			suffix = lines[endLine][endChar:]
		}

		replacement := prefix + edit.NewText + suffix
		newLines := strings.Split(replacement, "\n")

		// Replace the affected line range with the new lines.
		result := make([]string, 0, len(lines)-endLine+startLine+len(newLines))
		result = append(result, lines[:startLine]...)
		result = append(result, newLines...)
		result = append(result, lines[endLine+1:]...)
		lines = result
	}
	return strings.Join(lines, "\n")
}
