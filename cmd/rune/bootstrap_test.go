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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide"
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

// TestRenderPreset pins that the vim choice maps to the vim
// preset, the standard-editor choice maps to the standard preset
// (which switches editor.mode to standard), the emacs choice maps to
// the emacs preset, the deprecated modeless alias resolves to the
// standard preset, and that an unknown choice is an error.
func TestRenderPreset(t *testing.T) {
	vim, err := renderPreset(editorVim, false)
	require.NoError(t, err)
	require.NotContains(t, vim, "mode: standard",
		"vim mode must not switch the editor into standard")
	require.NotContains(t, vim, "modal editor preset",
		"the preset written to the user config must not name the retired mode")
	require.Contains(t, vim, "enabled: false",
		"telemetry=false must render enabled: false")

	std, err := renderPreset(editorStandard, true)
	require.NoError(t, err)
	require.Contains(t, std, "mode: standard",
		"the standard choice must switch the editor into standard")
	require.Contains(t, std, "enabled: true",
		"telemetry=true must render enabled: true")

	deprecated, err := renderPreset(editorModeless, true)
	require.NoError(t, err)
	require.Equal(t, std, deprecated,
		"the deprecated modeless alias must resolve to the standard preset")

	ema, err := renderPreset(editorEmacs, false)
	require.NoError(t, err)
	require.Contains(t, ema, "enabled: false",
		"telemetry=false must render enabled: false")
	require.Contains(t, ema, "mode: emacs",
		"the emacs choice must switch the editor into emacs")

	hx, err := renderPreset(editorHelix, true)
	require.NoError(t, err)
	require.Contains(t, hx, "mode: helix",
		"the helix choice must switch the editor into helix")
	require.Contains(t, hx, "enabled: true",
		"telemetry=true must render enabled: true")
	for _, binding := range []string{
		`"<space>f": searchfile`,
		`"<space>e": fexplorer`,
		`"<space>b": tabsearch`,
		`"<space>/": searchtext`,
		`"<ctrl-w>v": "windownew right"`,
	} {
		require.Contains(t, hx, binding,
			"helix must reach commands through its <space> and <ctrl-w> menus")
	}
	require.NotContains(t, hx, `"<alt-d>"`,
		"helix keeps <alt> for its own grammar")
	// renderPreset embeds the host's preset: emacs puts Rune's layer on
	// Command on macOS and on Alt+Shift on Linux, where Super belongs to the
	// desktop and Alt is Emacs Meta.
	layer, move, resizeLeft := "meta", "shift-meta", "<meta-left>"
	if runtime.GOOS != "darwin" {
		layer, move, resizeLeft = "alt-shift", "ctrl-shift-alt", "<ctrl-shift-left>"
	}
	on := func(mods, key, command string) string {
		return fmt.Sprintf(`"<%s-%s>": %s`, mods, key, command)
	}
	require.Contains(t, ema, on(layer, "f", `"windowfocus right"`),
		"emacs must use the PNBF direction layer for window focus")
	require.Contains(t, ema, `"<ctrl-x>u": "undo prefix"`,
		"emacs must expose GNU's C-x u undo alias")
	closeWindow := on(layer, "k", "windowclose")
	closeOthers := on(move, "k", "windowcloseall")
	splitDown := on(layer, "d", `"windownew down"`)
	splitRight := on(layer, "r", `"windownew right"`)
	for _, binding := range []string{
		splitDown,
		splitRight,
		closeWindow,
		closeOthers,
		on(layer, "m", "windowtogglemaximize"),
		on(layer, "o", "fexplorer"),
		fmt.Sprintf(`"%s": "windowresize decrease width"`, resizeLeft),
	} {
		require.Contains(t, ema, binding,
			"emacs layout bindings must remain reachable from terminals")
	}
	// A focused terminal eats C-x, so the GNU lifecycle chords may only ever
	// duplicate a Rune-layer binding, never be the sole way to reach a command.
	for cx, direct := range map[string]string{
		`"<ctrl-x>0": windowclose`:       closeWindow,
		`"<ctrl-x>1": windowcloseall`:    closeOthers,
		`"<ctrl-x>2": "windownew down"`:  splitDown,
		`"<ctrl-x>3": "windownew right"`: splitRight,
	} {
		if strings.Contains(ema, cx) {
			require.Contains(t, ema, direct,
				"%s must duplicate a Rune-layer binding, not replace it", cx)
		}
	}
	require.NotContains(t, ema,
		on(layer, "f", `"echo {prompt}jumptoast<space>locals.scm<space>`),
		"the displaced function search binding must remain prompt-only")

	_, err = renderPreset("bogus", true)
	require.Error(t, err)
}

