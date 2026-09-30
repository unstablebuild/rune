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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/agentools/applypatch"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/workspace"
)

func TestReviewChanges(t *testing.T) {
	tests := []reviewCase{
		// --- transcript shape ---
		{
			name: "single applied update",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", updatePatch)),
				toolResult("c1", okResult),
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: foo.go\n" +
				"@@ func main\n" +
				" ctx\n" +
				"-old\n" +
				"+new\n",
			wantHeaders: []string{"apply_patch #1 [applied]"},
		},
		{
			name: "several calls in one assistant message",
			msgs: []llmapi.Message{
				assistant(
					patchCall(t, "c1", addPatch),
					patchCall(t, "c2", deletePatch),
				),
				toolResult("c1", okResult),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: new.go\n" +
				"+package main\n" +
				"+\n" +
				"\n" +
				"apply_patch #2 [applied]\n" +
				"*** Delete File: gone.go\n",
			wantHeaders: []string{
				"apply_patch #1 [applied]", "apply_patch #2 [applied]",
			},
		},
		{
			name: "calls across messages keep transcript order",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", okResult),
				{Role: llmapi.RoleUser, Content: "again"},
				assistant(patchCall(t, "c2", addPatch)),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Delete File: gone.go\n" +
				"\n" +
				"apply_patch #2 [applied]\n" +
				"*** Add File: new.go\n" +
				"+package main\n" +
				"+\n",
		},
		{
			name: "repeated path appears once per call",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", updatePatch)),
				toolResult("c1", okResult),
				assistant(patchCall(t, "c2", updatePatch)),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: foo.go\n@@ func main\n ctx\n-old\n+new\n" +
				"\n" +
				"apply_patch #2 [applied]\n" +
				"*** Update File: foo.go\n@@ func main\n ctx\n-old\n+new\n",
		},
		{
			name: "tool calls outside assistant messages are ignored",
			msgs: []llmapi.Message{
				{
					Role:      llmapi.RoleUser,
					ToolCalls: []llmapi.ToolCall{patchCall(t, "c0", addPatch)},
				},
				toolResult("c0", okResult),
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", okResult),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "other tools are skipped",
			msgs: []llmapi.Message{
				assistant(toolCall("c0", "read_file", `{"path":"foo.go"}`)),
				toolResult("c0", "contents"),
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", okResult),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "result recorded before the call still correlates",
			msgs: []llmapi.Message{
				toolResult("c1", okResult),
				assistant(patchCall(t, "c1", deletePatch)),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "the last result for an id wins",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", okResult),
				toolResult("c1", "interrupted"),
			},
			want: "apply_patch #1 [unknown]\n*** Delete File: gone.go\n",
		},
		{
			name: "result without an id cannot correlate",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("", okResult),
			},
			want: "apply_patch #1 [no result]\n*** Delete File: gone.go\n",
		},
		{
			name: "call without an id cannot correlate",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "", deletePatch)),
				toolResult("", okResult),
			},
			want: "apply_patch #1 [no result]\n*** Delete File: gone.go\n",
		},

		// --- outcome classification ---
		{
			name: "multi operation success",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", "applied 3/3 operations successfully"),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "result is trimmed before classification",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", "\n  "+okResult+"  \n"),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "partial failure is hidden",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", updatePatch)),
				toolResult("c1", failResult),
				assistant(patchCall(t, "c2", deletePatch)),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "error prefixed result is hidden",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", updatePatch)),
				toolResult("c1", "error: parse patch: boom"),
				assistant(patchCall(t, "c2", deletePatch)),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name: "missing result",
			msgs: []llmapi.Message{assistant(patchCall(t, "c1", deletePatch))},
			want: "apply_patch #1 [no result]\n*** Delete File: gone.go\n",
		},
		{
			name: "empty result content",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", ""),
			},
			want: "apply_patch #1 [unknown]\n*** Delete File: gone.go\n",
		},
		{
			name: "unrecognized result content",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", deletePatch)),
				toolResult("c1", "interrupted by user"),
			},
			want: "apply_patch #1 [unknown]\n*** Delete File: gone.go\n",
		},

		// --- operation kinds ---
		{
			name: "added file body renders as insertions",
			msgs: applied(t, addPatch),
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: new.go\n+package main\n+\n",
			wantSnippets: []string{"package main\n"},
		},
		{
			name: "added file with an empty body",
			msgs: applied(t, "*** Begin Patch\n*** Add File: empty.go\n"+
				"*** End Patch"),
			want:         "apply_patch #1 [applied]\n*** Add File: empty.go\n",
			wantSnippets: []string{},
		},
		{
			name:         "deletion is neutral metadata",
			msgs:         applied(t, deletePatch),
			want:         "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
			wantSnippets: []string{},
		},
		{
			name: "move precedes the hunks",
			msgs: applied(t, movePatch),
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: old.go\n*** Move to: moved.go\n@@\n-a\n+b\n",
		},
		{
			name: "add, update and delete in one patch",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Add File: a.go\n+package a\n"+
				"*** Update File: b.go\n@@\n ctx\n-x\n+y\n"+
				"*** Delete File: c.go\n"+
				"*** End Patch"),
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: a.go\n+package a\n" +
				"*** Update File: b.go\n@@\n ctx\n-x\n+y\n" +
				"*** Delete File: c.go\n",
			wantSnippets: []string{"package a", "ctx\ny", "ctx\nx"},
		},
		{
			name: "several hunks in one file",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@ first\n ctx1\n+one\n"+
				"@@ second\n ctx2\n+two\n"+
				"*** End Patch"),
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n" +
				"@@ first\n ctx1\n+one\n" +
				"@@ second\n ctx2\n+two\n",
			wantSnippets: []string{"ctx1\none", "ctx2\ntwo"},
		},
		{
			name: "hunk without a context hint",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n+one\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n@@\n+one\n",
		},
		// --- content fidelity ---
		{
			name: "tabs are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+\tif x {\n+\t\ty()\n+\t}\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n" +
				"+\tif x {\n+\t\ty()\n+\t}\n",
			wantSnippets: []string{"\tif x {\n\t\ty()\n\t}"},
		},
		{
			name: "wide characters are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Update File: 日本.go\n"+
				"@@ 関数\n 日本語テスト\n-你好\n+世界\n*** End Patch"),
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: 日本.go\n@@ 関数\n 日本語テスト\n-你好\n+世界\n",
			wantSnippets: []string{"日本語テスト\n世界", "日本語テスト\n你好"},
		},
		{
			name: "emoji are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+x := \"🎆🎆\"\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n+x := \"🎆🎆\"\n",
		},
		{
			name: "combining marks are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+e\u0301cole\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n+e\u0301cole\n",
		},
		{
			name: "nul and control bytes are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+a\x00b\n+c\x07d\x1be\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n" +
				"+a\x00b\n+c\x07d\x1be\n",
			wantSnippets: []string{"a\x00b\nc\x07d\x1be"},
		},
		{
			name: "format verbs are not interpreted",
			msgs: applied(t, "*** Begin Patch\n*** Add File: %s.go\n"+
				"+fmt.Printf(\"%d %!q(MISSING)\")\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: %s.go\n" +
				"+fmt.Printf(\"%d %!q(MISSING)\")\n",
		},
		{
			name: "trailing whitespace is preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+x   \n*** End Patch"),
			want:         "apply_patch #1 [applied]\n*** Add File: a.go\n+x   \n",
			wantSnippets: []string{"x   "},
		},
		{
			name: "carriage returns are preserved",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"+x\r\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n+x\r\n",
		},
		{
			name: "blank lines in a hunk are context",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n\n+added\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n ctx\n \n+added\n",
			wantSnippets: []string{"ctx\n\nadded"},
		},
		{
			name: "content that looks like diff markers",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n"+
				"++added\n+-removed\n+ ctx\n+@@ hunk\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n" +
				"++added\n+-removed\n+ ctx\n+@@ hunk\n",
			wantSnippets: []string{"+added\n-removed\n ctx\n@@ hunk"},
		},
		{
			name: "paths with spaces and unicode",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Add File: some dir/héllo wörld.go\n+package a\n"+
				"*** End Patch"),
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: some dir/héllo wörld.go\n+package a\n",
			wantSnippets: []string{"package a"},
		},
		{
			name: "a very long line is not truncated",
			msgs: applied(t, "*** Begin Patch\n*** Add File: a.go\n+"+
				strings.Repeat("x", 5000)+"\n*** End Patch"),
			want: "apply_patch #1 [applied]\n*** Add File: a.go\n+" +
				strings.Repeat("x", 5000) + "\n",
		},

		// --- malformed input ---
		{
			name: "malformed json arguments",
			msgs: []llmapi.Message{
				assistant(toolCall("c1", applyPatchToolName, "{not json")),
			},
			want: "apply_patch #1 [no result]\n" +
				"(invalid arguments: invalid character 'n' " +
				"looking for beginning of object key string)\n",
		},
		{
			name: "empty arguments",
			msgs: []llmapi.Message{
				assistant(toolCall("c1", applyPatchToolName, "")),
			},
			want: "apply_patch #1 [no result]\n" +
				"(invalid arguments: unexpected end of JSON input)\n",
		},
		{
			name: "arguments without a patch key",
			msgs: []llmapi.Message{
				assistant(toolCall("c1", applyPatchToolName, `{"other":1}`)),
			},
			want: "apply_patch #1 [no result]\n" +
				"(invalid patch: empty patch: no file operations found)\n",
		},
		{
			name: "empty patch payload",
			msgs: []llmapi.Message{assistant(patchCall(t, "c1", ""))},
			want: "apply_patch #1 [no result]\n" +
				"(invalid patch: empty patch: no file operations found)\n",
		},
		{
			name: "unparsable patch body",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", "*** Begin Patch\n"+
					"*** Update File: a.go\n@@\nno prefix\n*** End Patch")),
			},
			want: "apply_patch #1 [no result]\n" +
				"(invalid patch: unexpected diff line prefix " +
				"\"no prefix\" at line 4)\n",
		},
		{
			name: "one bad call does not hide the rest",
			msgs: []llmapi.Message{
				assistant(toolCall("c1", applyPatchToolName, "{not json")),
				assistant(patchCall(t, "c2", deletePatch)),
				toolResult("c2", okResult),
			},
			want: "apply_patch #1 [no result]\n" +
				"(invalid arguments: invalid character 'n' " +
				"looking for beginning of object key string)\n" +
				"\n" +
				"apply_patch #2 [applied]\n" +
				"*** Delete File: gone.go\n",
		},

		// --- snippets handed to the parser ---
		{
			name:         "each hunk side is parsed on its own",
			msgs:         applied(t, updatePatch),
			want:         "apply_patch #1 [applied]\n*** Update File: foo.go\n@@ func main\n ctx\n-old\n+new\n",
			wantSnippets: []string{"ctx\nnew", "ctx\nold"},
		},
		{
			name: "an add only hunk needs no pre-image",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n+added\n*** End Patch"),
			want:         "apply_patch #1 [applied]\n*** Update File: a.go\n@@\n ctx\n+added\n",
			wantSnippets: []string{"ctx\nadded"},
		},
		{
			name: "a remove only hunk keeps both sides",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n-gone\n*** End Patch"),
			want:         "apply_patch #1 [applied]\n*** Update File: a.go\n@@\n ctx\n-gone\n",
			wantSnippets: []string{"ctx", "ctx\ngone"},
		},

		// --- context expansion ---
		{
			name:  "expands around a hunk",
			msgs:  applied(t, serveUpdatePatch),
			files: map[string]string{"server.go": serveFile},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: server.go\n" +
				"@@\n" +
				" \n" +
				" import \"fmt\"\n" +
				" \n" +
				" func serve() {\n" +
				"-\told\n" +
				"+\tnew\n" +
				" }\n" +
				" \n" +
				" func other() {}\n",
		},
		{
			name: "clamps at both ends of the file",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n-old\n+new\n*** End Patch"),
			files: map[string]string{"a.go": "new\ntail\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n-old\n+new\n tail\n",
		},
		{
			name:  "no source means no expansion",
			msgs:  applied(t, serveUpdatePatch),
			files: nil,
			want: "apply_patch #1 [applied]\n*** Update File: server.go\n" +
				"@@\n func serve() {\n-\told\n+\tnew\n }\n",
		},
		{
			name: "an ambiguous anchor is left alone",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n+new\n*** End Patch"),
			files: map[string]string{"a.go": "ctx\nnew\nfiller\nctx\nnew\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n ctx\n+new\n",
		},
		{
			name: "expansion keeps tabs and wide characters",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n-旧\n+新\n*** End Patch"),
			files: map[string]string{
				"a.go": "\tlead 日本\n新\n\ttail 你好\n",
			},
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n \tlead 日本\n-旧\n+新\n \ttail 你好\n",
			wantSnippets: []string{
				"\tlead 日本\n新\n\ttail 你好",
				"\tlead 日本\n旧\n\ttail 你好",
			},
		},
		{
			name: "each hunk expands independently",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n+one\n"+
				"@@\n+two\n*** End Patch"),
			files: map[string]string{
				"a.go": "a\none\nb\nc\nd\ne\nf\ntwo\ng\n",
			},
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n a\n+one\n b\n c\n d\n" +
				"@@\n d\n e\n f\n+two\n g\n",
		},
		{
			name: "only files the source knows are expanded",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Update File: known.go\n@@\n+one\n"+
				"*** End Patch"),
			files: map[string]string{"known.go": "lead\none\ntail\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: known.go\n@@\n lead\n+one\n tail\n",
		},
		{
			name:  "added files are never expanded",
			msgs:  applied(t, addPatch),
			files: map[string]string{"new.go": "lead\npackage main\n\ntail\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: new.go\n+package main\n+\n",
		},
		{
			name: "a file read twice is expanded consistently",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Update File: a.go\n@@\n+one\n"+
				"*** Update File: a.go\n@@\n+two\n"+
				"*** End Patch"),
			files: map[string]string{"a.go": "lead\none\nmid\ntwo\ntail\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n lead\n+one\n mid\n two\n tail\n" +
				"*** Update File: a.go\n@@\n lead\n one\n mid\n+two\n tail\n",
		},

		// --- configurable context width ---
		{
			name:    "a narrow width shows fewer lines",
			msgs:    applied(t, serveUpdatePatch),
			files:   map[string]string{"server.go": serveFile},
			context: 1,
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: server.go\n@@\n" +
				" \n func serve() {\n-\told\n+\tnew\n }\n \n",
		},
		{
			name:    "a wide width clamps to the file",
			msgs:    applied(t, serveUpdatePatch),
			files:   map[string]string{"server.go": serveFile},
			context: 100,
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: server.go\n@@\n" +
				" package main\n \n import \"fmt\"\n \n" +
				" func serve() {\n-\told\n+\tnew\n }\n \n func other() {}\n",
		},

		// --- corroboration against the workspace ---
		//
		// A patch that reached the workspace can be undone afterwards,
		// by `git checkout --`, by a later tool call, or by the user.
		// Showing it would bury the changes that do still hold, so it
		// is dropped along with any heading left standing over nothing.
		{
			name: "a reverted hunk drops out",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n+kept\n"+
				"@@\n+undone\n*** End Patch"),
			files: map[string]string{"a.go": "kept\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n+kept\n",
		},
		{
			name: "a file whose every hunk is gone takes its heading",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Update File: kept.go\n@@\n+kept\n"+
				"*** Update File: reverted.go\n@@\n+undone\n"+
				"*** End Patch"),
			files: map[string]string{
				"kept.go":     "kept\n",
				"reverted.go": "the original line\n",
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: kept.go\n@@\n+kept\n",
		},
		{
			name: "a file that is no longer readable drops out",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Update File: kept.go\n@@\n+kept\n"+
				"*** Update File: deleted.go\n@@\n+undone\n"+
				"*** End Patch"),
			files: map[string]string{"kept.go": "kept\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: kept.go\n@@\n+kept\n",
		},
		{
			name: "an invocation left with nothing takes its header",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", "*** Begin Patch\n"+
					"*** Update File: a.go\n@@\n+undone\n*** End Patch")),
				toolResult("c1", okResult),
				assistant(patchCall(t, "c2", "*** Begin Patch\n"+
					"*** Update File: a.go\n@@\n+kept\n*** End Patch")),
				toolResult("c2", okResult),
			},
			files: map[string]string{"a.go": "kept\n"},
			// The survivor is renumbered: #N counts what is shown.
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n+kept\n",
		},
		{
			name: "an unrelated edit to the same file is not a revert",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n+added\n*** End Patch"),
			files: map[string]string{
				"a.go": "someone else's line\nctx\nadded\nand another\n",
			},
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n someone else's line\n ctx\n+added\n" +
				" and another\n",
		},
		{
			name: "a change that now appears twice is kept",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n+new\n*** End Patch"),
			files: map[string]string{"a.go": "ctx\nnew\nfiller\nctx\nnew\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: a.go\n@@\n ctx\n+new\n",
		},
		{
			name: "an undone deletion drops out",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n-gone\n"+
				"@@\n other\n+kept\n*** End Patch"),
			files: map[string]string{"a.go": "ctx\ngone\nother\nkept\n"},
			// The undone removal's lines survive as plain context of
			// the hunk that did hold, which is what the file now says.
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n ctx\n gone\n other\n+kept\n",
		},
		{
			name: "a deletion still in effect is kept",
			msgs: applied(t, "*** Begin Patch\n*** Update File: a.go\n"+
				"@@\n ctx\n-gone\n*** End Patch"),
			files: map[string]string{"a.go": "ctx\n"},
			want: "apply_patch #1 [applied]\n*** Update File: a.go\n" +
				"@@\n ctx\n-gone\n",
		},
		{
			name: "an added file that is gone drops out",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Add File: kept.go\n+package kept\n"+
				"*** Add File: gone.go\n+package gone\n"+
				"*** End Patch"),
			files: map[string]string{"kept.go": "package kept\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: kept.go\n+package kept\n",
		},
		{
			name:  "an added file still on disk is kept",
			msgs:  applied(t, addPatch),
			files: map[string]string{"new.go": "package main\n\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: new.go\n+package main\n+\n",
		},
		{
			name: "a restored deleted file drops out",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Delete File: gone.go\n"+
				"*** Delete File: back.go\n"+
				"*** End Patch"),
			files: map[string]string{"back.go": "package main\n"},
			want:  "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name:  "a file that stayed deleted is kept",
			msgs:  applied(t, deletePatch),
			files: map[string]string{"other.go": "package main\n"},
			want:  "apply_patch #1 [applied]\n*** Delete File: gone.go\n",
		},
		{
			name:  "a moved file is corroborated at its new path",
			msgs:  applied(t, movePatch),
			files: map[string]string{"moved.go": "lead\nb\ntail\n"},
			want: "apply_patch #1 [applied]\n" +
				"*** Update File: old.go\n*** Move to: moved.go\n" +
				"@@\n lead\n-a\n+b\n tail\n",
		},
		{
			name: "an undone move drops out",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Add File: kept.go\n+package kept\n"+
				"*** Update File: old.go\n*** Move to: moved.go\n@@\n-a\n+b\n"+
				"*** End Patch"),
			files: map[string]string{
				"kept.go": "package kept\n",
				// The move never took: the old path still holds a.
				"old.go": "a\n",
			},
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: kept.go\n+package kept\n",
		},
		{
			name: "without a workspace nothing is corroborated",
			msgs: applied(t, "*** Begin Patch\n"+
				"*** Add File: a.go\n+package a\n"+
				"*** Delete File: b.go\n"+
				"*** Update File: c.go\n@@\n-x\n+y\n"+
				"*** End Patch"),
			files: nil,
			want: "apply_patch #1 [applied]\n" +
				"*** Add File: a.go\n+package a\n" +
				"*** Delete File: b.go\n" +
				"*** Update File: c.go\n@@\n-x\n+y\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t) })
	}
}

