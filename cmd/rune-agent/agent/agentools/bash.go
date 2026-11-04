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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/internal/term/shellplan"
)

const (
	maxCommandOutput = 100 * 1024 // 100KB
)

type bashTool struct {
	exec   workspaceapi.Executor
	cwd    workspaceapi.URI
	guard  grepGuard
	policy toolpolicy.Policy
}

type bashArgs struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	WorkingDir  string `json:"working_dir"`
}

// newBash builds a bash tool. The guard intercepts bare grep
// invocations: it reads force_builtin_tools from cfg, asks the user
// (via agent.PrompterFromContext) when the key is absent, and
// persists "Always"/"Never" choices back into cfg so subsequent
// resolves observe the new value in the same session.
func newBash(
	exec workspaceapi.Executor,
	cwd workspaceapi.URI,
	cfg configedit.Config,
) agent.Tool {
	return &bashTool{
		exec:  exec,
		cwd:   cwd,
		guard: newGrepGuard(cfg),
	}
}

// WithPolicy implements agent.PolicyTool: the copy rejects the commands p
// does not allow before running them.
func (t *bashTool) WithPolicy(p toolpolicy.Policy) agent.Tool {
	c := *t
	c.policy = p
	return &c
}

// withCommandHelp appends the restrictions of p on commands to the
// description of a shell tool.
func withCommandHelp(description string, p toolpolicy.Policy) string {
	if help := p.CommandHelp(); help != "" {
		return description + "\n\n" + help
	}
	return description
}

// checkCommand returns the result of a shell tool rejecting the command
// shell runs with -c, and false when p allows it.
func checkCommand(p toolpolicy.Policy, shell, command string) (agent.ToolResult, bool) {
	if err := p.CheckCommand(shellplan.Command(shell, []string{"-c", command}, nil, nil)); err != nil {
		return agent.ToolResult{Content: "error: " + err.Error(), IsError: true}, true
	}
	return agent.ToolResult{}, false
}

// Definition returns the LLM tool definition for bash.
func (t *bashTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "bash",
			Description: withCommandHelp(`Executes a bash command and returns its output.

Commands run in the workspace root by default — do NOT prepend cd to
the command. Use working_dir only if you need a different directory.
Each invocation starts a fresh shell; state does not persist between calls.

Output is truncated at 100 KB.

Do NOT use this tool for tasks that have a dedicated tool:
• grep/rg/ag → use search_content or find_files
• cat/head/tail → use read_file
• gofmt/goimports → use format_file
• find → use find_files
• Symbol search → use search_symbols or find_definition

Use bash only for running tests, build commands, git, or other tasks
with no dedicated tool.`, t.policy),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute.",
					},
					"description": map[string]any{
						"type": "string",
						"description": "Clear, concise description of what " +
							"this command does (5-10 words for simple " +
							"commands, more for complex ones).",
					},
					"working_dir": map[string]any{
						"type": []string{"string", "null"},
						"description": "Working directory (relative to " +
							"workspace root or absolute). Defaults to " +
							"workspace root.",
					},
				},
				"required":             []string{"command", "description"},
				"additionalProperties": false,
			},
		},
	}
}

// Summary returns a short human-readable summary of the tool arguments.
func (t *bashTool) Summary(arguments string) string {
	var args bashArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Command
}

func (t *bashTool) NeedsDeterministicOrder() bool { return true }

// Execute runs the bash command.
func (t *bashTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args bashArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}

	if rejected, ok := checkCommand(t.policy, "bash", args.Command); ok {
		return rejected
	}

	if isGrepInvocation(args.Command) {
		if rejected, decided := t.guard.decideGrep(ctx); decided && rejected {
			return agent.ToolResult{
				Content: builtinToolsErrorMessage("anthropic"),
				IsError: true,
			}
		}
	}

	workDir := t.cwd.Path()
	if args.WorkingDir != "" {
		workDir = resolvePath(t.cwd, args.WorkingDir)
	}

	var buf bytes.Buffer
	watcher := newProcessWatcher()

	cmd := workspaceapi.Cmd{
		Path: "bash",
		Args: []string{"-c", args.Command},
		Dir:  workDir,
		// Env is intentionally nil: the host executor falls back to its
		// own os.Environ(), which carries the shell-loaded PATH and any
		// gui.env overrides. Setting Env from the extension would shadow
		// those values because Go's os/exec lets the last duplicate key
		// win when merging.
		Stdout:  &buf,
		Stderr:  &buf,
		Watcher: watcher,
		// The workspace shows it when it asks the user to authorize the
		// command.
		Description: args.Description,
	}

	_, err := t.exec.Start(ctx, cmd)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: %v", err),
			IsError: true,
		}
	}

	// Wait for process to finish or context cancellation.
	var procErr error
	select {
	case procErr = <-watcher.WatchProcess():
	case <-ctx.Done():
		procErr = ctx.Err()
	}

	output := buf.Bytes()

	// Truncate if too large. Snap the cut to a UTF-8 rune boundary
	// so we don't slice through a multi-byte character and produce
	// invalid UTF-8.
	if len(output) > maxCommandOutput {
		cut := maxCommandOutput
		for cut > 0 && !utf8.RuneStart(output[cut]) {
			cut--
		}
		output = fmt.Appendf(output[:cut], "\n\n(output truncated at %d bytes)", maxCommandOutput)
	}

	hints := bashHints(args.Command, workDir)

	if procErr != nil {
		return agent.ToolResult{
			Content: appendHints(
				utf8validate.Sanitize(fmt.Sprintf("%s\nerror: %v", string(output), procErr)),
				hints,
			),
			IsError: true,
		}
	}

	return agent.ToolResult{Content: appendHints(utf8validate.Sanitize(string(output)), hints)}
}

func appendHints(content string, hints []string) string {
	for _, hint := range hints {
		content += "\n\nTIP: " + hint
	}
	return content
}

type processWatcher struct {
	ch chan error
}

func newProcessWatcher() *processWatcher {
	return &processWatcher{ch: make(chan error, 1)}
}

func (w *processWatcher) WatchProcess() chan error {
	return w.ch
}
