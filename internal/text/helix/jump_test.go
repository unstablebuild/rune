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

package helix

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// TestGotoWord pins goto_word's candidate set and label order. Helix
// walks outward from the caret, so the word the caret sits on is never
// offered a label, and only runs of at least two word characters are.
func TestGotoWord(t *testing.T) {
	const two = "alpha beta\ngamma delta"
	runMotionCases(t, []motionCase{
		{name: "aa selects the first candidate", content: two,
			evs: keys("gwaa"), wantAt: term.Coordinates{X: 9}, wantSel: "beta"},
		{name: "ab selects the second candidate", content: two,
			evs:    keys("gwab"),
			wantAt: term.Coordinates{X: 4, Y: 1}, wantSel: "gamma"},
		{name: "ac selects the third candidate", content: two,
			evs:    keys("gwac"),
			wantAt: term.Coordinates{X: 10, Y: 1}, wantSel: "delta"},
		{name: "an unlabelled slot is a no-op", content: two,
			evs: keys("gwad"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "a first char past the last label aborts", content: two,
			evs: keys("gwz"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "esc aborts", content: two,
			evs:    append(keys("gw"), namedKey(term.KeyEsc)),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "the caret walks backwards too", content: "alpha beta",
			at: term.Coordinates{X: 5}, evs: keys("gwab"),
			wantAt: term.Coordinates{X: 4}, wantSel: "alpha"},
		{name: "select mode extends to the label", content: two,
			evs:    keys("vgwab"),
			wantAt: term.Coordinates{X: 4, Y: 1}, wantSel: "alpha beta\ngamma"},
		{name: "a backward label still selects forwards",
			content: "alpha beta", at: term.Coordinates{X: 9},
			evs: keys("gwaa"), wantAt: term.Coordinates{X: 4}, wantSel: "alpha"},
		{name: "select mode extends backwards to the label",
			content: "alpha beta", at: term.Coordinates{X: 9},
			evs:    keys("vgwaa"),
			wantAt: term.Coordinates{}, wantSel: "alpha beta"},
		{name: "a one-character word earns no label", content: "ab c de",
			at: term.Coordinates{X: 3}, evs: keys("gwaa"),
			wantAt: term.Coordinates{X: 6}, wantSel: "de"},
		{name: "the backward candidate follows the forward one",
			content: "ab c de", at: term.Coordinates{X: 3}, evs: keys("gwab"),
			wantAt: term.Coordinates{X: 1}, wantSel: "ab"},
		{name: "the one-character word has no slot of its own",
			content: "ab c de", at: term.Coordinates{X: 3}, evs: keys("gwac"),
			wantAt: term.Coordinates{X: 3}, wantSel: "c"},
		{name: "punctuation runs earn no label", content: "x =< yy",
			evs: keys("gwaa"), wantAt: term.Coordinates{X: 6}, wantSel: "yy"},
	})
}

// TestGotoWordAbortsRestoreNormalMode pins that a cancelled goto_word
// hands the next key back to the normal-mode table instead of eating it.
func TestGotoWordAbortsRestoreNormalMode(t *testing.T) {
	hx, _, _ := newHelix(t, "alpha beta", term.Coordinates{})
	send(t, hx, append(keys("gw"), namedKey(term.KeyEsc))...)
	require.True(t, hx.IsNormalMode())
	send(t, hx, keys("e")...)
	assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
}

// TestGotoWordLabelsAreDrawn pins that the two label characters overwrite
// the start of each candidate, which is the only thing that makes the
// jump addressable.
func TestGotoWordLabelsAreDrawn(t *testing.T) {
	hx, _, _ := newHelix(t, "alpha beta\ngamma delta", term.Coordinates{})
	hx.ShowCommandBar(false)
	hx.Resize(20, 4)
	send(t, hx, keys("gw")...)
	comptest.TestComponent(t, hx, term.NewStringWriter(20, 4), []comptest.TestCase{
		{Expected: `
alpha aata          
abmma aclta         
                    
                    `},
	})
}
