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

package input_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/handler/input"
	"unstable.build/rune/internal/text/standard"
)

func TestBox(t *testing.T) {
	ed := standard.Editor()
	t.Run("min and max height passed are coherent or else it panics", func(t *testing.T) {
		// ok
		input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{})

		// ok
		input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{MinHeight: 1})

		// ok
		input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{MaxHeight: 1})

		// ok
		input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{MaxHeight: 1, MinHeight: 1})

		// ok
		input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{MaxHeight: 2, MinHeight: 1})

		assert.Panics(t, func() {
			input.NewBox(cell.NewBuffer(), ed, input.BoxConfig{MaxHeight: 1, MinHeight: 3})
		})
	})
	t.Run("no placeholder", func(t *testing.T) {
		buf := cell.NewBuffer()
		b := input.NewBox(buf, ed, input.BoxConfig{})
		b.Resize(20, 4)

		w := term.NewStringWriter(20, 9)

		tests := []comptest.TestCase{
			{
				nil, `
┌──────────────────┐
│                  │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(2, 2) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(20, 9) }, `
┌──────────────────┐
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() { b.Resize(20, 1) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(20, 9) }, `
┌──────────────────┐
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, "hello world")
					assert.Equal(t, 3, b.Height(20))
					b.Resize(20, 1)
				}, `
hello world         
                    
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() {
					b.Resize(20, 9)
					writeBuffer(b, ". Let's test its responsiveness")
					require.Equal(t, 5, b.Height(20))
					b.Resize(20, 6)
				}, `
┌──────────────────┐
│hello world. Let's│
│ test its responsi│
│veness            │
│                  │
└──────────────────┘
                    
                    
                    `,
			}, {
				func() {
					writeBuffer(b, ". Let's test its responsiveness")
					require.Equal(t, 7, b.Height(20))
					b.Resize(20, 8)
				}, `
┌──────────────────┐
│ test its responsi│
│veness. Let's test│
│ its responsivenes│
│s                 │
│                  │
│                  │
└──────────────────┘
                    `,
			}, {
				func() {
					writeBuffer(b, ". Let's test its responsiveness")
					assert.Equal(t, 8, b.Height(20))
					b.Resize(20, 9)
				}, `
┌──────────────────┐
│ test its responsi│
│veness. Let's test│
│ its responsivenes│
│s. Let's test its │
│responsiveness    │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, ". Let's test its scrolling")
					assert.Equal(t, 10, b.Height(20))
				}, `
┌──────────────────┐
│ test its responsi│
│veness. Let's test│
│ its responsivenes│
│s. Let's test its │
│responsiveness. Le│
│t's test its scrol│
│ling              │
└──────────────────┘`,
			}, {
				func() {
					handled := true
					for handled {
						_, handled = b.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
					}
				}, `
┌──────────────────┐
│hello world. Let's│
│ test its responsi│
│veness. Let's test│
│ its responsivenes│
│s. Let's test its │
│responsiveness. Le│
│t's test its scrol│
└──────────────────┘`,
			},
		}
		comptest.TestComponent(t, b, w, tests)
	})
	t.Run("with placeholder", func(t *testing.T) {
		buf := cell.NewBuffer()
		b := input.NewBox(buf, ed, input.BoxConfig{Placeholder: "HERE..."})
		b.Resize(20, 4)

		w := term.NewStringWriter(20, 9)

		tests := []comptest.TestCase{
			{
				nil, `
┌──────────────────┐
│HERE...           │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
			}, {
				func() {
					assert.Equal(t, 3, b.Height(20))
					b.Resize(2, 2)
					assert.Equal(t, 3, b.Height(20))
				}, `
HE                  
RE                  
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(10, 2) }, `
HERE...             
                    
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(20, 9) }, `
┌──────────────────┐
│HERE...           │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() { b.Resize(20, 9) }, `
┌──────────────────┐
│HERE...           │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, "hello world")
					assert.Equal(t, 3, b.Height(20))
				}, `
┌──────────────────┐
│hello world       │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					// last row full feature
					writeBuffer(b, "xxxxxxx")
					assert.Equal(t, 4, b.Height(20))
				}, `
┌──────────────────┐
│hello worldxxxxxxx│
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, "x")
					assert.Equal(t, 4, b.Height(20))
				}, `
┌──────────────────┐
│hello worldxxxxxxx│
│x                 │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			},
		}
		comptest.TestComponent(t, b, w, tests)
	})

	t.Run("with long placeholder", func(t *testing.T) {
		buf := cell.NewBuffer()
		b := input.NewBox(buf, ed, input.BoxConfig{Placeholder: "please write to your great, lovely, assistant"})
		b.Resize(20, 4)

		w := term.NewStringWriter(20, 9)

		tests := []comptest.TestCase{
			{
				nil, `
