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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/ide/pkgtrust"
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
    },
}
`)
	assert.False(t, c.animationsLoadingWorkspace())
	assert.False(t, c.animationsOpenWorkspace())
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
}}}`,
		shader: "shine",
		fps:    12,
		loop:   2 * time.Second,
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
		name: "wrong types fall back and warn",
		src: `config = {"animations": {"KEY": {
    "enabled": "yes", "shader": 42, "fps": "fast", "loop": 1200,
}}}`,
		shader: "pulse",
		fps:    shaderloop.DefaultFPS,
		loop:   shaderloop.DefaultLoop,
		errors: []string{
			"animations.KEY.enabled", "animations.KEY.shader",
			"animations.KEY.fps", "animations.KEY.loop",
		},
	}} {
		for _, key := range []string{animActiveContentTab, animActiveWorkspaceTab} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				c := newAnimConfig(t, strings.ReplaceAll(tc.src, "KEY", key))
				assert.Equal(t, tc.shader, c.animationsActiveTabShader(key))
				assert.Equal(t, tc.fps, c.animationsActiveTabFPS(key))
				assert.Equal(t, tc.loop, c.animationsActiveTabLoop(key))
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
	sentinel := func(term.Attributes) shader.Shader {
		sentinelCalled = true
		return shader.Nop()
	}

	i := new(IDE)
	require.NoError(t, i.init(".", configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)),
		WithDefaultConfigStarlark(starlarkSrc, true, true),
		WithOpenShader(sentinel, 60, 200*time.Millisecond),
	))
	t.Cleanup(func() { _ = i.closeResources() })

	require.NotNil(t, i.root.openShaderCfg.shader,
		"open shader must be configured")

	_ = i.root.openShaderCfg.shader(term.Attributes{})
	assert.False(t, sentinelCalled,
		"rune.star override must replace the WithOpenShader factory; "+
			"the sentinel from WithOpenShader was invoked instead")

	assert.Equal(t, time.Second, i.root.openShaderCfg.duration,
		"open shader duration override must be applied")
}
