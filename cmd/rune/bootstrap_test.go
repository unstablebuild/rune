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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/keymeta"
	"unstable.build/rune/internal/term/gui"
)

func TestTelemetryOptionToChoiceMapping(t *testing.T) {
	require.Equal(t, " yes ", optTelemetryYes)
	require.Equal(t, " no thanks ", optTelemetryNo)

	cases := []struct {
		option string
		want   bool
	}{
		{optTelemetryYes, true},
		{optTelemetryNo, false},
		{"unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.option, func(t *testing.T) {
			require.Equal(t, tc.want, telemetryOptionToChoice(tc.option))
		})
	}
}
func TestOptionToChoiceMapping(t *testing.T) {
	require.Equal(t, " standard ", optStandard)

	cases := []struct {
		option string
		want   string
	}{
		{optVimYes, editorVim},
		{optStandard, editorStandard},
		{optEmacs, editorEmacs},
		{optHelix, editorHelix},
		{"unknown", editorVim}, // default fallback
	}
	for _, tc := range cases {
		t.Run(tc.option, func(t *testing.T) {
			require.Equal(t, tc.want, optionToChoice(tc.option))
		})
	}
}

func TestRenderPreset(t *testing.T) {
	vim, err := renderPreset(editorVim, keymeta.Super, false, gui.AltModifierNone)
	require.NoError(t, err)
	require.NotContains(t, vim, "mode: standard",
		"vim mode must not switch the editor into standard")
	require.NotContains(t, vim, "modal editor preset",
		"the preset written to the user config must not name the retired mode")
	require.Contains(t, vim, "enabled: false",
		"telemetry=false must render enabled: false")
	std, err := renderPreset(editorStandard, keymeta.Super, true, gui.AltModifierNone)
	require.NoError(t, err)
	require.Contains(t, std, "mode: standard",
		"the standard choice must switch the editor into standard")
	require.Contains(t, std, "enabled: true",
		"telemetry=true must render enabled: true")

	deprecated, err := renderPreset(editorModeless, keymeta.Super, true, gui.AltModifierNone)
	require.NoError(t, err)
	require.Equal(t, std, deprecated,
		"the deprecated modeless alias must resolve to the standard preset")

	ema, err := renderPreset(editorEmacs, keymeta.Super, false, gui.AltModifierNone)
	require.NoError(t, err)
	require.Contains(t, ema, "enabled: false",
		"telemetry=false must render enabled: false")
	require.Contains(t, ema, "mode: emacs",
		"the emacs choice must switch the editor into emacs")

	hx, err := renderPreset(editorHelix, keymeta.Super, true, gui.AltModifierNone)
	require.NoError(t, err)
	require.Contains(t, hx, "mode: helix",
		"the helix choice must switch the editor into helix")
	require.Contains(t, hx, "enabled: true",
		"telemetry=true must render enabled: true")
	hxKeys := decodeKeyBindings(t, "helix preset", []byte(hx))
	for key, cmd := range map[string]string{
		"<space>f":  "searchfile",
		"<space>e":  "fexplorer",
		"<space>b":  "tabsearch",
		"<space>/":  "searchtext",
		"<ctrl-w>v": "windownew right",
	} {
		require.Equalf(t, cmd, hxKeys[key],
			"helix must reach commands through its <space> and <ctrl-w> menus: %s", key)
	}
	require.NotContains(t, hxKeys, "<alt-d>", "helix keeps <alt> for its own grammar")
	emaKeys := decodeKeyBindings(t, "emacs preset", []byte(ema))
	require.Equal(t, "windowfocus right", emaKeys["<meta-f>"],
		"emacs must use the PNBF direction layer for window focus")
	require.Equal(t, "undo prefix", emaKeys["<ctrl-x>u"],
		"emacs must expose GNU's C-x u undo alias")
	emaLayout := map[string]string{
		"<meta-d>":       "windownew down",
		"<meta-r>":       "windownew right",
		"<meta-k>":       "windowclose",
		"<shift-meta-k>": "windowcloseall",
		"<meta-m>":       "windowtogglemaximize",
		"<meta-o>":       "fexplorer",
	}
	// Linux keeps resize off <meta> arrows, which GNOME and KDE take for
	// window snapping.
	if runtime.GOOS == "darwin" {
		emaLayout["<meta-left>"] = "windowresize decrease width"
	} else {
		emaLayout["<ctrl-alt-meta-b>"] = "windowresize decrease width"
	}
	for key, cmd := range emaLayout {
		require.Equalf(t, cmd, emaKeys[key],
			"emacs layout bindings must remain reachable from terminals: %s", key)
	}
	// A focused terminal eats C-x, so the GNU lifecycle chords may only ever
	// duplicate a <meta> binding, never be the sole way to reach a command.
	for cx, meta := range map[string]string{
		"<ctrl-x>0": "<meta-k>",
		"<ctrl-x>1": "<shift-meta-k>",
		"<ctrl-x>2": "<meta-d>",
		"<ctrl-x>3": "<meta-r>",
	} {
		if cmd, ok := emaKeys[cx]; ok {
			require.Equalf(t, cmd, emaKeys[meta],
				"%s must duplicate a <meta> binding, not replace it", cx)
		}
	}

	_, err = renderPreset("bogus", keymeta.Super, true, gui.AltModifierNone)
	require.Error(t, err)
}

