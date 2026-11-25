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

package vte

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/vsh/vshtest"
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
			// entering vi mode moves the cursor back a column, onto the
			// prompt: its start is only known once the shell answers
			{"<",
				`$ echo bla          
bla                 
$▐                  
                    
                    
                    
                    
                    
                    
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
$▐                  `},
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
			// 0 lands on the prompt before the shell answered, so seven l
			// reach the quote
			{"echo \"<k0llvk0yG0lllllllp", // multiline paste lands on its last character
				`$ echo bla          
bla                 
$ echo "$ echo blabl
▐                   
                    
                    
                    
                    
                    
                    `},
			{"la\"",
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
			// the prompt start is only known once the shell answers, so
			// until then nothing keeps the cursor off the prompt
			{"<0kk",
				`▐ echo blaaaaaaaaaaa
aaaaaaaaaaaaaaaaaaaa
aaaaaaaaaaaaaaaa    
                    
                    
                    
                    
                    
                    
                    `},
			{"\\$\\$0",
				`▐ echo blaaaaaaaaaaa
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

	t.Run("keys typed faster than the shell answers", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		cfg.Modal = true
		h, ch := testSequence(t, cfg, defaultWaitForIdleVte, []vtetest.Case{
			{"echo<0C", screen("$ ▐")},
		})
		// Each key schedules its edit while the bells the previous ones
		// asked for are still being answered.
		for _, r := range "echo hello" {
			h.Handle(term.Event{Type: term.EventKey, Ch: r, Raw: []byte(string(r))})
		}
		vtetest.TestCases(t, h, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{"", screen("$ echo hello▐")},
		})
	})

	t.Run("tab", func(t *testing.T) {
		t.Parallel()
		// The shell's Tab completes nothing for "zz", so the tab is
		// only vi's: one cell in its model, as on the screen.
		cases := []vtetest.Case{
			{"echo<0Cecho zz\ta",
				`$ echo zz ▐         
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, cases)
	})

	// A Tab that lists completions prompts again below the listing; the
	// vi edits that follow must change that line, not the one above.
	t.Run("vi edits after a Tab listing", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceVsh(t, cfg, defaultWaitForIdleVte,
			vshtest.Options{Files: map[string]string{"src/a.txt": "", "src/b.txt": ""}}, []string{"sh"},
			[]vtetest.Case{
				{InputSequence: "ls src/✌", Expected: screen("$ ls src/", "a.txt  b.txt", "$ ls src/▐")},
				{InputSequence: "<0C", Expected: screen("$ ls src/", "a.txt  b.txt", "$ ▐")},
			})
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

	// RUNE-193: entering modal mode while the shell cursor sits on a
	// wrapped continuation row must keep the prompt row in the editable
	// block, so an edit that lands on the upper row mutates the line
	// instead of being bell-rejected.
	t.Run("modal entered on wrapped continuation row", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequence(t, cfg, defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo aaaaaaaaaaaaaaaaaa<kiX", Expected: screen("$ ecX▐o aaaaaaaaaaaa", "aaaaaa")},
		})
	})

	// Once the wrapped line scrolls the screen, or there is scrollback
	// above the prompt, the vi cursor must keep inserting where the
	// user is typing instead of drifting to another row.
	t.Run("edits on a wrapping bottom row", func(t *testing.T) {
		t.Parallel()
		var lines strings.Builder
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&lines, "%d\n", i)
		}
		scrolled := []string{"23", "24", "25", "26", "27", "28", "29", "30"}
		unwrapped := append([]string{"22"}, scrolled...)
		// Every vi-mode key is synchronised with the shell through a
		// bell round-trip, so each case types a burst short enough for
		// the bell queue even on a loaded host.
		for _, tc := range []struct {
			name  string
			cases []vtetest.Case
		}{
			{name: "insert within the wrapped line", cases: []vtetest.Case{
				// 0 lands on the prompt before the shell answered, a WORD
				// of its own
				{InputSequence: "cat lines>echo aa bb cc dd<03WaXXXX",
					Expected: screen(append(scrolled, "$ echo aa bXXXX▐ cc ", "dd")...)},
			}},
			{name: "insert past the wrap", cases: []vtetest.Case{
				{InputSequence: "cat lines>echo<0C", Expected: screen(append(unwrapped, "$ ▐")...)},
				{InputSequence: "echo aaaaaaaaaa", Expected: screen(append(unwrapped, "$ echo aaaaaaaaaa▐")...)},
				{InputSequence: "aaaaaaaaaaaaaaaaaaaa",
					Expected: screen(append(scrolled, "$ echo aaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaa▐")...)},
			}},
			// ^L clears the screen, which leaves the prompt on the top
			// row with scrollback above it.
			{name: "insert past the wrap after a clear", cases: []vtetest.Case{
				{InputSequence: "cat lines>$echo<0C", Expected: screen("$ ▐")},
				{InputSequence: "echo aaaaaaaaaa", Expected: screen("$ echo aaaaaaaaaa▐")},
				{InputSequence: "aaaaaaaaaaaaaaaaaaaa",
					Expected: screen("$ echo aaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaa▐")},
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceVsh(t, cfg, defaultWaitForIdleVte,
					vshtest.Options{Files: map[string]string{"lines": lines.String()}}, []string{"sh"},
					tc.cases)
			})
		}
	})

	t.Run("modal during a running command navigates scrollback and copies", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		clip := clipboard.NewInMemory()
		cfg.Clipboard = clip
		cfg.Modal = true
		handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo abc>sleep 30>", Expected: screen("$ echo abc", "abc", "$ sleep 30", "▐")},
			{InputSequence: "<kk0", Expected: screen("$ echo abc", "▐bc", "$ sleep 30", "")},
			{InputSequence: "vey", Expected: screen("$ echo abc", "▐bc", "$ sleep 30", "")},
		})
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "abc", data.Text)

		// ^C leaves modal mode; a second one reaches the command.
		vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "#", Expected: screen("$ echo abc", "abc", "$ sleep 30", "▐")},
		})
		_, handled := handler.Handle(ctrlC)
		require.True(t, handled)
		vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "", Expected: screen("$ echo abc", "abc", "$ sleep 30", "^C", "$ ▐")},
		})
	})

	t.Run("line joins", func(t *testing.T) {
		t.Parallel()
		for _, sequence := range modalLineJoinSequences {
			t.Run(sequence.name, func(t *testing.T) {
				t.Parallel()
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceVsh(t, cfg, defaultWaitForIdleVte,
					vshtest.Options{Commands: vshCommands()}, []string{"sh"}, sequence.cases)
			})
		}
	})

	// The frozen screen edits nothing, and its program reads none of the
	// keys, which fullscreen would print. A refused join leaves the
	// cursor where the rows would join, past the last column: 0 takes it
	// back to the row's start.
	t.Run("line joins on a full-screen program", func(t *testing.T) {
		t.Parallel()
		program := screen("first row", "se▐ond row")
		firstRow := screen("▐irst row", "second row")
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceVsh(t, cfg, defaultWaitForIdleVte, vshtest.Options{Commands: vshCommands()}, []string{"sh"},
			[]vtetest.Case{
				{InputSequence: "fullscreen>", Expected: program},
				{InputSequence: "⎋", Expected: program},
				{InputSequence: "kgJ0", Expected: firstRow},
				{InputSequence: "A⌦<0", Expected: firstRow},
				{InputSequence: "jI^<0", Expected: firstRow},
				{InputSequence: "j0X0", Expected: firstRow},
				{InputSequence: "jVkgJ0", Expected: firstRow},
				{InputSequence: "i>", Expected: screen("$ fullscreen", `read ""`, "$ ▐")},
			})
	})

	// A command reading a line answers the probe as the shell does, and
	// gets no line join either: the next line it reads is the one typed.
	t.Run("line joins during a command", func(t *testing.T) {
		t.Parallel()
		running := func(rows ...string) string {
			return screen(append([]string{"25", "26", "27", "28", "29", "30"}, rows...)...)
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceVsh(t, cfg, defaultWaitForIdleVte, vshtest.Options{Commands: vshCommands()}, []string{"sh"},
			[]vtetest.Case{
				{InputSequence: "seq 30>cat>one>", Expected: running("$ cat", "one", "one", "▐")},
				{InputSequence: "<kgJ0", Expected: running("$ cat", "one", "▐ne", "")},
				{InputSequence: "X0", Expected: running("$ cat", "▐ne", "one", "")},
				{InputSequence: "jI^<0", Expected: running("$ cat", "▐ne", "one", "")},
				{InputSequence: "kA⌦<0", Expected: running("▐ cat", "one", "one", "")},
				{InputSequence: "itwo>",
					Expected: screen("27", "28", "29", "30", "$ cat", "one", "one", "two", "two", "▐")},
			})
	})

	t.Run("append", func(t *testing.T) {
		t.Parallel()
		for _, sequence := range modalAppendSequences {
			t.Run(sequence.name, func(t *testing.T) {
				t.Parallel()
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceVsh(t, cfg, defaultWaitForIdleVte,
					vshtest.Options{Commands: vshCommands()}, []string{"sh"}, sequence.cases)
			})
		}
	})

	t.Run("append on a full-screen program", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceVsh(t, cfg, defaultWaitForIdleVte, vshtest.Options{Commands: vshCommands()}, []string{"sh"},
			[]vtetest.Case{
				{InputSequence: "fullscreen>⎋kA", Expected: screen("first row▐", "second row")},
				{InputSequence: "X", Expected: screen("first row▐", "second row")},
				{InputSequence: "i>", Expected: screen("$ fullscreen", `read ""`, "$ ▐")},
			})
	})

	t.Run("hide", func(t *testing.T) {
		t.Parallel()
		for _, sequence := range modalHideSequences {
			t.Run(sequence.name, func(t *testing.T) {
				t.Parallel()
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceVsh(t, cfg, defaultWaitForIdleVte,
					vshtest.Options{Commands: vshCommands()}, []string{"sh"}, sequence.cases)
			})
		}
	})

	t.Run("hide on a full-screen program", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		cfg.Modal = true
		testSequenceVsh(t, cfg, defaultWaitForIdleVte, vshtest.Options{Commands: vshCommands()}, []string{"sh"},
			[]vtetest.Case{
				{InputSequence: "fullscreen>⎋kVjz", Expected: screen("first row", "se▐ond row")},
				{InputSequence: "i>", Expected: screen("$ fullscreen", `read ""`, "$ ▐")},
			})
	})

	t.Run("leave", func(t *testing.T) {
		t.Parallel()
		for _, sequence := range modalLeaveSequences {
			t.Run(sequence.name, func(t *testing.T) {
				t.Parallel()
				cfg := DefaultConfig()
				cfg.Modal = true
				testSequenceVsh(t, cfg, defaultWaitForIdleVte,
					vshtest.Options{Commands: vshCommands()}, []string{"sh"}, sequence.cases)
			})
		}
	})
}

