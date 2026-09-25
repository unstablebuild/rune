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
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
)

var tutorialCallRe = regexp.MustCompile(`(?m)^tutorial\([^\n]*\)$`)

// withoutTutorialCall strips a shipped tutorial's top-level
// tutorial(...) registration so a test can append its own entry point
// while reusing the file's copy and key resolution.
func withoutTutorialCall(t *testing.T, src string) string {
	t.Helper()
	out := tutorialCallRe.ReplaceAllString(src, "")
	require.NotEqual(t, src, out, "no top-level tutorial() call to strip")
	return out
}

// TestBasicsTutorialParses asserts that the embedded basics.star
// tutorial parses through starlarktutorial.New, registers a
// callable entry, and reports the expected id/title/version.
func TestBasicsTutorialParses(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, basicsTutorial,
		"basicsTutorial embed must not be empty")
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"standard", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)

	assert.Equal(t, "basics", tut.ID())
	assert.Equal(t, "Rune basics", tut.Title())
	assert.Equal(t, "70", tut.Version())
}

// TestBasicsTutorialParsesModalMode asserts the embedded basics
// tutorial also parses under modal editor mode, exercising the
// modal-only branches (e.g. the modal-surfaces step).
func TestBasicsTutorialParsesModalMode(t *testing.T) {
	t.Parallel()

	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"modal", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	assert.Equal(t, "70", tut.Version())
}

// TestBasicsTutorialWorkspaceOpenCopyByOS asserts the welcome window's
// workspace-open step teaches the native macOS File ▸ Open Project…
// flow on darwin and keeps the command-prompt steps on other systems.
func TestBasicsTutorialWorkspaceOpenCopyByOS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		os        string
		contains  []string
		forbidden []string
	}{
		{
			os:        "darwin",
			contains:  []string{"Opening a project", "Open Project…", "File"},
			forbidden: []string{"Opening a workspace"},
		},
		{
			os:        "linux",
			contains:  []string{"Opening a workspace", "workspaceopen"},
			forbidden: []string{"Open Project…"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.os, func(t *testing.T) {
			t.Parallel()
			tut, err := starlarktutorial.New(
				"basics", basicsTutorial,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				term.KeyComb{Ch: ':'},
				"standard", tt.os, nil,
				nil, nil, nil,
			)
			require.NoError(t, err)
			tut.Resize(120, 40)
			tut.Reset()
			require.True(t, tut.WaitActive("wait_command", time.Second))

			w := term.NewStringWriter(120, 40)
			tut.Draw(w)
			require.NoError(t, w.Flush())
			rendered := strings.Join(strings.Fields(
				strings.ReplaceAll(w.String(), "│", " ")), " ")
			for _, expected := range tt.contains {
				assert.Contains(t, rendered, expected)
			}
			for _, forbidden := range tt.forbidden {
				assert.NotContains(t, rendered, forbidden)
			}
		})
	}
}

// TestBasicsTutorialNamesTheConfiguredConfigPath asserts the lesson
// points at the config file the running Rune actually reads. `rune -d`
// moves that file, so a hardcoded ~/.rune path would send users to a
// file their session never loads.
func TestBasicsTutorialNamesTheConfiguredConfigPath(t *testing.T) {
	t.Parallel()

	const path = "/tmp/rune-data/rune.star"
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "linux", nil,
		nil, nil, nil,
		starlarktutorial.WithConfigPath(path),
	)
	require.NoError(t, err)
	tut.Resize(120, 40)
	tut.Reset()

	named := false
	deadline := time.Now().Add(10 * time.Second)
	for !named && time.Now().Before(deadline) {
		if tut.WaitFinished(time.Millisecond) {
			break
		}
		if tut.ActiveKind() == "" {
			continue
		}
		named = strings.Contains(tut.ActiveText(), path)
		tut.Skip()
	}
	assert.True(t, named, "no step named the configured config file")
}

// TestBasicsTutorialCompleterKeysByMode asserts the completion-list
// phrasing names each preset's own list bindings rather than a single
// hardcoded arrow-key spelling.
func TestBasicsTutorialCompleterKeysByMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode      string
		expected  []string
		forbidden []string
	}{
		{mode: "modal", expected: []string{"<ctrl-j>", "<ctrl-k>", "<up>", "<down>"}},
		{
			mode:      "standard",
			expected:  []string{"<up>", "<down>"},
			forbidden: []string{"<ctrl-i>", "<ctrl-k>"},
		},
		{mode: "emacs", expected: []string{"<ctrl-p>", "<ctrl-n>", "<up>", "<down>"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			src := `
def run():
    wait_event(event = "open", text = welcome_md)
    wait_event(event = "open", text = edit_md)
    wait_event(event = "open", text = completer_pick_phrase)
tutorial(entry=run)
`
			modeSrc := withoutTutorialCall(t, basicsTutorial) + src
			tut, err := starlarktutorial.New(
				"basics-completer-keys", modeSrc,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				term.KeyComb{Ch: ':'}, tt.mode, "",
				nil, nil, nil, nil,
			)
			require.NoError(t, err)
			tut.Resize(80, 24)
			tut.Reset()
			// The workspace picker and the file completer name the same
			// keys as every other completer the lesson drives.
			for _, step := range []string{"workspace", "file", "phrase"} {
				require.True(t, tut.WaitActive("wait_event", time.Second),
					"expected the %s step", step)
				text := tut.ActiveText()
				for _, key := range tt.expected {
					assert.Contains(t, text, key, "%s step", step)
				}
				for _, key := range tt.forbidden {
					assert.NotContains(t, text, key, "%s step", step)
				}
				tut.ObserveEvent("open", "file:///workspace/a.go")
			}
		})
	}
}

