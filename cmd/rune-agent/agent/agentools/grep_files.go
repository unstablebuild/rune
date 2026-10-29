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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
	"unstable.build/rune/internal/workspace/walkdir"
)

const (
	defaultGrepLimit = 100
	maxGrepLimit     = 2000
)

type grepFilesTool struct {
	fs       workspaceapi.FileSystem
	cwd      workspaceapi.URI
	tracker  *FileTracker
	searcher searchapi.Searcher
}

type grepFilesArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
	Limit   *int   `json:"limit"`
}

// NewGrepFiles creates a grep_files tool that returns file paths whose
// contents match a regex pattern, sorted by modification time. It complements
// search_content, which returns matching lines rather than only file paths.
// It searches through searcher when it can, so searcher must not be nil:
// pass NoIndex() for a workspace without an index.
func NewGrepFiles(wfs workspaceapi.FileSystem, cwd workspaceapi.URI, tracker *FileTracker, searcher searchapi.Searcher) agent.Tool {
	if searcher == nil {
		panic("agentools.NewGrepFiles: searcher must not be nil; pass NoIndex() when there is no index")
	}
	return &grepFilesTool{fs: wfs, cwd: cwd, tracker: tracker, searcher: searcher}
}

func (t *grepFilesTool) NeedsDeterministicOrder() bool { return false }

func (t *grepFilesTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name:        "grep_files",
			Description: "Finds files whose contents match the pattern and lists them by modification time.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regular expression pattern to search for.",
					},
					"include": map[string]any{
						"type":        "string",
						"description": `Optional glob that limits which files are searched (e.g. "*.rs" or "*.{ts,tsx}").`,
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory or file path to search. Defaults to the session's working directory.",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum number of file paths to return (defaults to 100).",
					},
				},
				"required":             []string{"pattern"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *grepFilesTool) Summary(arguments string) string {
	var args grepFilesArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	s := `"` + args.Pattern + `"`
	if args.Path != "" {
		s += " in " + args.Path
	}
	return s
}

// grepFileMatch holds a matched file path and its modification time for sorting.
type grepFileMatch struct {
	relPath string
	absPath string
	modTime time.Time
}

func (t *grepFilesTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args grepFilesArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}

	if args.Pattern == "" {
		return agent.ToolResult{Content: "error: pattern must not be empty", IsError: true}
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid regex: %v", err), IsError: true}
	}

	limit := defaultGrepLimit
	if args.Limit != nil && *args.Limit > 0 {
		limit = *args.Limit
		if limit > maxGrepLimit {
			limit = maxGrepLimit
		}
	}

	root := t.cwd.Path()
	if args.Path != "" {
		root = resolvePath(t.cwd, args.Path)
	}

	var (
		relPaths []string
		answered bool
	)
	if within(t.cwd.Path(), root) {
		relPaths, answered, err = t.searchIndex(ctx, root, args)
	}
	if !answered {
		relPaths, err = t.walk(ctx, re, root, args)
	}
	warning, fatal := walkIterErr(ctx, err, len(relPaths))
	if fatal {
		return agent.ToolResult{Content: warning, IsError: true}
	}

	if len(relPaths) == 0 {
		return agent.ToolResult{Content: "No matches found."}
	}

	matches := make([]grepFileMatch, len(relPaths))
	for i, relPath := range relPaths {
		absPath := filepath.Join(t.cwd.Path(), relPath)
		var modTime time.Time
		if info, err := t.fs.Stat(absPath); err == nil {
			modTime = info.ModTime()
		}
		matches[i] = grepFileMatch{relPath: relPath, absPath: absPath, modTime: modTime}
	}

	// Sort by modification time, most recent first.
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	// Track discovered paths.
	discoveredPaths := make([]string, len(matches))
	results := make([]string, len(matches))
	for i, m := range matches {
		results[i] = m.relPath
		discoveredPaths[i] = m.absPath
	}
	t.tracker.TrackDiscovery(ctx, discoveredPaths)

	output := strings.Join(results, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n(results truncated at %d files)", limit)
	}
	if warning != "" {
		output += "\n\n" + warning
	}
	return agent.ToolResult{Content: output}
}

// searchIndex reports answered false when the index cannot serve the
// search, so Execute walks the files instead.
func (t *grepFilesTool) searchIndex(ctx context.Context, root string, args grepFilesArgs) (relPaths []string, answered bool, err error) {
	req := searchapi.RegexpRequest{Pattern: args.Pattern, Roots: []string{root}, MaxPerFile: 1}
	if args.Include != "" {
		req.Include = []string{args.Include}
	}
	answered, err = indexedSearch(ctx, t.searcher, t.fs, t.cwd, req, func(rel string, _ searchapi.RegexpResult) bool {
		relPaths = append(relPaths, rel)
		return true
	})
	return relPaths, answered, err
}

// walk lists the files under root that hold a line matching re.
func (t *grepFilesTool) walk(ctx context.Context, re *regexp.Regexp, root string, args grepFilesArgs) ([]string, error) {
	walkCtx := boundedWalkdirContext(ctx)
	wsRoot := t.cwd.Path()

	paths, err := listToolFiles(walkCtx, t.fs, t.cwd, root, nil)
	if err != nil {
		return nil, fmt.Errorf("listing files: %w", err)
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
		// Skip binary files (e.g. compiled objects, fonts, images
		// without recognised extensions). Reading lines from them
		// would emit invalid UTF-8 and bloats the line iterator
		// channel; this matches ripgrep's default behaviour.
		return !pathIsBinary(t.fs, filepath.Join(wsRoot, path))
	})

	lines, err := walkdir.ReadLines(walkCtx, t.fs, filtered)
	if err != nil {
		return nil, fmt.Errorf("reading files: %w", err)
	}
	defer func() { _ = lines.Close() }()

	seen := make(map[string]struct{})
	var relPaths []string
	for {
		line, ok := lines.Next(ctx)
		if !ok {
			break
		}
		relPath, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if _, dup := seen[relPath]; dup {
			continue
		}
		if re.MatchString(utf8validate.Sanitize(contentAfterLineNum(line))) {
			seen[relPath] = struct{}{}
			relPaths = append(relPaths, relPath)
		}
	}
	return relPaths, lines.Err()
}

// pathIsBinary opens path and inspects up to the first 8 KiB to decide
// whether it should be treated as binary content. Files that fail to
// open are conservatively treated as non-binary so the existing
// per-file error path can surface the failure to the caller.
func pathIsBinary(fs workspaceapi.FileSystem, path string) bool {
	f, err := fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8*1024)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	return utf8validate.IsBinary(buf[:n])
}