// modalAppendSequences press A in modal mode, which appends after the
// last cell the program wrote on the cursor's row: past it are the cells
// that make the row as wide as the screen, which it never wrote. On the
// prompt, what is typed next reaches the end of the shell's line. They
// run on the Rune shell, and on real ones in the e2e suite.
var modalAppendSequences = func() []struct {
	name  string
	cases []vtetest.Case
} {
	scrolled := func(rows ...string) string {
		return screen(append([]string{"22", "23", "24", "25", "26", "27", "28", "29"}, rows...)...)
	}
	return []struct {
		name  string
		cases []vtetest.Case
	}{
		{name: "on the first row", cases: []vtetest.Case{
			{InputSequence: "echo bla<AX", Expected: screen("$ echo blaX▐")},
		}},
		{name: "below output", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<AX", Expected: screen("$ echo one", "one", "$ echo blaX▐")},
		}},
		{name: "on a scrolled screen", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<AX", Expected: scrolled("30", "$ echo blaX▐")},
		}},
		{name: "after a space", cases: []vtetest.Case{
			{InputSequence: "echo bla <AX", Expected: screen("$ echo bla X▐")},
		}},
		{name: "twice", cases: []vtetest.Case{
			{InputSequence: "echo bla<AX<AY", Expected: screen("$ echo blaXY▐")},
		}},
		{name: "above the prompt", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<kA", Expected: scrolled("30▐", "$ echo bla")},
			{InputSequence: "X<GAY", Expected: scrolled("30", "$ echo blaY▐")},
		}},
	}
}()

