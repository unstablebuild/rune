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
	"context"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/ernestrc/sensible/find"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/term/vte/vtescreen"
	"unstable.build/rune/internal/term/vte/vtetest"
	"unstable.build/rune/internal/text"
)

func TestHandlerViIntegration(t *testing.T) {
	t.Parallel()
	t.Run("search", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo bla></bla",
				`$ echo bla          
bla                 
$                   
                    
                    
                    
                    
                    
                    
/bla▐               `},
			{">",
				`$ echo ▐la          
bla                 
$                   
                    
                    
                    
                    
                    
                    
     searching 'bla'`},
			{"n",
				`$ echo bla          
▐la                 
$                   
                    
                    
                    
                    
                    
                    
     searching 'bla'`},
			{"/.bla>",
				`$ echo bla          
▐la                 
$                   
                    
                    
                    
                    
                    
                    
    searching '.bla'`},
			{"/ibla>",
				`$ echo bla          
▐la                 
$                   
                    
                    
                    
                    
                    
                    
    searching 'ibla'`},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("edit/movement", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo bla>",
				`$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
			{"<", // cursor should stay the same entering vi mode
				`$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
			{"k0veyj\\$iecho <p", // copy and paste
				`$ echo bla          
bla                 
$ echo bl▐          
                    
                    
                    
                    
                    
                    
                    `},
			{"bved",
				`$ echo bla          
bla                 
$ echo▐             
                    
                    
                    
                    
                    
                    
                    `},
			{"0Cecho bla12345678901234567890", // delete line and insert wrap around
				`$ echo bla          
bla                 
$ echo bla1234567890
1234567890▐         
                    
                    
                    
                    
                    
                    `},
			{"<hhhrolll", // replace
				`$ echo bla          
bla                 
$ echo bla1234567890
123456o89▐          
                    
                    
                    
                    
                    
                    `},
			{"u", // undo doesn' panic
				`$ echo bla          
bla                 
$ echo bla1234567890
123456o89▐          
                    
                    
                    
                    
                    
                    `},
			{"aaaaaaaaaaaaaaaaaaaaaaaaa",
				`$ echo bla          
bla                 
$ echo bla1234567890
123456o890aaaaaaaaaa
aaaaaaaaaaaaaa▐     
                    
                    
                    
                    
                    `},
			{"<rXa",
				`$ echo bla          
bla                 
$ echo bla1234567890
123456o890aaaaaaaaaa
aaaaaaaaaaaaaX▐     
                    
                    
                    
                    
                    `},
			{"<>>>", // ensure that attr bar doesn't occlude last line in shell mode
				`bla                 
$ echo bla1234567890
123456o890aaaaaaaaaa
aaaaaaaaaaaaaX      
bla1234567890123456o
890aaaaaaaaaaaaaaaaa
aaaaaaX             
$                   
$                   
$ ▐                 `},
			{"<", // ensure that attr bar doesn't occlude last line in vi mode
				`bla                 
$ echo bla1234567890
123456o890aaaaaaaaaa
aaaaaaaaaaaaaX      
bla1234567890123456o
890aaaaaaaaaaaaaaaaa
aaaaaaX             
$                   
$                   
$ ▐                 `},
			{"iclear>",
				`$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
			{"echo bla", // clear is respected on shell insert
				`$ echo bla▐         
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
			{"<hi", // clear is respected when switching in/out of vi
				`$ echo b▐a          
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
			{">", // enter on vi mode, bypasses vi
				`$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
			{"echo \"<k0llvk0yG0lllllpjla\"", // multiline paste
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"▐                 
                    
                    
                    
                    
                    
                    `},
			{">", // execute paste
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ ▐                 
                    
                    
                    
                    `},
			{"1Z<0\\$aX<0", // a after $
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ ▐ZX               
                    
                    
                    
                    `},
			{"⬆⬆", // position after scroll up through history
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ echo bl▐          
                    
                    
                    
                    `},
			{"\\$", // $ after scroll through history
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ echo bl▐          
                    
                    
                    
                    `},
			{"kkkk#a", // ctrl-c exits vi mode, no matter where cursor is
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ echo blaa▐        
                    
                    
                    
                    `},
			{"<0D\\$", // $ end of line if only prompt stays at prompt
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ ▐                 
                    
                    
                    
                    `},
			{"iecho '.i\\$'>", // is able to use special characters in shell mode
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ echo '.i$'        
.i$                 
$ ▐                 
                    
                    `},
			{"<⬇⬇⬇⬇", // position after scroll down through history to the start
				`$ echo bla          
bla                 
$ echo "$ echo blabl
a"                  
$ echo blabla       
$ echo '.i$'        
.i$                 
$ ▐                 
                    
                    `},
		}

		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("dollar key with multiline prompt line", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo blaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa▐   
                    
                    
                    
                    
                    
                    
                    `},
			{"<0",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
▐aaaaaaaaaaaaaaa    
                    
                    
                    
                    
                    
                    
                    `},
			{"\\$\\$",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaa▐    
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("multiline go up before prompt start", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo blaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa▐   
                    
                    
                    
                    
                    
                    
                    `},
			{"<0kk",
				`$ ▐cho blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa    
                    
                    
                    
                    
                    
                    
                    `},
			{"\\$\\$0",
				`$ ▐cho blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa    
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("multiline paste", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo blaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa>",
				`$ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa    
blaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaa           
$ ▐                 
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("last line wrap around", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{">>>>>>>>>>",
				`$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$ ▐                 `},
			{"echo<0Cecho aaaaaaaaaaaaabcde",
				`$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$ echo aaaaaaaaaaaaa
bcde▐               `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("tab", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo<0Cecho \ta",
				`$ echo  ▐           
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	t.Run("copy/paste", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"printf 'abc\\\\x00\\\\n'><k0velllyjj0iecho '<p",
				`$ printf 'abc\x00\n'
abc                 
$ echo 'ab▐         
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		clip := clipboard.NewInMemory()
		cfg.Clipboard = clip
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)

		// assert data that leaks outside of handler via clipboard
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "abc", data.Text)
	})

	t.Run("go to start of buffer, go to end of buffer", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo a>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>echo<",
				`$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$ ech▐              `},
			{"gg",
				`$ ech▐ a            
a                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   `},
			{"G",
				`$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$                   
$ ech▐              `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})
}

func TestZshEdgeCases(t *testing.T) {
	t.Parallel()
	// only run this if zsh is present in system running test harness
	zshPath, err := find.Executable("zsh")
	if err != nil {
		t.SkipNow()
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)

	f, err := os.Create(path.Join(tempDir, ".zshrc"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	})

	_, err = f.Write([]byte(`
bindkey '^a' beginning-of-line
bindkey '^g' beep
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
`))
	require.NoError(t, err)

	os.Setenv("ZDOTDIR", tempDir)

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

func TestViEditUnit(t *testing.T) {
	t.Parallel()
	t.Run("screen context bypasses Edit", func(t *testing.T) {
		t.Parallel()
		comp := newTestParentComponent("a\nb", term.Coordinates{})
		var vi viHandler
		vi.doInit(comp, DefaultConfig())

		ctx := vtescreen.NewContext(context.Background())
		vi.Edit(ctx, term.Coordinates{}, term.Coordinates{Y: 1, X: 1}, "b\nb")
		assert.Equal(t, "b\nb", comp.scroll.Buffer().String())
	})

	t.Run("parser edits keep predictive cursor valid for modal commands", func(t *testing.T) {
		operations := []struct {
			name       string
			content    string
			cursor     term.Coordinates
			start      term.Coordinates
			end        term.Coordinates
			insert     string
			want       string
			wantCursor term.Coordinates
		}{
			{
				name: "line insert above", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 4}, insert: "\n", want: "\n0\n1\n2\n3\n4",
				wantCursor: term.Coordinates{Y: 5},
			},
			{
				name: "same-line insert before", content: "abcde",
				cursor: term.Coordinates{X: 4}, start: term.Coordinates{X: 1},
				end: term.Coordinates{X: 1}, insert: "X", want: "aXbcde",
				wantCursor: term.Coordinates{X: 5},
			},
			{
				name: "same-line delete before", content: "abcde",
				cursor: term.Coordinates{X: 4}, start: term.Coordinates{X: 1},
				end: term.Coordinates{X: 3}, want: "ade",
				wantCursor: term.Coordinates{X: 2},
			},
			{
				name: "line delete above", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 4}, end: term.Coordinates{Y: 2},
				want: "2\n3\n4", wantCursor: term.Coordinates{Y: 2},
			},
			{
				name: "line replacement above", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 4}, end: term.Coordinates{Y: 2},
				insert: "X\nY", want: "X\nY2\n3\n4", wantCursor: term.Coordinates{Y: 3},
			},
			{
				name: "delete through cursor", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 2}, start: term.Coordinates{Y: 1},
				end: term.Coordinates{Y: 3}, want: "0\n3\n4",
				wantCursor: term.Coordinates{Y: 1},
			},
			{
				name: "line insert below", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 1}, start: term.Coordinates{Y: 4, X: 1},
				end: term.Coordinates{Y: 4, X: 1}, insert: "\n5", want: "0\n1\n2\n3\n4\n5",
				wantCursor: term.Coordinates{Y: 1},
			},
			{
				name: "line delete below", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 1}, start: term.Coordinates{Y: 3},
				end: term.Coordinates{Y: 4}, want: "0\n1\n2\n4",
				wantCursor: term.Coordinates{Y: 1},
			},
			{
				name: "delete whole buffer", content: "0\n1\n2\n3\n4",
				cursor: term.Coordinates{Y: 2}, end: term.Coordinates{Y: 4, X: 1},
				want: "", wantCursor: term.Coordinates{},
			},
		}
		commands := []struct {
			name string
			ev   term.Event
		}{
			{name: "open below", ev: term.Event{Type: term.EventKey, Ch: 'o'}},
			{name: "open above", ev: term.Event{Type: term.EventKey, Ch: 'O'}},
			{name: "append", ev: term.Event{Type: term.EventKey, Ch: 'a'}},
			{name: "delete character", ev: term.Event{Type: term.EventKey, Ch: 'x'}},
			{name: "move up", ev: term.Event{Type: term.EventKey, Ch: 'k'}},
			{name: "move down", ev: term.Event{Type: term.EventKey, Ch: 'j'}},
			{name: "end of line", ev: term.Event{Type: term.EventKey, Ch: '$'}},
			{name: "join lines", ev: term.Event{Type: term.EventKey, Ch: 'J'}},
		}

		for _, operation := range operations {
			for _, command := range commands {
				t.Run(operation.name+"/"+command.name, func(t *testing.T) {
					comp := newTestParentComponent("", term.Coordinates{})
					var vi viHandler
					vi.doInit(comp, DefaultConfig())
					vi.Resize(10, 3)

					ctx := vtescreen.NewContext(context.Background())
					vi.Edit(ctx, term.Coordinates{}, term.Coordinates{}, operation.content)
					vi.setCursorAtScroll(operation.cursor)
					require.Equal(t, operation.cursor, vi.copy.vi.CursorAtScroll())

					vi.Edit(ctx, operation.start, operation.end, operation.insert)

					assert.Equal(t, operation.want, vi.copy.vi.CellView().String())
					at := vi.copy.vi.CursorAtScroll()
					assert.Equal(t, operation.wantCursor, at)
					require.Greater(t, vi.copy.vi.CellView().Rows(), 0)
					assert.GreaterOrEqual(t, at.Y, 0)
					assert.Less(t, at.Y, vi.copy.vi.CellView().Rows())
					if at.Y >= 0 && at.Y < vi.copy.vi.CellView().Rows() {
						assert.GreaterOrEqual(t, at.X, 0)
						assert.LessOrEqual(t, at.X, vi.copy.vi.CellView().Columns(at.Y))
					}
					assert.NotPanics(t, func() {
						_, _ = vi.copy.vi.Handle(command.ev)
					})
				})
			}
		}
	})

	// Mirrors the production restore path: vte.Component.RestoreFromSnapshot
	// rewrites primary-buffer cells in place (preserving *cell.Buffer
	// identity) and the cursor moves. Handler.RestoreFromSnapshot then
	// calls vi.setCursorAtScroll(newCursor). The viHandler is NOT
	// re-initialised; doing so on top of itself produced a
	// self-referential editor chain (v.sync.editor == v) which made
	// v.Edit recurse forever and pegged the event loop after a
	// remote-workspace snapshot restore.
	t.Run("restore refreshes editable buffer in place", func(t *testing.T) {
		t.Parallel()
		comp := newTestParentComponent("$ stale ", term.Coordinates{X: 2})
		var vi viHandler
		cfg := DefaultConfig()
		var actualBellsRung int
		cfg.RingBell = func() {
			actualBellsRung++
		}
		vi.doInit(comp, cfg)
		vi.remote = newTestRemote(comp.scroll, comp.cursor)
		vi.Resize(18, 18)

		// Mimic what vte.Component.RestoreFromSnapshot does: rewrite
		// the existing primary buffer's cells in place and move the
		// cursor. *cell.Buffer / *component.Scroll identity is
		// preserved.
		comp.scroll.Buffer().ResetCells(term.StringToCells("$ restored "))
		comp.cursor = term.Coordinates{X: 11}
		vi.setCursorAtScroll(comp.cursor)
		vi.remote = newTestRemote(comp.scroll, comp.cursor)

		from, to, old := vi.Edit(context.Background(),
			comp.cursor, comp.cursor, "X")

		assert.Equal(t, "$ restored X", comp.scroll.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 11}, from)
		assert.Equal(t, term.Coordinates{X: 12}, to)
		assert.Equal(t, "", old)
		assert.Equal(t, 0, actualBellsRung)
	})

	// Pins the invariant that bulk-replacing a cell.Buffer's contents
	// (as AltBuffer.restore does for snapshot restore) preserves both
	// *cell.Buffer and *rawCells identity. Without this invariant,
	// any cached editor pointer (e.g. v.sync.editor, captured by
	// scroll.Buffer().WithEditor in newSyncState) would silently go
	// stale: v.sync.editor.Edit would write into a dead rawCells,
	// AltBuffer.WriteAt would see CellAt(at) == nil forever, and the
	// recursion InsertAt ↔ WriteAt would peg a CPU core during
	// terminal-session restore.
	t.Run("restore preserves cached editor target", func(t *testing.T) {
		t.Parallel()
		comp := newTestParentComponent("hi", term.Coordinates{})
		var vi viHandler
		vi.doInit(comp, DefaultConfig())

		// Run the assertions on a watchdogged goroutine so a
		// regression that causes WriteAt ↔ InsertAt to spin (the
		// original symptom) fails the test fast instead of hanging
		// the suite.
		done := make(chan struct{})
		go func() {
			defer close(done)

			buf := comp.scroll.Buffer()
			bufPtr := buf
			require.NotEmpty(t, buf.RawCells())

			// Snapshot the current editor that v.sync.editor points
			// at; ResetCells must not invalidate it.
			cachedEditor := vi.sync.editor

			// Mutate buffer contents through the same path
			// AltBuffer.restore uses.
			newCells := term.StringToCells("restored")
			buf.ResetCells(newCells)

			require.Equal(t, bufPtr, comp.scroll.Buffer(),
				"*cell.Buffer identity must survive ResetCells; "+
					"otherwise viHandler.v.sync.* and "+
					"component.Scroll.buf go stale")
			require.Equal(t, "restored", buf.String(),
				"buffer contents must reflect the ResetCells payload")

			// Drive an Edit through the cached editor — exactly
			// what the vte parser does via viHandler.Edit on a
			// screen-context write. If ResetCells had replaced
			// the *rawCells, cachedEditor would still point at
			// the dead instance and this Edit would either be a
			// no-op or panic. Either way, the new contents
			// wouldn't appear via buf.String().
			_, _, _ = cachedEditor.Edit(
				vtescreen.NewContext(context.Background()),
				term.Coordinates{Y: 0, X: 8},
				term.Coordinates{Y: 0, X: 8},
				"!")
			assert.Equal(t, "restored!", buf.String(),
				"cached v.sync.editor must continue to mutate "+
					"the current rawCells after ResetCells")
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("restore-preserves-cached-editor watchdog tripped; " +
				"likely a regression of the AltBuffer.restore identity " +
				"invariant causing WriteAt ↔ InsertAt to recurse")
		}
	})

	// Pins the invariant that drove the original goroutine freeze:
	// the viHandler created at vte.Handler.Init time installs itself
	// as the primary buffer's editor via WithEditor(v); a screen-context
	// Edit on v must NOT recurse into v.sync.editor.Edit because
	// v.sync.editor must be the previous (real) cell editor, not v
	// itself. Calling vi.doInit a second time onto the same buffer
	// used to break this invariant; the snapshot restore path now
	// keeps the viHandler in place so the chain stays correct.
	t.Run("sync editor is not self-referential after init", func(t *testing.T) {
		t.Parallel()
		comp := newTestParentComponent("hi", term.Coordinates{})
		var v viHandler
		v.doInit(comp, DefaultConfig())

		// Drive a screen-context Edit on a watchdogged goroutine so
		// a regression that re-introduces v.sync.editor == v fails
		// fast instead of hanging the suite.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = v.Edit(
				vtescreen.NewContext(context.Background()),
				term.Coordinates{Y: 0, X: 2},
				term.Coordinates{Y: 0, X: 2},
				"!")
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("viHandler.Edit recursed indefinitely; " +
				"v.sync.editor likely points back at v")
		}
	})

	suite := []struct {
		description    string
		initialContent string
		// it is assumed that when Edit is invoked the component cursor
		// is at the start of the prompt.
		promptStart term.Coordinates
		start       term.Coordinates
		end         term.Coordinates
		str         string

		expectedRingBell bool
		expectedContent  string
		expectedFrom     term.Coordinates
		expectedTo       term.Coordinates
		expectedOld      string
	}{
		{
			description:      "(invalid) zero Edit",
			initialContent:   "",
			promptStart:      term.Coordinates{},
			start:            term.Coordinates{},
			end:              term.Coordinates{},
			str:              "",
			expectedRingBell: true,
			expectedContent:  "",
		},
		{
			description: "insert within last prompt line, exactly after prompt",
			initialContent: `
~/src/blue master
$ 
`,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 2},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "echo",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ echo
`,
		},
		{
			description: "insert within last prompt line, before prompt is shifted",
			initialContent: `
~/src/blue master
$ 
`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 2},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "$ echo",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ echo
`,
		},
		{
			description: "delete until end of prompt line starting at prompt",
			initialContent: `
~/src/blue master
$ echo
`,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 7},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "echo",
			expectedContent: `
~/src/blue master
$ 
`,
		},
		{
			description: "delete until end of prompt line starting before prompt is trimmed to prompt start",
			initialContent: `
~/src/blue master
$ echo
`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 2, X: 7},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "echo",
			expectedContent: `
~/src/blue master
$ 
`,
		},
		{
			description: "delete prompt line until next line, (vi's dd), last line",
			initialContent: `
~/src/blue master
$ echo`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 3},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "echo",
			expectedContent: `
~/src/blue master
$ `,
		},
		{
			description: "delete prompt line until next line, (vi's dd), not last line",
			initialContent: `
~/src/blue master
$ echo
`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 3},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 6},
			expectedOld:      "echo",
			expectedContent: `
~/src/blue master
$ 
`,
		},
		{
			description: "delete prompt multiline, no last line",
			initialContent: `
~/src/blue master
$ echo blaaaaaaaaa
aaaaaaaaa`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 3, X: 16},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 3, X: 9},
			// must guarantee reversibility: since shell wraps lines
			// automatically, we must remove newlines.
			// expectedOld:      "echo blaaaaaaaaa\naaaaaaaaa",
			expectedOld: "echo blaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ `,
		},
		{
			description: "delete prompt multiline, with last line",
			initialContent: `
~/src/blue master
$ echo blaaaaaaaaa
aaaaaaaaa
`,
			start:            term.Coordinates{Y: 2},
			end:              term.Coordinates{Y: 3, X: 16},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 3, X: 9},
			expectedOld:      "echo blaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ 
`,
		},
		{
			description: "insert with new line, with last line",
			initialContent: `
~/src/blue master
$ echo 
`,
			start:            term.Coordinates{Y: 2, X: 7},
			end:              term.Coordinates{Y: 2, X: 7},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "aaaaaaaaaaaaaaaaaaaa",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 7},
			// must guarantee reversibility so must be multiline
			expectedTo:  term.Coordinates{Y: 3, X: 9},
			expectedOld: "",
			expectedContent: `
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaa
`,
		},
		{
			description: "insert with new line, no last line",
			initialContent: `
~/src/blue master
$ echo `,
			start:            term.Coordinates{Y: 2, X: 7},
			end:              term.Coordinates{Y: 2, X: 7},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "aaaaaaaaaaaaaaaaaaaa",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 7},
			expectedTo:       term.Coordinates{Y: 3, X: 9},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaa`,
		},
		{
			description: "insert above last prompt rings bell",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 2},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "a",
			expectedRingBell: true,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 2},
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
		},
		{
			description: "delete above last prompt rings bell",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 3},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "",
			expectedRingBell: true,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 2},
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
		},
		{
			description: "delete up to prompt start rings a bell",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 4, X: 2},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "",
			expectedRingBell: true,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 4, X: 2},
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo `,
		},
		{
			description: "insert at shell-wrapped line",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
a`,
			start:            term.Coordinates{Y: 5, X: 1},
			end:              term.Coordinates{Y: 5, X: 1},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "xyz",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 5, X: 1},
			expectedTo:       term.Coordinates{Y: 5, X: 4},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