// TestBasicsTutorialWarnsAboutTheSwallowedShiftTab asserts the
// file-explorer step tells modal users why their binding does nothing
// from a terminal, and that presets without a NORMAL mode never carry
// the warning.
func TestBasicsTutorialWarnsAboutTheSwallowedShiftTab(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"modal", "standard", "emacs"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			src := withoutTutorialCall(t, basicsTutorial) + `
def run():
    wait_event(event = "open", text = tabs_md)
tutorial(entry=run)
`
			keyFor := func(cmd string, _ []string) string {
				if cmd == "fexplorer" {
					return "<shift-tab>"
				}
				return ""
			}
			tut, err := starlarktutorial.New(
				"basics-shift-tab", src,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				term.KeyComb{Ch: ':'}, mode, "",
				keyFor, nil, nil, nil,
			)
			require.NoError(t, err)
			tut.Resize(80, 24)
			tut.Reset()
			t.Cleanup(tut.Stop)
			require.True(t, tut.WaitActive("wait_event", time.Second))

			text := tut.ActiveText()
			require.Contains(t, text, "<shift-tab>")
			if mode == "modal" {
				assert.Contains(t, text, "NORMAL mode")
				return
			}
			assert.NotContains(t, text, "NORMAL mode")
		})
	}
}

// trimmedScreen is a term.StringWriter whose String drops each row's
// trailing blanks and the blank rows around the drawn content, so an
// expected screen can be written without whitespace an editor strips.
type trimmedScreen struct{ *term.StringWriter }

func (s trimmedScreen) String() string {
	rows := strings.Split(s.StringWriter.String(), "\n")
	for i, row := range rows {
		rows[i] = strings.TrimRight(row, " ")
	}
	return strings.Trim(strings.Join(rows, "\n"), "\n")
}

// TestBasicsTutorialRunAProgram renders the step that teaches running
// a one-shot program. The prompt commands are spelled the way the user
// types them: the command key first, then `!`.
func TestBasicsTutorialRunAProgram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		commandKey term.KeyComb
		want       string
	}{
		{
			name:       "printable command key",
			commandKey: term.KeyComb{Ch: ':'},
			want: `
  Run a program

 Rune is also a terminal multiplexer 👾 : terminals live in
 windows and tabs, just like files. For a program you only
 need to run once, skip the terminal and run it straight
 from the command prompt.


 Running a quick, one-shot program ⚡

 • :! runs a program in a floating window that shows its
   output, for example :! git log.
 • :!! runs a program but hides its output. Use it when you
   only care whether it worked.

 Let's run git log:

 1. Press : to open the command prompt.

 2. Type ! git log and press <enter>.

 Rune opens a floating window streaming its output.`,
		},
		{
			name:       "modified command key",
			commandKey: term.KeyComb{Mod: term.ModAlt, Ch: 'x'},
			want: `
  Run a program

 Rune is also a terminal multiplexer 👾 : terminals live in
 windows and tabs, just like files. For a program you only
 need to run once, skip the terminal and run it straight
 from the command prompt.


 Running a quick, one-shot program ⚡

 • <alt-x>! runs a program in a floating window that shows
   its output, for example <alt-x>! git log.
 • <alt-x>!! runs a program but hides its output. Use it
   when you only care whether it worked.

 Let's run git log:

 1. Press <alt-x> to open the command prompt.

 2. Type ! git log and press <enter>.

 Rune opens a floating window streaming its output.`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := withoutTutorialCall(t, basicsTutorial) + `
def run():
    teach_terminals()
tutorial(entry=run)
`
			tut, err := starlarktutorial.New(
				"basics-terminals", src,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				tt.commandKey, "standard", "",
				nil, nil, nil, nil,
			)
			require.NoError(t, err)
			const width, height = 60, 30
			tut.Resize(width, height)
			tut.Reset()
			t.Cleanup(tut.Stop)
			require.True(t, tut.WaitActive("wait_command", time.Second))

			comptest.TestComponent(t, tut,
				trimmedScreen{term.NewStringWriter(width, height)},
				[]comptest.TestCase{{Expected: tt.want}})
		})
	}
}

func TestBasicsTutorialLayoutIntro(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     string
		contains []string
	}{
		{
			name: "modal",
			mode: "modal",
			contains: []string{
				"HJKL controls the layout",
				"Hold <meta> and press HJKL",
				"Hold <alt> and press H/L",
				"Add <shift> to move content instead of focus it",
			},
		},
		{
			name: "emacs",
			mode: "emacs",
			contains: []string{
				"Emacs directions control the layout",
				"Rather than teach a second direction map, Rune changes the target",
				"hold <meta> with the same PNBF directions to focus windows",
				"add <shift> to move window content instead",
				"Reusing that muscle memory keeps repeated layout actions fast",
				"host Meta layer stays reachable from terminals",
				"<shift-meta-w> closes a window, <meta-k> closes the others",
				"<meta-d> / <meta-r> split below or right",
				"<meta-e> toggles maximization",
				"<meta-w> closes a tab; adding <shift> escalates",
				"<meta-[> / <meta-]> cycle tabs",
				"<shift-meta-[> / <shift-meta-]> reorder the current tab",
			},
		},
		{
			name: "standard",
			mode: "standard",
			contains: []string{
				"Alt drives the layout",
				"Vim made generations of programmers extraordinarily productive",
				"Keyboard-driven does not have to mean learning an entirely new way to edit",
				"Rune brings that advantage to a familiar, non-modal editor",
				"So hold <alt> and press IJKL to focus a window",
				"Add <shift> to move its content, or add <meta> to resize it",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyFor := func(cmd string, args []string) string {
				keys := map[string]string{
					"windowfocus left":   "<alt-j>",
					"windowmove left":    "<alt-shift-j>",
					"tabprevious":        "<alt-[>",
					"tabnext":            "<alt-]>",
					"tabmove left":       "<alt-shift-[>",
					"tabmove right":      "<alt-shift-]>",
					"windownew":          "<alt-n>",
					"terminalneworsplit": "<alt-enter>",
					"windowclose":        "<alt-q>",
					"tabnew":             "<alt-t>",
					"tabclose":           "<alt-w>",
				}
				if tt.mode == "emacs" {
					keys["windownew down"] = "<meta-d>"
					keys["windownew right"] = "<meta-r>"
					keys["windowclose"] = "<shift-meta-w>"
					keys["windowcloseall"] = "<meta-k>"
					keys["windowtogglemaximize"] = "<meta-e>"
					keys["tabclose"] = "<meta-w>"
					keys["tabprevious"] = "<meta-[>"
					keys["tabnext"] = "<meta-]>"
					keys["tabmove left"] = "<shift-meta-[>"
					keys["tabmove right"] = "<shift-meta-]>"
				}
				return keys[strings.Join(append([]string{cmd}, args...), " ")]
			}
			tut, err := starlarktutorial.New(
				"basics", basicsTutorial,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				term.KeyComb{Ch: ':'},
				tt.mode, "", keyFor,
				nil, nil, nil,
			)
			require.NoError(t, err)
			tut.Resize(120, 40)
			tut.Reset()

			require.True(t, tut.WaitActive("wait_command", time.Second))
			tut.ObserveCommand("workspaceopen", "workspaceopen", []string{"/tmp/workspace"}, nil)
			require.True(t, tut.WaitActive("wait_command", time.Second))
			tut.ObserveCommand("edit", "edit", []string{"README.md"}, nil)
			require.True(t, tut.WaitActive("wait_command", time.Second))

			// The layout lesson heads the split-a-window step.
			w := term.NewStringWriter(120, 60)
			tut.Resize(120, 60)
			tut.Draw(w)
			require.NoError(t, w.Flush())
			rendered := strings.Join(strings.Fields(
				strings.ReplaceAll(w.String(), "│", " ")), " ")
			for _, expected := range tt.contains {
				assert.Contains(t, rendered, expected)
			}
			assert.NotContains(t, rendered, "standard and Emacs")
			assert.Contains(t, rendered, "Split the focused window in two")
		})
	}
}

