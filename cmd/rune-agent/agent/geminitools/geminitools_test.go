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
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/internal/term/shellplan"
)

// recordingTool is a base agent.Tool that records the arguments passed to it.
type recordingTool struct {
	name     string
	lastArgs string
	result   agent.ToolResult
}

func (t *recordingTool) Definition() llmapi.Tool {
	return llmapi.Tool{Function: llmapi.FunctionDefinition{Name: t.name}}
}

func (t *recordingTool) Execute(_ context.Context, arguments string) agent.ToolResult {
	t.lastArgs = arguments
	return t.result
}

func (t *recordingTool) Summary(arguments string) string { return arguments }

func (t *recordingTool) NeedsDeterministicOrder() bool { return false }

// policyBash is a base bash tool that rejects the commands its policy does
// not allow, as agentools' bash does.
type policyBash struct {
	recordingTool
	policy toolpolicy.Policy
}

func (t *policyBash) WithPolicy(p toolpolicy.Policy) agent.Tool {
	return &policyBash{recordingTool: recordingTool{name: t.name, result: t.result}, policy: p}
}

func (t *policyBash) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}
	}
	if err := t.policy.CheckCommand(shellplan.Command("bash", []string{"-c", args.Command}, nil, nil)); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}
	}
	return t.recordingTool.Execute(ctx, arguments)
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestRunCommandTranslatesArgs(t *testing.T) {
	base := &recordingTool{name: "bash", result: agent.ToolResult{Content: "ok"}}
	tool := runCommand(base)

	assert.Equal(t, "run_command", tool.Definition().Function.Name)

	res := tool.Execute(context.Background(),
		`{"CommandLine":"go test ./...","Cwd":"/repo/pkg"}`)
	assert.Equal(t, "ok", res.Content)

	got := decode(t, base.lastArgs)
	assert.Equal(t, "go test ./...", got["command"])
	assert.Equal(t, "/repo/pkg", got["working_dir"])
	// The authorization prompt shows the description as what the command
	// is for, and Antigravity gives none.
	assert.NotContains(t, got, "description")

	assert.Equal(t, "go test ./...", tool.Summary(`{"CommandLine":"go test ./...","Cwd":"/"}`))
}

func TestRunCommandWithPolicy(t *testing.T) {
	r := agent.NewRegistry(&policyBash{recordingTool: recordingTool{name: "bash", result: agent.ToolResult{Content: "ok"}}})
	Register(r)
	p, err := toolpolicy.Parse([]string{"bash(git log *)"}, nil)
	require.NoError(t, err)

	tool, ok := r.WithPolicy(p).Get("run_command", LLMProvider)
	require.True(t, ok)

	assert.Contains(t, tool.Definition().Function.Description,
		"Commands are restricted: every program a command runs must match one of `git log *`.")
	denied := tool.Execute(context.Background(), `{"CommandLine":"git push","Cwd":"/repo"}`)
	assert.True(t, denied.IsError)
	assert.Contains(t, denied.Content, `"git push" matches no allowed pattern`)
	allowed := tool.Execute(context.Background(), `{"CommandLine":"git log -1","Cwd":"/repo"}`)
	assert.False(t, allowed.IsError, allowed.Content)
	assert.Equal(t, "ok", allowed.Content)

	unrestricted, ok := r.Get("run_command", LLMProvider)
	require.True(t, ok)
	assert.NotContains(t, unrestricted.Definition().Function.Description, "Commands are restricted")
}

