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

package gui

import (
	"image"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	imagefont "golang.org/x/image/font"
	"unstable.build/rune/internal/term/gui/drawrect"
	"unstable.build/rune/internal/term/gui/drawtext"
	"unstable.build/rune/internal/term/gui/font"
)

const (
	dimAlphaPerc    float32 = 0.7
	longestLigature int     = 2
)

var (
	frameToScreenOptions = ebiten.DrawImageOptions{
		Blend: ebiten.Blend{
			BlendFactorSourceRGB:        ebiten.BlendFactorOne,
			BlendFactorSourceAlpha:      ebiten.BlendFactorOne,
			BlendFactorDestinationRGB:   ebiten.BlendFactorZero,
			BlendFactorDestinationAlpha: ebiten.BlendFactorZero,
			BlendOperationRGB:           ebiten.BlendOperationAdd,
			BlendOperationAlpha:         ebiten.BlendOperationAdd,
		},
	}
)

var ligatures = map[string]rune{
	":=": '≔',
	"!=": '≠',
	"<=": '≤',
	">=": '≥',
	"=>": '⇒',
	"->": '→',
	"<-": '←',
	"<>": '≷',
}

type renderer struct {
	fontManager      *font.Manager
	drawer           *drawtext.Drawer
	font             fontFace
	emojiFace        colorEmojiFace
	bgOpacity        float64
	fgOpacity        float64
	bgColor          color.RGBA
	fgColor          color.RGBA
	frame            *ebiten.Image
	enableLigatures  bool
	cursorBackground color.RGBA
	cursorForeground color.RGBA

	bufPath     drawrect.Path
	bufVertices []ebiten.Vertex
	bufIndices  []uint16

	// images composites image placements over the cell frame. It is
	// drawn straight onto the screen, so it neither participates in nor
	// perturbs the frame's row-damage tracking.
	images imageLayer

	// Reusable scratch for a cell's cluster so routing to the color path
	// does not allocate per frame; valid only within one cell's handling.
	emojiCluster []rune

	// rectBatch accumulates all background rectangles and underline
	// strokes for a repaint so they issue as a single DrawTriangles,
	// separated from the glyph pass to let ebiten merge each class.
	rectBatch drawrect.Batch

	// Row-damage tracking. prevCells holds the grid drawn into frame
	// on the previous Draw; only rows that differ from it (plus the
	// old and new cursor rows and neighbours of vertical-offset rows)
	// are repainted into the persistent frame. prevValid is false
	// until the first full paint and after any full invalidation
	// (resize / font / theme / opacity / render-offset change), which
	// forces the next Draw to repaint every row.
	prevCells  [][]term.Cell
	prevCursor cursorState
	prevValid  bool
	dirtyRows  []bool

	// forceFullRepaint disables row-damage tracking: every Draw repaints
	// the whole frame. It exists so the differential correctness harness
	// can compare the damage-tracked path against the reference
	// full-repaint path in the same process, and as a runtime fallback.
	forceFullRepaint bool
}

type cursorState struct {
	pos   term.Coordinates
	style term.CursorStyle
	show  bool
}

type fontFace struct {
	Regular    imagefont.Face
	Bold       imagefont.Face
	Italic     imagefont.Face
	BoldItalic imagefont.Face
	CellSize   font.CharSize
	OffsetY    float64
}

// colorEmojiFace is the renderer's view of the color-emoji source: the
// predicate that routes a cluster to the color path plus the rasterizer
// the color atlas draws from. *emoji.Face satisfies it; a nil value
// disables the color path. A test double can stand in to observe routing.
type colorEmojiFace interface {
	Has(cluster []rune) bool
	drawtext.ColorGlyphSource
}

func newFontFace(fontManager *font.Manager) fontFace {
	return fontFace{
		Regular:    fontManager.RegularFontFace(),
		Bold:       fontManager.BoldFontFace(),
		Italic:     fontManager.ItalicFontFace(),
		BoldItalic: fontManager.BoldItalicFontFace(),
		CellSize:   fontManager.CharSize(),
		OffsetY:    fontManager.OffsetY(),
	}
}

