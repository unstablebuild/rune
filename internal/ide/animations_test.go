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

package ide

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

func newAnimConfig(t *testing.T, src string) ideConfig {
	t.Helper()
	cfg, err := decodeStarlark(src, nil, nil)
	require.NoError(t, err)
	return ideConfig{
		cfg:    cfg,
		errors: make(map[string]error),
	}
}

func TestAnimationsDefaultsEnabled(t *testing.T) {
	c := newAnimConfig(t, `config = {}`)
	assert.True(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
	assert.True(t, c.animationsIntro())
	assert.Empty(t, c.errors)
}

func TestAnimationsEmptySection(t *testing.T) {
	c := newAnimConfig(t, `config = {"animations": {}}`)
	assert.True(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
	assert.Empty(t, c.errors)
}

func TestAnimationsExplicitFalseDisables(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "loading_workspace": False,
        "open_workspace":    False,
        "intro":             False,
    },
}
`)
	assert.False(t, c.animationsLoadingWorkspace())
	assert.False(t, c.animationsOpenWorkspace())
	assert.False(t, c.animationsIntro())
	assert.Empty(t, c.errors)
}

func TestAnimationsExplicitTrue(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "loading_workspace": True,
        "open_workspace":    True,
    },
}
`)
	assert.True(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
	assert.Empty(t, c.errors)
}

func TestAnimationsLoadingIndependentOfOpen(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "loading_workspace": False,
    },
}
`)
	assert.False(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
}

func TestAnimationsCommandPromptDefaultsEnabled(t *testing.T) {
	c := newAnimConfig(t, `config = {"animations": {}}`)
	assert.True(t, c.animationsCommandPrompt())
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptExplicitFalseDisables(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": False,
    },
}
`)
	assert.False(t, c.animationsCommandPrompt())
	assert.True(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": "yes",
    },
}
`)
	assert.True(t, c.animationsCommandPrompt(),
		"wrong type must fall back to enabled")
	require.Contains(t, c.errors, "animations.command_prompt")
}

func TestAnimationsCommandPromptDictColorNamed(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "color": "red",
        },
    },
}
`)
	col, ok := c.animationsCommandPromptColor()
	assert.True(t, ok)
	assert.Equal(t, term.ColorRed, col)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptDictColorHexString(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "color": "#ff8800",
        },
    },
}
`)
	col, ok := c.animationsCommandPromptColor()
	assert.True(t, ok)
	assert.Equal(t, term.GetColor("#ff8800"), col)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptDictColorHexInt(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "color": 0xff8800,
        },
    },
}
`)
	col, ok := c.animationsCommandPromptColor()
	assert.True(t, ok)
	assert.Equal(t, term.NewHexColor(0xff8800), col)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptDictColorWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "color": True,
        },
    },
}
`)
	_, ok := c.animationsCommandPromptColor()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.command_prompt.color")
}

func TestAnimationsCommandPromptDictAngularWidthFloat(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "angular_width": 0.2,
        },
    },
}
`)
	w, ok := c.animationsCommandPromptAngularWidth()
	assert.True(t, ok)
	assert.InDelta(t, 0.2, w, 1e-9)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptDictAngularWidthOutOfRange(t *testing.T) {
	for _, src := range []string{
		`config = {"animations": {"command_prompt": {"angular_width": 0}}}`,
		`config = {"animations": {"command_prompt": {"angular_width": 1.5}}}`,
	} {
		c := newAnimConfig(t, src)
		_, ok := c.animationsCommandPromptAngularWidth()
		assert.False(t, ok, src)
		require.Contains(t, c.errors,
			"animations.command_prompt.angular_width", src)
	}
}

func TestAnimationsCommandPromptDictAngularWidthWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "angular_width": "wide",
        },
    },
}
`)
	_, ok := c.animationsCommandPromptAngularWidth()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.command_prompt.angular_width")
}

func TestAnimationsCommandPromptDictCyclesInt(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "cycles": 3,
        },
    },
}
`)
	n, ok := c.animationsCommandPromptCycles()
	assert.True(t, ok)
	assert.Equal(t, 3, n)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptDictCyclesBelowOne(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "cycles": 0,
        },
    },
}
`)
	_, ok := c.animationsCommandPromptCycles()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.command_prompt.cycles")
}

func TestAnimationsCommandPromptDictCyclesWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "cycles": "many",
        },
    },
}
`)
	_, ok := c.animationsCommandPromptCycles()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.command_prompt.cycles")
}

