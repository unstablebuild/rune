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

package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

func TestEncodeParseAttachmentPartV1Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		meta attachmentPartMeta
		body string
	}{
		{
			name: "file with body",
			meta: attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			},
			body: "package main\n",
		},
		{
			name: "symbol with body",
			meta: attachmentPartMeta{
				ID: "attachment-2", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			},
			body: "## Definition\na.go:1",
		},
		{
			name: "image without body",
			meta: attachmentPartMeta{
				ID: "attachment-3", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			},
		},
		{
			name: "file failure keeps id and status but no image",
			meta: attachmentPartMeta{
				ID: "attachment-4", Kind: attachmentKindFile,
				Name: "gone.go", Path: "gone.go",
				Status: attachmentStatusError, Error: "no such file",
			},
			body: "could not be read: no such file",
		},
		{
			name: "unicode name and multiline body",
			meta: attachmentPartMeta{
				ID: "attachment-5", Kind: attachmentKindFile,
				Name: "résumé.pdf", Path: "docs/résumé.pdf", Status: attachmentStatusOK,
			},
			body: "line one\nline two\n日本語",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeAttachmentPart(tc.meta, tc.body)
			gotMeta, gotBody, ok := parseAttachmentPartV1(encoded)
			require.True(t, ok)
			assert.Equal(t, tc.meta, gotMeta)
			assert.Equal(t, tc.body, gotBody)
		})
	}
}

func TestPreviewSpanPadsMarkdown(t *testing.T) {
	md, err := markdown.New("hello")
	require.NoError(t, err)
	span := newPreviewSpan(mdhandler.New(md))
	span.Resize(20, 4)
	w := term.NewStringWriter(20, 4)
	span.Draw(w)
	assert.Zero(t, w.Cells()[0].Ch)
	assert.NotZero(t, w.Cells()[1].Ch)
	assert.Zero(t, w.Cells()[19].Ch)
	exit, handled := span.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
	assert.True(t, handled)
}

func TestToolCallPreviewerOpensOneWindowPerToolCall(t *testing.T) {
	turn := dialoguetui.NewTurn(&dialoguetui.ComponentConfig{}, nil)
	turn.AddToolCall("a", "read_file", "", "")
	turn.AddToolCall("b", "write_stdin", "", "")
	turn.Resize(60, turn.Height(60))
	a, ok := turn.ToolCallAt(term.Coordinates{X: 2, Y: 0})
	require.True(t, ok)
	b, ok := turn.ToolCallAt(term.Coordinates{X: 2, Y: 1})
	require.True(t, ok)
	require.NotEqual(t, a.Key, b.Key)

	wm := &previewWindowManager{open: make(map[uint64]browserapi.Floating)}
	p := newToolCallPreviewer(&aiEditorHandler{wm: wm, n: stubNotifications{}})

	p.OpenToolCall(a)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{2}, wm.openIDs(), "clicking again replaces the preview")
	// The replaced window's Close arrives after its replacement opened.
	wm.deliverCloses(t)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{3}, wm.openIDs(), "a stale close must not orphan the preview")

	p.OpenToolCall(b)
	assert.Equal(t, []uint64{3, 4}, wm.openIDs(), "each tool call has its own preview")

	require.NoError(t, wm.CloseWindow(previewWindow(3)))
	wm.deliverCloses(t)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{4, 5}, wm.openIDs())
}

func TestMarkdownPreviewCloseLeavesItsWindow(t *testing.T) {
	wm := &previewWindowManager{open: make(map[uint64]browserapi.Floating)}
	h := &aiEditorHandler{wm: wm, n: stubNotifications{}}
	var closed browserapi.Window
	win, err := h.openMarkdownPreview("hello", func(w browserapi.Window) { closed = w })
	require.NoError(t, err)
	preview := wm.open[win.WindowID()]
	require.NotNil(t, preview)

	require.NoError(t, preview.Close())

	assert.Equal(t, win, closed, "the owner still learns of the close")
	assert.Equal(t, []uint64{win.WindowID()}, wm.openIDs(),
		"the window may already show other content when the preview is released")
}

