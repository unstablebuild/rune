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

package geminitools

import (
	"encoding/json"
	"regexp"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// runCommand presents Antigravity's run_command over the base bash tool.
// Antigravity uses CommandLine + Cwd; our bash uses command + working_dir and
// additionally requires a description, which we synthesize from the command.
func runCommand(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		CommandLine string `json:"CommandLine"`
		Cwd         string `json:"Cwd"`
	}
	type bashArgs struct {
		Command    string `json:"command"`
		WorkingDir string `json:"working_dir,omitempty"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		return remarshal(bashArgs{
			Command:    a.CommandLine,
			WorkingDir: a.Cwd,
		})
	}
	return &specializedTool{
		base:         base,
		translate:    translate,
		runsCommands: true,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return a.CommandLine
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name: "run_command",
				Description: "Executes a terminal command and returns its output. " +
					"State does not persist between calls; output is truncated at 100 KB.\n\n" +
					"Do NOT use this for tasks with a dedicated tool:\n" +
					"• grep/rg/ag → use grep_search or codebase_search\n" +
					"• cat/head/tail → use read_file\n" +
					"• find → use find\n" +
					"• Symbol search → use codebase_search or find_definition\n\n" +
					"Use run_command only for running tests, build commands, git, or other " +
					"tasks with no dedicated tool.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"CommandLine": map[string]any{
							"type":        "string",
							"description": "The exact command line string to execute.",
						},
						"Cwd": map[string]any{
							"type":        "string",
							"description": "The current working directory for the command.",
						},
					},
					"required":             []string{"CommandLine", "Cwd"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// grepSearch presents Antigravity's grep_search over the base search_content
// tool. Antigravity treats Query as literal unless IsRegexp is set; our
// search_content always treats pattern as RE2 regex, so a literal query is
// escaped before delegation.
func grepSearch(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		Query           string `json:"Query"`
		SearchDirectory string `json:"SearchDirectory"`
		IsRegexp        bool   `json:"IsRegexp"`
		Includes        string `json:"Includes"`
	}
	type searchArgs struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path,omitempty"`
		Include string `json:"include,omitempty"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		pattern := a.Query
		if !a.IsRegexp {
			pattern = regexp.QuoteMeta(a.Query)
		}
		return remarshal(searchArgs{
			Pattern: pattern,
			Path:    a.SearchDirectory,
			Include: a.Includes,
		})
	}
	return &specializedTool{
		base:      base,
		translate: translate,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return `"` + a.Query + `"`
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name: "grep_search",
				Description: "Searches file contents for a query across the workspace, returning " +
					"matching lines as \"filepath:line:content\", capped at 200 matches.\n\n" +
					"Best for literal text: string literals, error messages, comments, TODOs, " +
					"or config values. For finding a function or type by name, prefer " +
					"codebase_search or find_definition.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"Query": map[string]any{
							"type":        "string",
							"description": "Search query.",
						},
						"SearchDirectory": map[string]any{
							"type":        "string",
							"description": "The directory to search within. Defaults to the workspace root.",
						},
						"IsRegexp": map[string]any{
							"type": "boolean",
							"description": "If true, treats Query as a regular expression (RE2). " +
								"If false, treats Query as a literal string. Use false for normal " +
								"text searches and true only when you need regex functionality.",
						},
						"Includes": map[string]any{
							"type":        "string",
							"description": "Optional glob pattern to filter files (e.g. '*.go').",
						},
					},
					"required":             []string{"Query"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// find presents Antigravity's find over the base find_files tool. Antigravity
// matches a glob Pattern against names; find_files matches an RE2 regex against
// the full relative path. The glob is converted to an RE2 pattern.
func find(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		SearchDirectory string `json:"SearchDirectory"`
		Pattern         string `json:"Pattern"`
	}
	type findFilesArgs struct {
		Pattern   string `json:"pattern"`
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		return remarshal(findFilesArgs{
			Pattern:   globToRegex(a.Pattern),
			Path:      a.SearchDirectory,
			Recursive: true,
		})
	}
	return &specializedTool{
		base:      base,
		translate: translate,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return `"` + a.Pattern + `"`
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name: "find",
				Description: "Finds files by name within a directory using a glob pattern. " +
					"Returns matching file paths relative to the workspace root, capped at 500 results.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"SearchDirectory": map[string]any{
							"type":        "string",
							"description": "The directory to search within. Defaults to the workspace root.",
						},
						"Pattern": map[string]any{
							"type":        "string",
							"description": "Pattern to search for, supports glob format (e.g. '*.go', 'Makefile').",
						},
					},
					"required":             []string{"Pattern"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// codebaseSearch presents Antigravity's codebase_search over the base
// search_symbols tool. Antigravity's tool is embeddings-based and accepts
// TargetDirectories; our symbol search is name-based and workspace-wide, so
// only Query is forwarded and TargetDirectories is advisory.
func codebaseSearch(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		Query             string   `json:"Query"`
		TargetDirectories []string `json:"TargetDirectories"`
	}
	type searchSymbolsArgs struct {
		Query string `json:"query"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		return remarshal(searchSymbolsArgs{Query: a.Query})
	}
	return &specializedTool{
		base:      base,
		translate: translate,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return a.Query
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name: "codebase_search",
				Description: "Finds code relevant to a query across the workspace, returning " +
					"matching symbols with their kind and location (e.g. " +
					"\"handler.go:42:function:HandleRequest\"). Use to locate a function or " +
					"type when you know roughly what it is but not its file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"Query": map[string]any{
							"type":        "string",
							"description": "Search query.",
						},
						"TargetDirectories": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Optional list of directories to prioritize in the search.",
						},
					},
					"required":             []string{"Query"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// viewFileOutline presents Antigravity's view_file_outline over the base
// outline_file tool. Antigravity uses AbsolutePath; outline_file uses path.
func viewFileOutline(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		AbsolutePath string `json:"AbsolutePath"`
	}
	type outlineArgs struct {
		Path string `json:"path"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		return remarshal(outlineArgs{Path: a.AbsolutePath})
	}
	return &specializedTool{
		base:      base,
		translate: translate,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return a.AbsolutePath
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name: "view_file_outline",
				Description: "Generates a map of the symbols (types, functions, methods) defined " +
					"in a file with their line numbers, for fast navigation without reading the " +
					"whole file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"AbsolutePath": map[string]any{
							"type":        "string",
							"description": "Path to the file to outline.",
						},
					},
					"required":             []string{"AbsolutePath"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// listDirectory presents Antigravity's list_dir over the base list_dir tool.
// Antigravity uses DirectoryPath; our list_dir uses dir_path.
func listDirectory(base agent.Tool) agent.Tool {
	type antigravityArgs struct {
		DirectoryPath string `json:"DirectoryPath"`
	}
	type listDirArgs struct {
		DirPath string `json:"dir_path"`
	}
	translate := func(arguments string) (string, error) {
		var a antigravityArgs
		if err := json.Unmarshal([]byte(arguments), &a); err != nil {
			return "", err
		}
		return remarshal(listDirArgs{DirPath: a.DirectoryPath})
	}
	return &specializedTool{
		base:      base,
		translate: translate,
		summarize: func(arguments string) string {
			var a antigravityArgs
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return ""
			}
			return a.DirectoryPath
		},
		def: llmapi.Tool{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "list_dir",
				Description: "Lists the contents of a directory to understand project structure.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"DirectoryPath": map[string]any{
							"type":        "string",
							"description": "Path to list contents of.",
						},
					},
					"required":             []string{"DirectoryPath"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// globToRegex converts a shell glob into an RE2 pattern matched against the
// full relative path, mirroring find_files semantics. Supports * and ?.
func globToRegex(glob string) string {
	if glob == "" {
		return ""
	}
	var b []byte
	for _, r := range glob {
		switch r {
		case '*':
			b = append(b, '.', '*')
		case '?':
			b = append(b, '.')
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b = append(b, '\\', byte(r))
		default:
			b = append(b, string(r)...)
		}
	}
	return string(b) + "$"
}