func TestBasicsTutorialWelcomeUsesResolvedBindings(t *testing.T) {
	t.Parallel()

	keyFor := func(cmd string, args []string) string {
		key := strings.Join(append([]string{cmd}, args...), " ")
		if strings.HasPrefix(key, "workspacefocus ") {
			return "<f" + args[0] + ">"
		}
		if key == "terminalneworsplit" {
			return "<f10>"
		}
		return ""
	}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(120, 40)
	tut.Reset()
	require.True(t, tut.WaitActive("wait_command", time.Second))

	w := term.NewStringWriter(120, 40)
	tut.Draw(w)
	require.NoError(t, w.Flush())
	rendered := strings.Join(strings.Fields(
		strings.ReplaceAll(w.String(), "│", " ")), " ")
	assert.Contains(t, rendered,
		"<f1> <f2> <f3> <f4> <f5> <f6> <f7> <f8> <f9>")

	for _, key := range []term.Key{term.KeyF1, term.KeyF10} {
		exit, handled := tut.Handle(term.Event{Type: term.EventKey, Key: key})
		assert.False(t, exit)
		assert.Falsef(t, handled, "%v must fall through to the IDE", key)
		assert.True(t, tut.WaitActive("wait_command", time.Second))
	}
}

func TestBasicsTutorialHasNoHardcodedCommandKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"<meta-1>",
		"<ctrl-x>0",
		"<ctrl-x>1",
		"<ctrl-x>2",
		"<ctrl-x>3",
		"<ctrl-x>9",
		"<ctrl-tab>",
		"<ctrl-shift-tab>",
	} {
		assert.NotContainsf(t, basicsTutorial, key,
			"command key %s must be resolved through key_for", key)
	}
}

func TestBasicsTutorialResolvesDirectionalBindings(t *testing.T) {
	t.Parallel()

	requested := map[string]bool{}
	keyFor := func(cmd string, args []string) string {
		requested[strings.Join(append([]string{cmd}, args...), " ")] = true
		return ""
	}

	_, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)

	for _, command := range []string{
		"windowfocus up",
		"windowfocus left",
		"windowfocus down",
		"windowfocus right",
		"windowmove up",
		"windowmove left",
		"windowmove down",
		"windowmove right",
		"windowresize increase height",
		"windowresize decrease width",
		"windowresize decrease height",
		"windowresize increase width",
		"windowdefaultsplit h",
		"tabnext",
		"tabprevious",
		"tabmove left",
		"tabmove right",
	} {
		assert.Truef(t, requested[command],
			"the basics tutorial must resolve %q through the active preset", command)
	}
}

func TestBasicsTutorialResolvesEmacsLayoutBindings(t *testing.T) {
	t.Parallel()

	requested := map[string]bool{}
	keyFor := func(cmd string, args []string) string {
		requested[strings.Join(append([]string{cmd}, args...), " ")] = true
		return ""
	}

	_, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"emacs", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)

	for _, command := range []string{
		"windowtogglemaximize",
		"windownew down",
		"windownew right",
		"windowclose",
		"windowcloseall",
		"windowfocus up",
		"windowfocus left",
		"windowfocus down",
		"windowfocus right",
		"windowmove up",
		"windowmove left",
		"windowmove down",
		"windowmove right",
		"windowresize increase height",
		"windowresize decrease width",
		"windowresize decrease height",
		"windowresize increase width",
		"tabnext",
		"tabprevious",
		"tabmove left",
		"tabmove right",
		"tabnew",
		"tabclose",
	} {
		assert.Truef(t, requested[command],
			"the Emacs basics tutorial must resolve %q through the active preset", command)
	}
}

