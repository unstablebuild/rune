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

package component

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestComponentScrollDrawEdgeCase(t *testing.T) {
	const sampleSnippet = `
/*
 * Ch@ek if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
} /* { */ `
	scroll := newScroll(4, false, 10, 10)
	scroll.Wrap = true
	scroll.Buffer().Reset()
	scroll.Buffer().WriteString(sampleSnippet)
	for range 15 {
		scroll.SeekDown()
	}

	w := term.NewStringWriter(10, 10)

	tests := []comptest.TestCase{
		{
			nil, `
 */       
    void  
diff_buf_a
djust(win_
T *win)   
{         
    win_T 
   *wp;   
    int   
     i;   `,
		},
	}

	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollDrawsRingBackedRowsInLogicalOrder(t *testing.T) {
	buf := new(cell.Buffer)
	buf.InitPerformance(4, 1, ' ')
	require.True(t, buf.AppendBlankRowsBounded(4, 1, 4))
	for y, ch := range "0123" {
		buf.MutableRow(y, 1)[0].Ch = ch
	}
	// Recycle twice so the physical row boundary lies between logical
	// rows 1 and 2.
	for _, ch := range "45" {
		require.True(t, buf.AppendBlankRowsBounded(1, 1, 4))
		buf.MutableRow(3, 1)[0].Ch = ch
	}

	scroll := new(Scroll)
	scroll.InitPerformance(buf)
	scroll.Resize(1, 3)
	scroll.InvertOffset = false
	scroll.SetOffset(term.Coordinates{Y: 1})
	w := term.NewStringWriter(1, 3)
	scroll.Draw(w)
	w.Flush()

	assert.Equal(t, "3\n4\n5", w.String())
}

func TestInitPerfWithHidden(t *testing.T) {
	s := new(Scroll)
	s.InitPerformance(cell.NewBuffer())
	s.Buffer().WriteString("abc\n")
	assert.NotPanics(t, func() {
		assert.False(t, s.MarkHidden(0, 3))
		assert.False(t, s.MarkVisible(0))
		s.Draw(term.NewStringWriter(10, 10))
		s.ScrollToWindowCoordinates(term.Coordinates{})
		s.WindowToScrollCoordinates(term.Coordinates{})
	})
}

func TestScrollCenterAt(t *testing.T) {
	scroll := newScroll(4, false, 24, 9)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(hiddenCopy))
	require.NoError(t, err)

	w := term.NewStringWriter(24, 9)

	tests := []comptest.TestCase{
		{
			nil, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
		{
			func() {
				assert.Equal(t, 0, scroll.RepositionLineCenter(0))
			}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
		{
			func() {
				assert.Equal(t, 6, scroll.RepositionLineCenter(10))
			}, `    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g`,
		},
		{
			func() {
				assert.Equal(t, -1, scroll.RepositionLineCenter(9))
				assert.Equal(t, -5, scroll.RepositionLineCenter(0))
				assert.Equal(t, 0, scroll.RepositionLineCenter(0))
			}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
		{
			func() {
				assert.Equal(t, 10, scroll.RepositionLineCenter(99))
			}, `
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
)                       
                        
replace this => that    
                        `,
		},
		{
			func() {
				assert.Equal(t, -10, scroll.RepositionLineCenter(-1 /* invalid but legal */))
			}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
	}
	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollDrawHidden(t *testing.T) {
	t.Run("via Mark* methods", func(t *testing.T) {
		scroll := newScroll(4, false, 24, 9)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(hiddenCopy))
		require.NoError(t, err)

		var calledOnHide, calledOnVisible int
		scroll.Subscribe(subscriber{
			expectOnHide: func(start, end int) {
				calledOnHide++
			},
			expectOnVisible: func(start int) {
				calledOnVisible++
			},
		})

		w := term.NewStringWriter(24, 9)

		tests := []comptest.TestCase{
			{
				nil, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.False(t, scroll.MarkHidden(2, 2))
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.False(t, scroll.MarkVisible(2))
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.True(t, scroll.MarkHidden(1, 2))
				}, `
module github.com/unstab
 [2 lines] go 1.14      
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.True(t, scroll.MarkVisible(1))
					assert.False(t, scroll.MarkVisible(1))
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.True(t, scroll.MarkHidden(4, 15))
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require ( [12 lines] )  
                        
replace this => that    
                        
                        `,
			},
			{
				func() {
					assert.True(t, scroll.SeekRight())
					assert.True(t, scroll.SeekRight())
				}, `
dule github.com/unstable
                        
 1.14                   
                        
quire ( [12 lines] )    
                        
place this => that      
                        
                        `,
			},
			{
				func() {
					for range 8 {
						assert.True(t, scroll.SeekRight())
					}
				}, `
hub.com/unstablebuild/bl
                        
                        
                        
[12 lines] )            
                        
is => that              
                        
                        `,
			},
			{
				func() {
					for range 4 {
						assert.True(t, scroll.SeekRight())
					}
				}, `
com/unstablebuild/blue  
                        
                        
                        
lines] )                
                        
> that                  
                        
                        `,
			},
			{
				func() {
					for range 10 {
						assert.True(t, scroll.SeekRight())
					}
				}, `
lebuild/blue            
                        
                        
                        
                        
                        
                        
                        
                        `,
			},
			{
				func() {
					for scroll.SeekRight() {
					}
				}, `
                        
                        
                        
                        
                        
                        
                        
                        
                        `,
			},
			{
				func() {
					require.True(t, scroll.SeekStartLine())
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require ( [12 lines] )  
                        
replace this => that    
                        
                        `,
			},
			{
				func() {
					require.False(t, scroll.SeekDown())
				}, `
module github.com/unstab
                        
go 1.14                 
                        
require ( [12 lines] )  
                        
replace this => that    
                        
                        `,
			},
			{
				/* trimming of start of line */
				func() {
					require.False(t, scroll.SeekStartFile())
					require.True(t, scroll.MarkVisible(4))
					require.True(t, scroll.MarkHidden(2, 5))
				}, `
module github.com/unstab
                        
go 1.14 [4 lines] cloud.
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo`,
			},
			{
				/* max offset is reduced while at max offset */
				func() {
					require.True(t, scroll.SeekEndFile())
					require.True(t, scroll.MarkHidden(15, 18))
				}, `
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
) [4 lines]             
                        
                        
                        `,
			},
			{
				func() {
					require.True(t, scroll.SeekEndFile())
				}, `
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
) [4 lines]             `,
			},
		}
		comptest.TestComponent(t, scroll, w, tests)
		assert.Equal(t, 4, calledOnHide)
		assert.Equal(t, 2, calledOnVisible)
	})
	t.Run("tabs are considered when placing [ Lines ]", func(t *testing.T) {
		scroll := newScroll(4, false, 60, 9)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(hiddenCopy))
		require.NoError(t, err)

		w := term.NewStringWriter(60, 9)

		tests := []comptest.TestCase{
			{
				nil, `
module github.com/unstablebuild/blue                        
                                                            
go 1.14                                                     
                                                            
require (                                                   
    cloud.google.com/go v0.63.0 // indirect                 
    cloud.google.com/go/firestore v1.2.0                    
    github.com/adrianmo/go-nmea v1.2.0                      
    github.com/ernestrc/go-multierror v1.1.2 // indirect    `,
			},
			{
				func() {
					assert.True(t, scroll.MarkHidden(5, 8))
				}, `
module github.com/unstablebuild/blue                        
                                                            
go 1.14                                                     
                                                            
require (                                                   
    cloud.google.com/go v0.63.0 // indirect [4 lines] github
    github.com/ernestrc/logd-go v0.0.0-20180509171507-65871c
    github.com/ernestrc/sensible v0.0.0-20170704153812-102a9
    github.com/golang/mock v1.4.4                           `,
			},
		}
		comptest.TestComponent(t, scroll, w, tests)
	})

	t.Run("clear hidden via edit", func(t *testing.T) {
		scroll := newScroll(4, false, 24, 9)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(hiddenCopy))
		require.NoError(t, err)

		var calledOnHide, calledOnVisible int
		scroll.Subscribe(subscriber{
			expectOnHide: func(start, end int) {
				calledOnHide++
			},
			expectOnVisible: func(start int) {
				calledOnVisible++
			},
		})

		w := term.NewStringWriter(24, 9)

		tests := []comptest.TestCase{
			{
				nil, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					assert.True(t, scroll.MarkHidden(1, 2))
				}, `
module github.com/unstab
 [2 lines] go 1.14      
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/`,
			},
			{
				func() {
					// replace start line before, ends in the middle of hidden
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 0, X: 10},
						term.Coordinates{Y: 1, X: 1},
						"blabla\nhell",
					)
				}, `
module gitblabla        
hell                    
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
			},
			{
				func() {
					require.True(t, scroll.MarkHidden(1, 2))
					// replace start line before, ends past it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 0, X: 10},
						term.Coordinates{Y: 4, X: 1},
						"blabla\nhell",
					)
				}, `
module gitblabla        
hellequire (            
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo`,
			},
			{
				func() {
					require.True(t, scroll.MarkHidden(1, 2))
					// replace start line in the middle, ends past it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 2, X: 0},
						term.Coordinates{Y: 4, X: 1},
						"blabla\nhell",
					)
				}, `
module gitblabla        
hellequire (            
blabla                  
hellgithub.com/adrianmo/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr`,
			},
			{
				func() {
					require.True(t, scroll.MarkHidden(1, 2))
					// replace start line before, ends past it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 0, X: 0},
						term.Coordinates{Y: 4, X: 1},
						"blabla\nhell",
					)
				}, `
blabla                  
hellgithub.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
)                       `,
			},
			{
				func() {
					require.True(t, scroll.MarkHidden(3, 5))
					// insert before moves it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 0, X: 0},
						term.Coordinates{Y: 0, X: 0},
						"X\n",
					)
				}, `
X                       
blabla                  
hellgithub.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/google/uu
    github.com/jacobsa/g
)                       
                        `,
			},
			{
				func() {
					// delete before moves it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 0, X: 0},
						term.Coordinates{Y: 1, X: 0},
						"",
					)
				}, `
blabla                  
hellgithub.com/ernestrc/
    github.com/ernestrc/
    github.com/ernestrc/
    github.com/google/uu
    github.com/jacobsa/g
)                       
                        
replace this => that    `,
			},
			{
				func() {
					// insert in the middle clears it
					scroll.Buffer().Edit(context.Background(),
						term.Coordinates{Y: 3, X: 0},
						term.Coordinates{Y: 3, X: 0},
						"X\n",
					)
				}, `
blabla                  
hellgithub.com/ernestrc/
    github.com/ernestrc/
X                       
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g`,
			},
		}
		comptest.TestComponent(t, scroll, w, tests)
		assert.Equal(t, 5, calledOnHide)
		assert.Equal(t, 5, calledOnVisible)
	})
}

func TestScrollDrawInverseOffset(t *testing.T) {
	scroll := newScroll(4, false, 24, 9)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(hiddenCopy))
	require.NoError(t, err)
	scroll.InvertOffset = true

	w := term.NewStringWriter(24, 9)

	tests := []comptest.TestCase{
		{
			nil, `
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
)                       
                        
replace this => that    
                        `,
		},
		{
			func() {
				assert.True(t, scroll.SeekRight())
				assert.True(t, scroll.SeekRight())
			}, `
  github.com/ernestrc/se
  github.com/golang/mock
  github.com/golang/prot
  github.com/google/uuid
  github.com/jacobsa/go-
                        
                        
place this => that      
                        `,
		},
		{
			func() {
				assert.True(t, scroll.SeekStartLine())
				assert.True(t, scroll.SeekStartFile())
			}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
		{
			func() {
				assert.True(t, scroll.SeekEndFile())
			}, `
    github.com/ernestrc/
    github.com/golang/mo
    github.com/golang/pr
    github.com/google/uu
    github.com/jacobsa/g
)                       
                        
replace this => that    
                        `,
		},
		{
			func() {
				scroll.InvertOffset = false
			}, `
module github.com/unstab
                        
go 1.14                 
                        
require (               
    cloud.google.com/go 
    cloud.google.com/go/
    github.com/adrianmo/
    github.com/ernestrc/`,
		},
		{
			func() {
				scroll.InvertOffset = true
				scroll.Buffer().Reset()
				scroll.Buffer().ReadFrom(strings.NewReader("one liner"))
				scroll.SetOffset(term.Coordinates{})
			}, `
                        
                        
                        
                        
                        
                        
                        
                        
one liner               `,
		},
		{
			func() {
				scroll.InvertOffset = false
			}, `
one liner               
                        
                        
                        
                        
                        
                        
                        
                        `,
		},
	}
	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollNew(t *testing.T) {
	scroll := newScroll(5, true, 100, 100)
	assert.True(t, scroll.Wrap)
	assert.Equal(t, 100, scroll.width)
	assert.Equal(t, 100, scroll.height)
}

func TestScrollWordAt(t *testing.T) {
	var fortune = `Love in your heart wasn't put there to stay.
Love isn't love 'til you give it_away.
		-- Oscar Hammerstein ⌘⌘
`
	scroll := newScroll(4, false, 100, 100)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)

	tsuite := []struct {
		in      term.Coordinates
		wantOut string
	}{
		{term.Coordinates{}, "Love"},
		{term.Coordinates{X: 4}, ""},
		{term.Coordinates{X: 6}, "in"},
		{term.Coordinates{X: 7}, ""},
		{term.Coordinates{X: 8}, "your"},
		{term.Coordinates{X: 23}, ""},
		{term.Coordinates{X: 39}, "stay"},
		{term.Coordinates{X: 43}, ""},
		{term.Coordinates{Y: 1}, "Love"},
		{term.Coordinates{Y: 2, X: 11}, "Hammerstein"},
		{term.Coordinates{X: 999}, ""},
		{term.Coordinates{X: -1}, ""},
		{term.Coordinates{Y: 5}, ""},
		{term.Coordinates{Y: 1, X: 32}, "it_away"},
	}

	for i, tcase := range tsuite {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			start, end, out := scroll.WordAt(tcase.in)
			assert.Equal(t, tcase.wantOut, out)
			// start, end used in a select statement should return
			// return string
			if out != "" {
				cells, _, ok := scroll.Buffer().Select(start, end)
				require.True(t, ok)
				assert.Equal(t, tcase.wantOut, term.CellsToString(cells))
			}
		})
	}
}

func TestScrollDraw(t *testing.T) {
	width, height := 8, 2
	tabspaces := 4
	wrap := false
	scroll := newScroll(tabspaces, wrap, width, height)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)

	var dispatchedSubscribe int
	var prevAt term.Coordinates
	scroll.Subscribe(FuncScrollSubscriber(func(from, to term.Coordinates) {
		dispatchedSubscribe++
		require.Equal(t, prevAt, from, scroll.Buffer().String())
		prevAt = to
	}))

	w := term.NewStringWriter(width, height)

	tests := []comptest.TestCase{
		{nil, "Love in \nLove isn"},
		{func() { assert.False(t, scroll.SeekUp()) }, "Love in \nLove isn"},
		{func() { assert.False(t, scroll.SeekLeft()) }, "Love in \nLove isn"},
		{func() { assert.True(t, scroll.SeekRight()) }, "ove in y\nove isn'"},
		{func() { assert.True(t, scroll.SeekLeft()) }, "Love in \nLove isn"},
		{func() { assert.True(t, scroll.SeekDown()) }, "Love isn\n        "},
		{func() { assert.False(t, scroll.SeekDown()) }, "Love isn\n        "},
		{func() { assert.True(t, scroll.SeekRight()) }, "ove isn'\n       -"},
		{func() { assert.True(t, scroll.SeekStartFile()) }, "ove in y\nove isn'"},
		{func() { assert.True(t, scroll.SeekEndFile()) }, "ove isn'\n       -"},
		{func() { assert.True(t, scroll.SeekStartLine()) }, "Love isn\n        "},
		// 11
		{func() { assert.True(t, scroll.SeekStartFile()) }, "Love in \nLove isn"},
		{func() { scroll.Search("Love") }, "Love in \nLove isn"},
		{func() { assert.False(t, scroll.SeekNextResult()) }, "Love in \nLove isn"},
		{func() { scroll.Resize(20, 1); w = term.NewStringWriter(20, 1) }, "Love in your heart w"},
		{func() { scroll.Search("you") }, "Love in your heart w"},
		{func() { assert.False(t, scroll.SeekNextResult()) }, "Love in your heart w"},
		{func() { assert.True(t, scroll.SeekNextResult()) }, " isn't love 'til you"},
		{func() { assert.True(t, scroll.SeekPrevResult()) }, " in your heart wasn'"},
		{func() { assert.Equal(t, 1, scroll.Search("⌘⌘")); assert.True(t, scroll.SeekNextResult()) }, "Oscar Hammerstein ⌘⌘"},
		{func() { assert.Equal(t, 2, scroll.Search("⌘")); assert.False(t, scroll.SeekNextResult()) }, "Oscar Hammerstein ⌘⌘"},
		// 21
		{func() { scroll.Search("Oscar"); assert.False(t, scroll.SeekNextResult()) }, "Oscar Hammerstein ⌘⌘"},
		{func() { assert.True(t, scroll.SeekStartFile()); assert.True(t, scroll.SeekStartLine()) }, "Love in your heart w"},
		{func() { scroll.Buffer().DeleteCell(term.Coordinates{X: 0, Y: 0}) }, "ove in your heart wa"},
		{func() { scroll.Buffer().DeleteCell(term.Coordinates{X: 14, Y: 0}) }, "ove in your hert was"},
		{func() { assert.True(t, scroll.SeekDown()) }, "Love isn't love 'til"},
		{func() { scroll.Buffer().DeleteCell(term.Coordinates{X: 16, Y: 1}) }, "Love isn't love til "},
		// note that there's a "space" after 中 that's because scroll skips drawing the second cell
		// in the double cell rune.
		{func() { scroll.Buffer().Insert(term.Coordinates{X: 16, Y: 1}, '中') }, "Love isn't love 中 ti"},
		{func() {
			buf := cell.NewBuffer()
			buf.WriteString("aa\nbb\ncc")
			scroll.searcher.Reset()
			scroll.offset = term.Coordinates{Y: 1}
			scroll.searchText = nil
			scroll.initBuffer(buf)
		}, "bb                  "},
		{func() {
			assert.Equal(t, 1, scroll.Search("aa"))
			assert.True(t, scroll.SeekNextResult())
		}, "aa                  "},
		{func() {
			assert.True(t, scroll.SetOffset(term.Coordinates{Y: 1}))
		}, "bb                  "},
		// {scroll.SeekDown, "        -- Oscar Ham"},
		// {scroll.SeekEndLine, "rstein 中            "},
		// {func() { scroll.Insert(term.Coordinates{X: 20, Y: 2}, '中') }, "rstein 中中           "},
	}

	for i, tcase := range tests {
		w.Clear(term.Attributes{})
		if tcase.Action != nil {
			tcase.Action()
		}

		scroll.Draw(w)

		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, tcase.Expected, w.String(), "test case %d", i)
	}

	assert.Equal(t, 18, dispatchedSubscribe)
}

func TestScrollDrawWrap(t *testing.T) {
	width, height := 8, 2
	tabspaces := 4
	wrap := true
	scroll := newScroll(tabspaces, wrap, width, height)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)

	w := term.NewStringWriter(width, height)

	tests := []comptest.TestCase{
		{nil, "Love in \nyour hea"},
		{func() { scroll.SeekUp() }, "Love in \nyour hea"},
		{func() { scroll.SeekLeft() }, "Love in \nyour hea"},
		{func() { scroll.SeekRight() }, "Love in \nyour hea"},
		{func() { scroll.SeekLeft() }, "Love in \nyour hea"},
		{func() { scroll.SeekDown() }, "your hea\nrt wasn'"},
		{func() { scroll.SeekDown() }, "rt wasn'\nt put th"},
		{func() { scroll.SeekUp() }, "your hea\nrt wasn'"},
		{func() { scroll.SeekRight() }, "your hea\nrt wasn'"},
		{func() { scroll.SeekStartFile() }, "Love in \nyour hea"},
		{func() { scroll.SeekEndFile() }, " Hammers\ntein ⌘⌘ "},
		{func() { scroll.SeekStartLine() }, " Hammers\ntein ⌘⌘ "},
		{func() { scroll.SeekStartFile() }, "Love in \nyour hea"},
		{func() { scroll.Search("Love") }, "Love in \nyour hea"},
		{func() { scroll.SeekNextResult() }, "Love in \nyour hea"},
		{func() { scroll.Resize(20, 1); w.Resize(20, 1) }, "Love in your heart w"},
		{func() { scroll.Search("you") }, "Love in your heart w"},
		{func() { scroll.SeekNextResult() }, "Love in your heart w"},
	}

	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollDrawWrap2(t *testing.T) {
	width, height := 8, 1
	tabspaces := 4
	wrap := true
	scroll := newScroll(tabspaces, wrap, width, height)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)

	w := term.NewStringWriter(width, height)

	tests := []comptest.TestCase{
		{nil, "Love in "},
	}

	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollDraw3(t *testing.T) {
	width, height := 51, 17
	scroll, w := newScrollWrapTestCase(t, width, height)

	tests := []comptest.TestCase{
		{nil, `module github.com/unstablebuild/blue               
                                                   
go 1.14                                            
                                                   
require (                                          
    cloud.google.com/go v0.63.0 // indirect        
    cloud.google.com/go/firestore v1.2.0           
    github.com/adrianmo/go-nmea v1.2.0             
    github.com/ernestrc/go-multierror v1.1.2 // ind
irect                                              
    github.com/ernestrc/logd-go v0.0.0-201805091715
07-65871c1d5504                                    
    github.com/ernestrc/sensible v0.0.0-20170704153
812-102a955adfdf                                   
    github.com/golang/mock v1.4.4                  
    github.com/golang/protobuf v1.4.2              
    github.com/google/uuid v1.1.1                  `},
		{func() { assert.True(t, scroll.SeekDown()) }, `                                                   
go 1.14                                            
                                                   
require (                                          
    cloud.google.com/go v0.63.0 // indirect        
    cloud.google.com/go/firestore v1.2.0           
    github.com/adrianmo/go-nmea v1.2.0             
    github.com/ernestrc/go-multierror v1.1.2 // ind
irect                                              
    github.com/ernestrc/logd-go v0.0.0-201805091715
07-65871c1d5504                                    
    github.com/ernestrc/sensible v0.0.0-20170704153
812-102a955adfdf                                   
    github.com/golang/mock v1.4.4                  
    github.com/golang/protobuf v1.4.2              
    github.com/google/uuid v1.1.1                  
    github.com/jacobsa/go-serial v0.0.0-20180131005`},
	}

	comptest.TestComponent(t, scroll, w, tests)
}

func TestScrollDrawInvalidSize(t *testing.T) {
	constructors := []func(*testing.T, int, int) (*Scroll, *term.StringWriter){
		newScrollWrapTestCase, func(t *testing.T, width, height int) (*Scroll, *term.StringWriter) {
			w := term.NewStringWriter(width, height)
			return newScroll(4, false, width, height), w
		},
	}
	for i, constructor := range constructors {
		t.Run(fmt.Sprintf("%d: negative width is considered as 0", i),
			func(t *testing.T) {
				width, height := 10, 1
				scroll, w := constructor(t, width, height)
				scroll.Resize(-22, 1)

				tests := []comptest.TestCase{
					{nil, `          `},
				}
				comptest.TestComponent(t, scroll, w, tests)
			})
		t.Run(fmt.Sprintf("%d: negative height is considered as 0", i),
			func(t *testing.T) {
				width, height := 10, 1
				scroll, w := constructor(t, width, height)
				scroll.Resize(10, -1)

				tests := []comptest.TestCase{
					{nil, `          `},
				}
				comptest.TestComponent(t, scroll, w, tests)
			})
	}

}

func TestScrollWraps(t *testing.T) {

	t.Run("returns empty map if Draw has not been called yet", func(t *testing.T) {
		scroll, _ := newScrollWrapTestCase(t, 10, 10)
		assert.Zero(t, nil, scroll.Wraps())
	})
	t.Run("returns wrapped lines with at most 1 wrap", func(t *testing.T) {
		scroll, w := newScrollWrapTestCase(t, 51, 17)
		expected := []int{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0, 0, 1, 0}
		scroll.Draw(w)
		assert.Equal(t, expected, scroll.Wraps())
	})
	t.Run("returns wrapped lines with more than 1 wrap", func(t *testing.T) {
		scroll, w := newScrollWrapTestCase(t, 20, 20)
		expected := []int{1, 0, 0, 0, 0, 2, 1, 1, 2, 3, 3, 1, 1, 1, 3, 0}
		scroll.Draw(w)
		assert.Equal(t, expected, scroll.Wraps())
	})
}

func TestScrollHeightNoWrap(t *testing.T) {
	scroll := newScroll(4, false, 20, 1)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)
	for _, width := range []int{0, 1, 10, 100} {
		assert.Equal(t, 3, scroll.Height(width))
	}
}

func TestScrollHeightWrap(t *testing.T) {
	scroll := newScroll(4, true, 20, 1)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)
	assert.Equal(t, 0, scroll.Height(0))
	assert.Equal(t, 13, scroll.Height(10))
	assert.Equal(t, 3, scroll.Height(100))
}

func TestScrollSetOffset(t *testing.T) {
	scroll := newScroll(4, false, 20, 1)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)
	assert.True(t, scroll.SetOffset(term.Coordinates{Y: 100}))
	assert.Equal(t, term.Coordinates{Y: 100}, scroll.Offset())

	assert.True(t, scroll.SetOffset(term.Coordinates{Y: 2}))
	assert.Equal(t, term.Coordinates{Y: scroll.Buffer().Rows() - 1}, scroll.Offset())
}

func TestScrollDrawOffsetOOB(t *testing.T) {
	scroll := newScroll(4, false, 20, 1)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)
	assert.True(t, scroll.SeekEndFile())

	// modify buffer such that cells now are empty
	assert.True(t, scroll.Buffer().TruncateFrom(term.Coordinates{}))

	w := term.NewStringWriter(10, 10)
	assert.NotPanics(t, func() {
		// this should not panic
		scroll.Draw(w)
	})
}

func TestScrollClampOffset(t *testing.T) {
	t.Run("no-op when already in range", func(t *testing.T) {
		scroll := newScroll(4, false, 20, 1)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
		require.NoError(t, err)
		require.True(t, scroll.SetOffset(term.Coordinates{Y: 2}))

		assert.False(t, scroll.ClampOffset())
		assert.Equal(t, term.Coordinates{Y: 2}, scroll.Offset())
	})

	t.Run("clamps an offset past the max down to the max", func(t *testing.T) {
		scroll := newScroll(4, false, 20, 1)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
		require.NoError(t, err)
		// SetOffset does not clamp, so this leaves the offset OOB, as a
		// resize that shrinks the content would.
		require.True(t, scroll.SetOffset(term.Coordinates{Y: 100, X: 100}))

		assert.True(t, scroll.ClampOffset())
		assert.Equal(t, scroll.MaxOffset(), scroll.Offset())
	})

	t.Run("clamps against the inverted max", func(t *testing.T) {
		scroll := newScroll(4, false, 20, 1)
		scroll.InvertOffset = true
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
		require.NoError(t, err)
		require.True(t, scroll.SetOffset(term.Coordinates{Y: 100}))

		assert.True(t, scroll.ClampOffset())
		assert.Equal(t, scroll.MaxOffset().Y, scroll.Offset().Y)
	})

	t.Run("does not notify subscribers", func(t *testing.T) {
		scroll := newScroll(4, false, 20, 1)
		_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
		require.NoError(t, err)
		require.True(t, scroll.SetOffset(term.Coordinates{Y: 100}))

		seeks := 0
		scroll.Subscribe(FuncScrollSubscriber(func(from, to term.Coordinates) {
			seeks++
		}))
		assert.True(t, scroll.ClampOffset())
		assert.Zero(t, seeks, "ClampOffset must not dispatch seek events")
	})
}

func TestScrollDrawWrapZeroWidth(t *testing.T) {
	scroll := newScroll(4, true, 0, 0)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	require.NoError(t, err)
	w := term.NewStringWriter(10, 10)
	assert.NotPanics(t, func() {
		scroll.Draw(w)
	})
}

func TestScrollDrawWrapWithBufferUpdates(t *testing.T) {
	buf := cell.NewBuffer()
	b := NewScroll(buf)
	b.Wrap = true
	b.Resize(20, 4)

	w := term.NewStringWriter(20, 9)

	tests := []comptest.TestCase{
		{
			nil, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 1) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() { b.Resize(20, 9) }, `
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString("hello world")
				b.Resize(20, 1)
			}, `
hello world         
                    
                    
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Resize(20, 9)
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 6)
			}, `
hello world. Let's t
est its responsivene
ss                  
                    
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 8)
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness       
                    
                    
                    
                    
                    `,
		}, {
			func() {
				b.Buffer().WriteString(". Let's test its responsiveness")
				b.Resize(20, 9)
				b.Buffer().WriteString(". Let's test its scrolling. " +
					"Let's make it overflow below and wrap," +
					"which might just take a little bit of text.")
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh`,
		}, {
			func() {
				// NOTE: seek ops do not detect wraps without a call to draw
				// between writing new data and attempting to seek.
				handled := b.SeekDown()
				require.True(t, handled)
				handled = b.SeekDown()
				require.True(t, handled)
			}, `
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh
t just take a little
 bit of text.       `,
		}, {
			func() {
				handled := b.SeekUp()
				require.True(t, handled)
				handled = b.SeekUp()
				require.True(t, handled)
			}, `
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below
 and wrap,which migh`,
		}, {
			func() {
				assert.Equal(t, 4, b.Search("test"))
				// shift all lines lower, coud cause a panic if not careful
				b.Buffer().Insert(term.Coordinates{Y: 0}, '\n')
			}, `
                    
hello world. Let's t
est its responsivene
ss. Let's test its r
esponsiveness. Let's
 test its responsive
ness. Let's test its
 scrolling. Let's ma
ke it overflow below`,
		},
	}
	comptest.TestComponent(t, b, w, tests)
}