func TestToolCallPreviewMarkdown(t *testing.T) {
	block := func(language, text string) string {
		return "````" + language + "\n" + text + "\n````\n\n"
	}
	const waiting = "_Waiting for result…_\n"
	for _, tc := range []struct {
		name    string
		details dialoguetui.ToolCallDetails
		want    string
	}{
		{
			name:    "call without arguments waits for its result",
			details: dialoguetui.ToolCallDetails{Name: "TaskList"},
			want:    "# TaskList\n\n" + waiting,
		},
		{
			name:    "summary precedes indented arguments",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Summary: "main.go", Args: `{"path":"main.go","offset":0}`},
			want: "# read_file\n\n" + block("text", "main.go") +
				"## Arguments\n\n" + block("json", "{\n  \"path\": \"main.go\",\n  \"offset\": 0\n}") + waiting,
		},
		{
			name:    "summary markdown is literal",
			details: dialoguetui.ToolCallDetails{Name: "web_browser", Summary: "# not a heading\n**not bold** <b>x</b>\n- not a list\n[not](a link)"},
			want:    "# web_browser\n\n" + block("text", "# not a heading\n**not bold** <b>x</b>\n- not a list\n[not](a link)") + waiting,
		},
		{
			name:    "summary fence outgrows the fences it contains",
			details: dialoguetui.ToolCallDetails{Name: "web_browser", Summary: "a ```` b ````` c"},
			want:    "# web_browser\n\n``````text\na ```` b ````` c\n``````\n\n" + waiting,
		},
		{
			name:    "malformed arguments are shown verbatim",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Args: `{"path":`},
			want:    "# read_file\n\n## Arguments\n\n" + block("json", `{"path":`) + waiting,
		},
		{
			name:    "argument order and large integers survive formatting",
			details: dialoguetui.ToolCallDetails{Name: "agent", Args: `{"z":1,"value":12345678901234567890}`},
			want: "# agent\n\n## Arguments\n\n" +
				block("json", "{\n  \"z\": 1,\n  \"value\": 12345678901234567890\n}") + waiting,
		},
		{
			name:    "arguments are not HTML escaped",
			details: dialoguetui.ToolCallDetails{Name: "web_browser", Args: `{"command":["open","https://x.dev/?a=<b>&c"]}`},
			want: "# web_browser\n\n## Arguments\n\n" +
				block("json", "{\n  \"command\": [\n    \"open\",\n    \"https://x.dev/?a=<b>&c\"\n  ]\n}") + waiting,
		},
		{
			name:    "text output is literal",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Output: "L1: # not a heading\nL2: *not em*", Done: true},
			want:    "# read_file\n\n## Result\n\n" + block("text", "L1: # not a heading\nL2: *not em*"),
		},
		{
			name:    "JSON output is highlighted as JSON",
			details: dialoguetui.ToolCallDetails{Name: "web_browser", Output: `{"result":true}`, Done: true},
			want:    "# web_browser\n\n## Result\n\n" + block("json", `{"result":true}`),
		},
		{
			name:    "output fence outgrows the fences it contains",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Output: "```go\nx\n```\n````", Done: true},
			want:    "# read_file\n\n## Result\n\n`````text\n```go\nx\n```\n````\n`````\n\n",
		},
		{
			name:    "error output is literal",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Args: `{"path":"x"}`, Output: "# not a heading\n```embedded```", Done: true, IsError: true},
			want: "# read_file\n\n## Arguments\n\n" + block("json", "{\n  \"path\": \"x\"\n}") +
				"## Error\n\n" + block("text", "# not a heading\n```embedded```"),
		},
		{
			name:    "error without output",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Done: true, IsError: true},
			want:    "# read_file\n\n## Error\n\n_(no output)_\n",
		},
		{
			name:    "result without output",
			details: dialoguetui.ToolCallDetails{Name: "write_stdin", Done: true},
			want:    "# write_stdin\n\n## Result\n\n_(no output)_\n",
		},
		{
			name:    "agent response is markdown",
			details: dialoguetui.ToolCallDetails{Name: "agent", Args: `{"description":"research"}`, Output: "# Findings\n\n**Important**", Done: true, MarkdownOutput: true},
			want: "# agent\n\n## Arguments\n\n" + block("json", "{\n  \"description\": \"research\"\n}") +
				"## Result\n\n# Findings\n\n**Important**\n",
		},
		{
			name:    "agent skill result is markdown",
			details: dialoguetui.ToolCallDetails{Name: "skill", Output: "# Research\n\n**Findings**", Done: true, MarkdownOutput: true},
			want:    "# skill\n\n## Result\n\n# Research\n\n**Findings**\n",
		},
		{
			name: "agent prompt is highlighted as literal markdown",
			details: dialoguetui.ToolCallDetails{
				Name: "agent", Summary: "Review changes",
				Args:   `{"subagent_type":"explore","description":"Review changes","prompt":"# not a heading\n1. Run ` + "`git diff`" + `"}`,
				Output: "# Findings", Done: true, MarkdownOutput: true,
			},
			want: "# agent\n\n" + block("markdown", "# not a heading\n1. Run `git diff`") +
				"## Arguments\n\n" + block("json", "{\n  \"description\": \"Review changes\",\n  \"subagent_type\": \"explore\"\n}") +
				"## Result\n\n# Findings\n",
		},
		{
			name:    "agent error is literal",
			details: dialoguetui.ToolCallDetails{Name: "agent", Output: "# failed", Done: true, IsError: true, MarkdownOutput: true},
			want:    "# agent\n\n## Error\n\n" + block("text", "# failed"),
		},
		{
			name:    "tools outside the whitelist are generic",
			details: dialoguetui.ToolCallDetails{Name: "mcp__shell__run", Summary: "ls", Args: `{"command":"ls"}`},
			want: "# mcp__shell__run\n\n" + block("text", "ls") +
				"## Arguments\n\n" + block("json", "{\n  \"command\": \"ls\"\n}") + waiting,
		},
		{
			name: "bash command is highlighted in place of the summary",
			details: dialoguetui.ToolCallDetails{
				Name: "bash", Summary: "cat <<'EOF'\n# not a heading\nEOF",
				Args:   `{"command":"cat <<'EOF'\n# not a heading\nEOF","description":"Write file","working_dir":null}`,
				Output: "# done", Done: true,
			},
			want: "# bash\n\n" + block("bash", "cat <<'EOF'\n# not a heading\nEOF") +
				"## Arguments\n\n" + block("json", "{\n  \"description\": \"Write file\",\n  \"working_dir\": null\n}") +
				"## Result\n\n" + block("text", "# done"),
		},
		{
			name:    "bash command alone has no arguments section",
			details: dialoguetui.ToolCallDetails{Name: "bash", Summary: "ls", Args: `{"command":"ls"}`},
			want:    "# bash\n\n" + block("bash", "ls") + waiting,
		},
		{
			name:    "remaining arguments are sorted, unescaped and keep large integers",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"working_dir":"<dir>","command":"ls","description":"a & b","n":12345678901234567890}`},
			want: "# bash\n\n" + block("bash", "ls") + "## Arguments\n\n" +
				block("json", "{\n  \"description\": \"a & b\",\n  \"n\": 12345678901234567890,\n  \"working_dir\": \"<dir>\"\n}") + waiting,
		},
		{
			name:    "bash command fence outgrows the fences it contains",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: "{\"command\":\"printf '````'\"}"},
			want:    "# bash\n\n`````bash\nprintf '````'\n`````\n\n" + waiting,
		},
		{
			name:    "bash arguments still streaming in are generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"command":"ls`},
			want:    "# bash\n\n## Arguments\n\n" + block("json", `{"command":"ls`) + waiting,
		},
		{
			name:    "bash arguments without a command are generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Summary: "x", Args: `{"description":"x"}`},
			want: "# bash\n\n" + block("text", "x") +
				"## Arguments\n\n" + block("json", "{\n  \"description\": \"x\"\n}") + waiting,
		},
		{
			name:    "bash non-string command is generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"command":["ls"]}`},
			want:    "# bash\n\n## Arguments\n\n" + block("json", "{\n  \"command\": [\n    \"ls\"\n  ]\n}") + waiting,
		},
		{
			name:    "bash empty command is generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"command":""}`},
			want:    "# bash\n\n## Arguments\n\n" + block("json", "{\n  \"command\": \"\"\n}") + waiting,
		},
		{
			name:    "bash null command is generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"command":null}`},
			want:    "# bash\n\n## Arguments\n\n" + block("json", "{\n  \"command\": null\n}") + waiting,
		},
		{
			name:    "bash non-object arguments are generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `["ls"]`},
			want:    "# bash\n\n## Arguments\n\n" + block("json", "[\n  \"ls\"\n]") + waiting,
		},
		{
			name:    "bash JSON output stays generic",
			details: dialoguetui.ToolCallDetails{Name: "bash", Args: `{"command":"cat a.json"}`, Output: `{"output":"x"}`, Done: true},
			want:    "# bash\n\n" + block("bash", "cat a.json") + "## Result\n\n" + block("json", `{"output":"x"}`),
		},
		{
			name:    "run_command line is highlighted as bash",
			details: dialoguetui.ToolCallDetails{Name: "run_command", Summary: "go test ./...", Args: `{"CommandLine":"go test ./...","Cwd":"/repo"}`},
			want: "# run_command\n\n" + block("bash", "go test ./...") +
				"## Arguments\n\n" + block("json", "{\n  \"Cwd\": \"/repo\"\n}") + waiting,
		},
		{
			name: "exec_command highlights the command and unwraps the output",
			details: dialoguetui.ToolCallDetails{
				Name: "exec_command", Summary: "make test",
				Args:   `{"cmd":"make test","workdir":"pkg","tty":false}`,
				Output: `{"exit_code":0,"output":"# PASS\nok","wall_time_seconds":1.5}`, Done: true,
			},
			want: "# exec_command\n\n" + block("bash", "make test") +
				"## Arguments\n\n" + block("json", "{\n  \"tty\": false,\n  \"workdir\": \"pkg\"\n}") +
				"## Result\n\n" + block("text", "# PASS\nok") +
				block("json", "{\n  \"exit_code\": 0,\n  \"wall_time_seconds\": 1.5\n}"),
		},
		{
			name:    "exec_command failure unwraps the output under Error",
			details: dialoguetui.ToolCallDetails{Name: "exec_command", Args: `{"cmd":"false"}`, Output: `{"exit_code":1,"output":"boom"}`, Done: true, IsError: true},
			want: "# exec_command\n\n" + block("bash", "false") +
				"## Error\n\n" + block("text", "boom") + block("json", "{\n  \"exit_code\": 1\n}"),
		},
		{
			name:    "exec_command output alone has no metadata block",
			details: dialoguetui.ToolCallDetails{Name: "exec_command", Args: `{"cmd":"ls"}`, Output: `{"output":"a.go"}`, Done: true},
			want:    "# exec_command\n\n" + block("bash", "ls") + "## Result\n\n" + block("text", "a.go"),
		},
		{
			name:    "exec_command error message is generic",
			details: dialoguetui.ToolCallDetails{Name: "exec_command", Args: `{"cmd":"ls"}`, Output: "error: invalid arguments: x", Done: true, IsError: true},
			want:    "# exec_command\n\n" + block("bash", "ls") + "## Error\n\n" + block("text", "error: invalid arguments: x"),
		},
		{
			name:    "exec_command response without output is generic",
			details: dialoguetui.ToolCallDetails{Name: "exec_command", Args: `{"cmd":"sleep 9"}`, Output: `{"session_id":3,"output":"","wall_time_seconds":0.2}`, Done: true},
			want: "# exec_command\n\n" + block("bash", "sleep 9") +
				"## Result\n\n" + block("json", `{"session_id":3,"output":"","wall_time_seconds":0.2}`),
		},
		{
			name: "write_stdin keeps generic arguments and unwraps the output",
			details: dialoguetui.ToolCallDetails{
				Name: "write_stdin", Summary: "session 3: y\n",
				Args:   `{"session_id":3,"chars":"y\n"}`,
				Output: `{"session_id":3,"output":"Proceed? y\n","wall_time_seconds":0.1}`, Done: true,
			},
			want: "# write_stdin\n\n" + block("text", "session 3: y\n") +
				"## Arguments\n\n" + block("json", "{\n  \"session_id\": 3,\n  \"chars\": \"y\\n\"\n}") +
				"## Result\n\n" + block("text", "Proceed? y\n") +
				block("json", "{\n  \"session_id\": 3,\n  \"wall_time_seconds\": 0.1\n}"),
		},
		{
			name: "apply_patch is highlighted as a diff",
			details: dialoguetui.ToolCallDetails{
				Name: "apply_patch", Summary: "README.md",
				Args:   `{"patch":"*** Begin Patch\n*** Update File: README.md\n@@\n-# Old\n+# New\n*** End Patch"}`,
				Output: "Success. Updated: README.md", Done: true,
			},
			want: "# apply_patch\n\n" +
				block("diff", "*** Begin Patch\n*** Update File: README.md\n@@\n-# Old\n+# New\n*** End Patch") +
				"## Result\n\n" + block("text", "Success. Updated: README.md"),
		},
		{
			name:    "apply_patch fence outgrows the fences it adds",
			details: dialoguetui.ToolCallDetails{Name: "apply_patch", Args: "{\"patch\":\"*** Add File: a.md\\n+````go\\n+````\"}"},
			want:    "# apply_patch\n\n`````diff\n*** Add File: a.md\n+````go\n+````\n`````\n\n" + waiting,
		},
		{
			name:    "query_ast query is highlighted as a tree-sitter query",
			details: dialoguetui.ToolCallDetails{Name: "query_ast", Summary: "(call_expression) @call", Args: `{"query":"(call_expression) @call","captures":["call"]}`},
			want: "# query_ast\n\n" + block("query", "(call_expression) @call") +
				"## Arguments\n\n" + block("json", "{\n  \"captures\": [\n    \"call\"\n  ]\n}") + waiting,
		},
		{
			name:    "query_file_ast query is highlighted as a tree-sitter query",
			details: dialoguetui.ToolCallDetails{Name: "query_file_ast", Summary: "a.go", Args: `{"path":"a.go","query":"(identifier) @id","captures":null}`},
			want: "# query_file_ast\n\n" + block("query", "(identifier) @id") +
				"## Arguments\n\n" + block("json", "{\n  \"captures\": null,\n  \"path\": \"a.go\"\n}") + waiting,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, toolCallPreviewMarkdown(tc.details))
		})
	}
}

