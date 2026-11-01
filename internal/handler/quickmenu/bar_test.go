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

package quickmenu

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/sounds"
)

// The bar of these tests fills the last 3 columns of a 10x5 window from
// row 1. Its cells are 4x5 pixels, so its buttons are 8 pixels square,
// 2 apart and 2 in from the column's edges: First is on rows 1 and 2,
// and Second on rows 3 and 4, both over the middle of the column's
// first and last columns.
const (
	testWidth, testHeight = 10, 5
	testColumns, testTop  = 3, 1
)

var testCell = cell.PixelSize{Width: 4, Height: 5}

func TestBar(t *testing.T) {
	pics := newPictures(t, "abcdef")
	idle := `
..........
.......aa.
.......aa.
.......dd.
.......dd.`
	firstHovered := `
..........
.......bb.
.First.bb.
.......dd.
.......dd.`
	firstClicked := `
..........
.......bb.
.......bb.
.......dd.
.......dd.`
	secondHovered := `
..........
.......aa.
.......aa.
.......ee.
.Secondee.`
	firstPressed := `
..........
.......cc.
.......cc.
.......dd.
.......dd.`

	tests := []struct {
		name      string
		steps     []step
		activated []int
		expected  string
	}{
		{
			name:     "every button starts idle",
			expected: idle,
		},
		{
			name:     "the pointer over a button shows its hover image and title",
			steps:    []step{{move(8, 1), false}},
			expected: firstHovered,
		},
		{
			name:     "a title is cut at the window's left edge",
			steps:    []step{{move(8, 3), false}},
			expected: secondHovered,
		},
		{
			name:     "the window's last column never hovers",
			steps:    []step{{move(9, 1), false}},
			expected: idle,
		},
		{
			name:     "the window's last row never hovers",
			steps:    []step{{move(8, 4), false}},
			expected: idle,
		},
		{
			name:     "moving off a button leaves it idle",
			steps:    []step{{move(8, 1), false}, {move(2, 1), false}},
			expected: idle,
		},
		{
			name:     "pressing a button shows its pressed image",
			steps:    []step{{move(8, 1), false}, {mouse(term.MouseLeft, 8, 1), true}},
			expected: firstPressed,
		},
		{
			name: "a click activates the button",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true}, {mouse(term.MouseRelease, 8, 1), true},
			},
			activated: []int{0},
			expected:  firstClicked,
		},
		{
			name: "a click hides the title until the pointer leaves the button",
			steps: []step{
				{move(8, 1), false},
				{mouse(term.MouseLeft, 8, 1), true},
				{mouse(term.MouseRelease, 8, 1), true},
				{move(8, 2), false},
			},
			activated: []int{0},
			expected:  firstClicked,
		},
		{
			name: "leaving a clicked button shows its title again on return",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true},
				{mouse(term.MouseRelease, 8, 1), true},
				{move(2, 1), false},
				{move(8, 1), false},
			},
			activated: []int{0},
			expected:  firstHovered,
		},
		{
			name: "a click on the last column activates the button",
			steps: []step{
				{mouse(term.MouseLeft, 9, 3), true}, {mouse(term.MouseRelease, 9, 3), true},
			},
			activated: []int{1},
			expected:  idle,
		},
		{
			name: "dragging off the pressed button shows every button idle",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true}, {mouse(term.MouseLeft, 8, 3), true},
			},
			expected: idle,
		},
		{
			name: "dragging back onto the pressed button presses it again",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true},
				{mouse(term.MouseLeft, 2, 0), true},
				{mouse(term.MouseLeft, 8, 2), true},
			},
			expected: firstPressed,
		},
		{
			name: "releasing off the pressed button activates nothing",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true},
				{mouse(term.MouseLeft, 8, 3), true},
				{mouse(term.MouseRelease, 8, 3), true},
			},
			expected: secondHovered,
		},
		{
			name: "a press that begins off the buttons is left to what is under the bar",
			steps: []step{
				{mouse(term.MouseLeft, 2, 1), false},
				{mouse(term.MouseLeft, 8, 1), false},
				{mouse(term.MouseRelease, 8, 1), false},
			},
			expected: firstHovered,
		},
		{
			name: "a drag that begins off the buttons hovers none",
			steps: []step{
				{mouse(term.MouseLeft, 2, 1), false}, {mouse(term.MouseLeft, 8, 1), false},
			},
			expected: idle,
		},
		{
			name: "other mouse buttons are left to what is under the bar",
			steps: []step{
				{mouse(term.MouseRight, 8, 1), false},
				{mouse(term.MouseRelease, 8, 1), false},
				{mouse(term.MouseMiddle, 8, 3), false},
				{mouse(term.MouseRelease, 8, 3), false},
			},
			expected: secondHovered,
		},
		{
			name: "other mouse buttons during a press do not end it",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true},
				{mouse(term.MouseRight, 8, 1), true},
				{mouse(term.MouseRelease, 8, 1), true},
			},
			activated: []int{0},
			expected:  firstClicked,
		},
		{
			name: "a motion ends a press whose release went elsewhere",
			steps: []step{
				{mouse(term.MouseLeft, 8, 1), true},
				{move(8, 3), false},
				{mouse(term.MouseRelease, 8, 1), false},
			},
			expected: firstHovered,
		},
		{
			name:     "the wheel is left to what is under the bar",
			steps:    []step{{mouse(term.MouseWheelDown, 8, 1), false}},
			expected: idle,
		},
		{
			name:     "keys are left to what is under the bar",
			steps:    []step{{term.Event{Type: term.EventKey, Key: term.KeyEnter}, false}},
			expected: idle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var activated []int
			b := newTestBar(t, func(i int) { activated = append(activated, i) },
				pics.button("First", "a", "b", "c"), pics.button("Second", "d", "e", "f"))
			for i, s := range tt.steps {
				_, handled := b.Handle(s.ev)
				assert.Equal(t, s.handled, handled, "step %d handled", i)
			}
			assert.Equal(t, tt.activated, activated)
			assert.Equal(t, tt.expected, render(t, b, pics, false))
		})
	}
}