func TestScrollToWindowCoordinates(t *testing.T) {
	suite := []struct {
		description string
		inScroll    func(t *testing.T) *Scroll
		scroll      term.Coordinates
		window      term.Coordinates
		expectOk    bool
	}{
		{"no wrap, no offset, within view bounds, start of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{}, term.Coordinates{}, true},
		{"no wrap, no offset, within view bounds, end of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 4, X: 13}, true},
		{"no wrap, no offset, within view bounds, past end of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 5, X: 1}, true},
		{"no wrap, no offset, within view bounds, past end of one line",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 0, X: 100}, true},
		{"no wrap, no offset, outside view bounds, end of file",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 4, X: 13}, true},
		{"no wrap, no offset, outside view bounds, past end of file",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 5, X: 1}, true},
		{"no wrap, no offset, outside view bounds, past end of one line",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 0, X: 100}, true},
		{"no wrap, with offset, outside view bounds, end of file",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 3, X: 12}, true},
		{"no wrap, with offset, outside view bounds, past end of file",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 4, X: 0}, true},
		{"no wrap, with offset, outside view bounds, past end of one line",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: -1, X: 99}, true},

		{"wrap, no offset, within view bounds, start of file",
			makeScroll(true, 100, 100, 0, 0), term.Coordinates{}, term.Coordinates{}, true},
		{"wrap, no offset, within view bounds, end of file",
			makeScroll(true, 100, 100, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 4, X: 13}, true},
		{"wrap, no offset, within view bounds, past end of file",
			makeScroll(true, 100, 100, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 5, X: 1}, true},
		{"wrap, no offset, within view bounds, past end of one line",
			makeScroll(true, 100, 100, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 0, X: 100}, true},

		{"wrap, no offset, outside view bounds, end of file",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 7, X: 3}, true},
		{"wrap, no offset, outside view bounds, past end of file",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 8, X: 1}, true},
		{"wrap, no offset, outside view bounds, past end of one line",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 1, X: 90}, true},

		{"wrap, with offset, outside view bounds, end of file",
			makeScroll(true, 10, 3, 1, 1), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 6, X: 2}, true},
		{"wrap, with offset, outside view bounds, past end of file",
			makeScroll(true, 10, 3, 1, 1), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 7, X: 0}, true},
		{"wrap, with offset, outside view bounds, past end of one line",
			makeScroll(true, 10, 3, 1, 1), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 0, X: 89}, true},

		{"wrap, no offset, outside view bounds, line in the middle, at the end of line",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 2, X: 13}, term.Coordinates{Y: 4, X: 3}, true},
		{"wrap, no offset, outside view bounds, line in the middle, past the end of file, would wrap, past end of one line",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 5, X: 13}, term.Coordinates{Y: 8, X: 13}, true},
		{"no wrap, end of file offset, first line",
			makeScroll(false, 10, 3, 2, 0), term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: -2, X: 0}, true},
		{"wrap, end of file offset, first line",
			makeScroll(true, 10, 3, 5, 0), term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: -5, X: 0}, true},
		{"wrap, past end of file offset, first line",
			makeScroll(true, 10, 3, 6, 1), term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: -6, X: -1}, true},
		{"wrap, halfway through line offset, first line",
			makeScroll(true, 10, 3, 4, 0), term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: -4, X: 0}, true},
		{"wrap, (2nd) halfway through line offset, first line",
			makeScroll(true, 10, 3, 3, 0), term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: -3, X: 0}, true},
		{"3rd wrap, halfway through line offset, negative window pos",
			makeScroll(true, 10, 3, 2, 0), term.Coordinates{Y: 0, X: 10}, term.Coordinates{Y: -1, X: 0}, true},
		{"hidden lines, no offset, position before hidden lines",
			makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{0, 1}, startEndBlock{4, 5}),
			term.Coordinates{Y: 0, X: 0}, term.Coordinates{Y: 0, X: 0}, true},
		{"hidden lines, no offset, position inside hidden block",
			makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15}),
			term.Coordinates{Y: 5, X: 5}, term.Coordinates{Y: 4, X: 0}, false},
		{"hidden lines, no offset, position after hidden blocks",
			makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15}),
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true},
		{"hidden lines, with offset, position after hidden blocks",
			makeScrollWithHiddenLines(false, 10, 3, 1, 1, startEndBlock{4, 15}),
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 4, X: 4}, true},
		{"hidden lines, with offset, position after multiple hidden blocks",
			makeScrollWithHiddenLines(false, 10, 3, 1, 1, startEndBlock{4, 10}, startEndBlock{11, 15}),
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 4}, true},
		{"hidden lines, no offset, position after hidden block, after insert line above",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().Insert(term.Coordinates{}, '\n')
				return scroll
			},
			term.Coordinates{Y: 5, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, after insert line above",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().Insert(term.Coordinates{}, '\n')
				return scroll
			},
			term.Coordinates{Y: 17, X: 5}, term.Coordinates{Y: 6, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, after insert line below",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().Insert(term.Coordinates{Y: 17}, '\n')
				return scroll
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, after remove line above",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().DeleteLine(term.Coordinates{}, term.Coordinates{})
				return scroll
			},
			term.Coordinates{Y: 15, X: 5}, term.Coordinates{Y: 4, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, after remove line below",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().DeleteLine(term.Coordinates{Y: 17}, term.Coordinates{Y: 17})
				return scroll
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, remove start of hidden block clears it",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().DeleteLine(term.Coordinates{Y: 4}, term.Coordinates{Y: 4})
				return scroll
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 16, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, remove end of hidden block clears it",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().DeleteLine(term.Coordinates{Y: 12}, term.Coordinates{Y: 15})
				return scroll
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 16, X: 5}, true,
		},
		{"hidden lines, no offset, position after hidden block, replace before hidden block",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().Edit(context.Background(),
					term.Coordinates{Y: 0}, term.Coordinates{Y: 2}, "\n\n")
				return scroll
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
		{"nested hidden lines cancel out, latest one sticks, no offset, position after hidden block",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0,
					startEndBlock{6, 10},
					startEndBlock{8, 9},
					startEndBlock{4, 15},
				)
				return fn(t)
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
		{"nested hidden lines cancel out, biggest one sticks, no offset, position after hidden block",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0,
					startEndBlock{4, 15},
					startEndBlock{6, 10},
					startEndBlock{8, 9},
				)
				return fn(t)
			},
			term.Coordinates{Y: 16, X: 5}, term.Coordinates{Y: 5, X: 5}, true,
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			actual, actualOk := test.inScroll(t).ScrollToWindowCoordinates(test.scroll)
			require.Equal(t, test.expectOk, actualOk)
			require.Equal(t, test.window, actual, "scroll to window")
			if strings.Contains(test.description, "inside hidden block") {
				return
			}
			assert.Equal(t, test.scroll,
				test.inScroll(t).WindowToScrollCoordinates(test.window), "window to scroll")
		})
	}
}