axyz`,
		},
		{
			description: "insert at shell-wrapped line, with more than line wrapped line",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
a`,
			start:            term.Coordinates{Y: 6, X: 1},
			end:              term.Coordinates{Y: 6, X: 1},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "xyz",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 6, X: 1},
			expectedTo:       term.Coordinates{Y: 6, X: 4},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
axyz`,
		},
		{
			description: "delete entire content, deletes only prompt lines, last line + 1",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
a`,
			start:            term.Coordinates{},
			end:              term.Coordinates{Y: 7},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 6, X: 1},
			expectedOld:      "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ `,
		},
		{
			description: "delete entire content, deletes only prompt lines, last column + 1",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
a`,
			start:            term.Coordinates{},
			end:              term.Coordinates{Y: 6, X: 1},
			promptStart:      term.Coordinates{Y: 4, X: 2},
			str:              "",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 6, X: 1},
			expectedOld:      "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ `,
		},
		{
			description: "entire content replace, replaces only prompt lines, exact length",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
a`,
			start:       term.Coordinates{},
			end:         term.Coordinates{Y: 6, X: 1},
			promptStart: term.Coordinates{Y: 4, X: 2},
			str: `
~/SRC/BLUE MASTER
% ECHO XXXXXXXXXX
~/src/blue master
$ ECHO AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA`,
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 6, X: 1},
			expectedOld:      "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ ECHO AAAAAAAAAAA
AAAAAAAAAAAAAAAAAA
A`,
		},
		{
			description: "entire content replace, replaces prompt lines + newlines until height",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaaaaaaaaaaaa
a`,
			start:       term.Coordinates{},
			end:         term.Coordinates{Y: 6, X: 1},
			promptStart: term.Coordinates{Y: 4, X: 2},
			str: `
~/SRC/BLUE MASTER
% ECHO XXXXXXXXXX
~/src/blue master
$ ECHO AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA












`,
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 17, X: 0},
			expectedOld:      "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ ECHO AAAAAAAAAAA
AAAAAAAAAAAAAAAAAA
A












`,
		},
		{
			description: "paste at prompt, not last line",
			initialContent: `
~/src/blue master
$ 
`,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 2},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "echo bla",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 10},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ echo bla
`,
		},
		{
			description: "paste at prompt, last line",
			initialContent: `
