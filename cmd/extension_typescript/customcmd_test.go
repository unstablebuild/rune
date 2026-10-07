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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCustomRequestsTargetTypeScriptServers(t *testing.T) {
	lsp := &requestLSP{answers: map[string][]answer{"custom/runGC": {{raw: "null"}}}}
	env := newActionEnv(t, lsp, true)
	require.NoError(t, env.router.HandleCommand(context.Background(), tsCmd(t, "/ws/a.ts", 0, 0, "gc")))
	reqs := lsp.recorded()
	require.Len(t, reqs, 1)
	assert.Equal(t, "typescript", reqs[0].ServerID,
		"other languages' servers would fail tsgo's requests")
	assert.Equal(t, []string{"Ran the garbage collector of the TypeScript language servers"},
		env.notify.messages())
}

func TestCustomRequestErrorsShowTheServerMessage(t *testing.T) {
	lsp := &requestLSP{answers: map[string][]answer{"custom/runGC": {
		{err: status.Error(codes.Unknown, "request failed: boom")},
	}}}
	env := newActionEnv(t, lsp, true)
	err := env.router.HandleCommand(context.Background(), tsCmd(t, "/ws/a.ts", 0, 0, "gc"))
	require.EqualError(t, err, "custom/runGC: request failed: boom")
}

func TestSourceDefinitionCmd(t *testing.T) {
	const target = "file:///ws/node_modules/greet/index.js"
	tests := []struct {
		name       string
		answer     answer
		wantOpened []string
		wantMoves  []term.Coordinates
		wantNotify []string
		wantErr    string
	}{
		{
			name:       "jumps to the implementation",
			answer:     answer{raw: `[{"uri":"` + target + `","range":{"start":{"line":4,"character":16},"end":{"line":4,"character":21}}}]`},
			wantOpened: []string{target},
			wantMoves:  []term.Coordinates{{X: 16, Y: 4}},
		},
		{
			name: "follows a location link to its selection range",
			answer: answer{raw: `[{"targetUri":"` + target + `",` +
				`"targetRange":{"start":{"line":3,"character":0},"end":{"line":6,"character":1}},` +
				`"targetSelectionRange":{"start":{"line":3,"character":16},"end":{"line":3,"character":21}}}]`},
			wantOpened: []string{target},
			wantMoves:  []term.Coordinates{{X: 16, Y: 3}},
		},
		{
			name:       "nothing found",
			answer:     answer{raw: "null"},
			wantNotify: []string{"No source definition for the symbol at the cursor"},
		},
		{
			name:    "server error",
			answer:  answer{err: errors.New("no project")},
			wantErr: "custom/textDocument/sourceDefinition: no project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &requestLSP{answers: map[string][]answer{
				"custom/textDocument/sourceDefinition": {tt.answer},
			}}
			env := newActionEnv(t, lsp, false)
			err := env.router.HandleCommand(context.Background(),
				tsCmd(t, "/ws/src/main.ts", 2, 9, "source-definition"))
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, `{"textDocument":{"uri":"file:///ws/src/main.ts"},"position":{"line":2,"character":9}}`,
				string(lsp.recorded()[0].Params))
			assert.Equal(t, tt.wantOpened, env.opener.openedURIs())
			assert.Equal(t, tt.wantMoves, env.editor.moves)
			assert.Equal(t, tt.wantNotify, env.notify.messages())
			if tt.wantOpened != nil {
				win, content := env.wm.shown()
				assert.Equal(t, uint64(editorWinID), win.WindowID())
				assert.Equal(t, target, content.(*stubResource).Resource().String(),
					"the window shows the opener's handler")
			}
		})
	}
}

func TestProjectConfigCmd(t *testing.T) {
	tests := []struct {
		name       string
		answer     answer
		wantOpened []string
		wantNotify []string
		wantErr    string
	}{
		{
			name:       "opens the owning tsconfig",
			answer:     answer{raw: `{"configFilePath":"/ws/app/tsconfig.json"}`},
			wantOpened: []string{"file:///ws/app/tsconfig.json"},
		},
		{
			name:   "inferred project",
			answer: answer{raw: `{"configFilePath":""}`},
			wantNotify: []string{
				"No tsconfig.json or jsconfig.json includes main.ts; it is checked as an inferred project",
			},
		},
		{
			name:    "server error",
			answer:  answer{err: errors.New("no project")},
			wantErr: "custom/projectInfo: no project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &requestLSP{answers: map[string][]answer{"custom/projectInfo": {tt.answer}}}
			env := newActionEnv(t, lsp, false)
			err := env.router.HandleCommand(context.Background(),
				tsCmd(t, "/ws/app/src/main.ts", 4, 2, "open-tsconfig"))
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, `{"textDocument":{"uri":"file:///ws/app/src/main.ts"}}`,
				string(lsp.recorded()[0].Params))
			assert.Equal(t, tt.wantOpened, env.opener.openedURIs())
			assert.Equal(t, tt.wantNotify, env.notify.messages())
		})
	}
}