func TestToolPreviewFieldSplit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		field     toolPreviewField
		raw       string
		wantValue string
		wantRest  string
		wantOK    bool
	}{
		{name: "zero field", raw: `{"command":"ls"}`},
		{name: "lone field", field: toolPreviewField{"command", "bash"}, raw: `{"command":"ls"}`, wantValue: "ls", wantOK: true},
		{name: "whitespace around the object", field: toolPreviewField{"command", "bash"}, raw: " \n{\"command\": \"ls\"}\n", wantValue: "ls", wantOK: true},
		{name: "escapes are decoded", field: toolPreviewField{"command", "bash"}, raw: `{"command":"a\tb\n\"c\" \u003cd\u003e"}`, wantValue: "a\tb\n\"c\" <d>", wantOK: true},
		{
			name: "nested values are kept", field: toolPreviewField{"output", "text"},
			raw:       `{"output":"x","meta":{"b":[1,2],"a":true}}`,
			wantValue: "x", wantRest: "{\n  \"meta\": {\n    \"b\": [\n      1,\n      2\n    ],\n    \"a\": true\n  }\n}", wantOK: true,
		},
		{name: "field is case sensitive", field: toolPreviewField{"command", "bash"}, raw: `{"Command":"ls"}`},
		{name: "missing field", field: toolPreviewField{"command", "bash"}, raw: `{}`},
		{name: "empty value", field: toolPreviewField{"command", "bash"}, raw: `{"command":""}`},
		{name: "null value", field: toolPreviewField{"command", "bash"}, raw: `{"command":null}`},
		{name: "number value", field: toolPreviewField{"command", "bash"}, raw: `{"command":1}`},
		{name: "null document", field: toolPreviewField{"command", "bash"}, raw: `null`},
		{name: "string document", field: toolPreviewField{"command", "bash"}, raw: `"ls"`},
		{name: "empty document", field: toolPreviewField{"command", "bash"}, raw: ``},
		{name: "truncated document", field: toolPreviewField{"command", "bash"}, raw: `{"command":"ls"`},
		{name: "trailing data", field: toolPreviewField{"command", "bash"}, raw: `{"command":"ls"} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, rest, ok := tc.field.split(tc.raw)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantValue, value)
			assert.Equal(t, tc.wantRest, rest)
		})
	}
}

func TestToolCallPreviewRendersCodeLiterally(t *testing.T) {
	// The bash call from the bug report: comment lines in a heredoc were
	// rendered as headings and globs as emphasis.
	command := "cat > /tmp/chk.sh <<'EOF'\n#!/bin/bash\n# fast checks: build, vet both tags\n" +
		"gofmt -l $(git diff --name-only -- '*.go'; git diff --name-only -- '*.go')\n" +
		"printf '\\nCo-Authored-By: Rune Agent <agent@rune.build>\\n'\nEOF"
	commandJSON, err := json.Marshal(command)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		details dialoguetui.ToolCallDetails
		want    []string
	}{
		{
			name: "bash",
			details: dialoguetui.ToolCallDetails{
				Name: "bash", Summary: command,
				Args:   `{"command":` + string(commandJSON) + `,"description":"Write *check* script"}`,
				Output: "# heading?\n- list?\n```fence?```", Done: true,
			},
			want: []string{command, "{\n  \"description\": \"Write *check* script\"\n}", "# heading?\n- list?\n```fence?```"},
		},
		{
			name:    "generic summary",
			details: dialoguetui.ToolCallDetails{Name: "mcp__shell__run", Summary: command},
			want:    []string{command},
		},
		{
			name: "exec_command",
			details: dialoguetui.ToolCallDetails{
				Name: "exec_command", Args: `{"cmd":` + string(commandJSON) + `}`,
				Output: "{\"exit_code\":2,\"output\":\"# FAIL\\n````\\n*bad*\"}", Done: true, IsError: true,
			},
			want: []string{command, "# FAIL\n````\n*bad*", "{\n  \"exit_code\": 2\n}"},
		},
		{
			name: "apply_patch",
			details: dialoguetui.ToolCallDetails{
				Name: "apply_patch",
				Args: `{"patch":"*** Begin Patch\n*** Update File: a.md\n@@\n-# Old\n+## New\n+` + "```" + `\n*** End Patch"}`,
			},
			want: []string{"*** Begin Patch\n*** Update File: a.md\n@@\n-# Old\n+## New\n+```\n*** End Patch"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultMarkdownConfig()
			cfg.CodeBlockCopy = true
			cfg.CodeBlockPadding = markdown.Padding{Top: 1, Right: 2}
			md, err := markdown.NewWithConfig(toolCallPreviewMarkdown(tc.details), *cfg)
			require.NoError(t, err)
			md.Resize(200, 200)
			var got []string
			for _, target := range md.CodeBlockCopyTargets() {
				got = append(got, target.Code)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseAttachmentPartV1RejectsNonV1Text(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"plain text", "please look at the parser"},
		{"legacy file prefix", "Attached file pkg/main.go:\npackage main"},
		{"malformed json header", "Rune attachment v1: {not json"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := parseAttachmentPartV1(tc.text)
			assert.False(t, ok)
		})
	}
}

func TestParseAttachmentPartV1(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{
		{
			name: "workspace file",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			}, "package main"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
			wantOK: true,
		},
		{
			name: "absolute path keeps the dragged-file icon",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "notes.txt", Path: "/tmp/notes.txt", Status: attachmentStatusOK,
			}, "hi"),
			want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
			wantOK: true,
		},
		{
			name: "symbol",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			}, "## Definition\na.go:1"),
			want: dialoguetui.NewResolvedSymbolAttachment(
				"pkg.Symbol", "## Definition\na.go:1"),
			wantOK: true,
		},
		{
			name: "image",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			}, ""),
			want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
			wantOK: true,
		},
		{
			name: "unreadable file still yields a chip",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "gone.go", Path: "pkg/gone.go",
				Status: attachmentStatusError, Error: "no such file",
			}, "could not be read: no such file"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAttachmentContentPartsV1Roundtrip(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "main.go"), []byte("package main"), 0o600))

	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	sent := []dialoguetui.Attachment{
		dialoguetui.NewWorkspaceFileAttachment("main.go"),
		dialoguetui.NewResolvedSymbolAttachment(
			"value", "## Definition\n- `scope.go:3`"),
	}
	finalized := make([]finalizedAttachment, len(sent))
	for i, a := range sent {
		finalized[i] = finalizedAttachment{id: fmt.Sprintf("attachment-%d", i+1), Attachment: a}
	}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 2)
	for _, p := range parts {
		assert.Contains(t, p.Text, attachmentPartV1Prefix)
	}

	msg := llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: "look",
		MultiContent: append([]llmapi.ContentPart{{
			Type: llmapi.ContentPartTypeText, Text: "look",
		}}, parts...),
	}

	assert.Equal(t, sent, replayedAttachments(msg))
}