// TestGuardedPromptChainReopensOnUnadvancedClose proves that an Esc
// (or any dismissal that does not call OnSelect) re-opens the same
// prompt, while a normal OnSelect-then-Close sequence does not, and
// neither does a close fired by the pre-config IDE's own teardown.
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

// TestShouldSwallowBootstrapEvent enumerates the dangerous keys that
// must not reach the pre-config IDE while the bootstrap flow is in
// progress, plus a handful of events that must pass through untouched.
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
		{"app-q quit", keyEv('q', hostAppModifier), false},
		{"meta-w windowclose", keyEv('w', term.ModMeta), true},
		{"alt-w tabclose", keyEv('w', term.ModAlt), true},
		{"ctrl-w tabclose", keyEv('w', term.ModCtrl), true},
		{"ctrl-alt-w tabclose", keyEv('w', term.ModCtrlAlt), true},
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

// TestBootstrapHandleQuit asserts the platform's quit chord (Cmd+Q,
// Ctrl+Alt+Q) exits the app while the bootstrap wizard is still up, and
// stops being special once the real IDE has been swapped in.
func TestBootstrapHandleQuit(t *testing.T) {
	t.Run("quits while pre-config IDE is up", func(t *testing.T) {
		inner := &recordingHandler{}
		b := &bootstrapHandler{inner: inner}
		exit, handled := b.Handle(keyEv('q', hostAppModifier))
		require.True(t, exit, "the quit chord must exit during bootstrap")
		require.True(t, handled)
		require.Zero(t, inner.handled,
			"quit must not reach the pre-config IDE")
	})

	t.Run("delegates once the real IDE is ready", func(t *testing.T) {
		inner := &recordingHandler{}
		b := &bootstrapHandler{inner: inner, realIDE: &ide.IDE{}}
		exit, handled := b.Handle(keyEv('q', hostAppModifier))
		require.False(t, exit)
		require.False(t, handled)
		require.Equal(t, 1, inner.handled)
	})
}

// TestAppModifier pins the modifier app-wide chords use per platform. On
// Linux Super belongs to the desktop and plain Ctrl to the terminal.
func TestAppModifier(t *testing.T) {
	require.Equal(t, term.ModMeta, appModifier("darwin"))
	require.Equal(t, "\u2318", appModifierSymbol("darwin"))
	require.Equal(t, term.ModCtrlAlt, appModifier("linux"))
	require.Equal(t, "Ctrl+Alt", appModifierSymbol("linux"))
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

// TestBootstrapFontSizeDelta covers the chords the welcome prompt tells
// the user to press before any editor preset (and its guifontsize
// bindings) has been written to the user config.
func TestBootstrapFontSizeDelta(t *testing.T) {
	other := term.ModCtrlAlt
	if hostAppModifier == term.ModCtrlAlt {
		other = term.ModMeta
	}
	cases := []struct {
		name string
		ev   term.Event
		want int
	}{
		{"app-equals increases", keyEv('=', hostAppModifier), 1},
		{"app-plus increases", keyEv('+', hostAppModifier), 1},
		{"app-shift-plus increases", keyEv('+', hostAppModifier|term.ModShift), 1},
		{"app-minus decreases", keyEv('-', hostAppModifier), -1},
		{"app-underscore decreases", keyEv('_', hostAppModifier), -1},
		{"equals without modifier", keyEv('=', 0), 0},
		{"ctrl-equals", keyEv('=', term.ModCtrl), 0},
		{"other platform's modifier", keyEv('=', other), 0},
		{"app-a", keyEv('a', hostAppModifier), 0},
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
	prompter := &fakeBootstrapPrompter{}
	b := &bootstrapHandler{
		prompter:     prompter,
		publishEvent: func(term.Event) bool { return true },
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

	// Selecting an editor option in Vim prompt advances to Telemetry prompt.
	prompter.prompts[1].handler.OnSelect(0, optStandard)
	require.Equal(t, editorStandard, b.chosenEditor)
	require.Len(t, prompter.prompts, 3)

	telPrompt := prompter.prompts[2]
	require.Contains(t, telPrompt.message, "## Help us pick what to build next")
	require.Contains(t, telPrompt.message, "We never send file names, paths, file contents, terminal output, or anything you type.")
	require.Contains(t, telPrompt.message, "any time in your config")
	require.Contains(t, telPrompt.message, "Telemetry page in the docs")
	require.NotContains(t, telPrompt.message, "[Telemetry](")
	require.NotContains(t, telPrompt.message, "```json")
	require.Equal(t, []string{optTelemetryYes, optTelemetryNo}, telPrompt.options)
	require.Equal(t, bootstrapTelemetryKeys, telPrompt.bindings)
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
