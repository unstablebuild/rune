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

package searchbox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/searchbox"
	"unstable.build/rune/internal/text/standard"
)

type testWindow uint64

func (w testWindow) WindowID() uint64 { return uint64(w) }

type testWindowManager struct {
	floating browserapi.Floating
	config   browserapi.FloatingConfig
	closed   int
	err      error
	closeErr error
	// closeContent forwards CloseWindow to the floating content, the way
	// a real window manager tears down the window it owns.
	closeContent bool
}

func (m *testWindowManager) Floating(
	f browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.floating, m.config = f, cfg
	if m.err != nil {
		return nil, m.err
	}
	return testWindow(1), nil
}

func (m *testWindowManager) CloseWindow(browserapi.Window) error {
	m.closed++
	if m.closeContent && m.floating != nil {
		return errors.Join(m.closeErr, m.floating.Close())
	}
	return m.closeErr
}

func (m *testWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

// findController records the calls a find-only host would receive.
type findController struct {
	begun    int
	finished int
	advanced int
	query    string
	last     string
	origin   term.Coordinates
}

func (c *findController) BeginSearch(query []rune, origin term.Coordinates) {
	c.begun++
	c.query, c.origin = string(query), origin
}
func (c *findController) SetSearchQuery(query string)    { c.query = query }
func (c *findController) AdvanceSearch()                 { c.advanced++ }
func (c *findController) FinishSearch()                  { c.finished++ }
func (c *findController) SearchLast() string             { return c.last }
func (c *findController) SearchOrigin() term.Coordinates { return c.origin }
func (c *findController) Selection() (string, bool)      { return "", false }

type replaceController struct {
	findController
	next []string
	all  []string
}

func (c *replaceController) ReplaceNext(replacement string) {
	c.next = append(c.next, replacement)
}

func (c *replaceController) ReplaceAll(replacement string) {
	c.all = append(c.all, replacement)
}

// selectionController is a host whose own content carries a selection,
// like a terminal the reader dragged over while the box had focus.
type selectionController struct {
	findController
	selection string
}

func (c *selectionController) Selection() (string, bool) {
	return c.selection, c.selection != ""
}

func testConfig() searchbox.Config {
	return searchbox.Config{
		Editor:          standard.Editor(),
		FindKey:         term.KeyComb{Mod: term.ModMeta, Ch: 'f'},
		ReplaceKey:      term.KeyComb{Mod: term.ModMeta, Ch: 'r'},
		PaddingTop:      1,
		FocusFrameAttr:  term.Attributes{Fg: term.ColorSilver},
		ButtonAttr:      term.Attributes{Bg: term.ColorGray},
		ButtonHoverAttr: term.Attributes{Bg: term.ColorBlue},
	}
}

func newReplaceBox(t *testing.T, wm searchbox.WindowManager) (*searchbox.Box, *replaceController) {
	t.Helper()
	cfg := testConfig()
	cfg.WindowManager = wm
	ctrl := new(replaceController)
	return searchbox.New(ctrl, cfg), ctrl
}

func golden(width int, lines ...string) string {
	for i, line := range lines {
		lines[i] = line + strings.Repeat(" ", max(0, width-len([]rune(line))))
	}
	return strings.Join(lines, "\n")
}

func mouseEvent(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

func TestFloatingDimensionsAreIdeal(t *testing.T) {
	tests := []struct {
		name, query, replacement string
		mode                     searchbox.Mode
		wantWidth, wantHeight    int
	}{
		{name: "empty find", mode: searchbox.ModeFind, wantWidth: 44, wantHeight: 4},
		{name: "empty replace", mode: searchbox.ModeReplace, wantWidth: 50, wantHeight: 8},
		{name: "long find", mode: searchbox.ModeFind, query: strings.Repeat("q", 80),
			wantWidth: 95, wantHeight: 4},
		{name: "wide replace", mode: searchbox.ModeReplace, query: "界界",
			replacement: strings.Repeat("界", 30), wantWidth: 81, wantHeight: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box, _ := newReplaceBox(t, nil)
			f := searchbox.NewFloating(box, tt.mode, tt.query)
			if f.HasReplacement() {
				for _, ch := range tt.replacement {
					f.HandleReplacement(term.Event{Type: term.EventKey, Ch: ch})
				}
			}
			beforeW, beforeH := f.Dimensions()
			assert.Equal(t, tt.wantWidth, beforeW)
			assert.Equal(t, tt.wantHeight, beforeH)
			f.Resize(beforeW, beforeH)
			assert.Equal(t, searchbox.PaddingX, f.QueryRect().X)
			assert.Equal(t, beforeH, f.ContentHeight())
			last := f.AllRect()
			if tt.mode == searchbox.ModeFind {
				last = f.UpgradeRect()
			}
			assert.Equal(t, beforeW-searchbox.PaddingX, last.X+last.Width)

			f.Resize(13, 3)
			w := term.NewStringWriter(13, 3)
			f.Draw(w)
			require.NoError(t, w.Flush())
			afterW, afterH := f.Dimensions()
			assert.Equal(t, beforeW, afterW)
			assert.Equal(t, beforeH, afterH)
		})
	}
}

func TestFloatingConstrainedResizeDrawAndSeek(t *testing.T) {
	tests := []struct {
		name          string
		mode          searchbox.Mode
		query         string
		replacement   string
		width, height int
		wantHeight    int
		want          string
	}{
		{name: "find wraps wide query", mode: searchbox.ModeFind, query: "界界界界",
			width: 8, height: 3, wantHeight: 8, want: golden(8, " │界│", " │▐│", " └─┘")},
		{name: "replace scrolls focused replacement", mode: searchbox.ModeReplace,
			query: "abcdef", replacement: "123456", width: 12, height: 4, wantHeight: 20,
			want: golden(12, " │5│", " │6│", " │▐│", " └─┘")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box, _ := newReplaceBox(t, nil)
			f := searchbox.NewFloating(box, tt.mode, tt.query)
			if f.HasReplacement() {
				for _, ch := range tt.replacement {
					f.HandleReplacement(term.Event{Type: term.EventKey, Ch: ch})
				}
				f.FocusReplacement()
			}
			assert.Equal(t, tt.wantHeight, f.Height(tt.width))
			f.Resize(tt.width, tt.height)
			assert.LessOrEqual(t, f.SeekOffset(), f.MaxSeekOffset())
			assert.GreaterOrEqual(t, f.SeekOffset(), 0)
			assert.Equal(t, tt.want, handlertest.DrawHandler(f, tt.width, tt.height))
			for f.SeekDown() {
			}
			assert.Equal(t, f.MaxSeekOffset(), f.SeekOffset())
			assert.False(t, f.SeekDown())
			for f.SeekUp() {
			}
			assert.Zero(t, f.SeekOffset())
			assert.False(t, f.SeekUp())
		})
	}
}

