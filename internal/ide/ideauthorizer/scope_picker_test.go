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

package ideauthorizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

type scopePickerResult struct {
	calls    int
	selected []int
	ok       bool
}

var testScopes = []commandScope{
	{Key: "git", Label: "git *"},
	{Key: "git commit", Label: "git commit *", Recommended: true, Parent: "git"},
	{Key: "git commit -m fix", Label: "git commit -m fix", Parent: "git commit"},
	{Key: "git status", Label: "git status *", Parent: "git"},
	{Key: "python3 -c", Label: "python3 -c *", Warn: true},
}

func newTestScopePicker(t *testing.T, allow bool) (*scopePicker, *scopePickerResult) {
	t.Helper()
	result := &scopePickerResult{}
	p, err := newScopePicker(scopePickerRequest{
		Allow: allow, Requester: "extension Agent", Scopes: testScopes, Selected: []int{1},
	}, func(selected []int, ok bool) {
		result.calls++
		result.selected = selected
		result.ok = ok
	})
	require.NoError(t, err)
	return p, result
}

func TestScopePickerDimensions(t *testing.T) {
	t.Parallel()

	long := "git log " + strings.Repeat("a", 100) + " *"
	cases := []struct {
		name       string
		allow      bool
		scopes     []commandScope
		wantWidth  int
		wantHeight int
		wantTitle  string
	}{
		{name: "the keys set the width of short rows and the text wraps to it", allow: true,
			scopes: testScopes, wantWidth: 52, wantHeight: 16, wantTitle: "Always allow"},
		{name: "never allow", scopes: testScopes,
			wantWidth: 52, wantHeight: 16, wantTitle: "Never allow"},
		{name: "rows are never clipped", allow: true,
			scopes: []commandScope{{Key: "git", Label: "git *"}, {Key: "log", Label: long, Parent: "git"}},
			// The padding, the branch, the box and its space, the label.
			wantWidth: 2 + 3 + 3 + len(long), wantHeight: 10, wantTitle: "Always allow"},
		{name: "the warning of a scope that runs arbitrary code is part of its row", allow: true,
			scopes:    []commandScope{{Key: "sed", Label: long, Warn: true}},
			wantWidth: 2 + 3 + len(long) + 2 + 2 + len(" runs arbitrary code"), wantHeight: 8,
			wantTitle: "Always allow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := newScopePicker(scopePickerRequest{
				Allow: tc.allow, Requester: "extension Agent", Scopes: tc.scopes,
			}, func([]int, bool) {})
			require.NoError(t, err)
			w, h := p.Dimensions()
			assert.Equal(t, tc.wantWidth, w)
			assert.Equal(t, tc.wantHeight, h)
			assert.Equal(t, tc.wantTitle, p.title())
		})
	}
}