func TestRenderPresetMetaKey(t *testing.T) {
	all := []keymeta.Meta{keymeta.Super, keymeta.Alt, keymeta.CtrlSuper, keymeta.AltSuper}
	for _, editor := range []string{editorVim, editorHelix, editorStandard, editorEmacs} {
		offered := keymeta.Options(runtime.GOOS, editor)
		for _, meta := range all {
			for _, telemetry := range []bool{true, false} {
				name := fmt.Sprintf("%s/%s/%t", editor, meta, telemetry)
				t.Run(name, func(t *testing.T) {
					body, err := renderPreset(editor, meta, telemetry, gui.AltModifierNone)
					if !slices.Contains(offered, meta) {
						require.ErrorContains(t, err, "not offered")
						return
					}
					require.NoError(t, err)
					require.NotContains(t, body, "[[")
					var got struct {
						GUI struct {
							MetaKey string `yaml:"meta_key"`
						} `yaml:"gui"`
						Telemetry struct {
							Enabled bool `yaml:"enabled"`
						} `yaml:"telemetry"`
					}
					require.NoError(t, yaml.Unmarshal([]byte(body), &got))
					require.Equal(t, telemetry, got.Telemetry.Enabled)
					if len(offered) > 1 {
						require.Equal(t, meta.String(), got.GUI.MetaKey)
					} else {
						require.Empty(t, got.GUI.MetaKey,
							"a host with a single option writes no meta_key")
					}
				})
			}
		}
	}
}

func TestBootstrapAltModifierRoundTrip(t *testing.T) {
	all := []gui.AltModifier{gui.AltModifierNone, gui.AltModifierLeft, gui.AltModifierRight}
	for _, editor := range []string{editorVim, editorHelix, editorStandard, editorEmacs} {
		for _, modifier := range all {
			t.Run(editor+"/"+modifier.String(), func(t *testing.T) {
				dir := t.TempDir()
				b := &bootstrapHandler{
					dataDir: dir, chosenEditor: editor, chosenAltModifier: modifier,
					configPath: filepath.Join(dir, configFilename),
				}
				err := b.writePresetConfig()
				if modifier != gui.AltModifierNone && runtime.GOOS != "darwin" {
					require.ErrorContains(t, err, "not offered")
					return
				}
				require.NoError(t, err)

				cfg := mustLoadConfig(t, filepath.Join(dir, configFilename))
				guiCfg, err := cfg.GetConfig("gui")
				require.NoError(t, err)
				got, err := guiCfg.GetString("alt_modifier")
				if runtime.GOOS == "darwin" {
					require.NoError(t, err)
					require.Equal(t, modifier.String(), got)
				} else {
					require.ErrorIs(t, err, config.ErrNotFound,
						"only macOS presets carry gui.alt_modifier")
				}
				ctrl := gomock.NewController(t)
				require.Equal(t, modifier,
					getGUIAltModifier(browsertest.NewMockBrowser(ctrl), guiCfg))
			})
		}
	}
}