func TestFloatingMouseStateAndActions(t *testing.T) {
	tests := []struct {
		name   string
		button searchbox.Button
		mode   searchbox.Mode
		action func(*searchbox.Floating, searchbox.Rect)
		assert func(*testing.T, *searchbox.Floating, *replaceController)
	}{
		{name: "upgrade", mode: searchbox.ModeFind, button: searchbox.ButtonUpgrade,
			action: clickButton,
			assert: func(t *testing.T, f *searchbox.Floating, _ *replaceController) {
				assert.Equal(t, searchbox.ModeReplace, f.Mode())
			}},
		{name: "replace next", mode: searchbox.ModeReplace, button: searchbox.ButtonNext,
			action: clickButton,
			assert: func(t *testing.T, _ *searchbox.Floating, c *replaceController) {
				assert.Equal(t, []string{"x"}, c.next)
			}},
		{name: "replace all", mode: searchbox.ModeReplace, button: searchbox.ButtonAll,
			action: clickButton,
			assert: func(t *testing.T, _ *searchbox.Floating, c *replaceController) {
				assert.Equal(t, []string{"x"}, c.all)
			}},
		{name: "drag off", mode: searchbox.ModeReplace, button: searchbox.ButtonNext,
			action: func(f *searchbox.Floating, r searchbox.Rect) {
				f.Handle(mouseEvent(term.MouseLeft, r.X, r.Y))
				f.Handle(mouseEvent(term.MouseRelease, r.X-1, r.Y))
			},
			assert: func(t *testing.T, _ *searchbox.Floating, c *replaceController) {
				assert.Empty(t, c.next)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.WindowManager = new(testWindowManager)
			ctrl := new(replaceController)
			box := searchbox.New(ctrl, cfg)
			openMode(t, box, tt.mode)
			f := box.Floating()
			require.NotNil(t, f)
			f.Resize(48, 9)
			if f.HasReplacement() {
				f.HandleReplacement(term.Event{Type: term.EventKey, Ch: 'x'})
			}
			r := map[searchbox.Button]searchbox.Rect{
				searchbox.ButtonUpgrade: f.UpgradeRect(),
				searchbox.ButtonNext:    f.NextRect(),
				searchbox.ButtonAll:     f.AllRect(),
			}[tt.button]

			_, handled := f.Handle(mouseEvent(0, r.X, r.Y))
			assert.True(t, handled)
			assert.Equal(t, tt.button, f.Hover())
			w := term.NewStringWriter(48, 9)
			f.Draw(w)
			assert.Equal(t, cfg.ButtonHoverAttr, w.Cells()[r.Y*48+r.X].Attributes())
			f.Handle(mouseEvent(0, 47, 0))
			assert.Equal(t, searchbox.ButtonNone, f.Hover())

			tt.action(f, r)
			tt.assert(t, f, ctrl)
		})
	}
}

