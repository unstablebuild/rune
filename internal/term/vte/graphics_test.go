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
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vtegraphics"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

const (
	testCellWidth  = 10
	testCellHeight = 20
)

// graphicsHarness drives a parser handler through the full parser, the
// way pty output reaches it in production.
type graphicsHarness struct {
	ph     *parserHandler
	pty    *workspacetest.File
	parser *vteparser.Parser
}

func newGraphicsHarness(t *testing.T, cellPixelSize func() (int, int)) *graphicsHarness {
	t.Helper()
	return newGraphicsHarnessFS(t, cellPixelSize, nil)
}

func newGraphicsHarnessFS(
	t *testing.T, cellPixelSize func() (int, int), fs schemeapi.FileSystem,
) *graphicsHarness {
	t.Helper()
	testURI, err := workspaceapi.ParseURI("memory:///radical")
	require.NoError(t, err)
	mockPtyFile := &workspacetest.File{}
	tm := &mockTabManager{}
	attrs := DefaultConfig().NeedsAttentionAttributes
	pty := workspaceapi.Pty{Master: mockPtyFile, Slave: mockPtyFile}
	ph := newParserHandler(new(sync.Mutex), pty, tm,
		clipboard.NewInMemory(), tm.bell, testURI, attrs, false, 100, 0,
		cellPixelSize, fs, "")
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	ph.Resize(20, 5)
	return &graphicsHarness{
		ph:     ph,
		pty:    mockPtyFile,
		parser: vteparser.NewParser(ph, new(vteparser.StdTimeout)),
	}
}

func (h *graphicsHarness) write(s string) {
	for _, b := range []byte(s) {
		h.parser.Advance(b)
	}
}

// apc sends one graphics command. A nil payload sends no payload at all.
func (h *graphicsHarness) apc(ctl string, payload []byte) {
	data := "\x1b_G" + ctl
	if payload != nil {
		data += ";" + base64.StdEncoding.EncodeToString(payload)
	}
	h.write(data + "\x1b\\")
}

// transmit sends an opaque wxh image with the given id.
func (h *graphicsHarness) transmit(id string, w, hgt int) {
	h.apc("a=t,i="+id+",s="+strconv.Itoa(w)+",v="+strconv.Itoa(hgt)+",q=2", make([]byte, w*hgt*4))
}

func (h *graphicsHarness) responses() string {
	var sb strings.Builder
	for _, w := range h.pty.Writes {
		sb.Write(w)
	}
	h.pty.Writes = nil
	return sb.String()
}

func (h *graphicsHarness) cursor() term.Coordinates {
	h.ph.sync.mu.Lock()
	defer h.ph.sync.mu.Unlock()
	return h.ph.sync.buf.CursorAtScreen()
}

// visible reports the placements of the active buffer as drawn at the
// current scroll position.
func (h *graphicsHarness) visible(scrolledBy int) []term.Image {
	h.ph.sync.mu.Lock()
	defer h.ph.sync.mu.Unlock()
	view := vtegraphics.View{
		Width: h.ph.width, Height: h.ph.height, ScrolledBy: scrolledBy,
		Cell: vtegraphics.CellSize{Width: testCellWidth, Height: testCellHeight},
	}
	return h.ph.graphicsStore().Visible(view, nil)
}

func positions(images []term.Image) []term.Coordinates {
	out := make([]term.Coordinates, 0, len(images))
	for _, img := range images {
		out = append(out, img.Pos)
	}
	return out
}

func testCell() (int, int) { return testCellWidth, testCellHeight }

func TestGraphicsDisabledWithoutCellSize(t *testing.T) {
	h := newGraphicsHarness(t, nil)
	h.apc("a=q,i=31,s=1,v=1", []byte{0, 0, 0, 255})
	h.apc("a=T,i=1,s=1,v=1", []byte{0, 0, 0, 255})
	h.write("\x1b[16t\x1b[14t")
	assert.Equal(t, "", h.responses(), "a cells-only display stays silent so clients fall back")
	assert.Equal(t, term.Coordinates{}, h.cursor())
	assert.True(t, h.ph.graphics.prim.Empty())
}