// A review is only worth opening when something reached the workspace.
func TestReviewChangesNoAppliedChanges(t *testing.T) {
	tests := []reviewCase{
		{name: "empty conversation", wantErr: errNoAppliedChanges},
		{
			name: "no tool calls",
			msgs: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "hi"},
				{Role: llmapi.RoleAssistant, Content: "hello"},
			},
			wantErr: errNoAppliedChanges,
		},
		{
			name: "only other tools",
			msgs: []llmapi.Message{
				assistant(toolCall("c0", "bash", `{"command":"ls"}`)),
				toolResult("c0", "ok"),
			},
			wantErr: errNoAppliedChanges,
		},
		{
			name: "every patch failed",
			msgs: []llmapi.Message{
				assistant(patchCall(t, "c1", updatePatch)),
				toolResult("c1", failResult),
				assistant(patchCall(t, "c2", addPatch)),
				toolResult("c2", "error: parse patch: boom"),
			},
			wantErr: errNoAppliedChanges,
		},
		{
			name:    "a patch with no operations",
			msgs:    applied(t, "*** Begin Patch\n*** End Patch"),
			wantErr: errNoChangesRemain,
		},
		{
			name:    "the whole patch was reverted",
			msgs:    applied(t, serveUpdatePatch),
			files:   map[string]string{"server.go": "func serve() {\n\told\n}\n"},
			wantErr: errNoChangesRemain,
		},
		{
			name:    "the patched file is unreadable",
			msgs:    applied(t, serveUpdatePatch),
			files:   map[string]string{"other.go": serveFile},
			wantErr: errNoChangesRemain,
		},
		{
			name:    "the patched file was rewritten past recognition",
			msgs:    applied(t, serveUpdatePatch),
			files:   map[string]string{"server.go": "package main\n"},
			wantErr: errNoChangesRemain,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t) })
	}
}

