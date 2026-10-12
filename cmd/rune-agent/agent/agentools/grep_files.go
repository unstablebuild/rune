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
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
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
	fs      workspaceapi.FileSystem
	cwd     workspaceapi.URI
	tracker *FileTracker
	lsp     semanticapi.LSP
	parser  syntaxapi.Parser
}

type grepFilesArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
	Limit   *int   `json:"limit"`
}

// NewGrepFiles creates a grep_files tool that returns file paths whose
// contents match a regex pattern, sorted by modification time. It is
// designed to override search_content for providers that expect the
// Codex grep_files interface (e.g. OpenAI).
func NewGrepFiles(
	wfs workspaceapi.FileSystem,
	cwd workspaceapi.URI,
	tracker *FileTracker,
	lsp semanticapi.LSP,
	parser syntaxapi.Parser,
) agent.Tool {
	return &grepFilesTool{
		fs:      wfs,
		cwd:     cwd,
		tracker: tracker,
		lsp:     lsp,
		parser:  parser,
	}
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

	if dotted, ok := resolveDottedReferences(ctx, t.parser, t.lsp, args.Pattern); ok {
		seen := make(map[string]struct{})
		var relPaths []string
		var discovered []string
		for _, refs := range dotted.references {
			for _, ref := range refs {
				rel := uriToRelPath(ref.URI, t.cwd)
				if _, dup := seen[rel]; !dup {
					seen[rel] = struct{}{}
					relPaths = append(relPaths, rel)
					if parsed, err := workspaceapi.ParseURI(ref.URI); err == nil {
						discovered = append(discovered, parsed.Path())
					}
				}
			}
		}
		truncated := len(relPaths) > limit
		if truncated {
			relPaths = relPaths[:limit]
			if len(discovered) > limit {
				discovered = discovered[:limit]
			}
		}
		if t.tracker != nil {
			t.tracker.TrackDiscovery(ctx, dedupPaths(discovered))
		}
		note := fmt.Sprintf(symbolRedirectNote, args.Pattern)
		output := note + "\n\n" + strings.Join(relPaths, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(results truncated at %d files)", limit)
		}
		return agent.ToolResult{Content: output}
	}

	root := t.cwd.Path()
	if args.Path != "" {
		root = resolvePath(t.cwd, args.Path)
	}
	walkCtx := boundedWalkdirContext(ctx)
	wsRoot := t.cwd.Path()

	paths, err := listToolFiles(walkCtx, t.fs, t.cwd, root, nil)
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
		// Skip binary files (e.g. compiled objects, fonts, images
		// without recognised extensions). Reading lines from them
		// would emit invalid UTF-8 and bloats the line iterator
		// channel; this matches ripgrep's default behaviour.
		return !pathIsBinary(t.fs, filepath.Join(wsRoot, path))
	})

	lines, err := walkdir.ReadLines(walkCtx, t.fs, filtered)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: reading files: %v", err), IsError: true}
	}
	defer func() { _ = lines.Close() }()

	// Collect unique file paths that contain at least one match.
	seen := make(map[string]struct{})
	var matches []grepFileMatch
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
		content := utf8validate.Sanitize(contentAfterLineNum(line))
		if re.MatchString(content) {
			seen[relPath] = struct{}{}
			absPath := filepath.Join(wsRoot, relPath)
			var modTime time.Time
			if info, err := t.fs.Stat(absPath); err == nil {
				modTime = info.ModTime()
			}
			matches = append(matches, grepFileMatch{
				relPath: relPath,
				absPath: absPath,
				modTime: modTime,
			})
		}
	}

	warning, fatal := walkIterErr(ctx, lines.Err(), len(matches))
	if fatal {
		return agent.ToolResult{Content: warning, IsError: true}
	}

	if len(matches) == 0 {
		return agent.ToolResult{Content: "No matches found."}
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
