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

package component

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestTabsDraw(t *testing.T) {
	l := NewTabs()
	l.Resize(20, 4)
	fb := component.FrameCharSetDefault()
	fb.BottomLeft, fb.BottomRight = '├', '┤'
	l.SetFrameCharSet(fb)

	w := term.NewStringWriter(20, 9)

	tests := []comptest.TestCase{
		{
			nil, `
┌──────────────────┐
│                  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() { l.Add('X', "Atzari") }, `
┌━━━━━━━━──────────┐
│X Atzari          │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() { l.Resize(20, 9) }, `
┌━━━━━━━━──────────┐
│                  │
│                  │
│                  │
│X Atzari          │
│                  │
│                  │
│                  │
├──────────────────┤`,
		}, {
			func() {
				l.Add('$', "Saturn")
				l.Resize(20, 4)
			}, `
┌━━━━━━━━──────────┐
│X Atzari  $ Satu  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() {
				assert.True(t, l.MoveLeft(1))
				assert.False(t, l.MoveLeft(0))
			}, `
┌────────━━━━━━━━──┐
│$ Satu  X Atzari  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() {
				assert.True(t, l.MoveTo(1, 0))
			}, `
┌━━━━━━━━──────────┐
│X Atzari  $ Satu  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() {
				assert.True(t, l.MoveTo(0, 1))
			}, `
┌────────━━━━━━━━──┐
│$ Satu  X Atzari  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		}, {
			func() {
				assert.True(t, l.MoveRight(0))
				assert.False(t, l.MoveRight(1))
			}, `
┌━━━━━━━━──────────┐
│X Atzari  $ Satu  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		// ----------------------------------------------------------------
		// Resize algorithm: when not all tabs fit at full width, the
		// focused tab keeps its full label and the rest are truncated to
		// share the remaining inner width minus the reserved
		// tabBarRightPad cells. No scrolling, no "..", every tab stays
		// visible when min widths fit in the budget.
		// ----------------------------------------------------------------
		// State at this point: tabs = ["X Atzari" (focused), "$ Saturn"],
		// width=20, inner=18, budgetW=16 (innerW - tabBarRightPad).
		// Add "X Other" — three tabs of full widths 8, 8, 7 + 2
		// separators (2 chars each) = 27. Doesn't fit in budgetW=16.
		// Focused (idx 0) keeps full=8; remaining = 16 - 8 - 2*2 = 4
		// split equally between the two non-focused tabs (2 each → "$ ",
		// "X "). padAfter = innerW - used = 18 - 16 = 2.
		{
			func() { l.Add('X', "Other") }, `
┌━━━━━━━━──────────┐
│X Atzari  $   X   │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		// Add "X Things" — 4 tabs, budgetW=16. Window prunes Things
		// (idx 3) so only [0..2] is visible: A(8, focused), Saturn=2,
		// Other=2 with 2 cells of trailing pad.
		{
			func() { l.Add('X', "Things") }, `
┌━━━━━━━━──────────┐
│X Atzari  $   X   │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		{
			func() { l.SetFocus(2) }, `
┌──────━━━━━━━─────┐
│X  $  X Other  X  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		{
			func() { l.Add('#', "Morsins"); l.Add('#', "Morsillonins") }, `
┌───━━━━━━━────────┐
│$  X Other  X  #  │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		{
			func() { l.SetFocus(5) }, `
┌━━━━━━━━━━━━━━────┐
│# Morsillonins    │
│                  │
├──────────────────┤
                    
                    
                    
                    
                    `,
		},
		// Borderless mode: innerW = full width = 20, budgetW=18.
		{
			func() { l.SetBorder(false); l.Resize(20, 1) }, `
#   # Morsillonins  
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
		{
			func() { l.SetFocus(3) }, `
X   X Things  #  #  
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
		{
			func() { l.SetBorder(false); l.Resize(4, 0) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
	}

	comptest.TestComponent(t, l, w, tests)
}

// TestTabsResetTabIcon verifies that SetTabIcon updates the icon and
// ResetTabIcon restores it to either the icon supplied to Add or the
// last default set via SetTabDefaultIcon.
func TestTabsResetTabIcon(t *testing.T) {
	l := NewTabs()
	l.Add('A', "alpha")
	l.Add('B', "beta")

	assert.Equal(t, 'A', l.TabIcon(0))
	assert.Equal(t, 'B', l.TabIcon(1))

	l.SetTabIcon(0, '★')
	assert.Equal(t, '★', l.TabIcon(0))
	assert.Equal(t, 'B', l.TabIcon(1))

	l.ResetTabIcon(0)
	assert.Equal(t, 'A', l.TabIcon(0))

	// SetTabDefaultIcon changes what ResetTabIcon restores to.
	l.SetTabDefaultIcon(1, '☆')
	l.SetTabIcon(1, '?')
	l.ResetTabIcon(1)
	assert.Equal(t, '☆', l.TabIcon(1))
}

// TestTabsDrawFocusHighlightBorderless verifies that in borderless mode at
// h>=2 (the production tab-bar config: TabBarHeight=2, frame=false), tab
// labels render on y=1 and the focused-tab columns on y=0 get the focus
// highlight rune. The non-focused tab columns (and the separator gap) on
// y=0 stay blank.
func TestTabsDrawFocusHighlightBorderless(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(20, 2)

	w := term.NewStringWriter(20, 2)

	tests := []comptest.TestCase{
		// Single focused tab "A alpha" (full=7) at innerW=20.
		// Fast path: 7 ≤ 20. Layout: A alpha + 13 trailing pad.
		// Highlight on y=0 covers columns [0..6].
		{
			func() { l.Add('A', "alpha") }, `
━━━━━━━             
A alpha             `,
		},
		// Two tabs A alpha (focused) + B beta. innerW=20, fast path
		// (7+6+2=15 ≤ 20). Highlight columns [0..6].
		{
			func() { l.Add('B', "beta") }, `
━━━━━━━             
A alpha  B beta     `,
		},
		// Focus moves to B beta (idx 1). Cell starts at column 9
		// (7 + 2 sep), width=6 → highlight columns [9..14].
		{
			func() { l.SetFocus(1) }, `
         ━━━━━━     
A alpha  B beta     `,
		},
	}

	comptest.TestComponent(t, l, w, tests)
}

// TestTabsDrawFocusHighlightCharAttr verifies SetFocusFrameChar overrides
// the highlight rune and that the focusFrame attr passed to SetAttr is
// applied to the highlight cells (and only those — non-focused columns on
// the highlight row stay unattributed).
func TestTabsDrawFocusHighlightCharAttr(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(20, 2)

	focusFrameAttr := term.Attributes{Fg: term.ColorRed, Attrs: term.AttrBold}
	l.SetAttr(term.Attributes{}, term.Attributes{},
		term.Attributes{}, term.Attributes{},
		focusFrameAttr, term.Attributes{}, term.Attributes{})
	l.SetFocusFrameChar('▀')

	l.Add('A', "alpha")
	l.Add('B', "beta")
	l.SetFocus(1)

	w := term.NewStringWriter(20, 2)
	l.Draw(w)
	cells := w.Cells()

	// y=0, columns [9..14] hold the focus highlight; outside that range
	// the row is left untouched by Tabs.Draw (Ch=0).
	for x := 0; x < 20; x++ {
		c := cells[x]
		if x >= 9 && x <= 14 {
			assert.Equal(t, '▀', c.Ch, "expected highlight rune at x=%d", x)
			assert.Equal(t, focusFrameAttr, c.Attributes(),
				"expected focus-frame attr at x=%d", x)
		} else {
			assert.NotEqual(t, '▀', c.Ch,
				"unexpected highlight at x=%d", x)
			assert.Equal(t, term.Attributes{}, c.Attributes(),
				"expected no attr at x=%d on highlight row", x)
		}
	}

	// y=1 carries the labels (sanity).
	assert.Equal(t, 'A', cells[20+0].Ch)
	assert.Equal(t, 'B', cells[20+9].Ch)
}

// TestTabsDrawHighlightDisabled verifies that setting the focus-frame
// rune to 0 disables the highlight overlay entirely (no character is
// painted on the highlight row).
func TestTabsDrawHighlightDisabled(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(20, 2)
	l.SetAttr(term.Attributes{}, term.Attributes{},
		term.Attributes{}, term.Attributes{},
		term.Attributes{Fg: term.ColorRed}, term.Attributes{}, term.Attributes{})
	l.SetFocusFrameChar(0)
	l.Add('A', "alpha")
	l.Add('B', "beta")
	l.SetFocus(1)

	w := term.NewStringWriter(20, 2)
	l.Draw(w)
	cells := w.Cells()

	// y=0 (the highlight row) must be left untouched.
	for x := 0; x < 20; x++ {
		assert.Equal(t, rune(0), cells[x].Ch,
			"highlight row must be empty at x=%d", x)
		assert.Equal(t, term.Attributes{}, cells[x].Attributes(),
			"highlight row must carry no attr at x=%d", x)
	}
}

// TestTabsDrawBottomHighlight verifies SetBottomHighlight(true) renders
// the focus-frame highlight on the bottom row of the tab bar and keeps
// labels anchored to y=0 in borderless mode (no y=1 push-down).
func TestTabsDrawBottomHighlight(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.SetBottomHighlight(true)
	l.Resize(20, 2)

	focusFrameAttr := term.Attributes{Fg: term.ColorRed}
	l.SetAttr(term.Attributes{}, term.Attributes{},
		term.Attributes{}, term.Attributes{},
		focusFrameAttr, term.Attributes{}, term.Attributes{})
	l.SetFocusFrameChar('━')

	l.Add('A', "alpha")
	l.Add('B', "beta")
	l.SetFocus(1)

	w := term.NewStringWriter(20, 2)
	l.Draw(w)
	cells := w.Cells()

	// y=0 carries the labels. "A alpha" then "  " separator then
	// "B beta" starting at column 9.
	assert.Equal(t, 'A', cells[0].Ch)
	assert.Equal(t, 'B', cells[9].Ch)

	// y=1 (the bottom row in a height=2 borderless bar) carries the
	// highlight only over the focused tab's cell columns (9..14).
	for x := 0; x < 20; x++ {
		c := cells[20+x]
		if x >= 9 && x <= 14 {
			assert.Equal(t, '━', c.Ch,
				"expected highlight rune at x=%d on bottom row", x)
			assert.Equal(t, focusFrameAttr, c.Attributes(),
				"expected focus-frame attr at x=%d on bottom row", x)
		} else {
			assert.NotEqual(t, '━', c.Ch,
				"unexpected highlight at x=%d on bottom row", x)
		}
	}
}

func TestTabsDrawIconAttr(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(30, 1)

	focusAttr := term.Attributes{Fg: term.ColorWhite}
	nonFocusAttr := term.Attributes{Fg: term.ColorBlue}
	iconAttr := term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold}
	l.SetAttr(focusAttr, nonFocusAttr, term.Attributes{}, term.Attributes{},
		term.Attributes{}, term.Attributes{}, term.Attributes{})

	l.Add('A', "alpha")
	l.Add('B', "beta")
	l.ResetFocus()
	l.SetFocus(0)
	l.SetIconAttr(1, iconAttr)

	w := term.NewStringWriter(30, 1)
	l.Draw(w)
	cells := w.Cells()

	assert.Equal(t, 'A', cells[0].Ch)
	assert.Equal(t, term.Attributes{}, cells[0].Attributes())
	assert.Equal(t, 'a', cells[2].Ch)
	assert.Equal(t, focusAttr, cells[2].Attributes())

	assert.Equal(t, 'B', cells[9].Ch)
	assert.Equal(t, iconAttr, cells[9].Attributes())
	assert.Equal(t, term.Attributes{}, cells[10].Attributes())
	assert.Equal(t, 'b', cells[11].Ch)
	assert.Equal(t, nonFocusAttr, cells[11].Attributes())
}

// TestTabsSetAttrIconAttrs verifies SetAttr's focus/non-focus icon attributes
// are applied to tab icons that don't have a per-tab icon attr override, and
// that per-tab overrides take precedence (via AttributesUnion semantics).
func TestTabsSetAttrIconAttrs(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(30, 1)

	focusAttr := term.Attributes{Fg: term.ColorWhite}
	nonFocusAttr := term.Attributes{Fg: term.ColorBlue}
	focusIconAttr := term.Attributes{Fg: term.ColorGreen, Attrs: term.AttrBold}
	nonFocusIconAttr := term.Attributes{Fg: term.ColorRed}
	l.SetAttr(focusAttr, nonFocusAttr, focusIconAttr, nonFocusIconAttr,
		term.Attributes{}, term.Attributes{}, term.Attributes{})

	l.Add('A', "alpha")
	l.Add('B', "beta")
	l.ResetFocus()
	l.SetFocus(0)

	w := term.NewStringWriter(30, 1)
	l.Draw(w)
	cells := w.Cells()

	assert.Equal(t, 'A', cells[0].Ch)
	assert.Equal(t, focusIconAttr, cells[0].Attributes())
	assert.Equal(t, 'a', cells[2].Ch)
	assert.Equal(t, focusAttr, cells[2].Attributes())

	assert.Equal(t, 'B', cells[9].Ch)
	assert.Equal(t, nonFocusIconAttr, cells[9].Attributes())
	assert.Equal(t, 'b', cells[11].Ch)
	assert.Equal(t, nonFocusAttr, cells[11].Attributes())
}

func TestTabsDrawCustomSeparator(t *testing.T) {
	l := NewTabs()
	l.Resize(20, 4)
	l.SetNameSeparator(" | ")

	w := term.NewStringWriter(20, 9)

	tests := []comptest.TestCase{
		{
			nil, `
┌──────────────────┐
│                  │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
		}, {
			func() { l.Add('#', "Atzari") }, `
┌━━━━━━━━──────────┐
│# Atzari          │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
		}, {
			// innerW=18, budgetW=16. "# Atzari" + " | " + "# Saturn" =
			// 8+3+8=19 > 16 → resize path. Focused (Atzari, full=8)
			// keeps full; Saturn gets 16 - 8 - 3 = 5 columns, hard-cut
			// to "# Sat".
			func() {
				l.Add('#', "Saturn")
			}, `
┌━━━━━━━━──────────┐
│# Atzari | # Sat  │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
		},
	}

	comptest.TestComponent(t, l, w, tests)
}

func setupOneTab(width, height int) *Tabs {
	l := NewTabs()
	l.Resize(width, height)

	const name = "blah"
	l.Add('#', name)

	return l
}

// TestTabsDrawResize exercises the resize algorithm at a width wide enough
// that the algorithm's individual cases — full-fit, fair-share with
// give-back, leftover distributed leftmost-first, window pruning when even
// min width doesn't fit, and tie-breaking shrink direction — each show up
// in their own inline literal. Tab full widths chosen for clarity:
//
//	"A alpha"=7, "B beta"=6, "C gamma"=7, "D delta"=7,
//	"E epsilon"=9, "F zeta"=6.
//
// Separator is 2 chars. Width 40 → innerW=38, budgetW=36; width 30 → 28
// / 26; width 20 → 18 / 16. budgetW = innerW - tabBarRightPad.
func TestTabsDrawResize(t *testing.T) {
	l := NewTabs()
	l.Resize(40, 4)

	w := term.NewStringWriter(40, 4)

	tests := []comptest.TestCase{
		// 4 tabs, focused idx 0. Full sum 7+6+7+7=27 + 3*2 seps = 33 ≤ 36.
		// Everyone renders full width; remaining 5 columns become
		// trailing pad.
		{
			func() {
				l.Add('A', "alpha")
				l.Add('B', "beta")
				l.Add('C', "gamma")
				l.Add('D', "delta")
			}, `
┌━━━━━━━───────────────────────────────┐
│A alpha  B beta  C gamma  D delta     │
│                                      │
└──────────────────────────────────────┘`,
		},
		// Add "E epsilon". Total full 42 + 4*2 seps = 50 > 36. Focused
		// (A, 7) keeps full; remaining = 36 - 7 - 8 = 21 over 4
		// non-focused. fairShare=5, leftover=1 → leftmost (B) gets +1
		// then caps cascade. Widths: A=7, B=6, C=6, D=5, E=5.
		{
			func() { l.Add('E', "epsilon") }, `
┌━━━━━━━───────────────────────────────┐
│A alpha  B beta  C gam  D del  E eps  │
│                                      │
└──────────────────────────────────────┘`,
		},
		// Shift focus to E (idx 4, full=9). remaining=36-9-8=19 over 4.
		// fairShare=4, leftover=3 → A,B,C get +1. Widths: A=5, B=5,
		// C=5, D=4, E=9.
		{
			func() { l.ResetFocus(); l.SetFocus(4) }, `
┌───────────────────────────━━━━━━━━━──┐
│A alp  B bet  C gam  D de  E epsilon  │
│                                      │
└──────────────────────────────────────┘`,
		},
		// Add "F zeta" (idx 5). 6 tabs, focus still E. remaining =
		// 36 - 9 - 5*2 = 17 over 5. fairShare=3, leftover=2 → A,B get
		// +1. Widths: A=4, B=4, C=3, D=3, F=3.
		{
			func() { l.Add('F', "zeta") }, `
┌──────────────────────━━━━━━━━━───────┐
│A al  B be  C g  D d  E epsilon  F z  │
│                                      │
└──────────────────────────────────────┘`,
		},
		// Resize to width=30, innerW=28, budgetW=26. Focus still E.
		// Window [0..5] at min fits: 9 + 5 + 10 = 24 ≤ 26. remaining =
		// 26 - 9 - 10 = 7 over 5. fairShare=1, leftover=2 → A,B get
		// +1. Widths: A=2, B=2, C=1, D=1, F=1.
		{
			func() { l.Resize(30, 4) }, `
┌──────────────━━━━━━━━━─────┐          
│A   B   C  D  E epsilon  F  │          
│                            │          
└────────────────────────────┘          `,
		},
		// Resize to width=20, innerW=18, budgetW=16. Focus still E
		// (idx 4). Window [0..5] at min 9+5+10=24 > 16 → prune until
		// [3..5] (min 9+2+4=15 ≤ 16). Visible: D, E (focused), F.
		// remaining = 16-9-4 = 3 over 2. fairShare=1, leftover=1 →
		// D gets +1. Widths: D=2, E=9, F=1.
		{
			func() { l.Resize(20, 4) }, `
┌────━━━━━━━━━─────┐                    
│D   E epsilon  F  │                    
│                  │                    
└──────────────────┘                    `,
		},
		// Focus to A (idx 0, full=7) at width=20, budgetW=16.
		// Window [0..5] min 22 > 16 → prune. Ends at [0..3] (min
		// 7+3+6=16 ≤ 16). remaining = 16-7-6 = 3 over 3. fairShare=1,
		// leftover=0. Widths: A=7, B=1, C=1, D=1.
		{
			func() { l.ResetFocus(); l.SetFocus(0) }, `
┌━━━━━━━───────────┐                    
│A alpha  B  C  D  │                    
│                  │                    
└──────────────────┘                    `,
		},
		// Focus to C (idx 2, full=7) at width=20 — tie-breaking in
		// window shrink. Window shrinks to [1..3] (min 7+2+4=13 ≤ 16).
		// remaining = 16-7-4 = 5 over 2. fairShare=2, leftover=1 →
		// B gets +1. Widths: B=3, C=7, D=2.
		{
			func() { l.ResetFocus(); l.SetFocus(2) }, `
┌───━━━━━━━────────┐                    
│B  C gamma  D  E  │                    
│                  │                    
└──────────────────┘                    `,
		},
	}

	comptest.TestComponent(t, l, w, tests)
}

// TestTabsTabRect checks that TabRect lands on the cells Draw actually
// paints a tab's label into, across the frame and highlight modes that
// move the label row and a layout that shrinks or hides tabs.
func TestTabsTabRect(t *testing.T) {
	type want struct {
		x, y, width int
	}
	for _, tc := range []struct {
		name          string
		width, height int
		border        bool
		bottom        bool
		separator     string
		noIcon        bool
		action        rune
		tabs          []string
		focus         int
		want          map[int]want
	}{
		{
			name: "bordered", width: 20, height: 3, border: true,
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {3, 1, 5}, 1: {12, 1, 4}},
		},
		{
			name: "bordered tall centres the label", width: 20, height: 9,
			border: true, tabs: []string{"alpha"},
			want: map[int]want{0: {3, 4, 5}},
		},
		{
			name: "borderless below highlight", width: 20, height: 2,
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 1, 5}, 1: {11, 1, 4}},
		},
		{
			name: "borderless tall below highlight", width: 20, height: 3,
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 2, 5}, 1: {11, 2, 4}},
		},
		{
			name: "borderless taller below highlight", width: 20, height: 4,
			tabs: []string{"alpha"},
			want: map[int]want{0: {2, 2, 5}},
		},
		{
			name: "borderless tallest below highlight", width: 20, height: 5,
			tabs: []string{"alpha"},
			want: map[int]want{0: {2, 3, 5}},
		},
		{
			name: "borderless tall bottom highlight", width: 20, height: 3,
			bottom: true, tabs: []string{"alpha"},
			want: map[int]want{0: {2, 1, 5}},
		},
		{
			name: "borderless bottom highlight", width: 20, height: 2,
			bottom: true, tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 0, 5}, 1: {11, 0, 4}},
		},
		{
			name: "borderless single row", width: 20, height: 1,
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 0, 5}, 1: {11, 0, 4}},
		},
		{
			name: "custom separator", width: 20, height: 1,
			separator: " | ", tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 0, 5}, 1: {12, 0, 4}},
		},
		{
			name: "without icons", width: 20, height: 1, noIcon: true,
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {0, 0, 5}, 1: {7, 0, 4}},
		},
		{
			name: "action glyph", width: 30, height: 1, action: 'x',
			tabs: []string{"alpha", "beta"},
			want: map[int]want{0: {2, 0, 8}, 1: {14, 0, 7}},
		},
		{
			// Same geometry as TestTabsDrawResize at width 20 with
			// focus on E: D, E and F are visible, the rest are not,
			// and D and F shrank to their icons.
			name: "shrunk layout hides tabs", width: 20, height: 4,
			border: true, focus: 4,
			tabs: []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"},
			want: map[int]want{4: {7, 1, 7}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewTabs()
			l.SetBorder(tc.border)
			l.SetBottomHighlight(tc.bottom)
			if tc.separator != "" {
				l.SetNameSeparator(tc.separator)
			}
			l.Resize(tc.width, tc.height)
			icon := func(idx int) rune {
				if tc.noIcon {
					return 0
				}
				return rune(tc.tabs[idx][0] - 'a' + 'A')
			}
			for idx, name := range tc.tabs {
				l.Add(icon(idx), name)
				if tc.action != 0 {
					l.SetTabAction(idx, tc.action, term.Attributes{})
				}
			}
			l.ResetFocus()
			l.SetFocus(tc.focus)

			_, _, ok := l.TabRect(0)
			assert.False(t, ok, "no layout before the first draw")

			w := term.NewStringWriter(tc.width, tc.height)
			l.Draw(w)
			cells := w.Cells()

			for idx := range tc.tabs {
				offset, width, ok := l.TabRect(idx)
				exp, visible := tc.want[idx]
				require.Equal(t, visible, ok, "tab %d", idx)
				if !visible {
					continue
				}
				assert.Equal(t, exp, want{offset.X, offset.Y, width}, "tab %d", idx)
				// The rect opens on the tab's name, leaving the icon
				// and the blank after it outside.
				row := cells[offset.Y*tc.width : (offset.Y+1)*tc.width]
				assert.Equal(t, rune(tc.tabs[idx][0]), row[offset.X].Ch, "tab %d", idx)
				if !tc.noIcon {
					assert.Equal(t, icon(idx), row[offset.X-2].Ch, "tab %d", idx)
					assert.Equal(t, ' ', row[offset.X-1].Ch, "tab %d", idx)
				}
			}
			_, _, ok = l.TabRect(len(tc.tabs))
			assert.False(t, ok, "an index past the tabs is not laid out")
		})
	}
}