func TestFloatingMouseInputFocusAndHitboxEdges(t *testing.T) {
	box, _ := newReplaceBox(t, new(testWindowManager))
	openMode(t, box, searchbox.ModeReplace)
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(48, 9)

	r := f.ReplacementRect()
	x, y := r.X+1, r.Y+1
	_, handled := f.Handle(mouseEvent(term.MouseLeft, x, y))
	assert.True(t, handled)
	f.Handle(mouseEvent(term.MouseRelease, x, y))
	assert.True(t, f.ReplacementFocused())
	f.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.Equal(t, "x", f.ReplacementText())
	assert.Empty(t, f.QueryText())

	for _, r := range []searchbox.Rect{f.NextRect(), f.AllRect()} {
		assert.NotEqual(t, searchbox.ButtonNone, f.ButtonAt(r.X, r.Y))
		assert.NotEqual(t, searchbox.ButtonNone, f.ButtonAt(r.X+r.Width-1, r.Y))
		assert.Equal(t, searchbox.ButtonNone, f.ButtonAt(r.X+r.Width, r.Y))
	}
}

func TestFloatingMouseSelection(t *testing.T) {
	tests := []struct {
		name         string
		mode         searchbox.Mode
		query        string
		replacement  string
		selectQuery  bool
		displayStart int
		displayWidth int
		drag         bool
		height       int
		want         string
		frame        string
	}{
		{
			name: "double-click query word", mode: searchbox.ModeFind, query: "one two",
			selectQuery: true, displayStart: 4, displayWidth: 3, height: 5, want: "two",
			frame: golden(48,
				"", " ┌──────────────────────────────┐",
				" │one two▐                      │  Replace ",
				" └──────────────────────────────┘", ""),
		},
		{
			name: "double-click replacement word", mode: searchbox.ModeReplace,
			query: "needle", replacement: "red blue", displayStart: 4, displayWidth: 4,
			height: 9, want: "blue",
			frame: golden(48,
				"", " ┌────────────────────────────┐", " │needle                      │",
				" └────────────────────────────┘", "", " ┌────────────────────────────┐",
				" │red blue▐                   │  Replace   All  ",
				" └────────────────────────────┘", ""),
		},
		{
			name: "drag query word", mode: searchbox.ModeFind, query: "one two",
			selectQuery: true, displayStart: 4, displayWidth: 3, drag: true, height: 5,
			want: "two",
			frame: golden(48,
				"", " ┌──────────────────────────────┐",
				" │one two▐                      │  Replace ",
				" └──────────────────────────────┘", ""),
		},
		{
			name: "wide character before query word", mode: searchbox.ModeFind, query: "界 two",
			selectQuery: true, displayStart: 3, displayWidth: 3, height: 5, want: "two",
			frame: golden(48,
				"", " ┌──────────────────────────────┐",
				" │界  two▐                       │  Replace ",
				" └──────────────────────────────┘", ""),
		},
		{
			name: "double-click wide query word", mode: searchbox.ModeFind, query: "one 界",
			selectQuery: true, displayStart: 4, displayWidth: 2, height: 5, want: "界",
			frame: golden(48,
				"", " ┌──────────────────────────────┐",
				" │one 界 ▐                       │  Replace ",
				" └──────────────────────────────┘", ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box, _ := newReplaceBox(t, new(testWindowManager))
			openMode(t, box, tt.mode)
			f := box.Floating()
			require.NotNil(t, f)
			f.Resize(48, tt.height)
			for _, ch := range tt.query {
				f.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			r := f.ReplacementRect()
			if tt.selectQuery {
				r = f.QueryRect()
			} else {
				f.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
				for _, ch := range tt.replacement {
					f.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
			}
			x, y := r.X+1+tt.displayStart, r.Y+1
			f.Handle(mouseEvent(term.MouseLeft, x, y))
			if tt.drag {
				f.Handle(mouseEvent(term.MouseLeft, x+tt.displayWidth, y))
			} else {
				f.Handle(mouseEvent(term.MouseRelease, x, y))
				f.Handle(mouseEvent(term.MouseLeft, x, y))
			}
			releaseX := x
			if tt.drag {
				releaseX += tt.displayWidth
			}
			f.Handle(mouseEvent(term.MouseRelease, releaseX, y))

			selected, ok := f.Selection()
			require.True(t, ok)
			assert.Equal(t, tt.want, selected)

			w := term.NewStringWriter(48, tt.height)
			handlertest.RunHandlerSequenceWriter(t, w, f, 48, tt.height,
				[]handlertest.SequenceTestCase{{Expected: tt.frame}})
			for cellX := x; cellX < x+tt.displayWidth; cellX++ {
				assert.NotZero(t, w.Cells()[y*48+cellX].Attrs&term.AttrReverse)
			}
		})
	}
}

func TestFloatingConfiguredAttributesAndPureDraw(t *testing.T) {
	cfg := testConfig()
	cfg.Attr = term.Attributes{Bg: term.ColorBlue}
	cfg.InputAttr = term.Attributes{Fg: term.ColorGreen}
	cfg.PlaceholderAttr = term.Attributes{Fg: term.ColorYellow}
	cfg.FrameAttr = term.Attributes{Fg: term.ColorRed}
	cfg.FocusFrameAttr = term.Attributes{Fg: term.ColorPurple}
	cfg.ButtonAttr = term.Attributes{Fg: term.ColorTeal}
	cfg.ButtonHoverAttr = term.Attributes{Bg: term.ColorWhite}
	f := searchbox.NewFloating(searchbox.New(new(replaceController), cfg),
		searchbox.ModeFind, "")
	f.Resize(48, 5)
	f.SetHover(searchbox.ButtonUpgrade)
	before := *f
	w := term.NewStringWriter(48, 5)
	f.Draw(w)
	assert.Equal(t, before, *f)
	cells := w.Cells()
	assert.Equal(t, cfg.Attr, cells[0].Attributes())
	assert.Equal(t, cfg.FocusFrameAttr, cells[48+1].Attributes())
	assert.Equal(t, cfg.PlaceholderAttr, cells[2*48+2].Attributes())
	upgrade := f.UpgradeRect()
	assert.Equal(t, cfg.ButtonHoverAttr, cells[upgrade.Y*48+upgrade.X].Attributes())
}

func TestFloatingReplacementEditRelayout(t *testing.T) {
	box, _ := newReplaceBox(t, new(testWindowManager))
	openMode(t, box, searchbox.ModeReplace)
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(12, 4)
	f.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	before := f.ContentHeight()
	for _, ch := range "replacement" {
		f.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	assert.Greater(t, f.ContentHeight(), before)
	pos, _, show := f.Cursor()
	assert.True(t, show)
	assert.GreaterOrEqual(t, pos.Y, 0)
	assert.Less(t, pos.Y, f.ViewportHeight())
}

func TestFloatingDimensionsAndWideWrapping(t *testing.T) {
	box, _ := newReplaceBox(t, nil)
	f := searchbox.NewFloating(box, searchbox.ModeFind, "界界界界")
	w, h := f.Dimensions()
	assert.Equal(t, 44, w)
	assert.Equal(t, 4, h)

	f.Resize(8, 3)
	assert.Equal(t, w, func() int { got, _ := f.Dimensions(); return got }())
	assert.Greater(t, f.Height(8), 5)
	assert.Greater(t, f.MaxSeekOffset(), 0)
	for f.SeekUp() {
	}
	assert.True(t, f.SeekDown())
	assert.Equal(t, 1, f.SeekOffset())

	f.Upgrade()
	w, h = f.Dimensions()
	assert.Equal(t, 50, w)
	assert.Equal(t, 8, h)
}

func TestFloatingButtonPointerAndIdempotentClose(t *testing.T) {
	wm := &testWindowManager{closeContent: true}
	box, ctrl := newReplaceBox(t, wm)
	openMode(t, box, searchbox.ModeFind)
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(48, 5)

	r := f.UpgradeRect()
	_, handled := f.Handle(term.Event{Type: term.EventMouse, MouseX: r.X, MouseY: r.Y})
	assert.True(t, handled)
	assert.Equal(t, searchbox.ButtonUpgrade, f.Hover())
	f.Handle(mouseEvent(term.MouseLeft, r.X, r.Y))
	f.Handle(mouseEvent(term.MouseRelease, r.X, r.Y))
	assert.Equal(t, searchbox.ModeReplace, f.Mode())

	require.NoError(t, f.Close())
	require.NoError(t, f.Close())
	assert.Equal(t, 1, wm.closed)
	assert.Equal(t, 1, ctrl.finished)
	assert.False(t, box.Active())
}

// TestFloatingCtrlCActsLikeEscape pins that ctrl-c dismisses the box the
// same way <esc> does, since it is the terminal's own cancel gesture and
// reads as "close this" rather than as a request to edit the query.
func TestFloatingCtrlCActsLikeEscape(t *testing.T) {
	wm := &testWindowManager{closeContent: true}
	box, ctrl := newReplaceBox(t, wm)
	openMode(t, box, searchbox.ModeFind)
	f := box.Floating()
	require.NotNil(t, f)

	exit, handled := f.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, exit, "ctrl-c must exit the box, exactly like <esc>")
	assert.True(t, handled)
	assert.Equal(t, 0, ctrl.finished, "the caller, not the box itself, closes on exit")
}

func TestBoxCloseClosesFloatingWindowOnce(t *testing.T) {
	wm := &testWindowManager{closeContent: true}
	box, _ := newReplaceBox(t, wm)
	openMode(t, box, searchbox.ModeFind)

	require.NoError(t, box.Close())
	require.NoError(t, box.Close())
	assert.Equal(t, 1, wm.closed)
	assert.False(t, box.Active())
}

func TestBoxTriggersAndLifecycle(t *testing.T) {
	wm := &testWindowManager{closeErr: errors.New("close")}
	cfg := testConfig()
	cfg.WindowManager = wm
	cfg.FindKey = term.KeyComb{Mod: term.ModCtrl, Ch: 's'}
	cfg.ReplaceKey = term.KeyComb{Mod: term.ModCtrl, Ch: 'h'}
	ctrl := new(replaceController)
	ctrl.last = "one"
	box := searchbox.New(ctrl, cfg)

	assert.False(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'}))
	assert.False(t, box.Active())

	assert.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 's'}))
	require.True(t, box.Active())
	assert.Equal(t, 1, ctrl.begun)
	assert.Equal(t, "one", box.Floating().QueryText(), "reopens with the last query")

	assert.ErrorIs(t, box.Finish(true), wm.closeErr)
	assert.Equal(t, 1, wm.closed)
	assert.False(t, box.Active())
	assert.Equal(t, 1, ctrl.finished)

	assert.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'h'}))
	require.True(t, box.Active())
	assert.Equal(t, searchbox.ModeReplace, box.Floating().Mode())
}