func TestBarFallsBack(t *testing.T) {
	pics := newPictures(t, "ab")
	tests := []struct {
		name     string
		steps    []step
		noImages bool
		expected string
	}{
		{
			name: "a button without its idle image shows its title's first letter",
			expected: `
..........
.......aa.
.......aa.
........S.
..........`,
		},
		{
			name:  "a hover without its image shows the idle one",
			steps: []step{{move(8, 1), false}},
			expected: `
..........
.......aa.
.first.aa.
........S.
..........`,
		},
		{
			name:  "a press without its image shows the hover one",
			steps: []step{{mouse(term.MouseLeft, 8, 3), true}},
			expected: `
..........
.......aa.
.......aa.
.......bb.
.......bb.`,
		},
		{
			name:     "a writer that cannot draw images gets the first letters",
			noImages: true,
			expected: `
..........
........F.
..........
........S.
..........`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBar(t, nil,
				pics.button("first", "a", "missing", "missing"),
				pics.button("second", "missing", "b", "missing"))
			for i, s := range tt.steps {
				_, handled := b.Handle(s.ev)
				assert.Equal(t, s.handled, handled, "step %d handled", i)
			}
			assert.Equal(t, tt.expected, render(t, b, pics, tt.noImages))
		})
	}
}

func TestBarLaysButtonsOutLikeTheNativeBar(t *testing.T) {
	pics := newPictures(t, "a")
	for _, size := range []cell.PixelSize{
		{Width: 8, Height: 17}, {Width: 16, Height: 34},
		{Width: 7.5, Height: 16.25}, {Width: 9.6, Height: 19.2},
	} {
		t.Run(fmt.Sprintf("%vx%v", size.Width, size.Height), func(t *testing.T) {
			b := newTestBar(t, nil, pics.button("First", "a", "a", "a"),
				pics.button("Second", "a", "a", "a"), pics.button("Third", "a", "a", "a"))
			b.Resize(40, 30)
			s := newImageScreen(40, 30, size)
			b.Draw(s)
			require.Len(t, s.images, 3)

			pad := size.Width / 2
			side := float64(testColumns-1) * size.Width
			for i, img := range s.images {
				r := raster(img, size)
				top := float64(testTop)*size.Height + pad + float64(i)*(side+pad)
				assert.InDelta(t, float64(40-testColumns)*size.Width+pad, r.Min.X, 1,
					"button %d left", i)
				assert.InDelta(t, top, r.Min.Y, 1, "button %d top", i)
				assert.InDelta(t, side, r.Dx(), 1, "button %d width", i)
				assert.InDelta(t, side, r.Dy(), 1, "button %d height", i)
			}
		})
	}
}

