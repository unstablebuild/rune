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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/internal/handler/command"
)

// TestPkgEditorMode asserts the exported helper the remote provisioning server
// uses to resolve RUNE_EDITOR_MODE from a config.Config applies the same
// normalization the editor uses: exo resolves to its fallback, the deprecated
// modal and modeless map to vim and standard, and a missing/unset editor.mode
// defaults to vim.
func TestPkgEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"nil config defaults to vim", nil, "vim"},
		{"empty config defaults to vim", config.MapConfig(map[string]any{}), "vim"},
		{
			"missing editor.mode defaults to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{}}),
			"vim",
		},
		{
			"vim passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "vim"}}),
			"vim",
		},
		{
			"modal maps to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modal"}}),
			"vim",
		},
		{
			"standard passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "standard"}}),
			"standard",
		},
		{
			"modeless maps to standard",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modeless"}}),
			"standard",
		},
		{
			"emacs passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "emacs"}}),
			"emacs",
		},
		{
			"exo without fallback resolves to standard",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "exo"}}),
			"standard",
		},
		{
			"exo with vim fallback resolves to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "vim"},
			}}),
			"vim",
		},
		{
			"exo with modal fallback resolves to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modal"},
			}}),
			"vim",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PkgEditorMode(tc.cfg))
		})
	}
}

func TestEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"missing editor defaults to vim", config.MapConfig(map[string]any{}), "vim"},
		{
			"vim passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "vim"}}),
			"vim",
		},
		{
			"modal maps to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modal"}}),
			"vim",
		},
		{
			"modeless maps to standard",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modeless"}}),
			"standard",
		},
		{
			"helix passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "helix"}}),
			"helix",
		},
		{
			"an unknown mode falls back to vim",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "vi"}}),
			"vim",
		},
		{
			"exo is preserved",
			config.MapConfig(map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "vim"},
			}}),
			"exo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EditorMode(tc.cfg))
		})
	}
}

// TestNewPromptEditorExo asserts that the in-memory prompt editor, which
// exo cannot host, follows the configured exo.fallback rather than
// hardwiring vi. The default fallback is standard.
func TestNewPromptEditorExo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback string
		want     command.Editor
	}{
		{"default fallback is standard", "", standardPromptEditor{}},
		{"explicit standard fallback", "standard", standardPromptEditor{}},
		{"deprecated modeless fallback", "modeless", standardPromptEditor{}},
		{"explicit emacs fallback", "emacs", emacsPromptEditor{}},
		{"explicit vim fallback", "vim", viPromptEditor{}},
		{"deprecated modal fallback", "modal", viPromptEditor{}},
		{"explicit helix fallback", "helix", helixPromptEditor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &workspaceManagerHandler{}
			exo := map[string]any{"command": "vim {file}"}
			if tc.fallback != "" {
				exo["fallback"] = tc.fallback
			}
			cfg := ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{
						"mode": "exo",
						"exo":  exo,
					},
				},
				errors: map[string]error{},
			}
			var ed command.Editor
			require.NotPanics(t, func() {
				ed = h.newPromptEditor(cfg)
			})
			assert.IsType(t, tc.want, ed)
		})
	}
}

// TestNewPromptEditorMode asserts that each built-in editor mode picks
// its own in-memory prompt editor, so the command prompt and console
// input line keep the grammar the user configured.
func TestNewPromptEditorMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want command.Editor
	}{
		{editorModeVim, viPromptEditor{}},
		{editorModeModal, viPromptEditor{}},
		{editorModeHelix, helixPromptEditor{}},
		{editorModeStandard, standardPromptEditor{}},
		{editorModeEmacs, emacsPromptEditor{}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h := &workspaceManagerHandler{}
			cfg := ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{"mode": tc.mode},
				},
				errors: map[string]error{},
			}
			var ed command.Editor
			require.NotPanics(t, func() {
				ed = h.newPromptEditor(cfg)
			})
			assert.IsType(t, tc.want, ed)
		})
	}
}

