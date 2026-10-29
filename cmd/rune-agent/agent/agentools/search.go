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
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/workspace/walkdir"
)

const maxSearchResults = 200

type searchTool struct {
	fs       workspaceapi.FileSystem
	cwd      workspaceapi.URI
	tracker  *FileTracker
	filter   walkdir.Filter
	searcher searchapi.Searcher
}

type searchArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}

func newSearch(
	wfs workspaceapi.FileSystem, cwd workspaceapi.URI, tracker *FileTracker,
	filter walkdir.Filter, searcher searchapi.Searcher,
) agent.Tool {
	if searcher == nil {
		panic("agentools.newSearch: searcher must not be nil; pass NoIndex() when there is no index")
	}
	return &searchTool{fs: wfs, cwd: cwd, tracker: tracker, filter: filter, searcher: searcher}
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
excluded. Hidden folders are searched only when path points into one.

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

// inSkipDir reports whether the file at rel lies in one of skipDirs.
func inSkipDir(rel string) bool {
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

// NoIndex returns a Searcher for a workspace without an index. The
// search tools then walk the files themselves.
func NoIndex() searchapi.Searcher { return noIndex{} }

type noIndex struct{}

func (noIndex) RegexpSearch(context.Context, searchapi.RegexpRequest) (sdkiterator.Iterator[searchapi.RegexpResult], error) {
	return nil, searchapi.ErrUnsupported
}

func (noIndex) SemanticSearch(context.Context, searchapi.SemanticRequest) (sdkiterator.Iterator[searchapi.SemanticResult], error) {
	return nil, searchapi.ErrUnsupported
}

// within reports whether path is dir or lies below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// indexedSearch runs req against the workspace index and passes each
// line it reports to yield, with its path relative to cwd, until yield
// returns false. Lines inside skipDirs are dropped. answered is false
// when the index reported ErrUnsupported before any line, so the
// caller has to walk the files itself.
func indexedSearch(
	ctx context.Context, s searchapi.Searcher, fs workspaceapi.FileSystem, cwd workspaceapi.URI,
	req searchapi.RegexpRequest, yield func(rel string, r searchapi.RegexpResult) bool,
) (answered bool, err error) {
	it, err := s.RegexpSearch(ctx, req)
	if err != nil {
		return !errors.Is(err, searchapi.ErrUnsupported), err
	}
	defer func() { _ = it.Close() }()
	// Results are relative to the workspace root, which differs from
	// cwd when a headless run starts in a subfolder.
	wsRoot, err := fs.URI(".")
	if err != nil {
		return true, fmt.Errorf("workspace root: %w", err)
	}
	received := false
	for {
		r, ok := it.Next(ctx)
		if !ok {
			break
		}
		received = true
		rel, err := filepath.Rel(cwd.Path(), filepath.Join(wsRoot.Path(), filepath.FromSlash(r.Path)))
		if err != nil || inSkipDir(rel) {
			continue
		}
		if !yield(rel, r) {
			return true, nil
		}
	}
	err = it.Err()
	return received || !errors.Is(err, searchapi.ErrUnsupported), err
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

	root := t.cwd.Path()
	if args.Path != "" {
		root = resolvePath(t.cwd, args.Path)
	}
	wsRoot := t.cwd.Path()

	var (
		results   []string
		truncated bool
		answered  bool
	)
	if within(wsRoot, root) {
		results, truncated, answered, err = t.searchIndex(ctx, root, args)
	}
	if !answered {
		results, truncated, err = t.walk(ctx, re, root, args)
	}
	warning, fatal := walkIterErr(ctx, err, len(results))
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

// searchIndex reports answered false when the index cannot serve the
// search, so Execute walks the files instead.
func (t *searchTool) searchIndex(ctx context.Context, root string, args searchArgs) (results []string, truncated, answered bool, err error) {
	req := searchapi.RegexpRequest{Pattern: args.Pattern, Roots: []string{root}}
	if args.Include != "" {
		req.Include = []string{args.Include}
	}
	answered, err = indexedSearch(ctx, t.searcher, t.fs, t.cwd, req, func(rel string, r searchapi.RegexpResult) bool {
		results = append(results, fmt.Sprintf("%s:%d:%s", rel, r.Line, r.Text))
		truncated = len(results) >= maxSearchResults
		return !truncated
	})
	return results, truncated, answered, err
}

func (t *searchTool) walk(ctx context.Context, re *regexp.Regexp, root string, args searchArgs) (results []string, truncated bool, err error) {
	walkCtx := walkdir.WithContextFilter(boundedWalkdirContext(ctx), t.filter)
	wsRoot := t.cwd.Path()

	paths, err := listToolFiles(walkCtx, t.fs, t.cwd, root, t.filter)
	if err != nil {
		return nil, false, fmt.Errorf("listing files: %w", err)
	}

	filtered := iterator.Filter(paths, func(path string) bool {
		if inSkipDir(path) {
			return false
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
		return nil, false, fmt.Errorf("reading files: %w", err)
	}
	defer func() { _ = lines.Close() }()

	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		if re.MatchString(contentAfterLineNum(line)) {
			results = append(results, line)
			if len(results) >= maxSearchResults {
				return results, true, nil
			}
		}
	}
	return results, false, lines.Err()
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