func TestBarSnapsToCellsWithoutACellSize(t *testing.T) {
	pics := newPictures(t, "a")
	b := New(Config{
		Buttons:     []Button{pics.button("First", "a", "a", "a")},
		Columns:     testColumns,
		Top:         testTop,
		Storage:     storagestub.NewInMemoryService(),
		Interrupter: term.NopInterrupter(),
	})
	t.Cleanup(func() { assert.NoError(t, b.Close()) })
	b.Resize(testWidth, testHeight)
	require.Eventually(t, func() bool {
		s := newImageScreen(testWidth, testHeight, cell.PixelSize{})
		b.Draw(s)
		return len(s.images) == 1
	}, 5*time.Second, 10*time.Millisecond)

	s := newImageScreen(testWidth, testHeight, cell.PixelSize{})
	b.Draw(s)
	require.Len(t, s.images, 1)
	assert.Zero(t, s.images[0].Offset)
	assert.Equal(t, term.Coordinates{X: testWidth - testColumns, Y: testTop}, s.images[0].Pos)
}

func TestBarClipsButtonsToTheWindow(t *testing.T) {
	pics := newPictures(t, "abc")
	b := newTestBar(t, nil,
		pics.button("First", "a", "a", "a"),
		pics.button("Second", "b", "b", "b"),
		pics.button("Third", "c", "c", "c"))
	b.Resize(testWidth, 4)

	assert.Equal(t, 1, b.buttonAt(8, 3))
	assert.Equal(t, -1, b.buttonAt(8, 4), "Third is below the window")
	assert.Equal(t, -1, b.buttonAt(6, 1), "left of the column")
	assert.Equal(t, -1, b.buttonAt(8, 0), "above the bar")

	s := newImageScreen(testWidth, 4, testCell)
	b.Draw(s)
	require.Len(t, s.images, 2)
	assert.Equal(t, image.Rect(7, 1, 10, 4), s.images[1].Clip)
}

func TestBarDelaysTheTitle(t *testing.T) {
	const delay = 500 * time.Millisecond
	type tipStep struct {
		ev    *term.Event
		wait  time.Duration
		shown string
	}
	on := func(ev term.Event, shown string) tipStep { return tipStep{ev: &ev, shown: shown} }
	after := func(d time.Duration, shown string) tipStep { return tipStep{wait: d, shown: shown} }

	tests := []struct {
		name  string
		steps []tipStep
	}{
		{
			name: "the title waits for the delay",
			steps: []tipStep{
				on(move(8, 1), ""), after(delay-time.Millisecond, ""),
				after(time.Millisecond, "First"),
			},
		},
		{
			name: "moving within a button keeps its delay running",
			steps: []tipStep{
				on(move(8, 1), ""), after(300*time.Millisecond, ""),
				on(move(8, 2), ""), after(200*time.Millisecond, "First"),
			},
		},
		{
			name: "moving to another button restarts the delay",
			steps: []tipStep{
				on(move(8, 1), ""), after(300*time.Millisecond, ""),
				on(move(8, 3), ""), after(300*time.Millisecond, ""),
				after(200*time.Millisecond, "Second"),
			},
		},
		{
			name: "leaving a button hides its title and restarts the delay",
			steps: []tipStep{
				on(move(8, 1), ""), after(delay, "First"),
				on(move(2, 1), ""), on(move(8, 1), ""), after(delay, "First"),
			},
		},
		{
			name: "a press hides the title until the pointer leaves the button",
			steps: []tipStep{
				on(move(8, 1), ""), after(delay, "First"),
				on(mouse(term.MouseLeft, 8, 1), ""), on(mouse(term.MouseRelease, 8, 1), ""),
				after(time.Second, ""), on(move(8, 2), ""), after(time.Second, ""),
				on(move(2, 1), ""), on(move(8, 1), ""), after(delay, "First"),
			},
		},
	}
	pics := newPictures(t, "ab")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBar(t, nil,
				pics.button("First", "a", "a", "a"), pics.button("Second", "b", "b", "b"))
			clock := time.Unix(0, 0)
			b.now = func() time.Time { return clock }
			b.cfg.TooltipDelay = delay
			for i, s := range tt.steps {
				if s.ev != nil {
					b.Handle(*s.ev)
				}
				clock = clock.Add(s.wait)
				out := render(t, b, pics, false)
				shown := ""
				for _, title := range []string{"First", "Second"} {
					if strings.Contains(out, title) {
						shown = title
					}
				}
				assert.Equal(t, s.shown, shown, "step %d", i)
			}
		})
	}
}