func newRenderer(
	width, height int, deviceScale float64,
	fontManager *font.Manager, bgOpacity, fgOpacity float64, enableLigatures bool,
	cursorAttributes, defaultAttr term.Attributes,
) *renderer {
	bgBlack := applyOpacity(0, 0, 0, bgOpacity)
	fgWhite := applyOpacity(255, 255, 255, fgOpacity)

	imageWidth, imageHeight := fontManager.ImageWidth(width), fontManager.ImageHeight(height)
	var cursorForeground, cursorBackground color.RGBA
	if cursorAttributes.Bg.Valid() {
		rr, g, b := cursorAttributes.Bg.TrueColor().RGB()
		cursorBackground = color.RGBA{R: uint8(rr), G: uint8(g), B: uint8(b), A: 255}
	} else {
		cursorBackground = applyOpacity(0, 0, 0, bgOpacity)
	}
	if cursorAttributes.Fg.Valid() {
		rr, g, b := cursorAttributes.Fg.TrueColor().RGB()
		cursorForeground = color.RGBA{R: uint8(rr), G: uint8(g), B: uint8(b), A: 255}
	} else {
		cursorForeground = applyOpacity(0, 0, 0, fgOpacity)
	}

	bgColor := tcellToColor(defaultAttr.Bg, bgBlack, bgOpacity)
	return &renderer{
		fontManager:      fontManager,
		drawer:           drawtext.New(),
		bgColor:          bgColor,
		frame:            ebiten.NewImage(imageWidth, imageHeight),
		fgColor:          tcellToColor(defaultAttr.Fg, fgWhite, fgOpacity),
		font:             newFontFace(fontManager),
		emojiFace:        resolveColorEmojiFace(fontManager),
		bgOpacity:        bgOpacity,
		fgOpacity:        fgOpacity,
		enableLigatures:  enableLigatures,
		cursorForeground: cursorForeground,
		cursorBackground: cursorBackground,
	}
}

func resolveColorEmojiFace(fontManager *font.Manager) colorEmojiFace {
	face, _ := fontManager.EmojiFace()
	if face == nil {
		return nil
	}
	return face
}

func (r *renderer) deallocate() {
	if r.frame != nil {
		r.frame.Deallocate()
		r.frame = nil
	}
	if r.drawer != nil {
		r.drawer.Deallocate()
		r.drawer = nil
	}
	r.images.deallocate()
}

func (r *renderer) Draw(
	screen *ebiten.Image, cells [][]term.Cell, images []term.Image,
	drawCursor bool, cursorPos term.Coordinates,
	cursorStyle term.CursorStyle,
	offsetX, offsetY float64,
) {
	cursor := cursorState{pos: cursorPos, style: cursorStyle, show: drawCursor}
	full := r.computeDirtyRows(cells, cursor)
	if full {
		// fill default background so we can skip drawing individual
		// cells with default background.
		r.frame.Fill(r.bgColor)
		r.renderContent(r.frame, cells)
	} else {
		r.repaintRows(cells)
	}
	screen.DrawImage(r.frame, &frameToScreenOptions)
	r.drawImages(screen, cells, images, offsetX, offsetY)
	// The cursor goes onto the screen rather than the frame so it stays
	// above the image layer without being retained across frames.
	if drawCursor {
		r.renderCursor(screen, cells, cursorPos, cursorStyle)
	}
	r.snapshot(cells, cursor)
}

// drawImages composites the image placements over the cell frame, one
// layer at a time (spec §8.5 of the kitty graphics protocol). The frame
// already carries backgrounds and glyphs, so a placement that belongs
// below them is painted over it and the cell geometry that must stay on
// top is repainted, clipped to the pixels the placement covered.
func (r *renderer) drawImages(
	screen *ebiten.Image, cells [][]term.Cell, images []term.Image,
	offsetX, offsetY float64,
) {
	if len(images) == 0 && len(r.images.textures) == 0 {
		return
	}
	belowBg := r.drawImageLayer(screen, images, term.ImageLayerBelowBackground, offsetX, offsetY)
	r.repaintOver(screen, cells, belowBg, passRects)
	belowText := r.drawImageLayer(screen, images, term.ImageLayerBelowText, offsetX, offsetY)
	r.repaintOver(screen, cells, append(belowBg, belowText...), passGlyphs, passGlyphsBackground)
	r.drawImageLayer(screen, images, term.ImageLayerAboveText, offsetX, offsetY)
	r.images.evictUnused()
}

