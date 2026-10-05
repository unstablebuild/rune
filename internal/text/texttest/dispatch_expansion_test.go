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

package texttest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
)

func TestDispatchCommandExpansion(t *testing.T) {
	type spec struct {
		// name is the sub-test name.
		name string
		// envSource is the user-level cmdenv.Source wired into the
		// Component; nil means none was configured.
		envSource cmdenv.Source
		// alias is the optional command alias under test. When set,
		// dispatch targets aliasName with aliasArgs and the sink
		// captures the expanded alias-body argv. When empty, the
		// dispatch goes directly at "sink" with directArgs.
		alias      string
		aliasName  string
		aliasArgs  []string
		directArgs []string
		// open is the resource URI to open and focus before dispatch
		// (empty means no tab is opened — captureExpansionContext
		// returns a zero context).
		open string
		// envVars are key/value pairs set via t.Setenv before
		// dispatch, so cases can exercise os.Getenv fallback
		// deterministically without leaking into other tests.
		envVars map[string]string
		// wantArgs is the expected argv reaching the sink handler.
		// Ignored when wantSinkCalls is 0.
		wantArgs []string
		// wantSinkCalls is the expected number of times sink runs.
		// 0 means sink must NOT run; 1 means exactly once.
		wantSinkCalls int
		// wantOK is the expected DispatchCommand handled bool.
		wantOK bool
		// wantErrContains, when non-empty, requires
		// DispatchCommand to return an error whose message
		// contains the substring. Empty means require.NoError.
		wantErrContains string
	}

	uri, err := workspaceapi.ParseURI("file:///dispatch")
	require.NoError(t, err)

	// envWS returns a cmdenv.Source wiring WORKSPACE_URI and
	// WORKSPACE_PATH the way the production workspace handler does.
	envWS := func(wsURI, wsPath string) cmdenv.Source {
		return func(name string) (string, bool) {
			switch name {
			case "WORKSPACE_URI":
				return wsURI, true
			case "WORKSPACE_PATH":
				return wsPath, true
			}
			return "", false
		}
	}

	cases := []spec{
		// ---- alias-target expansion ----------------------------
		{
			name:          "alias $1 substitutes first positional",
			alias:         "echo $1",
			aliasName:     "shout",
			aliasArgs:     []string{"hello"},
			wantArgs:      []string{"hello"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias ${1} braced positional",
			alias:         "echo ${1}_done",
			aliasName:     "shout",
			aliasArgs:     []string{"hello"},
			wantArgs:      []string{"hello_done"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:            "alias $N missing positional errors before dispatch",
			alias:           "echo $1 $2",
			aliasName:       "shout",
			aliasArgs:       []string{"only-one"},
			wantSinkCalls:   0,
			wantOK:          false,
			wantErrContains: "alias expects an argument at position 2 ($2)",
		},
		{
			name:          "alias repeated $1 expands each occurrence",
			alias:         "echo $1 $1",
			aliasName:     "twice",
			aliasArgs:     []string{"x"},
			wantArgs:      []string{"x", "x"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias unreferenced args appended after target",
			alias:         "echo $1",
			aliasName:     "shout",
			aliasArgs:     []string{"first", "tail-a", "tail-b"},
			wantArgs:      []string{"first", "tail-a", "tail-b"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias $$ escapes to literal dollar",
			alias:         `echo $$HOME`,
			aliasName:     "litdollar",
			wantArgs:      []string{"$HOME"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias positional arg with spaces stays one field",
			alias:         "echo $1",
			aliasName:     "shout",
			aliasArgs:     []string{"hello world"},
			wantArgs:      []string{"hello world"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias quoted token preserves whitespace and expands",
			alias:         `echo "prefix $1 suffix"`,
			aliasName:     "shout",
			aliasArgs:     []string{"x"},
			wantArgs:      []string{"prefix x suffix"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias ${VAR:-default} uses default when unset",
			alias:         "echo ${MISSING_VAR_FOR_DISPATCH_TEST:-fallback}",
			aliasName:     "fb",
			wantArgs:      []string{"fallback"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:            "alias ${VAR:?msg} errors when unset",
			alias:           "echo ${MISSING_VAR_FOR_DISPATCH_TEST:?required}",
			aliasName:       "needit",
			wantSinkCalls:   0,
			wantOK:          false,
			wantErrContains: "required",
		},
		{
			name:            "alias command substitution rejected at expand time",
			alias:           "echo $(echo hi)",
			aliasName:       "trycs",
			wantSinkCalls:   0,
			wantOK:          false,
			wantErrContains: "expand alias target token",
		},
		// ---- file-context expansion in alias targets -----------
		{
			name:          "alias $FILE_STEM uses focused file stem",
			alias:         "echo ${FILE_STEM}_test.go",
			aliasName:     "stem",
			open:          "file:///root/src/main.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"main_test.go"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias $FILE_DIR with workspace path composition",
			alias:         "echo $WORKSPACE_PATH $FILE_DIR",
			aliasName:     "where",
			open:          "file:///root/src/main.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"/root", "/root/src"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias $FILE_REL empty when WORKSPACE_URI invalid",
			alias:         "echo $FILE_REL",
			aliasName:     "rel",
			open:          "file:///root/main.go",
			envSource:     envWS("not a uri", "/root"),
			wantArgs:      []string{""},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias $FILE empty without focused tab",
			alias:         "echo [$FILE]",
			aliasName:     "nofile",
			wantArgs:      []string{"[]"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "alias $LINE/$COLUMN default to 1/1 on fresh tab",
			alias:         "echo $LINE $COLUMN",
			aliasName:     "pos",
			open:          "file:///root/x.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"1", "1"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		// ---- env precedence ------------------------------------
		{
			name:      "alias builtin overrides user env for FILE",
			alias:     "echo $FILE",
			aliasName: "ovr",
			open:      "file:///root/main.go",
			envSource: func(name string) (string, bool) {
				if name == "FILE" {
					return "user-wins-not", true
				}
				return "", false
			},
			wantArgs:      []string{"/root/main.go"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:      "alias user env overrides os env",
			alias:     "echo $RUNE_TEST_CUSTOM_VAR",
			aliasName: "ovr",
			envSource: func(name string) (string, bool) {
				if name == "RUNE_TEST_CUSTOM_VAR" {
					return "from-source", true
				}
				return "", false
			},
			envVars: map[string]string{
				"RUNE_TEST_CUSTOM_VAR": "from-os",
			},
			wantArgs:      []string{"from-source"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:      "alias falls back to os.Getenv when source returns false",
			alias:     "echo $RUNE_TEST_CUSTOM_VAR",
			aliasName: "fb",
			envVars: map[string]string{
				"RUNE_TEST_CUSTOM_VAR": "from-os",
			},
			wantArgs:      []string{"from-os"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:      "alias source with empty value yields empty field",
			alias:     "echo [$EMPTY_BUT_SET]",
			aliasName: "empty",
			envSource: func(name string) (string, bool) {
				if name == "EMPTY_BUT_SET" {
					return "", true
				}
				return "", false
			},
			wantArgs:      []string{"[]"},
			wantSinkCalls: 1,
			wantOK:        true,
		},

		// ---- dispatched-argv expansion -------------------------
		{
			name:          "direct args expand $FILE in argv",
			directArgs:    []string{"$FILE", "--all"},
			open:          "file:///root/main.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"/root/main.go", "--all"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "direct args $1 is NOT positional outside alias",
			directArgs:    []string{"$1"},
			envVars:       map[string]string{"1": ""},
			wantArgs:      []string{""},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "direct args preserve whitespace inside single field",
			directArgs:    []string{"hello $WORD world"},
			open:          "file:///root/x.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"hello  world"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:            "direct args reject $(...) command substitution",
			directArgs:      []string{"$(echo nope)"},
			wantSinkCalls:   0,
			wantOK:          false,
			wantErrContains: "expand dispatched arg",
		},
		{
			name:          "direct args $$ escapes to literal dollar",
			directArgs:    []string{"$$HOME"},
			wantArgs:      []string{"$HOME"},
			wantSinkCalls: 1,
			wantOK:        true,
		},

		// ---- context-capture corner cases ----------------------
		{
			name:          "no focused tab leaves all file vars empty",
			alias:         "echo [$FILE] [$FILE_URI] [$FILE_DIR] [$FILE_BASENAME] [$LANG]",
			aliasName:     "blank",
			wantArgs:      []string{"[]", "[]", "[]", "[]", "[]"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "FILE_EXT strips leading dot, FILE_BASENAME keeps it",
			alias:         "echo $FILE_EXT $FILE_BASENAME",
			aliasName:     "ext",
			open:          "file:///root/main.go",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"go", "main.go"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
		{
			name:          "LANG empty for unknown extension",
			alias:         "echo [$LANG]",
			aliasName:     "lang",
			open:          "file:///root/notes_no_ext",
			envSource:     envWS("file:///root", "/root"),
			wantArgs:      []string{"[]"},
			wantSinkCalls: 1,
			wantOK:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.envVars {
				t.Setenv(k, v)
			}
			cfg := text.DefaultConfig()
			cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
			cfg.EnvSource = tc.envSource
			if tc.alias != "" {
				cfg.CommandAliases = map[string]text.CommandAlias{
					tc.aliasName: {Commands: []string{tc.alias}},
				}
			}

			c, _ := newTestComponentConfig(t, NopEditor(), cfg)
			win, _ := c.Focus()

			if tc.open != "" {
				resource, err := workspaceapi.ParseURI(tc.open)
				require.NoError(t, err)
				h, err := c.Open(resource)
				require.NoError(t, err)
				require.NoError(t, c.Browser().Focus().SetContent(h))
			}

			var (
				gotArgs  []string
				sinkRuns int
			)
			c.SubscribeCommand(testCommand("echo", "", ""),
				text.FuncCommandHandler(func(_ context.Context, cmd textapi.Command) error {
					sinkRuns++
					gotArgs = cmd.Args
					return nil
				}, nil))
			c.SubscribeCommand(testCommand("sink", "", ""),
				text.FuncCommandHandler(func(_ context.Context, cmd textapi.Command) error {
					sinkRuns++
					gotArgs = cmd.Args
					return nil
				}, nil))

			name := tc.aliasName
			args := tc.aliasArgs
			if tc.alias == "" {
				name = "sink"
				args = tc.directArgs
			}

			cmd := textapi.Command{
				Resource: NewTestHandler(),
				URI:      uri,
				Name:     name,
				Args:     args,
				Window:   win,
			}
			ok, derr := dispatchWithAliases(context.Background(), c, cmd)

			if tc.wantErrContains != "" {
				require.Error(t, derr)
				assert.Contains(t, derr.Error(), tc.wantErrContains)
			} else {
				require.NoError(t, derr)
			}
			assert.Equal(t, tc.wantOK, ok, "DispatchCommand ok")
			assert.Equal(t, tc.wantSinkCalls, sinkRuns, "sink handler runs")
			if tc.wantSinkCalls > 0 {
				assert.Equal(t, tc.wantArgs, gotArgs)
			}
		})
	}
}