func TestBarInterruptsWhenTheTitleIsDue(t *testing.T) {
	pics := newPictures(t, "a")
	b := newTestBar(t, nil, pics.button("First", "a", "a", "a"))
	irq := make(interruptCounter, 4)
	b.cfg.Interrupter, b.cfg.TooltipDelay = irq, 20*time.Millisecond

	b.Handle(move(8, 1))
	irq.wait(t, 1)
	assert.Contains(t, render(t, b, pics, false), "First")

	b.cfg.TooltipDelay = 100 * time.Millisecond
	b.Handle(move(2, 1))
	b.Handle(move(8, 1))
	require.NoError(t, b.Close())
	select {
	case <-irq:
		assert.Fail(t, "a closed bar must not interrupt")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestBarPlaysSounds(t *testing.T) {
	pics := newPictures(t, "ab")
	tests := []struct {
		name     string
		sounds   Sounds
		noPlayer bool
		events   []term.Event
		played   []string
	}{
		{
			name:   "moving onto a button plays hover",
			events: []term.Event{move(8, 1)},
			played: []string{"tick"},
		},
		{
			name:   "moving within a button plays nothing more",
			events: []term.Event{move(8, 1), move(8, 2)},
			played: []string{"tick"},
		},
		{
			name:   "moving onto another button plays hover again",
			events: []term.Event{move(8, 1), move(8, 3)},
			played: []string{"tick", "tick"},
		},
		{
			name:   "coming back to a button plays hover again",
			events: []term.Event{move(8, 1), move(2, 1), move(8, 1)},
			played: []string{"tick", "tick"},
		},
		{
			name:   "moving off the buttons plays nothing",
			events: []term.Event{move(2, 1), move(9, 1)},
		},
		{
			name: "a click plays click",
			events: []term.Event{
				move(8, 1), mouse(term.MouseLeft, 8, 1), mouse(term.MouseRelease, 8, 1),
			},
			played: []string{"tick", "clack"},
		},
		{
			name: "a click on the last column plays click",
			events: []term.Event{
				mouse(term.MouseLeft, 9, 1), mouse(term.MouseRelease, 9, 1),
			},
			played: []string{"clack"},
		},
		{
			name: "a press released off its button plays no click",
			events: []term.Event{
				move(8, 1), mouse(term.MouseLeft, 8, 1),
				mouse(term.MouseLeft, 2, 1), mouse(term.MouseRelease, 2, 1),
			},
			played: []string{"tick"},
		},
		{
			name: "a press released over another button plays its hover",
			events: []term.Event{
				move(8, 1), mouse(term.MouseLeft, 8, 1),
				mouse(term.MouseLeft, 8, 3), mouse(term.MouseRelease, 8, 3),
			},
			played: []string{"tick", "tick"},
		},
		{
			name: "a drag from elsewhere plays hover where it is released",
			events: []term.Event{
				mouse(term.MouseLeft, 2, 1), mouse(term.MouseLeft, 8, 1),
				mouse(term.MouseRelease, 8, 1),
			},
			played: []string{"tick"},
		},
		{
			name:   "an empty name plays nothing",
			sounds: Sounds{Click: "clack"},
			events: []term.Event{
				move(8, 1), mouse(term.MouseLeft, 8, 1), mouse(term.MouseRelease, 8, 1),
			},
			played: []string{"clack"},
		},
		{
			name:     "a bar without a player plays nothing",
			noPlayer: true,
			events: []term.Event{
				move(8, 1), mouse(term.MouseLeft, 8, 1), mouse(term.MouseRelease, 8, 1),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBar(t, func(int) {},
				pics.button("First", "a", "a", "a"), pics.button("Second", "b", "b", "b"))
			var played []string
			b.cfg.Sounds = Sounds{Hover: "tick", Click: "clack"}
			if tt.sounds != (Sounds{}) {
				b.cfg.Sounds = tt.sounds
			}
			if !tt.noPlayer {
				b.cfg.Player = sounds.PlayerFunc(func(name string) {
					played = append(played, name)
				})
			}
			for _, ev := range tt.events {
				b.Handle(ev)
			}
			assert.Equal(t, tt.played, played)
		})
	}
}

func TestBarPointerShape(t *testing.T) {
	pics := newPictures(t, "a")
	b := newTestBar(t, nil, pics.button("First", "a", "a", "a"))

	shape := func(ev term.Event) (term.PointerShape, bool) {
		p := new(term.Pointer)
		ev.Context = term.ContextWithPointer(context.Background(), p)
		b.Handle(ev)
		return p.Shape()
	}
	s, set := shape(move(8, 1))
	assert.True(t, set)
	assert.Equal(t, term.PointerLink, s, "over a button")

	_, set = shape(move(2, 1))
	assert.False(t, set, "off the buttons")

	s, _ = shape(mouse(term.MouseLeft, 8, 1))
	assert.Equal(t, term.PointerLink, s, "pressing a button")

	_, set = shape(mouse(term.MouseLeft, 2, 1))
	assert.False(t, set, "dragging a press off its button")

	_, set = shape(mouse(term.MouseRelease, 2, 1))
	assert.False(t, set, "released off the buttons")

	_, set = shape(mouse(term.MouseLeft, 2, 1))
	require.False(t, set)
	_, set = shape(mouse(term.MouseLeft, 8, 1))
	assert.False(t, set, "a drag from elsewhere is not the bar's")
}

func TestBarPointerLeaves(t *testing.T) {
	pics := newPictures(t, "a")
	for _, size := range [][2]int{{testWidth, testHeight}, {testColumns, 2}, {1, 1}, {0, 0}} {
		b := newTestBar(t, func(int) {}, pics.button("First", "a", "a", "a"))
		handlertest.RunPointerLeaves(t, b, size[0], size[1])
	}
}

type step struct {
	ev      term.Event
	handled bool
}

func move(x, y int) term.Event {
	return term.Event{Type: term.EventMouse, MouseX: x, MouseY: y}
}

func mouse(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

// newTestBar returns a bar sized to the window of these tests whose
// images have all settled, drawn once to learn the size of a cell.
func newTestBar(t *testing.T, activate func(int), buttons ...Button) *Bar {
	t.Helper()
	irq := make(interruptCounter, 16)
	b := New(Config{
		Buttons:     buttons,
		Columns:     testColumns,
		Top:         testTop,
		Storage:     storagestub.NewInMemoryService(),
		Interrupter: irq,
		Activate:    activate,
	})
	t.Cleanup(func() { assert.NoError(t, b.Close()) })
	b.Resize(testWidth, testHeight)
	irq.wait(t, len(b.images))
	b.Draw(newImageScreen(testWidth, testHeight, testCell))
	return b
}

// pictures holds one solid-colored picture on disk per name, and
// "missing" names a file that does not exist.
type pictures struct {
	uris  map[string]string
	names map[color.RGBA]rune
}

func newPictures(t *testing.T, names string) pictures {
	t.Helper()
	dir := t.TempDir()
	fileURI := func(name string) string {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, name))}).String()
	}
	p := pictures{
		uris:  map[string]string{"missing": fileURI("missing.png")},
		names: make(map[color.RGBA]rune),
	}
	for i, name := range names {
		c := color.RGBA{R: uint8(40 * (i + 1)), G: 10, B: 20, A: 255}
		img := image.NewRGBA(image.Rect(0, 0, 4, 4))
		for y := range 4 {
			for x := range 4 {
				img.SetRGBA(x, y, c)
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		file := string(name) + ".png"
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), buf.Bytes(), 0o600))
		p.uris[string(name)] = fileURI(file)
		p.names[c] = name
	}
	return p
}