// TestBasicsTutorialLayoutKeysPlayable asserts that the step showing
// the "Your current layout keys" table lets the user actually try the
// bindings it lists: they must reach the IDE while the step stays up,
// otherwise the table is just something to read past.
func TestBasicsTutorialLayoutKeysPlayable(t *testing.T) {
	t.Parallel()

	layoutKeys := map[string]string{
		"windowfocus up":    "<alt-i>",
		"windowfocus left":  "<alt-j>",
		"windowfocus down":  "<alt-k>",
		"windowfocus right": "<alt-l>",
		"windowmove right":  "<alt-shift-l>",
	}
	keyFor := func(cmd string, args []string) string {
		return layoutKeys[strings.Join(append([]string{cmd}, args...), " ")]
	}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, &capturingNotis{}, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(100, 30)
	tut.Reset()

	observe := func(command string, args ...string) {
		t.Helper()
		require.True(t, tut.WaitActive("wait_command", time.Second),
			"expected a wait_command step")
		tut.ObserveCommand(command, command, args, nil)
	}

	observe("workspaceopen", "/tmp/tutorial-workspace")
	observe("edit", "README.md")
	observe("windownew")
	observe("terminalneworsplit")
	observe("windowdefaultsplit", "h")
	observe("terminalneworsplit")

	require.True(t, tut.WaitActive("wait_command", time.Second))
	require.Contains(t, tut.ActiveText(), "Your current layout keys")
	for _, spec := range []string{"<alt-i>", "<alt-l>", "<alt-shift-l>"} {
		ks, err := term.ParseKeys(spec)
		require.NoError(t, err)
		require.Len(t, ks, 1)
		exit, handled := tut.Handle(term.Event{
			Type: term.EventKey, Key: ks[0].Key, Mod: ks[0].Mod, Ch: ks[0].Ch,
		})
		assert.False(t, exit)
		assert.Falsef(t, handled,
			"%s must fall through to the IDE so the user can try it", spec)
		assert.Truef(t, tut.WaitActive("wait_command", time.Second),
			"%s must not advance past the layout table", spec)
		assert.Contains(t, tut.ActiveText(), "Your current layout keys")
	}
}

// TestBasicsTutorialDirectionalHintNamesKey asserts that both halves
// of a two-direction lesson name the chords they are waiting for:
// direction-qualified commands have no binding under their bare name,
// so the copy must resolve the exact invocation.
func TestBasicsTutorialDirectionalHintNamesKey(t *testing.T) {
	t.Parallel()

	layoutKeys := map[string]string{
		"windowfocus up":   "<alt-i>",
		"windowfocus left": "<alt-j>",
		"windowmove right": "<alt-shift-l>",
		"windowmove left":  "<alt-shift-j>",
	}
	keyFor := func(cmd string, args []string) string {
		return layoutKeys[strings.Join(append([]string{cmd}, args...), " ")]
	}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, &capturingNotis{}, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(100, 30)
	tut.Reset()

	observe := func(command string, args ...string) {
		t.Helper()
		require.True(t, tut.WaitActive("wait_command", time.Second))
		tut.ObserveCommand(command, command, args, nil)
	}

	observe("workspaceopen", "/tmp/tutorial-workspace")
	observe("edit", "README.md")
	observe("windownew")
	observe("terminalneworsplit")
	observe("windowdefaultsplit", "h")
	observe("terminalneworsplit")

	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, tut.ActiveText(), "<alt-i>")
	assert.Contains(t, tut.ActiveText(), "<alt-j>")
	tut.ObserveCommand("windowfocus", "windowfocus", []string{"up"}, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, tut.ActiveText(), "<alt-j>")
	tut.ObserveCommand("windowfocus", "windowfocus", []string{"left"}, nil)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, tut.ActiveText(), "<alt-shift-l>")
	assert.Contains(t, tut.ActiveText(), "<alt-shift-j>")
	tut.ObserveCommand("windowmove", "windowmove", []string{"right"}, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, tut.ActiveText(), "<alt-shift-j>")
}

func TestBasicsTutorialEmacsSplitHintNamesKey(t *testing.T) {
	t.Parallel()

	var (
		lookupMu sync.Mutex
		lookups  []string
	)
	keyFor := func(cmd string, args []string) string {
		lookupMu.Lock()
		lookups = append(lookups,
			strings.Join(append([]string{cmd}, args...), " "))
		lookupMu.Unlock()
		if cmd == "windownew" && slices.Equal(args, []string{"right"}) {
			return "<meta-r>"
		}
		return ""
	}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, &capturingNotis{}, nil,
		nil, nil,
		term.KeyComb{Ch: ':'}, "emacs", "", keyFor,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(100, 30)
	tut.Reset()

	observe := func(command string, args ...string) {
		t.Helper()
		require.True(t, tut.WaitActive("wait_command", time.Second))
		tut.ObserveCommand(command, command, args, nil)
	}

	observe("workspaceopen", "/tmp/tutorial-workspace")
	observe("edit", "README.md")
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, tut.ActiveText(),
		"Split the focused window in two: press `<meta-r>`.")
	lookupMu.Lock()
	assert.Contains(t, lookups, "windownew right",
		"the split step must resolve the expected invocation, not its bare command")
	lookupMu.Unlock()
}

