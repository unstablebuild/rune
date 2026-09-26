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
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/starlarkconfig"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

// presetReachContent gives the editor something to act on, so a chord it
// owns is not declined merely because the buffer is empty.
const presetReachContent = "foo bar baz\n(qux) [quux]\n\tcorge grault\n"

// TestPresetBindingsReachCommandLayer sends every single-chord binding a
// shipped preset makes through the command pipeline in front of the
// editor that preset selects, and pins that the binding fires. The
// editor sees each key first, so a chord it owns would silently shadow
// the preset. The fuzzy-search package's bindings are merged in as the
// package manager would, and must not rebind a preset chord.
func TestPresetBindingsReachCommandLayer(t *testing.T) {
	runeStar := readRuneStar(t)
	fuzzySearchStar, err := os.ReadFile("../../cmd/extension_fuzzy_search/config.star")
	require.NoError(t, err)
	for _, tc := range []struct {
		file   string
		mode   string
		os     string
		editor func() text.Editor
		// shadowed lists chords the editor deliberately claims first,
		// with the reason the preset still binds them.
		shadowed map[string]string
	}{
		{
			file:   "preset_vim_darwin.yaml",
			mode:   editorModeVim,
			os:     "darwin",
			editor: func() text.Editor { return vi.Editor() },
		},
		{
			file:   "preset_vim_linux.yaml",
			mode:   editorModeVim,
			os:     "linux",
			editor: func() text.Editor { return vi.Editor() },
		},
		{
			file:   "preset_helix_darwin.yaml",
			mode:   editorModeHelix,
			os:     "darwin",
			editor: func() text.Editor { return helix.Editor() },
		},
		{
			file:   "preset_helix_linux.yaml",
			mode:   editorModeHelix,
			os:     "linux",
			editor: func() text.Editor { return helix.Editor() },
		},
		{
			file: "preset_standard_darwin.yaml",
			mode: editorModeStandard,
			os:   "darwin",
			editor: func() text.Editor {
				return standard.Editor(standard.WithHostMetaChords(true))
			},
			shadowed: map[string]string{
				"<meta-c>": "the editor copies its own selection first",
				"<meta-v>": "the editor pastes into its own buffer first",
			},
		},
		{
			file: "preset_standard_linux.yaml",
			mode: editorModeStandard,
			os:   "linux",
			editor: func() text.Editor {
				return standard.Editor(standard.WithHostMetaChords(false))
			},
		},
		{
			file:   "preset_emacs_darwin.yaml",
			mode:   editorModeEmacs,
			os:     "darwin",
			editor: func() text.Editor { return emacs.Editor() },
		},
		{
			file:   "preset_emacs_linux.yaml",
			mode:   editorModeEmacs,
			os:     "linux",
			editor: func() text.Editor { return emacs.Editor() },
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			base, err := decodeDefaultConfig(DefaultConfig{
				src: string(runeStar), modal: true, os: tc.os,
			})
			require.NoError(t, err)
			overlay, err := os.ReadFile("../../cmd/rune/" + tc.file)
			require.NoError(t, err)
			cfg, err := decodeOverlayConfigFile(bytes.NewReader(overlay), tc.file, base)
			require.NoError(t, err)
			ic := &ideConfig{cfg: cfg, errors: map[string]error{}}

			pkg, err := starlarkconfig.Decode(starlarkconfig.Source{
				Src:      fuzzySearchStar,
				Filename: "config.star",
				Params: map[string]any{
					"RUNE_DATADIR":     "/data",
					"RUNE_PKG_ID":      "fuzzy_search",
					"RUNE_PKG_VERSION": "1",
					"RUNE_EDITOR_MODE": tc.mode,
					"RUNE_OS":          tc.os,
				},
			})
			require.NoError(t, err)
			pkgCommand := pkg["command"].(map[string]any)
			presetMappings := ic.commandKeyMappings()
			pkgMappings := parseCommandKeyMappings(config.MapConfig(pkgCommand), ic.errors)
			for seq, cmds := range pkgMappings {
				if preset, dup := presetMappings[seq]; dup &&
					!slices.EqualFunc(preset, cmds, slices.Equal) {
					t.Errorf("fuzzy search binds %v to %v, which the preset binds to %v",
						seq, cmds, preset)
				}
			}
			maps.Copy(cfg["command"].(map[string]any)["key_bindings"].(map[string]any),
				pkgCommand["key_bindings"].(map[string]any))
			require.Empty(t, ic.errors)

			spelling := map[term.KeyComb]string{}
			for seq := range ic.commandKeyMappings() {
				if seq.Last != (term.KeyComb{}) {
					continue
				}
				spelling[seq.First] = seq.First.String()
			}
			chords := make([]term.KeyComb, 0, len(spelling))
			for k := range spelling {
				chords = append(chords, k)
			}
			sort.Slice(chords, func(i, j int) bool {
				return chords[i].String() < chords[j].String()
			})
			shadowed := map[term.KeyComb]bool{}
			for key := range tc.shadowed {
				k, err := term.ParseKey(key)
				require.NoError(t, err)
				require.Containsf(t, spelling, k, "%s is not bound", key)
				shadowed[k] = true
			}

			markers := make(map[term.KeyComb][][]string, len(chords))
			for i, k := range chords {
				markers[k] = [][]string{{fmt.Sprintf("presetchord%d", i)}}
			}
			newHarness := func() exSequencerHarness {
				var recMu sync.Mutex
				h := newExSequencerHarnessWithEditor(
					t, tc.editor(), &recMu, nil, markers, 20*time.Millisecond)
				uri, err := workspaceapi.ParseURI("file:///seq.go")
				require.NoError(t, err)
				ed, err := h.ex.Editor().Editor(uri)
				require.NoError(t, err)
				ed.CellEditor().Edit(context.Background(),
					term.Coordinates{}, term.Coordinates{}, presetReachContent)
				return h
			}

			h := newHarness()
			for i, k := range chords {
				before := len(h.firedCommands())
				_, _ = h.ex.Handle(term.Event{Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch})
				fired := h.firedCommands()
				reached := len(fired) == before+1 && fired[before] == fmt.Sprintf("presetchord%d", i)
				switch {
				case reached && shadowed[k]:
					t.Errorf("%s reaches the command layer; drop it from the shadowed list",
						spelling[k])
				case !reached && !shadowed[k]:
					t.Errorf("%s never reaches the command layer: the editor claims it",
						spelling[k])
				}
				if !reached {
					// The editor may have changed mode or started a
					// prefix; start over so the next chord is judged
					// from the initial state.
					h = newHarness()
				}
			}
		})
	}
}