func TestAttachmentContentPartsV1Failures(t *testing.T) {
	root := t.TempDir()
	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	missing := dialoguetui.NewWorkspaceFileAttachment("gone.go")
	finalized := []finalizedAttachment{{id: "attachment-1", Attachment: missing}}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 1)
	require.Equal(t, llmapi.ContentPartTypeText, parts[0].Type)

	meta, _, ok := parseAttachmentPartV1(parts[0].Text)
	require.True(t, ok)
	assert.Equal(t, "attachment-1", meta.ID)
	assert.Equal(t, attachmentKindFile, meta.Kind)
	assert.Equal(t, attachmentStatusError, meta.Status)
	assert.NotEmpty(t, meta.Error)
}

func TestParseAttachmentPart(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{{
		name:   "workspace file",
		text:   "Attached file pkg/main.go:\npackage main",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
		wantOK: true,
	}, {
		name:   "absolute path keeps the dragged-file icon",
		text:   "Attached file /tmp/notes.txt:\nhi",
		want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
		wantOK: true,
	}, {
		name: "symbol",
		text: "Attached symbol pkg.Symbol:\n## Definition\na.go:1",
		want: dialoguetui.NewResolvedSymbolAttachment(
			"pkg.Symbol", "## Definition\na.go:1"),
		wantOK: true,
	}, {
		name:   "image",
		text:   "Attached image shot.png:",
		want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
		wantOK: true,
	}, {
		name:   "unreadable file still yields a chip",
		text:   "Attached file pkg/gone.go could not be read: no such file",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
		wantOK: true,
	}, {
		name: "ordinary text",
		text: "please look at the parser",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReplayedAttachmentsIgnoresPlainMessages(t *testing.T) {
	assert.Nil(t, replayedAttachments(llmapi.Message{
		Role: llmapi.RoleUser, Content: "Attached file foo.go: is what I meant",
	}))
}

type previewWindow uint64

func (w previewWindow) WindowID() uint64 { return uint64(w) }

// previewWindowManager closes windows like the browser does over RPC:
// the window goes away at once, but its handler's Close only runs later,
// when the test calls deliverCloses.
type previewWindowManager struct {
	browserapi.WindowManager
	nextID  uint64
	open    map[uint64]browserapi.Floating
	closing []browserapi.Floating
}

func (m *previewWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.nextID++
	m.open[m.nextID] = h
	return previewWindow(m.nextID), nil
}

func (m *previewWindowManager) CloseWindow(win browserapi.Window) error {
	h, ok := m.open[win.WindowID()]
	if !ok {
		return nil
	}
	delete(m.open, win.WindowID())
	m.closing = append(m.closing, h)
	return nil
}

func (m *previewWindowManager) deliverCloses(t *testing.T) {
	for len(m.closing) > 0 {
		h := m.closing[0]
		m.closing = m.closing[1:]
		require.NoError(t, h.Close())
	}
}

func (m *previewWindowManager) openIDs() []uint64 {
	return slices.Sorted(maps.Keys(m.open))
}