func TestGuardedPromptChainReopensOnUnadvancedClose(t *testing.T) {
	t.Run("esc reopens", func(t *testing.T) {
		var reopened int
		g := &guardedPromptChain{closing: func() bool { return false }}
		_ = g.onClose(func() { reopened++ })()
		require.Equal(t, 1, reopened, "Esc-equivalent close must re-open")
	})

	t.Run("select does not reopen", func(t *testing.T) {
		var reopened int
		var advanced int
		g := &guardedPromptChain{closing: func() bool { return false }}
		// Simulate the normal selection-then-close ordering: the
		// SDK calls OnSelect first (inside Handle), which marks
		// advanced, then Close → OnClose.
		g.onSelect(func(_ int, _ string) { advanced++ })(0, "")
		_ = g.onClose(func() { reopened++ })()
		require.Equal(t, 1, advanced)
		require.Equal(t, 0, reopened, "selection must not re-open")
	})

	t.Run("closing pre-config IDE does not reopen", func(t *testing.T) {
		var reopened int
		g := &guardedPromptChain{closing: func() bool { return true }}
		_ = g.onClose(func() { reopened++ })()
		require.Equal(t, 0, reopened,
			"teardown-driven close must not re-open the prompt")
	})
}

func TestShouldSwallowBootstrapEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   term.Event
		want bool
	}{
		// Dangerous: ':' opens the command prompt (configurable
		// activation key from default rune.star).
		{"colon opens command prompt", keyEv(':', 0), true},

		// Dangerous: default close-window / close-tab bindings from
		// the editor presets. Quit is not swallowed: it is handled
		// as a real exit by bootstrapHandler.Handle.
		{"meta-q quit", keyEv('q', term.ModMeta), false},
		{"meta-w windowclose", keyEv('w', term.ModMeta), true},
		{"alt-w tabclose", keyEv('w', term.ModAlt), true},
		{"ctrl-w tabclose", keyEv('w', term.ModCtrl), true},
		{"meta-shift-w", keyEv('w', term.ModMeta|term.ModShift), true},

		// Pass-through: arrow keys (prompt navigation), mouse,
		// resize, normal text input, Enter (used for one-button
		// welcome screen), Esc (the prompt's own close path, which
		// the guarded close callback re-opens).
		{"arrow left", namedKeyEv(term.KeyArrowLeft), false},
		{"enter", namedKeyEv(term.KeyEnter), false},
		{"esc", namedKeyEv(term.KeyEsc), false},
		{"plain rune", keyEv('a', 0), false},
		{"resize", term.Event{Type: term.EventResize, Width: 80, Height: 24}, false},
		{"mouse", term.Event{Type: term.EventMouse}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldSwallowBootstrapEvent(tc.ev))
		})
	}
}

func TestBootstrapHandleQuit(t *testing.T) {
	t.Run("quits while pre-config IDE is up", func(t *testing.T) {
		inner := &recordingHandler{}
		b := &bootstrapHandler{inner: inner}
		exit, handled := b.Handle(keyEv('q', term.ModMeta))
		require.True(t, exit, "meta-q must exit during bootstrap")
		require.True(t, handled)
		require.Zero(t, inner.handled,
			"quit must not reach the pre-config IDE")
	})

	t.Run("delegates once the real IDE is ready", func(t *testing.T) {
		inner := &recordingHandler{}
		b := &bootstrapHandler{inner: inner, realIDE: &ide.IDE{}}
		exit, handled := b.Handle(keyEv('q', term.ModMeta))
		require.False(t, exit)
		require.False(t, handled)
		require.Equal(t, 1, inner.handled)
	})
}