func TestBasicsTutorialDirectionalCommandFlow(t *testing.T) {
	t.Parallel()

	notis := &capturingNotis{}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(100, 30)
	tut.Reset()

	wait := func(kind string) {
		t.Helper()
		require.True(t, tut.WaitActive(kind, time.Second),
			"expected a %s step", kind)
	}
	observe := func(command string, args ...string) {
		t.Helper()
		wait("wait_command")
		tut.ObserveCommand(command, command, args, nil)
	}

	observe("workspaceopen", "/tmp/tutorial-workspace")
	observe("edit", "README.md")

	// Keys never advance a step: only the milestone does.
	wait("wait_command")
	for _, ev := range []term.Event{
		{Type: term.EventKey, Key: term.KeyEnter},
		{Type: term.EventKey, Key: term.KeySpace},
		{Type: term.EventKey, Key: term.KeyEsc},
		{Type: term.EventKey, Ch: ':'},
	} {
		_, _ = tut.Handle(ev)
	}
	wait("wait_command")
	assert.Equal(t, "Split a window", tut.ActiveTitle())
	observe("windownew")
	observe("terminalneworsplit")
	observe("windowdefaultsplit", "h")
	observe("terminalneworsplit")

	observe("windowfocus", "up")
	// A wrong direction keeps the step armed.
	wait("wait_command")
	tut.ObserveCommand("windowfocus", "windowfocus", []string{"right"}, nil)
	observe("windowfocus", "left")

	observe("windowmove", "right")
	observe("windowmove", "left")

	wait("wait_command")
	tut.ObserveCommand("windowresize", "windowresize",
		[]string{"increase", "height"}, nil)
	observe("windowresize", "increase", "width")
	observe("windowresize", "decrease", "width")

	observe("windowtogglemaximize")
	// The close lesson teaches closing, not focusing: whichever window
	// the user happens to be on, one windowclose has to move it along.
	wait("wait_command")
	assert.Equal(t, "Close a window", tut.ActiveTitle())
	tut.ObserveCommand("windowclose", "windowclose", nil, nil)
	// The emacs-only "Keep one window" lesson must not run here, so the
	// tab lesson follows the close lesson directly.
	wait("wait_command")
	assert.Equal(t, "Open another tab", tut.ActiveTitle())
	tut.ObserveCommand("fexplorer", "fexplorer", nil, nil)
	wait("wait_event")
	tut.ObserveEvent("open", "file:///README.md")
	observe("fexplorer")
	observe("tabnext")
	observe("tabprevious")

	observe("tabmove", "left")
	observe("tabmove", "right")

	observe("tabclose")
	observe("!", "git", "log")
	observe("windowclose")

	observe("guitheme", "mullen")

	observe("config")

	wait("wait_event")
	assert.False(t, tut.ObserveEvent("flush", "file:///README.md"),
		"saving an unrelated buffer must not advance the config step")
	require.True(t, tut.WaitActive("wait_event", time.Second),
		"the config step stays armed after an unrelated flush")
	tut.ObserveEvent("flush", "file:///home/u/.rune/config.yaml")

	wait("wait_command")
	assert.Contains(t, tut.ActiveText(), "Rune reads the config once at startup")
	observe("cheatsheet")
	require.True(t, tut.WaitFinished(time.Second))

	assert.Empty(t, notis.successes(),
		"reaching a milestone must not raise a notification")
}

func TestBasicsTutorialEmacsWindowFlow(t *testing.T) {
	t.Parallel()

	notis := &capturingNotis{}
	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"emacs", "", nil,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(100, 30)
	tut.Reset()

	wait := func(kind string) {
		t.Helper()
		require.True(t, tut.WaitActive(kind, time.Second),
			"expected a %s step", kind)
	}
	observe := func(command string, args ...string) {
		t.Helper()
		wait("wait_command")
		tut.ObserveCommand(command, command, args, nil)
	}

	observe("workspaceopen", "/tmp/tutorial-workspace")
	observe("edit", "README.md")

	// Emacs has no bare windownew; the split step insists on the
	// rightward split so every mode ends up with the same layout.
	wait("wait_command")
	tut.ObserveCommand("windownew", "windownew", []string{"down"}, nil)
	wait("wait_command")
	assert.Equal(t, "Split a window", tut.ActiveTitle())
	tut.ObserveCommand("windownew", "windownew", []string{"right"}, nil)

	observe("terminalneworsplit")
	observe("windowdefaultsplit", "h")
	observe("terminalneworsplit")

	observe("windowfocus", "up")
	observe("windowfocus", "left")
	observe("windowmove", "right")
	observe("windowmove", "left")
	observe("windowresize", "increase", "width")
	observe("windowresize", "decrease", "width")
	observe("windowtogglemaximize")
	wait("wait_command")
	assert.Equal(t, "Close a window", tut.ActiveTitle())
	tut.ObserveCommand("windowclose", "windowclose", nil, nil)
	wait("wait_command")
	assert.Equal(t, "Keep one window", tut.ActiveTitle())
	observe("windowcloseall")
	wait("wait_command")
	assert.Equal(t, "Open another tab", tut.ActiveTitle())

	assert.Empty(t, notis.successes(),
		"reaching a milestone must not raise a notification")
}

func TestAgentTutorialInstallAndHelpFlow(t *testing.T) {
	t.Parallel()

	notis := &capturingNotis{}
	tut, err := starlarktutorial.New(
		"agent", agentTutorial,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(80, 24)
	tut.Reset()

	wait := func(kind string) {
		t.Helper()
		require.True(t, tut.WaitActive(kind, time.Second),
			"expected a %s step", kind)
	}

	// Layout cleanup comes first so the console lands in a clean layout.
	wait("wait_command")
	assert.Equal(t, "Clear the layout", tut.ActiveTitle())
	tut.ObserveCommand("windowcloseall", "windowcloseall", nil, nil)

	// The console introduction rides along with the console step.
	wait("wait_command")
	assert.Equal(t, "Set up the Rune Agent", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "Rune console")
	tut.ObserveCommand("console", "console", nil, nil)

	// Installation requires the console's shell observation; console
	// keystrokes pass through the armed step untouched.
	wait("wait_shell")
	_, handled := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.False(t, handled)
	wait("wait_shell")
	// The step is armed even for a user who already has the package,
	// so it has to say how to get past it.
	assert.Contains(t, tut.ActiveText(), "already installed")
	tut.ObserveCommand("console", "console", []string{"pkg", "install", "rune-agent"}, nil)

	// Dismissing the provider choice skips straight to the help step.
	wait("choice")
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	wait("wait_command")
	assert.Equal(t, "One last thing", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "https://discord.gg/quxhV7khwg")
	tut.ObserveCommand("help", "help", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))
	assert.Empty(t, notis.successes(),
		"reaching a milestone must not raise a notification")
}

func TestTutorialPackageInstallOwnership(t *testing.T) {
	t.Parallel()
	// The basics tutorial installs nothing and never sends the user to
	// the console; the agent tutorial owns both the console introduction
	// and the only package install of the playlist.
	assert.NotContains(t, basicsTutorial, "pkg install")
	assert.NotContains(t, basicsTutorial, `command  = "console"`)
	assert.Contains(t, agentTutorial, "Rune console")
	assert.Contains(t, agentTutorial, "pkg install rune-agent")
}