// modalHideSequences press z on a selection in modal mode, which would
// hide its rows in an editor: the terminal hides none, and leaves visual
// mode with the rows as the program drew them. They run on the Rune
// shell, and on real ones in the e2e suite.
var modalHideSequences = func() []struct {
	name  string
	cases []vtetest.Case
} {
	fresh := func(prompt string) string { return screen("$ echo one", "one", prompt) }
	scrolled := func(rows ...string) string {
		return screen(append([]string{"22", "23", "24", "25", "26", "27", "28", "29"}, rows...)...)
	}
	return []struct {
		name  string
		cases []vtetest.Case
	}{
		{name: "characters", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<vhz", Expected: fresh("$ echo b▐a")},
			{InputSequence: "\\$aX", Expected: fresh("$ echo blaX▐")},
		}},
		{name: "rows", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<Vkz0", Expected: screen("$ echo one", "▐ne", "$ echo bla")},
			{InputSequence: "G\\$aX", Expected: fresh("$ echo blaX▐")},
		}},
		{name: "rows on a scrolled screen", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<Vkz0", Expected: scrolled("▐0", "$ echo bla")},
			{InputSequence: "G\\$aX", Expected: scrolled("30", "$ echo blaX▐")},
		}},
		{name: "history", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<ggVjjz0",
				Expected: screen("$ seq 30", "1", "▐", "3", "4", "5", "6", "7", "8", "9")},
			{InputSequence: "G\\$aX", Expected: scrolled("30", "$ echo blaX▐")},
		}},
	}
}()

