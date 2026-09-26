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
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
)

func TestCompleteChatAddSymbolUsesReferencedSymbols(t *testing.T) {
	h := &aiEditorHandler{parser: &symbolsParser{
		symbols: []string{"pkg.A", "pkg.B", "pkg.A"},
	}}

	it, err := h.Complete(t.Context(), commandAddSymbol, []string{"pkg"})
	require.NoError(t, err)
	defer func() { require.NoError(t, it.Close()) }()

	var got []string
	for {
		symbol, ok := it.Next(t.Context())
		if !ok {
			break
		}
		got = append(got, symbol)
	}
	require.NoError(t, it.Err())
	assert.Equal(t, []string{"pkg.A", "pkg.B"}, got)
}

// TestOpenChatTabUsesDialogueIDAsLabel verifies that the visible tab name is
// the dialogue's petname ID (e.g. "rolling-fox") and not the internal
// "rune-agent://<model>/<id>" URI.
func TestOpenChatTabUsesDialogueIDAsLabel(t *testing.T) {
	const dialogueID = "rolling-fox"
	uri, err := dialoguemanager.TabURI(dialogueID, "gpt-5")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(uri.String(), "rune-agent://"),
		"precondition: URI must use the rune-agent scheme")

	wm := &recordingWindowManager{}
	_, err = openChatTab(wm, uri, dialogueID, nil)
	require.NoError(t, err)

	assert.Equal(t, dialogueID, wm.gotName,
		"visible tab label must be the dialogue ID, not the internal URI")
	assert.False(t, strings.HasPrefix(wm.gotName, "rune-agent://"),
		"tab label must not be the internal rune-agent:// URI")
	assert.Equal(t, uri, wm.gotURI, "URI must still be passed unchanged as tab identity")
}

func TestRetitleOpenChatUsesOpenTabURI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "RUNE-256"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	store := newMemDialogueStore()
	// The stored model differs from the one used to open the chat: the
	// retitle must not rebuild the tab URI from d.Model.
	require.NoError(t, store.Create(ctx, dialoguemanager.Dialogue{
		ID: dialogueID, Model: "stored-model",
	}))
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  store,
		wm:             wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	t.Cleanup(func() {
		if wm.gotHandler != nil {
			require.NoError(t, wm.gotHandler.Close())
		}
	})
	require.NoError(t, store.SetTitle(ctx, dialogueID, "new title"))

	h.retitleOpenChat(ctx, dialogueID)

	require.Len(t, wm.Renames(), 1, "the open chat tab must be relabeled")
	assert.Equal(t, "rune-agent://test-model/"+dialogueID,
		wm.Renames()[0].uri.String(),
		"the URI of the open tab, not one rebuilt from the stored model")
	assert.Equal(t, "new title", wm.Renames()[0].name)
}

func TestChatRenameCommandRetitlesOpenTab(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "RUNE-256"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	store := newMemDialogueStore()
	require.NoError(t, store.Create(ctx, dialoguemanager.Dialogue{
		ID: dialogueID, Model: "test-model",
	}))
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  store,
		wm:             wm,
		p:              term.FuncInterrupter(func(context.Context) error { return nil }),
		n:              stubNotifications{},
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	t.Cleanup(func() {
		if wm.gotHandler != nil {
			require.NoError(t, wm.gotHandler.Close())
		}
	})

	require.NoError(t, h.HandleCommand(ctx, textapi.Command{
		Name: commandRename,
		Args: []string{"workspace", "scan", "loop", "fix"},
		URI:  wm.gotURI,
	}))

	require.Eventually(t, func() bool {
		renames := wm.Renames()
		return len(renames) == 1 && renames[0].name == "workspace scan loop fix"
	}, 2*time.Second, 10*time.Millisecond, "chatrename must relabel the open tab")
	assert.Equal(t, wm.gotURI, wm.Renames()[0].uri)
}