func TestWindowCoordinatesToScrollCoordinatesWrapLastLine(t *testing.T) {
	suite := []struct {
		yoffset int
		xoffset int
		wpos    term.Coordinates
		spos    term.Coordinates
	}{
		{0, 0, term.Coordinates{Y: 5, X: 9}, term.Coordinates{Y: 3, X: 9}},
		{0, 0, term.Coordinates{Y: 6, X: 0}, term.Coordinates{Y: 4, X: 0}},
		{0, 0, term.Coordinates{Y: 7, X: 0}, term.Coordinates{Y: 4, X: 10}},
		{0, 0, term.Coordinates{Y: 8, X: 0}, term.Coordinates{Y: 5, X: 0}},
		{0, 0, term.Coordinates{Y: 9, X: 0}, term.Coordinates{Y: 6, X: 0}},
		{0, 0, term.Coordinates{Y: 10, X: 0}, term.Coordinates{Y: 7, X: 0}},

		{1, 1, term.Coordinates{Y: 4, X: 8}, term.Coordinates{Y: 3, X: 9}},
		{1, 1, term.Coordinates{Y: 5, X: -1}, term.Coordinates{Y: 4, X: 0}},
		{1, 1, term.Coordinates{Y: 6, X: -1}, term.Coordinates{Y: 4, X: 10}},
		{1, 1, term.Coordinates{Y: 7, X: -1}, term.Coordinates{Y: 5, X: 0}},
		{1, 1, term.Coordinates{Y: 8, X: -1}, term.Coordinates{Y: 6, X: 0}},
		{1, 1, term.Coordinates{Y: 9, X: -1}, term.Coordinates{Y: 7, X: 0}},
	}

	for i, test := range suite {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			scroll := makeScroll(true, 10, 3, test.yoffset, test.xoffset)(t)
			actual := scroll.WindowToScrollCoordinates(test.wpos)
			assert.Equal(t, test.spos, actual)
		})
	}
}

func TestScrollToWindowCoordinatesInverseOffset(t *testing.T) {
	suite := []struct {
		description string
		inScroll    func(t *testing.T) *Scroll
		scroll      term.Coordinates
		window      term.Coordinates
		expectOk    bool
	}{
		{"no wrap, no offset, within view bounds, start of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{}, term.Coordinates{Y: 95}, true},
		{"no wrap, no offset, within view bounds, end of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 99, X: 13}, true},
		{"no wrap, no offset, within view bounds, past end of file",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 100, X: 1}, true},
		{"no wrap, no offset, within view bounds, past end of one line",
			makeScroll(false, 100, 100, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: 95, X: 100}, true},
		{"no wrap, no offset, outside view bounds, end of file",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 2, X: 13}, true},
		{"no wrap, no offset, outside view bounds, past end of file",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 3, X: 1}, true},
		{"no wrap, no offset, outside view bounds, past end of one line",
			makeScroll(false, 10, 3, 0, 0), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: -2, X: 100}, true},
		{"no wrap, with offset, outside view bounds, end of file",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 3, X: 12}, true},
		{"no wrap, with offset, outside view bounds, past end of file",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 5, X: 1}, term.Coordinates{Y: 4, X: 0}, true},
		{"no wrap, with offset, outside view bounds, past end of one line",
			makeScroll(false, 10, 3, 1, 1), term.Coordinates{Y: 0, X: 100}, term.Coordinates{Y: -1, X: 99}, true},
		{"wrap is ignored",
			makeScroll(true, 10, 3, 0, 0), term.Coordinates{Y: 4, X: 13}, term.Coordinates{Y: 5, X: 3}, true},
		{"hidden lines are ignored",
			func(t *testing.T) *Scroll {
				fn := makeScrollWithHiddenLines(false, 10, 3, 0, 0, startEndBlock{4, 15})
				scroll := fn(t)
				scroll.Buffer().Insert(term.Coordinates{}, '\n')
				return scroll
			},
			term.Coordinates{Y: 17, X: 5}, term.Coordinates{Y: -8, X: 5}, true,
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			scroll := test.inScroll(t)
			scroll.InvertOffset = true
			actual, actualOk := scroll.ScrollToWindowCoordinates(test.scroll)
			require.Equal(t, test.expectOk, actualOk)
			require.Equal(t, test.window, actual, "scroll to window")
			if strings.Contains(test.description, "inside hidden block") {
				return
			}
			assert.Equal(t, test.scroll,
				scroll.WindowToScrollCoordinates(test.window), "window to scroll")
		})
	}
}

func TestScrollDrawSearchResults(t *testing.T) {
	width, height := 51, 17
	scroll, sw := newScrollWrapTestCase(t, width, height)

	w := searchResultsWriter{StringWriter: sw, scroll: scroll}

	scroll.RecalculateWraps()

	tests := []comptest.TestCase{
		{func() {
			assert.Equal(t, 9, scroll.Search("github"))
		}, `       github                                      
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   
    github                                         
    github                                         
                                                   
    github                                         
                                                   
    github                                         
                                                   
    github                                         
    github                                         
    github                                         `,
		},
		{func() {
			assert.True(t, scroll.SeekDown())
		}, `                                                   
                                                   
                                                   
                                                   
                                                   
                                                   
    github                                         
    github                                         
                                                   
    github                                         
                                                   
    github                                         
                                                   
    github                                         
    github                                         
    github                                         
    github                                         `,
		},
		{func() {
			assert.Equal(t, 2, scroll.Search("indirect"))
		}, `                                                   
                                                   
                                                   
                                                   
                                   indirect        
                                                   
                                                   
                                                ind
irect                                              
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   `},
		{func() {
			scroll.Buffer().InsertString(scroll.Offset(), "indirect\n")
		}, `indirect                                           
                                                   
                                                   
                                                   
                                                   
                                   indirect        
                                                   
                                                   
                                                ind
irect                                              
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   
                                                   `},
	}

	comptest.TestComponent(t, resultDrawer{scroll}, w, tests)
}

