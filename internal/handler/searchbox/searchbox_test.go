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
	"image"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
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

func (m *testWindowManager) SetTabStatus(workspaceapi.URI, browserapi.TabStatus) error { return nil }

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

func TestFloatingButtonImagesLayout(t *testing.T) {
	tests := []struct {
		name   string
		images *component.ButtonImages
		// before, between and after are the cells around the buttons.
		before, between, after int
	}{
		{name: "without images", before: 1, between: 1, after: 1},
		{name: "images without sides", images: &component.ButtonImages{
			Idle: newButtonImage("idle", false),
		}, before: 1, between: 1, after: 1},
		{name: "sides make room", images: &component.ButtonImages{
			Idle:   newButtonImage("idle", false),
			Outset: component.ButtonOutset{Top: 1, Bottom: 1, Sides: 2},
		}, before: 2, between: 4, after: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.ButtonImages = func() *component.ButtonImages { return tt.images }
			f := searchbox.NewFloating(searchbox.New(new(replaceController), cfg),
				searchbox.ModeReplace, "")
			width, height := f.Dimensions()
			assert.Equal(t, 8, height, "the images take no rows of their own")
			f.Resize(width, height)

			query, next, all := f.QueryRect(), f.NextRect(), f.AllRect()
			assert.Equal(t, 32, query.Width, "the input keeps its width")
			assert.Equal(t, tt.before, next.X-(query.X+query.Width))
			assert.Equal(t, tt.between, all.X-(next.X+next.Width))
			assert.Equal(t, tt.after, width-(all.X+all.Width))
		})
	}
}

func TestFloatingButtonImages(t *testing.T) {
	cfg := testConfig()
	cfg.Attr = term.Attributes{Bg: term.ColorBlack}
	cfg.ButtonAttr = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorGray}
	cfg.ButtonHoverAttr = term.Attributes{Fg: term.ColorYellow, Bg: term.ColorBlue}
	outset := component.ButtonOutset{Top: 1, Bottom: 1, Sides: 2}
	images := &component.ButtonImages{
		Idle:    newButtonImage("idle", false),
		Hover:   newButtonImage("hover", false),
		Pressed: newButtonImage("pressed", false),
		Outset:  outset,
	}
	hover := func(f *searchbox.Floating) { f.SetHover(searchbox.ButtonUpgrade) }
	press := func(f *searchbox.Floating) {
		r := f.UpgradeRect()
		f.Handle(mouseEvent(term.MouseLeft, r.X, r.Y))
	}
	tests := []struct {
		name    string
		images  *component.ButtonImages
		refuse  bool
		setup   func(*searchbox.Floating)
		want    string
		wantFg  term.Attributes
		noImage bool
	}{
		{name: "without images", wantFg: cfg.ButtonAttr, noImage: true},
		{name: "idle", images: images, want: "idle",
			wantFg: term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack}},
		{name: "hover", images: images, setup: hover, want: "hover",
			wantFg: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorBlack}},
		{name: "pressed", images: images, setup: press, want: "pressed",
			wantFg: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorBlack}},
		{name: "pressed without its images is hovered", setup: press, want: "hover",
			images: &component.ButtonImages{
				Idle: images.Idle, Hover: images.Hover, Outset: outset,
			},
			wantFg: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorBlack}},
		{name: "images still loading", images: &component.ButtonImages{
			Idle: newButtonImage("loading", true), Outset: outset,
		}, wantFg: cfg.ButtonAttr, noImage: true},
		{name: "writer draws no images", images: images, refuse: true,
			wantFg: cfg.ButtonAttr, noImage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfg
			cfg.ButtonImages = func() *component.ButtonImages { return tt.images }
			f := searchbox.NewFloating(searchbox.New(new(replaceController), cfg),
				searchbox.ModeFind, "")
			width, height := f.Dimensions()
			f.Resize(width, height)
			if tt.setup != nil {
				tt.setup(f)
			}
			w := &imageWriter{StringWriter: term.NewStringWriter(width, height), refuse: tt.refuse}
			f.Draw(w)

			r := f.UpgradeRect()
			cells := w.Cells()
			for x := r.X; x < r.X+r.Width; x++ {
				assert.Equal(t, tt.wantFg, cells[r.Y*width+x].Attributes(), "label cell %d", x)
			}
			if tt.noImage {
				assert.Empty(t, w.images)
				return
			}
			require.NotEmpty(t, w.images)
			var bounds image.Rectangle
			for _, img := range w.images {
				assert.Equal(t, tt.want, buttonImageNames[img.ID])
				assert.Equal(t, term.ImageLayerBelowText, img.Layer)
				bounds = bounds.Union(img.Bounds())
			}
			assert.Equal(t, image.Rect(r.X-outset.Sides, r.Y-outset.Top,
				r.X+r.Width+outset.Sides, r.Y+1+outset.Bottom), bounds,
				"the images reach the outset around the label")
			assert.True(t, bounds.In(image.Rect(0, 0, width, height)),
				"the images stay in the box")
		})
	}
}