func TestScopePicker(t *testing.T) {
	t.Parallel()

	const width, height = 52, 16
	initial := `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄮  git *                                           
 ├─ 󰄲  git commit *                                 
 │  └─ 󰄲  git commit -m fix                         
 └─ 󰄮  git status *                                 
 󰄮  python3 -c *  ⚠  runs arbitrary code            
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:]
	gitPicked := `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄲  git *                                           
 ├─ 󰄲  git commit *                                 
 │  └─ 󰄲  git commit -m fix                         
 └─ 󰄲  git status *                                 
 󰄮  python3 -c *  ⚠  runs arbitrary code            
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:]
	cases := []struct {
		name         string
		input        string
		expected     string
		wantSelected []int
		wantOK       bool
		wantCalls    int
	}{
		{name: "rows nest under the scope they narrow", expected: initial},
		{name: "picking a scope picks the scopes nested under it", input: "<space>",
			expected: gitPicked},
		{name: "unpicking a scope restores the picks nested under it", input: "<space><space>",
			expected: initial},
		{name: "enter confirms without the picks a pick covers", input: "<space><enter>",
			expected: gitPicked, wantSelected: []int{0}, wantOK: true, wantCalls: 1},
		{name: "covered rows do not toggle",
			input:    "<space><down><space><down><space><up><up><space><enter>",
			expected: initial, wantSelected: []int{1}, wantOK: true, wantCalls: 1},
		{name: "focus moves with arrows and ctrl keys",
			input: "<up><c-j><c-n><down><down><down><space><c-k><space><c-p><c-p><space><enter>",
			expected: `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄮  git *                                           
 ├─ 󰄮  git commit *                                 
 │  └─ 󰄮  git commit -m fix                         
 └─ 󰄲  git status *                                 
 󰄲  python3 -c *  ⚠  runs arbitrary code            
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:], wantSelected: []int{3, 4}, wantOK: true, wantCalls: 1},
		{name: "nothing picked", input: "<down><space><enter>",
			expected: `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄮  git *                                           
 ├─ 󰄮  git commit *                                 
 │  └─ 󰄮  git commit -m fix                         
 └─ 󰄮  git status *                                 
 󰄮  python3 -c *  ⚠  runs arbitrary code            
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:], wantOK: true, wantCalls: 1},
		{name: "typing does nothing", input: "py", expected: initial},
		{name: "esc cancels", input: "<space><esc>", expected: gitPicked, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, result := newTestScopePicker(t, true)
			handlertest.RunHandlerSequence(t, p, width, height, []handlertest.SequenceTestCase{
				{InputSequence: tc.input, Expected: tc.expected},
			})
			assert.Equal(t, tc.wantCalls, result.calls)
			assert.Equal(t, tc.wantSelected, result.selected)
			assert.Equal(t, tc.wantOK, result.ok)
		})
	}
}