func TestBoxFindKeyAliasOpensAndAdvances(t *testing.T) {
	cfg := testConfig()
	cfg.WindowManager = new(testWindowManager)
	cfg.FindKeyAliases = []term.KeyComb{{Mod: term.ModCtrl, Ch: 'f'}}
	ctrl := new(replaceController)
	box := searchbox.New(ctrl, cfg)

	alias := term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'}
	assert.True(t, box.HandleKey(alias))
	require.True(t, box.Active())
	assert.True(t, box.HandleKey(alias))
	assert.Equal(t, 1, ctrl.advanced)
}

func TestBoxOpenErrorReportsAndStaysClosed(t *testing.T) {
	wm := &testWindowManager{err: errors.New("boom")}
	cfg := testConfig()
	cfg.WindowManager = wm
	var got []searchbox.Mode
	cfg.OnOpenError = func(mode searchbox.Mode, err error) {
		assert.ErrorIs(t, err, wm.err)
		got = append(got, mode)
	}
	ctrl := new(replaceController)
	box := searchbox.New(ctrl, cfg)

	box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'r'})
	assert.Equal(t, []searchbox.Mode{searchbox.ModeFind, searchbox.ModeReplace}, got)
	assert.False(t, box.Active())
	assert.Zero(t, ctrl.begun)
}