~/src/blue master
$ `,
			start:            term.Coordinates{Y: 2, X: 2},
			end:              term.Coordinates{Y: 2, X: 2},
			promptStart:      term.Coordinates{Y: 2, X: 2},
			str:              "echo bla",
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 2, X: 2},
			expectedTo:       term.Coordinates{Y: 2, X: 10},
			expectedOld:      "",
			expectedContent: `
~/src/blue master
$ echo bla`,
		},
		{
			description: "undo on wrapped line",
			initialContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaXXaaaa





`,
			start:       term.Coordinates{},
			end:         term.Coordinates{Y: 6},
			promptStart: term.Coordinates{Y: 4, X: 2},
			str: `
~/SRC/BLUE MASTER
% ECHO XXXXXXXXXX
~/src/blue master
$ echo aaaaaaaaaaaaaaaaaaooaaaa`,
			expectedRingBell: false,
			expectedFrom:     term.Coordinates{Y: 4, X: 2},
			expectedTo:       term.Coordinates{Y: 5, X: 13},
			expectedOld:      "echo aaaaaaaaaaaaaaaaaaXXaaaa",
			expectedContent: `
~/src/blue master
$ 
~/src/blue master
$ echo aaaaaaaaaaa
aaaaaaaooaaaa





`,
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			t.Parallel()
			comp := newTestParentComponent(test.initialContent, test.promptStart)
			var vi viHandler
			cfg := DefaultConfig()
			var actualBellsRung int
			cfg.RingBell = func() {
				actualBellsRung++
			}
			vi.doInit(comp, cfg)
			vi.remote = newTestRemote(comp.scroll, test.promptStart)
			vi.Resize(18, 18)

			ctx := context.Background()
			actualFrom, actualTo, actualOld := vi.Edit(ctx, test.start, test.end, test.str)

			assert.Equal(t, test.expectedContent, comp.scroll.Buffer().String())
			assert.Equal(t, test.expectedFrom, actualFrom, "from")
			assert.Equal(t, test.expectedTo, actualTo, "to")
			assert.Equal(t, test.expectedOld, actualOld)

			var expectedBellsRung int
			if test.expectedRingBell {
				expectedBellsRung = 1
			}
			assert.Equal(t, expectedBellsRung, actualBellsRung, "bells rung")
		})
	}
}