// TestTutorialsThatNeedAWorkspaceRefuseTheHomeWorkspace asserts the
// lessons whose steps only work inside a project workspace bail out
// with an error instead of arming a step the user cannot complete.
// On the home workspace commands like `agent` are answered by Rune's
// "open a workspace first" fallback, so the step would never resolve.
func TestTutorialsThatNeedAWorkspaceRefuseTheHomeWorkspace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
	}{
		{"navigation", navigationTutorial},
		{"agent", agentTutorial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notis := &recordingNotis{}
			tut, err := starlarktutorial.New(
				tt.name, tt.src,
				idetutorial.PromptStyle{}, nil, notis, nil,
				nil, nil,
				term.KeyComb{Ch: ':'},
				"standard", "", nil,
				nil,
				func() bool { return false }, // workspace_open()
				func() bool { return true },  // is_lsp_server_running()
			)
			require.NoError(t, err)
			tut.Resize(24, 20)
			tut.Reset()
			require.True(t, tut.WaitFinished(2*time.Second),
				"the lesson must end without arming a step")
			assert.Empty(t, tut.ActiveKind(),
				"no step may be armed off a workspace")
			_, errs, _ := notis.snapshot()
			assert.Contains(t, strings.Join(errs, "\n"),
				"Open a workspace first")
		})
	}
}

// TestNavigationTutorialFlow drives the embedded navigation tutorial
// through its full step sequence and pins the ordering the tutorial
// teaches: definition-under-cursor comes before definition-by-name, the
// cursor history is walked back then forward as two distinct steps, and
// the `lsp` verbs step comes last.
func TestNavigationTutorialFlow(t *testing.T) {
	t.Parallel()

	alwaysTrue := func() bool { return true }
	// Give searchfile/searchtext bound keys so their copy renders the
	// keypress wording. Whether the finder steps run at all is decided
	// by command_exists, which defaults to true with no manual lookup
	// wired, so this flow covers the already-installed path.
	keyFor := func(cmd string, _ []string) string {
		switch cmd {
		case "searchfile":
			return "<c-p>"
		case "searchtext":
			return "<c-f>"
		}
		return ""
	}
	notis := &capturingNotis{}
	tut, err := starlarktutorial.New(
		"navigation", navigationTutorial,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil,
		alwaysTrue, // workspace_open()
		alwaysTrue, // is_lsp_server_running()
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	tut.Reset()

	waitCmd := func() {
		t.Helper()
		require.True(t, tut.WaitActive("wait_command", 2*time.Second),
			"expected a wait_command step")
	}
	waitEvent := func() {
		t.Helper()
		require.True(t, tut.WaitActive("wait_event", 2*time.Second),
			"expected a wait_event step")
	}

	// Layout cleanup comes first so the tutorial starts on a clean
	// screen; the intro rides along with it.
	waitCmd()
	assert.Equal(t, "Navigate code 🧭", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "How do I find a file by name?")
	tut.ObserveCommand("windowcloseall", "windowcloseall", nil, nil)

	// searchfile: command -> file-open event.
	waitCmd()
	assert.Equal(t, "Find a file by name", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "press `<c-p>`")
	tut.ObserveCommand("searchfile", "searchfile", nil, nil)
	waitEvent()
	assert.Contains(t, tut.ActiveText(), "do not have to be contiguous")
	assert.Contains(t, tut.ActiveText(), "<up>")
	assert.Contains(t, tut.ActiveText(), "<down>")
	tut.ObserveEvent("open", "file:///workspace/a.go")

	// searchtext: command -> file-open event.
	waitCmd()
	assert.Equal(t, "Search for text", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "press `<c-f>`")
	tut.ObserveCommand("searchtext", "searchtext", nil, nil)
	waitEvent()
	assert.Contains(t, tut.ActiveText(),
		"Type the word you are after, or a few characters of it.")
	assert.Contains(t, tut.ActiveText(), "do not have to be contiguous")
	assert.Contains(t, tut.ActiveText(), "<up>")
	assert.Contains(t, tut.ActiveText(), "<down>")
	assert.NotContains(t, tut.ActiveText(), "<ctrl-i>")
	assert.NotContains(t, tut.ActiveText(), "<ctrl-k>")
	tut.ObserveEvent("open", "file:///workspace/b.go")

	// jumptoast: fuzzy-jump to a function in the file.
	waitCmd()
	assert.Equal(t, "Jump to a function in this file", tut.ActiveTitle())
	// The syntax-tree steps are useless on a LICENSE or a README, so
	// the step says which kind of file it needs.
	assert.Contains(t, tut.ActiveText(), "source file")
	tut.ObserveCommand("jumptoast", "jumptoast",
		[]string{"locals.scm", "local.definition.method|local.definition.function", "run"}, nil)

	// Definition under the cursor comes FIRST (the basic verb), and the
	// lsp intro rides along with it...
	waitCmd()
	assert.Equal(t, "Go to definition", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "language server")
	assert.Contains(t, tut.ActiveText(), "Put your cursor on a symbol")
	tut.ObserveCommand("lsp", "lsp", []string{"definition"}, nil)

	// ...then definition by name (fuzzy symbol search).
	waitCmd()
	assert.Equal(t, "Find a definition by name", tut.ActiveTitle())
	tut.ObserveCommand("lsp", "lsp", []string{"definition", "SomeSymbol"}, nil)

	// Cursor history: jump back...
	waitCmd()
	assert.Equal(t, "Navigate back and forth", tut.ActiveTitle())
	tut.ObserveCommand("cursorhistory", "cursorhistory", []string{"prev"}, nil)

	// ...then jump forward again as a distinct step.
	waitCmd()
	assert.Equal(t, "Jump forward", tut.ActiveTitle())
	tut.ObserveCommand("cursorhistory", "cursorhistory", []string{"next"}, nil)

	// lsp references (the other verbs work the same way).
	waitCmd()
	assert.Equal(t, "Ask about a symbol", tut.ActiveTitle())
	tut.ObserveCommand("lsp", "lsp", []string{"references"}, nil)

	// The wrap-up is a notification, so the last milestone finishes
	// the lesson.
	require.True(t, tut.WaitFinished(2*time.Second),
		"tutorial must finish after the last step")

	assert.Empty(t, notis.successes(),
		"reaching a milestone must not raise a notification")
}

// TestNavigationTutorialPrefillKeysMatchPresets pins the chords the
// tutorial hardcodes against the presets that bind them. They cannot
// come from key_for because they are prompt-prefill macros, and the
// jumptoast copy went stale once already when the emacs preset moved
// that binding off <ctrl-x>j.
func TestNavigationTutorialPrefillKeysMatchPresets(t *testing.T) {
	t.Parallel()

	const (
		jumpPrefill = `": "echo {prompt}jumptoast<space>locals.scm<space>` +
			`local.definition.method|local.definition.function<space>"`
		defPrefill = `": "echo {prompt}lsp<space>definition<space>"`
	)
	tests := []struct {
		mode    string
		jumpKey string
		defKey  string
		preset  string
	}{
		{
			mode:    "emacs",
			jumpKey: "<meta-j>",
			defKey:  "<ctrl-alt-.>",
			preset:  presetEmacsYAML,
		},
		{
			mode:    "modal",
			jumpKey: "<alt-f>",
			defKey:  "<alt-shift-d>",
			preset:  presetModalYAML,
		},
		{
			mode:    "standard",
			jumpKey: "<alt-f>",
			defKey:  "<alt-shift-d>",
			preset:  presetStandardYAML,
		},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, tt.preset, `"`+tt.jumpKey+jumpPrefill,
				"the preset must bind the chord the tutorial teaches")
			assert.Contains(t, tt.preset, `"`+tt.defKey+defPrefill,
				"the preset must bind the chord the tutorial teaches")

			src := `
def run():
    wait_command(command = "nonesuch", text = jump_symbol_md + lsp_definition_name_md)
tutorial(entry=run)
`
			modeSrc := withoutTutorialCall(t, navigationTutorial) + src
			tut, err := starlarktutorial.New(
				"navigation-prefill-keys", modeSrc,
				idetutorial.PromptStyle{}, nil, nil, nil,
				nil, nil,
				term.KeyComb{Ch: ':'}, tt.mode, "",
				nil, nil, nil, nil,
			)
			require.NoError(t, err)
			tut.Resize(80, 24)
			tut.Reset()
			require.True(t, tut.WaitActive("wait_command", time.Second))
			assert.Contains(t, tut.ActiveText(), tt.jumpKey)
			assert.Contains(t, tut.ActiveText(), tt.defKey)
		})
	}
}

