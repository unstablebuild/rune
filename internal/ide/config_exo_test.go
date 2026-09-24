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
// normalization the editor uses: exo resolves to its fallback, modeless maps to
// standard, and a missing/unset editor.mode defaults to modal.
func TestPkgEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"nil config defaults to modal", nil, "modal"},
		{"empty config defaults to modal", config.MapConfig(map[string]any{}), "modal"},
		{
			"missing editor.mode defaults to modal",
			config.MapConfig(map[string]any{"editor": map[string]any{}}),
			"modal",
		},
		{
			"modal passes through",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modal"}}),
			"modal",
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
			"exo with modal fallback resolves to modal",
			config.MapConfig(map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modal"},
			}}),
			"modal",
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
		{"missing editor defaults to modal", config.MapConfig(map[string]any{}), "modal"},
		{
			"modeless maps to standard",
			config.MapConfig(map[string]any{"editor": map[string]any{"mode": "modeless"}}),
			"standard",
		},
		{
			"exo is preserved",
			config.MapConfig(map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modal"},
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
		{"explicit modal fallback", "modal", viPromptEditor{}},
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
// exo.fallback so packages always see a concrete modal or modeless mode.
func TestPkgEditorModeSubstitutesExoFallback(t *testing.T) {
	cases := []struct {
		name     string
		fallback string
		want     string
	}{
		{"default_fallback_is_standard", "", "standard"},
		{"explicit_modal_fallback", "modal", "modal"},
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

// TestPkgEditorModePassesNonExoThrough verifies modal/modeless are
// forwarded verbatim to packages.
func TestPkgEditorModePassesNonExoThrough(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"modal", "modal"},
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
// with an invalid (no {file}) command is rewritten back to "modal" so
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
	ic := &ideConfig{cfg: cfg, errors: map[string]error{}}
	assert.Equal(t, "modal", ic.editorMode())
}

// TestValidateExoFallsBackOnInvalidGoto verifies that an unparseable
// goto rewrites editor.mode back to "modal". exo relies on goto to
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
	assert.Equal(t, "modal", ic.editorMode())
}

// TestValidateExoFallsBackOnMissingGoto verifies that an unset goto
// rewrites editor.mode back to "modal" for the same reason as an
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
	assert.Equal(t, "modal", ic.editorMode())
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
	assert.Equal(t, "modal", ic.editorMode())
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
	assert.Equal(t, "modal", ic.editorMode())
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

// TestExoFallbackExplicitValues asserts "modal" and "standard"
// round-trip through the accessor, and the deprecated "modeless"
// alias normalizes to "standard".
func TestExoFallbackExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		fallback string
		want     string
	}{
		{"modal", "modal"},
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
