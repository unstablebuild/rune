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

//go:build e2e

package vte

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ernestrc/sensible/find"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vte/vtetest"
)

func TestZshEdgeCases(t *testing.T) {
	// only run this if zsh is present in system running test harness
	zshPath, err := find.Executable("zsh")
	if err != nil {
		t.SkipNow()
	}

	// zsh started as terminal.shell loads Rune's dotfiles, which source
	// the ones in $HOME
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`
setopt COMBINING_CHARS
PS1='$ '
autoload -U compinit && compinit -u
zstyle ':completion:*' menu select
# Deterministic completion target for RUNE-193: xfoo has a fixed
# completion list so the rendered menu does not depend on which
# command binaries the host has installed.
xfoo() { :; }
_xfoo() { compadd -- alpha bravo charlie delta }
compdef _xfoo xfoo
`), 0o644))
	t.Setenv("HOME", home)

	t.Run("insert mode edit wrap-around", func(t *testing.T) {
		cases := []vtetest.Case{
			{"echo blaaaaaa<0Cecho blaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa▐   
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceShell(t, cfg, defaultWaitForIdleVte, zshPath, cases)
	})

	// Once the wrapped line scrolls the screen, or there is scrollback above
	// the prompt, the vi cursor must keep inserting where the user is typing
	// instead of drifting to another row.
	for _, tc := range []struct {
		name string
		vtetest.Case
	}{
		{"insert mid-line on the bottom row wraps and scrolls", vtetest.Case{
			// 0 lands on the prompt before the shell answered, a WORD of
			// its own
			InputSequence: "seq 30>echo aa bb cc dd<03WaXXXX",
			Expected: `23                  
24                  
25                  
26                  
27                  
28                  
29                  
30                  
$ echo aa bXXXX▐ cc 
dd                  `}},
		{"insert at end of line on the bottom row wraps and scrolls", vtetest.Case{
			InputSequence: "seq 30>echo<0Cecho aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Expected: `23                  
24                  
25                  
26                  
27                  
28                  
29                  
30                  
$ echo aaaaaaaaaaaaa
aaaaaaaaaaaaaaaaa▐  `}},
		// '$' clears the screen, which leaves the prompt on the top row
		// with scrollback above it
		{"insert at end of line with scrollback above the prompt wraps", vtetest.Case{
			InputSequence: "seq 30>$echo<0Cecho aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Expected: `$ echo aaaaaaaaaaaaa
aaaaaaaaaaaaaaaaa▐  
                    
                    
                    
                    
                    
                    
                    
                    `}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Modal = true
			testSequenceShell(t, cfg, defaultWaitForIdleVte, zshPath, []vtetest.Case{tc.Case})
		})
	}

	// RUNE-193: tab-completion below the prompt confused lastPromptLine
	// so that subsequent vi edits on the real prompt row rang the bell
	// instead of mutating the buffer. With menu-select enabled, zsh
	// renders the completion list on rows below the prompt; after <esc>
	// dismisses the menu, leftover prompt-like rows can still sit below
	// the cursor row and trick the heuristic.
	t.Run("tab completion then vi delete-to-end", func(t *testing.T) {
		cases := []vtetest.Case{
			// type `xfoo `, press <tab> to render the (fixed)
			// zsh completion menu below the prompt, then
			// <esc>0C to delete the command and re-enter
			// insert mode. The prompt row must show an empty
			// `$ ` with the cursor right after it — proving
			// the `C` was not bell-rejected by lastPromptLine
			// misclassifying the real prompt row. zsh leaves
			// the completion menu drawn below the prompt; the
			// visual cleanup of those rows is the shell's
			// responsibility, so the test only asserts the
			// prompt row, leaving menu rows un-checked.
			{"xfoo ✌<0C",
				`$ ▐                 
alpha    charlie    
bravo    delta      
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceShell(t, cfg, defaultWaitForIdleVte, zshPath, cases)
	})

	// RUNE-193 regression guard: when the user enters modal mode
	// while the shell cursor sits on a wrapped continuation row,
	// lastPromptLine must still include the original prompt row
	// in the block so vi motions/edits that land on the upper row
	// succeed instead of being bell-rejected. Type `echo` plus
	// enough `a`s to wrap onto row 1, then <esc>k (vi up onto the
	// prompt row) and insert `X`. The `X` must land on row 0,
	// proving the prompt row is part of the editable block.
	t.Run("modal entered on wrapped continuation row", func(t *testing.T) {
		cases := []vtetest.Case{
			{"echo aaaaaaaaaaaaaaaaaa<kiX",
				`$ ecX▐o aaaaaaaaaaaa
aaaaaa              
                    
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceShell(t, cfg, defaultWaitForIdleVte, zshPath, cases)
	})
}

func TestBashViModeEdgeCases(t *testing.T) {
	bashPath, err := find.Executable("bash")
	if err != nil {
		t.Skip("bash not found in PATH")
	}
	for _, rc := range []struct {
		name    string
		bashrc  string
		inputrc string
	}{
		{name: "set -o vi in bashrc", bashrc: "set -o vi\n"},
		{name: "editing-mode vi in inputrc", inputrc: "set editing-mode vi\n"},
	} {
		for _, sh := range []struct {
			name           string
			loginShell     string
			commandAndArgs []string
		}{
			{"SHELL", bashPath, nil},
			// SHELL is not bash so that only terminal.shell can start it
			{"terminal.shell", "/bin/sh", []string{bashPath}},
		} {
			t.Run(rc.name+"/"+sh.name, func(t *testing.T) {
				home := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(home, ".bash_profile"),
					[]byte(". ~/.bashrc\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"),
					[]byte(rc.bashrc+"PS1='$ '\n"), 0o644))
				if rc.inputrc != "" {
					require.NoError(t, os.WriteFile(filepath.Join(home, ".inputrc"),
						[]byte(rc.inputrc), 0o644))
				}
				t.Setenv("HOME", home)
				t.Setenv("SHELL", sh.loginShell)
				// an inherited INPUTRC would mask a missing export
				t.Setenv("INPUTRC", "")
				// bash 3.2 as shipped on macOS prints a zsh-migration banner on
				// every interactive startup, which would scroll the screen away.
				t.Setenv("BASH_SILENCE_DEPRECATION_WARNING", "1")

				cases := []vtetest.Case{
					{"echo blaaa<0Cecho hi", `$ echo hi▐          
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
				}
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceCommand(t, cfg, defaultWaitForIdleVte, sh.commandAndArgs, cases)
			})
		}
	}
}

func TestZshViModeEdgeCases(t *testing.T) {
	zshPath, err := find.Executable("zsh")
	if err != nil {
		t.Skip("zsh not found in PATH")
	}
	for _, sh := range []struct {
		name           string
		loginShell     string
		commandAndArgs []string
	}{
		{"SHELL", zshPath, nil},
		// SHELL is not zsh so that only terminal.shell can start it
		{"terminal.shell", "/bin/sh", []string{zshPath}},
	} {
		t.Run(sh.name, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(home, ".zshrc"),
				[]byte("bindkey -v\nPS1='$ '\n"), 0o644))
			t.Setenv("HOME", home)
			t.Setenv("SHELL", sh.loginShell)
			// an inherited ZDOTDIR would mask a missing export
			t.Setenv("ZDOTDIR", "")

			cases := []vtetest.Case{
				{"echo blaaa<0Cecho hi", `$ echo hi▐          
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
			}
			cfg := DefaultConfig()
			cfg.Modal = true
			testSequenceCommand(t, cfg, defaultWaitForIdleVte, sh.commandAndArgs, cases)
		})
	}
}

func TestFishEdgeCases(t *testing.T) {
	fishPath, err := find.Executable("fish")
	if err != nil {
		t.SkipNow()
	}
	for _, keys := range []struct{ name, configFish string }{
		{"default key bindings", ""},
		{"vi key bindings", "fish_vi_key_bindings\n"},
	} {
		for _, sh := range []struct {
			name           string
			loginShell     string
			commandAndArgs []string
		}{
			{"SHELL", fishPath, nil},
			// SHELL is not fish so that only terminal.shell can start it
			{"terminal.shell", "/bin/sh", []string{fishPath}},
		} {
			t.Run(keys.name+"/"+sh.name, func(t *testing.T) {
				home := t.TempDir()
				configDir := filepath.Join(home, ".config")
				require.NoError(t, os.MkdirAll(filepath.Join(configDir, "fish"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(configDir, "fish", "config.fish"),
					[]byte("set -g fish_greeting\nfunction fish_prompt; printf '$ '; end\n"+
						"function fish_mode_prompt; end\n"+keys.configFish), 0o644))
				t.Setenv("HOME", home)
				t.Setenv("XDG_CONFIG_HOME", configDir)
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
				t.Setenv("SHELL", sh.loginShell)

				cases := []vtetest.Case{
					{"echo blaaa<0Cecho hi", `$ echo hi▐          
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
				}
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceCommand(t, cfg, defaultWaitForIdleVte, sh.commandAndArgs, cases)
			})
		}
	}
}
