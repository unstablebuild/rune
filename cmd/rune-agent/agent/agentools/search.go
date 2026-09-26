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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/workspace/walkdir"
)

const maxSearchResults = 200

type searchTool struct {
	fs      workspaceapi.FileSystem
	cwd     workspaceapi.URI
	tracker *FileTracker
	filter  walkdir.Filter
	lsp     semanticapi.LSP
	parser  syntaxapi.Parser
}

type searchArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}

func newSearch(
	wfs workspaceapi.FileSystem,
	cwd workspaceapi.URI,
	tracker *FileTracker,
	filter walkdir.Filter,
	lsp semanticapi.LSP,
	parser syntaxapi.Parser,
) agent.Tool {
	return &searchTool{
		fs:      wfs,
		cwd:     cwd,
		tracker: tracker,
		filter:  filter,
		lsp:     lsp,
		parser:  parser,
	}
}

func (t *searchTool) NeedsDeterministicOrder() bool { return false }

func (t *searchTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "search_content",
			Description: `Search for a regex pattern in file contents across the workspace.
Recursively walks the directory tree starting from path (defaults to
workspace root), skipping .git, node_modules, and vendor directories as
well as gitignored and editor swap files. Binary files are automatically
excluded.

Returns matching lines formatted as "filepath:line:content", one per
line, capped at 200 matches. The filepath is relative to the workspace
root.

Best for searching string literals, error messages, comments, TODOs,
configuration values, or non-code text. When searching for a function
or type by name, prefer search_symbols or outline_file which are
semantic and more precise. When finding where a symbol is defined, use
find_definition. Use include to restrict to specific file
types (e.g. "*.go"). The pattern uses Go regex (RE2) syntax.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex pattern to search for.",
					},
					"path": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Optional directory to search in (relative to workspace root or absolute). Defaults to workspace root.",
					},
					"include": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Optional glob pattern to filter files (e.g. '*.go', '*.ts').",
					},
				},
				"required":             []string{"pattern", "path", "include"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *searchTool) Summary(arguments string) string {
	var args searchArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	s := `"` + args.Pattern + `"`
	if args.Path != "" {
		s += " in " + summaryPath(t.cwd, args.Path)
	}
	return s
}

// skipDirs contains directory names that should be excluded from search.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

func (t *searchTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args searchArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid regex: %v", err), IsError: true}
	}

	if dotted, ok := resolveDottedReferences(ctx, t.parser, t.lsp, args.Pattern); ok {
		blocks := make([]string, 0, len(dotted.locations))
		var discovered []string
		fileCache := make(map[string][]string)
		for i := range dotted.locations {
			refs := dotted.references[i]
			blocks = append(blocks, formatReferences(t.fs, t.cwd, refs, fileCache))
			limit := min(len(refs), maxSearchResults)
			discovered = append(discovered, pathsFromLocations(refs[:limit])...)
		}
		if t.tracker != nil {
			t.tracker.TrackDiscovery(ctx, dedupPaths(discovered))
		}
		rendered := renderMultiLocation(t.cwd, dotted.locations, blocks, dotted.truncated, len(dotted.locations))
		note := fmt.Sprintf(symbolRedirectNote, args.Pattern)
		return agent.ToolResult{Content: note + "\n\n" + rendered}
	}

	root := t.cwd.Path()
	if args.Path != "" {
		root = resolvePath(t.cwd, args.Path)
	}
	walkCtx := walkdir.WithContextFilter(boundedWalkdirContext(ctx), t.filter)
	wsRoot := t.cwd.Path()

	paths, err := listToolFiles(walkCtx, t.fs, t.cwd, root, t.filter)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: listing files: %v", err), IsError: true}
	}

	filtered := iterator.Filter(paths, func(path string) bool {
		for _, part := range strings.Split(filepath.Dir(path), string(filepath.Separator)) {
			if skipDirs[part] {
				return false
			}
		}
		if args.Include != "" {
			matched, _ := filepath.Match(args.Include, filepath.Base(path))
			if !matched {
				return false
			}
		}
		return !pathIsBinary(t.fs, filepath.Join(wsRoot, path))
	})

	lines, err := walkdir.ReadLines(walkCtx, t.fs, filtered)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: reading files: %v", err), IsError: true}
	}
	defer func() { _ = lines.Close() }()

	var results []string
	truncated := false
	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		content := contentAfterLineNum(line)
		if re.MatchString(content) {
			results = append(results, line)
			if len(results) >= maxSearchResults {
				truncated = true
				break
			}
		}
	}

	var iterErr error
	if !truncated {
		iterErr = lines.Err()
	}
	warning, fatal := walkIterErr(ctx, iterErr, len(results))
	if fatal {
		return agent.ToolResult{Content: warning, IsError: true}
	}

	if len(results) == 0 {
		return agent.ToolResult{Content: "no matches found"}
	}

	// Collect unique absolute paths for discovery tracking.
	seen := make(map[string]struct{})
	var discoveredPaths []string
	for _, line := range results {
		if relPath, _, ok := strings.Cut(line, ":"); ok {
			absPath := filepath.Join(wsRoot, relPath)
			if _, dup := seen[absPath]; !dup {
				seen[absPath] = struct{}{}
				discoveredPaths = append(discoveredPaths, absPath)
			}
		}
	}
	t.tracker.TrackDiscovery(ctx, discoveredPaths)

	output := strings.Join(results, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n(results truncated at %d matches)", maxSearchResults)
	}
	if warning != "" {
		output += "\n\n" + warning
	}
	return agent.ToolResult{Content: output}
}

// contentAfterLineNum extracts the content portion from a "path:linenum:content" line.
func contentAfterLineNum(line string) string {
	// Skip past first colon (end of path).
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line
	}
	// Skip past second colon (end of line number).
	j := strings.IndexByte(line[i+1:], ':')
	if j < 0 {
		return line
	}
	return line[i+1+j+1:]
}