// modalLeaveSequences type right after a key that leaves modal mode,
// before the shell can have answered the bell that moves its cursor
// there: what is typed reaches it after. They run on the Rune shell, and
// on real ones in the e2e suite.
var modalLeaveSequences = []struct {
	name  string
	cases []vtetest.Case
}{
	{name: "i", cases: []vtetest.Case{
		{InputSequence: "echo bla<hh⏩iX", Expected: screen("$ echo X▐la")},
	}},
	{name: "ctrl-c", cases: []vtetest.Case{
		{InputSequence: "echo bla<hh⏩#X", Expected: screen("$ echo X▐la")},
	}},
	// entering again, before or after the shell got the i, and leaving
	// without moving, which leaves the shell's cursor where it was
	{name: "i, then esc", cases: []vtetest.Case{
		{InputSequence: "echo bla<hh⏩iX<iY>", Expected: screen("$ echo XYbla", "XYbla", "$ ▐")},
	}},
	{name: "i, then esc once the shell got it", cases: []vtetest.Case{
		{InputSequence: "echo bla<hhiX", Expected: screen("$ echo X▐la")},
		{InputSequence: "⏩<iY>", Expected: screen("$ echo XYbla", "XYbla", "$ ▐")},
	}},
}

// modalLineJoinSequences press the keys that join two rows in modal mode,
// which a shell's single line refuses: on the prompt or above it, on
// screen or in the history. Each leaves the rows as the shell drew them
// and its line editable. They run on the Rune shell, and on real ones in
// the e2e suite.
var modalLineJoinSequences = func() []struct {
	name  string
	cases []vtetest.Case
} {
	fresh := func(prompt string) string { return screen("$ echo one", "one", prompt) }
	scrolled := func(prompt string) string {
		return screen("22", "23", "24", "25", "26", "27", "28", "29", "30", prompt)
	}
	return []struct {
		name  string
		cases []vtetest.Case
	}{
		{name: "gJ", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<gJ", Expected: fresh("$ echo bl▐")},
			{InputSequence: "k0gJ0", Expected: screen("$ echo one", "▐ne", "$ echo bla")},
			{InputSequence: "G\\$aX", Expected: fresh("$ echo blaX▐")},
		}},
		{name: "delete at the end of the line", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<A⌦", Expected: fresh("$ echo bla▐")},
			{InputSequence: "X", Expected: fresh("$ echo blaX▐")},
			{InputSequence: "<kA⌦<0", Expected: screen("$ echo one", "▐ne", "$ echo blaX")},
			{InputSequence: "G\\$aY", Expected: fresh("$ echo blaXY▐")},
		}},
		{name: "backspace at the start of a line", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<k0I^<G\\$aX", Expected: fresh("$ echo blaX▐")},
		}},
		{name: "backspace on a changed line", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<S^", Expected: fresh("$ ▐")},
			{InputSequence: "echo two>", Expected: screen("$ echo one", "one", "$ echo two", "two", "$ ▐")},
		}},
		{name: "delete before the start of a line", cases: []vtetest.Case{
			{InputSequence: "echo one>echo bla<k0XG\\$aX", Expected: fresh("$ echo blaX▐")},
		}},
		{name: "gJ on a scrolled screen", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<gJ", Expected: scrolled("$ echo bl▐")},
			{InputSequence: "kgJ0", Expected: screen("22", "23", "24", "25", "26", "27", "28", "29", "▐0", "$ echo bla")},
			{InputSequence: "G\\$aX", Expected: scrolled("$ echo blaX▐")},
		}},
		{name: "delete at the end of the line on a scrolled screen", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<\\$a⌦X", Expected: scrolled("$ echo blaX▐")},
		}},
		{name: "delete at the end of a scrolled row", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<kA⌦<G\\$aX", Expected: scrolled("$ echo blaX▐")},
		}},
		{name: "backspace at the start of a scrolled row", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<k0I^<G\\$aX", Expected: scrolled("$ echo blaX▐")},
		}},
		{name: "backspace on a changed line on a scrolled screen", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<S^", Expected: scrolled("$ ▐")},
			{InputSequence: "echo two>",
				Expected: screen("24", "25", "26", "27", "28", "29", "30", "$ echo two", "two", "$ ▐")},
		}},
		{name: "delete before the start of a scrolled row", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<k0XG\\$aX", Expected: scrolled("$ echo blaX▐")},
		}},
		{name: "history", cases: []vtetest.Case{
			{InputSequence: "seq 30>echo bla<gg0",
				Expected: screen("▐ seq 30", "1", "2", "3", "4", "5", "6", "7", "8", "9")},
			{InputSequence: "jgJ0XjI^<jA⌦<0",
				Expected: screen("$ seq 30", "▐", "2", "3", "4", "5", "6", "7", "8", "9")},
			{InputSequence: "G\\$aX", Expected: scrolled("$ echo blaX▐")},
		}},
	}
}()

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
			keys string
		}{
			{name: "open below", keys: "o"},
			{name: "open above", keys: "O"},
			{name: "append", keys: "a"},
			{name: "delete character", keys: "x"},
			{name: "delete character before", keys: "X"},
			{name: "move up", keys: "k"},
			{name: "move down", keys: "j"},
			{name: "end of line", keys: "$"},
			{name: "join lines", keys: "J"},
			{name: "join lines verbatim", keys: "gJ"},
			{name: "join selected lines verbatim", keys: "VkgJ"},
			{name: "backspace at the start of a line", keys: "I<backspace>"},
			{name: "backspace on a changed line", keys: "cc<backspace>"},
			{name: "backspace on an opened line", keys: "o<backspace>"},
			{name: "delete at the end of a line", keys: "A<delete>"},
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
						for _, ev := range keyEvents(t, command.keys) {
							_, _ = vi.copy.vi.Handle(ev)
						}
					})
					assert.Equal(t, operation.want, comp.scroll.Buffer().String(),
						"the copy vi only tells what a key does")
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
		var played []string
		cfg.Sounds = sounds.PlayerFunc(func(name string) {
			played = append(played, name)
		})
		vi.doInit(comp, cfg)
		vi.remote = newTestRemote(comp.scroll, 18, comp.cursor)
		vi.Resize(18, 18)

		// Mimic what vte.Component.RestoreFromSnapshot does: rewrite
		// the existing primary buffer's cells in place and move the
		// cursor. *cell.Buffer / *component.Scroll identity is
		// preserved.
		comp.scroll.Buffer().ResetCells(term.StringToCells("$ restored "))
		comp.cursor = term.Coordinates{X: 11}
		vi.setCursorAtScroll(comp.cursor)
		vi.remote = newTestRemote(comp.scroll, 18, comp.cursor)

		from, to, old := vi.Edit(context.Background(),
			comp.cursor, comp.cursor, "X")

		assert.Equal(t, "$ restored X", comp.scroll.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 11}, from)
		assert.Equal(t, term.Coordinates{X: 12}, to)
		assert.Equal(t, "", old)
		assert.Empty(t, played)
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

		expectedError   bool
		expectedContent string
		expectedFrom    term.Coordinates
		expectedTo      term.Coordinates
		expectedOld     string
	}{
		{
			description:     "(invalid) zero Edit",
			initialContent:  "",
			promptStart:     term.Coordinates{},
			start:           term.Coordinates{},
			end:             term.Coordinates{},
			str:             "",
			expectedError:   true,
			expectedContent: "",
		},
		{
			description: "insert within last prompt line, exactly after prompt",
			initialContent: `
~/src/blue master
$ 
`,
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 2},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "echo",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "",
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 2},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "$ echo",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "",
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 7},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "echo",
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 2, X: 7},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "echo",
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 3},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "echo",
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 3},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 6},
			expectedOld:   "echo",
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 3, X: 16},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 3, X: 9},
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
			start:         term.Coordinates{Y: 2},
			end:           term.Coordinates{Y: 3, X: 16},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 3, X: 9},
			expectedOld:   "echo blaaaaaaaaaaaaaaaaaa",
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
			start:         term.Coordinates{Y: 2, X: 7},
			end:           term.Coordinates{Y: 2, X: 7},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "aaaaaaaaaaaaaaaaaaaa",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 7},
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
			start:         term.Coordinates{Y: 2, X: 7},
			end:           term.Coordinates{Y: 2, X: 7},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "aaaaaaaaaaaaaaaaaaaa",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 7},
			expectedTo:    term.Coordinates{Y: 3, X: 9},
			expectedOld:   "",
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 2},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "a",
			expectedError: true,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 2},
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 3},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "",
			expectedError: true,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 2},
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 4, X: 2},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "",
			expectedError: true,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 4, X: 2},
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
			start:         term.Coordinates{Y: 5, X: 1},
			end:           term.Coordinates{Y: 5, X: 1},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "xyz",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 5, X: 1},
			expectedTo:    term.Coordinates{Y: 5, X: 4},
			expectedOld:   "",
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
			start:         term.Coordinates{Y: 6, X: 1},
			end:           term.Coordinates{Y: 6, X: 1},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "xyz",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 6, X: 1},
			expectedTo:    term.Coordinates{Y: 6, X: 4},
			expectedOld:   "",
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
			start:         term.Coordinates{},
			end:           term.Coordinates{Y: 7},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 6, X: 1},
			expectedOld:   "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
			start:         term.Coordinates{},
			end:           term.Coordinates{Y: 6, X: 1},
			promptStart:   term.Coordinates{Y: 4, X: 2},
			str:           "",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 6, X: 1},
			expectedOld:   "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 6, X: 1},
			expectedOld:   "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 19, X: 0},
			expectedOld:   "echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 2},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "echo bla",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 10},
			expectedOld:   "",
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
			start:         term.Coordinates{Y: 2, X: 2},
			end:           term.Coordinates{Y: 2, X: 2},
			promptStart:   term.Coordinates{Y: 2, X: 2},
			str:           "echo bla",
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 2, X: 2},
			expectedTo:    term.Coordinates{Y: 2, X: 10},
			expectedOld:   "",
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
			expectedError: false,
			expectedFrom:  term.Coordinates{Y: 4, X: 2},
			expectedTo:    term.Coordinates{Y: 5, X: 13},
			expectedOld:   "echo aaaaaaaaaaaaaaaaaaXXaaaa",
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
			var played []string
			cfg.Sounds = sounds.PlayerFunc(func(name string) {
				played = append(played, name)
			})
			vi.doInit(comp, cfg)
			vi.remote = newTestRemote(comp.scroll, 18, test.promptStart)
			vi.Resize(18, 18)

			ctx := context.Background()
			actualFrom, actualTo, actualOld := vi.Edit(ctx, test.start, test.end, test.str)

			assert.Equal(t, test.expectedContent, comp.scroll.Buffer().String())
			assert.Equal(t, test.expectedFrom, actualFrom, "from")
			assert.Equal(t, test.expectedTo, actualTo, "to")
			assert.Equal(t, test.expectedOld, actualOld)

			var expectedPlayed []string
			if test.expectedError {
				expectedPlayed = []string{sounds.Error}
			}
			assert.Equal(t, expectedPlayed, played, "sounds played")
		})
	}
}

