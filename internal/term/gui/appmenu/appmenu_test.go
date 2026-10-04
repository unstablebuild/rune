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

package appmenu

import (
	"reflect"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestKeyEquivalent(t *testing.T) {
	tests := []struct {
		name  string
		comb  term.KeyComb
		equiv string
		mods  uint
		ok    bool
	}{
		{
			name:  "meta character",
			comb:  term.KeyComb{Mod: term.ModMeta, Ch: 'q'},
			equiv: "q",
			mods:  modCommandMask,
			ok:    true,
		},
		{
			name:  "uppercase character implies shift",
			comb:  term.KeyComb{Mod: term.ModMeta, Ch: 'Q'},
			equiv: "q",
			mods:  modCommandMask | modShiftMask,
			ok:    true,
		},
		{
			name:  "ctrl meta character",
			comb:  term.KeyComb{Mod: term.ModCtrlMeta, Ch: 'f'},
			equiv: "f",
			mods:  modControlMask | modCommandMask,
			ok:    true,
		},
		{
			name:  "all modifiers",
			comb:  term.KeyComb{Mod: term.ModCtrlShiftMeta | term.ModAlt, Ch: 'a'},
			equiv: "a",
			mods:  modControlMask | modShiftMask | modOptionMask | modCommandMask,
			ok:    true,
		},
		{
			name:  "unmodified character",
			comb:  term.KeyComb{Ch: 'x'},
			equiv: "x",
			ok:    true,
		},
		{
			name:  "function key",
			comb:  term.KeyComb{Key: term.KeyF11},
			equiv: "\uF70E",
			ok:    true,
		},
		{
			name:  "arrow key with alt",
			comb:  term.KeyComb{Mod: term.ModAlt, Key: term.KeyArrowLeft},
			equiv: "\uF702",
			mods:  modOptionMask,
			ok:    true,
		},
		{
			name:  "enter",
			comb:  term.KeyComb{Mod: term.ModMeta, Key: term.KeyEnter},
			equiv: "\u000D",
			mods:  modCommandMask,
			ok:    true,
		},
		{
			name:  "space carries both key and ch",
			comb:  term.KeyComb{Mod: term.ModMeta, Key: term.KeySpace, Ch: ' '},
			equiv: " ",
			mods:  modCommandMask,
			ok:    true,
		},
		{
			name: "zero value has no equivalent",
			comb: term.KeyComb{},
		},
		{
			name: "mouse button has no equivalent",
			comb: term.KeyComb{Key: term.MouseLeft},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			equiv, mods, ok := keyEquivalent(tt.comb)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if equiv != tt.equiv {
				t.Errorf("equiv = %q, want %q", equiv, tt.equiv)
			}
			if mods != tt.mods {
				t.Errorf("mods = %#x, want %#x", mods, tt.mods)
			}
		})
	}
}

func TestBuildSpec(t *testing.T) {
	quit := Command{Title: "Quit Rune", Command: "quit", Key: term.KeyComb{Mod: term.ModMeta, Ch: 'q'}}
	settings := Command{Title: "Settings...", Command: "config", Args: []string{"edit"}}
	fullscreen := Native{
		Title:    "Enter Full Screen",
		Selector: "toggleFullScreen:",
		Key:      term.KeyComb{Mod: term.ModCtrlMeta, Ch: 'f'},
	}

	specs, byTag := buildSpec([]Menu{
		{Title: "Rune", Items: []Item{settings, Separator{}, quit}},
		{Title: "View", Items: []Item{fullscreen}},
	})

	want := []menuSpec{
		{
			title: "Rune",
			items: []itemSpec{
				{title: "Settings...", tag: 1},
				{separator: true},
				{title: "Quit Rune", keyEquiv: "q", modifiers: modCommandMask, tag: 2},
			},
		},
		{
			title: "View",
			items: []itemSpec{
				{
					title:     "Enter Full Screen",
					selector:  "toggleFullScreen:",
					keyEquiv:  "f",
					modifiers: modControlMask | modCommandMask,
				},
			},
		},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("specs = %+v, want %+v", specs, want)
	}

	wantTags := map[int]Command{1: settings, 2: quit}
	if !reflect.DeepEqual(byTag, wantTags) {
		t.Errorf("byTag = %+v, want %+v", byTag, wantTags)
	}
}

func TestBuildSpecNestedSubmenu(t *testing.T) {
	open := Command{Title: "Open File…", Command: "edit"}
	recentA := Command{Title: "proj-a", Command: "workspaceopen", Args: []string{"/a"}}
	recentB := Command{Title: "proj-b", Command: "workspaceopen", Args: []string{"/b"}}
	empty := Command{Title: "No Recent Projects", Command: "", Disabled: true}
	quit := Command{Title: "Quit", Command: "quit"}

	specs, byTag := buildSpec([]Menu{
		{Title: "File", Items: []Item{
			open,
			Submenu{Title: "Open Recent", Items: []Item{recentA, recentB}},
			quit,
		}},
		{Title: "Help", Items: []Item{
			Submenu{Title: "Empty", Items: []Item{empty}},
		}},
	})

	want := []menuSpec{
		{
			title: "File",
			items: []itemSpec{
				{title: "Open File…", tag: 1},
				{title: "Open Recent", children: []itemSpec{
					{title: "proj-a", tag: 2},
					{title: "proj-b", tag: 3},
				}},
				{title: "Quit", tag: 4},
			},
		},
		{
			title: "Help",
			items: []itemSpec{
				{title: "Empty", children: []itemSpec{
					{title: "No Recent Projects", tag: 5, disabled: true},
				}},
			},
		},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("specs = %+v, want %+v", specs, want)
	}

	// Nested commands are addressable by unique tags.
	wantTags := map[int]Command{1: open, 2: recentA, 3: recentB, 4: quit, 5: empty}
	if !reflect.DeepEqual(byTag, wantTags) {
		t.Errorf("byTag = %+v, want %+v", byTag, wantTags)
	}
}

func TestActivateNestedTag(t *testing.T) {
	recent := Command{Title: "proj", Command: "workspaceopen", Args: []string{"/p"}}

	var got []Command
	Install([]Menu{
		{Title: "File", Items: []Item{
			Submenu{Title: "Open Recent", Items: []Item{recent}},
		}},
	}, func(c Command) { got = append(got, c) })

	activateTag(1)

	want := []Command{recent}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("activated = %+v, want %+v", got, want)
	}
}

func TestActivateTag(t *testing.T) {
	quit := Command{Title: "Quit Rune", Command: "quit"}
	settings := Command{Title: "Settings...", Command: "config"}

	var got []Command
	Install([]Menu{
		{Title: "Rune", Items: []Item{
			settings,
			Separator{},
			Native{Title: "Hide", Selector: "hide:"},
			quit,
		}},
	}, func(c Command) { got = append(got, c) })

	activateTag(2)
	activateTag(1)
	// Tag 0 is AppKit's default for items without a command.
	activateTag(0)
	activateTag(99)

	want := []Command{quit, settings}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("activated = %+v, want %+v", got, want)
	}
}

func TestActivateTagWithoutInstall(t *testing.T) {
	Install(nil, nil)
	activateTag(1)
}