// drawImageLayer paints the placements belonging to one layer and
// returns the pixel rectangles they covered.
func (r *renderer) drawImageLayer(
	screen *ebiten.Image, images []term.Image, layer term.ImageLayer,
	offsetX, offsetY float64,
) []image.Rectangle {
	var rects []image.Rectangle
	for _, img := range images {
		if img.Layer != layer {
			continue
		}
		if rect := r.images.drawOne(screen, img, r.fontManager, offsetX, offsetY); !rect.Empty() {
			rects = append(rects, rect)
		}
	}
	return rects
}

// repaintOver redraws the given passes of the rows each rect covers,
// clipped to that rect so the rest of the frame is not composited twice.
func (r *renderer) repaintOver(
	screen *ebiten.Image, cells [][]term.Cell,
	rects []image.Rectangle, passes ...renderPass,
) {
	for _, rect := range rects {
		clipped := rect.Intersect(screen.Bounds())
		if clipped.Empty() {
			continue
		}
		dst := screen.SubImage(clipped).(*ebiten.Image)
		first, last := r.rowsCovering(clipped, len(cells))
		for _, pass := range passes {
			for viewY := last; viewY >= first; viewY-- {
				r.renderRow(dst, cells, viewY, pass)
			}
			r.endPass(dst, pass)
		}
	}
}

// rowsCovering returns the rows that can paint into rect, widened by one
// row because vertical-offset cells paint outside their own strip.
func (r *renderer) rowsCovering(rect image.Rectangle, height int) (first, last int) {
	first = max(0, r.fontManager.CellY(float64(rect.Min.Y))-1)
	last = min(height-1, r.fontManager.CellY(float64(rect.Max.Y-1))+1)
	return first, last
}

// computeDirtyRows marks r.dirtyRows for the rows that must be
// repainted this frame and reports whether the whole frame must be
// repainted (full). A full repaint is required on the first paint
// after an invalidation and whenever the grid dimensions change.
// Otherwise a row is dirty when its cells differ from the previous
// frame, when it is the old or new cursor row, or when a repainted row
// carries a vertical render offset that paints into it (those cells
// paint half a cell outside their own row, so the spill target must be
// cleared and repainted too or the spill re-composites there on every
// repaint). The spill rule is closed transitively: a spill target that
// itself carries an offset spills onward. The rule also runs inward:
// clearing a dirty row erases whatever a clean neighbour's offset
// cells painted into it, so that neighbour must repaint as well or its
// glyphs are left cut in half.
func (r *renderer) computeDirtyRows(cells [][]term.Cell, cursor cursorState) (full bool) {
	height := len(cells)
	if r.forceFullRepaint || !r.prevValid || len(r.prevCells) != height {
		return true
	}
	if cap(r.dirtyRows) < height {
		r.dirtyRows = make([]bool, height)
	}
	r.dirtyRows = r.dirtyRows[:height]
	for y := range r.dirtyRows {
		r.dirtyRows[y] = false
	}

	for y := range height {
		if len(cells[y]) != len(r.prevCells[y]) {
			return true
		}
		if !rowsEqual(cells[y], r.prevCells[y]) {
			r.markDirty(y, height)
		}
	}

	// Repaint the old and new cursor rows so a moved or hidden cursor
	// is erased and redrawn even when the underlying cells are equal.
	if r.prevCursor.show {
		r.markDirty(r.prevCursor.pos.Y, height)
	}
	if cursor.show {
		r.markDirty(cursor.pos.Y, height)
	}

	for changed := true; changed; {
		changed = false
		for y := range height {
			if !r.dirtyRows[y] {
				continue
			}
			up, down := rowSpill(cells[y])
			if up && y > 0 && !r.dirtyRows[y-1] {
				r.dirtyRows[y-1] = true
				changed = true
			}
			if down && y+1 < height && !r.dirtyRows[y+1] {
				r.dirtyRows[y+1] = true
				changed = true
			}
			if y > 0 && !r.dirtyRows[y-1] {
				if _, spillsDown := rowSpill(cells[y-1]); spillsDown {
					r.dirtyRows[y-1] = true
					changed = true
				}
			}
			if y+1 < height && !r.dirtyRows[y+1] {
				if spillsUp, _ := rowSpill(cells[y+1]); spillsUp {
					r.dirtyRows[y+1] = true
					changed = true
				}
			}
		}
	}
	return false
}