func TestScopePickerResize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		height   int
		expected string
	}{
		{name: "rows the window has to spare go below the list", height: 18,
			expected: `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄮  git *                                           
 ├─ 󰄲  git commit *                                 
 │  └─ 󰄲  git commit -m fix                         
 └─ 󰄮  git status *                                 
 󰄮  python3 -c *  ⚠  runs arbitrary code            
                                                    
                                                    
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:]},
		{name: "the list gives up the rows the window lacks", height: 14,
			expected: `
                                                    
 From now on, extension Agent may run the checked   
 commands without asking you. * stands for any      
 further arguments.                                 
                                                    
 󰄮  git *                                           
 ├─ 󰄲  git commit *                                 
 │  └─ 󰄲  git commit -m fix                         
                                                    
 Run authorizer list in the console to see what     
 each extension may run, and authorizer revoke <id> 
 to revoke a decision.                              
                                                    
 <space> toggles · <enter> confirms · <esc> cancels `[1:]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, _ := newTestScopePicker(t, true)
			handlertest.RunHandlerSequence(t, p, 52, tc.height, []handlertest.SequenceTestCase{
				{Expected: tc.expected},
			})
		})
	}
}

func TestScopePickerAttributes(t *testing.T) {
	t.Parallel()

	const width, height = 52, 16
	code := term.Attributes{Bg: scopeGrayAttr.Fg}
	type cell struct {
		x, y int
		ch   rune
		attr term.Attributes
	}
	cases := []struct {
		name  string
		input string
		cells []cell
	}{
		{name: "picked scopes are green and the others gray", cells: []cell{
			{x: 1, y: 5, ch: []rune(scopeUnchecked)[0], attr: scopeGrayAttr},
			{x: 1, y: 6, ch: '├', attr: scopeTreeAttr},
			{x: 4, y: 6, ch: []rune(scopeChecked)[0], attr: scopePickedAttr},
			{x: 7, y: 6, ch: 'g'},
		}},
		{name: "scopes a pick covers are picked in gray", input: "<space>", cells: []cell{
			{x: 1, y: 5, ch: []rune(scopeChecked)[0], attr: scopePickedAttr},
			{x: 4, y: 6, ch: []rune(scopeChecked)[0], attr: scopeGrayAttr},
			{x: 7, y: 7, ch: []rune(scopeChecked)[0], attr: scopeGrayAttr},
		}},
		{name: "the label of the focused row stands out", input: "<down>", cells: []cell{
			{x: 4, y: 5, ch: 'g'},
			{x: 7, y: 6, ch: 'g', attr: scopeFocusAttr},
		}},
		{name: "the warning of a scope that runs arbitrary code is yellow", cells: []cell{
			{x: 15, y: 9, ch: '*'},
			{x: 18, y: 9, ch: '⚠', attr: scopeWarnAttr},
			{x: 21, y: 9, ch: 'r', attr: scopeWarnAttr},
		}},
		{name: "a row of padding tops the header, which names the requester in bold and * as code",
			cells: []cell{
				{x: 1, y: 0},
				{x: 1, y: 1, ch: 'F'},
				{x: 14, y: 1, ch: 'e', attr: term.Attributes{Attrs: term.AttrBold}},
				{x: 30, y: 2, ch: '*', attr: code},
			}},
		{name: "the reminder is gray with its commands as code and the keys are not", cells: []cell{
			{x: 1, y: 11, ch: 'R', attr: scopeGrayAttr},
			{x: 5, y: 11, ch: 'a', attr: code},
			{x: 1, y: 15, ch: '<'},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, _ := newTestScopePicker(t, true)
			p.Resize(width, height)
			keys, err := term.ParseKeys(tc.input)
			require.NoError(t, err)
			for _, k := range keys {
				p.Handle(term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod})
			}
			w := term.NewStringWriter(width, height)
			p.Draw(w)
			for _, want := range tc.cells {
				got := w.Cells()[want.y*width+want.x]
				assert.Equal(t, want.ch, got.Ch, "cell %d,%d", want.x, want.y)
				assert.Equal(t, want.attr, got.Attributes(), "cell %d,%d", want.x, want.y)
			}
		})
	}
}

func TestScopePickerHandle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		ev          term.Event
		wantExit    bool
		wantHandled bool
		wantCalls   int
		wantOK      bool
	}{
		{name: "enter confirms", ev: term.Event{Type: term.EventKey, Key: term.KeyEnter},
			wantExit: true, wantHandled: true, wantCalls: 1, wantOK: true},
		{name: "esc cancels", ev: term.Event{Type: term.EventKey, Key: term.KeyEsc},
			wantExit: true, wantHandled: true, wantCalls: 1},
		{name: "ctrl-c cancels", ev: term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl},
			wantExit: true, wantHandled: true, wantCalls: 1},
		{name: "space toggles", ev: term.Event{Type: term.EventKey, Key: term.KeySpace},
			wantHandled: true},
		{name: "other keys are not handled", ev: term.Event{Type: term.EventKey, Ch: 'x'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, result := newTestScopePicker(t, true)
			exit, handled := p.Handle(tc.ev)
			assert.Equal(t, tc.wantExit, exit)
			assert.Equal(t, tc.wantHandled, handled)
			assert.Equal(t, tc.wantCalls, result.calls)
			assert.Equal(t, tc.wantOK, result.ok)
		})
	}
}

func TestScopePickerCloseCancelsOnce(t *testing.T) {
	t.Parallel()

	p, result := newTestScopePicker(t, true)
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())
	assert.Equal(t, 1, result.calls)
	assert.False(t, result.ok)

	// Closing the window after the user confirmed changes nothing.
	p, result = newTestScopePicker(t, true)
	p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.NoError(t, p.Close())
	assert.Equal(t, 1, result.calls)
	assert.True(t, result.ok)
	assert.Equal(t, []int{1}, result.selected)
}

func TestScopePickerDisambiguatesEqualLabels(t *testing.T) {
	t.Parallel()

	p, err := newScopePicker(scopePickerRequest{
		Allow:  true,
		Scopes: []commandScope{{Label: "git *"}, {Label: "git *"}},
	}, func([]int, bool) {})
	require.NoError(t, err)
	assert.Equal(t, []string{"git *", "git * (2)"}, p.labels)
}