// TestFindOnlyControllerHidesReplaceAffordances pins that hosts which cannot
// replace, such as terminals, get a query-only box.
func TestFindOnlyControllerHidesReplaceAffordances(t *testing.T) {
	wm := new(testWindowManager)
	cfg := testConfig()
	cfg.WindowManager = wm
	ctrl := new(findController)
	box := searchbox.New(ctrl, cfg)

	require.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	assert.Equal(t, "Find", wm.config.Title)
	f := box.Floating()
	require.NotNil(t, f)

	assert.False(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'r'}))
	_, handled := f.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'r'})
	assert.False(t, handled)
	assert.Equal(t, searchbox.ModeFind, f.Mode())
	assert.False(t, f.HasReplacement())
	assert.Zero(t, f.UpgradeRect().Width)

	width, _ := f.Dimensions()
	f.Resize(width, 4)
	assert.Equal(t, golden(width,
		"", " ┌──────────────────────────────┐", " │▐ind                          │",
		" └──────────────────────────────┘"),
		handlertest.DrawHandler(f, width, 4))
}

// TestFloatingPaddingTopZeroSitsFlush pins that PaddingTop: 0 drops the
// box's leading blank row, so a host presenting it flush against an
// edge, such as a terminal overlay, does not need to crop anything
// itself.
func TestFloatingPaddingTopZeroSitsFlush(t *testing.T) {
	cfg := testConfig()
	cfg.PaddingTop = 0
	box := searchbox.New(new(findController), cfg)

	require.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	f := box.Floating()
	require.NotNil(t, f)

	width, height := f.Dimensions()
	assert.Equal(t, 3, height, "one less row than the padded default")
	f.Resize(width, height)
	assert.Equal(t, golden(width,
		" ┌──────────────────────────────┐", " │▐ind                          │",
		" └──────────────────────────────┘"),
		handlertest.DrawHandler(f, width, height))
}