func TestNavigationTutorialFinderPickerKeysByMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode      string
		up        string
		down      string
		forbidden []string
	}{
		{mode: "modal", up: "<ctrl-k>", down: "<ctrl-j>"},
		{
			mode:      "standard",
			up:        "<up>",
			down:      "<down>",
			forbidden: []string{"<ctrl-i>", "<ctrl-k>"},
		},
		{mode: "emacs", up: "<ctrl-p>", down: "<ctrl-n>"},
	}
	// Every screen that asks the user to walk a completion list has to
	// name that preset's own bindings.
	pickers := []string{
		"searchfile_picker_md", "searchtext_picker_md", "jump_symbol_md",
		"lsp_definition_name_md",
	}
	for _, picker := range pickers {
		for _, tt := range tests {
			t.Run(picker+"/"+tt.mode, func(t *testing.T) {
				t.Parallel()
				src := `
def run():
    wait_event(event="open", text=` + picker + `)
tutorial(entry=run)
`
				modeSrc := withoutTutorialCall(t, navigationTutorial) + src
				tut, err := starlarktutorial.New(
					"navigation-picker-keys", modeSrc,
					idetutorial.PromptStyle{}, nil, nil, nil,
					nil, nil,
					term.KeyComb{Ch: ':'}, tt.mode, "",
					nil, nil, nil, nil,
				)
				require.NoError(t, err)
				tut.Resize(80, 24)
				tut.Reset()
				require.True(t, tut.WaitActive("wait_event", time.Second))

				text := tut.ActiveText()
				assert.Contains(t, text, tt.up)
				assert.Contains(t, text, tt.down)
				assert.Contains(t, text, "<up>")
				assert.Contains(t, text, "<down>")
				for _, key := range tt.forbidden {
					assert.NotContains(t, text, key)
				}
			})
		}
	}
}

// TestNavigationTutorialInstallsFuzzySearchFirst asserts that when the
// fuzzy-search extension is missing, the tutorial walks the user
// through installing it from the console before reaching the finder
// steps. Without this, the first `searchfile` would trigger Rune's own
// install prompt while the tutorial assumed a finder was already open.
func TestNavigationTutorialInstallsFuzzySearchFirst(t *testing.T) {
	t.Parallel()

	alwaysTrue := func() bool { return true }
	lookup := func(name string) (command.Manual, bool) {
		return command.Manual{}, false
	}
	notis := &capturingNotis{}
	tut, err := starlarktutorial.New(
		"navigation", navigationTutorial,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil, lookup,
		alwaysTrue, // workspace_open()
		alwaysTrue, // is_lsp_server_running()
	)
	require.NoError(t, err)
	tut.Resize(80, 24)
	tut.Reset()

	wait := func(kind string) {
		t.Helper()
		require.True(t, tut.WaitActive(kind, 2*time.Second),
			"expected a %s step", kind)
	}

	wait("wait_command") // clear the layout
	tut.ObserveCommand("windowcloseall", "windowcloseall", nil, nil)

	wait("wait_command") // open the console
	assert.Equal(t, "Install the finder", tut.ActiveTitle())
	assert.Contains(t, tut.ActiveText(), "not installed yet")
	tut.ObserveCommand("console", "console", nil, nil)

	wait("wait_shell") // pkg install fuzzy-search
	// The console is focused here, so the step must let the user type
	// the install command instead of swallowing its keys.
	_, handled := tut.Handle(term.Event{Type: term.EventKey, Ch: 'p'})
	assert.False(t, handled, "wait_shell must not swallow console input")
	assert.Contains(t, tut.ActiveText(), "Rune's console")
	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "fuzzy-search"}, nil)

	// Command registration is asynchronous after the install completes,
	// so the live lookup can still be stale here. A successful install
	// must proceed to the finder lesson rather than claim it failed.
	wait("wait_command") // searchfile
	assert.Equal(t, "Find a file by name", tut.ActiveTitle())
	tut.ObserveCommand("searchfile", "searchfile", nil, nil)
	wait("wait_event")
	tut.ObserveEvent("open", "file:///workspace/a.go")

	assert.Empty(t, notis.successes(),
		"reaching a milestone must not raise a notification")
}