func TestTabsTabAt(t *testing.T) {
	t.Run("should return false if no tab in list", func(t *testing.T) {
		l := NewTabs()
		_, ok := l.TabAt(term.Coordinates{})
		assert.False(t, ok)
	})

	t.Run("should return first tab if coordinates is zero", func(t *testing.T) {
		l := setupOneTab(4, 4)

		idx, ok := l.TabAt(term.Coordinates{})
		require.True(t, ok)
		assert.Equal(t, "blah", l.tabs[idx].name)
	})

	t.Run("should return first tab if size is 0", func(t *testing.T) {
		l := setupOneTab(0, 0)

		l.TabAt(term.Coordinates{})
		idx, ok := l.TabAt(term.Coordinates{})
		require.True(t, ok)
		assert.Equal(t, "blah", l.tabs[idx].name)
	})

	t.Run("should resolve the focused tab when not all tabs fit", func(t *testing.T) {
		l := setupOneTab(6, 4)

		l.Add(0, "1111")
		l.Add(0, "2222222222222222222")
		l.Add(0, "3")
		l.SetFocus(l.Add(0, "4"))

		// force calculating offsets
		w := term.NewStringWriter(6, 4)
		l.Draw(w)
		require.NoError(t, w.Flush())

		idx, ok := l.TabAt(term.Coordinates{})
		require.True(t, ok)
		assert.Equal(t, "4", l.tabs[idx].name)
	})
}