func TestGraphicsQueryAndSizeReports(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.apc("a=q,i=31,s=1,v=1", []byte{0, 0, 0, 255})
	assert.Equal(t, "\x1b_Gi=31;OK\x1b\\", h.responses())
	assert.True(t, h.ph.graphics.prim.Empty(), "queries leave nothing behind")

	h.apc("a=q,i=31,s=1,v=1,t=f", []byte("/nope"))
	assert.Equal(t, "\x1b_Gi=31;EBADF:Failed to read image file\x1b\\", h.responses())

	h.write("\x1b[16t")
	assert.Equal(t, "\x1b[6;20;10t", h.responses())
	h.write("\x1b[14t")
	assert.Equal(t, "\x1b[4;100;200t", h.responses())
}

func TestGraphicsBelTerminatedCommand(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.write("\x1b_Ga=q,i=31,s=1,v=1;" +
		base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 255}) + "\x07x")
	assert.Equal(t, "\x1b_Gi=31;OK\x1b\\", h.responses())
	assertEqualBuf(t, h.ph, "x                   \n"+strings.Repeat(" ", 20)+"\n"+
		strings.Repeat(" ", 20)+"\n"+strings.Repeat(" ", 20)+"\n"+strings.Repeat(" ", 20))
}

func TestGraphicsResponseOrdering(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	// The support probe: a query followed by DA1 must be answered in order.
	h.apc("a=q,i=1,s=1,v=1", []byte{0, 0, 0, 255})
	h.write("\x1b[c")
	resp := h.responses()
	assert.True(t, strings.HasPrefix(resp, "\x1b_Gi=1;OK\x1b\\"), resp)
	assert.Contains(t, resp[len("\x1b_Gi=1;OK\x1b\\"):], "\x1b[?")
}

func TestGraphicsPayloadDoesNotPrint(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.write("ab")
	h.transmit("1", 1, 1)
	h.write("cd")
	assertEqualBuf(t, h.ph, "abcd                \n"+strings.Repeat(" ", 20)+"\n"+
		strings.Repeat(" ", 20)+"\n"+strings.Repeat(" ", 20)+"\n"+strings.Repeat(" ", 20))
}

func TestGraphicsPlacementMovesCursor(t *testing.T) {
	tests := []struct {
		name    string
		start   term.Coordinates
		ctl     string
		cursor  term.Coordinates
		pos     term.Coordinates
		virtual bool
	}{
		{
			name:   "cursor lands after the last column on the last row",
			ctl:    "a=T,i=1,s=30,v=40",
			cursor: term.Coordinates{X: 3, Y: 1},
			pos:    term.Coordinates{X: 0, Y: 0},
		},
		{
			name:   "C=1 leaves the cursor alone",
			ctl:    "a=T,i=1,s=30,v=40,C=1",
			cursor: term.Coordinates{X: 0, Y: 0},
			pos:    term.Coordinates{X: 0, Y: 0},
		},
		{
			name:   "placement at the right edge wraps",
			start:  term.Coordinates{X: 18, Y: 1},
			ctl:    "a=T,i=1,s=30,v=40",
			cursor: term.Coordinates{X: 0, Y: 3},
			pos:    term.Coordinates{X: 18, Y: 1},
		},
		{
			name:   "placement past the bottom scrolls",
			start:  term.Coordinates{X: 0, Y: 4},
			ctl:    "a=T,i=1,s=30,v=40",
			cursor: term.Coordinates{X: 3, Y: 4},
			pos:    term.Coordinates{X: 0, Y: 3},
		},
		{
			name:    "virtual placements do not move the cursor",
			ctl:     "a=T,i=1,s=30,v=40,U=1",
			cursor:  term.Coordinates{X: 0, Y: 0},
			virtual: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newGraphicsHarness(t, testCell)
			h.write("\x1b[" + strconv.Itoa(tt.start.Y+1) + ";" + strconv.Itoa(tt.start.X+1) + "H")
			require.Equal(t, tt.start, h.cursor())
			h.apc(tt.ctl, make([]byte, 30*40*4))
			assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", h.responses())
			assert.Equal(t, tt.cursor, h.cursor())
			images := h.visible(0)
			if tt.virtual {
				assert.Empty(t, images, "virtual placements are only shown through placeholders")
				return
			}
			require.Len(t, images, 1)
			assert.Equal(t, tt.pos, images[0].Pos)
			assert.Equal(t, 3, images[0].Width)
			assert.Equal(t, 2, images[0].Height)
		})
	}
}