// capturingNotis records success-level notification messages in order so
// tests can assert the sequence of tutorial steps that completed.
type capturingNotis struct {
	mu   sync.Mutex
	msgs []string
}

func (n *capturingNotis) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	if level == browserapi.LevelSuccess {
		n.mu.Lock()
		n.msgs = append(n.msgs, fmt.Sprintf(msg, args...))
		n.mu.Unlock()
	}
	return "", nil
}

func (n *capturingNotis) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *capturingNotis) UpdateNotificationProgress(
	_, _ string, _, _ int64,
) error {
	return nil
}

func (n *capturingNotis) successes() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.msgs))
	copy(out, n.msgs)
	return out
}

// TestNavigationTutorialParses asserts that the embedded navigation.star
// tutorial parses through starlarktutorial.New, registers a callable
// entry, and reports the expected id/title/version.
func TestNavigationTutorialParses(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, navigationTutorial,
		"navigationTutorial embed must not be empty")
	tut, err := starlarktutorial.New(
		"navigation", navigationTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"standard", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)

	assert.Equal(t, "navigation", tut.ID())
	assert.Equal(t, "Navigate code", tut.Title())
	assert.Equal(t, "26", tut.Version())
}

func TestAgentTutorialParses(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, agentTutorial, "agentTutorial embed must not be empty")
	tut, err := starlarktutorial.New(
		"agent", agentTutorial,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	assert.Equal(t, "agent", tut.ID())
	assert.Equal(t, "Rune Agent", tut.Title())
	assert.Equal(t, "12", tut.Version())
}

func TestEmbeddedTutorialPlaylist(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []ide.TutorialPlaylistItem{
		{
			Name:        "basics",
			Description: "Learn the essential Rune workspace and window management commands and key bindings.",
		},
		{
			Name:        "navigation",
			Description: "Learn about structural navigation and how to exploit Rune's code intelligence tools.",
		},
		{
			Name:        "agent",
			Description: "Install Rune Agent, connect a model provider, start a conversation, and get help.",
		},
	}, embeddedTutorialPlaylist)
	assert.NotEmpty(t, embeddedTutorialOptions())
}

// TestNavigationTutorialParsesModalMode asserts the embedded navigation
// tutorial also parses under modal editor mode.
func TestNavigationTutorialParsesModalMode(t *testing.T) {
	t.Parallel()

	tut, err := starlarktutorial.New(
		"navigation", navigationTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"modal", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	assert.Equal(t, "26", tut.Version())
}

// TestNavigationTutorialParsesEmacsMode asserts the embedded navigation
// tutorial also parses under the emacs editor mode.
func TestNavigationTutorialParsesEmacsMode(t *testing.T) {
	t.Parallel()

	tut, err := starlarktutorial.New(
		"navigation", navigationTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"emacs", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	assert.Equal(t, "26", tut.Version())
}

// TestBasicsTutorialParsesEmacsMode asserts the embedded basics tutorial
// also parses under the emacs editor mode, exercising the emacs branch of
// the direction-phrasing logic (IJKL layout, GNU-Emacs buffer motion keys,
// arrow-key completer).
func TestBasicsTutorialParsesEmacsMode(t *testing.T) {
	t.Parallel()

	tut, err := starlarktutorial.New(
		"basics", basicsTutorial,
		idetutorial.PromptStyle{},
		nil,
		nil,
		nil,
		nil,
		nil,
		term.KeyComb{Ch: ':'},
		"emacs", "",
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	assert.Equal(t, "70", tut.Version())
}

// TestShippedWaitCommandsCarryTheirLesson guards the shape of a
// lesson: a step's screen stays up until its milestone is met and
// there is no separate copy screen, so a wait_command without text
// would leave the user with nothing but a generated one-liner.
func TestShippedWaitCommandsCarryTheirLesson(t *testing.T) {
	t.Parallel()

	for name, src := range shippedTutorials() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := waitCommandCalls(src)
			require.NotEmpty(t, calls)
			for _, call := range calls {
				if call.text != "" {
					continue
				}
				t.Errorf("wait_command(command=%q) carries no text; "+
					"the step's screen is the lesson, so say what the "+
					"user is meant to learn and do", call.command)
			}
		})
	}
}

type waitCommandCall struct {
	command string
	text    string
}

var (
	waitCommandRe = regexp.MustCompile(`(?s)wait_command\((.*?)\n    \)`)
	kwargRe       = regexp.MustCompile(`(?m)^\s*(command|text)\s*=\s*(.*)$`)
)

// waitCommandCalls extracts the command and whether text was supplied
// for every multi-line wait_command call in src. Single-line calls
// carry neither kwarg on its own line and are skipped.
func waitCommandCalls(src string) []waitCommandCall {
	var out []waitCommandCall
	for _, m := range waitCommandRe.FindAllStringSubmatch(src, -1) {
		var call waitCommandCall
		for _, kw := range kwargRe.FindAllStringSubmatch(m[1], -1) {
			value := strings.Trim(strings.TrimSpace(kw[2]), `",(`)
			switch kw[1] {
			case "command":
				call.command = value
			case "text":
				call.text = value
			}
		}
		if call.command != "" {
			out = append(out, call)
		}
	}
	return out
}
