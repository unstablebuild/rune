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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/text"
)

// TestKeyBindingsRender drives rendering through representative and
// adversarial binding configurations. Every case also verifies the
// structural integrity of the rendered lists, so any content that would
// split an entry across lines or leak a pipe table fails here.
func TestKeyBindingsRender(t *testing.T) {
	tests := []struct {
		name        string
		keys        map[term.KeyComb][][]string
		seqs        map[thandler.Sequence][][]string
		aliases     map[string]text.CommandAlias
		contains    []string
		notContains []string
	}{
		{
			name: "single chords resolve manual summaries",
			keys: map[term.KeyComb][][]string{
				{Ch: 'h', Mod: term.ModMeta}: {{"windowfocus", "left"}},
				{Ch: 'n', Mod: term.ModMeta}: {{"windownew"}},
			},
			contains: []string{
				"- `<meta-h>` **windowfocus left**: Focus the window in a direction",
				"- `<meta-n>` **windownew**: Open a new window with the default split orientation",
				"## Windows",
			},
		},
		{
			name: "sequence bindings render both chords",
			seqs: map[thandler.Sequence][][]string{
				{First: term.KeyComb{Ch: 'g'}, Last: term.KeyComb{Ch: 'd'}}: {{"lsp", "definition"}},
			},
			contains: []string{
				"`gd`",
				"**lsp definition**",
				"Go to the definition of the symbol at the cursor",
				"## Language intelligence",
			},
		},
		{
			name: "args walk into subcommand manuals",
			keys: map[term.KeyComb][][]string{
				{Ch: 'n', Mod: term.ModMeta}: {{"jumptolocation", "next", "mymarks"}},
				{Ch: 'x', Mod: term.ModAlt}:  {{"lsp", "unknown-sub"}},
			},
			contains: []string{
				"- `<meta-n>` **jumptolocation next mymarks**: Jumps to the next location",
				// Unknown subcommands fall back to the parent summary.
				"- `<alt-x>` **lsp unknown-sub**: Interact with the language server",
			},
		},
		{
			// Direct lsp bindings dispatch immediately, so they act on
			// the symbol at the cursor even though the command accepts a
			// symbol argument.
			name: "direct lsp bindings describe the symbol at the cursor",
			keys: map[term.KeyComb][][]string{
				{Ch: 'd', Mod: term.ModAlt}: {{"lsp", "definition"}},
				{Ch: 't', Mod: term.ModAlt}: {{"lsp", "hover"}},
				{Ch: 'r', Mod: term.ModAlt}: {{"lsp", "references"}},
				{Ch: 'i', Mod: term.ModAlt}: {{"lsp", "implementation"}},
			},
			contains: []string{
				"- `<alt-d>` **lsp definition**: Go to the definition of the symbol at the cursor",
				"- `<alt-t>` **lsp hover**: Show documentation and types for the symbol at the cursor",
				"- `<alt-r>` **lsp references**: Find references to the symbol at the cursor",
				"- `<alt-i>` **lsp implementation**: Find implementations of the symbol at the cursor",
			},
		},
		{
			name: "jumptoast macros describe their syntax capture",
			keys: map[term.KeyComb][][]string{
				{Ch: 'f', Mod: term.ModAlt}: {{
					"echo",
					"{prompt}jumptoast<space>locals.scm<space>" +
						"local.definition.method|local.definition.function<space>",
				}},
				{Ch: 'v', Mod: term.ModAlt}: {{
					"echo",
					"{prompt}jumptoast<space>locals.scm<space>local.definition.var<space>",
				}},
				{Ch: 's', Mod: term.ModAlt}: {{
					"echo",
					"{prompt}jumptoast<space>locals.scm<space>local.definition.type<space>",
				}},
			},
			contains: []string{
				"Jump to a function in the current file",
				"Jump to a variable in the current file",
				"Jump to a type in the current file",
			},
		},
		{
			// The fuzzy-search extension binds searchfunc/searchvar/
			// searchtype as aliases over searchast prefill macros.
			name: "searchast alias macros describe their workspace search",
			keys: map[term.KeyComb][][]string{
				{Ch: 'f', Mod: term.ModAltShift}: {{"searchfunc"}},
				{Ch: 'v', Mod: term.ModAltShift}: {{"searchvar"}},
				{Ch: 's', Mod: term.ModAltShift}: {{"searchtype"}},
			},
			aliases: map[string]text.CommandAlias{
				"searchfunc": {Name: "searchfunc", Commands: []string{
					"echo {prompt}searchast<space>locals.scm<space>" +
						"local.definition.method|local.definition.function<enter>",
				}},
				"searchvar": {Name: "searchvar", Commands: []string{
					"echo {prompt}searchast<space>locals.scm<space>local.definition.var<enter>",
				}},
				"searchtype": {Name: "searchtype", Commands: []string{
					"echo {prompt}searchast<space>locals.scm<space>local.definition.type<enter>",
				}},
			},
			contains: []string{
				"- `<alt-shift-f>` **searchfunc**: Fuzzy search a function in the workspace",
				"- `<alt-shift-v>` **searchvar**: Fuzzy search a variable in the workspace",
				"- `<alt-shift-s>` **searchtype**: Fuzzy search a type in the workspace",
				"## Code navigation",
			},
		},
		{
			// Without an override, an alias resolves through its target
			// command's manual for the description while the bound name
			// still selects the category.
			name: "aliases resolve through the target manual",
			keys: map[term.KeyComb][][]string{
				{Ch: 'j', Mod: term.ModAlt}: {{"gomoveup"}},
			},
			aliases: map[string]text.CommandAlias{
				"gomoveup": {
					Name:     "gomoveup",
					Commands: []string{"jumptolocation next marks"},
				},
			},
			contains: []string{
				"- `<alt-j>` **gomoveup**: Jumps to the next location",
				// The bound alias name, not its expansion, selects the
				// category, so this unmatched alias lands in Other.
				"## Other",
			},
		},
		{
			// Overrides for alias bindings match the bound name, not the
			// expanded target, so the config alias gets a purpose-built
			// description rather than the generic jumptolocation summary.
			name: "override alias bindings by their bound name",
			keys: map[term.KeyComb][][]string{
				{Ch: 'j', Mod: term.ModAlt}:      {{"lspnextdiagnostic"}},
				{Ch: 'k', Mod: term.ModAlt}:      {{"lspprevdiagnostic"}},
				{Ch: 'j', Mod: term.ModAltShift}: {{"gitnextchange"}},
				{Ch: 'k', Mod: term.ModAltShift}: {{"gitprevchange"}},
			},
			aliases: map[string]text.CommandAlias{
				"lspnextdiagnostic": {Name: "lspnextdiagnostic", Commands: []string{"jumptolocation next lsp-diagnostics"}},
				"lspprevdiagnostic": {Name: "lspprevdiagnostic", Commands: []string{"jumptolocation prev lsp-diagnostics"}},
				"gitnextchange":     {Name: "gitnextchange", Commands: []string{"jumptolocation next git-changes"}},
				"gitprevchange":     {Name: "gitprevchange", Commands: []string{"jumptolocation prev git-changes"}},
			},
			contains: []string{
				"- `<alt-j>` **lspnextdiagnostic**: Jump to the next LSP diagnostic in the file",
				"- `<alt-k>` **lspprevdiagnostic**: Jump to the previous LSP diagnostic in the file",
				"- `<alt-shift-j>` **gitnextchange**: Jump to the next git change in the file",
				"- `<alt-shift-k>` **gitprevchange**: Jump to the previous git change in the file",
			},
			notContains: []string{"Jumps to the next location", "Jumps to the previous location"},
		},
		{
			name: "direct overrides for cursor history and workspace search",
			keys: map[term.KeyComb][][]string{
				{Ch: 'o', Mod: term.ModCtrl}:      {{"cursorhistory", "prev"}},
				{Ch: 'i', Mod: term.ModCtrl}:      {{"cursorhistory", "next"}},
				{Ch: 'f', Mod: term.ModShiftMeta}: {{"searchtext"}},
				{Ch: 'p', Mod: term.ModMeta}:      {{"searchfile"}},
			},
			contains: []string{
				"- `<ctrl-i>` **cursorhistory next**: " +
					"Jump forward to a more recently visited file or cursor position",
				"- `<ctrl-o>` **cursorhistory prev**: " +
					"Jump back to a previously visited file or cursor position",
				"- `<shift-meta-f>` **searchtext**: Fuzzy search content across the workspace",
				"- `<meta-p>` **searchfile**: Fuzzy search a file by name across the workspace",
			},
			notContains: []string{"Jump to the next cursor position", "Jump to the previous cursor position"},
		},
		{
			name: "aliases targeting prefill macros document the prefilled command",
			keys: map[term.KeyComb][][]string{
				{Ch: 't', Mod: term.ModMeta}: {{"tabnew"}},
				{Ch: '`', Mod: term.ModAlt}:  {{"tabsearch"}},
			},
			aliases: map[string]text.CommandAlias{
				"tabnew":    {Name: "tabnew", Commands: []string{"echo {prompt}edit<space>"}},
				"tabsearch": {Name: "tabsearch", Commands: []string{"echo {prompt}tabfocus<space>"}},
			},
			contains: []string{
				"- `<meta-t>` **tabnew**: Open a file for editing",
				"- `` <alt-`> `` **tabsearch**: Focus the tab at the given position",
			},
			notContains: []string{"Replay the given sequence of keys"},
		},
		{
			// Prefill lsp macros stage the command for a typed symbol
			// name, so they get a "by symbol name" override rather than
			// the direct binding's "at the cursor" phrasing, and the raw
			// echo macro must never leak into the description.
			name: "prefill lsp macros describe searching by symbol name",
			keys: map[term.KeyComb][][]string{
				{Ch: 'd', Mod: term.ModAltShift}: {{"echo", "{prompt}lsp<space>definition<space>"}},
				{Ch: 'i', Mod: term.ModAltShift}: {{"echo", "{prompt}lsp<space>implementation<space>"}},
				{Ch: 'r', Mod: term.ModAltShift}: {{"echo", "{prompt}lsp<space>references<space>"}},
				{Ch: 't', Mod: term.ModAltShift}: {{"echo", "{prompt}lsp<space>hover<space>"}},
			},
			contains: []string{
				boldCmd("echo {prompt}lsp<space>definition<space>") +
					": Go to a definition by symbol name",
				"Find implementations by symbol name",
				"Find references by symbol name",
				"Show documentation and types by symbol name",
			},
			// The raw macro must render verbatim: no backslashes and the
			// <space> tokens intact (not dropped as raw HTML).
			notContains: []string{
				"at the cursor", "if no symbol argument is passed", `\<space>`,
			},
		},
		{
			name: "unknown commands render an empty action",
			keys: map[term.KeyComb][][]string{
				{Ch: 'j', Mod: term.ModMeta}: {{"echo", "{prompt}jumptoast", "local.definition.func"}},
			},
			contains: []string{
				"- `<meta-j>` **echo {prompt}jumptoast local.definition.func**",
			},
		},
		{
			name: "numbered families compress into a placeholder row",
			keys: digitFamilyBindings(),
			contains: []string{
				"- `<alt-X>` **tabfocus X**: Focus the tab at the given position (X = 1-9)",
			},
			notContains: []string{"`tabfocus 1`", "`tabfocus 9`"},
		},
		{
			name: "mark sequences compress with backtick-safe keys",
			seqs: markJumpSequences(),
			contains: []string{
				"- `` `X `` **jumptolocation next X**: Jumps to the next location (X = a-z)",
				"## Marks and locations",
			},
			notContains: []string{"`jumptolocation next a`"},
		},
		{
			name: "small families stay explicit",
			keys: map[term.KeyComb][][]string{
				{Ch: '1', Mod: term.ModAlt}: {{"tabfocus", "1"}},
				{Ch: '2', Mod: term.ModAlt}: {{"tabfocus", "2"}},
			},
			contains: []string{
				"- `<alt-1>` **tabfocus 1**",
				"- `<alt-2>` **tabfocus 2**",
			},
			notContains: []string{"<alt-X>"},
		},
		{
			name: "multi-command macros join with semicolons",
			keys: map[term.KeyComb][][]string{
				{Ch: '+', Mod: term.ModShiftMeta}: {
					{"windowresize", "max", "width"},
					{"windowresize", "max", "height"},
				},
			},
			contains: []string{"**windowresize max width; windowresize max height**"},
		},
		{
			// List entries have no cell delimiters, so pipes need no
			// escaping and the macro reads exactly as configured.
			name: "pipes in command tokens render literally",
			keys: map[term.KeyComb][][]string{
				{Ch: 'f', Mod: term.ModAlt}: {{
					"echo",
					"{prompt}jumptoast<space>locals.scm<space>" +
						"local.definition.method|local.definition.function<space>",
				}},
			},
			contains:    []string{"local.definition.method|local.definition.function"},
			notContains: []string{`\|`},
		},
		{
			name: "multiline manual summaries collapse to one entry",
			keys: map[term.KeyComb][][]string{
				{Key: term.KeyEnter, Mod: term.ModShiftMeta}: {{"!"}},
			},
			contains: []string{
				"**!**: Open a new plugin terminal in a new floating window. " +
					"If no executable is passed, this command opens the companion terminal emulator.",
			},
		},
		{
			name: "whitespace-only summaries render an empty action",
			keys: map[term.KeyComb][][]string{
				{Ch: 'b', Mod: term.ModMeta}: {{"blank"}},
			},
			contains:    []string{"- `<meta-b>` **blank**"},
			notContains: []string{"**blank**:"},
		},
		{
			name: "pipes in manual summaries render literally",
			keys: map[term.KeyComb][][]string{
				{Ch: 'p', Mod: term.ModMeta}: {{"pickone"}},
			},
			contains: []string{"**pickone**: Choose a|b or c|d"},
		},
		{
			// The SDK normalizes a | chord to shift-backslash, so key
			// specs never carry raw pipes; the backslashes must still
			// render inside an intact entry.
			name: "pipe key chords normalize to shifted backslash",
			keys: map[term.KeyComb][][]string{
				{Ch: '|', Mod: term.ModAlt}: {{"windownew"}},
			},
			contains: []string{
				"- `<alt-shift-\\\\>` **windownew**: Open a new window with the default split orientation",
			},
		},
		{
			name: "sequence chords with pipes normalize to shifted backslash",
			seqs: map[thandler.Sequence][][]string{
				{First: term.KeyComb{Ch: '|'}, Last: term.KeyComb{Ch: 'x'}}: {{"windownew"}},
			},
			contains: []string{"- `<shift-\\\\>x` **windownew**"},
		},
		{
			// Bold command text is neutralized with zero-width breaks,
			// so backticks and pipes survive verbatim with no escaping.
			name: "backticks and pipes in command tokens render verbatim in bold",
			keys: map[term.KeyComb][][]string{
				{Ch: 's', Mod: term.ModAlt}: {{"searchast", "a|b`c"}},
			},
			contains:    []string{"- `<alt-s>` " + boldCmd("searchast a|b`c")},
			notContains: []string{`\`},
		},
		{
			name: "newlines and tabs inside command tokens collapse",
			keys: map[term.KeyComb][][]string{
				{Ch: 'z', Mod: term.ModMeta}: {{"echo", "line1\nline2", "a\tb"}},
			},
			contains: []string{"- `<meta-z>` **echo line1 line2 a b**"},
		},
		{
			name: "carriage returns inside command tokens collapse",
			keys: map[term.KeyComb][][]string{
				{Ch: 'r', Mod: term.ModMeta}: {{"echo", "a\r\nb"}},
			},
			contains: []string{"- `<meta-r>` **echo a b**"},
		},
		{
			name: "markdown metacharacters stay inside their entry",
			keys: map[term.KeyComb][][]string{
				{Ch: 'm', Mod: term.ModMeta}: {{"echo", "**bold**", "_it_", "[x](y)", "<div>"}},
			},
			contains: []string{boldCmd("echo **bold** _it_ [x](y) <div>")},
		},
		{
			name: "unicode chords render",
			keys: map[term.KeyComb][][]string{
				{Ch: 'ñ', Mod: term.ModMeta}: {{"windownew"}},
			},
			contains: []string{"Open a new window with the default split orientation"},
		},
		{
			name: "empty command token does not panic",
			keys: map[term.KeyComb][][]string{
				{Ch: '0', Mod: term.ModMeta}: {{""}},
			},
			contains: []string{"## Other"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := text.DefaultConfig()
			cfg.CommandKeyBindings = tc.keys
			cfg.CommandSequenceBindings = tc.seqs
			cfg.CommandAliases = tc.aliases

			md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
			require.NoError(t, err)

			require.True(t, strings.HasPrefix(md, "# Key bindings"),
				"the license template comment must not render into the output")
			assertKeyBindingListIntegrity(t, md)
			for _, want := range tc.contains {
				assert.Contains(t, md, want)
			}
			for _, unwanted := range tc.notContains {
				assert.NotContains(t, md, unwanted)
			}
		})
	}
}

// TestKeyBindingsSectionsOrdered verifies language intelligence renders
// first, whitelisted categories in between, and Other last, with each
// section carrying its explanatory copy.
func TestKeyBindingsSectionsOrdered(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = map[term.KeyComb][][]string{
		{Ch: 'd', Mod: term.ModAlt}:  {{"lsp", "definition"}},
		{Ch: 'h', Mod: term.ModMeta}: {{"windowfocus", "left"}},
		{Ch: 'q', Mod: term.ModMeta}: {{"quit"}},
	}
	cfg.CommandSequenceBindings = nil

	md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
	require.NoError(t, err)

	lang := strings.Index(md, "## Language intelligence")
	windows := strings.Index(md, "## Windows")
	other := strings.Index(md, "## Other")
	require.GreaterOrEqual(t, lang, 0)
	require.GreaterOrEqual(t, windows, 0)
	require.GreaterOrEqual(t, other, 0)
	assert.Less(t, lang, windows, "language intelligence must render first")
	assert.Less(t, windows, other, "Other must render last")

	assert.Contains(t, md, "powered by the language server")
	assert.Contains(t, md, "Everything else bound in your configuration.")
	assert.NotContains(t, md, "## Tabs", "empty sections must be omitted")
}

// TestKeyBindingsCodeNavigationSection verifies the code navigation
// whitelist claims its bindings, including multi-token entries such as
// "lsp diagnostics" that out-rank the broader lsp prefix, and renders
// right after language intelligence.
func TestKeyBindingsCodeNavigationSection(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = map[term.KeyComb][][]string{
		{Ch: 'e', Mod: term.ModShift}:     {{"fexplorer"}},
		{Ch: 'f', Mod: term.ModShiftMeta}: {{"searchtext"}},
		{Ch: 'f', Mod: term.ModAltShift}:  {{"searchfunc"}},
		{Ch: 'o', Mod: term.ModCtrl}:      {{"cursorhistory", "prev"}},
		{Ch: 'i', Mod: term.ModCtrl}:      {{"cursorhistory", "next"}},
		{Ch: 'e', Mod: term.ModAltShift}:  {{"lsp", "diagnostics"}},
		{Ch: 'j', Mod: term.ModAlt}:       {{"lspnextdiagnostic"}},
		{Ch: 'k', Mod: term.ModAlt}:       {{"lspprevdiagnostic"}},
		{Ch: 'g', Mod: term.ModAltShift}:  {{"gitnextchange"}},
		{Ch: 'd', Mod: term.ModAlt}:       {{"lsp", "definition"}},
	}
	cfg.CommandSequenceBindings = nil

	md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
	require.NoError(t, err)

	lang := strings.Index(md, "## Language intelligence")
	nav := strings.Index(md, "## Code navigation")
	require.GreaterOrEqual(t, lang, 0)
	require.GreaterOrEqual(t, nav, 0)
	assert.Less(t, lang, nav, "code navigation renders right after language intelligence")

	defRow := strings.Index(md, "- `<alt-d>` **lsp definition**")
	require.GreaterOrEqual(t, defRow, 0)
	assert.Less(t, defRow, nav, "lsp definition stays under language intelligence")

	for _, row := range []string{
		"- `<shift-e>` **fexplorer**: Toggle the file explorer",
		"- `<ctrl-o>` **cursorhistory prev**",
		"- `<ctrl-i>` **cursorhistory next**",
		"- `<alt-shift-e>` **lsp diagnostics**",
		"- `<alt-j>` **lspnextdiagnostic**",
		"- `<alt-k>` **lspprevdiagnostic**",
		"- `<alt-shift-g>` **gitnextchange**",
		"- `<shift-meta-f>` **searchtext**",
		"- `<alt-shift-f>` **searchfunc**",
	} {
		idx := strings.Index(md, row)
		require.GreaterOrEqual(t, idx, 0, "missing row: %s", row)
		assert.Greater(t, idx, nav, "row must render under code navigation: %s", row)
	}
}

// TestKeyBindingsEditorModeIntro verifies the intro links to the docs
// page for the configured editor mode.
func TestKeyBindingsEditorModeIntro(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = nil
	cfg.CommandSequenceBindings = nil

	for _, tc := range []struct {
		mode string
		want string
	}{
		{"vim", "https://docs.rune.build/learn/vim-editor"},
		{"helix", "https://docs.rune.build/learn/helix-editor"},
		{"standard", "https://docs.rune.build/learn/standard-editor"},
		{"emacs", "https://docs.rune.build/learn/emacs-editor"},
		{"exo", "consult its documentation for in-buffer key bindings"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			md, err := renderKeyBindings(cfg, keybindingsTestManuals(), tc.mode, "darwin")
			require.NoError(t, err)
			assert.Contains(t, md, tc.want)
		})
	}
}

// TestKeyBindingsMetaKeyHint verifies the top-of-document hint names the
// physical key that produces <meta> chords on the user's platform.
func TestKeyBindingsMetaKeyHint(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = nil
	cfg.CommandSequenceBindings = nil

	for _, tc := range []struct {
		goos string
		want string
	}{
		{"darwin", "`<meta>` is the Command key."},
		{"linux", "`<meta>` is the Windows or Super key."},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", tc.goos)
			require.NoError(t, err)
			assert.Contains(t, md, tc.want)
			hint := strings.Index(md, "`<meta>` is")
			intro := strings.Index(md, "These are your command key bindings.")
			require.GreaterOrEqual(t, hint, 0)
			require.GreaterOrEqual(t, intro, 0)
			assert.Less(t, hint, intro, "the meta hint renders at the very top")
		})
	}
}

// TestKeyBindingsMacrosSection verifies the bespoke macros section
// documents recording and playback for the configured editor mode and
// is omitted for exo, whose editor owns in-buffer keystrokes.
func TestKeyBindingsMacrosSection(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = nil
	cfg.CommandSequenceBindings = nil

	t.Run("vim", func(t *testing.T) {
		withOther := cfg
		withOther.CommandKeyBindings = map[term.KeyComb][][]string{
			{Ch: 'q', Mod: term.ModMeta}: {{"quit"}},
		}
		md, err := renderKeyBindings(withOther, keybindingsTestManuals(), "vim", "darwin")
		require.NoError(t, err)
		assertKeyBindingListIntegrity(t, md)
		assert.Contains(t, md, "## Macros")
		assert.Contains(t, md, "- `q{reg}`: Start recording into register `{reg}` (`a`-`z`; `A`-`Z` appends)")
		assert.Contains(t, md, "- `q`: Stop recording")
		assert.Contains(t, md, "- `@{reg}`: Play register `{reg}`")
		assert.Contains(t, md, "- `@@`: Replay the last played register")
		assert.Contains(t, md, "echo {register}a")
		assert.Contains(t, md, "https://docs.rune.build/learn/vim-editor#macros")

		macros := strings.Index(md, "## Macros")
		other := strings.Index(md, "## Other")
		require.GreaterOrEqual(t, other, 0)
		assert.Less(t, macros, other, "macros must render above Other")
	})

	t.Run("standard", func(t *testing.T) {
		md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "standard", "darwin")
		require.NoError(t, err)
		assertKeyBindingListIntegrity(t, md)
		assert.Contains(t, md, "## Macros")
		assert.Contains(t, md, "- `<ctrl-q>`: Start recording; press again to stop (unnamed register)")
		assert.Contains(t, md, "- `<ctrl-shift-q>`: Play the recorded macro")
		assert.Contains(t, md, "https://docs.rune.build/learn/standard-editor#macros")
	})

	t.Run("exo", func(t *testing.T) {
		md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "exo", "darwin")
		require.NoError(t, err)
		assert.NotContains(t, md, "## Macros")
	})
}

// TestKeyBindingsEmptyConfig verifies the fallback message renders when
// no bindings are configured and the title still leads the output.
func TestKeyBindingsEmptyConfig(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = nil
	cfg.CommandSequenceBindings = nil

	md, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(md, "# Key bindings"),
		"the license comment must not render into the output")
	assert.Contains(t, md, "No key bindings configured.")
}

// TestKeyBindingsDeterministic verifies identical input yields identical
// output regardless of map iteration order, including compressed
// families and sequence bindings.
func TestKeyBindingsDeterministic(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = digitFamilyBindings()
	cfg.CommandKeyBindings[term.KeyComb{Ch: 'h', Mod: term.ModMeta}] = [][]string{{"windowfocus", "left"}}
	cfg.CommandKeyBindings[term.KeyComb{Ch: 'n', Mod: term.ModMeta}] = [][]string{{"windownew"}}
	cfg.CommandSequenceBindings = markJumpSequences()

	first, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
	require.NoError(t, err)
	for range 5 {
		got, err := renderKeyBindings(cfg, keybindingsTestManuals(), "vim", "darwin")
		require.NoError(t, err)
		assert.Equal(t, first, got)
	}
}

func keybindingsTestManuals() []command.Manual {
	return []command.Manual{
		{Name: "windowfocus", Summary: "Focus the window in a direction"},
		{Name: "windownew", Summary: "Open a new window with the default split orientation"},
		{
			Name:    "lsp",
			Summary: "Interact with the language server",
			Commands: []command.Manual{
				{Name: "definition", Summary: "Go to definition of a symbol"},
				{Name: "hover", Summary: "Displays documentation for a symbol"},
			},
		},
		{Name: "tabfocus", Summary: "Focus the tab at the given position"},
		{Name: "edit", Summary: "Open a file for editing"},
		{Name: "echo", Summary: "Replay the given sequence of keys back into the event loop"},
		{Name: "fexplorer", Summary: "Toggle the file explorer"},
		{
			Name: "!",
			Summary: "Open a new plugin terminal in a new floating window. \n\n" +
				"If no executable is passed, this command opens the companion terminal emulator.",
		},
		{Name: "blank", Summary: " \n\t "},
		{Name: "pickone", Summary: "Choose a|b or c|d"},
		{
			Name:    "cursorhistory",
			Summary: "Navigate the cursor history",
			Commands: []command.Manual{
				{Name: "next", Summary: "Jump to the next cursor position"},
				{Name: "prev", Summary: "Jump to the previous cursor position"},
			},
		},
		{
			Name:    "jumptolocation",
			Summary: "Jump to the next or previous location on a list",
			Commands: []command.Manual{
				{Name: "next", Summary: "Jumps to the next location"},
				{Name: "previous", Summary: "Jumps to the previous location"},
			},
		},
	}
}

// digitFamilyBindings mirrors the autogenerated tabfocus 1-9 defaults.
func digitFamilyBindings() map[term.KeyComb][][]string {
	keys := make(map[term.KeyComb][][]string, 9)
	for ch := '1'; ch <= '9'; ch++ {
		keys[term.KeyComb{Ch: ch, Mod: term.ModAlt}] = [][]string{{"tabfocus", string(ch)}}
	}
	return keys
}

// markJumpSequences mirrors the vi a-z mark jump built-ins.
func markJumpSequences() map[thandler.Sequence][][]string {
	seqs := make(map[thandler.Sequence][][]string, 26)
	for ch := 'a'; ch <= 'z'; ch++ {
		seq := thandler.Sequence{First: term.KeyComb{Ch: '`'}, Last: term.KeyComb{Ch: ch}}
		seqs[seq] = [][]string{{"jumptolocation", "next", string(ch)}}
	}
	return seqs
}

// boldCmd renders a command line the way a list entry does, so tests can
// assert the visible text without hard-coding the zero-width breaks
// mdBold inserts.
func boldCmd(s string) string {
	return mdBold(s)
}

// assertKeyBindingListIntegrity verifies the view renders bullet lists
// rather than pipe tables and every entry leads with its key code span
// on a single line.
func assertKeyBindingListIntegrity(t *testing.T, md string) {
	t.Helper()
	for line := range strings.SplitSeq(md, "\n") {
		assert.False(t, strings.HasPrefix(line, "|"),
			"no pipe tables expected in the key bindings view: %q", line)
		if strings.HasPrefix(line, "- ") {
			assert.True(t, strings.HasPrefix(line, "- `"),
				"entries must lead with a key code span: %q", line)
		}
	}
}