// This is the production path that previously had no regression test:
// the real apply_patch tool mutates a git worktree, git restores the
// file out of band, and the review reads that restored file through
// workspaceSource. Feeding reviewChanges a hand-built map does not
// exercise path expansion, URI handling, or workspace FileSystem I/O.
func TestReviewChangesOmitsApplyPatchRevertedByGitCheckout(t *testing.T) {
	before, patch := bluectxRemovalFixture()
	dir, path, fs, cwd, applyTool := newGitPatchWorkspace(t, before)

	args := patchArgs(t, patch)
	result := applyTool.Execute(context.Background(), args)
	require.False(t, result.IsError, result.Content)
	require.NotContains(t, string(mustReadFile(t, path)),
		"Unstable Build LLC", "apply_patch must remove the header first")

	// This is intentionally not an apply_patch call and does not alter
	// the transcript. It models the user's real `git checkout --`.
	testgit.Run(t, dir, "checkout", "--", "bluectx/first.go")
	require.Equal(t, before, string(mustReadFile(t, path)))

	msgs := []llmapi.Message{
		assistant(toolCall("c1", applyPatchToolName, args)),
		toolResult("c1", result.Content),
	}
	review, err := reviewChanges(msgs, workspaceView{
		src:     workspaceSource(fs, cwd),
		context: defaultHunkContextLines,
	})
	assert.ErrorIs(t, err, errNoChangesRemain)
	assert.Empty(t, review.diffs)
}

