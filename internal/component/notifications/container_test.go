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

package notifications

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	tcomponent "unstable.build/rune/internal/component"
)

func TestComponentDraw(t *testing.T) {
	inner := &component.TestComponent{Ch: 'X'}

	c := New(inner, Config{
		AutoClose:   1 * time.Hour,
		ProgressBar: true,
		Width:       10,
		ProgressRunes: ProgressRunes{
			Start:      '{',
			Current:    '-',
			CurrentTip: '>',
			Remain:     ' ',
			End:        '}',
		},
	})
	c.Resize(40, 15)

	w := term.NewStringWriter(40, 15)

	var id string
	tests := []comptest.TestCase{
		{
			nil, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() { c.Notify(LevelInfo, "hello world") }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ world  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() { c.Resize(2, 15) }, `
                                        
he                                      
ll                                      
o                                       
wo                                      
rl                                      
d                                       
{}                                      
XX                                      
XX                                      
XX                                      
XX                                      
XX                                      
XX                                      
XX                                      `,
		}, {
			func() { c.Resize(4, 15) }, `
┌──┐                                    
│he│                                    
│ll│                                    
│o │                                    
│wo│                                    
│rl│                                    
│d │                                    
{> }                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    `,
		}, {
			func() { c.Resize(40, 15) }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ world  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() { c.Notify(LevelInfo, "Does not fit I think") }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ Does   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ not    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit I  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ think  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ world  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() { c.Notify(LevelInfo, "This time it won't fit for sure") }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ for    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ sure   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ Does   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ not    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit I  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ think  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }`,
		}, {
			func() { c.Notify(LevelInfo, "This time it won't fit for SURE SURE") }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ for    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │`,
		}, {
			func() { c.Resize(20, 10) }, `
XXXXXXXXXX┌────────┐                    
XXXXXXXXXX│ This   │                    
XXXXXXXXXX│ time   │                    
XXXXXXXXXX│ it     │                    
XXXXXXXXXX│ won't  │                    
XXXXXXXXXX│ fit    │                    
XXXXXXXXXX│ for    │                    
XXXXXXXXXX│ SURE   │                    
XXXXXXXXXX│ SURE   │                    
XXXXXXXXXX{>       }                    
                                        
                                        
                                        
                                        
                                        `,
		}, {
			func() { c.Resize(40, 15) }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ for    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │`,
		}, {
			func() {
				c.PauseAll()
				c.ResumeAll()
				c.PauseAll()
				c.ResumeAll()
			}, /* simply tests data races and deadlocks */ `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ for    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ SURE   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ This   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ time   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ it     │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ won't  │`,
		}, {
			func() { c.CloseAll() }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				id := c.Notify(LevelInfo, "hello world")
				c.UpdateProgress(id, "hola mon", 0, 10)

			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hola   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ mon    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{        }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				id := c.Notify(LevelInfo, "hello world")
				c.UpdateProgress(id, "hola mon", 1, 10)

			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hola   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ mon    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				id := c.Notify(LevelInfo, "hello world")
				c.UpdateProgress(id, "hola mon", 8, 10)

			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hola   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ mon    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{------->}
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				id = c.Notify(LevelInfo, "hello world")
				c.UpdateProgress(id, "h0l4 m0n", 1, 10)
				c.UpdateProgress(id, "hola mon", 5, 10)
			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hola   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ mon    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{---->   }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				c.UpdateProgress(id, "hola mon boig", 6, 10)
			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hola   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ mon    │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ boig   │
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXX{----->  }
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		},
	}

	comptest.TestComponent(t, c, w, tests)
}