func TestWindowCoordinatesPanicDeleteRow(t *testing.T) {
	scroll := makeScroll(true, 10, 3, 0, 0)(t)
	scroll.Buffer().DeleteRow(0)
	assert.NotPanics(t, func() {
		scroll.WindowToScrollCoordinates(term.Coordinates{Y: 8, X: 0})
		// do not assert result as it will always be incorrect
	})
}

func TestScrollGetMaxXOffsetWithTabs(t *testing.T) {
	tabspaces := 4
	width, height := 10, 4

	for _, tc := range []struct {
		name    string
		content string
	}{
		{
			name:    "tabs only",
			content: "\t\t\t\t\t\t\t\t\nshort\nshorter\n",
		},
		{
			name:    "mixed tabs and text",
			content: "ab\tcd\tef\tgh\tij\tkl\nshort\nshorter\n",
		},
		{
			name:    "tabs not on longest visual line",
			content: "\t\tx\naaaaaaaaaaaaaaaaaaaaaaaa\nshort\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scroll := newScroll(tabspaces, false /*wrap*/, width, height)
			_, err := scroll.Buffer().ReadFrom(strings.NewReader(tc.content))
			require.NoError(t, err)

			var expanded int
			for i := 0; i < scroll.buf.Rows(); i++ {
				if c := scroll.viewColumns(i); c > expanded {
					expanded = c
				}
			}
			wantMax := max(0, expanded-width)
			assert.Equal(t, wantMax, scroll.MaxOffset().X,
				"getMaxXOffset must use tab-expanded visual width")

			require.True(t, scroll.SeekEndLine())

			// Pick the row that produces the longest expanded width.
			var longestRow int
			expanded = 0
			for i := 0; i < scroll.buf.Rows(); i++ {
				if c := scroll.viewColumns(i); c > expanded {
					expanded = c
					longestRow = i
				}
			}
			rawCols := scroll.buf.Columns(longestRow)
			win, _ := scroll.ScrollToWindowCoordinates(
				term.Coordinates{Y: longestRow, X: rawCols - 1},
			)
			assert.GreaterOrEqual(t, win.X, 0)
			assert.Less(t, win.X, width,
				"end-of-line cursor must stay inside the viewport")
		})
	}
}

func BenchmarkScrollWrapDraw10(b *testing.B) {
	benchmarkScrollWrapDraw(b, 10, 0)
}
func BenchmarkScrollWrapDraw100(b *testing.B) {
	benchmarkScrollWrapDraw(b, 100, 0)
}
func BenchmarkScrollWrapDraw1000(b *testing.B) {
	benchmarkScrollWrapDraw(b, 1000, 0)
}
func BenchmarkScrollWrapDrawBigOffset1000(b *testing.B) {
	benchmarkScrollWrapDraw(b, 1000, 0.7)
}

func BenchmarkScrollDraw10(b *testing.B) {
	benchmarkScrollDraw(b, 10, 0)
}
func BenchmarkScrollDraw100(b *testing.B) {
	benchmarkScrollDraw(b, 100, 0)
}
func BenchmarkScrollDraw1000(b *testing.B) {
	benchmarkScrollDraw(b, 1000, 0)
}
func BenchmarkScrollDrawBigOffset1000(b *testing.B) {
	benchmarkScrollDraw(b, 1000, 0.7)
}

func BenchmarkScrollHiddenDraw10(b *testing.B) {
	benchmarkScrollHiddenDraw(b, 10, 0, 2)
}
func BenchmarkScrollHiddenDraw100(b *testing.B) {
	benchmarkScrollHiddenDraw(b, 100, 0, 2)
}
func BenchmarkScrollHiddenDraw100ManyHidden(b *testing.B) {
	benchmarkScrollHiddenDraw(b, 100, 0, 20)
}
func BenchmarkScrollHiddenDraw1000(b *testing.B) {
	benchmarkScrollHiddenDraw(b, 1000, 0, 2)
}
func BenchmarkScrollHiddenDrawBigOffset1000(b *testing.B) {
	benchmarkScrollHiddenDraw(b, 1000, 0.7, 2)
}

func BenchmarkScrollDraw100MB(b *testing.B) {
	benchmarkScrollDraw(b, 1000000, 0)
}
func BenchmarkScrollWrapDraw100MB(b *testing.B) {
	benchmarkScrollDraw(b, 1000000, 0)
}

func makeScroll(wrap bool, width, height, offsetY, offsetX int) func(t *testing.T) *Scroll {
	const content = `AAAAAAAAAAAAA
BBBBBBB
CCCCCCCCCCCCCC
DDDDDDD
EEEEEEEEEEEEEE`
	return makeScrollContent(wrap, width, height, offsetY, offsetX, content)
}

func makeScrollWithHiddenLines(wrap bool, width, height, offsetY, offsetX int, hidden ...startEndBlock) func(t *testing.T) *Scroll {
	fn := makeScrollContent(wrap, width, height, offsetY, offsetX, wrapCopy)
	return func(t *testing.T) *Scroll {
		scroll := fn(t)
		for _, block := range hidden {
			scroll.MarkHidden(block.start, block.end)
		}
		return scroll
	}
}

func makeScrollContent(wrap bool, width, height, offsetY, offsetX int, content string) func(t *testing.T) *Scroll {
	return func(t *testing.T) *Scroll {
		buf := cell.NewBuffer()
		buf.WriteString(content)
		ret := NewScroll(buf)
		ret.Wrap = wrap
		ret.Resize(width, height)
		// necessary for some wrap to work for seekTo and scroll.Wraps usage
		ret.Draw(term.NewStringWriter(width, height))
		if offsetY != 0 {
			require.True(t, ret.seekVertical(offsetY))
		}
		if offsetX != 0 {
			require.True(t, ret.seekHorizontal(offsetX))
		}
		return ret
	}
}

func newBigScroll(fortunes int) (scroll *Scroll) {
	scroll = NewScroll(cell.NewBuffer())
	for range fortunes {
		_, _ = scroll.Buffer().ReadFrom(strings.NewReader(fortune))
	}
	// assume big screen
	scroll.Resize(3000, 2000)
	return
}

func benchmarkScrollDraw(b *testing.B, fortunes int, offset float32) {
	scroll := newBigScroll(fortunes)
	seekPercRows(scroll, offset)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scroll.Draw(term.NoopWriter{})
	}
	// b.Logf("benchmark draw using payload of %d bytes\n", fortunes*len(fortune))
}

func benchmarkScrollHiddenDraw(b *testing.B, fortunes int, offset float32, hidden int) {
	scroll := newBigScroll(fortunes)
	seekPercRows(scroll, offset)
	for i := range hidden {
		scroll.MarkHidden(i, i+1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scroll.Draw(term.NoopWriter{})
	}
	// b.Logf("benchmark draw using payload of %d bytes\n", fortunes*len(fortune))
}

func seekPercRows(scroll *Scroll, offset float32) {
	offsetRows := int(float32(scroll.Buffer().Rows()) * offset)
	for range offsetRows {
		scroll.SeekDown()
	}
}

func benchmarkScrollWrapDraw(b *testing.B, fortunes int, offset float32) {
	scroll := newBigScroll(int(float32(fortunes) * (1 + offset)))
	scroll.Wrap = true
	seekPercRows(scroll, offset)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scroll.Draw(term.NoopWriter{})
	}
	// b.Logf("benchmark draw using payload of %d bytes\n", fortunes*len(fortune))
}