type recordingHandler struct {
	tui.Handler
	handled int
}

func (h *recordingHandler) Handle(term.Event) (bool, bool) {
	h.handled++
	return false, false
}

func keyEv(ch rune, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch, Mod: mod}
}

func TestBootstrapFontSizeDelta(t *testing.T) {
	cases := []struct {
		name string
		ev   term.Event
		want int
	}{
		{"meta-equals increases", keyEv('=', term.ModMeta), 1},
		{"meta-plus increases", keyEv('+', term.ModMeta), 1},
		{"meta-shift-plus increases", keyEv('+', term.ModMeta|term.ModShift), 1},
		{"meta-minus decreases", keyEv('-', term.ModMeta), -1},
		{"meta-underscore decreases", keyEv('_', term.ModMeta), -1},
		{"equals without meta", keyEv('=', 0), 0},
		{"ctrl-equals", keyEv('=', term.ModCtrl), 0},
		{"meta-a", keyEv('a', term.ModMeta), 0},
		{"mouse", term.Event{Type: term.EventMouse}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, bootstrapFontSizeDelta(tc.ev))
		})
	}
}

func namedKeyEv(k term.Key) term.Event {
	return term.Event{Type: term.EventKey, Key: k}
}

type fakePromptCall struct {
	message  string
	options  []string
	bindings []term.KeyComb
	handler  sdkhandler.PromptHandler
}

type fakeBootstrapPrompter struct {
	prompts []fakePromptCall
}

func (f *fakeBootstrapPrompter) Prompt(message string, options []string, bindings []term.KeyComb, h sdkhandler.PromptHandler) browser.Window {
	f.prompts = append(f.prompts, fakePromptCall{
		message:  message,
		options:  options,
		bindings: bindings,
		handler:  h,
	})
	return nil
}

func TestBootstrapPromptProgression(t *testing.T) {
	for _, tc := range []struct {
		goos   string
		askAlt bool
	}{
		{"darwin", true},
		{"windows", false},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			prompter := &fakeBootstrapPrompter{}
			b := &bootstrapHandler{
				prompter:     prompter,
				publishEvent: func(term.Event) bool { return true },
				goos:         tc.goos,
			}

			b.openWelcomePrompt()
			require.Len(t, prompter.prompts, 1)
			require.Equal(t, []string{optWelcomeGo}, prompter.prompts[0].options)

			// Selecting welcome advances to Vim prompt.
			prompter.prompts[0].handler.OnSelect(0, optWelcomeGo)
			require.Len(t, prompter.prompts, 2)
			require.Equal(t,
				[]string{optStandard, optEmacs, optVimYes, optHelix},
				prompter.prompts[1].options)

			prompter.prompts[1].handler.OnSelect(0, optStandard)
			require.Equal(t, editorStandard, b.chosenEditor)

			if tc.askAlt {
				require.Len(t, prompter.prompts, 3)
				altPrompt := prompter.prompts[2]
				require.Contains(t, altPrompt.message, "## Choose your Alt/Option (\u2325) key")
				require.Equal(t, []string{optAltRight, optAltLeft, optAltNone}, altPrompt.options)
				require.Equal(t, bootstrapAltModifierKeys, altPrompt.bindings)

				altPrompt.handler.OnSelect(0, optAltLeft)
				require.Equal(t, gui.AltModifierLeft, b.chosenAltModifier)
				require.Len(t, prompter.prompts, 4)
			} else {
				require.Len(t, prompter.prompts, 3, "only macOS asks for the Option key")
				require.Equal(t, gui.AltModifierNone, b.chosenAltModifier)
			}

			telPrompt := prompter.prompts[len(prompter.prompts)-1]
			require.Contains(t, telPrompt.message, "## Help us pick what to build next")
			require.Contains(t, telPrompt.message, "We never send file names, paths, file contents, terminal output, or anything you type.")
			require.Contains(t, telPrompt.message, "any time in your config")
			require.Contains(t, telPrompt.message, "Telemetry page in the docs")
			require.NotContains(t, telPrompt.message, "[Telemetry](")
			require.NotContains(t, telPrompt.message, "```json")
			require.Equal(t, []string{optTelemetryYes, optTelemetryNo}, telPrompt.options)
			require.Equal(t, bootstrapTelemetryKeys, telPrompt.bindings)
		})
	}
}

