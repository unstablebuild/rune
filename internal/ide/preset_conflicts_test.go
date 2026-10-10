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
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/idepreset"
	"unstable.build/rune/internal/ide/keymeta"
	"unstable.build/rune/internal/ide/starlarkconfig"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

const (
	vimPreset      = "preset_modal_linux.yaml"
	helixPreset    = "preset_helix_linux.yaml"
	standardPreset = "preset_standard_linux.yaml"
	emacsPreset    = "preset_emacs_linux.yaml"
)

var linuxPresets = []struct{ file, mode string }{
	{vimPreset, editorModeVim},
	{helixPreset, editorModeHelix},
	{standardPreset, editorModeStandard},
	{emacsPreset, editorModeEmacs},
}

// loadLinuxPreset loads a Linux preset the way a first run on Linux does:
// rendered with meta over the shipped defaults, then with the
// fuzzy_search package installed, whose settings only fill what the
// preset leaves unset, key bindings matched by their raw spelling.
func loadLinuxPreset(t *testing.T, file, mode string, meta keymeta.Meta) ideConfig {
	t.Helper()
	base, err := decodeDefaultConfig(DefaultConfig{
		src: string(readRuneStar(t)), modal: true, tui: false,
	})
	require.NoError(t, err)
	cfg, err := decodeOverlayConfigFile(bytes.NewReader(
		readPreset(t, file, idepreset.Data{Meta: meta})), file, base)
	require.NoError(t, err)

	src, err := os.ReadFile("../../cmd/extension_fuzzy_search/config.star")
	require.NoError(t, err)
	pkg, err := starlarkconfig.Decode(starlarkconfig.Source{
		Src:      src,
		Filename: "config.star",
		Params: map[string]any{
			"RUNE_DATADIR":     "/data",
			"RUNE_PKG_ID":      "fuzzy_search",
			"RUNE_PKG_VERSION": "1",
			"RUNE_EDITOR_MODE": mode,
		},
	})
	require.NoError(t, err)
	fillMissing(cfg, pkg)

	require.NoError(t, validateConfigOn(cfg, "linux"))
	return ideConfig{cfg: cfg, errors: map[string]error{}, goos: "linux"}
}

func fillMissing(dst, src map[string]any) {
	for k, v := range src {
		prev, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		prevMap, ok1 := prev.(map[string]any)
		srcMap, ok2 := v.(map[string]any)
		if ok1 && ok2 {
			fillMissing(prevMap, srcMap)
		}
	}
}