func TestFileCommandsNeedAFile(t *testing.T) {
	env := newActionEnv(t, &requestLSP{}, false)
	for _, sub := range []string{"source-definition", "open-tsconfig"} {
		err := env.router.HandleCommand(context.Background(),
			textapi.Command{Name: tsActionCmdName, Args: []string{sub}})
		require.ErrorIs(t, err, errNoFile, sub)
	}
	assert.Empty(t, env.lsp.recorded())
}

func TestCPUProfileCmd(t *testing.T) {
	const stop = "custom/stopCPUProfile"
	tests := []struct {
		name       string
		args       []string
		answers    map[string][]answer
		wantParams []string
		wantNotify []string
		wantErr    string
	}{
		{
			name:       "start writes to the default directory",
			args:       []string{"start"},
			answers:    map[string][]answer{"custom/startCPUProfile": {{raw: "null"}}},
			wantParams: []string{`{"dir":"/tmp/rune-tsgo-profiles"}`},
			wantNotify: []string{"CPU profiling started; profiles go to /tmp/rune-tsgo-profiles"},
		},
		{
			name:       "a relative directory is under the workspace",
			args:       []string{"start", "profiles"},
			answers:    map[string][]answer{"custom/startCPUProfile": {{raw: "null"}}},
			wantParams: []string{`{"dir":"/ws/profiles"}`},
			wantNotify: []string{"CPU profiling started; profiles go to /ws/profiles"},
		},
		{
			name:       "an absolute directory is kept",
			args:       []string{"start", "/var/prof"},
			answers:    map[string][]answer{"custom/startCPUProfile": {{raw: "null"}}},
			wantParams: []string{`{"dir":"/var/prof"}`},
			wantNotify: []string{"CPU profiling started; profiles go to /var/prof"},
		},
		{
			name: "stop collects the profile of every server",
			args: []string{"stop"},
			answers: map[string][]answer{stop: {
				{raw: `{"file":"/tmp/p/1-cpuprofile.pb.gz"}`},
				{raw: `{"file":"/tmp/p/2-cpuprofile.pb.gz"}`},
				{err: errors.New("CPU profiling not in progress")},
			}},
			wantParams: []string{"", "", ""},
			wantNotify: []string{"CPU profile saved to /tmp/p/1-cpuprofile.pb.gz, /tmp/p/2-cpuprofile.pb.gz"},
		},
		{
			name: "stop without profiling",
			args: []string{"stop"},
			answers: map[string][]answer{stop: {
				{err: errors.New("CPU profiling not in progress")},
			}},
			wantErr: "custom/stopCPUProfile: CPU profiling not in progress",
		},
		{
			name:    "unknown action",
			args:    []string{"pause"},
			wantErr: `unknown cpu-profile action "pause"; use start or stop`,
		},
		{
			name:    "missing action",
			wantErr: "usage: ts cpu-profile start [<dir>] | stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &requestLSP{answers: tt.answers}
			env := newActionEnv(t, lsp, true)
			err := env.router.HandleCommand(context.Background(),
				tsCmd(t, "/ws/a.ts", 0, 0, "cpu-profile", tt.args...))
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			var params []string
			for _, r := range lsp.recorded() {
				params = append(params, string(r.Params))
			}
			assert.Equal(t, tt.wantParams, params)
			assert.Equal(t, tt.wantNotify, env.notify.messages())
		})
	}
}

func TestSaveProfileCmd(t *testing.T) {
	tests := []struct {
		sub, method, file, want string
	}{
		{"heap-profile", "custom/saveHeapProfile", "/tmp/p/1-heapprofile.pb.gz",
			"Heap profile saved to /tmp/p/1-heapprofile.pb.gz"},
		{"alloc-profile", "custom/saveAllocProfile", "/tmp/p/1-allocprofile.pb.gz",
			"Allocation profile saved to /tmp/p/1-allocprofile.pb.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			lsp := &requestLSP{answers: map[string][]answer{tt.method: {{raw: `{"file":"` + tt.file + `"}`}}}}
			env := newActionEnv(t, lsp, true)
			require.NoError(t, env.router.HandleCommand(context.Background(),
				tsCmd(t, "/ws/a.ts", 0, 0, tt.sub, "out")))
			reqs := lsp.recorded()
			require.Len(t, reqs, 1)
			assert.JSONEq(t, `{"dir":"/ws/out"}`, string(reqs[0].Params))
			assert.Equal(t, []string{tt.want}, env.notify.messages())
		})
	}
}

func TestLocationEntries(t *testing.T) {
	loc := semanticapi.Location{URI: "file:///a.js"}
	got := locationEntries(semanticapi.LocationResult{
		Location:  &loc,
		Locations: []semanticapi.Location{{URI: "file:///b.js"}},
		LocationLinks: []semanticapi.LocationLink{{
			TargetURI:            "file:///c.js",
			TargetSelectionRange: semanticapi.Range{Start: semanticapi.Position{Line: 2}},
		}},
	})
	assert.Equal(t, []semanticapi.Location{
		loc,
		{URI: "file:///b.js"},
		{URI: "file:///c.js", Range: semanticapi.Range{Start: semanticapi.Position{Line: 2}}},
	}, got)
}
