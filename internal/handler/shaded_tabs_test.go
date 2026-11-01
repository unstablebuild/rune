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

package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/handler/handlertest"
)

func newTestShadedTabs(cfg ShadedTabsConfig) *ShadedTabs {
	tabs := NewTabs()
	tabs.SetBorder(false)
	tabs.Add('A', "alpha")
	tabs.Add('B', "beta")
	tabs.SetFocus(0)
	s := NewShadedTabs(tabs, cfg)
	s.Resize(20, 1)
	return s
}

func drawShadedTabs(s *ShadedTabs) []term.Cell {
	w := term.NewStringWriter(20, 1)
	s.Draw(w)
	return w.Cells()
}

func TestShadedTabsRunsOnlyWhileActive(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	s := newTestShadedTabs(ShadedTabsConfig{
		Shader:      "pulse",
		Interrupter: term.NopInterrupter(),
		Active:      func() []int { return []int{1} },
	})
	plain := drawShadedTabs(s)

	require.True(t, s.SetRunning(true))
	assert.True(t, s.Running())
	assert.False(t, s.SetRunning(true),
		"starting a running effect must not report a transition")
	_ = drawShadedTabs(s)

	require.True(t, s.SetRunning(false))
	assert.False(t, s.Running())
	assert.False(t, s.SetRunning(false),
		"stopping a stopped effect must not report a transition")
	assert.Equal(t, plain, drawShadedTabs(s))
}

func TestShadedTabsLeavesIconsAlone(t *testing.T) {
	// glsl effects keep a worker pool that is reused across frames and
	// only reaped once the shader is garbage collected.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(), goleak.IgnoreAnyFunction(
		"unstable.build/rune/internal/component/shader/glslshader.(*glslHelper).initWorkers.func1"))
	s := newTestShadedTabs(ShadedTabsConfig{
		Shader:      "inferno",
		FPS:         100,
		Interrupter: term.NopInterrupter(),
		Active:      func() []int { return []int{1} },
	})
	plain := drawShadedTabs(s)
	// beta's icon sits at 9 and its name at [11, 15).
	require.Equal(t, 'B', plain[9].Ch)
	require.Equal(t, 'b', plain[11].Ch)

	require.True(t, s.SetRunning(true))
	defer s.SetRunning(false)
	// Early frames may not change a cell yet, so keep drawing until the
	// name is visibly painted; no frame may touch anything else.
	painted := false
	for deadline := time.Now().Add(5 * time.Second); !painted && time.Now().Before(deadline); {
		for x, c := range drawShadedTabs(s) {
			if x >= 11 && x < 15 {
				painted = painted || c != plain[x]
				continue
			}
			require.Equal(t, plain[x], c, "cell %d", x)
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, painted, "the effect never painted the active tab's name")
}