// runeChords returns the chords Rune claims for itself, sorted: the first
// key of every command binding, the command prompt key and the terminal's
// scrollback find key.
func runeChords(c ideConfig) []term.KeyComb {
	set := map[term.KeyComb]bool{
		c.commandKey():                   true,
		c.terminalSearchConfig().FindKey: true,
	}
	for seq, cmds := range c.commandKeyMappings() {
		if !isUnbindCommand(cmds) {
			set[seq.First] = true
		}
	}
	keys := make([]term.KeyComb, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

// probe is one surface a key can land on before Rune's own bindings, in
// one of its states. fresh builds it anew, since a key it handles can
// leave it in another state.
type probe struct {
	surface string
	fresh   func() tui.Handler
}

func chordEvent(k term.KeyComb) term.Event {
	return term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod}
}

func mustKeys(t *testing.T, spec string) []term.KeyComb {
	t.Helper()
	keys, err := term.ParseKeys(spec)
	require.NoError(t, err)
	return keys
}

// editorProbes returns the preset's real buffer editor in each of its
// states, and the command prompt's editor and the input box.
func editorProbes(t *testing.T, c ideConfig, mode string) []probe {
	t.Helper()
	schedule := func(fn func()) bool { fn(); return true }
	buffer := func(prep string, h func(buf *cell.Buffer) tui.Handler) func() tui.Handler {
		keys := mustKeys(t, prep)
		return func() tui.Handler {
			buf := cell.NewBuffer()
			buf.WriteString("alpha beta gamma\ndelta epsilon\n")
			ret := h(buf)
			ret.Resize(80, 24)
			for _, k := range keys {
				ret.Handle(chordEvent(k))
			}
			return ret
		}
	}
	uri := workspaceapi.RandomURI("memory")
	var edit func(buf *cell.Buffer) tui.Handler
	var states []struct{ name, prep string }
	switch mode {
	case editorModeVim:
		edit = func(buf *cell.Buffer) tui.Handler {
			return vi.NewWithIndent(buf, uri, text.IndentRuneTab, 4,
				vi.WithScheduleNextTick(schedule), vi.WithClipboard(clipboard.NewInMemory()))
		}
		states = []struct{ name, prep string }{
			{"normal", ""}, {"insert", "i"}, {"visual", "v"},
		}
	case editorModeHelix:
		edit = func(buf *cell.Buffer) tui.Handler {
			return helix.NewWithIndent(buf, uri, text.IndentRuneTab, 4,
				helix.WithScheduleNextTick(schedule),
				helix.WithClipboard(clipboard.NewInMemory()))
		}
		states = []struct{ name, prep string }{
			{"normal", ""}, {"insert", "i"}, {"select", "v"},
		}
	case editorModeEmacs:
		edit = func(buf *cell.Buffer) tui.Handler {
			return emacs.NewHandler(buf, uri, text.IndentRuneTab, 4,
				emacs.WithCommandBar(true), emacs.WithScheduleNextTick(schedule),
				emacs.WithClipboard(clipboard.NewInMemory()))
		}
		states = []struct{ name, prep string }{
			{"default", ""}, {"mark", "<ctrl-space>"},
		}
	case editorModeStandard:
		edit = func(buf *cell.Buffer) tui.Handler {
			search := c.standardSearchConfig(noSearchWindows{})
			return standard.NewHandler(buf, uri, text.IndentRuneTab, 4,
				standard.WithKeymap(standard.KeymapLinux),
				standard.WithCommandBar(true),
				standard.WithSearchConfig(search),
				standard.WithScheduleNextTick(schedule),
				standard.WithClipboard(clipboard.NewInMemory()))
		}
		states = []struct{ name, prep string }{
			{"plain", ""}, {"selection", "<shift-right><shift-right>"},
		}
	default:
		t.Fatalf("no probe for editor %q", mode)
	}
	var probes []probe
	for _, s := range states {
		probes = append(probes, probe{"buffer/" + s.name, buffer(s.prep, edit)})
	}
	prompt := (&workspaceManagerHandler{clip: clipboard.NewInMemory()}).newPromptEditor(c)
	probes = append(probes,
		probe{"prompt", buffer("", func(buf *cell.Buffer) tui.Handler {
			return prompt.Edit(buf)
		})},
		probe{"inputbox", func() tui.Handler {
			ib := inputbox.New()
			ib.Resize(80, 3)
			for _, r := range "alpha beta" {
				ib.Handle(term.Event{Type: term.EventKey, Ch: r})
			}
			return ib
		}},
	)
	return probes
}

// noSearchWindows fails to open the standard editor's search box; a probe
// only needs to know whether the editor takes the key.
type noSearchWindows struct{}

func (noSearchWindows) Floating(
	browserapi.Floating, browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return nil, errors.New("no windows in a key probe")
}

func (noSearchWindows) CloseWindow(browserapi.Window) error { return nil }

// overlap is a Rune chord another surface claims first, which makes the
// Rune command unreachable while that surface has focus.
type overlap struct {
	preset, meta, surface, chord string
}

func (o overlap) String() string {
	meta := "every gui.meta_key"
	if o.meta != "" {
		meta = "gui.meta_key " + o.meta
	}
	if o.surface == "terminal" {
		return fmt.Sprintf("%s with %s: the terminal sends %s to the shell",
			o.preset, meta, o.chord)
	}
	return fmt.Sprintf("%s with %s: %s handles %s", o.preset, meta, o.surface, o.chord)
}

// acceptedOverlap is an overlap a preset lives with on purpose. An empty
// meta accepts it under every meta. twin requires every command behind a
// chord the terminal keeps to also sit on a chord that reaches Rune from a
// terminal, except exactly the commands listed in twinless.
type acceptedOverlap struct {
	overlap
	twin     bool
	twinless []string
	reason   string
}

// acceptedOverlaps lists every overlap the Linux presets accept, and
// why. Every entry must still match an overlap the harness finds, so the
// list cannot rot.
var acceptedOverlaps = func() []acceptedOverlap {
	var out []acceptedOverlap
	add := func(preset, meta, chord, reason string, twin bool, surfaces ...string) {
		for _, s := range surfaces {
			out = append(out, acceptedOverlap{
				overlap: overlap{preset, meta, s, chord}, twin: twin, reason: reason,
			})
		}
	}
	// twinless names commands a terminal cannot reach yet: the preset binds
	// them only behind a chord the terminal keeps.
	twinless := func(preset, chord string, cmds ...string) {
		for i := range out {
			if out[i].preset == preset && out[i].chord == chord && out[i].surface == "terminal" {
				out[i].twinless = cmds
			}
		}
	}
	for _, preset := range []string{vimPreset, helixPreset} {
		add(preset, "", "<shift-;>",
			"`:` opens the prompt from normal mode, as in vim; typing surfaces type it",
			false, "buffer/insert", "inputbox", "terminal")
		add(preset, "", "<shift-tab>",
			"<shift-tab> opens the explorer from normal mode; typing surfaces "+
				"complete or outdent with it",
			false, "inputbox", "terminal")
		add(preset, "", "<ctrl-o>",
			"the jumplist keys step the cursor history of buffers only",
			false, "terminal")
		add(preset, "", "<ctrl-i>",
			"the jumplist keys step the cursor history of buffers only",
			false, "terminal")
		add(preset, "", "<ctrl-space>",
			"completion only applies to a buffer", false, "terminal")
		add(preset, "<alt>", "<alt-f>",
			"the input box keeps readline's M-f under <alt>", false, "inputbox")
	}
	add(vimPreset, "", "<ctrl-o>",
		"vim's insert-mode C-o runs one normal-mode command", false, "buffer/insert")
	add(vimPreset, "<alt>", "<alt-b>",
		"the input box keeps readline's M-b under <alt>", false, "inputbox")
	add(vimPreset, "<alt>", "<alt-d>",
		"the input box keeps readline's M-d under <alt>", false, "inputbox")

	add(helixPreset, "", "<shift-tab>",
		"Helix outdents with <shift-tab> in insert mode", false, "buffer/insert")
	add(helixPreset, "", "<space>",
		"the <space> menu is a normal-mode leader; typing surfaces type a space",
		false, "buffer/insert", "inputbox", "terminal")
	add(helixPreset, "<alt>", "<alt-c>",
		"Helix changes without yanking on A-c; clipboardcopy yields to it under <alt>",
		false, "buffer/normal", "buffer/select", "prompt")
	add(helixPreset, "", "<ctrl-w>",
		"Helix's <ctrl-w> window menu is a backup for muscle memory",
		true, "inputbox", "terminal")
	add(helixPreset, "", "<ctrl-tab>",
		"Helix's tab chords are a backup for the <meta> tab keys", true, "terminal")
	add(helixPreset, "", "<ctrl-shift-tab>",
		"Helix's tab chords are a backup for the <meta> tab keys", true, "terminal")

	add(standardPreset, "<alt>", "<alt-f>",
		"the input box keeps readline's M-f under <alt>", false, "inputbox")
	add(standardPreset, "<alt>", "<alt-b>",
		"the input box keeps readline's M-b under <alt>", false, "inputbox")
	add(standardPreset, "<alt>", "<alt-d>",
		"the input box keeps readline's M-d under <alt>", false, "inputbox")
	add(standardPreset, "", "<ctrl-space>",
		"completion only applies to a buffer", false, "terminal")
	for _, chord := range []string{"<f2>", "<shift-f2>", "<f3>", "<shift-f3>"} {
		add(standardPreset, "", chord,
			"bookmarks and search results step through buffers, as in VS Code",
			false, "terminal")
	}

	add(emacsPreset, "", "<ctrl-x>",
		"GNU's C-x chords are a second path to commands on <meta>", true, "terminal")

	twinless(helixPreset, "<ctrl-w>",
		"echo {prompt}edit<space>", "windowcloseall", "windowfocus other",
		"windownew down", "windownew right")
	twinless(emacsPreset, "<ctrl-x>",
		"exchangepointandmark", "searchfile", "undo prefix", "windowfocus other",
		"write", "writeall")
	return out
}()

func (o overlap) matches(a overlap) bool {
	return o.preset == a.preset && o.surface == a.surface && o.chord == a.chord &&
		(a.meta == "" || o.meta == a.meta)
}

func acceptedFor(o overlap) (acceptedOverlap, bool) {
	for _, a := range acceptedOverlaps {
		if o.matches(a.overlap) {
			return a, true
		}
	}
	return acceptedOverlap{}, false
}

// terminalReachable reports whether a focused terminal lets k through to
// Rune: it writes every key with only <ctrl> and <shift> to the shell.
func terminalReachable(k term.KeyComb) bool {
	return k.Mod&^term.ModCtrlShift != 0
}

// unboundChord is a chord no editor binds, to prove a probe can decline.
var unboundChord = term.KeyComb{Key: term.KeyF12, Mod: term.ModCtrl | term.ModAlt | term.ModMeta}

func TestPresetConflicts(t *testing.T) {
	var found []overlap
	bound := map[overlap]string{}
	for _, p := range linuxPresets {
		for _, meta := range keymeta.Options("linux", p.mode) {
			t.Run(p.file+"/"+meta.String(), func(t *testing.T) {
				c := loadLinuxPreset(t, p.file, p.mode, meta)
				chords := runeChords(c)
				requireNoCollapse(t, c, p.file, p.mode, meta)
				requireLoadsClean(t, c, p.file, meta)
				mappings := c.commandKeyMappings()

				var here []overlap
				record := func(surface string, k term.KeyComb) overlap {
					o := overlap{p.file, meta.String(), surface, k.String()}
					here = append(here, o)
					bound[o] = boundTo(mappings, k)
					return o
				}
				for _, pr := range editorProbes(t, c, p.mode) {
					_, handled := pr.fresh().Handle(chordEvent(unboundChord))
					require.Falsef(t, handled,
						"%s takes %s, so it cannot tell which chords it owns",
						pr.surface, unboundChord)
					h := pr.fresh()
					for _, k := range chords {
						if _, handled := h.Handle(chordEvent(k)); handled {
							record(pr.surface, k)
							h = pr.fresh()
						}
					}
				}
				for _, k := range chords {
					if terminalReachable(k) {
						continue
					}
					o := record("terminal", k)
					if a, ok := acceptedFor(o); ok && a.twin {
						requireTwins(t, o, a.twinless, terminalTwinless(c, k))
					}
				}
				found = append(found, here...)
			})
		}
	}

	var unexpected []string
	for _, o := range found {
		if _, ok := acceptedFor(o); !ok {
			line := "  " + o.String()
			if bound[o] != "" {
				line += " instead of running " + bound[o]
			}
			unexpected = append(unexpected, line)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Fatalf("These Rune chords are taken before Rune sees them, so their commands "+
			"never run while that surface has focus:\n%s\nMove each binding to a chord "+
			"nothing takes, or add it to acceptedOverlaps with the reason the preset "+
			"can live with it.", strings.Join(unexpected, "\n"))
	}
	// A failed case stops before it records its overlaps, so its entries
	// would look stale.
	if t.Failed() {
		return
	}
	for _, a := range acceptedOverlaps {
		if !slices.ContainsFunc(found, func(o overlap) bool {
			return o.matches(a.overlap)
		}) {
			t.Errorf("acceptedOverlaps lists %s (%s), but that no longer happens; "+
				"remove the entry", a.overlap, a.reason)
		}
	}
}

// terminalTwinless returns the commands bound behind chord, which a
// focused terminal keeps, that no chord reaching Rune from a terminal
// also runs.
func terminalTwinless(c ideConfig, chord term.KeyComb) []string {
	mappings := c.commandKeyMappings()
	reachable := map[string]bool{}
	for seq, cmds := range mappings {
		if terminalReachable(seq.First) {
			reachable[commandLine(cmds)] = true
		}
	}
	var out []string
	for seq, cmds := range mappings {
		if seq.First == chord && !isUnbindCommand(cmds) && !reachable[commandLine(cmds)] &&
			!slices.Contains(out, commandLine(cmds)) {
			out = append(out, commandLine(cmds))
		}
	}
	return out
}

func commandLine(cmds [][]string) string {
	lines := make([]string, len(cmds))
	for i, cmd := range cmds {
		lines[i] = strings.Join(cmd, " ")
	}
	return strings.Join(lines, "; ")
}

// requireTwins checks that the commands behind o's chord that no chord
// reaches from a terminal are exactly the ones accepted as twinless.
func requireTwins(t *testing.T, o overlap, accepted, got []string) {
	t.Helper()
	for _, cmd := range got {
		if !slices.Contains(accepted, cmd) {
			t.Errorf("%s, and %q has no other chord, so it never runs from a terminal. "+
				"Also bind it to a chord with a modifier other than <ctrl> or <shift>, or "+
				"list it as twinless for %s in acceptedOverlaps.", o, cmd, o.chord)
		}
	}
	for _, cmd := range accepted {
		if !slices.Contains(got, cmd) {
			t.Errorf("acceptedOverlaps lists %q as twinless for %s, but it is no longer "+
				"bound only behind that chord; remove it from the list.", cmd, o)
		}
	}
}

// boundTo quotes the commands of every binding that starts with chord.
func boundTo(mappings map[handler.Sequence][][]string, chord term.KeyComb) string {
	var out []string
	for seq, cmds := range mappings {
		line := strconv.Quote(commandLine(cmds))
		if seq.First == chord && !isUnbindCommand(cmds) && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// requireNoCollapse pins, independently of the config error, that no two
// spellings of the preset end up running different commands on one chord.
// It stops the case on a collapse, since the config error that follows
// would only repeat it.
func requireNoCollapse(t *testing.T, c ideConfig, file, mode string, meta keymeta.Meta) {
	t.Helper()
	cmdCfg, ok := c.command()
	require.True(t, ok)
	raw, err := cmdCfg.GetMap("key_bindings")
	require.NoError(t, err)
	mappings := c.commandKeyMappings()
	var offered []string
	for _, m := range keymeta.Options("linux", mode) {
		offered = append(offered, m.String())
	}
	seen := map[handler.Sequence]string{}
	by := map[handler.Sequence]string{}
	var collapsed bool
	for _, spec := range slices.Sorted(maps.Keys(raw)) {
		line := fmt.Sprint(raw[spec])
		if line == "" {
			continue
		}
		seq := meta.ApplySequence(mustParseBindingKey(t, spec))
		if prev, ok := seen[seq]; ok && prev != line {
			collapsed = true
			t.Errorf("%s binds %s to %q and %s to %q. With gui.meta_key %s both are %s, "+
				"so only %q runs.\n<meta> is whichever key the user picks for "+
				"gui.meta_key on first run (%s offers %s), and every binding must stay "+
				"distinct under each of them. Move one of the two to another chord.",
				file, by[seq], prev, spec, line, meta, sequenceSpec(seq),
				commandLine(mappings[seq]), mode, strings.Join(offered, ", "))
		}
		seen[seq], by[seq] = line, spec
	}
	if collapsed {
		t.FailNow()
	}
}

// requireLoadsClean fails on each config error the preset reports, which
// a user would get as a notification on first run.
func requireLoadsClean(t *testing.T, c ideConfig, file string, meta keymeta.Meta) {
	t.Helper()
	for _, key := range slices.Sorted(maps.Keys(c.errors)) {
		t.Errorf("%s with gui.meta_key %s reports a config error at %s, which users "+
			"would get on first run: %v", file, meta, key, c.errors[key])
	}
	if len(c.errors) > 0 {
		t.FailNow()
	}
}