func TestViRemoteMoveTo(t *testing.T) {
	t.Parallel()
	// the fake prompt "$ " occupies the first two columns
	const promptEnd = 2

	for _, tc := range []struct {
		name    string
		content string
		target  term.Coordinates
		want    []string
		wantX   int
	}{
		{
			name:    "append past last character asks for end of line",
			content: "$ e", target: term.Coordinates{X: 3},
			want: []string{"moveEndOfLine"}, wantX: 3,
		},
		{
			name:    "column inside the line still walks right",
			content: "$ echo", target: term.Coordinates{X: 4},
			want: []string{"moveRight", "moveRight"}, wantX: 4,
		},
		{
			name:    "append column already under the cursor moves nothing",
			content: "$ ", target: term.Coordinates{X: promptEnd},
			want: nil, wantX: promptEnd,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			comp := newTestParentComponent(tc.content, term.Coordinates{X: promptEnd})
			var v viHandler
			v.doInit(comp, DefaultConfig())
			v.width = 20
			scroll, _ := comp.PrimaryScroll()
			remote := newTestRemote(scroll, 20, term.Coordinates{X: promptEnd})
			v.remote = remote

			assert.Equal(t, tc.wantX, v.remoteMoveTo(tc.target))
			assert.Equal(t, tc.want, remote.calls)
		})
	}
}

