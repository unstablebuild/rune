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

package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestSynopsisSlots(t *testing.T) {
	tsuite := []struct {
		synopsis string
		expected []string
	}{
		{"", nil},
		{"   ", nil},
		{"<name>", []string{"<name>"}},
		{"<name> <alignment> [<filter>]",
			[]string{"<name>", "<alignment>", "[<filter>]"}},
		{"[<executable> [<args>]]", []string{"[<executable> [<args>]]"}},
		{"(increase|decrease) (height|width)",
			[]string{"(increase|decrease)", "(height|width)"}},
		{"[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>",
			[]string{"[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>"}},
		{"<extension-id> <command> [<args>...]",
			[]string{"<extension-id>", "<command>", "[<args>...]"}},
	}
	for _, tcase := range tsuite {
		assert.Equal(t, tcase.expected, synopsisSlots(tcase.synopsis), tcase.synopsis)
	}
}

func TestArgHint(t *testing.T) {
	const tasknew = "<name> <alignment> [<filter>] -- <cmd> [<args>]"
	tsuite := []struct {
		desc     string
		command  string
		synopsis string
		args     []string
		expected string
	}{
		{"first slot", "tasknew", "<name> <alignment>", nil, "<name>"},
		{"second slot", "tasknew", "<name> <alignment>",
			[]string{"hello"}, "<alignment>"},
		{"past the last slot", "tasknew", "<name> <alignment>",
			[]string{"hello", "left"}, ""},
		{"variadic tail repeats", "extensionready",
			"<extension-id> <command> [<args>...]",
			[]string{"a", "b", "c", "d", "e"}, "[<args>...]"},
		{"no synopsis", "quit", "", nil, ""},
		{"override replaces synopsis", "workspaceopen",
			"[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>", nil, "<path>"},
		{"override does not extend past its slots", "workspaceopen",
			"[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>",
			[]string{"~/src/rune"}, ""},
		{"alias without a synopsis is overridable", "worktreenew", "",
			nil, "<name>"},
		{"literal slot is hinted while unmet", "tasknew", tasknew,
			[]string{"hello", "left", "*.go"}, "--"},
		{"literal slot is not repeated once typed", "tasknew", tasknew,
			[]string{"hello", "left", "*.go", "--"}, "<cmd>"},
		{"skipped optional slot does not shift the literal", "tasknew", tasknew,
			[]string{"hello", "left", "--"}, "<cmd>"},
		{"optional slot is still offered before the literal", "tasknew", tasknew,
			[]string{"hello", "left"}, "[<filter>]"},
		{"skipped optional slot does not shift later slots", "tasknew", tasknew,
			[]string{"hello", "left", "--", "ls"}, "[<args>]"},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			assert.Equal(t, tcase.expected,
				argHint(tcase.command, tcase.synopsis, tcase.args))
		})
	}
}

var argHintTestCommands = []Manual{
	{
		Name:     "tasknew",
		Summary:  "Create a task.",
		Synopsis: "<name> <alignment> [<filter>] -- <cmd> [<args>]",
	},
	{
		Name:     "workspaceopen",
		Summary:  "Open a workspace.",
		Synopsis: "[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>",
	},
	{
		Name:     "lsp",
		Summary:  "Language server commands.",
		Synopsis: "<subcommand> [<args>...]",
		Commands: []Manual{
			{Name: "rename", Summary: "Rename a symbol.", Synopsis: "<symbol>"},
		},
	},
	{
		Name:    "worktreenew",
		Summary: "Create a worktree.",
		AliasOf: []string{"! git worktree add $1"},
	},
	{Name: "quit", Summary: "Quit."},
}

func TestCommandHandlerArgHintDraw(t *testing.T) {
	tsuite := []struct {
		desc         string
		sequence     string
		width        int
		expectedDraw string
	}{
		{"no hint while the command is still being typed", "tasknew", 30, `
tasknew▐                      
tasknew                       
                              
                              
                              `},
		{"first argument slot", "tasknew<space>", 30, `
tasknew ▐name>                
                              
                              
                              
                              `},
		{"hint disappears on the first typed character", "tasknew<space>a", 30, `
tasknew a▐                    
                              
                              
                              
                              `},
		{"hint advances to the next slot", "tasknew<space>a<space>", 30, `
tasknew a ▐alignment>         
                              
                              
                              
                              `},
		{"optional slots are hinted verbatim", "tasknew<space>a<space>b<space>", 30, `
tasknew a b ▐<filter>]        
                              
                              
                              
                              `},
		{"literal slot is hinted once the optional is filled", "tasknew<space>a<space>b<space>c<space>", 30, `
tasknew a b c ▐-              
                              
                              
                              
                              `},
		{"skipping the optional keeps later slots aligned", "tasknew<space>a<space>b<space>--<space>", 30, `
tasknew a b -- ▐cmd>          
                              
                              
                              
                              `},
		{"no hint past the last slot", "tasknew<space>a<space>b<space>--<space>ls<space>x<space>", 30, `
tasknew a b -- ls x ▐         
                              
                              
                              
                              `},
		{"whitespace-free synopsis uses the override", "workspaceopen<space>", 30, `
workspaceopen ▐path>          
                              
                              
                              
                              `},
		{"subcommand synopsis replaces the parent's", "lsp<space>rename<space>", 30, `
lsp rename ▐symbol>           
                              
                              
                              
                              `},
		{"command without a synopsis has no hint", "quit<space>", 30, `
quit ▐                        
                              
                              
                              
                              `},
		{"alias without a synopsis uses the override", "worktreenew<space>", 30, `
worktreenew ▐name>            
                              
                              
                              
                              `},
		{"hint is clipped at the progress widget", "workspaceopen<space>", 20, `
workspaceopen ▐pa   
                    
                    
                    
                    `},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			t.Parallel()
			cfg := testDefaultConfig()
			cfg.Sync = true
			cfg.ShowManual = false
			cfg.ShowArgHint = true

			dispatchFn, cleanup := nopDispatch()
			defer cleanup(t)
			completeFn, cleanupComplete := nopComplete()
			defer cleanupComplete(t)

			b := NewPrompt(
				storagestub.NewInMemoryService(), FuncCompleter(completeFn),
				FuncDispatcher(dispatchFn), term.NopInterrupter(),
				argHintTestCommands, cfg,
			)
			defer b.Close()
			cases := []handlertest.SequenceTestCase{
				{InputSequence: tcase.sequence, Expected: tcase.expectedDraw[1:]},
			}
			handlertest.RunHandlerSequence(t, testCommandHandler{b}, tcase.width, 5, cases)
		})
	}
}
