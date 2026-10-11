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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

const inferredHint = "%s is in no tsconfig.json's project, so TypeScript checks it with default " +
	"options, without path aliases or packages' types such as @types/node. " +
	"Add a tsconfig.json that includes it."

func openInferredHint(t *testing.T, h *inferredProjectHint, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	assert.False(t, h.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: uri}))
}

func TestInferredProjectHint(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)
	fs := newFakeFS("/ws").addFile("web/package.json").addFile("api/package.json")
	inferred := answer{raw: `{"configFilePath": ""}`}
	// step opens a file and waits for the totals of requests sent and
	// hints shown so far.
	type step struct {
		open     string
		requests int
		hints    []string
	}
	tests := []struct {
		name    string
		answers []answer
		steps   []step
	}{
		{
			name:    "a file in no project, once the server knows it",
			answers: []answer{{err: errors.New("no project found")}, {raw: "null"}, inferred},
			steps: []step{
				{"/ws/web/scripts/gen.ts", 3, []string{fmt.Sprintf(inferredHint, "web/scripts/gen.ts")}},
			},
		},
		{
			name:    "once per server root",
			answers: []answer{inferred, inferred},
			steps: []step{
				{"/ws/web/a.ts", 1, []string{fmt.Sprintf(inferredHint, "web/a.ts")}},
				{"/ws/web/b.ts", 1, []string{fmt.Sprintf(inferredHint, "web/a.ts")}},
				{"/ws/api/c.mts", 2, []string{
					fmt.Sprintf(inferredHint, "web/a.ts"), fmt.Sprintf(inferredHint, "api/c.mts"),
				}},
			},
		},
		{
			name:    "a file in a project",
			answers: []answer{{raw: `{"configFilePath": "/ws/web/tsconfig.json"}`}},
			steps:   []step{{"/ws/web/src/main.tsx", 1, nil}},
		},
		{
			name: "javascript, dependencies and files outside the workspace",
			steps: []step{
				{"/ws/web/build.mjs", 0, nil},
				{"/ws/web/node_modules/dep/index.d.ts", 0, nil},
				{"/elsewhere/x.ts", 0, nil},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &requestLSP{answers: map[string][]answer{"custom/projectInfo": tt.answers}}
			notify := &fakeNotifications{}
			h := newInferredProjectHint(context.Background(), lsp, notify, fs, uri)
			var last step
			for _, s := range tt.steps {
				openInferredHint(t, h, s.open)
				require.Eventually(t, func() bool {
					return len(lsp.recorded()) == s.requests && len(notify.messages()) == len(s.hints)
				}, 5*time.Second, 10*time.Millisecond, "after opening %s", s.open)
				assert.Equal(t, s.hints, notify.messages())
				last = s
			}
			assert.Never(t, func() bool {
				return len(lsp.recorded()) != last.requests || len(notify.messages()) != len(last.hints)
			}, 100*time.Millisecond, 10*time.Millisecond)
			for _, r := range lsp.recorded() {
				assert.Equal(t, tsLanguageID, r.ServerID)
			}
		})
	}
}