func TestGraphicsScrollsWithText(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.write("\x1b[2;1H")
	h.transmit("1", 10, 20)
	h.apc("a=p,i=1,q=2", nil)
	require.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(0)))

	h.write("\x1b[5;1H\n\n")
	assert.Empty(t, h.visible(0), "the placement scrolled into history with its line")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(2)),
		"scrolling back brings it into view")

	h.write("\x1b[1;1H\x1b[2J")
	assert.Empty(t, h.visible(0), "2J clears the screen placements")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(2)),
		"but keeps those in history")

	h.write("\x1b[3J")
	assert.Empty(t, h.visible(2), "3J clears history placements")
}

// TestGraphicsClearFollowsTextUnderMargins pins that ED 2, which pushes
// the whole primary screen into history regardless of DECSTBM, moves
// every placement with the text and not only those inside the region.
func TestGraphicsClearFollowsTextUnderMargins(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 10, 20)
	h.transmit("2", 10, 20)
	h.write("\x1b[1;1H")
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[4;1H")
	h.apc("a=p,i=2,q=2", nil)
	h.write("\x1b[5;1Hx")
	require.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 3}}, positions(h.visible(0)))

	h.write("\x1b[2;4r\x1b[2J")
	assert.Empty(t, h.visible(0), "2J clears the screen placements")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 3}}, positions(h.visible(5)),
		"both placements went into history with their rows")
}

// TestGraphicsInsertDeleteLinesLeavePlacements pins kitty's behaviour:
// IL and DL move text without moving image references, they only drop
// the cell images of the rows they touch (kitty screen.c:1722-1739,
// :2898, :2942). Cell images are rescanned from the placeholder cells
// on every draw here, so nothing has to be dropped.
func TestGraphicsInsertDeleteLinesLeavePlacements(t *testing.T) {
	for _, alt := range []string{"", "\x1b[?1049h\x1b[H"} {
		name := "primary"
		if alt != "" {
			name = "alternate"
		}
		t.Run(name, func(t *testing.T) {
			h := newGraphicsHarness(t, testCell)
			h.write(alt)
			h.write("\x1b[2;1H")
			h.transmit("1", 10, 20)
			h.apc("a=p,i=1,q=2", nil)
			require.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(0)))

			h.write("\x1b[1;1H\x1b[2L")
			assert.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(0)),
				"insert lines does not move the placement")
			h.write("\x1b[1;1H\x1b[1M")
			assert.Equal(t, []term.Coordinates{{X: 0, Y: 1}}, positions(h.visible(0)),
				"delete lines does not move the placement")
		})
	}
}

// TestGraphicsPrimaryScrollingRegion covers margins on the primary
// screen: only placements entirely inside the region move, and they are
// clipped to it (kitty screen.c:429-439).
func TestGraphicsPrimaryScrollingRegion(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 10, 20)
	h.write("\x1b[1;1H")
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[3;1H")
	h.transmit("2", 10, 20)
	h.apc("a=p,i=2,q=2", nil)
	require.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 2}}, positions(h.visible(0)))

	h.write("\x1b[2;4r\x1b[4;1H\n")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 1}}, positions(h.visible(0)),
		"only the placement inside the region moved")
	assert.Equal(t, 5, h.ph.sync.primBuf.Rows(),
		"a region below the top saves nothing to history")

	h.write("\x1b[4;1H\n")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(0)),
		"a placement scrolled out of the region is dropped")
}