func TestCommandAdapterOmitsApplyPatchRevertedByGitCheckout(t *testing.T) {
	before, patch := bluectxRemovalFixture()
	dir, path, fs, cwd, applyTool := newGitPatchWorkspace(t, before)
	args := patchArgs(t, patch)
	result := applyTool.Execute(context.Background(), args)
	require.False(t, result.IsError, result.Content)
	require.NotEqual(t, before, string(mustReadFile(t, path)))

	testgit.Run(t, dir, "checkout", "--", "bluectx/first.go")
	require.Equal(t, before, string(mustReadFile(t, path)))

	msgs := []llmapi.Message{
		assistant(toolCall("remove", applyPatchToolName, args)),
		toolResult("remove", result.Content),
	}
	a, wm, _, _ := newReviewAdapter(t, msgs)
	a.fs, a.cwd = fs, cwd

	_, err := a.HandleCommand(t.Context(), "reviewchanges", nil)
	assert.ErrorIs(t, err, errNoChangesRemain)
	assert.Empty(t, wm.floatings)
}

// The real failure had a later apply_patch restore what an earlier one
// removed. Looking at each call independently kept the restoration as a
// large positive diff even though the conversation's net change was
// zero. The final checkout is deliberately out of band, matching the
// user's workflow and ensuring the review is corroborated against disk.
func TestReviewChangesNetsInverseApplyPatchesAfterGitCheckout(t *testing.T) {
	before, removePatch := bluectxRemovalFixture()
	dir, path, fs, cwd, applyTool := newGitPatchWorkspace(t, before)

	removeArgs := patchArgs(t, removePatch)
	removed := applyTool.Execute(context.Background(), removeArgs)
	require.False(t, removed.IsError, removed.Content)

	restoreLicenseArgs := patchArgs(t, bluectxRestoreLicensePatch())
	restoredLicense := applyTool.Execute(context.Background(), restoreLicenseArgs)
	require.False(t, restoredLicense.IsError, restoredLicense.Content)
	restoreDocArgs := patchArgs(t, bluectxRestoreDocPatch())
	restoredDoc := applyTool.Execute(context.Background(), restoreDocArgs)
	require.False(t, restoredDoc.IsError, restoredDoc.Content)
	require.Equal(t, before, string(mustReadFile(t, path)))

	testgit.Run(t, dir, "checkout", "--", "bluectx/first.go")
	require.Equal(t, before, string(mustReadFile(t, path)))

	msgs := []llmapi.Message{
		assistant(toolCall("remove", applyPatchToolName, removeArgs)),
		toolResult("remove", removed.Content),
		assistant(toolCall("restore-license", applyPatchToolName, restoreLicenseArgs)),
		toolResult("restore-license", restoredLicense.Content),
		assistant(toolCall("restore-doc", applyPatchToolName, restoreDocArgs)),
		toolResult("restore-doc", restoredDoc.Content),
	}
	review, err := reviewChanges(msgs, workspaceView{
		src:     workspaceSource(fs, cwd),
		context: defaultHunkContextLines,
	})
	assert.ErrorIs(t, err, errNoChangesRemain)
	assert.Empty(t, review.diffs)
}