func TestHandleChatRejectsAlreadyOpenDialogue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "RUNE-256"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  newMemDialogueStore(),
		wm:             wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	require.NotNil(t, wm.gotHandler)
	t.Cleanup(func() {
		require.NoError(t, wm.gotHandler.Close())
		wm.gotHandler = nil
	})
	assert.Equal(t, dialogueID, wm.gotName)

	err := h.handleChat(textapi.Command{Args: []string{dialogueID}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent chat "RUNE-256" is already open`)
}

// A chat's turns mark the tab it was opened in, so the status component
// must carry the same URI the tab was created with.
func TestHandleChatTracksTabURIForActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "rolling-fox"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  newMemDialogueStore(),
		wm:             wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	require.NotNil(t, wm.gotHandler)
	t.Cleanup(func() { require.NoError(t, wm.gotHandler.Close()) })

	v, ok := h.openChats.Load(dialogueID)
	require.True(t, ok)
	assert.Equal(t, wm.gotURI, v.(syncComponent).uri)
}

// A restored workspace reopens a chat tab through OpenResource, which must
// return the dialogue the tab URI names, known by that very URI, and leave
// the tab to the host: it shows the content where it keeps the tab.
func TestOpenResourceResumesChat(t *testing.T) {
	const dialogueID = "rolling-fox"
	chatURI := func(model string) workspaceapi.URI {
		uri, err := getModelUri(dialogueID, model)
		require.NoError(t, err)
		return uri
	}
	tests := []struct {
		name      string
		uri       string
		stored    *dialoguemanager.Dialogue
		wantErr   string
		wantModel string
	}{
		{
			name:    "other scheme",
			uri:     "file:///rolling-fox",
			wantErr: "is not an agent chat",
		},
		{
			name:    "no dialogue id",
			uri:     "rune-agent://test-model",
			wantErr: "is not an agent chat",
		},
		{
			name:      "dialogue that was never stored uses the default model",
			uri:       chatURI("other-model").String(),
			wantModel: "test-model",
		},
		{
			name:      "stored model is resumed",
			uri:       chatURI("test-model").String(),
			stored:    &dialoguemanager.Dialogue{ID: dialogueID, Model: "other-model"},
			wantModel: "other-model",
		},
		{
			name:      "unavailable stored model falls back to the default",
			uri:       chatURI("gone-model").String(),
			stored:    &dialoguemanager.Dialogue{ID: dialogueID, Model: "gone-model"},
			wantModel: "test-model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			store := newMemDialogueStore()
			if tt.stored != nil {
				require.NoError(t, store.Create(ctx, *tt.stored))
			}
			svc := llmtest.New([]llmapi.ModelEntry{
				{Provider: "test", Name: "test-model"},
				{Provider: "test", Name: "other-model"},
			})
			wm := &recordingWindowManager{}
			fs := nopFileSystem{}
			h := &aiEditorHandler{
				ctx:            ctx,
				llmSvc:         svc,
				defaultModel:   "test-model",
				dialogueStore:  store,
				wm:             wm,
				n:              stubNotifications{},
				p:              term.NopInterrupter(),
				config:         configedit.NopConfig(),
				skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
				toolRegistry:   agent.NewRegistry(),
				agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
				cwd:            dirURI(""),
				fs:             fs,
				memoryDataPath: t.TempDir(),
			}
			uri, err := workspaceapi.ParseURI(tt.uri)
			require.NoError(t, err)

			content, err := h.OpenResource(ctx, uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, content)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, content)
			assert.Nil(t, wm.gotHandler, "the tab is the host's, not the extension's")
			assert.Zero(t, wm.setContentCalls, "placement is the host's")
			v, ok := h.openChats.Load(dialogueID)
			require.True(t, ok)
			assert.Equal(t, uri, v.(syncComponent).uri,
				"the chat must go by the URI the host asked for, whatever the model")
			a, ok := h.openChatAgents.Load(dialogueID)
			require.True(t, ok)
			assert.Equal(t, tt.wantModel, a.(*agent.Agent).Model())

			_, err = h.OpenResource(ctx, uri)
			require.ErrorContains(t, err, "is already open")

			require.NoError(t, content.Close())
			_, ok = h.openChats.Load(dialogueID)
			assert.False(t, ok, "closing the content must end the chat")
		})
	}
}

// TestE2ECtrlCDismissesSelectionPrompt drives the chat tab handler
// end-to-end through the public handlertest.RunHandlerSequence API:
// the user types "hi" and presses Enter, the scripted LLM emits an
// ask_user_question tool call which renders a selection prompt, and
// Ctrl-C must clear that prompt. The two cases compare the rendered
// frame before and after Ctrl-C, with no access to private dialogue
// component state.
func TestE2ECtrlCDismissesSelectionPrompt(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "Pick one", "header": "Choice",
			"options": []map[string]any{
				{"label": "A", "description": ""},
				{"label": "B", "description": ""},
			},
			"multiSelect": false,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Submit the user message; the agent loop emits the
			// tool call and the selection prompt becomes visible.
			InputSequence: "hi<enter>",
			Expected: frame(
				"hi",
				"Pick one                        [Choice]",
				blanks(),
				"> A",
				"  B",
				"  Other",
				"      None of the above",
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Ctrl-C through the wrapped chat handler must clear
			// the prompt and return focus to the empty input box.
			InputSequence: "<c-c>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

// TestE2EMultiSelectSpaceToggles drives a multiSelect ask_user_question
// prompt through handlertest.RunHandlerSequence, whose <space> token
// arrives as Key=KeySpace with Ch=0, the shape input backends deliver
// it in. Enter with nothing checked must leave the prompt open (a nil
// result would be reported to the agent as a dismissal), <space> must
// tick the checkbox under the cursor, and Enter must then submit.
func TestE2EMultiSelectSpaceToggles(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "Pick some", "header": "Choice",
			"options": []map[string]any{
				{"label": "A", "description": ""},
				{"label": "B", "description": ""},
			},
			"multiSelect": true,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	unchecked := frame(
		"hi",
		"Pick some                       [Choice]",
		blanks(),
		">[ ] A",
		"[ ] B",
		"[ ] Other",
		"      None of the above",
		"   ┌───────────────────────────────┐    ",
		"   │                               │    ",
		"   └───────────────────────────────┘    ",
	)
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Submit the user message; the agent loop emits the
			// tool call and the multiSelect prompt becomes visible.
			InputSequence: "hi<enter>",
			Expected:      unchecked,
		},
		{
			// Enter with nothing checked is ignored: the prompt
			// stays exactly as it was.
			InputSequence: "<enter>",
			Expected:      unchecked,
		},
		{
			// Space ticks the box under the cursor.
			InputSequence: "<space>",
			Expected: frame(
				"hi",
				"Pick some                       [Choice]",
				blanks(),
				">[x] A",
				"[ ] B",
				"[ ] Other",
				"      None of the above",
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Enter submits the checked option; the prompt is
			// cleared and focus returns to the empty input box.
			InputSequence: "<enter>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

// TestE2ENoLSPLanguageDisablesAutoDiagnostics is the RUNE-AGENT-96
// end-to-end regression. The scripted LLM edits a .py file on two
// consecutive turns through a real apply_patch tool, while the stub LSP
// reports that no language server is running for python. The first edit
// must still auto-inject check_file_errors (so the user sees the error
// once); the second edit must not, because the language was learned to
// be unsupported for the remainder of the session.
func TestE2ENoLSPLanguageDisablesAutoDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		diagErr string
	}{
		{"not supported yet", "python language LSP is not supported yet"},
		{"server not running", "no language server: server python not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			svc := llmtest.New(
				[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
				applyPatchResponse("call-edit-1", "a.py"),
				applyPatchResponse("call-edit-2", "b.py"),
				llmtest.Response{
					Chunks:       []string{"done"},
					FinishReason: llmapi.FinishReasonStop,
				},
			)

			lsp := stubLSP{
				diagnosticFn: func(semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
					return semanticapi.DocumentDiagnosticReport{}, errors.New(tc.diagErr)
				},
			}

			h := agentE2EHandler(t, svc, dir, lsp)

			handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
				InputSequence: "hi<enter>",
				Expected: frame(
					"hi",
					blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
					"   ┌───────────────────────────────┐    ",
					"   │▐                              │    ",
					"   └───────────────────────────────┘    ",
				),
			}})

			// Wait for the full scripted loop (3 CreateCompletion calls).
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if svc.CallCount() >= 3 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			require.GreaterOrEqualf(t, svc.CallCount(), 3,
				"expected scripted agent loop to issue all 3 CreateCompletion calls, got %d",
				svc.CallCount())

			requests := svc.Requests()
			require.GreaterOrEqual(t, len(requests), 3)

			// request[1] carries the messages produced after turn 1: the
			// first apply_patch and its auto-injected check_file_errors.
			firstTurn := requests[1].Request.Messages
			assert.Equal(t, 1, assistantSyntheticDiagCalls(firstTurn),
				"first .py edit must auto-inject check_file_errors")
			diagResult, ok := findToolResult(firstTurn, "auto-diag-call-edit-1")
			require.True(t, ok, "first turn must carry the synthetic diagnostic result")
			assert.Contains(t, diagResult, tc.diagErr,
				"synthetic diagnostic must surface the no-LSP error once")

			// request[2] carries the messages produced after turn 2: the
			// second apply_patch must have NO synthetic check_file_errors,
			// because python was disabled for the session on turn 1.
			secondTurn := requests[2].Request.Messages
			assert.Equal(t, 1, assistantSyntheticDiagCalls(secondTurn),
				"second .py edit must not re-inject check_file_errors for a disabled language")
			_, ok = findToolResult(secondTurn, "auto-diag-call-edit-2")
			assert.False(t, ok,
				"second edit must not produce a synthetic diagnostic for a disabled language")
		})
	}
}

// TestE2EUTF8SafetyAcrossAgentLoop is the RUNE-179 end-to-end
// regression. The scripted LLM walks a workspace that contains:
//
//   - utf8.txt: valid UTF-8 text
//   - latin1.log: text with a stray 0xff byte
//   - bin.dat: binary blob with a NUL in the first 8 KiB
//
// It issues a read_file call against each path and a final grep_files
// over the workspace before replying with "done". The test then
// inspects the captured llmapi.Request log to verify:
//
//  1. Layer 1 — every outgoing string field is valid UTF-8.
//  2. Layer 3 — read_file on the binary file returns a BinaryStub that
//     steers the model toward bash inspection.
//  3. Layer 3 — read_file on the latin-1 file ends with the U+FFFD
//     marker and the bad byte is replaced.
//  4. Layer 3 — read_file on the UTF-8 file is passed through with no
//     marker.
//  5. Layer 4 — grep_files for "needle" returns the UTF-8 file but
//     not the binary blob, even though both contain the literal bytes.
func TestE2EUTF8SafetyAcrossAgentLoop(t *testing.T) {
	dir := t.TempDir()

	// utf8.txt: valid multi-byte text containing the literal "needle"
	// so grep_files has at least one positive hit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "utf8.txt"),
		[]byte("héllo · 世界\nfind the needle in the haystack\n"), 0o644))
	// latin1.log: ASCII with one stray 0xff byte sliced into a line.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "latin1.log"),
		[]byte("normal line\nbad byte here: \xff oops\nmore text\n"), 0o644))
	// bin.dat: looks binary by both heuristics (NUL in first 8 KiB)
	// and embeds the same "needle" token between binary bytes so the
	// grep filter is the only thing keeping it out of results.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin.dat"),
		[]byte("\x7fELF\x02\x01\x01\x00\x00\x00needle\x00data\x01\x02"), 0o644))

	// Scripted LLM: four tool-calls then stop. The arguments include a
	// stray invalid UTF-8 byte in a path field on purpose so the
	// wire-level sanitiser in agent.go (Layer 1) has to scrub it
	// before CreateCompletion is invoked on the next turn.
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-utf8",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"utf8.txt","offset":0,"limit":0}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-latin1",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name: "read_file",
					// Embed a stray 0xff byte to exercise Layer 1's
					// tool-call Arguments sanitisation.
					Arguments: "{\"path\":\"latin1.log\",\"offset\":0,\"limit\":0,\"_pad\":\"\xff\"}",
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-bin",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"bin.dat","offset":0,"limit":0}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-grep",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "grep_files",
					Arguments: `{"pattern":"needle"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	// Drive the handler through its public interface. The user types
	// "hi" and presses Enter; the scripted agent loop runs all four
	// tool calls and the final stop. We only assert on the final
	// frame (assistant text "done" appears) — the per-tool assertions
	// run after the sequence against the captured request log.
	// The chat area shows the user's message at the top; the
	// assistant's "done" reply and tool-spinner lines are written
	// into the dialogue component asynchronously by the agent loop
	// and may not be flushed by the time this frame is captured.
	// The RUNE-179 assertions below run after polling for the full
	// scripted sequence to complete.
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// Give the agent loop a final moment to flush the post-tool
	// CreateCompletion calls into the recorder. The promptFlusher
	// settle window covers most of this, but the stop reply itself
	// arrives after the last interrupt so we wait once more.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 5,
		"expected scripted agent loop to issue all 5 CreateCompletion calls, got %d", svc.CallCount())

	requests := svc.Requests()

	// (1) Layer 1: every outgoing string field is valid UTF-8.
	for k, r := range requests {
		for i, m := range r.Request.Messages {
			assert.Truef(t, utf8.ValidString(m.Content),
				"request %d message %d Content has invalid UTF-8: %q", k, i, m.Content)
			assert.Truef(t, utf8.ValidString(m.ReasoningContent),
				"request %d message %d ReasoningContent has invalid UTF-8", k, i)
			for j, tc := range m.ToolCalls {
				assert.Truef(t, utf8.ValidString(tc.Function.Arguments),
					"request %d message %d toolcall %d Arguments has invalid UTF-8: %q",
					k, i, j, tc.Function.Arguments)
				assert.Truef(t, utf8.ValidString(tc.Function.Name),
					"request %d message %d toolcall %d Name has invalid UTF-8", k, i, j)
			}
		}
	}

	// The Nth scripted response is processed during the Nth call; the
	// next call (N+1) is the first one whose Messages slice carries
	// the tool-role result from that response. So the message log on
	// request[i+1] is where we can inspect tool result i.
	require.GreaterOrEqual(t, len(requests), 5)
	utf8Msgs := requests[1].Request.Messages
	latin1Msgs := requests[2].Request.Messages
	binMsgs := requests[3].Request.Messages
	grepMsgs := requests[4].Request.Messages

	// (2) Layer 3: binary file → BinaryStub with bash hint.
	binResult, ok := findToolResult(binMsgs, "call-read-bin")
	require.True(t, ok, "expected tool result for call-read-bin in request 3")
	header := regexp.MustCompile(`^<binary file: bin\.dat, \d+ bytes, sha256=[0-9a-f]{64}`)
	assert.Regexp(t, header, binResult, "binary file result must start with the stub header")
	assert.Contains(t, binResult, "use bash",
		"binary stub must instruct the model to fall back to bash")
	assert.Contains(t, binResult, "xxd",
		"binary stub must mention xxd / hexdump / strings as inspection options")

	// (3) Layer 3: latin-1 file → U+FFFD marker, bad byte replaced.
	latin1Result, ok := findToolResult(latin1Msgs, "call-read-latin1")
	require.True(t, ok, "expected tool result for call-read-latin1 in request 2")
	assert.Truef(t, utf8.ValidString(latin1Result),
		"latin-1 tool result must be valid UTF-8: %q", latin1Result)
	assert.Contains(t, latin1Result, "\ufffd",
		"latin-1 tool result must replace bad bytes with U+FFFD")
	assert.Contains(t, latin1Result, "(1 invalid UTF-8 byte replaced with U+FFFD)",
		"latin-1 tool result must carry the byte-count marker")

	// (4) Layer 3: valid UTF-8 file → verbatim, no marker, no stub.
	utf8Result, ok := findToolResult(utf8Msgs, "call-read-utf8")
	require.True(t, ok, "expected tool result for call-read-utf8 in request 1")
	assert.Contains(t, utf8Result, "héllo · 世界",
		"utf-8 tool result must round-trip multi-byte characters")
	assert.NotContains(t, utf8Result, "invalid UTF-8",
		"valid utf-8 file must not be tagged with a sanitisation marker")
	assert.NotContains(t, utf8Result, "<binary file:",
		"valid utf-8 file must not be tagged as binary")

	// (5) Layer 4: grep_files matches the utf-8 text file but skips
	// the binary blob even though both contain the literal "needle".
	grepResult, ok := findToolResult(grepMsgs, "call-grep")
	require.True(t, ok, "expected tool result for call-grep in request 4")
	assert.Contains(t, grepResult, "utf8.txt", "grep must find the text file")
	assert.NotContains(t, grepResult, "bin.dat",
		"grep must skip binary files even when their bytes contain the pattern")
}

// TestE2EPauseTurnResumesAgentLoop drives the chat handler end-to-end
// through the "agent" command path. The scripted LLM ends the first
// turn with FinishReasonPause (Anthropic's pause_turn) carrying a
// partial assistant message, then completes on the resume. The agent
// loop must re-enter without a user turn, re-sending the partial
// assistant message, instead of ending with "unexpected finish
// reason".
func TestE2EPauseTurnResumesAgentLoop(t *testing.T) {
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			Chunks:       []string{"thinking out loud"},
			FinishReason: llmapi.FinishReasonPause,
		},
		llmtest.Response{
			Chunks:       []string{"final answer"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := stopReasonE2EHandler(t, svc)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		// The paused turn's partial text ("thinking out loud") and the
		// resumed turn's text ("final answer") render as one continuous
		// assistant message, proving the loop resumed in place.
		Expected: frame(
			"hi",
			"thinking out loudfinal answer",
			blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// The pause must trigger a second CreateCompletion with no
	// intervening user turn, whose request re-sends the partial
	// assistant message so the model can continue where it paused.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		requests := svc.Requests()
		require.GreaterOrEqual(c, len(requests), 2)
		var foundPartial bool
		for _, m := range requests[1].Request.Messages {
			if m.Role == llmapi.RoleAssistant && strings.Contains(m.Content, "thinking out loud") {
				foundPartial = true
			}
		}
		assert.True(c, foundPartial,
			"resume request must re-send the partial assistant message from the paused turn")
	}, 3*time.Second, 10*time.Millisecond)
}

// TestE2ERefusalTerminatesCleanly drives the chat handler end-to-end
// through the "agent" command path. The scripted LLM ends the turn
// with FinishReasonRefusal. The agent loop must terminate without a
// resume and without emitting the generic "unexpected finish reason"
// error: exactly one CreateCompletion call is issued.
func TestE2ERefusalTerminatesCleanly(t *testing.T) {
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			Chunks:       []string{"I can't help with that"},
			FinishReason: llmapi.FinishReasonRefusal,
		},
	)

	h := stopReasonE2EHandler(t, svc)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		// The refusal renders the partial assistant text followed by
		// the distinct refusal banner from the EventRefusal branch.
		Expected: frame(
			"hi",
			"I can't help with that",
			blanks(),
			"The model declined to continue with",
			"this request.",
			blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// The refusal banner above only renders after the single
	// completion finished and EventRefusal was processed, so the turn
	// is already terminal here. A resume would require a second
	// completion; assert it never happens (and stays at one).
	assert.Never(t, func() bool { return svc.CallCount() != 1 },
		300*time.Millisecond, 10*time.Millisecond,
		"refusal must terminate the turn without resuming the agent loop")
}