// TestExoModeAndAccessors covers editorMode/exoCommand/exoGoto on a
// hand-crafted config map.
func TestExoModeAndAccessors(t *testing.T) {
	cfg := &ideConfig{
		cfg: map[string]any{
			"editor": map[string]any{
				"mode": "exo",
				"exo": map[string]any{
					"command": "vim {file}",
					"goto":    "<esc>:{line}<enter>",
				},
			},
		},
		errors: map[string]error{},
	}
	assert.Equal(t, "exo", cfg.editorMode())
	assert.Equal(t, "vim {file}", cfg.exoCommand())
	assert.Equal(t, "<esc>:{line}<enter>", cfg.exoGoto())
}

// TestPkgEditorModeSubstitutesExoFallback verifies the value forwarded to
// package config.star scripts: exo mode is rewritten to the configured
// exo.fallback so packages always see a concrete built-in editor mode.
func TestPkgEditorModeSubstitutesExoFallback(t *testing.T) {
	cases := []struct {
		name     string
		fallback string
		want     string
	}{
		{"default_fallback_is_standard", "", "standard"},
		{"explicit_vim_fallback", "vim", "vim"},
		{"deprecated_modal_fallback", "modal", "vim"},
		{"explicit_modeless_fallback", "modeless", "standard"},
		{"explicit_standard_fallback", "standard", "standard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exo := map[string]any{
				"command": "vim {file}",
				"goto":    "<esc>:{line}<enter>",
			}
			if tc.fallback != "" {
				exo["fallback"] = tc.fallback
			}
			cfg := &ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{
						"mode": "exo",
						"exo":  exo,
					},
				},
				errors: map[string]error{},
			}
			assert.Equal(t, "exo", cfg.editorMode())
			assert.Equal(t, tc.want, cfg.pkgEditorMode())
		})
	}
}

// TestPkgEditorModePassesNonExoThrough verifies built-in modes are forwarded
// to packages in their canonical spelling, never as a deprecated alias.
func TestPkgEditorModePassesNonExoThrough(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"vim", "vim"},
		{"modal", "vim"},
		{"helix", "helix"},
		{"modeless", "standard"},
		{"standard", "standard"},
		{"emacs", "emacs"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := &ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{"mode": tc.mode},
				},
				errors: map[string]error{},
			}
			assert.Equal(t, tc.want, cfg.pkgEditorMode())
		})
	}
}

// TestValidateExoFallsBackOnMissingFile verifies that a exo mode
// with an invalid (no {file}) command is rewritten back to "vim" so
// the IDE still boots.
func TestValidateExoFallsBackOnMissingFile(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command": "vim",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, `falling back to "vim"`)
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "vim", ic.editorMode())
	assert.Equal(t, "vim", cfg["editor"].(map[string]any)["mode"],
		"the rewrite must use the canonical spelling")
}

// TestValidateExoFallsBackOnInvalidGoto verifies that an unparseable
// goto rewrites editor.mode back to "vim". exo relies on goto to
// position the cursor, so a bad value is a hard misconfiguration.
func TestValidateExoFallsBackOnInvalidGoto(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command": "vim {file}",
				"goto":    "<bogus-key>",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "vim", ic.editorMode())
}

// TestValidateExoFallsBackOnMissingGoto verifies that an unset goto
// rewrites editor.mode back to "vim" for the same reason as an
// invalid goto: exo requires both editor.exo.command and
// editor.exo.goto.
func TestValidateExoFallsBackOnMissingGoto(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command": "vim {file}",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "vim", ic.editorMode())
}

func TestValidateExoFallsBackOnMissingQuit(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command": "vim {file}",
				"goto":    "<esc>:{line}<enter>",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "vim", ic.editorMode())
}

func TestValidateExoFallsBackOnInvalidQuit(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command": "vim {file}",
				"goto":    "<esc>:{line}<enter>",
				"quit":    "<bogus-key>",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "vim", ic.editorMode())
}

// TestValidateExoAcceptsKnownTemplates table-tests the bundled sample
// goto templates parse without errors.
func TestValidateExoAcceptsKnownTemplates(t *testing.T) {
	templates := []string{
		"<esc>:{line}<enter>{col}|",
		"<esc>:goto<space>{line}<enter>",
		"<c-l>{line}:{col}<enter>",
		"<a-x>goto-line<enter>{line}<enter>",
		"<c-_>{line},{col}<enter>",
	}
	for _, tpl := range templates {
		err := parseGotoTemplate(tpl)
		assert.NoError(t, err, "template %q must parse", tpl)
	}
}