// rowSpill reports whether repainting the row paints outside its own
// strip: up when any cell carries the negative vertical render offset,
// down when any cell carries the positive one.
func rowSpill(row []term.Cell) (up, down bool) {
	for i := range row {
		a := row[i].Attrs
		up = up || a&term.AttrNegativeVerticalRenderOffset != 0
		down = down || a&term.AttrVerticalRenderOffset != 0
		if up && down {
			return
		}
	}
	return
}

// markDirty flags row y and, because vertical-offset cells paint into
// the adjacent row, its immediate neighbours.
func (r *renderer) markDirty(y, height int) {
	for dy := y - 1; dy <= y+1; dy++ {
		if dy >= 0 && dy < height {
			r.dirtyRows[dy] = true
		}
	}
}

// clearRow resets a single row strip of the frame to the default
// background so renderRow can repaint it, mirroring the whole-frame
// Fill used on a full repaint.
func (r *renderer) clearRow(y int) {
	top := int(math.Floor(r.fontManager.PixelY(y)))
	bottom := int(math.Ceil(r.fontManager.PixelY(y) + r.font.CellSize.Y))
	w, h := r.frame.Bounds().Dx(), r.frame.Bounds().Dy()
	if top < 0 {
		top = 0
	}
	if bottom > h {
		bottom = h
	}
	if top >= bottom {
		return
	}
	strip := r.frame.SubImage(image.Rect(0, top, w, bottom)).(*ebiten.Image)
	strip.Fill(r.bgColor)
}

// snapshot records the grid and cursor drawn this frame so the next
// Draw can diff against it. The cell grid is copied row by row into a
// reused backing store; the pointer-valued Combining field is compared
// by identity in rowsEqual, so a conservative copy is sufficient.
func (r *renderer) snapshot(cells [][]term.Cell, cursor cursorState) {
	if cap(r.prevCells) < len(cells) {
		r.prevCells = make([][]term.Cell, len(cells))
	}
	r.prevCells = r.prevCells[:len(cells)]
	for y := range cells {
		if cap(r.prevCells[y]) < len(cells[y]) {
			r.prevCells[y] = make([]term.Cell, len(cells[y]))
		}
		r.prevCells[y] = r.prevCells[y][:len(cells[y])]
		copy(r.prevCells[y], cells[y])
	}
	r.prevCursor = cursor
	r.prevValid = true
}