// TestE2EEmptyToolResultIsNeverSentEmpty reproduces the mid-turn 400
// "text content blocks must be non-empty" failure. A bash command that
// produces no output (and matches no tool hint) returns an empty
// ToolResult; that empty content flows verbatim into the tool-role
// message replayed on the next turn. Anthropic rejects an empty
// tool_result text block, so the agent must guarantee the replayed
// tool result content is non-empty. The assertion runs at the
// llmapi.Service boundary — the exact bytes the provider would send.
func TestE2EEmptyToolResultIsNeverSentEmpty(t *testing.T) {
	dir := t.TempDir()

	// `touch` writes nothing to stdout/stderr and matches none of the
	// bashToolHint rules, so the bash tool returns ToolResult{Content: ""}.
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-empty",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "bash",
					Arguments: `{"command":"touch empty_marker.txt"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// Wait for both scripted calls: the tool turn and the stop reply.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 2,
		"expected the scripted agent loop to issue both CreateCompletion calls, got %d", svc.CallCount())

	// The tool result for call-empty is replayed on the second request.
	requests := svc.Requests()
	require.GreaterOrEqual(t, len(requests), 2)
	result, ok := findToolResult(requests[1].Request.Messages, "call-empty")
	require.True(t, ok, "expected tool result for call-empty in request 1")
	assert.NotEmpty(t, result,
		"empty tool output must not be replayed as an empty tool_result; "+
			"Anthropic rejects it with \"text content blocks must be non-empty\"")
}

// TestE2ECtrlCDismissesFreeFormPrompt drives the same handler with an
// ask_user_question call that has no options (free-form text input).
// The user types "Ali" into the main inputbox; Ctrl-C must dismiss
// the prompt without submitting the typed text. The third frame
// asserts that the typed text remains in the inputbox for the user
// to edit or send as a regular chat message.
func TestE2ECtrlCDismissesFreeFormPrompt(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question":    "What is your name?",
			"header":      "Name",
			"options":     []map[string]any{},
			"multiSelect": false,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "hi<enter>",
			Expected: frame(
				"hi",
				"Name: What is your name?",
				blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			InputSequence: "Ali",
			Expected: frame(
				"hi",
				"Name: What is your name?",
				blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │Ali▐                           │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			InputSequence: "<c-c>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │Ali▐                           │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

// TestE2ECtrlCDismissesRequiresInputPrompt covers the PromptInputMode
// branch of the dialogue handler. ask_user_question never marks an
// option as RequiresInput, so the fixture installs a synthetic prompt
// with RequiresInput=true before any user input. Enter selects the
// only option and transitions to text-input mode; Ctrl-C must dismiss
// the entire prompt instead of merely leaving text-input mode.
func TestE2ECtrlCDismissesRequiresInputPrompt(t *testing.T) {
	h := newPromptHandler(t, promptHandlerOpts{
		extraPrompt: &dialoguetui.MessageEvent{
			Type:         dialoguetui.MessageEventPrompt,
			PromptTitle:  "Choose",
			PromptHeader: "Choice",
			PromptOptions: []dialoguetui.PromptEventOption{
				{Label: "Other", RequiresInput: true},
			},
			PromptResult: make(chan []string, 1),
		},
	})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Enter selects "Other" which has RequiresInput=true,
			// switching the dialogue handler into PromptInputMode.
			InputSequence: "<enter>",
			Expected: frame(
				"▐ype your feedback and press Enter to   ",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Ctrl-C in PromptInputMode must dismiss the whole
			// prompt, not just exit text input.
			InputSequence: "<c-c>",
			Expected: frame(
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

// TestE2ESubAgentInheritsQualifiedModel drives the chat handler
// end-to-end through the production "agent" command path after switching
// the parent to a different model. The parent emits one `agent` tool call
// without a model override, which spawns a sub-agent through a real
// agent.GoroutineSpawner. The sub-agent inherits the parent's active
// model from context, and the spawner's service factory resolves it
// through llmarg against a catalog that exposes the same model name under
// two providers. A bare name would be ambiguous and fail; the test
// asserts the factory received the provider-qualified active model.
func TestE2ESubAgentInheritsQualifiedModel(t *testing.T) {
	const (
		modelName = "claude-opus-4-8"
		provider  = "anthropic"
	)
	// Two providers expose the same model name, so a bare-name lookup
	// is ambiguous; only a provider-qualified argument resolves.
	catalog := []llmapi.ModelEntry{
		{Provider: "anthropic", Name: modelName, ContextWindow: 128_000},
		{Provider: "claude", Name: modelName, ContextWindow: 128_000},
	}

	// Parent LLM: one agent-tool call (inherits the model), then stop.
	parentSvc := llmtest.New(catalog,
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-spawn",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "agent",
					Arguments: `{"description":"do work","prompt":"investigate"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	// Sub-agent LLM: a single stop reply. Wrapped in a strict service
	// so the spawner's llmarg.Resolve must disambiguate by provider.
	subSvc := &strictProviderService{
		Service: llmtest.New(catalog, llmtest.Response{
			Chunks:       []string{"sub done"},
			FinishReason: llmapi.FinishReasonStop,
		}),
		models: catalog,
	}

	var (
		factoryMu     sync.Mutex
		factoryModels []string
		factoryErr    error
	)
	serviceFactory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		entry, err := llmarg.Resolve(context.Background(), subSvc, model)
		factoryMu.Lock()
		factoryModels = append(factoryModels, model)
		if err != nil {
			factoryErr = err
		}
		factoryMu.Unlock()
		if err != nil {
			return nil, llmapi.ModelEntry{}, err
		}
		return subSvc, entry, nil
	}

	h, parent := subAgentSpawnE2EHandler(t, parentSvc, serviceFactory,
		llmapi.ModelEntry{Provider: "codex", Name: "gpt-5.6-sol", ContextWindow: 128_000})
	parent.SwapService(parentSvc,
		llmapi.ModelEntry{Provider: provider, Name: modelName, ContextWindow: 128_000})

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			// "✓" is one display column but three bytes; frame's
			// byte-based padding would under-pad, so pad this row
			// explicitly to the 40-column frame width.
			"✓ agent do work"+strings.Repeat(" ", frameWidth-len([]rune("✓ agent do work"))),
			"sub done",
			"done",
			blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		require.NotEmpty(c, factoryModels,
			"spawner service factory should have been invoked for the sub-agent")
	}, 3*time.Second, 10*time.Millisecond)

	factoryMu.Lock()
	defer factoryMu.Unlock()
	require.NoError(t, factoryErr,
		"sub-agent model must resolve without an ambiguity error")
	for _, m := range factoryModels {
		assert.Equalf(t, provider+"/"+modelName, m,
			"sub-agent must inherit the provider-qualified model, got %q", m)
	}
}