func clickButton(f *searchbox.Floating, r searchbox.Rect) {
	f.Handle(mouseEvent(term.MouseLeft, r.X, r.Y))
	f.Handle(mouseEvent(term.MouseRelease, r.X+r.Width-1, r.Y))
}

func openMode(t *testing.T, box *searchbox.Box, mode searchbox.Mode) {
	t.Helper()
	key := term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}
	if mode == searchbox.ModeReplace {
		key.Ch = 'r'
	}
	require.True(t, box.HandleKey(key))
	require.NotNil(t, box.Floating())
}

// TestFloatingReportsHostSelection pins that a selection made in the
// content behind the box stays copyable: window managers report the
// focused window's selection, and the box has the focus.
func TestFloatingReportsHostSelection(t *testing.T) {
	cfg := testConfig()
	cfg.WindowManager = new(testWindowManager)
	ctrl := &selectionController{selection: "from the host"}
	box := searchbox.New(ctrl, cfg)

	require.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(48, 5)

	selected, ok := f.Selection()
	assert.True(t, ok)
	assert.Equal(t, "from the host", selected)

	// a selection inside the query input wins
	for _, ch := range "query" {
		f.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	r := f.QueryRect()
	x, y := r.X+1, r.Y+1
	f.Handle(mouseEvent(term.MouseLeft, x, y))
	f.Handle(mouseEvent(term.MouseRelease, x, y))
	f.Handle(mouseEvent(term.MouseLeft, x, y))
	f.Handle(mouseEvent(term.MouseRelease, x, y))

	selected, ok = f.Selection()
	assert.True(t, ok)
	assert.Equal(t, "query", selected)
}

// TestFloatingCopyShortcutFallsThroughWithNoOwnSelection pins that
// cmd-c typed while the box is open, but with nothing selected inside
// it, is not swallowed by the query input's own
// empty-selection-copies-the-line fallback. The host must get a chance
// to act on it instead, e.g. to copy the selection made on its own
// content after the box opened. ctrl-c is not covered here: it always
// exits the box, like <esc>, when nothing inside it is selected (see
// TestFloatingCtrlCActsLikeEscape).
func TestFloatingCopyShortcutFallsThroughWithNoOwnSelection(t *testing.T) {
	defer searchbox.SetCopyShortcutFor("darwin")()
	cfg := testConfig()
	cfg.Editor = standard.Editor(standard.WithHostMetaChords(true))
	cfg.WindowManager = new(testWindowManager)
	ctrl := &selectionController{selection: "from the host"}
	box := searchbox.New(ctrl, cfg)

	require.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(48, 5)

	for _, ch := range "needle" {
		f.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}

	_, handled := f.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'c'})
	assert.False(t, handled, "the box has nothing selected, so it must not claim copy")
	assert.Equal(t, "needle", f.QueryText(), "the query itself must be untouched")

	selected, ok := f.Selection()
	assert.True(t, ok)
	assert.Equal(t, "from the host", selected)
}