type testParentComponent struct {
	scroll *component.Scroll
	uri    workspaceapi.URI
	cursor term.Coordinates
}

func newTestParentComponent(content string, cursorAtScroll term.Coordinates) *testParentComponent {
	buf := new(cell.Buffer)
	buf.InitPerformance(1, 1, vtescreen.DefaultChar)

	scroll := new(component.Scroll)
	scroll.InitPerformance(buf)
	// do not use ReadFrom or InsertString as null characters
	// will be elided.
	var next term.Coordinates
	for _, ch := range content {
		next = buf.Insert(next, ch)
	}
	testURI, _ := workspaceapi.ParseURI("memory:///")
	return &testParentComponent{
		scroll: scroll,
		uri:    testURI,
		cursor: cursorAtScroll,
	}
}

func (c *testParentComponent) PrimaryScroll() (*component.Scroll, sync.Locker) {
	return c.scroll, nopLocker{}
}

func (c *testParentComponent) URI() workspaceapi.URI {
	return c.uri
}

func (c *testParentComponent) cursorAtScroll() term.Coordinates {
	return c.cursor
}

func (c *testParentComponent) pendingCallbacks() int {
	return 0
}

func (c *testParentComponent) scheduleBellCallback(callback func()) bool {
	callback()
	return true
}