// TestPlanSkillSpawnInheritsQualifiedModel reproduces RUNE-258: invoking
// an agent-type skill via a slash command (e.g. /plan) spawned the
// sub-agent with the bare model name from ag.Model(), dropping the
// provider. When the name is registered under multiple providers the
// spawner's llmarg.Resolve then fails with an ambiguity error. The
// agent is bound to a fully-qualified model, so the spawn must pass the
// qualified provider/name to the service factory.
func TestPlanSkillSpawnInheritsQualifiedModel(t *testing.T) {
	const (
		modelName = "claude-opus-4-8"
		provider  = "claude"
	)
	catalog := []llmapi.ModelEntry{
		{Provider: "anthropic", Name: modelName, ContextWindow: 128_000},
		{Provider: "claude", Name: modelName, ContextWindow: 128_000},
	}

	// Register a "plan" agent-type skill on disk.
	skillDir := t.TempDir()
	planDir := filepath.Join(skillDir, "plan")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "SKILL.md"), []byte(
		"---\nname: plan\ndescription: Plan things\ntype: agent\n---\nYou are a planner.",
	), 0o644))
	skillReg := skills.NewRegistry(testLocalFS{root: "/"}, dirURI(""), []string{planDir}, nil)
	planSkill, ok := skillReg.Get("plan")
	require.True(t, ok, "plan skill must load")
	require.Equal(t, "agent", planSkill.Type)

	subSvc := &strictProviderService{
		Service: llmtest.New(catalog, llmtest.Response{
			Chunks:       []string{"sub done"},
			FinishReason: llmapi.FinishReasonStop,
		}),
		models: catalog,
	}
	var (
		factoryMu     sync.Mutex
		factoryModels []string
		factoryErr    error
	)
	serviceFactory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		entry, err := llmarg.Resolve(context.Background(), subSvc, model)
		factoryMu.Lock()
		factoryModels = append(factoryModels, model)
		if err != nil {
			factoryErr = err
		}
		factoryMu.Unlock()
		if err != nil {
			return nil, llmapi.ModelEntry{}, err
		}
		return subSvc, entry, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := newMemDialogueStore()
	const dialogueID = "test-dialogue"
	cfg := agent.NewConfig([]agent.Definition{
		{ID: "default", Name: "Default Agent", AllowAny: true},
	})
	spawner := agent.NewGoroutineSpawner(
		store, serviceFactory, cfg, skillReg, agent.NoMemory(), "",
		dialogueID, "default", dirURI(""), noopAgentPrompter{},
	)
	registry := agent.NewRegistry()
	spawner.SetRegistry(registry)

	parentSvc := llmtest.New(catalog)
	ag := agent.NewAgent(parentSvc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        llmapi.ModelEntry{Provider: provider, Name: modelName, ContextWindow: 128_000},
		SessionKey:   dialogueID,
		AgentID:      "default",
	})

	tx := make(chan dialoguetui.MessageEvent, 64)
	rx := make(chan completionRequest, 1)
	childEvents := make(chan agent.ChildEvent, 64)
	rx <- completionRequest{displayText: "plan it", modelText: "plan it", skillName: "plan", ctx: ctx}

	done := make(chan struct{})
	go func() {
		defer close(done)
		createAgentCompletions(ctx, cancel, tx, rx, ag, spawner,
			childEvents, skillReg, dialogueID,
			syncComponent{mu: new(sync.Mutex), comp: dialoguetui.NewComponent(dialoguetui.ComponentConfig{}), h: &aiEditorHandler{n: stubNotifications{}, p: term.NopInterrupter()}},
			stubNotifications{}, nil, store)
	}()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		require.NotEmpty(c, factoryModels,
			"spawner service factory should have been invoked for the plan sub-agent")
	}, 3*time.Second, 10*time.Millisecond)

	cancel()
	<-done

	factoryMu.Lock()
	defer factoryMu.Unlock()
	require.NoError(t, factoryErr,
		"plan sub-agent model must resolve without an ambiguity error")
	for _, m := range factoryModels {
		assert.Equalf(t, provider+"/"+modelName, m,
			"plan sub-agent must inherit the provider-qualified model, got %q", m)
	}
}