func (p pictures) button(title, idle, hover, pressed string) Button {
	return Button{Title: title, Idle: p.uris[idle], Hover: p.uris[hover], Pressed: p.uris[pressed]}
}

// render draws b and returns the window with each cell whose middle the
// picture of an image lands on replaced by the picture's name, and
// blanks shown as dots.
func render(t *testing.T, b *Bar, pics pictures, noImages bool) string {
	t.Helper()
	s := newImageScreen(testWidth, testHeight, testCell)
	s.noImages = noImages
	b.Draw(s)
	require.NoError(t, s.Flush())

	rows := strings.Split(s.String(), "\n")
	screen := make([][]rune, testHeight)
	for y := range screen {
		screen[y] = []rune(strings.Repeat(" ", testWidth))
		if y < len(rows) {
			copy(screen[y], []rune(rows[y]))
		}
	}
	for _, img := range s.images {
		assert.Equal(t, term.ImageFitContain, img.Fit)
		assert.Equal(t, term.ImageLayerAboveText, img.Layer)
		rgba, ok := img.Src.(*image.RGBA)
		require.True(t, ok)
		name, ok := pics.names[rgba.RGBAAt(0, 0)]
		require.True(t, ok, "an unknown picture")
		r := raster(img, testCell).Intersect(testCell.Pixels(img.Clip))
		for y := range testHeight {
			for x := range testWidth {
				mx := (float64(x) + 0.5) * testCell.Width
				my := (float64(y) + 0.5) * testCell.Height
				if mx >= float64(r.Min.X) && mx < float64(r.Max.X) &&
					my >= float64(r.Min.Y) && my < float64(r.Max.Y) {
					screen[y][x] = name
				}
			}
		}
	}
	var out strings.Builder
	for _, row := range screen {
		out.WriteByte('\n')
		out.WriteString(strings.ReplaceAll(string(row), " ", "."))
	}
	return out.String()
}