func TestCommandAdapterOmitsInverseApplyPatchesAfterGitCheckout(t *testing.T) {
	before, removePatch := bluectxRemovalFixture()
	dir, path, fs, cwd, applyTool := newGitPatchWorkspace(t, before)
	removeArgs := patchArgs(t, removePatch)
	removed := applyTool.Execute(context.Background(), removeArgs)
	require.False(t, removed.IsError, removed.Content)
	restoreLicenseArgs := patchArgs(t, bluectxRestoreLicensePatch())
	restoredLicense := applyTool.Execute(context.Background(), restoreLicenseArgs)
	require.False(t, restoredLicense.IsError, restoredLicense.Content)
	restoreDocArgs := patchArgs(t, bluectxRestoreDocPatch())
	restoredDoc := applyTool.Execute(context.Background(), restoreDocArgs)
	require.False(t, restoredDoc.IsError, restoredDoc.Content)
	testgit.Run(t, dir, "checkout", "--", "bluectx/first.go")
	require.Equal(t, before, string(mustReadFile(t, path)))

	msgs := []llmapi.Message{
		assistant(toolCall("remove", applyPatchToolName, removeArgs)),
		toolResult("remove", removed.Content),
		assistant(toolCall("restore-license", applyPatchToolName, restoreLicenseArgs)),
		toolResult("restore-license", restoredLicense.Content),
		assistant(toolCall("restore-doc", applyPatchToolName, restoreDocArgs)),
		toolResult("restore-doc", restoredDoc.Content),
	}
	a, wm, _, _ := newReviewAdapter(t, msgs)
	a.fs, a.cwd = fs, cwd

	_, err := a.HandleCommand(t.Context(), "reviewchanges", nil)
	assert.ErrorIs(t, err, errNoChangesRemain)
	assert.Empty(t, wm.floatings)
}