func TestAnimationsCommandPromptDictEnabledStillRespected(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "enabled": False,
            "color":   "red",
        },
    },
}
`)
	assert.False(t, c.animationsCommandPrompt())
	col, ok := c.animationsCommandPromptColor()
	assert.True(t, ok)
	assert.Equal(t, term.ColorRed, col)
	assert.Empty(t, c.errors)
}

func TestAnimationsCommandPromptBoolFormUnchanged(t *testing.T) {
	for _, src := range []string{
		`config = {"animations": {"command_prompt": True}}`,
		`config = {"animations": {"command_prompt": False}}`,
		`config = {"animations": {}}`,
	} {
		c := newAnimConfig(t, src)
		_, ok := c.animationsCommandPromptColor()
		assert.False(t, ok, src)
		_, ok = c.animationsCommandPromptAngularWidth()
		assert.False(t, ok, src)
		_, ok = c.animationsCommandPromptCycles()
		assert.False(t, ok, src)
		assert.Empty(t, c.errors, src)
	}
}

func TestAnimationsCommandPromptShaderCfgComposesDefaults(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "command_prompt": {
            "enabled":       True,
            "color":         "red",
            "angular_width": 0.2,
            "cycles":        2,
        },
    },
}
`)
	got := c.commandPromptShaderCfg()
	assert.True(t, got.enabled)
	assert.True(t, got.colorSet)
	assert.Equal(t, term.ColorRed, got.color)
	assert.InDelta(t, 0.2, got.angularWidth, 1e-9)
	assert.Equal(t, 2, got.cycles)
	assert.Empty(t, c.errors)
}

func TestAnimationsWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "loading_workspace": "yes",
    },
}
`)
	assert.True(t, c.animationsLoadingWorkspace(),
		"wrong type must fall back to enabled")
	require.Contains(t, c.errors, "animations.loading_workspace")
}

func TestAnimationsSectionWrongType(t *testing.T) {
	c := newAnimConfig(t, `config = {"animations": "off"}`)
	assert.True(t, c.animationsLoadingWorkspace())
	assert.True(t, c.animationsOpenWorkspace())
	require.Contains(t, c.errors, "animations")
}

func TestAnimationsOpenWorkspaceDictEnabledFalse(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "enabled": False,
        },
    },
}
`)
	assert.False(t, c.animationsOpenWorkspace())
	assert.Empty(t, c.errors)
}

func TestAnimationsOpenWorkspaceDictEnabledMissingDefaultsTrue(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "shader": "burn",
        },
    },
}
`)
	assert.True(t, c.animationsOpenWorkspace())
	assert.Empty(t, c.errors)
}

func TestAnimationsOpenWorkspaceShaderAndDuration(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "shader":   "burn",
            "duration": "1500ms",
        },
    },
}
`)
	name, ok := c.animationsOpenWorkspaceShader()
	assert.True(t, ok)
	assert.Equal(t, "burn", name)

	dur, ok := c.animationsOpenWorkspaceDuration()
	assert.True(t, ok)
	assert.Equal(t, 1500*time.Millisecond, dur)
	assert.Empty(t, c.errors)
}

func TestAnimationsOpenWorkspaceShaderTakesTheReveals(t *testing.T) {
	for _, name := range []string{
		"burn", "beams", "binaryPath", "decrypt", "expand", "matrix", "middleOut",
		"pour", "print", "rain", "slide",
	} {
		t.Run(name, func(t *testing.T) {
			c := newAnimConfig(t, fmt.Sprintf(`
config = {
    "animations": {
        "open_workspace": {"shader": %q},
    },
}
`, name))
			got, ok := c.animationsOpenWorkspaceShader()
			assert.True(t, ok)
			assert.Equal(t, name, got)
			assert.Empty(t, c.errors)
		})
	}
}

func TestAnimationsOpenWorkspaceShaderUnknown(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "shader": "definitelyNotAShader",
        },
    },
}
`)
	_, ok := c.animationsOpenWorkspaceShader()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.open_workspace.shader")
}

func TestAnimationsOpenWorkspaceShaderWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "shader": 42,
        },
    },
}
`)
	_, ok := c.animationsOpenWorkspaceShader()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.open_workspace.shader")
}