// TestExoFallbackDefaults asserts the accessor returns "standard"
// when no fallback key is set (the documented default).
func TestExoFallbackDefaults(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"editor": map[string]any{
				"mode": "exo",
				"exo": map[string]any{
					"command": "vim {file}",
					"goto":    "<esc>:{line}<enter>",
				},
			},
		},
		errors: map[string]error{},
	}
	assert.Equal(t, "standard", cfg.exoFallback())
}

// TestExoFallbackExplicitValues asserts every built-in editor round-trips
// through the accessor, and the deprecated "modal" and "modeless" aliases
// normalize to "vim" and "standard".
func TestExoFallbackExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		fallback string
		want     string
	}{
		{"vim", "vim"},
		{"modal", "vim"},
		{"helix", "helix"},
		{"emacs", "emacs"},
		{"standard", "standard"},
		{"modeless", "standard"},
	} {
		t.Run(tc.fallback, func(t *testing.T) {
			cfg := ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{
						"mode": "exo",
						"exo": map[string]any{
							"command":  "vim {file}",
							"goto":     "<esc>:{line}<enter>",
							"fallback": tc.fallback,
						},
					},
				},
				errors: map[string]error{},
			}
			assert.Equal(t, tc.want, cfg.exoFallback())
		})
	}
}

// TestValidateExoFallbackAcceptsEveryBuiltIn asserts validation keeps each
// canonical fallback and deprecated alias as written, so it never rewrites a
// working config.
func TestValidateExoFallbackAcceptsEveryBuiltIn(t *testing.T) {
	for _, fallback := range []string{
		"vim", "modal", "helix", "standard", "modeless", "emacs",
	} {
		t.Run(fallback, func(t *testing.T) {
			cfg := map[string]any{
				"editor": map[string]any{
					"mode": "exo",
					"exo": map[string]any{
						"command":  "vim {file}",
						"goto":     "<esc>:{line}<enter>",
						"quit":     "<esc>:qa<enter>",
						"fallback": fallback,
					},
				},
			}
			require.NoError(t, validateConfig(cfg))
			exo := cfg["editor"].(map[string]any)["exo"].(map[string]any)
			assert.Equal(t, fallback, exo["fallback"])
		})
	}
}

// TestValidateExoFallbackInvalidValueRewrites verifies an unknown
// fallback string is rewritten to "standard" so the IDE still boots.
func TestValidateExoFallbackInvalidValueRewrites(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{
			"mode": "exo",
			"exo": map[string]any{
				"command":  "vim {file}",
				"goto":     "<esc>:{line}<enter>",
				"quit":     "<esc>:qa<enter>",
				"fallback": "bogus",
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`editor.exo.fallback must be "vim", "helix", "standard", or "emacs"; got "bogus"`)

	ic := ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "standard", ic.exoFallback(),
		"invalid fallback must be rewritten to %q", "standard")
	// editor.mode remains "exo" since command/goto are valid.
	assert.Equal(t, "exo", ic.editorMode())
}

// TestExoExperimentalHighlights table-tests the accessor: defaults to
// true when unset, round-trips explicit true/false, and falls back to
// true while recording an error when the value is the wrong type.
func TestExoExperimentalHighlights(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        any
		setRaw     bool
		wantValue  bool
		wantErrKey bool
	}{
		{name: "unset defaults to true", wantValue: true},
		{name: "explicit true", setRaw: true, raw: true, wantValue: true},
		{name: "explicit false", setRaw: true, raw: false, wantValue: false},
		{
			name:       "non-bool falls back to true",
			setRaw:     true,
			raw:        "yes",
			wantValue:  true,
			wantErrKey: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exo := map[string]any{
				"command": "vim {file}",
				"goto":    "<esc>:{line}<enter>",
			}
			if tc.setRaw {
				exo["experimental_highlights"] = tc.raw
			}
			cfg := ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{
						"mode": "exo",
						"exo":  exo,
					},
				},
				errors: map[string]error{},
			}
			got := cfg.exoExperimentalHighlights()
			assert.Equal(t, tc.wantValue, got)
			_, hadErr := cfg.errors["editor.exo.experimental_highlights"]
			assert.Equal(t, tc.wantErrKey, hadErr,
				"expected errors entry to %v", tc.wantErrKey)
		})
	}
}