// TestFloatingCtrlCCopiesOwnSelectionOnLinux pins that where ctrl-c is
// also the standard editor's copy chord (Super belongs to the desktop on
// Linux), it copies a selection made inside the box instead of closing it.
func TestFloatingCtrlCCopiesOwnSelectionOnLinux(t *testing.T) {
	defer searchbox.SetCopyShortcutFor("linux")()
	cfg := testConfig()
	cfg.Editor = standard.Editor(standard.WithHostMetaChords(false))
	cfg.WindowManager = new(testWindowManager)
	box := searchbox.New(&selectionController{selection: "from the host"}, cfg)

	require.True(t, box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	f := box.Floating()
	require.NotNil(t, f)
	f.Resize(48, 5)
	for _, ch := range "query" {
		f.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	r := f.QueryRect()
	x, y := r.X+1, r.Y+1
	f.Handle(mouseEvent(term.MouseLeft, x, y))
	f.Handle(mouseEvent(term.MouseRelease, x, y))
	f.Handle(mouseEvent(term.MouseLeft, x, y))
	f.Handle(mouseEvent(term.MouseRelease, x, y))
	selected, ok := f.Selection()
	require.True(t, ok)
	require.Equal(t, "query", selected)

	exit, handled := f.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.False(t, exit, "ctrl-c must copy the box's own selection, not close it")
	assert.True(t, handled)
	assert.Equal(t, "query", f.QueryText())
}