┌──────────────────┐
│please write to yo│
│ur great, lovely, │
└──────────────────┘
                    
                    
                    
                    
                    `,
			}, {
				func() {
					assert.Equal(t, 5, b.Height(20))
					b.Resize(2, 2)
					assert.Equal(t, 5, b.Height(20))
				}, `
pl                  
ea                  
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(10, 2) }, `
please wri          
te to your          
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() { b.Resize(20, 9) }, `
┌──────────────────┐
│please write to yo│
│ur great, lovely, │
│assistant         │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, "hello world")
					assert.Equal(t, 3, b.Height(20))
				}, `
┌──────────────────┐
│hello world       │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			},
		}
		comptest.TestComponent(t, b, w, tests)
	})
	t.Run("with min, max height", func(t *testing.T) {
		buf := cell.NewBuffer()
		b := input.NewBox(buf, ed, input.BoxConfig{MinHeight: 4, MaxHeight: 5, Placeholder: "HERE..."})
		b.Resize(20, 4)

		w := term.NewStringWriter(20, 9)

		tests := []comptest.TestCase{
			{
				nil, `
┌──────────────────┐
│HERE...           │
│                  │
└──────────────────┘
                    
                    
                    
                    
                    `,
			}, {
				func() {
					assert.Equal(t, 4, b.Height(20))
					b.Resize(2, 2)
					assert.Equal(t, 4, b.Height(20))
				}, `
HE                  
RE                  
                    
                    
                    
                    
                    
                    
                    `,
			}, {
				func() {
					b.Resize(20, 9)
					writeBuffer(b, "hello world")
					assert.Equal(t, 4, b.Height(20))
				}, `
┌──────────────────┐
│hello world       │
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					writeBuffer(b, "hello worldhelloworldhelloworld")
					assert.Equal(t, 5, b.Height(20))
				}, `
┌──────────────────┐
│hello worldhello w│
│orldhelloworldhell│
│oworld            │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			}, {
				func() {
					// last row full feature is also capped by maxHeight
					writeBuffer(b, "xxxxxxxxxxxx")
					assert.Equal(t, 5, b.Height(20))
				}, `
┌──────────────────┐
│hello worldhello w│
│orldhelloworldhell│
│oworldxxxxxxxxxxxx│
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
			},
		}
		comptest.TestComponent(t, b, w, tests)
	})
}

func TestInsertIntegration(t *testing.T) {
	ed := standard.Editor()
	buf := cell.NewBuffer()
	b := input.NewBox(buf, ed, input.BoxConfig{MinHeight: 4, MaxHeight: 5, Placeholder: "HERE..."})
	b.Resize(3, 3)

	for _, r := range "hello world" {
		// simulate real usage
		b.Handle(term.Event{Type: term.EventKey, Ch: r})
		b.Height(3)
		b.Resize(3, 3)
		b.Draw(term.NoopWriter{})
	}
	assert.Equal(t, "hello world", buf.String())
}

// we could write to cell.Buffer directly
// but this is a more realistic way
func writeBuffer(b tui.Handler, str string) {
	for _, r := range str {
		b.Handle(term.Event{Type: term.EventKey, Ch: r})
		b.Draw(term.NewStringWriter(20, 9))
		// scroll needs to be drawn for wrap features to be correct
		// width and height here are not important
	}
}

func TestBoxPointerLeaves(t *testing.T) {
	for _, content := range []string{"", strings.Repeat("some typed input that wraps ", 20)} {
		for _, size := range [][2]int{{60, 15}, {1, 1}} {
			buf := cell.NewBuffer()
			buf.WriteString(content)
			b := input.NewBox(buf, standard.Editor(), input.BoxConfig{
				Placeholder: "type something here, it is a long placeholder",
			})
			handlertest.RunPointerLeaves(t, b, size[0], size[1])
			assert.Equal(t, content, buf.String(), "the pointer does not edit the input")
		}
	}
}