func TestBootstrapMetaPrompt(t *testing.T) {
	superAlt := []string{" super ", " alt "}
	superAltKeys := []term.KeyComb{{Ch: 's'}, {Ch: 'a'}}
	emacs := []string{" super ", " ctrl+super ", " alt+super "}
	emacsKeys := []term.KeyComb{{Ch: 's'}, {Ch: 'c'}, {Ch: 'a'}}
	for _, tc := range []struct {
		option   string
		editor   string
		labels   []string
		keys     []term.KeyComb
		pick     int
		wantMeta keymeta.Meta
	}{
		{optVimYes, editorVim, superAlt, superAltKeys, 1, keymeta.Alt},
		{optHelix, editorHelix, superAlt, superAltKeys, 0, keymeta.Super},
		{optStandard, editorStandard, superAlt, superAltKeys, 1, keymeta.Alt},
		{optEmacs, editorEmacs, emacs, emacsKeys, 1, keymeta.CtrlSuper},
		{optEmacs, editorEmacs, emacs, emacsKeys, 2, keymeta.AltSuper},
	} {
		t.Run(tc.editor+tc.labels[tc.pick], func(t *testing.T) {
			prompter := &fakeBootstrapPrompter{}
			b := &bootstrapHandler{
				prompter:     prompter,
				publishEvent: func(term.Event) bool { return true },
				goos:         "linux",
			}
			b.openVimPrompt()
			prompter.prompts[0].handler.OnSelect(0, tc.option)
			require.Equal(t, tc.editor, b.chosenEditor)
			require.Len(t, prompter.prompts, 2)

			meta := prompter.prompts[1]
			require.Contains(t, meta.message, "`<meta>`")
			require.Contains(t, meta.message, "gui.meta_key")
			require.Equal(t, tc.labels, meta.options)
			require.Equal(t, tc.keys, meta.bindings)

			require.NoError(t, meta.handler.OnClose())
			require.Len(t, prompter.prompts, 3, "dismissing the meta prompt reopens it")
			require.Equal(t, tc.labels, prompter.prompts[2].options)

			prompter.prompts[2].handler.OnSelect(tc.pick, tc.labels[tc.pick])
			require.Equal(t, tc.wantMeta, b.chosenMeta)
			require.Len(t, prompter.prompts, 4)
			require.Equal(t, []string{optTelemetryYes, optTelemetryNo},
				prompter.prompts[3].options)
		})
	}
}

func TestBootstrapAltModifierAppliesToRunningGUI(t *testing.T) {
	var applied []gui.AltModifier
	prompter := &fakeBootstrapPrompter{}
	b := &bootstrapHandler{
		prompter:       prompter,
		setAltModifier: func(m gui.AltModifier) { applied = append(applied, m) },
	}
	b.openAltModifierPrompt()
	prompter.prompts[0].handler.OnSelect(0, optAltRight)

	require.Equal(t, []gui.AltModifier{gui.AltModifierRight}, applied,
		"the chosen Alt key must reach the running GUI, not just the config")
}

func TestBootstrapAltModifierPrompt(t *testing.T) {
	for _, tc := range []struct {
		option string
		want   gui.AltModifier
	}{
		{optAltLeft, gui.AltModifierLeft},
		{optAltRight, gui.AltModifierRight},
		{optAltNone, gui.AltModifierNone},
	} {
		t.Run(tc.option, func(t *testing.T) {
			prompter := &fakeBootstrapPrompter{}
			b := &bootstrapHandler{prompter: prompter, chosenAltModifier: gui.AltModifierRight}
			b.openAltModifierPrompt()

			require.Len(t, prompter.prompts, 1)
			prompter.prompts[0].handler.OnSelect(0, tc.option)
			require.Equal(t, tc.want, b.chosenAltModifier)
			require.Len(t, prompter.prompts, 2)
		})
	}
}