// rowsEqual reports whether two cell rows are identical. Cells are
// compared field by field; Combining is compared by pointer identity,
// which never yields a false "equal" (a genuinely changed combining
// sequence gets a fresh backing slice from the writer), so at worst a
// row is conservatively repainted.
func rowsEqual(a, b []term.Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderPass selects which class of geometry a single pass over the
// cells emits. Separating solid geometry from glyphs lets ebiten merge
// each class into a small number of GPU commands instead of thrashing
// pipeline state on every cell.
type renderPass int

const (
	// passRects accumulates background rectangles and underline strokes
	// into the shared rect batch.
	passRects renderPass = iota
	// passGlyphs draws ordinary (source-over) glyphs and counts
	// per-cell stats.
	passGlyphs
	// passGlyphsBackground draws background-rune glyphs, which use the
	// additive blend. Splitting the two blends into separate passes
	// keeps each atlas run uniform-blend so a whole page batches into
	// one DrawTriangles even when normal and background cells alternate.
	passGlyphsBackground
)

func (r *renderer) renderContent(screen *ebiten.Image, cells [][]term.Cell) {
	// Pass 1 accumulates every row's solid geometry into one rect batch
	// and flushes it as a single DrawTriangles so backgrounds land under
	// the glyphs. Passes 2 and 3 draw glyphs grouped by blend so each
	// atlas page batches into one DrawTriangles per blend.
	for _, pass := range glyphPasses {
		for viewY := len(cells) - 1; viewY >= 0; viewY-- {
			r.renderRow(screen, cells, viewY, pass)
		}
		r.endPass(screen, pass)
	}
}

// repaintRows re-renders the given dirty rows using the same two-pass
// (rects then glyphs) structure as a full repaint, so batching still
// applies to a partial frame.
func (r *renderer) repaintRows(cells [][]term.Cell) {
	for pass := range glyphPasses {
		for viewY := len(cells) - 1; viewY >= 0; viewY-- {
			if !r.dirtyRows[viewY] {
				continue
			}
			if pass == 0 {
				r.clearRow(viewY)
			}
			r.renderRow(r.frame, cells, viewY, glyphPasses[pass])
		}
		r.endPass(r.frame, glyphPasses[pass])
	}
}

// glyphPasses is the fixed pass order for a repaint: solid geometry
// first, then ordinary glyphs, then additive background-rune glyphs.
var glyphPasses = [...]renderPass{passRects, passGlyphs, passGlyphsBackground}

// endPass flushes the batch a pass accumulated into dst.
func (r *renderer) endPass(dst *ebiten.Image, pass renderPass) {
	switch pass {
	case passRects:
		r.rectBatch.Flush(dst)
	case passGlyphs, passGlyphsBackground:
		r.drawer.Flush(dst)
	}
}

func (r *renderer) renderRow(
	screen *ebiten.Image, cells [][]term.Cell, viewY int, pass renderPass,
) {
	row := cells[viewY]
	pixelY := r.fontManager.PixelY(viewY)
	textPixelY := pixelY + r.font.OffsetY
	halfCell := math.Floor(r.font.CellSize.Y/2) - 2

	var useFace imagefont.Face
	useFace = r.font.Regular

	var temp color.RGBA
	var skipRunes int
	// draw text content of each cell in row
	for viewX := range len(row) {
		if skipRunes > 0 {
			skipRunes--
			continue
		}
		cell := row[viewX]
		isBackground := graphemecluster.IsBackground(cell.Ch)

		var fg color.RGBA
		if isBackground {
			fg = tcellToColor(cell.Fg, r.fgColor, r.bgOpacity)
		} else {
			fg = tcellToColor(cell.Fg, r.fgColor, r.fgOpacity)
		}
		bg := tcellToColor(cell.Bg, r.bgColor, r.bgOpacity)
		pixelX := r.fontManager.PixelX(viewX)
		pixelY := pixelY
		textPixelY := textPixelY
		verticalOffset := cell.Attrs&term.AttrVerticalRenderOffset != 0
		negativeVerticalOffset := cell.Attrs&term.AttrNegativeVerticalRenderOffset != 0
		if verticalOffset {
			pixelY = math.Floor(pixelY + halfCell)
			textPixelY = math.Floor(textPixelY + halfCell)
		} else if negativeVerticalOffset {
			pixelY = max(0, math.Ceil(pixelY-halfCell-1))
			textPixelY = max(0, math.Ceil(textPixelY-halfCell-1))
		}

		// reverse attr if AttrReverse
		if cell.Attrs&term.AttrReverse != 0 {
			temp = fg
			fg = bg
			bg = temp
		}

		// we don't need to draw empty cells, just draw background
		if cell.Ch == 0 || cell.Ch == '\t' {
			// do not draw default background as a rect, since it's already
			// been instructed via frame.Fill above.
			if bg == r.bgColor {
				continue
			}
			if pass == passRects {
				r.rectBatch.AddRect(float32(pixelX), float32(pixelY),
					float32(r.font.CellSize.X), float32(r.font.CellSize.Y), bg)
			}
			continue
		}

		isBold := cell.Attrs&term.AttrBold != 0
		isItalic := cell.Attrs&term.AttrItalic != 0

		// pick a font face for the cell
		if !isBold && !isItalic {
			useFace = r.font.Regular
		} else if isBold && isItalic {
			useFace = r.font.BoldItalic
		} else if isBold {
			useFace = r.font.Bold
		} else if isItalic {
			useFace = r.font.Italic
		}

		if pass == passRects && cell.Attrs&term.AttrUnderline != 0 {
			underlinePixelY := pixelY + r.font.CellSize.Y - 1
			stroke := fg
			if u := cell.UnderlineColor(); u.Valid() {
				stroke = tcellToColor(u, fg, r.fgOpacity)
			}
			r.rectBatch.AddStroke(float32(pixelX), float32(underlinePixelY),
				float32(pixelX+r.font.CellSize.X),
				float32(underlinePixelY), 2, stroke)
		}

		if r.enableLigatures && skipRunes == 0 {
			skipRunes = r.ligatureLength(cells, viewX, viewY)
			if skipRunes > 0 && pass == passGlyphs {
				r.handleLigatures(screen, cells, viewX, viewY, useFace, fg)
			}
		}

		if skipRunes > 0 {
			skipRunes--
			continue
		}

		cellWidth := math.Max(1, float64(cell.Width))
		// do not draw default background as a rect, since it's already
		// been instructed via frame.Fill above.
		if pass == passRects {
			if bg != r.bgColor {
				r.rectBatch.AddRect(float32(pixelX), float32(pixelY),
					float32(r.font.CellSize.X*cellWidth), float32(r.font.CellSize.Y), bg)
			}
		} else if (pass == passGlyphs) != isBackground {
			// An emoji-presented cluster the face can render takes the
			// untinted color path; everything else takes the mask path.
			cluster := r.cellCluster(cell)
			if !isBackground && r.emojiFace != nil && r.emojiFace.Has(cluster) {
				r.drawer.DrawColorGlyph(screen, cluster, r.emojiFace,
					pixelX, pixelY,
					int(r.font.CellSize.X*cellWidth), int(r.font.CellSize.Y))
			} else {
				r.drawer.DrawGlyph(screen, cell.Ch, useFace, pixelX, textPixelY,
					glyphColor(fg, cell.Attrs&term.AttrDim != 0), isBackground)
			}
		}
		if cell.Width > 1 {
			skipRunes += int(cell.Width) - 1
		}
	}
}

// cellCluster returns the cell's cluster (Ch plus combining runes) in a
// reused scratch buffer; the result is valid only until the next call.
func (r *renderer) cellCluster(cell term.Cell) []rune {
	r.emojiCluster = append(r.emojiCluster[:0], cell.Ch)
	r.emojiCluster = append(r.emojiCluster, cell.CombiningRunes()...)
	return r.emojiCluster
}

func (r *renderer) handleLigatures(
	screen *ebiten.Image, cells [][]term.Cell, sx, sy int,
	face imagefont.Face, color color.RGBA,
) (length int) {
	return handleLigatures(r.drawer, cells, sx, sy, face, color, r.font, screen)
}

// ligatureLength reports how many cells starting at (sx, sy) collapse
// into a single ligature glyph, without drawing anything. It mirrors
// the candidate-matching in handleLigatures so the rect pass and the
// glyph pass agree on which cells a ligature consumes.
func (r *renderer) ligatureLength(cells [][]term.Cell, sx, sy int) int {
	if !r.enableLigatures {
		return 0
	}
	var c [longestLigature]rune
	candidate := c[:0]
	for i := range longestLigature {
		x := sx + i
		if sy >= len(cells) || x >= len(cells[sy]) || cells[sy][x].Ch == 0 {
			break
		}
		candidate = append(candidate, cells[sy][x].Ch)
	}
	for len(candidate) > 1 {
		if _, ok := ligatures[string(candidate)]; ok {
			return len(candidate)
		}
		candidate = candidate[:len(candidate)-1]
	}
	return 0
}

func (r *renderer) renderCursor(
	screen *ebiten.Image, cells [][]term.Cell,
	pos term.Coordinates, style term.CursorStyle,
) {
	cell := r.getCell(cells, pos)
	width := math.Max(1, float64(cell.Width))

	useFace := r.font.Regular
	isBold := cell.Attrs&term.AttrBold != 0
	isItalic := cell.Attrs&term.AttrItalic != 0
	if isBold && isItalic {
		useFace = r.font.BoldItalic
	} else if isBold {
		useFace = r.font.Bold
	} else if isItalic {
		useFace = r.font.Italic
	}

	pixelX := r.fontManager.PixelX(pos.X)
	pixelY := r.fontManager.PixelY(pos.Y)
	textPixelY := pixelY + r.font.OffsetY
	pixelW, pixelH := r.font.CellSize.X*width, r.font.CellSize.Y

	// empty rect without focus
	if !ebiten.IsFocused() {
		r.bufVertices, r.bufIndices = drawrect.DrawRect(
			&r.bufPath, r.bufVertices, r.bufIndices,
			screen, float32(pixelX), float32(pixelY),
			float32(pixelW), float32(pixelH), r.cursorBackground)
		r.bufVertices, r.bufIndices = drawrect.DrawRect(
			&r.bufPath, r.bufVertices, r.bufIndices,
			screen, float32(pixelX+1), float32(pixelY+1),
			float32(pixelW-2), float32(pixelH-2), r.cursorForeground)
		return
	}

	// draw the cursor shape
	switch style {
	case term.CursorStyleBlinkingBar, term.CursorStyleSteadyBar:
		r.bufVertices, r.bufIndices = drawrect.DrawRect(
			&r.bufPath, r.bufVertices, r.bufIndices,
			screen, float32(pixelX), float32(pixelY), 2,
			float32(pixelH), r.cursorBackground)
	case term.CursorStyleBlinkingUnderline, term.CursorStyleSteadyUnderline:
		r.bufVertices, r.bufIndices = drawrect.DrawRect(
			&r.bufPath, r.bufVertices, r.bufIndices,
			screen, float32(pixelX), float32(pixelY+pixelH-2),
			float32(pixelW), 2, r.cursorBackground)
	default:
		r.bufVertices, r.bufIndices = drawrect.DrawRect(
			&r.bufPath, r.bufVertices, r.bufIndices,
			screen, float32(pixelX), float32(pixelY),
			float32(pixelW), float32(pixelH), r.cursorBackground)
		if cell.Ch != 0 {
			r.drawer.DrawGlyph(screen, cell.Ch, useFace, pixelX, textPixelY,
				glyphColor(r.cursorForeground, false), false)
			r.drawer.Flush(screen)
		}
	}
}

func (r *renderer) getCell(cells [][]term.Cell, pos term.Coordinates) (ret term.Cell) {
	if pos.Y >= len(cells) || pos.X >= len(cells[pos.Y]) {
		return
	}
	return cells[pos.Y][pos.X]
}

func tcellToColor(tcolor term.Color, def color.RGBA, opacity float64) color.RGBA {
	if !tcolor.Valid() || tcolor == term.ColorDefault {
		return def
	}
	r, g, b := tcolor.TrueColor().RGB()
	return applyOpacity(r, g, b, opacity)
}

func handleLigatures(
	drawer *drawtext.Drawer, cells [][]term.Cell, sx, sy int, face imagefont.Face, color color.RGBA,
	font fontFace, frame *ebiten.Image,
) (length int) {
	var c [longestLigature]rune
	candidate := c[:0]
	for i := 0; i < longestLigature; i++ {
		x := sx + i
		if sy >= len(cells) || x >= len(cells[sy]) || cells[sy][x].Ch == 0 {
			break
		}
		candidate = append(candidate, cells[sy][x].Ch)
	}

	for len(candidate) > 1 {
		if ru, ok := ligatures[string(candidate)]; ok {
			// draw ligature
			ligX := (float64(sx) * font.CellSize.X) + ((float64(len(candidate)-1) * font.CellSize.X) / 2)
			ligY := float64(sy)*font.CellSize.Y + font.OffsetY
			drawer.DrawGlyph(frame, ru, face, ligX, ligY, glyphColor(color, false), false)
			return len(candidate)
		}
		candidate = candidate[:len(candidate)-1]
	}

	return 0
}

func applyOpacity(r, g, b int32, opacity float64) color.RGBA {
	alpha := uint8(float64(255) * opacity)
	return color.RGBA{
		R: uint8(float64(r) * opacity),
		G: uint8(float64(g) * opacity),
		B: uint8(float64(b) * opacity),
		A: alpha,
	}
}

// glyphColor converts a straight-alpha foreground color into the
// premultiplied per-vertex scale the glyph atlas expects. It mirrors the
// previous per-glyph path, which set a fresh ebiten ColorScale via
// Scale(r,g,b,a) on the white glyph mask and, for dim cells, multiplied
// it by dimAlphaPerc with ScaleAlpha. Because the mask is opaque white,
// that scale is exactly the resulting premultiplied vertex color.
func glyphColor(fg color.RGBA, dim bool) [4]float32 {
	r, g, b, a := fg.RGBA()
	col := [4]float32{
		float32(r) / 0xffff,
		float32(g) / 0xffff,
		float32(b) / 0xffff,
		float32(a) / 0xffff,
	}
	if dim {
		col[0] *= dimAlphaPerc
		col[1] *= dimAlphaPerc
		col[2] *= dimAlphaPerc
		col[3] *= dimAlphaPerc
	}
	return col
}