func TestViTypedKeys(t *testing.T) {
	t.Parallel()
	const (
		// press presses a key of kind in modal mode.
		press = iota
		// typeKeys types keys outside modal mode, which send keys.
		typeKeys
		// reach tells that the key pressed number key, from 1, reached
		// the program, or never will.
		reach
		// tick runs what was scheduled on the event loop.
		tick
	)
	type step struct {
		do   int
		kind viKeyKind
		keys string
		key  int
		// held is whether typeKeys held keys.
		held bool
		// want is what reached the program during the step.
		want string
	}
	for _, tc := range []struct {
		name string
		// noTicks is whether the event loop takes no more ticks.
		noTicks  bool
		writeErr error
		steps    []step
	}{
		{
			name: "keys typed while no key left modal mode are not held",
			steps: []step{
				{do: typeKeys, keys: "a", want: "a"},
			},
		},
		{
			name: "a key that stays in modal mode holds nothing",
			steps: []step{
				{do: press, kind: viKeyRead},
				{do: press, kind: viKeyEdit},
				{do: press, kind: viKeyUp},
				{do: typeKeys, keys: "a", want: "a"},
			},
		},
		{
			name: "keys typed after i wait for it to reach the program",
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: typeKeys, keys: "b", held: true},
				{do: tick},
				// what i sent is written on a tick scheduled before
				{do: reach, key: 1},
				{do: tick, want: "ab"},
			},
		},
		{
			name: "keys typed after enter wait for it to reach the program",
			steps: []step{
				{do: press, kind: viKeyEnter},
				{do: typeKeys, keys: "ls", held: true},
				{do: reach, key: 1},
				{do: tick, want: "ls"},
			},
		},
		{
			name: "keys typed after each of two keys that left wait for both",
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: press, kind: viKeyEnter},
				{do: typeKeys, keys: "b", held: true},
				{do: reach, key: 2},
				{do: tick},
				{do: reach, key: 1},
				{do: tick, want: "ab"},
			},
		},
		{
			name: "keys typed once an earlier key reached the program wait for a later one",
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: press, kind: viKeyEnter},
				{do: reach, key: 1},
				{do: tick, want: "a"},
				{do: typeKeys, keys: "b", held: true},
				{do: reach, key: 2},
				{do: tick, want: "b"},
			},
		},
		{
			name: "keys are held no more once every key that left reached the program",
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: reach, key: 1},
				{do: tick, want: "a"},
				{do: typeKeys, keys: "b", want: "b"},
			},
		},
		{
			name: "a key that left with nothing typed after it writes nothing",
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: reach, key: 1},
				{do: tick},
				{do: typeKeys, keys: "a", want: "a"},
			},
		},
		{
			name:    "the keys typed are written at once when the event loop takes no tick",
			noTicks: true,
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: reach, key: 1, want: "a"},
				{do: typeKeys, keys: "b", want: "b"},
			},
		},
		{
			name:     "the program failing a write holds no keys typed after",
			writeErr: errors.New("closed"),
			steps: []step{
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "a", held: true},
				{do: press, kind: viKeyExit},
				{do: typeKeys, keys: "b", held: true},
				{do: reach, key: 1},
				{do: reach, key: 2},
				{do: tick, want: "ab"},
				{do: typeKeys, keys: "c", want: "c"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			comp := newTestParentComponent("$ echo hi", term.Coordinates{X: 2})
			comp.writeErr = tc.writeErr
			var ticks []func()
			cfg := DefaultConfig()
			cfg.ScheduleNextTick = func(cb func()) bool {
				if tc.noTicks {
					return false
				}
				ticks = append(ticks, cb)
				return true
			}
			var v viHandler
			v.doInit(comp, cfg)

			var pressed []viKey
			for i, s := range tc.steps {
				before := len(comp.written)
				switch s.do {
				case press:
					pressed = append(pressed, v.leave(viKey{kind: s.kind}))
				case typeKeys:
					// as Handler.writeToPty does
					held := v.hold([]byte(s.keys))
					if !held {
						_ = comp.WriteToPty([]byte(s.keys))
					}
					assert.Equal(t, s.held, held, "step %d: held", i)
				case reach:
					v.sent(pressed[s.key-1].typed)
				case tick:
					for len(ticks) > 0 {
						cb := ticks[0]
						ticks = ticks[1:]
						cb()
					}
				}
				assert.Equal(t, s.want, string(comp.written[before:]), "step %d", i)
			}
		})
	}
}