// TestTabsTabIconAt scans every cell around the bar and checks that
// TabIconAt hits exactly the cells Draw paints an icon into, across the
// frame and highlight modes that move the label row and layouts that
// shrink, hide or blank tabs.
func TestTabsTabIconAt(t *testing.T) {
	const wide = '界'
	type tabSpec struct {
		icon   rune
		name   string
		action rune
	}
	letters := func(names ...string) []tabSpec {
		specs := make([]tabSpec, len(names))
		for i, name := range names {
			specs[i] = tabSpec{icon: rune(name[0] - 'a' + 'A'), name: name}
		}
		return specs
	}
	withAction := func(specs []tabSpec) []tabSpec {
		for i := range specs {
			specs[i].action = 'x'
		}
		return specs
	}
	for _, tc := range []struct {
		name          string
		width, height int
		border        bool
		bottom        bool
		separator     string
		tabs          []tabSpec
		focus         int
		row           int
		// icons maps each tab whose icon is drawn to its first column.
		icons map[int]int
	}{
		{
			name: "bordered", width: 20, height: 3, border: true,
			tabs: letters("alpha", "beta"), row: 1,
			icons: map[int]int{0: 1, 1: 10},
		},
		{
			name: "bordered tall centres the icon", width: 20, height: 9,
			border: true, tabs: letters("alpha"), row: 4,
			icons: map[int]int{0: 1},
		},
		{
			name: "borderless below highlight", width: 20, height: 2,
			tabs: letters("alpha", "beta"), row: 1,
			icons: map[int]int{0: 0, 1: 9},
		},
		{
			name: "borderless tall below highlight", width: 20, height: 3,
			tabs: letters("alpha", "beta"), row: 2,
			icons: map[int]int{0: 0, 1: 9},
		},
		{
			name: "borderless tallest below highlight", width: 20, height: 5,
			tabs: letters("alpha"), row: 3,
			icons: map[int]int{0: 0},
		},
		{
			name: "borderless bottom highlight", width: 20, height: 2,
			bottom: true, tabs: letters("alpha", "beta"), row: 0,
			icons: map[int]int{0: 0, 1: 9},
		},
		{
			name: "borderless single row", width: 20, height: 1,
			tabs: letters("alpha", "beta"), row: 0,
			icons: map[int]int{0: 0, 1: 9},
		},
		{
			name: "custom separator", width: 20, height: 1,
			separator: " | ", tabs: letters("alpha", "beta"), row: 0,
			icons: map[int]int{0: 0, 1: 10},
		},
		{
			name: "tabs without icons", width: 20, height: 1,
			tabs: []tabSpec{{name: "alpha"}, {name: "beta"}}, row: 0,
		},
		{
			name: "only the tabs with an icon", width: 20, height: 1,
			tabs: []tabSpec{{name: "alpha"}, {icon: 'B', name: "beta"}}, row: 0,
			icons: map[int]int{1: 7},
		},
		{
			name: "action glyph is not the icon", width: 30, height: 1,
			tabs: withAction(letters("alpha", "beta")), row: 0,
			icons: map[int]int{0: 0, 1: 12},
		},
		{
			// B and C shrink to two cells, too narrow for the action
			// glyph, so they drop it and keep their icons.
			name: "shrunk cell drops the action glyph", width: 20, height: 1,
			tabs: withAction(letters("alpha", "beta", "gamma")), row: 0,
			icons: map[int]int{0: 0, 1: 12, 2: 16},
		},
		{
			// B and C shrink to three cells, which the action glyph's
			// block takes whole, so they are drawn blank.
			name: "cell taken by the action glyph is blank", width: 22, height: 1,
			tabs: withAction(letters("alpha", "beta", "gamma")), row: 0,
			icons: map[int]int{0: 0},
		},
		{
			// Same geometry as TestTabsDrawResize at width 20 with
			// focus on E: D and F shrank to their icons and the rest
			// are scrolled out of view.
			name: "shrunk layout hides tabs", width: 20, height: 4,
			border: true, focus: 4, row: 1,
			tabs:  letters("alpha", "beta", "gamma", "delta", "epsilon", "zeta"),
			icons: map[int]int{3: 1, 4: 5, 5: 16},
		},
		{
			name: "wide icons", width: 20, height: 1, row: 0,
			tabs:  []tabSpec{{icon: wide, name: "alpha"}, {icon: wide, name: "beta"}},
			icons: map[int]int{0: 0, 1: 10},
		},
		{
			// The focused tab leaves a single cell to the last one,
			// which cannot hold a two-cell icon.
			name: "wide icon without room is left out", width: 18, height: 1,
			focus: 1, row: 0,
			tabs: []tabSpec{
				{icon: wide, name: "aaaa"},
				{icon: wide, name: "bbbbbbbbbb"},
				{icon: wide, name: "c"},
			},
			icons: map[int]int{1: 0},
		},
		{
			name:  "bar narrower than the focused tab keeps its icon",
			width: 3, height: 1, row: 0,
			tabs:  letters("alpha"),
			icons: map[int]int{0: 0},
		},
		{
			name:  "bar narrower than the focused tab's wide icon",
			width: 3, height: 1, row: 0,
			tabs: []tabSpec{{icon: wide, name: "alpha"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewTabs()
			l.SetBorder(tc.border)
			l.SetBottomHighlight(tc.bottom)
			if tc.separator != "" {
				l.SetNameSeparator(tc.separator)
			}
			l.Resize(tc.width, tc.height)
			icons := map[rune]bool{}
			for idx, spec := range tc.tabs {
				l.Add(spec.icon, spec.name)
				if spec.action != 0 {
					l.SetTabAction(idx, spec.action, term.Attributes{})
				}
				if spec.icon != 0 {
					icons[spec.icon] = true
				}
			}
			l.ResetFocus()
			l.SetFocus(tc.focus)

			w := term.NewStringWriter(tc.width, tc.height)
			l.Draw(w)
			cells := w.Cells()
			row := cells[tc.row*tc.width : (tc.row+1)*tc.width]

			want := map[term.Coordinates]int{}
			for idx, x := range tc.icons {
				icon := tc.tabs[idx].icon
				require.Equal(t, icon, row[x].Ch, "tab %d icon", idx)
				for dx := range runeCellWidth(icon) {
					want[term.Coordinates{X: x + dx, Y: tc.row}] = idx
				}
			}
			for x, c := range row {
				if icons[c.Ch] {
					_, ok := want[term.Coordinates{X: x, Y: tc.row}]
					assert.True(t, ok, "icon %q drawn at %d is not expected", c.Ch, x)
				}
			}

			for y := -1; y <= tc.height; y++ {
				for x := -1; x <= tc.width; x++ {
					pos := term.Coordinates{X: x, Y: y}
					wantIdx, wantOK := want[pos]
					if !wantOK {
						wantIdx = -1
					}
					idx, ok := l.TabIconAt(pos)
					assert.Equal(t, wantOK, ok, "at %+v", pos)
					assert.Equal(t, wantIdx, idx, "at %+v", pos)
				}
			}
		})
	}

	t.Run("not before the first draw", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(20, 1)
		l.Add('A', "alpha")

		_, ok := l.TabAt(term.Coordinates{})
		require.True(t, ok)
		_, ok = l.TabIconAt(term.Coordinates{})
		assert.False(t, ok)
	})

	t.Run("tab removed since the last draw", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(20, 1)
		l.Add('A', "alpha")
		l.Add('B', "beta")
		l.Draw(term.NewStringWriter(20, 1))

		idx, ok := l.TabIconAt(term.Coordinates{X: 9})
		require.True(t, ok)
		require.Equal(t, 1, idx)

		l.Remove(1)
		_, ok = l.TabIconAt(term.Coordinates{X: 9})
		assert.False(t, ok)
		idx, ok = l.TabIconAt(term.Coordinates{X: 0})
		assert.True(t, ok)
		assert.Equal(t, 0, idx)
	})

	t.Run("icon cleared since the last draw", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(20, 1)
		l.Add('A', "alpha")
		l.Draw(term.NewStringWriter(20, 1))

		l.SetTabIcon(0, 0)
		_, ok := l.TabIconAt(term.Coordinates{})
		assert.False(t, ok)
	})

	t.Run("no tabs", func(t *testing.T) {
		l := NewTabs()
		l.Resize(20, 3)
		l.Draw(term.NewStringWriter(20, 3))

		_, ok := l.TabIconAt(term.Coordinates{X: 1, Y: 1})
		assert.False(t, ok)
	})
}