func TestGraphicsScrollbackLimit(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 10, 20)
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[5;1H" + strings.Repeat("\n", 100))
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(100)), "just inside the scrollback")
	h.write("\n")
	assert.Empty(t, h.visible(100), "dropped once past the scrollback")
}

func TestGraphicsAlternateScreen(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 10, 20)
	h.apc("a=p,i=1,q=2", nil)

	h.write("\x1b[?1049h\x1b[H")
	assert.Empty(t, h.visible(0), "the alternate screen starts without placements")
	h.transmit("2", 10, 20)
	h.apc("a=p,i=2,q=2", nil)
	require.Len(t, h.visible(0), 1)

	h.write("\x1b[2;4r\x1b[4;1H\n")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(0)),
		"scrolling inside margins does not move placements outside them")
	h.write("\x1b[2;1H")
	h.apc("a=p,i=2,q=2", nil)
	h.write("\x1b[4;1H\n")
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(0)),
		"a placement scrolled out of the margins is dropped")

	h.write("\x1b[?1049l")
	images := h.visible(0)
	require.Len(t, images, 1, "primary placements survive a round trip")
	assert.Equal(t, term.Coordinates{X: 0, Y: 0}, images[0].Pos)
	h.write("\x1b[?1049h")
	assert.Empty(t, h.visible(0), "re-entering the alternate screen starts clean")
	assert.True(t, h.ph.graphics.alt.Empty())
}

func TestGraphicsResizeFollowsContent(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.write("\x1b[3;1H")
	h.transmit("1", 10, 20)
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[5;1Hx")
	require.Equal(t, []term.Coordinates{{X: 0, Y: 2}}, positions(h.visible(0)))

	h.ph.Resize(20, 3)
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(0)),
		"the placement stays with its line when rows move into history")
	h.ph.Resize(20, 5)
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 2}}, positions(h.visible(0)))
}

// TestGraphicsResizeRewrap covers a column change, which kitty ignores
// altogether (kitty graphics.c:2400-2423). A rewrap moves each logical
// line by a different number of rows, so the placement above the
// rewrapped line must stay put while the one below it follows.
func TestGraphicsResizeRewrap(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.ph.Resize(20, 6)
	h.transmit("1", 10, 20)
	h.transmit("2", 10, 20)
	h.write("\x1b[1;1H")
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[2;1H" + strings.Repeat("x", 25))
	h.write("\x1b[4;1H")
	h.apc("a=p,i=2,q=2", nil)
	require.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 3}}, positions(h.visible(0)))

	h.ph.Resize(10, 6)
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 4}}, positions(h.visible(0)),
		"only the placement below the line that gained a row moves")
	h.ph.Resize(20, 6)
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}, {X: 0, Y: 3}}, positions(h.visible(0)),
		"unwrapping puts it back")
}

func TestGraphicsDelete(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 10, 20)
	h.apc("a=p,i=1,q=2", nil)
	h.write("\x1b[3;3H")
	h.apc("a=p,i=1,q=2", nil)
	require.Len(t, h.visible(0), 2)

	h.write("\x1b[3;3H")
	h.apc("a=d,d=c", nil)
	assert.Equal(t, []term.Coordinates{{X: 0, Y: 0}}, positions(h.visible(0)),
		"d=c deletes the placement under the cursor")
	h.apc("a=d,d=I,i=1", nil)
	assert.Empty(t, h.visible(0))
	assert.True(t, h.ph.graphics.prim.Empty())
	assert.Equal(t, "", h.responses(), "deletes never respond")
}

// imageWriter records the images a draw places.
type imageWriter struct {
	term.Writer
	images []term.Image
}