func TestViEnterViMode(t *testing.T) {
	t.Parallel()
	// the shell's cursor is after "$ echo hi", and the session left the
	// vi cursor on the e
	var (
		shell   = term.Coordinates{X: 9}
		session = term.Coordinates{X: 2}
		// entering puts the cursor on the character before the shell's
		fresh = term.Coordinates{X: 8}
	)
	for _, tc := range []struct {
		name  string
		probe viProbe
		// leaving is whether a key left modal mode, and reached is
		// whether it reached the program since.
		leaving, reached bool
		wantCursor       term.Coordinates
		wantProbe        viProbe
		// wantResumed is whether leaving without moving sends nothing.
		wantResumed bool
	}{
		{
			name:       "a session starts at the shell's cursor",
			probe:      viUnprobed,
			wantCursor: fresh, wantProbe: viUnprobed,
		},
		{
			name:       "a probe still waiting for its bell answers for the next session",
			probe:      viProbing,
			wantCursor: fresh, wantProbe: viProbing,
		},
		{
			name:       "a session probes the shell again",
			probe:      viSynced,
			wantCursor: fresh, wantProbe: viUnprobed,
		},
		{
			name:       "a session probes the shell again after one it refused",
			probe:      viRefused,
			wantCursor: fresh, wantProbe: viUnprobed,
		},
		{
			name:  "a session goes on while the key that left waits for its probe",
			probe: viProbing, leaving: true,
			wantCursor: session, wantProbe: viProbing, wantResumed: true,
		},
		{
			name:  "a session goes on while the key that left waits for its bell",
			probe: viSynced, leaving: true,
			wantCursor: session, wantProbe: viSynced, wantResumed: true,
		},
		{
			name:  "a session starts at the shell's cursor once the key that left reached it",
			probe: viSynced, leaving: true, reached: true,
			wantCursor: fresh, wantProbe: viUnprobed,
		},
		{
			// the keys typed after it are written on the next tick
			name:  "a session starts at the shell's cursor once the shell refused the key that left",
			probe: viRefused, leaving: true,
			wantCursor: fresh, wantProbe: viUnprobed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			comp := newTestParentComponent("$ echo hi", shell)
			var ticks []func()
			cfg := DefaultConfig()
			cfg.ScheduleNextTick = func(cb func()) bool {
				ticks = append(ticks, cb)
				return true
			}
			var v viHandler
			v.doInit(comp, cfg)
			v.Resize(20, 4)
			v.viSetCursorAtScroll(session)
			v.probe = tc.probe
			if tc.leaving {
				k := v.leave(viKey{kind: viKeyExit})
				if tc.reached {
					v.sent(k.typed)
					for _, cb := range ticks {
						cb()
					}
				}
			}
			// whatever the last session left it at
			v.resumed = !tc.wantResumed

			v.enterViMode(shell)

			assert.Equal(t, tc.wantCursor, v.cursorAtScroll())
			assert.Equal(t, tc.wantCursor, v.entry, "leaving without moving must not reach the shell")
			assert.Equal(t, tc.wantProbe, v.probe)
			assert.Equal(t, tc.wantResumed, v.resumed)
			v.copy.mu.Lock()
			defer v.copy.mu.Unlock()
			assert.Equal(t, tc.wantCursor, v.copy.vi.CursorAtScroll(), "the copy vi runs keys from the same cursor")
		})
	}
}