var buttonImageNames = map[term.ImageID]string{}

// newButtonImage returns images that draw name over all of their cells,
// or nothing while loading, as an image that has not loaded yet does.
func newButtonImage(name string, loading bool) component.ButtonImage {
	img := &buttonImage{id: term.NewImageID(), loading: loading}
	buttonImageNames[img.id] = name
	return component.ButtonImage{Left: img, Middle: img, Right: img}
}

type buttonImage struct {
	id            term.ImageID
	width, height int
	loading       bool
}

func (i *buttonImage) Resize(width, height int) { i.width, i.height = width, height }

func (i *buttonImage) Draw(w term.Writer) {
	if !i.loading {
		w.DrawImage(term.Image{ID: i.id, Width: i.width, Height: i.height})
	}
}

// imageWriter records the images drawn on it, unless it refuses them as
// a writer that cannot draw images does.
type imageWriter struct {
	*term.StringWriter
	refuse bool
	images []term.Image
}

func (w *imageWriter) DrawImage(img term.Image) bool {
	if w.refuse {
		return false
	}
	w.images = append(w.images, img)
	return true
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
	assert.Equal(t, 1, ctrl.finished)
	assert.False(t, box.Active())
}

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

func TestFloatingCloseLeavesItsWindow(t *testing.T) {
	wm := &testWindowManager{}
	box, ctrl := newReplaceBox(t, wm)
	openMode(t, box, searchbox.ModeFind)
	f := box.Floating()
	require.NotNil(t, f)

	require.NoError(t, f.Close())

	assert.Zero(t, wm.closed,
		"the window may already show other content when the box is released")
	assert.Equal(t, 1, ctrl.finished)
	assert.False(t, box.Active())
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

func TestFloatingCopyShortcutFallsThroughWithNoOwnSelection(t *testing.T) {
	cfg := testConfig()
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

func TestFloatingPointerLeaves(t *testing.T) {
	for _, mode := range []searchbox.Mode{searchbox.ModeFind, searchbox.ModeReplace} {
		for _, size := range [][2]int{{48, 9}, {60, 15}, {1, 1}} {
			cfg := testConfig()
			cfg.WindowManager = new(testWindowManager)
			ctrl := new(replaceController)
			box := searchbox.New(ctrl, cfg)
			openMode(t, box, mode)
			f := box.Floating()
			require.NotNil(t, f)
			f.Resize(48, 9)
			if f.HasReplacement() {
				f.HandleReplacement(term.Event{Type: term.EventKey, Ch: 'x'})
			}
			r := f.UpgradeRect()
			if mode == searchbox.ModeReplace {
				r = f.NextRect()
			}
			f.Handle(mouseEvent(0, r.X, r.Y))
			require.NotEqual(t, searchbox.ButtonNone, f.Hover())
			f.Handle(mouseEvent(0, -1, r.Y))
			assert.Equal(t, searchbox.ButtonNone, f.Hover(), "a leave drops the hover")
			assert.Equal(t, mode, f.Mode())

			rhandlertest.RunPointerLeaves(t, f, size[0], size[1])
		}
	}
}