type subscriber struct {
	expectWillSeek  func(term.Coordinates)
	expectDidSeek   func(from, to term.Coordinates)
	expectOnHide    func(start, end int)
	expectOnVisible func(start int)
}

func (s subscriber) OnWillSeek(from term.Coordinates) {
	if s.expectWillSeek != nil {
		s.expectWillSeek(from)
	}
}

func (s subscriber) OnWillHide(start, end int) {
}

func (s subscriber) OnWillVisible(start int) {
}

func (s subscriber) OnDidHide(start, end int) {
	if s.expectOnHide != nil {
		s.expectOnHide(start, end)
	}
}
func (s subscriber) OnDidVisible(start int) {
	if s.expectOnVisible != nil {
		s.expectOnVisible(start)
	}
}
func (s subscriber) OnDidSeek(from, to term.Coordinates) {
	if s.expectDidSeek != nil {
		s.expectDidSeek(from, to)
	}
}

type resultDrawer struct {
	*Scroll
}

func (r resultDrawer) Draw(w term.Writer) {
	// simulate scroll draw
	r.RecalculateWraps()
	r.drawSearchResults(w)
}

type searchResultsWriter struct {
	*term.StringWriter
	scroll *Scroll
}

func (s searchResultsWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	c, ok := s.scroll.Buffer().Cell(s.scroll.WindowToScrollCoordinates(pos))
	if !ok {
		panic("hmm")
	}

	s.StringWriter.SetCell(pos, c)
}

const (
	fortune = `Love in your heart wasn't put there to stay.
Love isn't love 'til you give it away.
		-- Oscar Hammerstein ⌘⌘`

	wrapCopy = `module github.com/unstablebuild/blue

go 1.14

require (
	cloud.google.com/go v0.63.0 // indirect
	cloud.google.com/go/firestore v1.2.0
	github.com/adrianmo/go-nmea v1.2.0
	github.com/ernestrc/go-multierror v1.1.2 // indirect
	github.com/ernestrc/logd-go v0.0.0-20180509171507-65871c1d5504
	github.com/ernestrc/sensible v0.0.0-20170704153812-102a955adfdf
	github.com/golang/mock v1.4.4
	github.com/golang/protobuf v1.4.2
	github.com/google/uuid v1.1.1
	github.com/jacobsa/go-serial v0.0.0-20180131005756-15cf729a72d4
)`
	hiddenCopy = `module github.com/unstablebuild/blue

go 1.14

require (
	cloud.google.com/go v0.63.0 // indirect
	cloud.google.com/go/firestore v1.2.0
	github.com/adrianmo/go-nmea v1.2.0
	github.com/ernestrc/go-multierror v1.1.2 // indirect
	github.com/ernestrc/logd-go v0.0.0-20180509171507-65871c1d5504
	github.com/ernestrc/sensible v0.0.0-20170704153812-102a955adfdf
	github.com/golang/mock v1.4.4
	github.com/golang/protobuf v1.4.2
	github.com/google/uuid v1.1.1
	github.com/jacobsa/go-serial v0.0.0-20180131005756-15cf729a72d4
)
	
replace this => that
`
)

func newScroll(tabspaces int, wrap bool, width, height int) (scroll *Scroll) {
	buf := cell.NewBuffer()
	buf.Init()
	scroll = NewScroll(buf)
	scroll.SetTabspaces(tabspaces)
	scroll.Wrap = wrap
	scroll.Resize(width, height)
	return
}

func newScrollWrapTestCase(t *testing.T, width, height int) (*Scroll, *term.StringWriter) {
	tabspaces := 4
	wrap := true
	scroll := newScroll(tabspaces, wrap, width, height)
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(wrapCopy))
	require.NoError(t, err)

	w := term.NewStringWriter(width, height)
	return scroll, w
}

func TestScrollDrawRingMutationTable(t *testing.T) {
	tests := []struct {
		name       string
		wrap       bool
		width      int
		height     int
		offset     term.Coordinates
		attributes term.Attributes
	}{
		{
			name:   "no wrap with horizontal and vertical offsets",
			width:  6,
			height: 4,
			offset: term.Coordinates{X: 1, Y: 1},
		},
		{
			name:   "wrap across logical row boundary",
			wrap:   true,
			width:  5,
			height: 5,
			offset: term.Coordinates{Y: 1},
		},
		{
			name:       "draw attributes preserve explicit cell styles",
			width:      7,
			height:     4,
			offset:     term.Coordinates{Y: 1},
			attributes: term.Attributes{Fg: term.ColorGreen, Bg: term.ColorBlue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := scrollRingInitialRows()
			buf := new(cell.Buffer)
			buf.InitPerformance(len(rows), 8, ' ')
			buf.ResetCells(rows)
			for y, row := range rows {
				if row != nil && len(row) == 0 {
					buf.SetRow(y, make([]term.Cell, 0))
				}
			}
			model := cloneScrollRingRows(rows)

			scroll := new(Scroll)
			scroll.InitPerformance(buf)
			scroll.SetTabspaces(4)
			scroll.Wrap = tt.wrap
			scroll.InvertOffset = false
			scroll.Attributes = tt.attributes
			scroll.Resize(tt.width, tt.height)
			scroll.offset = tt.offset

			assertScrollRingDraw(t, scroll, model, "initial")
			for round := range 6 {
				exerciseScrollRingRound(t, scroll, &model, round)
			}
		})
	}
}

func exerciseScrollRingRound(
	t *testing.T, scroll *Scroll, model *[][]term.Cell, round int,
) {
	t.Helper()
	buf := scroll.Buffer()
	stage := func(operation string) {
		t.Helper()
		assertScrollRingDraw(t, scroll, *model, fmt.Sprintf("round %d: %s", round, operation))
	}

	rotation := 1 + round%max(1, len(*model)-1)
	if round%2 != 0 {
		rotation = -rotation
	}
	buf.RotateRows(0, len(*model), rotation)
	rotateScrollRingRows(*model, 0, len(*model), rotation)
	stage("rotate all rows")

	const limit = 6
	recycledWidth := 6 + round%3
	require.True(t, buf.AppendBlankRowsBounded(2, recycledWidth, limit))
	*model = appendScrollRingRowsBounded(*model, 2, recycledWidth, limit)
	for i := range 2 {
		y := len(*model) - 2 + i
		row := scrollRingReplacementRow(round*2 + i)
		buf.SetRow(y, cloneScrollRingRow(row))
		(*model)[y] = cloneScrollRingRow(row)
	}
	stage("append and populate recycled rows")

	raw := buf.RawCells()
	if y, ok := firstScrollRingRow(raw, 1); ok {
		color := term.Color(20 + round)
		raw[y][0].Bg = color
		(*model)[y][0].Bg = color
	}
	stage("linearize through raw cells")

	require.True(t, buf.AppendBlankRowsBounded(1, 5+round%2, limit))
	*model = appendScrollRingRowsBounded(*model, 1, 5+round%2, limit)
	last := len(*model) - 1
	row := scrollRingReplacementRow(20 + round)
	buf.SetRow(last, cloneScrollRingRow(row))
	(*model)[last] = cloneScrollRingRow(row)
	stage("return to bounded append")

	if len(*model) > 3 {
		start, end := 1, len(*model)-1
		count := 1
		if round%2 != 0 {
			count = -1
		}
		buf.RotateRows(start, end, count)
		rotateScrollRingRows(*model, start, end, count)
		stage("rotate a logical subregion")
	}

	if y, ok := firstScrollRingRow(*model, 4); ok {
		fill := scrollRingStyledCell(' ', 30+round)
		buf.ResetRowRange(y, 1, 3, fill)
		for x := 1; x < 3; x++ {
			(*model)[y][x] = fill
		}
		stage("reset cells in a wrapped row")

		buf.DeleteRowRange(y, 1, 2)
		copy((*model)[y][1:], (*model)[y][2:])
		(*model)[y] = (*model)[y][:len((*model)[y])-1]
		stage("delete cells in a wrapped row")
	}

	if y, ok := firstScrollRingRow(*model, 4); ok {
		at := len((*model)[y]) / 2
		require.True(t, buf.WrapRow(y, at))
		*model = splitScrollRingRow(*model, y, at)
		stage("linearize by splitting a row")

		_, ok := buf.ConflateRow(y)
		require.True(t, ok)
		*model = conflateScrollRingRow(*model, y)
		stage("conflate the split row")
	}

	if len(*model) > 1 {
		removed, ok := buf.TrimRowsFromStart(1)
		require.True(t, ok)
		require.Equal(t, 1, removed)
		*model = append([][]term.Cell(nil), (*model)[1:]...)
		stage("linearize by trimming from start")

		width := 5 + round%3
		require.True(t, buf.AppendBlankRows(1, width))
		*model = append(*model, repeatScrollRingCell(scrollRingBlankCell(), width))
		stage("append a recycled row after trimming")
	}

	buf.ResetCells(*model)
	for y, row := range *model {
		if len(row) == 0 {
			(*model)[y] = nil
		}
	}
	stage("reset cells before another ring round")
}