type testParentComponent struct {
	scroll *component.Scroll
	uri    workspaceapi.URI
	cursor term.Coordinates
	// written is what WriteToPty was given, and writeErr what it
	// returns.
	written  []byte
	writeErr error
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

func (c *testParentComponent) scheduleBellCallback(callback, _ func()) bool {
	callback()
	return true
}

func (c *testParentComponent) systemCanDispatchBell(callback func(error)) {
	callback(nil)
}

func (c *testParentComponent) shellCommand() (uint64, bool, bool) {
	return 0, false, false
}

func (c *testParentComponent) WriteToPty(data []byte) error {
	c.written = append(c.written, data...)
	return c.writeErr
}

type nopLocker struct {
}

func (nopLocker) Lock() {
}

func (nopLocker) Unlock() {
}

type testRemote struct {
	cursor             *text.Cursor
	width              int
	keyArrowUpCalled   int
	keyArrowDownCalled int
	formFeedCalled     int
	lineFeedCalled     int
	ops                []func()
	ctx                context.Context
	calls              []string
}

func newTestRemote(
	scroll *component.Scroll, width int, cursorPosition term.Coordinates,
) *testRemote {
	ret := new(testRemote)
	ret.cursor = new(text.Cursor)
	ret.width = width
	ret.cursor.InitPerformance(scroll)
	// needed to ensure that remote edits bypass Edit checks
	ret.ctx = vtescreen.NewContext(context.Background())
	ret.cursor.MoveToScroll(cursorPosition)
	return ret
}

func (r *testRemote) moveStartOfLine() {
	r.calls = append(r.calls, "moveStartOfLine")
	r.cursor.MoveStartLine()
}

func (r *testRemote) moveEndOfLine() {
	r.calls = append(r.calls, "moveEndOfLine")
	r.ops = append(r.ops, func() {
		// The shell's end-of-line reaches the end of its whole line
		// buffer, which the vte renders as a run of full-width rows.
		for {
			r.cursor.MoveEndLine()
			if r.width <= 0 || r.cursor.CursorAtScroll().X < r.width {
				return
			}
			if !r.cursor.MoveDown() {
				return
			}
			r.cursor.MoveStartLine()
		}
	})
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
	r.calls = append(r.calls, "moveLeft")
	r.ops = append(r.ops, func() {
		r.cursor.MoveLeft()
	})
}

func (r *testRemote) moveRight() {
	r.calls = append(r.calls, "moveRight")
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
	r.calls = append(r.calls, "cursorCRLF")
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