type nopLocker struct {
}

func (nopLocker) Lock() {
}

func (nopLocker) Unlock() {
}

type testRemote struct {
	cursor             *text.Cursor
	keyArrowUpCalled   int
	keyArrowDownCalled int
	formFeedCalled     int
	lineFeedCalled     int
	ops                []func()
	ctx                context.Context
}

func newTestRemote(scroll *component.Scroll, cursorPosition term.Coordinates) *testRemote {
	ret := new(testRemote)
	ret.cursor = new(text.Cursor)
	ret.cursor.InitPerformance(scroll)
	// needed to ensure that remote edits bypass Edit checks
	ret.ctx = vtescreen.NewContext(context.Background())
	ret.cursor.MoveToScroll(cursorPosition)
	return ret
}

func (r *testRemote) moveStartOfLine() {
	r.cursor.MoveStartLine()
}

func (r *testRemote) keyArrowUp() {
	r.keyArrowUpCalled++
}

func (r *testRemote) keyArrowDown() {
	r.keyArrowDownCalled++
}

func (r *testRemote) deleteChar() {
	r.ops = append(r.ops, func() {
		r.cursor.DeleteContext(r.ctx)
	})
}

func (r *testRemote) insertChar(ch rune) {
	r.ops = append(r.ops, func() {
		r.cursor.InsertContext(r.ctx, ch, text.IndentRuneTab, 0)
	})
}