func (w *imageWriter) DrawImage(img term.Image) bool {
	w.images = append(w.images, img)
	return true
}

func TestGraphicsDraw(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 30, 40)
	h.apc("a=p,i=1,q=2", nil)

	sw := term.NewStringWriter(20, 5)
	w := &imageWriter{Writer: sw}
	h.ph.sync.mu.Lock()
	_, running := h.ph.drawGraphics(w, 0, time.Now())
	h.ph.sync.mu.Unlock()
	assert.False(t, running)
	require.Len(t, w.images, 1)
	assert.Equal(t, term.Coordinates{}, w.images[0].Pos)
	assert.Equal(t, 3, w.images[0].Width)
	assert.Equal(t, 2, w.images[0].Height)
	assert.Equal(t, term.ImageFitFill, w.images[0].Fit)
}

func TestGraphicsPlaceholders(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 20, 20)
	h.apc("a=p,i=1,U=1,c=2,r=1,q=2", nil)

	// Two placeholder cells for image 1, row 0, columns 0 and 1.
	h.write("\x1b[38;5;1m")
	for col := range 2 {
		row, _ := vtegraphics.RowColumnDiacritic(0)
		column, _ := vtegraphics.RowColumnDiacritic(col)
		h.write(string(vtegraphics.PlaceholderChar) + string(row) + string(column))
	}
	h.write("\x1b[mX")

	sw := term.NewStringWriter(20, 5)
	h.ph.sync.mu.Lock()
	h.ph.sync.primBuf.Draw(sw)
	w := &imageWriter{Writer: sw}
	h.ph.drawGraphics(w, 0, time.Now())
	h.ph.sync.mu.Unlock()
	sw.Flush()

	assert.True(t, strings.HasPrefix(sw.String(), "  X"), "placeholder cells are blanked: %q", sw.String())
	require.Len(t, w.images, 1)
	assert.Equal(t, term.Coordinates{X: 0, Y: 0}, w.images[0].Pos)
	assert.Equal(t, 2, w.images[0].Width)
	assert.Equal(t, 1, w.images[0].Height)
	assert.Equal(t, term.ImageFitContain, w.images[0].Fit)
}

func TestGraphicsPlaceholderUnderlineColourSelectsPlacement(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 20, 20)
	h.apc("a=p,i=1,U=1,p=1,c=2,r=1,q=2", nil)
	h.apc("a=p,i=1,U=1,p=2,c=4,r=3,q=2", nil)

	// SGR 58 puts the placement id in the underline colour.
	h.write("\x1b[38;5;1m\x1b[58;2;0;0;2m")
	row, _ := vtegraphics.RowColumnDiacritic(0)
	column, _ := vtegraphics.RowColumnDiacritic(0)
	h.write(string(vtegraphics.PlaceholderChar) + string(row) + string(column))
	h.write("\x1b[m")

	w := &imageWriter{Writer: term.NewStringWriter(20, 5)}
	h.ph.sync.mu.Lock()
	h.ph.drawGraphics(w, 0, time.Now())
	h.ph.sync.mu.Unlock()

	require.Len(t, w.images, 1)
	assert.Equal(t, 4, w.images[0].Width, "placement 2 was selected")
	assert.Equal(t, 3, w.images[0].Height)
}

func TestGraphicsAnimationSchedulesRedraw(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.transmit("1", 1, 1)
	h.apc("a=f,i=1,s=1,v=1,z=30,q=2", []byte{0, 0, 0, 255})
	h.apc("a=a,i=1,r=1,z=50,s=3", nil)
	h.apc("a=p,i=1,q=2", nil)

	w := &imageWriter{Writer: term.NewStringWriter(20, 5)}
	h.ph.sync.mu.Lock()
	next, running := h.ph.drawGraphics(w, 0, time.Now())
	h.ph.sync.mu.Unlock()
	assert.True(t, running)
	assert.Greater(t, next, time.Duration(0))
	assert.LessOrEqual(t, next, 50*time.Millisecond)
}
