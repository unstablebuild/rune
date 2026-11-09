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
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

const (
	defaultYieldTimeMs = 10000
	minYieldTimeMs     = 250
	maxYieldTimeMs     = 30000
)

type execCommandTool struct {
	mgr    *SessionManager
	cwd    workspaceapi.URI
	guard  grepGuard
	policy toolpolicy.Policy
}

type execCommandArgs struct {
	Cmd         string `json:"cmd"`
	WorkDir     string `json:"workdir"`
	Shell       string `json:"shell"`
	TTY         *bool  `json:"tty"`
	YieldTimeMs *int   `json:"yield_time_ms"`
}

type execCommandOutput struct {
	SessionID       int     `json:"session_id,omitempty"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	Output          string  `json:"output"`
	WallTimeSeconds float64 `json:"wall_time_seconds"`
}

// NewExecCommand creates an exec_command tool backed by the given
// SessionManager. It is intended as an OpenAI-specific override that
// replaces the stateless bash tool with persistent sessions. The guard
// intercepts bare grep invocations: it reads force_builtin_tools from
// cfg, asks the user (via agent.PrompterFromContext) when the key is
// absent, and persists "Always"/"Never" choices back into cfg so
// subsequent resolves observe the new value in the same session.
func NewExecCommand(
	mgr *SessionManager,
	cwd workspaceapi.URI,
	cfg configedit.Config,
) agent.Tool {
	return &execCommandTool{
		mgr:   mgr,
		cwd:   cwd,
		guard: newGrepGuard(cfg),
	}
}

// WithPolicy implements agent.PolicyTool: the copy rejects the commands p
// does not allow before running them.
func (t *execCommandTool) WithPolicy(p toolpolicy.Policy) agent.Tool {
	c := *t
	c.policy = p
	return &c
}

func (t *execCommandTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "exec_command",
			Description: withCommandHelp(`Runs a command in a PTY, returning output or a session ID for ongoing interaction.

Do NOT use this tool for tasks that have a dedicated tool:
• grep/rg/ag → use grep_files or search_symbols
• cat/head/tail → use read_file
• gofmt/goimports → use format_file
• find → use find_files
• Symbol search → use search_symbols or find_definition

Use exec_command only for running tests, build commands, git, or other
tasks with no dedicated tool.`, t.policy),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{
						"type":        "string",
						"description": "Shell command to execute.",
					},
					"workdir": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Optional working directory to run the command in; defaults to the workspace root.",
					},
					"shell": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Shell binary to launch. Defaults to the user's default shell.",
					},
					"tty": map[string]any{
						"type":        []string{"boolean", "null"},
						"description": "Whether to allocate a TTY for the command. Defaults to false (plain pipes); set to true to open a PTY and access TTY process.",
					},
					"yield_time_ms": map[string]any{
						"type":        []string{"number", "null"},
						"description": "How long to wait (in milliseconds) for output before yielding.",
					},
				},
				"required":             []string{"cmd"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *execCommandTool) Summary(arguments string) string {
	var args execCommandArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return args.Cmd
}

func (t *execCommandTool) NeedsDeterministicOrder() bool { return true }

func (t *execCommandTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args execCommandArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}

	if args.Cmd == "" {
		return agent.ToolResult{Content: "error: cmd must not be empty", IsError: true}
	}

	// The session runs the command with the shell the model picks, so
	// the check plans that shell, whatever it is.
	shell := args.Shell
	if shell == "" {
		shell = "bash"
	}
	if rejected, ok := checkCommand(t.policy, shell, args.Cmd); ok {
		return rejected
	}

	if isGrepInvocation(args.Cmd) {
		if rejected, decided := t.guard.decideGrep(ctx); decided && rejected {
			return agent.ToolResult{
				Content: builtinToolsErrorMessage("openai"),
				IsError: true,
			}
		}
	}

	workDir := t.cwd.Path()
	if args.WorkDir != "" {
		workDir = resolvePath(t.cwd, args.WorkDir)
	}

	tty := false
	if args.TTY != nil {
		tty = *args.TTY
	}

	yieldMs := defaultYieldTimeMs
	if args.YieldTimeMs != nil {
		yieldMs = *args.YieldTimeMs
	}
	yieldMs = clamp(yieldMs, minYieldTimeMs, maxYieldTimeMs)

	sess, err := t.mgr.Create(args.Cmd, workDir, args.Shell, tty, nil)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}

	// Wait for process exit or yield timeout.
	yieldDuration := time.Duration(yieldMs) * time.Millisecond
	exited := sess.Wait(yieldDuration)

	// If not exited, wait a bit more for output to settle.
	if !exited {
		sess.buf.WaitForData(time.Now().Add(50 * time.Millisecond))
	}

	return t.buildResult(sess)
}

func (t *execCommandTool) buildResult(sess *Session) agent.ToolResult {
	out := execCommandOutput{
		Output:          sess.Output(),
		WallTimeSeconds: sess.WallTime(),
	}
	if sess.Exited() {
		code := sess.ExitCode()
		out.ExitCode = &code
	} else {
		out.SessionID = sess.ID
	}

	data, err := json.Marshal(out)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: marshal output: %v", err), IsError: true}
	}

	isError := out.ExitCode != nil && *out.ExitCode != 0
	return agent.ToolResult{Content: string(data), IsError: isError}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