func TestBootstrapMetaPromptOptionsFollowTheTable(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, editor := range []string{editorVim, editorHelix, editorStandard, editorEmacs} {
			b := &bootstrapHandler{goos: goos, chosenEditor: editor}
			require.Equal(t, keymeta.Options(goos, editor), b.metaOptions())
			seen := map[byte]bool{}
			for _, m := range b.metaOptions() {
				key := strings.TrimSpace(metaOptionLabel(m))[0]
				require.Falsef(t, seen[key], "%s/%s: two options share key %c",
					goos, editor, key)
				seen[key] = true
			}
		}
	}
}

func TestBootstrapMetaChoiceRoundTrip(t *testing.T) {
	for _, editor := range []string{editorVim, editorHelix, editorStandard, editorEmacs} {
		offered := keymeta.Options(runtime.GOOS, editor)
		for _, meta := range offered {
			t.Run(editor+meta.String(), func(t *testing.T) {
				dir := t.TempDir()
				b := &bootstrapHandler{
					dataDir: dir, chosenEditor: editor, chosenMeta: meta,
					configPath: filepath.Join(dir, configFilename),
				}
				require.NoError(t, b.writePresetConfig())
				cfg := mustLoadConfig(t, filepath.Join(dir, configFilename))
				guiCfg, err := cfg.GetConfig("gui")
				require.NoError(t, err)
				got, err := guiCfg.GetString("meta_key")
				if len(offered) == 1 {
					require.Error(t, err, "a host with a single option writes no meta_key")
					return
				}
				require.NoError(t, err)
				require.Equal(t, meta.String(), got)
			})
		}
	}
}

func TestBootstrapTelemetryPersistenceRoundTrip(t *testing.T) {
	cases := []struct {
		name        string
		option      string
		wantEnabled bool
	}{
		{"yes enables telemetry", optTelemetryYes, true},
		{"no thanks disables telemetry", optTelemetryNo, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prompter := &fakeBootstrapPrompter{}
			b := &bootstrapHandler{
				dataDir:      dir,
				configPath:   filepath.Join(dir, configFilename),
				chosenEditor: editorVim,
				prompter:     prompter,
				publishEvent: func(term.Event) bool { return true },
			}

			b.openTelemetryPrompt()
			require.Len(t, prompter.prompts, 1)

			prompter.prompts[0].handler.OnSelect(0, tc.option)
			require.Equal(t, tc.wantEnabled, b.telemetryEnabled)

			require.NoError(t, b.writePresetConfig())

			cfgPath := filepath.Join(dir, configFilename)
			content, err := os.ReadFile(cfgPath)
			require.NoError(t, err)
			require.Contains(t, string(content), fmt.Sprintf("enabled: %t", tc.wantEnabled))

			cfg := mustLoadConfig(t, cfgPath)
			require.Equal(t, tc.wantEnabled, ide.TelemetryEnabled(cfg))
		})
	}
}

func TestGuardedTelemetryPromptReopensOnDismiss(t *testing.T) {
	prompter := &fakeBootstrapPrompter{}
	b := &bootstrapHandler{
		prompter: prompter,
	}

	b.openTelemetryPrompt()
	require.Len(t, prompter.prompts, 1)

	// Dismissing without selecting (e.g. Esc) triggers OnClose, which must re-open.
	require.NoError(t, prompter.prompts[0].handler.OnClose())
	require.Len(t, prompter.prompts, 2, "dismissing telemetry prompt must re-open it")

	// Now selecting an option and then closing should NOT re-open.
	b.publishEvent = func(term.Event) bool { return true }
	prompter.prompts[1].handler.OnSelect(0, optTelemetryYes)
	require.NoError(t, prompter.prompts[1].handler.OnClose())
	require.Len(t, prompter.prompts, 2, "closing after selection must not re-open")
}