func TestAnimationsOpenWorkspaceDurationInvalid(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "duration": "not-a-duration",
        },
    },
}
`)
	_, ok := c.animationsOpenWorkspaceDuration()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.open_workspace.duration")
}

func TestAnimationsOpenWorkspaceDurationWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "duration": 1500,
        },
    },
}
`)
	_, ok := c.animationsOpenWorkspaceDuration()
	assert.False(t, ok)
	require.Contains(t, c.errors, "animations.open_workspace.duration")
}

func TestAnimationsOpenWorkspaceDictEnabledWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "open_workspace": {
            "enabled": "yes",
        },
    },
}
`)
	assert.True(t, c.animationsOpenWorkspace())
	require.Contains(t, c.errors, "animations.open_workspace.enabled")
}

func TestAnimationsActiveTab(t *testing.T) {
	for _, tc := range []struct {
		name   string
		src    string
		shader string
		fps    int
		loop   time.Duration
		params shaderloop.Params
		errors []string
	}{{
		name:   "absent",
		src:    `config = {"animations": {}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
	}, {
		name: "configured",
		src: `config = {"animations": {"KEY": {
    "enabled": True, "shader": "shine", "fps": 12, "loop": "2s",
    "shine": {"color": "green", "intensity": 0.8, "band_width": 0.4},
}}}`,
		shader: "shine",
		fps:    12,
		loop:   2 * time.Second,
		params: shaderloop.Params{Shine: shaderloop.ShineParams{
			Color: term.ColorGreen, Intensity: 0.8, BandWidth: 0.4,
		}},
	}, {
		name: "every shader keeps its own tuning",
		src: `config = {"animations": {"KEY": {
    "shader": "pulse",
    "pulse": {"color": "red", "intensity": 0.5},
    "shine": {"color": "green"},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		params: shaderloop.Params{
			Pulse: shaderloop.PulseParams{Color: term.ColorRed, Intensity: 0.5},
			Shine: shaderloop.ShineParams{Color: term.ColorGreen},
		},
	}, {
		name: "every shader's knobs",
		src: `config = {"animations": {"KEY": {
    "blaze":   {"animate_background": True, "color": "green", "swap_red_blue": True},
    "burn":    {
        "animate_background": True, "gradient": ["white", "green", 0x003300],
        "duration": 0.3, "seed": 7, "origin": 0,
    },
    "inferno": {"color": "#0000ff", "swap_red_blue": True},
    "shine":   {"direction": "left_to_right"},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		params: shaderloop.Params{
			Blaze: shaderloop.FireParams{
				AnimateBackground: true, Color: term.ColorGreen, SwapRedBlue: true,
			},
			Burn: shaderloop.BurnParams{
				AnimateBackground: true,
				Gradient: []term.Color{
					term.ColorWhite, term.ColorGreen, term.NewHexColor(0x003300),
				},
				Duration: 0.3, Seed: 7, FixedOrigin: true, Origin: 0,
			},
			Inferno: shaderloop.FireParams{
				Color: term.NewRGBColor(0, 0, 255), SwapRedBlue: true,
			},
			Shine: shaderloop.ShineParams{Direction: glslshader.DirectionLeftToRight},
		},
	}, {
		name:   "integer fractions",
		src:    `config = {"animations": {"KEY": {"shine": {"intensity": 1, "band_width": 1}}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		params: shaderloop.Params{Shine: shaderloop.ShineParams{Intensity: 1, BandWidth: 1}},
	}, {
		name: "tuning outside a shader's key is ignored",
		src: `config = {"animations": {"KEY": {
    "shader": "shine", "color": "green", "intensity": 0.8, "band_width": 0.4,
}}}`,
		shader: "shine",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
	}, {
		name:   "enabled missing defaults true",
		src:    `config = {"animations": {"KEY": {"fps": 12}}}`,
		shader: "pulse",
		fps:    12,
		loop:   shaderloop.DefaultLoop,
	}, {
		name:   "empty shader keeps the default",
		src:    `config = {"animations": {"KEY": {"shader": ""}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
	}, {
		name:   "dict disabled",
		src:    `config = {"animations": {"KEY": {"enabled": False, "shader": "shine"}}}`,
		shader: "",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
	}, {
		name:   "bool disabled",
		src:    `config = {"animations": {"KEY": False}}`,
		shader: "",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
	}, {
		name:   "unknown shader falls back and warns",
		src:    `config = {"animations": {"KEY": {"shader": "bogus"}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{"animations.KEY.shader"},
	}, {
		name: "invalid cadence falls back and warns",
		src: `config = {"animations": {"KEY": {
    "fps": 0, "loop": "soon",
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{"animations.KEY.fps", "animations.KEY.loop"},
	}, {
		name: "fractions out of range fall back and warn",
		src: `config = {"animations": {"KEY": {
    "pulse": {"intensity": 0},
    "shine": {"intensity": 1.5, "band_width": -0.1},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{
			"animations.KEY.pulse.intensity",
			"animations.KEY.shine.intensity", "animations.KEY.shine.band_width",
		},
	}, {
		name: "invalid knobs fall back and warn",
		src: `config = {"animations": {"KEY": {
    "blaze":   {"animate_background": "yes", "color": ["red"], "swap_red_blue": 1},
    "burn":    {"gradient": [], "duration": 2, "seed": "lucky", "origin": 1.5},
    "inferno": {"color": True, "swap_red_blue": "yes"},
    "shine":   {"direction": "sideways"},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		params: shaderloop.Params{
			Shine: shaderloop.ShineParams{Direction: glslshader.DirectionBottomLeftToTopRight},
		},
		errors: []string{
			"animations.KEY.blaze.animate_background", "animations.KEY.blaze.color",
			"animations.KEY.blaze.swap_red_blue",
			"animations.KEY.burn.gradient", "animations.KEY.burn.duration",
			"animations.KEY.burn.seed", "animations.KEY.burn.origin",
			"animations.KEY.inferno.color", "animations.KEY.inferno.swap_red_blue",
			"animations.KEY.shine.direction",
		},
	}, {
		name: "invalid gradient colours fall back and warn",
		src: `config = {"animations": {"KEY": {
    "burn": {"gradient": ["green", ["red"]]},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{"animations.KEY.burn.gradient"},
	}, {
		name: "None keeps the default",
		src: `config = {"animations": {"KEY": {
    "burn":  {"origin": None, "seed": 7},
    "shine": {"color": None, "direction": None},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		params: shaderloop.Params{Burn: shaderloop.BurnParams{Seed: 7}},
	}, {
		name: "wrong types fall back and warn",
		src: `config = {"animations": {"KEY": {
    "enabled": "yes", "shader": 42, "fps": "fast", "loop": 1200,
    "pulse": "red",
    "shine": {"color": ["green"], "intensity": "bright", "band_width": "wide"},
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{
			"animations.KEY.enabled", "animations.KEY.shader",
			"animations.KEY.fps", "animations.KEY.loop", "animations.KEY.pulse",
			"animations.KEY.shine.color", "animations.KEY.shine.intensity",
			"animations.KEY.shine.band_width",
		},
	}} {
		for _, key := range []string{animActiveContentTab, animActiveWorkspaceTab} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				c := newAnimConfig(t, strings.ReplaceAll(tc.src, "KEY", key))
				assert.Equal(t, tc.shader, c.animationsActiveTabShader(key))
				assert.Equal(t, tc.fps, c.animationsActiveTabFPS(key))
				assert.Equal(t, tc.loop, c.animationsActiveTabLoop(key))
				assert.Equal(t, tc.params, c.animationsActiveTabParams(key))
				want := make([]string, len(tc.errors))
				for i, e := range tc.errors {
					want[i] = strings.ReplaceAll(e, "KEY", key)
				}
				var got []string
				for k := range c.errors {
					got = append(got, k)
				}
				assert.ElementsMatch(t, want, got)
			})
		}
	}
}

func TestAnimationsActiveTabIndependent(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {
        "active_content_tab":   False,
        "active_workspace_tab": {"shader": "shine", "fps": 12, "loop": "2s"},
    },
}
`)
	assert.Empty(t, c.animationsActiveTabShader(animActiveContentTab))
	assert.Equal(t, "shine", c.animationsActiveTabShader(animActiveWorkspaceTab))
	assert.Equal(t, shaderloop.DefaultFPS, c.animationsActiveTabFPS(animActiveContentTab))
	assert.Equal(t, 12, c.animationsActiveTabFPS(animActiveWorkspaceTab))
	assert.Equal(t, shaderloop.DefaultLoop, c.animationsActiveTabLoop(animActiveContentTab))
	assert.Equal(t, 2*time.Second, c.animationsActiveTabLoop(animActiveWorkspaceTab))
	assert.Empty(t, c.errors)
}

func TestAnimationsScreensaver(t *testing.T) {
	defaults := screensaverConfig{
		idleTimeout: defaultScreensaverIdleTimeout,
		duration:    defaultScreensaverDuration,
		fps:         defaultShaderFPS,
		timeShader:  "loop",
	}
	withShaders := func(cfg screensaverConfig, shaders ...string) screensaverConfig {
		cfg.shaders = shaders
		return cfg
	}
	for _, tc := range []struct {
		name   string
		src    string
		want   screensaverConfig
		errors []string
	}{{
		name: "absent",
		src:  `config = {}`,
		want: defaults,
	}, {
		name: "no shaders",
		src:  `config = {"animations": {"screensaver": {"idle_timeout": "1m"}}}`,
		want: screensaverConfig{
			idleTimeout: time.Minute, duration: defaultScreensaverDuration,
			fps: defaultShaderFPS, timeShader: "loop",
		},
	}, {
		name: "configured",
		src: `config = {"animations": {"screensaver": {
    "enabled": True, "idle_timeout": "5m", "shaders": ["matrix", "rain"],
    "duration": "30s", "fps": 12, "time_shader": "ping_pong",
}}}`,
		want: screensaverConfig{
			shaders:     []string{"matrix", "rain"},
			idleTimeout: 5 * time.Minute, duration: 30 * time.Second, fps: 12,
			timeShader: "ping_pong",
		},
	}, {
		name: "disabled",
		src:  `config = {"animations": {"screensaver": False}}`,
		want: defaults,
	}, {
		name: "disabled in its dict",
		src:  `config = {"animations": {"screensaver": {"enabled": False, "shaders": ["matrix"]}}}`,
		want: defaults,
	}, {
		name:   "unknown shaders are skipped",
		src:    `config = {"animations": {"screensaver": {"shaders": ["matrix", "nope", "rain"]}}}`,
		want:   withShaders(defaults, "matrix", "rain"),
		errors: []string{"animations.screensaver.shaders"},
	}, {
		name:   "shaders that are not strings are skipped",
		src:    `config = {"animations": {"screensaver": {"shaders": [1, "rain"]}}}`,
		want:   withShaders(defaults, "rain"),
		errors: []string{"animations.screensaver.shaders"},
	}, {
		name: "a single shader need not be in a list",
		src:  `config = {"animations": {"screensaver": {"shaders": "rain"}}}`,
		want: withShaders(defaults, "rain"),
	}, {
		name:   "an unknown single shader is skipped",
		src:    `config = {"animations": {"screensaver": {"shaders": "nope"}}}`,
		want:   defaults,
		errors: []string{"animations.screensaver.shaders"},
	}, {
		name:   "shaders that are neither a list nor a string",
		src:    `config = {"animations": {"screensaver": {"shaders": 1}}}`,
		want:   defaults,
		errors: []string{"animations.screensaver.shaders"},
	}, {
		name:   "unknown time shader",
		src:    `config = {"animations": {"screensaver": {"shaders": ["rain"], "time_shader": "sine"}}}`,
		want:   withShaders(defaults, "rain"),
		errors: []string{"animations.screensaver.time_shader"},
	}, {
		name: "malformed values fall back to their defaults",
		src: `config = {"animations": {"screensaver": {
    "shaders": ["rain"], "idle_timeout": "soon", "duration": 3, "fps": "fast", "time_shader": 1,
}}}`,
		want: withShaders(defaults, "rain"),
		errors: []string{
			"animations.screensaver.idle_timeout", "animations.screensaver.duration",
			"animations.screensaver.fps", "animations.screensaver.time_shader",
		},
	}, {
		name: "zero values fall back to their defaults",
		src: `config = {"animations": {"screensaver": {
    "shaders": ["rain"], "idle_timeout": "0s", "duration": "0s", "fps": 0,
}}}`,
		want: withShaders(defaults, "rain"),
		errors: []string{
			"animations.screensaver.idle_timeout", "animations.screensaver.duration",
			"animations.screensaver.fps",
		},
	}, {
		name: "negative values fall back to their defaults",
		src: `config = {"animations": {"screensaver": {
    "shaders": ["rain"], "idle_timeout": "-1m", "duration": "-1s", "fps": -1,
}}}`,
		want: withShaders(defaults, "rain"),
		errors: []string{
			"animations.screensaver.idle_timeout", "animations.screensaver.duration",
			"animations.screensaver.fps",
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newAnimConfig(t, tc.src)
			assert.Equal(t, tc.want, c.screensaver())
			var got []string
			for k := range c.errors {
				got = append(got, k)
			}
			assert.ElementsMatch(t, tc.errors, got)
		})
	}
}

func TestIDEOpenShaderConfigOverrideAppliesToRoot(t *testing.T) {
	configFile, _ := makeTestFiles(t)
	require.NoError(t, os.WriteFile(configFile.Name(), []byte(""), 0666))

	starlarkSrc := `
config = {
    "animations": {
        "open_workspace": {
            "enabled":  True,
            "shader":   "shine",
            "duration": "1s",
        },
    },
}
`
	dir := t.TempDir()

	sentinelCalled := false
	sentinel := func(term.Attributes, shader.Snapshot) shader.Shader {
		sentinelCalled = true
		return shader.Nop()
	}

	i := new(IDE)
	require.NoError(t, i.init(".", configFile.Name(), dir, pkgtrust.NewStore(), newTestStorage(t, dir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)),
		WithDefaultConfigStarlark(starlarkSrc, true, true),
		WithOpenShader(sentinel, 60, 200*time.Millisecond),
	))
	t.Cleanup(func() { _ = i.closeResources() })

	require.NotNil(t, i.root.openShaderCfg.shader,
		"open shader must be configured")

	_ = i.root.openShaderCfg.shader(term.Attributes{}, shader.Snapshot{})
	assert.False(t, sentinelCalled,
		"rune.star override must replace the WithOpenShader factory; "+
			"the sentinel from WithOpenShader was invoked instead")

	assert.Equal(t, time.Second, i.root.openShaderCfg.duration,
		"open shader duration override must be applied")
}

func TestIDEOpenShaderConfigRevealsTheScreenTakenOver(t *testing.T) {
	configFile, _ := makeTestFiles(t)
	require.NoError(t, os.WriteFile(configFile.Name(), []byte(""), 0666))
	starlarkSrc := `
config = {
    "animations": {
        "open_workspace": {"shader": "rain"},
    },
}
`
	dir := t.TempDir()
	i := new(IDE)
	require.NoError(t, i.init(".", configFile.Name(), dir, pkgtrust.NewStore(), newTestStorage(t, dir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)),
		WithDefaultConfigStarlark(starlarkSrc, true, true),
		WithOpenShader(func(term.Attributes, shader.Snapshot) shader.Shader { return shader.Nop() }, 60, time.Second),
	))
	t.Cleanup(func() { _ = i.closeResources() })

	cells := func(ch rune) [][]term.Cell {
		out := make([][]term.Cell, 4)
		for y := range out {
			out[y] = make([]term.Cell, 10)
			for x := range out[y] {
				out[y][x] = term.NewCell(ch, 1, term.Attributes{Fg: term.ColorWhite})
			}
		}
		return out
	}
	open := i.root.openShaderCfg.shader(term.Attributes{}, shader.Snapshot{Cells: cells('O')})
	first := cells('N')
	open.Shade(0, 30, first)
	old := 0
	for _, row := range first {
		for _, c := range row {
			if c.Ch == 'O' {
				old++
			}
		}
	}
	assert.GreaterOrEqual(t, old, 30, "the rain starts from the screen it takes over")
}

func TestIDEScreensaverConfigArmsTheIdleTimer(t *testing.T) {
	configFile, _ := makeTestFiles(t)
	require.NoError(t, os.WriteFile(configFile.Name(), []byte(""), 0666))
	starlarkSrc := `
config = {
    "animations": {
        "screensaver": {"shaders": ["rain"], "idle_timeout": "3m"},
    },
}
`
	dir := t.TempDir()
	i := new(IDE)
	require.NoError(t, i.init(".", configFile.Name(), dir, pkgtrust.NewStore(), newTestStorage(t, dir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)),
		WithDefaultConfigStarlark(starlarkSrc, true, true),
		WithScheduleNextTick(func(func()) bool { return true }),
	))
	t.Cleanup(func() { _ = i.closeResources() })
	var armed []time.Duration
	i.options.afterFunc = func(d time.Duration, _ func()) *time.Timer {
		armed = append(armed, d)
		return nil
	}

	i.Ready()

	assert.Equal(t, []string{"rain"}, i.root.screensaverCfg.shaders)
	assert.Contains(t, armed, 3*time.Minute)
}
