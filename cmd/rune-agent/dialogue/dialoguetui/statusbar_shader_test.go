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

package dialoguetui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderloop"
)

// status_bar.shader accepts exactly the continuous catalog, plus an
// empty name that leaves the bar unshaded.
func TestValidStatusBarShader(t *testing.T) {
	require.NotEmpty(t, StatusBarShaderNames())
	for _, name := range StatusBarShaderNames() {
		assert.True(t, ValidStatusBarShader(name), name)
	}
	for _, name := range []string{"radarFrame", "incendium", "embers", "flames", "risingChars"} {
		assert.False(t, ValidStatusBarShader(name), name)
	}
	assert.True(t, ValidStatusBarShader(""), "an empty name disables the effect")
}

// The cadence knobs reach the effect rather than staying pinned to the
// shipped constants. A zero knob keeps the shipped value, which is what
// a config naming neither key leaves behind.
func TestShadedBarHonoursConfiguredFPS(t *testing.T) {
	for _, tc := range []struct {
		name string
		fps  int
		want int
	}{
		{name: "configured", fps: 12, want: 12},
		{name: "unset falls back", fps: 0, want: DefaultStatusBarShaderFPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bar := shippedStatusBar(t)
			bar.Resize(40, 1)
			s := &shadedBar{root: bar, name: "pulse", fps: tc.fps}
			s.Resize(40, 1)

			require.True(t, s.setRunning(true, term.NopInterrupter()))
			require.NotNil(t, s.shader)
			// Total is the frame budget the cadence buys over
			// shaderloop.Duration, so it reads the fps back out.
			assert.Equal(t,
				int(shaderloop.Duration/(time.Second/time.Duration(tc.want))),
				s.shader.Total())
			require.True(t, s.setRunning(false, term.NopInterrupter()))
		})
	}
}

// The effect only runs while a turn does, and stopping it must put the
// bar back exactly as it was.
func TestShadedBarRunsOnlyWhileActive(t *testing.T) {
	bar := shippedStatusBar(t)
	bar.Resize(40, 1)
	s := &shadedBar{root: bar, name: "pulse"}
	s.Resize(40, 1)

	plain := newCellRecorder()
	s.Draw(plain)

	require.True(t, s.setRunning(true, term.NopInterrupter()))
	assert.NotNil(t, s.shader)
	assert.False(t, s.setRunning(true, term.NopInterrupter()),
		"starting a running effect must not report a transition")

	require.True(t, s.setRunning(false, term.NopInterrupter()))
	assert.Nil(t, s.shader)

	restored := newCellRecorder()
	s.Draw(restored)
	assert.Equal(t, plain.row(40), restored.row(40))
}

// A bar with no effect configured, or with no interrupter to drive one,
// must draw straight through rather than allocating a shader.
func TestShadedBarWithoutShaderDrawsRoot(t *testing.T) {
	bar := shippedStatusBar(t)
	s := &shadedBar{root: bar}
	s.Resize(40, 1)
	assert.False(t, s.setRunning(true, term.NopInterrupter()))
	assert.Nil(t, s.shader)

	withName := &shadedBar{root: bar, name: "pulse"}
	withName.Resize(40, 1)
	assert.False(t, withName.setRunning(true, nil))
	assert.Nil(t, withName.shader)
}