func TestComponentDrawWithPadding(t *testing.T) {
	inner := &component.TestComponent{Ch: 'X'}

	c := New(inner, Config{
		AutoClose:   1 * time.Hour,
		ProgressBar: true,
		Width:       10,
		Padding:     1,
		ProgressRunes: ProgressRunes{
			Start:      '{',
			Current:    '-',
			CurrentTip: '>',
			Remain:     ' ',
			End:        '}',
		},
	})
	c.Resize(40, 15)

	w := term.NewStringWriter(40, 15)

	var id string
	tests := []comptest.TestCase{
		{
			func() { id = c.Notify(LevelInfo, "hello world") }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ world  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() { c.Resize(2, 15) }, `
XX                                      
                                        
he                                      
ll                                      
o                                       
wo                                      
rl                                      
d                                       
{}                                      
XX                                      
XX                                      
XX                                      
XX                                      
XX                                      
XX                                      `,
		}, {
			func() { c.Resize(4, 15) }, `
XXXX                                    
┌──┐                                    
│he│                                    
│ll│                                    
│o │                                    
│wo│                                    
│rl│                                    
│d │                                    
{> }                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    
XXXX                                    `,
		}, {
			func() { c.Resize(40, 15) }, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ world  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		}, {
			func() {
				c.UpdateProgress(id, "Does not fit I think", 1, 10)
			}, `
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ Does   │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ not    │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ fit I  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX│ think  │X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
		},
	}

	comptest.TestComponent(t, c, w, tests)
}

func TestRightInsetKeepsNotificationsOutOfReservedColumn(t *testing.T) {
	inner := &component.TestComponent{Ch: 'X'}

	c := New(inner, Config{
		AutoClose:   1 * time.Hour,
		ProgressBar: true,
		Width:       10,
		RightInset:  5,
		ProgressRunes: ProgressRunes{
			Start:      '{',
			Current:    '-',
			CurrentTip: '>',
			Remain:     ' ',
			End:        '}',
		},
	})
	c.Resize(40, 6)

	w := term.NewStringWriter(40, 6)
	tests := []comptest.TestCase{{
		func() { c.Notify(LevelInfo, "hello world") }, `
XXXXXXXXXXXXXXXXXXXXXXXXX┌────────┐XXXXX
XXXXXXXXXXXXXXXXXXXXXXXXX│ hello  │XXXXX
XXXXXXXXXXXXXXXXXXXXXXXXX│ world  │XXXXX
XXXXXXXXXXXXXXXXXXXXXXXXX{>       }XXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`,
	}}

	comptest.TestComponent(t, c, w, tests)
}

func TestRightInsetWiderThanWindow(t *testing.T) {
	c := New(&component.TestComponent{Ch: 'X'}, Config{
		AutoClose:  1 * time.Hour,
		Width:      10,
		RightInset: 12,
	})
	c.Resize(8, 4)
	c.Notify(LevelInfo, "hi")

	w := term.NewStringWriter(8, 4)
	c.Draw(w)
	assert.NoError(t, w.Flush())
}

func TestNotificationID(t *testing.T) {
	t.Run("two notifications with the same message have the same ID", func(t *testing.T) {
		c := New(&component.TestComponent{}, Config{Width: 10, AutoClose: 10 * time.Second})
		assert.Equal(t, c.ID(LevelError, "test: 1"), c.ID(LevelWarn, "test: 1"))
	})
	t.Run("two notifications with different messages have different IDs", func(t *testing.T) {
		c := New(&component.TestComponent{}, Config{Width: 10, AutoClose: 10 * time.Second})
		assert.NotEqual(t, c.ID(LevelError, "test: 1"), c.ID(LevelError, "test: 2"))
	})
}

func TestUpdateProgressFindsShownNotificationsOnly(t *testing.T) {
	const width, height = 40, 15
	click := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: x, MouseY: y}
	}
	pin := func(c *Container, id string) { require.True(t, c.UpdateProgress(id, "", 1, 10)) }
	for _, tc := range []struct {
		name      string
		autoClose time.Duration
		do        func(c *Container, id string)
		shown     bool
	}{{
		name:      "closed by a click",
		autoClose: time.Hour,
		do: func(c *Container, _ string) {
			pos := c.vlist.Position()
			_, handled := c.Handle(click(pos.X+1, pos.Y+1))
			require.True(t, handled)
		},
	}, {
		name:      "expired",
		autoClose: 20 * time.Millisecond,
		do:        func(*Container, string) { time.Sleep(200 * time.Millisecond) },
	}, {
		name:      "pinned, then all resumed",
		autoClose: 20 * time.Millisecond,
		do: func(c *Container, id string) {
			pin(c, id)
			c.ResumeAll()
			time.Sleep(200 * time.Millisecond)
		},
		shown: true,
	}, {
		name:      "pinned, then a click outside resumes all",
		autoClose: 20 * time.Millisecond,
		do: func(c *Container, id string) {
			pin(c, id)
			c.Handle(click(0, height-1))
			time.Sleep(200 * time.Millisecond)
		},
		shown: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(&component.TestComponent{Ch: 'X'}, Config{AutoClose: tc.autoClose, Width: 10})
			t.Cleanup(func() { _ = c.Close() })
			c.Resize(width, height)
			id := c.Notify(LevelInfo, "hello")
			tc.do(c, id)
			c.mu.RLock()
			shown := c.vlist.C.Len()
			c.mu.RUnlock()
			assert.Equal(t, tc.shown, shown == 1)
			assert.Equal(t, tc.shown, c.UpdateProgress(id, "still there", 2, 10))
		})
	}
}

func TestComponentDrawGraphics(t *testing.T) {
	const width, height = 20, 4
	pics := newPictures(t, 'A', 'B', 'C')
	frame := component.FrameCharSetDefault()
	frame.Graphics = component.FrameCharSetGraphics{
		TopLeft: pics.uri('A'), TopRight: pics.uri('A'),
		BottomLeft: pics.uri('A'), BottomRight: pics.uri('A'),
		HorizontalTop: pics.uri('A'), HorizontalBottom: pics.uri('A'),
		VerticalLeft: pics.uri('A'), VerticalRight: pics.uri('A'),
	}
	progress := ProgressGraphics{
		Start: pics.uri('B'), Current: pics.uri('B'), CurrentTip: pics.uri('B'),
		Remain: pics.uri('C'), End: pics.uri('B'),
	}
	overlay := progress
	overlay.Overlay = true

	tests := []struct {
		name        string
		progressBar bool
		progress    ProgressGraphics
		noGraphics  bool
		expected    string
		// underneath is the text under the images, when it matters.
		underneath string
	}{
		{
			name: "frame images replace every border cell",
			expected: `
XXXXXXXXXXAAAAAAAAAA
XXXXXXXXXXA hi     A
XXXXXXXXXXAAAAAAAAAA
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			name:        "the progress bar covers the bottom frame images",
			progressBar: true,
			expected: `
XXXXXXXXXXAAAAAAAAAA
XXXXXXXXXXA hi     A
XXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			name:        "progress images replace the progress bar runes",
			progressBar: true,
			progress:    progress,
			expected: `
XXXXXXXXXXAAAAAAAAAA
XXXXXXXXXXA hi     A
XXXXXXXXXXBBCCCCCCCB
XXXXXXXXXXXXXXXXXXXX`,
			underneath: `
XXXXXXXXXX          
XXXXXXXXXX  hi      
XXXXXXXXXX          
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			name:        "overlay progress images keep the runes under them",
			progressBar: true,
			progress:    overlay,
			expected: `
XXXXXXXXXXAAAAAAAAAA
XXXXXXXXXXA hi     A
XXXXXXXXXXBBCCCCCCCB
XXXXXXXXXXXXXXXXXXXX`,
			underneath: `
XXXXXXXXXX          
XXXXXXXXXX  hi      
XXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			name:        "no graphics draws the runes alone",
			progressBar: true,
			progress:    progress,
			noGraphics:  true,
			expected: `
XXXXXXXXXX┌────────┐
XXXXXXXXXX│ hi     │
XXXXXXXXXX{>       }
XXXXXXXXXXXXXXXXXXXX`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				AutoClose:    time.Hour,
				Width:        10,
				Graphics:     pics.graphics,
				ProgressBar:  tt.progressBar,
				FrameCharSet: frame,
				ProgressRunes: ProgressRunes{
					Start: '{', Current: '-', CurrentTip: '>', Remain: ' ', End: '}',
					Graphics: tt.progress,
				},
			}
			if tt.noGraphics {
				cfg.Graphics = nil
			}
			c := New(&component.TestComponent{Ch: 'X'}, cfg)
			t.Cleanup(func() { _ = c.Close() })
			c.Resize(width, height)
			c.Notify(LevelInfo, "hi")

			s := newImageScreen(width, height)
			c.Draw(s)
			assert.Equal(t, tt.expected, s.render(t, pics))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}
		})
	}
}

func TestContainerRestyle(t *testing.T) {
	const width, height = 20, 9
	runes := ProgressRunes{Start: '{', Current: '-', CurrentTip: '>', Remain: ' ', End: '}'}
	hashes := component.FrameCharSet{
		TopLeft: '#', TopRight: '#', BottomLeft: '#', BottomRight: '#',
		HorizontalTop: '#', HorizontalBottom: '#', VerticalLeft: '#', VerticalRight: '#',
	}
	c := New(&component.TestComponent{Ch: 'X'}, Config{
		AutoClose:     time.Hour,
		Width:         10,
		ProgressRunes: runes,
	})
	t.Cleanup(func() { _ = c.Close() })
	c.Resize(width, height)

	comptest.TestComponent(t, c, term.NewStringWriter(width, height), []comptest.TestCase{
		{
			Action: func() { c.Notify(LevelInfo, "hi") },
			Expected: `
XXXXXXXXXX┌────────┐
XXXXXXXXXX│ hi     │
XXXXXXXXXX└────────┘
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			Action: func() {
				c.Restyle(Style{
					ProgressBar:   true,
					Padding:       1,
					FrameCharSet:  hashes,
					ProgressRunes: runes,
				})
			},
			Expected: `
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXX##########X
XXXXXXXXX# hi     #X
XXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			Action: func() { c.Notify(LevelInfo, "yo") },
			Expected: `
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXX##########X
XXXXXXXXX# yo     #X
XXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXX##########X
XXXXXXXXX# hi     #X
XXXXXXXXX{>       }X
XXXXXXXXXXXXXXXXXXXX`,
		},
		{
			Action: func() { c.Restyle(Style{}) },
			Expected: `
XXXXXXXXXX┌────────┐
XXXXXXXXXX│ yo     │
XXXXXXXXXX└────────┘
XXXXXXXXXX┌────────┐
XXXXXXXXXX│ hi     │
XXXXXXXXXX└────────┘
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX
XXXXXXXXXXXXXXXXXXXX`,
		},
	})
}

func TestContainerRestyleProgressBarRepaints(t *testing.T) {
	tests := []struct {
		name          string
		before, after bool
		pinned        bool
		wantRepaints  bool
	}{
		{name: "switching the progress bar on repaints it", after: true, wantRepaints: true},
		{name: "switching the progress bar off stops repainting", before: true},
		{name: "a pinned notification repaints on progress updates", after: true, pinned: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interruptCounter, 1)
			c := New(&component.TestComponent{}, Config{
				AutoClose:   time.Hour,
				Width:       10,
				Interrupter: irq,
				ProgressBar: tt.before,
			})
			t.Cleanup(func() { _ = c.Close() })
			c.Resize(20, 5)
			id := c.Notify(LevelInfo, "hi")
			if tt.pinned {
				require.True(t, c.UpdateProgress(id, "", 1, 10))
			}

			c.Restyle(Style{ProgressBar: tt.after})
			// Drop what the old ticker queued around the restyle.
			for drain := time.After(150 * time.Millisecond); ; {
				select {
				case <-irq:
					continue
				case <-drain:
				}
				break
			}
			select {
			case <-irq:
				assert.True(t, tt.wantRepaints, "unexpected repaint")
			case <-time.After(500 * time.Millisecond):
				assert.False(t, tt.wantRepaints, "no repaint")
			}
		})
	}
}

// pictures holds a one-pixel picture of its own color for each name,
// loaded into the FrameGraphics that graphics returns.
type pictures struct {
	colors map[rune]color.RGBA
	g      *tcomponent.FrameGraphics
}

func newPictures(t *testing.T, names ...rune) pictures {
	p := pictures{
		colors: make(map[rune]color.RGBA),
		g:      tcomponent.NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter()),
	}
	t.Cleanup(func() { assert.NoError(t, p.g.Close()) })
	images := make(map[string]image.Image)
	for i, name := range names {
		c := color.RGBA{R: uint8(i + 1), A: 0xff}
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.SetRGBA(0, 0, c)
		p.colors[name] = c
		images[p.uri(name)] = img
	}
	p.g.Reload(images)
	return p
}

func (p pictures) uri(name rune) string {
	return "file:///" + string(name) + ".png"
}

func (p pictures) graphics() *tcomponent.FrameGraphics {
	return p.g
}

// imageScreen is a string writer that records the images drawn into it.
type imageScreen struct {
	*term.StringWriter
	images []term.Image
}

func newImageScreen(width, height int) *imageScreen {
	return &imageScreen{StringWriter: term.NewStringWriter(width, height)}
}

func (s *imageScreen) DrawImage(img term.Image) bool {
	s.images = append(s.images, img)
	return true
}

// render returns the screen with the cells of each image replaced by the
// name of its picture.
func (s *imageScreen) render(t *testing.T, pics pictures) string {
	t.Helper()
	require.NoError(t, s.Flush())
	rows := strings.Split(s.String(), "\n")
	screen := make([][]rune, len(rows))
	for i, row := range rows {
		screen[i] = []rune(row)
	}
	for _, img := range s.images {
		b := img.Src.Bounds()
		name := '?'
		for n, c := range pics.colors {
			if img.Src.At(b.Min.X, b.Min.Y) == c {
				name = n
			}
		}
		for y := img.Pos.Y; y < img.Pos.Y+img.Height; y++ {
			for x := img.Pos.X; x < img.Pos.X+img.Width; x++ {
				screen[y][x] = name
			}
		}
	}
	var b strings.Builder
	for _, row := range screen {
		b.WriteByte('\n')
		b.WriteString(string(row))
	}
	return b.String()
}

// interruptCounter is a term.Interrupter that queues an interrupt while
// none is queued.
type interruptCounter chan struct{}

func (c interruptCounter) Interrupt(context.Context) error {
	select {
	case c <- struct{}{}:
	default:
	}
	return nil
}
