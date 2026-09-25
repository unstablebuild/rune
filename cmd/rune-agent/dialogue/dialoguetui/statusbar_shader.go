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
	"slices"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderloop"
)

const (
	// DefaultStatusBarShaderFPS is the cadence a status-bar effect is
	// redrawn at when the config names none.
	DefaultStatusBarShaderFPS = shaderloop.DefaultFPS
	// DefaultStatusBarShaderLoop is how long one visual loop of a
	// status-bar effect lasts when the config names none.
	DefaultStatusBarShaderLoop = shaderloop.DefaultLoop
)

// StatusBarShaderNames lists the effects status_bar.shader accepts.
func StatusBarShaderNames() []string {
	return shaderloop.Names()
}

// ValidStatusBarShader reports whether name selects a known effect.
// An empty name leaves the bar unshaded.
func ValidStatusBarShader(name string) bool {
	return name == "" || slices.Contains(StatusBarShaderNames(), name)
}

// shadedBar runs a shader over the status bar while a turn is active.
// It is a tui.Component so the dialogue can swap it in for the bar
// without the layout knowing an effect is running.
type shadedBar struct {
	mu            sync.Mutex
	root          tui.Component
	name          string
	fps           int
	loop          time.Duration
	defAttr       term.Attributes
	shader        *shader.Component
	width, height int
}

// Draw satisfies tui.Component.
func (s *shadedBar) Draw(w term.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shader != nil {
		s.shader.Draw(w)
		return
	}
	s.root.Draw(w)
}

// Resize satisfies tui.Component.
func (s *shadedBar) Resize(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.width, s.height = width, height
	if s.shader != nil {
		s.shader.Resize(width, height)
		return
	}
	s.root.Resize(width, height)
}

// setRunning starts or stops the effect. It reports whether the state
// changed, so the caller can repaint only on the transition rather than
// on every status update.
func (s *shadedBar) setRunning(running bool, interrupter term.Interrupter) bool {
	s.mu.Lock()
	if s.name == "" || interrupter == nil || running == (s.shader != nil) {
		s.mu.Unlock()
		return false
	}
	var closing *shader.Component
	if running {
		fps, loop := s.fps, s.loop
		if fps <= 0 {
			fps = DefaultStatusBarShaderFPS
		}
		if loop <= 0 {
			loop = DefaultStatusBarShaderLoop
		}
		sh, ok := shaderloop.New(s.name, s.defAttr, fps, loop)
		if !ok {
			s.mu.Unlock()
			return false
		}
		s.shader = shader.New(s.root, sh, interrupter,
			fps, shaderloop.Duration)
		s.shader.Resize(s.width, s.height)
	} else {
		closing = s.shader
		s.shader = nil
	}
	s.mu.Unlock()
	if closing != nil {
		_ = closing.Close()
	}
	return true
}