func (r *testRemote) linefeed() {
	r.lineFeedCalled++
}

func (r *testRemote) formFeed() {
	r.formFeedCalled++
}

func (r *testRemote) moveLeft() {
	r.ops = append(r.ops, func() {
		r.cursor.MoveLeft()
	})
}

func (r *testRemote) moveRight() {
	r.ops = append(r.ops, func() {
		r.cursor.MoveRight()
	})
}

func (r *testRemote) conflate() {
	r.ops = append(r.ops, func() {
		r.cursor.ConflateContext(r.ctx)
	})
}

func (r *testRemote) wrapLine() {
	r.ops = append(r.ops, func() {
		r.cursor.InsertContext(r.ctx, '\n', text.IndentRuneTab, 0)
	})
}

func (r *testRemote) cursorCRLF() {
	r.ops = append(r.ops, func() {
		r.cursor.MoveDown()
		r.cursor.MoveStartLine()
	})
}

func (r *testRemote) flush() error {
	for _, op := range r.ops {
		op()
	}
	r.ops = r.ops[:0]
	return nil
}

func (r *testRemote) triggerBell() error {
	return nil
}

type nopTabManager struct {
}

func (nopTabManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	panic("not implemented")
}

func (nopTabManager) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (nopTabManager) OnTabExit(workspaceapi.URI) bool {
	return false
}

func (nopTabManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }
