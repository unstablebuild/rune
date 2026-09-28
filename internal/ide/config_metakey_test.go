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
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/keymeta"
)

func metaKeyTestConfig(goos, mode, metaKey string, rest map[string]any) ideConfig {
	cfg := map[string]any{
		"editor": map[string]any{"mode": mode},
		"gui":    map[string]any{},
	}
	if metaKey != "" {
		cfg["gui"].(map[string]any)["meta_key"] = metaKey
	}
	maps.Copy(cfg, rest)
	return ideConfig{cfg: cfg, errors: map[string]error{}, goos: goos}
}

func mustKey(t *testing.T, spec string) term.KeyComb {
	t.Helper()
	k, err := term.ParseKey(spec)
	require.NoError(t, err)
	return k
}

func TestMetaKeyResolution(t *testing.T) {
	for _, tc := range []struct {
		name, goos, mode, spec string
		want                   keymeta.Meta
		wantErr                bool
	}{
		{"unset", "linux", "vim", "", keymeta.Super, false},
		{"super", "linux", "vim", "<super>", keymeta.Super, false},
		{"vim alt", "linux", "vim", "<alt>", keymeta.Alt, false},
		{"helix alt", "linux", "helix", "<alt>", keymeta.Alt, false},
		{"standard alt", "linux", "standard", "<alt>", keymeta.Alt, false},
		{"deprecated modal alias", "linux", "modal", "<alt>", keymeta.Alt, false},
		{"emacs ctrl-super", "linux", "emacs", "<ctrl-super>", keymeta.CtrlSuper, false},
		{"emacs alt-super", "linux", "emacs", "<alt-super>", keymeta.AltSuper, false},
		{"emacs alt", "linux", "emacs", "<alt>", keymeta.Super, true},
		{"vim ctrl-super", "linux", "vim", "<ctrl-super>", keymeta.Super, true},
		{"darwin alt", "darwin", "vim", "<alt>", keymeta.Super, true},
		{"darwin super", "darwin", "vim", "<super>", keymeta.Super, false},
		{"invalid", "linux", "vim", "<hyper>", keymeta.Super, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := metaKeyTestConfig(tc.goos, tc.mode, tc.spec, nil)
			assert.Equal(t, tc.want, c.metaKey())
			if tc.wantErr {
				assert.Contains(t, c.errors, "gui.meta_key")
			} else {
				assert.Empty(t, c.errors)
			}
		})
	}
}

func TestMetaKeyNotAString(t *testing.T) {
	c := ideConfig{cfg: map[string]any{
		"gui": map[string]any{"meta_key": 3},
	}, errors: map[string]error{}, goos: "linux"}
	assert.Equal(t, keymeta.Super, c.metaKey())
	assert.Contains(t, c.errors, "gui.meta_key")
}

func TestValidateConfigMetaKey(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		cfg := metaKeyTestConfig("linux", "vim", "<alt>", nil).cfg
		require.NoError(t, validateConfigOn(cfg, "linux"))
		assert.Equal(t, "<alt>", cfg["gui"].(map[string]any)["meta_key"])
	})
	t.Run("disallowed drops the value", func(t *testing.T) {
		cfg := metaKeyTestConfig("linux", "emacs", "<alt>", nil).cfg
		err := validateConfigOn(cfg, "linux")
		require.ErrorContains(t, err, "gui.meta_key")
		assert.NotContains(t, cfg["gui"].(map[string]any), "meta_key")
	})
	t.Run("host without options", func(t *testing.T) {
		cfg := metaKeyTestConfig("darwin", "vim", "<alt>", nil).cfg
		require.ErrorContains(t, validateConfigOn(cfg, "darwin"), "gui.meta_key")
	})
	t.Run("other checks still run", func(t *testing.T) {
		cfg := metaKeyTestConfig("linux", "standard", "<hyper>", map[string]any{
			"command": map[string]any{"key": "p"},
		}).cfg
		err := validateConfigOn(cfg, "linux")
		require.ErrorContains(t, err, "gui.meta_key")
		require.ErrorContains(t, err, "command key must use")
	})
}

// TestMetaKeyAppliesToRuneKeys pins that every key setting of Rune's own
// reads <meta> as gui.meta_key says, including the code defaults.
func TestMetaKeyAppliesToRuneKeys(t *testing.T) {
	for _, tc := range []struct {
		mode, spec string
		mask       term.Modifier
	}{
		{"vim", "<super>", term.ModMeta},
		{"vim", "<alt>", term.ModAlt},
		{"standard", "<alt>", term.ModAlt},
		{"emacs", "<ctrl-super>", term.ModCtrl | term.ModMeta},
		{"emacs", "<alt-super>", term.ModAlt | term.ModMeta},
	} {
		t.Run(tc.mode+tc.spec, func(t *testing.T) {
			on := func(ch rune) term.KeyComb { return term.KeyComb{Ch: ch, Mod: tc.mask} }

			defaults := metaKeyTestConfig("linux", tc.mode, tc.spec, nil)
			assert.Equal(t, on('r'), defaults.commandHistoryKey())
			search := defaults.standardSearchConfig(nil)
			assert.Equal(t, on('f'), search.FindKey)
			assert.Equal(t, on('r'), search.ReplaceKey)
			assert.Equal(t, on('f'), defaults.terminalSearchConfig().FindKey)
			assert.Empty(t, defaults.errors)

			c := metaKeyTestConfig("linux", tc.mode, tc.spec, map[string]any{
				"command": map[string]any{
					"key":         "<meta-p>",
					"history_key": "<meta-y>",
					"key_bindings": map[string]any{
						"<meta-h>":       "windowfocus left",
						"<shift-meta-h>": "windowmove left",
						"<meta-g>o":      "searchtext",
						"<ctrl-x>s":      "writeall",
					},
				},
				"terminal": map[string]any{
					"search": map[string]any{"find_key": "<meta-s>"},
				},
			})
			c.cfg["editor"].(map[string]any)["standard"] = map[string]any{
				"search": map[string]any{"find_key": "<meta-j>", "replace_key": "<ctrl-r>"},
			}
			c.cfg["editor"].(map[string]any)["file_explorer"] = map[string]any{
				"edit_key": "<meta-e>",
			}
			assert.Equal(t, on('p'), c.commandKey())
			assert.Equal(t, on('y'), c.commandHistoryKey())
			assert.Equal(t, on('s'), c.terminalSearchConfig().FindKey)
			search = c.standardSearchConfig(nil)
			assert.Equal(t, on('j'), search.FindKey)
			assert.Equal(t, mustKey(t, "<ctrl-r>"), search.ReplaceKey)
			assert.Equal(t, on('e'), c.fileExplorerEditKey())

			bindings := map[string]string{}
			for seq, cmds := range c.commandKeyMappings() {
				key := seq.First.String()
				if seq.Last != (term.KeyComb{}) {
					key += seq.Last.String()
				}
				bindings[key] = strings.Join(cmds[0], " ")
			}
			assert.Equal(t, map[string]string{
				on('h').String():       "windowfocus left",
				on('H').String():       "windowmove left",
				on('g').String() + "o": "searchtext",
				"<ctrl-x>s":            "writeall",
			}, bindings)
			assert.Empty(t, c.errors)
		})
	}
}

func TestMetaKeyBindingCollisions(t *testing.T) {
	for _, tc := range []struct {
		name, mode, meta string
		bindings         map[string]any
		want             map[string]string
		wantErr          []string
	}{
		{
			name: "explicit spelling wins", mode: "vim", meta: "<alt>",
			bindings: map[string]any{"<meta-x>": "tabnew", "<alt-x>": "quit"},
			want:     map[string]string{"<alt-x>": "quit"},
			wantErr:  []string{"<meta-x>", "<alt-x>", "<alt-x> wins"},
		},
		{
			name: "unbind yields to a meta binding", mode: "vim", meta: "<alt>",
			bindings: map[string]any{"<shift-meta-f>": "windowtogglemaximize", "<a-s-f>": ""},
			want:     map[string]string{"<alt-shift-f>": "windowtogglemaximize"},
		},
		{
			name: "meta unbind yields to an explicit binding", mode: "vim", meta: "<alt>",
			bindings: map[string]any{"<meta-x>": "", "<alt-x>": "quit"},
			want:     map[string]string{"<alt-x>": "quit"},
		},
		{
			name: "two unbinds agree", mode: "standard", meta: "<alt>",
			bindings: map[string]any{"<s-m-f>": "", "<a-s-f>": ""},
			want:     map[string]string{"<alt-shift-f>": ""},
		},
		{
			name: "both carry meta", mode: "emacs", meta: "<ctrl-super>",
			bindings: map[string]any{"<meta-x>": "tabnew", "<ctrl-meta-x>": "quit"},
			want:     map[string]string{"<ctrl-meta-x>": "quit"},
			wantErr:  []string{"<meta-x>", "<ctrl-meta-x>", "<ctrl-meta-x> wins"},
		},
		{
			name: "no collision on super", mode: "vim", meta: "<super>",
			bindings: map[string]any{"<meta-x>": "tabnew", "<alt-x>": "quit"},
			want:     map[string]string{"<meta-x>": "tabnew", "<alt-x>": "quit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := metaKeyTestConfig("linux", tc.mode, tc.meta, map[string]any{
				"command": map[string]any{"key_bindings": tc.bindings},
			})
			got := map[string]string{}
			for seq, cmds := range c.commandKeyMappings() {
				got[seq.First.String()] = strings.Join(cmds[0], " ")
			}
			want := map[string]string{}
			for k, v := range tc.want {
				want[mustKey(t, k).String()] = v
			}
			assert.Equal(t, want, got)
			if len(tc.wantErr) == 0 {
				assert.Empty(t, c.errors)
				return
			}
			require.Len(t, c.errors, 1)
			for _, err := range c.errors {
				for _, s := range tc.wantErr {
					assert.ErrorContains(t, err, s)
				}
			}
		})
	}
}