// TestTabsDrawFocusLast regression-tests the case where the focused tab is
// the rightmost tab in the list and the bar doesn't have enough room for
// every tab at full width. The focused tab must keep its full label and
// non-focused tabs must shrink to fit; visually the focused (rightmost)
// tab should NOT be truncated while preceding non-focused tabs are.
func TestTabsDrawFocusLast(t *testing.T) {
	l := NewTabs()
	l.SetBorder(false)
	l.Resize(60, 1)

	w := term.NewStringWriter(60, 1)

	// Tab full widths (icon + space + name; we use icon=0 so full = name
	// width):
	//   ".golangci.yml" = 13
	//   "context.go"    = 10
	//   "events.go"     =  9
	//   "file.go"       =  7
	//   "handler_test.go" = 15
	//   "component.go"  = 12
	// Σ = 66, + 5 seps · 2 = 76 > budgetW=58 → resize path. Focus on
	// the last tab (idx 5, full=12). remaining = 58 − 12 − 10 = 36 over
	// 5. fairShare = 7, leftover = 1 → leftmost (.golangci.yml) gets
	// +1. Widths: .golangci.yml=8, context.go=7, events.go=7,
	// file.go=7 (cap), handler_test=7.
	tests := []comptest.TestCase{
		{
			func() {
				l.Add(0, ".golangci.yml")
				l.Add(0, "context.go")
				l.Add(0, "events.go")
				l.Add(0, "file.go")
				l.Add(0, "handler_test.go")
				l.Add(0, "component.go")
				l.ResetFocus()
				l.SetFocus(5)
			}, `
.golangc  context  events.  file.go  handler  component.go  `,
		},
	}

	comptest.TestComponent(t, l, w, tests)
}