// TestE2EMaxTokensRejectedAboveModelCeiling drives the floating chat
// handler end-to-end: the user types /max_tokens with a value above the
// bound model's documented output ceiling. The command must not run —
// the validation error renders inline and the agent's session override
// stays unset. claude-sonnet-4-5 caps output at 64000, so 128000 is
// rejected.
func TestE2EMaxTokensRejectedAboveModelCeiling(t *testing.T) {
	model := llmapi.ModelEntry{
		Provider: "anthropic", Name: "claude-sonnet-4-5", ContextWindow: 200_000,
	}
	h, ag := maxTokensE2EHandler(t, model)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "/max_tokens<space>128000<enter>",
		Expected: frame(
			"/max_tokens 128000",
			"! claude-sonnet-4-5 supports at most",
			"64000 max output tokens; 128000 is too",
			"large",
			blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	assert.Equal(t, 0, ag.MaxOutputTokens(),
		"rejected /max_tokens must not apply the session override")
}

// TestE2ESearchContentSkipsSwapFiles is the end-to-end regression for the
// agent's file-walking tools leaking editor swap files. The scripted LLM
// runs search_content for a token that is present in both a tracked file
// and a sibling .rswp swap file. Because agentools.DefaultTools loads the
// vctrl gitignore/swap matcher (matching the IDE fuzzy finder), the swap
// file must be excluded: the captured tool result sent back to the
// llmapi.Service must surface the tracked file but neither the swap file's
// name nor its contents.
//
// The handler under test is the floating dialogue handler the production
// "agent" command opens, driven black-box through handlertest.RunHandlerSequence.
func TestE2ESearchContentSkipsSwapFiles(t *testing.T) {
	dir := t.TempDir()

	// Both files contain the literal "needle". The swap file additionally
	// carries a sentinel so we can assert its contents never reach the LLM.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"),
		[]byte("find the needle here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tracked.txt.rswp"),
		[]byte("needle SWAPSENTINEL must not leak\n"), 0o644))

	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-search",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "search_content",
					Arguments: `{"pattern":"needle","path":null,"include":null}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 2,
		"expected scripted agent loop to issue both CreateCompletion calls, got %d", svc.CallCount())

	requests := svc.Requests()
	require.GreaterOrEqual(t, len(requests), 2)

	// The tool result from the first response is carried on the message
	// log of the second request.
	searchResult, ok := findToolResult(requests[1].Request.Messages, "call-search")
	require.True(t, ok, "expected tool result for call-search in request 1")

	assert.Contains(t, searchResult, "tracked.txt",
		"search_content must surface the tracked file")
	assert.NotContains(t, searchResult, ".rswp",
		"search_content must skip editor swap files")
	assert.NotContains(t, searchResult, "SWAPSENTINEL",
		"search_content must not leak swap file contents to the model")
}

// MCP servers connect in the background, so the base tool set grows
// after initialization while chats are already reading it.
func TestHandlerAddToolsIsConcurrentWithReaders(t *testing.T) {
	t.Parallel()

	h := &aiEditorHandler{
		baseTools:    []agent.Tool{stubTool{}},
		toolRegistry: agent.NewRegistry(),
	}
	snapshot := h.tools()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.addTools(stubTool{})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.tools()
		}()
	}
	wg.Wait()

	assert.Len(t, h.tools(), 9)
	assert.Len(t, snapshot, 1,
		"an earlier snapshot must not observe later MCP tools")
}

// stubTool is a minimal agent.Tool for registry bookkeeping tests.
type stubTool struct{ name string }

func (t stubTool) Definition() llmapi.Tool {
	return llmapi.Tool{Function: llmapi.FunctionDefinition{Name: t.name}}
}
func (stubTool) Execute(context.Context, string) agent.ToolResult {
	return agent.ToolResult{}
}
func (stubTool) Summary(string) string         { return "" }
func (stubTool) NeedsDeterministicOrder() bool { return false }

func TestSubscribedChatReceivesLateMCPTools(t *testing.T) {
	t.Parallel()

	h := &aiEditorHandler{toolRegistry: agent.NewRegistry()}
	h.addTools(stubTool{name: "early"})

	snapshot := h.tools()
	chatReg := agent.NewRegistry(snapshot...)

	h.addTools(stubTool{name: "mid"})
	h.subscribeTools("chat-1", len(snapshot), chatReg)
	_, ok := chatReg.Get("mid", "")
	assert.True(t, ok, "delta between snapshot and subscription must be applied")

	h.addTools(stubTool{name: "late"})
	_, ok = chatReg.Get("late", "")
	assert.True(t, ok, "subscribed chats must receive late tools")
	_, ok = h.toolRegistry.Get("late", "")
	assert.True(t, ok, "the persistent registry must receive late tools")

	h.unsubscribeTools("chat-1")
	h.addTools(stubTool{name: "after-close"})
	_, ok = chatReg.Get("after-close", "")
	assert.False(t, ok, "closed chats must not be updated")
}

func TestWarnPendingMCPServersReportsConnecting(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	m := runemcp.NewManagerWithTransport(
		func(string, runemcp.ServerConfig) (gomcp.Transport, error) {
			<-release
			return nil, errors.New("halted")
		})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.ConnectServer(
			context.Background(), "slow-srv", runemcp.ServerConfig{Command: "x"})
	}()
	t.Cleanup(func() { close(release); <-done })
	require.Eventually(t, func() bool {
		servers := m.Servers()
		return len(servers) == 1 && servers[0].Status == runemcp.StatusConnecting
	}, 5*time.Second, 10*time.Millisecond)

	h := &aiEditorHandler{mcpManager: m}
	tx := make(chan dialoguetui.MessageEvent, 1)
	h.warnPendingMCPServers(tx)

	select {
	case ev := <-tx:
		assert.Equal(t, dialoguetui.MessageEventWarning, ev.Type)
		assert.Contains(t, ev.Text, "slow-srv")
	default:
		t.Fatal("expected a pending-MCP warning event")
	}
}