func TestCancelInverseHunks(t *testing.T) {
	update := func(path, anchor string, before, after []string) applypatch.FileOp {
		lines := make([]applypatch.Line, 0, len(before)+len(after))
		if anchor != "" {
			lines = append(lines,
				applypatch.Line{Kind: applypatch.LineContext, Content: anchor})
		}
		for _, line := range before {
			lines = append(lines,
				applypatch.Line{Kind: applypatch.LineRemove, Content: line})
		}
		for _, line := range after {
			lines = append(lines,
				applypatch.Line{Kind: applypatch.LineAdd, Content: line})
		}
		return applypatch.FileOp{
			Type: applypatch.OpUpdate, Path: path,
			Hunks: []applypatch.Hunk{{Lines: lines}},
		}
	}
	tests := []struct {
		name string
		ops  [][]applypatch.FileOp
		file string
		want []int
	}{
		{
			name: "exact inverse cancels",
			ops: [][]applypatch.FileOp{
				{update("a.go", "func f()", []string{"old"}, []string{"new"})},
				{update("a.go", "func f()", []string{"new"}, []string{"old"})},
			},
			file: "func f()\nold\n",
			want: []int{0, 0},
		},
		{
			name: "another path cannot cancel",
			ops: [][]applypatch.FileOp{
				{update("a.go", "func f()", []string{"old"}, []string{"new"})},
				{update("b.go", "func f()", []string{"new"}, []string{"old"})},
			},
			file: "func f()\nold\n",
			want: []int{1, 1},
		},
		{
			name: "another anchor in the same file cannot cancel",
			ops: [][]applypatch.FileOp{
				{update("a.go", "func first()", []string{"old"}, []string{"new"})},
				{update("a.go", "func second()", []string{"new"}, []string{"old"})},
			},
			file: "func first()\nold\nfunc second()\nold\n",
			want: []int{1, 1},
		},
		{
			name: "a repeated shared anchor cannot cancel",
			ops: [][]applypatch.FileOp{
				{update("a.go", "}", []string{"old"}, []string{"new"})},
				{update("a.go", "}", []string{"new"}, []string{"old"})},
			},
			file: "}\nold\n}\nold\n",
			want: []int{1, 1},
		},
		{
			name: "a later rewrite cannot cancel",
			ops: [][]applypatch.FileOp{
				{update("a.go", "func f()", []string{"old"}, []string{"new"})},
				{update("a.go", "func f()", []string{"new"}, []string{"newer"})},
			},
			file: "func f()\nnewer\n",
			want: []int{1, 1},
		},
		{
			name: "nearest matching inverse cancels",
			ops: [][]applypatch.FileOp{
				{update("a.go", "func f()", []string{"old"}, []string{"new"})},
				{update("a.go", "func f()", []string{"other"}, []string{"third"})},
				{update("a.go", "func f()", []string{"new"}, []string{"old"})},
			},
			file: "func f()\nold\n",
			want: []int{0, 1, 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invocations := make([]reviewInvocation, len(tt.ops))
			for i, ops := range tt.ops {
				invocations[i].ops = ops
			}
			cancelInverseHunks(invocations, workspaceView{
				src: fileSource(map[string]string{"a.go": tt.file}),
			})
			got := make([]int, len(invocations))
			for i := range invocations {
				got[i] = len(invocations[i].ops)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func newGitPatchWorkspace(
	t *testing.T, before string,
) (string, string, workspaceapi.FileSystem, workspaceapi.URI, agent.Tool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bluectx", "first.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(before), 0o600))
	testgit.Run(t, dir, "init", "-q")
	testgit.Run(t, dir, "add", "bluectx/first.go")
	testgit.Run(t, dir, "commit", "-qm", "baseline")

	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(t.Context(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, scheme.Close()) })
	cwd, err := scheme.URI(".")
	require.NoError(t, err)
	tools, _ := agentools.DefaultTools(
		scheme, nil, cwd, watchedFilesLSP{}, nil,
		agentools.Config{}, configedit.NopConfig())
	for _, tool := range tools {
		if tool.Definition().Function.Name == applyPatchToolName {
			return dir, path, scheme, cwd, tool
		}
	}
	require.FailNow(t, "apply_patch tool is not registered")
	return "", "", nil, workspaceapi.URI{}, nil
}

// git exports GIT_DIR and GIT_INDEX_FILE to hook subprocesses without
// GIT_WORK_TREE, and those override cmd.Dir. When the commit comes from
// a linked worktree, GIT_DIR is that worktree's admin directory, so a
// fixture inheriting it runs `git init` against a repository git sees
// as having no work tree: it writes core.bare=true into the *shared*
// config and every worktree on the machine stops working.
func TestGitPatchWorkspaceIgnoresHookGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	hookRepo := t.TempDir()
	testgit.Run(t, hookRepo, "init", "-q")
	testgit.Run(t, hookRepo, "commit", "--allow-empty", "-m", "victim", "-q")
	hookWorktree := filepath.Join(t.TempDir(), "linked")
	testgit.Run(t, hookRepo, "worktree", "add", "-q", hookWorktree, "-b", "linked")

	adminDir := filepath.Join(hookRepo, ".git", "worktrees", "linked")
	t.Setenv("GIT_DIR", adminDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(adminDir, "index"))

	dir, _, _, _, _ := newGitPatchWorkspace(t, "package bluectx\n")

	_, err := os.Stat(filepath.Join(dir, ".git"))
	require.NoError(t, err, "the repo must be created in the fixture's dir")

	bare := strings.TrimSpace(string(testgit.Run(t, hookRepo,
		"rev-parse", "--is-bare-repository")))
	assert.Equal(t, "false", bare, "the hook repo must not be re-initialized")
	log := strings.TrimSpace(string(testgit.Run(t, hookRepo,
		"log", "--format=%s")))
	assert.Equal(t, "victim", log,
		"the hook repo must not receive the fixture's history")
}

// watchedFilesLSP supplies the one callback apply_patch invokes after a
// successful mutation. Embedding keeps this fixture focused on the real
// tool's filesystem behavior instead of implementing unrelated LSP calls.
type watchedFilesLSP struct{ semanticapi.LSP }

func (watchedFilesLSP) DidChangeWatchedFiles(
	context.Context, semanticapi.DidChangeWatchedFilesParams,
) error {
	return nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func bluectxRemovalFixture() (before, patch string) {
	license := []string{
		`// Unstable Build LLC ("COMPANY") CONFIDENTIAL`,
		"//",
		"// Unpublished Copyright (c) 2018-2024 Unstable Build, All Rights Reserved.",
		"//",
		"// NOTICE: All information contained herein is, and remains the property of COMPANY.",
		"// The intellectual and technical concepts contained herein are proprietary to",
		"// COMPANY and may be covered by U.S. and Foreign Patents, patents in process,",
		"// and are protected by trade secret or copyright law. Dissemination of this information",
		"// or reproduction of this material is strictly forbidden unless prior written permission",
		"// is obtained from COMPANY. Access to the source code contained herein is hereby",
		"// forbidden to anyone except current COMPANY employees, managers or contractors who",
		"// have executed Confidentiality and Non-disclosure agreements explicitly covering such access.",
		"//",
		"// The copyright notice above does not evidence any actual or intended publication or",
		"// disclosure of this source code, which includes information that is confidential and/or",
		"// proprietary, and is a trade secret, of COMPANY. ANY REPRODUCTION, MODIFICATION,",
		"// DISTRIBUTION, PUBLIC  PERFORMANCE, OR PUBLIC DISPLAY OF OR THROUGH USE OF THIS SOURCE CODE",
		"// WITHOUT  THE EXPRESS WRITTEN CONSENT OF COMPANY IS STRICTLY PROHIBITED, AND IN",
		"// VIOLATION OF APPLICABLE LAWS AND INTERNATIONAL TREATIES. THE RECEIPT OR POSSESSION OF",
		"// THIS SOURCE CODE AND/OR RELATED INFORMATION DOES NOT CONVEY OR IMPLY ANY RIGHTS TO",
		"// REPRODUCE, DISCLOSE OR DISTRIBUTE ITS CONTENTS, OR TO MANUFACTURE, USE, OR SELL",
		"// ANYTHING THAT IT MAY DESCRIBE, IN WHOLE OR IN PART.",
		"",
	}
	doc := []string{
		"// First returns a new context that combines parent with ctxs.",
		"// The returned context's Done channel is closed the first Done channel",
		"// of any of the passed ctx closes first. Deadline returns the",
		"// earliest of deadlines if there's any set. Value returns the first value",
		"// found, following the passed order, for the given key.",
		"// Callers must make sure that the returned cancel function is eventually called.",
	}
	beforeLines := append([]string{}, license...)
	beforeLines = append(beforeLines,
		"package bluectx", "", `import "context"`, "")
	beforeLines = append(beforeLines, doc...)
	beforeLines = append(beforeLines,
		"func First(parent context.Context, ctxs ...context.Context) (",
		"\tcontext.Context, context.CancelFunc,", ") {",
		"\treturn parent, func() {}", "}", "")

	var b strings.Builder
	b.WriteString("*** Begin Patch\n*** Update File: bluectx/first.go\n@@\n")
	for _, line := range license {
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(" package bluectx\n@@\n")
	for _, line := range doc {
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(" func First(parent context.Context, ctxs ...context.Context) (\n")
	b.WriteString("*** End Patch")
	return strings.Join(beforeLines, "\n"), b.String()
}

func bluectxRestoreLicensePatch() string {
	before, _ := bluectxRemovalFixture()
	lines := strings.Split(before, "\n")
	packageAt := slices.Index(lines, "package bluectx")
	var b strings.Builder
	b.WriteString("*** Begin Patch\n*** Update File: bluectx/first.go\n@@\n")
	for _, line := range lines[:packageAt] {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(" package bluectx\n*** End Patch")
	return b.String()
}

func bluectxRestoreDocPatch() string {
	before, _ := bluectxRemovalFixture()
	lines := strings.Split(before, "\n")
	funcAt := slices.Index(lines,
		"func First(parent context.Context, ctxs ...context.Context) (")
	var b strings.Builder
	b.WriteString("*** Begin Patch\n*** Update File: bluectx/first.go\n@@\n")
	b.WriteString(" import \"context\"\n \n")
	for _, line := range lines[funcAt-6 : funcAt] {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(" func First(parent context.Context, ctxs ...context.Context) (\n")
	b.WriteString("*** End Patch")
	return b.String()
}

// The invocation header is emphasised so call boundaries stay visible
// while scrolling a long review; its diff lines keep their own styling.
func TestReviewBufferBoldsInvocationHeaders(t *testing.T) {
	review, err := reviewChanges([]llmapi.Message{
		assistant(patchCall(t, "c1", updatePatch)),
		toolResult("c1", okResult),
		assistant(patchCall(t, "c2", deletePatch)),
		toolResult("c2", okResult),
	}, workspaceView{})
	require.NoError(t, err)
	require.Len(t, review.headers, 2)

	buf := reviewBuffer(t.Context(), review, nil)
	require.Equal(t, vctrl.DiffString(review.diffs), buf.String())

	for _, h := range review.headers {
		for x := range len([]rune(h.text)) {
			c, ok := buf.Cell(term.Coordinates{X: x, Y: h.row})
			require.Truef(t, ok, "no cell at %d,%d", x, h.row)
			assert.NotZerof(t, c.Attrs&term.AttrBold,
				"header cell %d,%d (%q) must be bold", x, h.row, string(c.Ch))
		}
	}

	plain, ok := buf.Cell(term.Coordinates{Y: review.headers[0].row + 1})
	require.True(t, ok)
	assert.Zero(t, plain.Attrs&term.AttrBold)
}

// review_context_lines is user input, so every shape a config can hold
// has to resolve to a width expansion can slice with.
func TestResolveReviewContextLines(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{name: "unset", cfg: map[string]any{}, want: defaultHunkContextLines},
		{
			name: "explicit width",
			cfg:  map[string]any{"review_context_lines": 10},
			want: 10,
		},
		{
			name: "zero disables expansion",
			cfg:  map[string]any{"review_context_lines": 0},
			want: 0,
		},
		{
			name: "negative is clamped",
			cfg:  map[string]any{"review_context_lines": -5},
			want: 0,
		},
		{
			name: "wrong type falls back to the default",
			cfg:  map[string]any{"review_context_lines": "three"},
			want: defaultHunkContextLines,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveReviewContextLines(config.MapConfig(tt.cfg))
			assert.Equal(t, tt.want, got)
		})
	}
}

// review_context_lines reaches expansion as a plain width. Zero turns
// expansion off, and a negative width must not index past the anchor.
func TestWorkspaceViewContextWidth(t *testing.T) {
	lines := []applypatch.Line{
		{Kind: applypatch.LineRemove, Content: "old"},
		{Kind: applypatch.LineAdd, Content: "new"},
	}
	src := fileSource(map[string]string{"a.go": "lead\nnew\ntail\n"})

	tests := []struct {
		name    string
		view    workspaceView
		wantPad bool
	}{
		{name: "no source", view: workspaceView{context: 3}},
		{name: "zero width", view: workspaceView{src: src}},
		{name: "negative width", view: workspaceView{src: src, context: -1}},
		{
			name:    "positive width",
			view:    workspaceView{src: src, context: 1},
			wantPad: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := tt.view.reviewHunk("a.go", lines)
			if !tt.wantPad {
				assert.Equal(t, lines, got)
				return
			}
			assert.Equal(t, []applypatch.Line{
				{Kind: applypatch.LineContext, Content: "lead"},
				{Kind: applypatch.LineRemove, Content: "old"},
				{Kind: applypatch.LineAdd, Content: "new"},
				{Kind: applypatch.LineContext, Content: "tail"},
			}, got)
		})
	}
}

func TestSplitSourceLines(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "empty", content: "", want: nil},
		{name: "single newline", content: "\n", want: []string{""}},
		{name: "no trailing newline", content: "a\nb", want: []string{"a", "b"}},
		{name: "trailing newline", content: "a\nb\n", want: []string{"a", "b"}},
		{
			name:    "blank line before eof",
			content: "a\n\n",
			want:    []string{"a", ""},
		},
		{name: "crlf is content", content: "a\r\n", want: []string{"a\r"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSourceLines(tt.content)
			if len(tt.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------

const (
	okResult   = "applied 1/1 operations successfully"
	failResult = "applied 0/1 operations; errors:\nfoo.go: context not found"

	updatePatch = "*** Begin Patch\n" +
		"*** Update File: foo.go\n" +
		"@@ func main\n" +
		" ctx\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch"
	addPatch = "*** Begin Patch\n" +
		"*** Add File: new.go\n" +
		"+package main\n" +
		"+\n" +
		"*** End Patch"
	deletePatch = "*** Begin Patch\n" +
		"*** Delete File: gone.go\n" +
		"*** End Patch"
	movePatch = "*** Begin Patch\n" +
		"*** Update File: old.go\n" +
		"*** Move to: moved.go\n" +
		"@@\n" +
		"-a\n" +
		"+b\n" +
		"*** End Patch"

	serveFile = "package main\n" +
		"\n" +
		"import \"fmt\"\n" +
		"\n" +
		"func serve() {\n" +
		"\tnew\n" +
		"}\n" +
		"\n" +
		"func other() {}\n"
	serveUpdatePatch = "*** Begin Patch\n" +
		"*** Update File: server.go\n" +
		"@@\n" +
		" func serve() {\n" +
		"-\told\n" +
		"+\tnew\n" +
		" }\n" +
		"*** End Patch"
)

// reviewCase drives reviewChanges over one transcript. want is the
// rendered aggregate diff; wantSnippets and wantHeaders are checked only
// when set, so a case can focus on the dimension it exercises.
type reviewCase struct {
	name         string
	msgs         []llmapi.Message
	files        map[string]string
	context      int
	want         string
	wantSnippets []string
	wantHeaders  []string
	wantErr      error
}

func (tc reviewCase) run(t *testing.T) {
	t.Helper()
	exp := workspaceView{context: defaultHunkContextLines}
	if tc.context != 0 {
		exp.context = tc.context
	}
	if tc.files != nil {
		exp.src = fileSource(tc.files)
	}
	review, err := reviewChanges(tc.msgs, exp)
	if tc.wantErr != nil {
		assert.ErrorIs(t, err, tc.wantErr)
		assert.Empty(t, review.diffs)
		assert.Empty(t, review.snippets)
		assert.Empty(t, review.headers)
		return
	}
	require.NoError(t, err)

	rendered := vctrl.DiffString(review.diffs)
	assert.Equal(t, tc.want, rendered)
	if tc.wantSnippets != nil {
		got := make([]string, 0, len(review.snippets))
		for _, s := range review.snippets {
			got = append(got, s.text)
		}
		assert.Equal(t, tc.wantSnippets, got)
	}
	if tc.wantHeaders != nil {
		got := make([]string, 0, len(review.headers))
		for _, h := range review.headers {
			got = append(got, h.text)
		}
		assert.Equal(t, tc.wantHeaders, got)
	}
	assertRowsAddressRenderedLines(t, review, rendered)
}

// assertRowsAddressRenderedLines checks the invariant every consumer of
// a review depends on: recorded rows address the lines they describe,
// and a snippet line sits behind exactly one gutter column. Highlight
// coordinates and header emphasis are both applied through these rows,
// so a drift here misplaces styling.
func assertRowsAddressRenderedLines(
	t *testing.T, review changesReview, rendered string,
) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	for _, h := range review.headers {
		require.Less(t, h.row, len(lines))
		assert.Equal(t, h.text, lines[h.row])
	}
	for _, s := range review.snippets {
		source := strings.Split(s.text, "\n")
		require.Len(t, s.rows, len(source))
		for i, want := range source {
			row := s.rows[i]
			require.Less(t, row, len(lines))
			got := lines[row]
			require.NotEmptyf(t, got, "row %d must hold a gutter", row)
			assert.Containsf(t, " +-", got[:1], "row %d gutter", row)
			assert.Equalf(t, want, got[1:], "row %d content", row)
		}
	}
}

func fileSource(files map[string]string) currentSource {
	return func(path string) ([]string, bool) {
		content, ok := files[path]
		if !ok {
			return nil, false
		}
		return splitSourceLines(content), true
	}
}

// expandFrom expands hunks against an in-memory workspace using the
// default context width.
func expandFrom(files map[string]string) workspaceView {
	return workspaceView{
		src: fileSource(files), context: defaultHunkContextLines,
	}
}

// applied is the common shape: one apply_patch call that succeeded.
func applied(t *testing.T, patch string) []llmapi.Message {
	t.Helper()
	return []llmapi.Message{
		assistant(patchCall(t, "c1", patch)),
		toolResult("c1", okResult),
	}
}

func patchArgs(t *testing.T, patch string) string {
	t.Helper()
	b, err := json.Marshal(reviewPatchArgs{Patch: patch})
	require.NoError(t, err)
	return string(b)
}

func patchCall(t *testing.T, id, patch string) llmapi.ToolCall {
	t.Helper()
	return toolCall(id, applyPatchToolName, patchArgs(t, patch))
}

func toolCall(id, name, args string) llmapi.ToolCall {
	return llmapi.ToolCall{
		ID:       id,
		Type:     llmapi.ToolTypeFunction,
		Function: llmapi.FunctionCall{Name: name, Arguments: args},
	}
}

func assistant(calls ...llmapi.ToolCall) llmapi.Message {
	return llmapi.Message{Role: llmapi.RoleAssistant, ToolCalls: calls}
}

func toolResult(id, content string) llmapi.Message {
	return llmapi.Message{
		Role: llmapi.RoleTool, ToolCallID: id,
		Name: applyPatchToolName, Content: content,
	}
}