func assertScrollRingDraw(t *testing.T, scroll *Scroll, rows [][]term.Cell, stage string) {
	t.Helper()
	assert.Equal(t, rows, scroll.Buffer().CopyRows(nil), "%s: logical rows", stage)

	writer := term.NewStringWriter(scroll.width, scroll.height)
	scroll.Draw(writer)
	assert.Equal(
		t,
		drawScrollRingModel(
			rows,
			scroll.width,
			scroll.height,
			scroll.tabspaces,
			scroll.offset,
			scroll.Wrap,
			scroll.Attributes,
		),
		writer.Cells(),
		"%s: drawn cells",
		stage,
	)
}

func drawScrollRingModel(
	rows [][]term.Cell,
	width, height, tabspaces int,
	offset term.Coordinates,
	wrap bool,
	attributes term.Attributes,
) []term.Cell {
	cells := make([]term.Cell, width*height)
	if attributes != (term.Attributes{}) {
		fill := term.NewCell(0, 0, attributes)
		for i := range cells {
			cells[i] = fill
		}
	}
	draw := func(x, y int, c term.Cell) {
		if x < 0 || x >= width || y < 0 || y >= height {
			return
		}
		if attributes != (term.Attributes{}) {
			if c.Bg == 0 {
				c.Bg = attributes.Bg
			}
			if c.Fg == 0 {
				c.Fg = attributes.Fg
			}
		}
		cells[y*width+x] = c
	}

	if !wrap {
		for y := max(0, offset.Y); y < len(rows) && y-offset.Y < height; y++ {
			xoffset := 0
			for x, c := range rows[y] {
				if c.Ch == '\t' {
					xoffset += tabspaces - 1
				}
				visualX := x + xoffset
				if c.Width > 1 {
					xoffset += int(c.Width) - 1
				}
				if visualX >= offset.X+width {
					break
				}
				draw(visualX-offset.X, y-offset.Y, c)
			}
		}
		return cells
	}

	wrapsBefore := 0
	for y, row := range rows {
		xoffset := 0
		rowWraps := 0
		for x, c := range row {
			if c.Ch == '\t' {
				xoffset += tabspaces - 1
			}
			visualX := x + xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			cellWrap := visualX / width
			rowWraps = max(rowWraps, cellWrap)
			draw(visualX%width, y+wrapsBefore+cellWrap-offset.Y, c)
		}
		wrapsBefore += rowWraps
	}
	return cells
}

func scrollRingInitialRows() [][]term.Cell {
	return [][]term.Cell{
		{
			scrollRingStyledCell('A', 1),
			scrollRingBlankCell(),
			scrollRingTabCell(),
			scrollRingWideCell('漢'),
			scrollRingCombiningCell(),
		},
		nil,
		{},
		{
			scrollRingStyledCell('B', 2),
			scrollRingStyledCell('C', 3),
			scrollRingWideCell('界'),
			scrollRingBlankCell(),
		},
		{
			scrollRingBlankCell(),
			scrollRingTabCell(),
			scrollRingStyledCell('D', 4),
			scrollRingWideCell('語'),
		},
		{
			scrollRingCombiningCell(),
			scrollRingStyledCell('E', 5),
			scrollRingBlankCell(),
			scrollRingStyledCell('F', 6),
		},
	}
}

func scrollRingReplacementRow(seed int) []term.Cell {
	return []term.Cell{
		scrollRingStyledCell(rune('a'+seed%26), seed+1),
		scrollRingTabCell(),
		scrollRingWideCell([]rune("漢界語")[seed%3]),
		scrollRingCombiningCell(),
		scrollRingBlankCell(),
		scrollRingStyledCell(rune('A'+seed%26), seed+7),
	}
}

func scrollRingBlankCell() term.Cell {
	return term.Cell{Ch: ' ', Width: 1, Bytes: 1}
}

func scrollRingTabCell() term.Cell {
	return term.Cell{Ch: '\t', Width: 1, Bytes: 1}
}

func scrollRingWideCell(ch rune) term.Cell {
	return term.Cell{Ch: ch, Width: 2, Bytes: uint8(len(string(ch)))}
}

func scrollRingCombiningCell() term.Cell {
	cell := term.Cell{Ch: 'e', Width: 1, Bytes: 3}
	cell.SetCombining([]rune{'\u0301'})
	return cell
}

func scrollRingStyledCell(ch rune, seed int) term.Cell {
	return term.Cell{
		Ch:    ch,
		Width: 1,
		Bytes: uint8(len(string(ch))),
		Fg:    term.Color(1 + seed%8),
		Bg:    term.Color(9 + seed%8),
		Attrs: term.AttrBold | term.AttrUnderline,
	}
}

func cloneScrollRingRows(rows [][]term.Cell) [][]term.Cell {
	cloned := make([][]term.Cell, len(rows))
	for y, row := range rows {
		cloned[y] = cloneScrollRingRow(row)
	}
	return cloned
}

func cloneScrollRingRow(row []term.Cell) []term.Cell {
	if row == nil {
		return nil
	}
	cloned := make([]term.Cell, len(row))
	copy(cloned, row)
	for x := range cloned {
		if combining := cloned[x].CombiningRunes(); combining != nil {
			cloned[x].SetCombining(append([]rune(nil), combining...))
		}
	}
	return cloned
}

func repeatScrollRingCell(fill term.Cell, count int) []term.Cell {
	row := make([]term.Cell, count)
	for x := range row {
		row[x] = fill
	}
	return row
}

func appendScrollRingRowsBounded(
	rows [][]term.Cell, count, width, limit int,
) [][]term.Cell {
	if count <= 0 || width < 0 || limit <= 0 {
		return rows
	}
	if excess := len(rows) - limit; excess > 0 {
		rows = append([][]term.Cell(nil), rows[excess:]...)
	}
	for range count {
		blank := repeatScrollRingCell(scrollRingBlankCell(), width)
		if len(rows) < limit {
			rows = append(rows, blank)
			continue
		}
		copy(rows, rows[1:])
		rows[len(rows)-1] = blank
	}
	return rows
}

func rotateScrollRingRows(rows [][]term.Cell, start, end, count int) {
	if start < 0 || end > len(rows) || start >= end {
		return
	}
	length := end - start
	count %= length
	if count < 0 {
		count += length
	}
	if count == 0 {
		return
	}
	rotated := append([][]term.Cell(nil), rows[start:end]...)
	copy(rows[start:end-count], rotated[count:])
	copy(rows[end-count:end], rotated[:count])
}

func splitScrollRingRow(rows [][]term.Cell, y, at int) [][]term.Cell {
	head := rows[y][:at]
	tail := rows[y][at:]
	rows = append(rows, nil)
	copy(rows[y+2:], rows[y+1:])
	rows[y] = head
	rows[y+1] = tail
	return rows
}

func conflateScrollRingRow(rows [][]term.Cell, y int) [][]term.Cell {
	joined := make([]term.Cell, 0, len(rows[y])+len(rows[y+1]))
	joined = append(joined, rows[y]...)
	joined = append(joined, rows[y+1]...)
	rows[y] = joined
	copy(rows[y+1:], rows[y+2:])
	return rows[:len(rows)-1]
}

func firstScrollRingRow(rows [][]term.Cell, count int) (int, bool) {
	for y, row := range rows {
		if len(row) >= count {
			return y, true
		}
	}
	return 0, false
}