// raster returns the pixels the square picture of img lands on: centred
// in the pixels of its cells, then moved by its offset.
func raster(img term.Image, size cell.PixelSize) image.Rectangle {
	area := size.Pixels(img.Bounds()).Add(img.Offset)
	side := min(area.Dx(), area.Dy())
	at := area.Min.Add(image.Pt((area.Dx()-side)/2, (area.Dy()-side)/2))
	return image.Rectangle{Min: at, Max: at.Add(image.Pt(side, side))}
}

// imageScreen is a string writer, with cells of size when it is not
// zero, that records the images drawn into it, or reports that it cannot
// draw them when noImages is set.
type imageScreen struct {
	*term.StringWriter
	ctx      context.Context
	images   []term.Image
	noImages bool
}

func newImageScreen(width, height int, size cell.PixelSize) *imageScreen {
	ctx := context.Background()
	if size != (cell.PixelSize{}) {
		ctx = cell.ContextWithPixelSize(ctx, size)
	}
	return &imageScreen{StringWriter: term.NewStringWriter(width, height), ctx: ctx}
}

func (s *imageScreen) Context() context.Context { return s.ctx }

func (s *imageScreen) DrawImage(img term.Image) bool {
	if s.noImages {
		return false
	}
	s.images = append(s.images, img)
	return true
}

// interruptCounter is a term.Interrupter that queues every interrupt.
type interruptCounter chan struct{}

func (c interruptCounter) Interrupt(context.Context) error {
	c <- struct{}{}
	return nil
}

func (c interruptCounter) wait(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for the images to load")
		}
	}
}