// TestMetaKeyLeavesExtensionConfigAsWritten pins that gui.meta_key only
// changes how Rune reads its own key specs: an extension receives its
// config as the user wrote it, key settings included.
func TestMetaKeyLeavesExtensionConfigAsWritten(t *testing.T) {
	ext := map[string]any{
		"fuzzy_search": map[string]any{
			"path":   "/bin/fuzzy",
			"config": map[string]any{"file": map[string]any{"history_key": "<m-p>"}},
		},
	}
	for _, meta := range []string{"<super>", "<alt>"} {
		t.Run(meta, func(t *testing.T) {
			c := metaKeyTestConfig("linux", "vim", meta, map[string]any{"extensions": ext})
			pcfg, ok := c.extensions()["fuzzy_search"].config()
			require.True(t, ok)
			file, err := pcfg.GetConfig("file")
			require.NoError(t, err)
			got, err := file.GetString("history_key")
			require.NoError(t, err)
			assert.Equal(t, "<m-p>", got)
			assert.Empty(t, c.errors)
		})
	}
}

func TestCommandKeyBindingsApplyMetaKey(t *testing.T) {
	cfg := config.MapConfig(map[string]any{
		"editor": map[string]any{"mode": "vim"},
		"gui":    map[string]any{"meta_key": "<alt>"},
		"command": map[string]any{
			"key_bindings": map[string]any{"<meta-q>": "quit"},
		},
	})
	assert.Equal(t, mustKey(t, "<alt-q>"), commandKeyBindingsOn(cfg, "linux")["quit"])
	assert.Equal(t, mustKey(t, "<meta-q>"), commandKeyBindingsOn(cfg, "darwin")["quit"],
		"a meta key the host does not offer reads as Super")
}

// TestExMetaKeyAltReachesCommandLayer pins that under <alt>, Alt+F runs
// a <meta-f> binding while a chord the editor handles, such as
// <alt-left>, still reaches the editor first.
func TestExMetaKeyAltReachesCommandLayer(t *testing.T) {
	c := metaKeyTestConfig("linux", "standard", "<alt>", map[string]any{
		"command": map[string]any{"key_bindings": map[string]any{
			"<meta-f>":    "metafind",
			"<meta-left>": "metaleft",
		}},
	})
	keyBindings := map[term.KeyComb][][]string{}
	for seq, cmds := range c.commandKeyMappings() {
		keyBindings[seq.First] = cmds
	}
	require.Empty(t, c.errors)
	altLeft := mustKey(t, "<alt-left>")
	h := newExSequencerHarness(t, nil, keyBindings,
		[]term.KeyComb{altLeft}, 20*time.Millisecond)

	altF := mustKey(t, "<alt-f>")
	_, _ = h.ex.Handle(term.Event{Type: term.EventKey, Ch: altF.Ch, Mod: altF.Mod})
	assert.Equal(t, []string{"metafind"}, h.firedCommands())

	_, _ = h.ex.Handle(term.Event{Type: term.EventKey, Key: altLeft.Key, Mod: altLeft.Mod})
	assert.Equal(t, []string{"metafind"}, h.firedCommands())
	assert.Contains(t, h.editorConsumed(), altLeft)

	superF := mustKey(t, "<meta-f>")
	_, _ = h.ex.Handle(term.Event{Type: term.EventKey, Ch: superF.Ch, Mod: superF.Mod})
	assert.Equal(t, []string{"metafind"}, h.firedCommands(),
		"Super no longer reaches a <meta> binding under <alt>")
}