func TestGrepSearchEscapesLiteralQuery(t *testing.T) {
	base := &recordingTool{name: "search_content"}
	tool := grepSearch(base)

	// Literal (IsRegexp=false): special chars must be escaped for RE2.
	tool.Execute(context.Background(),
		`{"Query":"foo.bar(","SearchDirectory":"sub","IsRegexp":false,"Includes":"*.go"}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, `foo\.bar\(`, got["pattern"])
	assert.Equal(t, "sub", got["path"])
	assert.Equal(t, "*.go", got["include"])
}

func TestGrepSearchPassesRegexThrough(t *testing.T) {
	base := &recordingTool{name: "search_content"}
	tool := grepSearch(base)

	tool.Execute(context.Background(),
		`{"Query":"foo.*bar","SearchDirectory":"","IsRegexp":true}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, "foo.*bar", got["pattern"])
}

func TestFindGlobToRegex(t *testing.T) {
	base := &recordingTool{name: "find_files"}
	tool := find(base)

	tool.Execute(context.Background(),
		`{"SearchDirectory":"cmd","Pattern":"*.go"}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, `.*\.go$`, got["pattern"])
	assert.Equal(t, "cmd", got["path"])
	assert.Equal(t, true, got["recursive"])
}

func TestCodebaseSearchForwardsQuery(t *testing.T) {
	base := &recordingTool{name: "search_symbols"}
	tool := codebaseSearch(base)

	tool.Execute(context.Background(),
		`{"Query":"http handler","TargetDirectories":["/a","/b"]}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, "http handler", got["query"])
	// TargetDirectories is advisory for our symbol search; not forwarded.
	_, hasTargets := got["TargetDirectories"]
	assert.False(t, hasTargets)
}

func TestViewFileOutlineTranslatesPath(t *testing.T) {
	base := &recordingTool{name: "outline_file"}
	tool := viewFileOutline(base)

	tool.Execute(context.Background(), `{"AbsolutePath":"/repo/main.go"}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, "/repo/main.go", got["path"])
}

func TestListDirectoryTranslatesPath(t *testing.T) {
	base := &recordingTool{name: "list_dir"}
	tool := listDirectory(base)

	tool.Execute(context.Background(), `{"DirectoryPath":"/repo"}`)
	got := decode(t, base.lastArgs)
	assert.Equal(t, "/repo", got["dir_path"])
}

func TestExecuteRejectsInvalidArgs(t *testing.T) {
	base := &recordingTool{name: "bash"}
	tool := runCommand(base)
	res := tool.Execute(context.Background(), `not json`)
	assert.True(t, res.IsError)
	assert.Empty(t, base.lastArgs, "base tool must not be invoked on bad args")
}

func TestAllSpecializedDefinitionsAreValid(t *testing.T) {
	bases := map[string]agent.Tool{
		"bash":           &recordingTool{name: "bash"},
		"search_content": &recordingTool{name: "search_content"},
		"find_files":     &recordingTool{name: "find_files"},
		"search_symbols": &recordingTool{name: "search_symbols"},
		"outline_file":   &recordingTool{name: "outline_file"},
		"list_dir":       &recordingTool{name: "list_dir"},
	}
	wantNames := map[string]string{
		"bash":           "run_command",
		"search_content": "grep_search",
		"find_files":     "find",
		"search_symbols": "codebase_search",
		"outline_file":   "view_file_outline",
		"list_dir":       "list_dir",
	}
	for baseName, wrap := range geminiSpecializations {
		def := wrap(bases[baseName]).Definition()
		assert.Equal(t, wantNames[baseName], def.Function.Name)
		params, ok := def.Function.Parameters.(map[string]any)
		require.True(t, ok, "%s parameters must be an object", def.Function.Name)
		assert.Equal(t, "object", params["type"])
		assert.Equal(t, false, params["additionalProperties"])
	}
}

func TestRegisterInstallsGeminiOverrides(t *testing.T) {
	r := agent.NewRegistry(
		&recordingTool{name: "bash"},
		&recordingTool{name: "search_content"},
		&recordingTool{name: "find_files"},
		&recordingTool{name: "search_symbols"},
		&recordingTool{name: "outline_file"},
		&recordingTool{name: "list_dir"},
		&recordingTool{name: "read_file"},
	)
	Register(r)

	gemini := map[string]bool{}
	for _, td := range r.Tools(LLMProvider) {
		gemini[td.Function.Name] = true
	}
	// Antigravity names present; base names hidden.
	for _, name := range []string{"run_command", "grep_search", "find", "codebase_search", "view_file_outline"} {
		assert.True(t, gemini[name], "gemini must expose %q", name)
	}
	for _, name := range []string{"bash", "search_content", "find_files", "search_symbols", "outline_file"} {
		assert.False(t, gemini[name], "gemini must not expose base %q", name)
	}
	// Non-specialized tools remain.
	assert.True(t, gemini["read_file"])

	// Base providers are unaffected.
	base := map[string]bool{}
	for _, td := range r.Tools("") {
		base[td.Function.Name] = true
	}
	assert.True(t, base["bash"])
	assert.False(t, base["run_command"])

	// Replacement pairings are recorded so excluded base tools can hint
	// the gemini-specialized replacement.
	assert.Equal(t, "run_command", r.ReplacementFor("bash", LLMProvider))
	assert.Equal(t, "grep_search", r.ReplacementFor("search_content", LLMProvider))
	assert.Empty(t, r.ReplacementFor("bash", ""))
}

func TestRegisterSkipsMissingBaseTools(t *testing.T) {
	r := agent.NewRegistry(&recordingTool{name: "read_file"})
	Register(r)
	_, ok := r.Get("run_command", LLMProvider)
	assert.False(t, ok, "missing base tools must not produce specialized tools")
}
