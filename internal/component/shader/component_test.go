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

package shader

import (
	"context"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
)

func TestShader(t *testing.T) {
	defer goleak.VerifyNone(t)

	t.Run("draws underlying component after shader is done", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		interrupter := term.FuncInterrupter(func(_ context.Context) error {
			defer wg.Done()
			return nil
		})
		s := New(&component.TestComponent{Ch: 'A'}, &testShader{}, interrupter, 1, 1*time.Second)
		s.Resize(20, 10)

		w := term.NewStringWriter(21, 11)
		tests := []comptest.TestCase{
			{Expected: `
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
                     `},
			{
				Action: func() {
					wg.Wait()
					assert.Equal(t, true, s.done.Load())
				},
				Expected: `
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
                     `},
		}

		comptest.TestComponent(t, s, w, tests)
		require.NoError(t, s.Close())
	})

	t.Run("releases frame buffer after shader is done", func(t *testing.T) {
		s := &Component{
			root: &component.TestComponent{Ch: 'A'},
			buf:  cell.NewBufferWriter(context.Background(), 20, 10),
		}
		s.done.Store(true)

		s.Draw(term.NewStringWriter(20, 10))

		assert.Nil(t, s.buf)
	})

	t.Run("forwards image placements cut to the cells still showing them", func(t *testing.T) {
		white := image.NewRGBA(image.Rect(0, 0, 2, 2))
		for i := range white.Pix {
			white.Pix[i] = 0xff
		}
		id := term.NewImageID()
		root := drawFunc(func(w term.Writer) {
			for y := range 4 {
				for x := range 10 {
					w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('B', 1, term.Attributes{}))
				}
			}
			assert.True(t, w.DrawImage(term.Image{
				Src: white, ID: id, Pos: term.Coordinates{X: 2, Y: 1},
				Width: 4, Height: 2, Fit: term.ImageFitFill,
			}))
			w.SetCell(term.Coordinates{X: 3, Y: 1}, term.NewCell('C', 1, term.Attributes{}))
		})
		s := &Component{
			root:  root,
			buf:   cell.NewBufferWriter(context.Background(), 10, 4),
			total: 10,
		}
		s.shader = &testShader{}
		s.Resize(10, 4)

		w := asciiart.NewStringWriter(10, 4, asciiart.DefaultConfig())
		comptest.TestComponent(t, s, w, []comptest.TestCase{{Expected: `
AAAAAAAAAA
AA@B@@AAAA
AA@@@@AAAA
AAAAAAAAAA`}})
	})

	t.Run("places images moved by pixels with the writer's cell size", func(t *testing.T) {
		id := term.NewImageID()
		root := drawFunc(func(w term.Writer) {
			w.DrawImage(term.Image{
				ID: id, Pos: term.Coordinates{X: 2, Y: 1}, Width: 4, Height: 2,
				Offset: image.Pt(23, 0),
			})
			// On the cells the placement moved off and onto.
			w.SetCell(term.Coordinates{X: 2, Y: 1}, term.NewCell('x', 1, term.Attributes{}))
			w.SetCell(term.Coordinates{X: 7, Y: 2}, term.NewCell('x', 1, term.Attributes{}))
		})
		s := &Component{
			root:  root,
			buf:   cell.NewBufferWriter(context.Background(), 10, 4),
			total: 10,
		}
		s.shader = &testShader{}
		s.Resize(10, 4)

		ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: 20})
		w := cell.NewBufferWriter(ctx, 10, 4)
		s.Draw(w)

		var got []image.Rectangle
		for _, img := range w.Images() {
			assert.Equal(t, image.Pt(23, 0), img.Offset)
			got = append(got, img.Clip)
		}
		assert.Equal(t, []image.Rectangle{
			image.Rect(4, 1, 9, 2),
			image.Rect(4, 2, 7, 3),
			image.Rect(8, 2, 9, 3),
		}, got, "cut on the cells it lands on, not the ones it was drawn on")
	})

	t.Run("resizes underlying component", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		interrupter := term.FuncInterrupter(func(_ context.Context) error {
			defer wg.Done()
			return nil
		})
		s := New(&component.TestComponent{Ch: 'A'}, &testShader{}, interrupter, 1, 1*time.Second)
		s.Resize(20, 10)

		w := term.NewStringWriter(21, 11)
		tests := []comptest.TestCase{
			{Expected: `
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
@@@@@@@@@@@@@@@@@@@@ 
                     `},
		}
		comptest.TestComponent(t, s, w, tests)

		s.Resize(21, 11)
		tests = []comptest.TestCase{
			{
				Action: func() {
					wg.Wait()
					assert.Equal(t, true, s.done.Load())
				},
				Expected: `
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAA`},
		}

		comptest.TestComponent(t, s, w, tests)
		require.NoError(t, s.Close())
	})

	t.Run("passes frame and total to shader", func(t *testing.T) {
		w := term.NewStringWriter(21, 11)
		mock := &testShader{}
		fps := 30

		var wg sync.WaitGroup
		var s *Component
		var i int
		var expectedFrames []int
		var expectedTotals []int
		const total = 30
		ready := make(chan struct{})
		interrupter := term.FuncInterrupter(func(_ context.Context) error {
			<-ready
			defer wg.Done()
			i++
			mock.called = false
			s.Draw(w)
			if i != total {
				expectedFrames = append(expectedFrames, i)
				expectedTotals = append(expectedTotals, total)
				assert.Equal(t, expectedFrames, mock.frames)
				assert.Equal(t, expectedTotals, mock.totals)
			}
			return nil
		})
		wg.Add(total)
		s = New(&component.TestComponent{Ch: 'A'}, mock, interrupter, fps, 1*time.Second)
		close(ready)
		wg.Wait()
		assert.NoError(t, s.Close())
	})
}

type testShader struct {
	called bool
	frames []int
	totals []int
}

type drawFunc func(term.Writer)

func (f drawFunc) Draw(w term.Writer) { f(w) }
func (drawFunc) Resize(int, int)      {}

func (t *testShader) Shade(frame, total int, in [][]term.Cell) {
	t.called = true
	t.frames = append(t.frames, frame)
	t.totals = append(t.totals, total)

	for y, row := range in {
		for x, cell := range row {
			in[y][x].Ch = cell.Ch - 1
		}
	}
}
