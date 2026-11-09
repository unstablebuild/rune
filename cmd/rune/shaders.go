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

package main

import (
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

const (
	// loadingShaderFPS is the cadence at which the shader played
	// while a workspace is loading is animated.
	loadingShaderFPS = 30

	// loadingShaderFadeDuration is how long the gray-fade animation takes
	// to reach full desaturation. The loading shader keeps running past
	// this point, holding at full desaturation, until the workspace finishes
	// loading and the open shader takes over.
	loadingShaderFadeDuration = 1 * time.Second

	// loadingShaderDuration is the lifetime of the underlying
	// [shader.Component] hosting the loading shader. It is intentionally
	// longer than loadingShaderFadeDuration so the Component does not
	// auto-expire mid-load: the loading shader holds at full desaturation
	// past the fade until [ide.WithLoadingShader]'s consumer swaps in the
	// open shader. 10s is an upper bound on plausible addWorkspace
	// latency; the shader is cancelled cleanly when the load completes.
	loadingShaderDuration = 30 * time.Second

	shutdownShaderDuration = 30 * time.Second

	// openShaderFPS is the cadence at which the shader played when a
	// workspace finishes loading is animated.
	openShaderFPS = 30

	// openShaderDuration is the lifetime of the open shader's
	// [shader.Component]. The first portion runs the burn sweep at
	// full intensity; the last [openShaderFadeOutDuration] fades the
	// burn output back to the live workspace cells via a cross-fade
	// transition with [shader.Nop] so the new content emerges
	// smoothly instead of snapping into place.
	openShaderDuration = 800 * time.Millisecond

	// openShaderFadeOutDuration is how long the trailing cross-fade
	// from the burn output to the live cells lasts. Must be strictly
	// less than [openShaderDuration]; the burn sweep itself plays
	// during the leading openShaderDuration-openShaderFadeOutDuration.
	openShaderFadeOutDuration = 100 * time.Millisecond
)

// loadingShader returns the shader played while a workspace is
// loading. It slowly fades the current screen to gray; the runner
// then swaps in the open shader which starts from the same tint to
// keep the two effects visually continuous. If loading completes
// before the fade does, the runner ends it early and reads
// [shader.GrayFadeShader.Progress] so the open shader matches the
// partial fade state.
func loadingShader(defaultAttr term.Attributes) shader.Shader {
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = int(loadingShaderFadeDuration / time.Second * loadingShaderFPS)
	return shader.GrayFade(params, defaultAttr)
}

// openShader returns the shader played when a workspace finishes
// loading. A quick "burn" sweep ignites from, the screen the workspace
// takes over, and resolves to the workspace, acknowledging the
// transition without slowing the user down.
func openShader(defaultAttr term.Attributes, from shader.Snapshot) shader.Shader {
	params := shader.DefaultBurnParams()
	params.BurnGradient = []term.Color{
		term.ColorWhite,
		term.ColorSilver,
		term.ColorYellow,
		term.ColorRed,
		term.ColorMaroon,
	}
	params.BurnSymbols = []rune{
		'░', '▒', '▓', '█', '█', '▓', '▒', '░',
	}
	params.BurnDuration = 0.05
	params.SmokeChance = 0.01
	params.SmokeSymbols = []rune{
		'.', '▀', '▄', 'o',
	}
	params.SmokeRise = 0.99
	params.SmokeMaxRise = 20
	params.From = from

	// Cross-fade the trailing portion of the open shader into the
	// live cells. The change point sits exactly between the burn
	// finish and the shader's end so the overlap is the full
	// openShaderFadeOutDuration window centred on that boundary —
	// half blending burn out and half blending the workspace in. A
	// long fade keeps the workspace from "popping" into view at the
	// end of the burn.
	fadeOutPerc := float64(openShaderFadeOutDuration) / float64(openShaderDuration)
	return shader.TransitionCrossFade(
		shader.TransitionCrossFadeParams{
			ChangeAtPerc: 1.0 - fadeOutPerc/2.0,
			OverlapPerc:  fadeOutPerc,
		},
		defaultAttr,
		shader.Burn(params, defaultAttr),
		shader.Nop(),
	)
}

func shutdownShader(defaultAttr term.Attributes) shader.Shader {
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = shutdownShaderFPS
	return shader.GrayFade(params, defaultAttr)
}