// TestTabsDrawRightPadding regression-tests that the tab bar always
// reserves at least 2 blank cells at the right edge of the rendered
// row, so the rightmost tab is never flush against the viewport edge.
// The invariant holds in both the fast path (everything fits at full
// width) and the resize path (truncation kicks in).
func TestTabsDrawRightPadding(t *testing.T) {
	t.Run("fast path borderless", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(20, 1)
		// Two tabs summing to 17 cells + 2 sep = 19. Without the
		// reservation padAfter=1, leaving cell 19 untouched (Ch=0).
		l.Add('A', "alphaXX")
		l.Add('B', "beta56789")
		l.SetFocus(0)

		w := term.NewStringWriter(20, 1)
		l.Draw(w)
		require.NoError(t, w.Flush())
		cells := w.Cells()

		assert.Equal(t, ' ', cells[18].Ch,
			"cell at x=18 must be blank (reserved right pad)")
		assert.Equal(t, ' ', cells[19].Ch,
			"cell at x=19 must be blank (reserved right pad)")
	})

	t.Run("resize path borderless", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(20, 1)
		for _, n := range []string{
			"alpha", "beta", "gamma", "delta", "epsilon", "zeta",
		} {
			l.Add(0, n)
		}
		l.SetFocus(5)

		w := term.NewStringWriter(20, 1)
		l.Draw(w)
		require.NoError(t, w.Flush())
		cells := w.Cells()

		assert.Equal(t, ' ', cells[18].Ch,
			"cell at x=18 must be blank (reserved right pad)")
		assert.Equal(t, ' ', cells[19].Ch,
			"cell at x=19 must be blank (reserved right pad)")
	})

	t.Run("bordered fast path", func(t *testing.T) {
		l := NewTabs()
		l.Resize(20, 4)
		// innerW=18 (border eats 2). Two tabs summing to 15 cells + 2
		// sep = 17. Without reservation padAfter=1, leaving x=18
		// (inside the right border at column 18) untouched.
		l.Add('A', "alphaXX")
		l.Add('B', "beta567")
		l.SetFocus(0)

		w := term.NewStringWriter(20, 4)
		l.Draw(w)
		require.NoError(t, w.Flush())
		cells := w.Cells()

		// Tab labels live on y=1, columns [1..18] (inside the border).
		// Reserved pad lives at columns 17 and 18 on the label row.
		assert.Equal(t, ' ', cells[20+17].Ch,
			"label-row cell at x=17 must be blank (reserved right pad)")
		assert.Equal(t, ' ', cells[20+18].Ch,
			"label-row cell at x=18 must be blank (reserved right pad)")
	})

	t.Run("bordered resize path", func(t *testing.T) {
		l := NewTabs()
		l.Resize(20, 4)
		for _, n := range []string{
			"alpha", "beta", "gamma", "delta", "epsilon", "zeta",
		} {
			l.Add(0, n)
		}
		l.SetFocus(5)

		w := term.NewStringWriter(20, 4)
		l.Draw(w)
		require.NoError(t, w.Flush())
		cells := w.Cells()

		assert.Equal(t, ' ', cells[20+17].Ch,
			"label-row cell at x=17 must be blank (reserved right pad)")
		assert.Equal(t, ' ', cells[20+18].Ch,
			"label-row cell at x=18 must be blank (reserved right pad)")
	})

	// When innerW < tabBarRightPad the budget collapses to 0 and no
	// tabs can be laid out; the row stays empty so the reservation is
	// trivially honored.
	t.Run("narrower than reservation", func(t *testing.T) {
		l := NewTabs()
		l.SetBorder(false)
		l.Resize(1, 1)
		l.Add(0, "alpha")
		l.SetFocus(0)

		w := term.NewStringWriter(1, 1)
		l.Draw(w)
		require.NoError(t, w.Flush())

		assert.Empty(t, l.layout.cells)
	})
}
