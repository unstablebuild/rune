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
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderloop"
)

var _ tui.Handler = (*ShadedTabs)(nil)

// ShadedTabsConfig configures the effect a ShadedTabs runs.
type ShadedTabsConfig struct {
	// Shader names an effect from shaderloop.Names. An empty or unknown
	// name leaves the bar unshaded.
	Shader string
	// FPS and Loop tune the effect; zero values fall back to
	// shaderloop.DefaultFPS and shaderloop.DefaultLoop.
	FPS  int
	Loop time.Duration
	// DefAttr resolves term.ColorDefault cells for the effect.
	DefAttr term.Attributes
	// Interrupter drives the animation. Without one the bar is never
	// shaded.
	Interrupter term.Interrupter
	// Active returns the indices of the tabs to shade. It is called on
	// every frame, so the shaded cells follow tabs as they are added,
	// removed, moved or resized.
	Active func() []int
}

// ShadedTabs runs a continuous shader over the labels of some of its
// Tabs while SetRunning(true) is in effect. Everything other than Draw
// and Resize goes straight to the Tabs.
type ShadedTabs struct {
	*Tabs
	config        ShadedTabsConfig
	shader        *shader.Component
	width, height int
}

// NewShadedTabs wraps tabs so their active labels can be shaded.
func NewShadedTabs(tabs *Tabs, config ShadedTabsConfig) *ShadedTabs {
	return &ShadedTabs{Tabs: tabs, config: config}
}

// SetInterrupter replaces the interrupter used the next time the effect
// starts.
func (s *ShadedTabs) SetInterrupter(i term.Interrupter) {
	s.config.Interrupter = i
}

// Running reports whether the effect is running.
func (s *ShadedTabs) Running() bool {
	return s.shader != nil
}

// SetRunning starts or stops the effect. It reports whether the state
// changed, so callers can repaint only on the transition. Starting is a
// no-op without a known shader name or an interrupter.
func (s *ShadedTabs) SetRunning(running bool) bool {
	if running == s.Running() {
		return false
	}
	if !running {
		_ = s.shader.Close()
		s.shader = nil
		return true
	}
	if s.config.Interrupter == nil || s.config.Active == nil {
		return false
	}
	fps, loop := s.config.FPS, s.config.Loop
	if fps <= 0 {
		fps = shaderloop.DefaultFPS
	}
	if loop <= 0 {
		loop = shaderloop.DefaultLoop
	}
	inner, ok := shaderloop.New(s.config.Shader, s.config.DefAttr, fps, loop)
	if !ok {
		return false
	}
	s.shader = shader.New(s.Tabs, shader.Regions(inner, s.activeRects),
		s.config.Interrupter, fps, shaderloop.Duration)
	s.shader.Resize(s.width, s.height)
	return true
}

func (s *ShadedTabs) activeRects() []shader.Rect {
	active := s.config.Active()
	rects := make([]shader.Rect, 0, len(active))
	for _, idx := range active {
		offset, width, ok := s.TabRect(idx)
		if !ok {
			continue
		}
		rects = append(rects, shader.Rect{Offset: offset, Width: width, Height: 1})
	}
	return rects
}

// Draw satisfies tui.Component.
func (s *ShadedTabs) Draw(w term.Writer) {
	if s.shader != nil {
		s.shader.Draw(w)
		return
	}
	s.Tabs.Draw(w)
}

// Resize satisfies tui.Component.
func (s *ShadedTabs) Resize(width, height int) {
	s.width, s.height = width, height
	if s.shader != nil {
		s.shader.Resize(width, height)
		return
	}
	s.Tabs.Resize(width, height)
}