func TestShadedTabsColor(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	s := newTestShadedTabs(ShadedTabsConfig{
		Shader: "pulse",
		FPS:    100,
		Params: shaderloop.Params{
			Pulse: shaderloop.PulseParams{Color: term.NewRGBColor(0, 255, 0)},
		},
		DefAttr:     term.Attributes{Fg: term.NewRGBColor(0, 0, 0)},
		Interrupter: term.NopInterrupter(),
		Active:      func() []int { return []int{1} },
	})
	plain := drawShadedTabs(s)
	require.True(t, s.SetRunning(true))
	defer s.SetRunning(false)
	painted := false
	for deadline := time.Now().Add(5 * time.Second); !painted && time.Now().Before(deadline); {
		cells := drawShadedTabs(s)
		// beta's name sits at [11, 15).
		for x := 11; x < 15; x++ {
			if cells[x].Fg == plain[x].Fg {
				continue
			}
			painted = true
			r, g, b := cells[x].Fg.RGB()
			assert.Truef(t, r == 0 && g > 0 && b == 0,
				"cell %d blends toward green: rgb(%d, %d, %d)", x, r, g, b)
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, painted, "the effect never painted the active tab's name")
}

func TestShadedTabsWithoutShaderDrawsTabs(t *testing.T) {
	active := func() []int { return []int{0} }
	for _, tc := range []struct {
		name string
		cfg  ShadedTabsConfig
	}{
		{"empty name", ShadedTabsConfig{
			Interrupter: term.NopInterrupter(), Active: active}},
		{"unknown name", ShadedTabsConfig{
			Shader: "radarFrame", Interrupter: term.NopInterrupter(), Active: active}},
		{"no interrupter", ShadedTabsConfig{Shader: "pulse", Active: active}},
		{"no active callback", ShadedTabsConfig{
			Shader: "pulse", Interrupter: term.NopInterrupter()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestShadedTabs(tc.cfg)
			assert.False(t, s.SetRunning(true))
			assert.False(t, s.Running())
			assert.Equal(t, 'A', drawShadedTabs(s)[0].Ch)
		})
	}
}

func TestShadedTabsHonoursConfiguredFPS(t *testing.T) {
	for _, tc := range []struct {
		name string
		fps  int
		want int
	}{
		{name: "configured", fps: 12, want: 12},
		{name: "unset falls back", fps: 0, want: shaderloop.DefaultFPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestShadedTabs(ShadedTabsConfig{
				Shader:      "pulse",
				FPS:         tc.fps,
				Interrupter: term.NopInterrupter(),
				Active:      func() []int { return nil },
			})
			require.True(t, s.SetRunning(true))
			assert.Equal(t,
				int(shaderloop.Duration/(time.Second/time.Duration(tc.want))),
				s.shader.Total())
			require.True(t, s.SetRunning(false))
		})
	}
}

func TestShadedTabsActiveRects(t *testing.T) {
	active := []int{1}
	s := newTestShadedTabs(ShadedTabsConfig{
		Active: func() []int { return active },
	})
	_ = drawShadedTabs(s)

	assert.Equal(t, []shader.Rect{
		{Offset: term.Coordinates{X: 11}, Width: 4, Height: 1},
	}, s.activeRects())

	active = []int{0, 1, 7}
	assert.Equal(t, []shader.Rect{
		{Offset: term.Coordinates{X: 2}, Width: 5, Height: 1},
		{Offset: term.Coordinates{X: 11}, Width: 4, Height: 1},
	}, s.activeRects(), "indices outside the layout are skipped")

	s.Remove(0)
	_ = drawShadedTabs(s)
	active = []int{0}
	assert.Equal(t, []shader.Rect{
		{Offset: term.Coordinates{X: 2}, Width: 4, Height: 1},
	}, s.activeRects(), "geometry follows the layout")
}

func TestShadedTabsActiveRectsSkipProgress(t *testing.T) {
	active := []int{0, 1}
	s := newTestShadedTabs(ShadedTabsConfig{
		Active: func() []int { return active },
	})
	s.SetTabProgress(0, 40, term.Attributes{Fg: term.ColorGreen})
	s.SetTabProgress(1, 100, term.Attributes{Fg: term.ColorGreen})
	_ = drawShadedTabs(s)

	assert.Equal(t, []shader.Rect{
		{Offset: term.Coordinates{X: 4}, Width: 3, Height: 1},
	}, s.activeRects(), "the effect runs over the unfinished part only")

	s.ResetTabProgress(0)
	s.ResetTabProgress(1)
	_ = drawShadedTabs(s)
	assert.Equal(t, []shader.Rect{
		{Offset: term.Coordinates{X: 2}, Width: 5, Height: 1},
		{Offset: term.Coordinates{X: 11}, Width: 4, Height: 1},
	}, s.activeRects())
}

func TestShadedTabsPointerLeaves(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for _, size := range [][2]int{{60, 15}, {60, 1}, {1, 1}} {
		var clicks, iconClicks int
		s := NewShadedTabs(newPointerTabs(&clicks, &iconClicks), ShadedTabsConfig{
			Shader:      "pulse",
			Interrupter: term.NopInterrupter(),
			Active:      func() []int { return []int{1, 3, 11} },
		})
		handlertest.RunPointerLeaves(t, s, size[0], size[1])
		require.True(t, s.SetRunning(true))
		handlertest.RunPointerLeaves(t, s, size[0], size[1])
		require.True(t, s.SetRunning(false))
	}
}
