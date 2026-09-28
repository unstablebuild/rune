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

package font

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"go.uber.org/multierr"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"unstable.build/rune/internal/term/gui/emoji"
	"unstable.build/rune/internal/term/gui/font/builtinfont"
	"unstable.build/rune/internal/workspace"
)

// Manager manages the underlying font.Face used to render
// characters on screen. It also exposes methods to help calculate
// the width and height in cells and pixels of the given screen.
//
// If SetFontByFamilyName is not called, a builtin font is used.
type Manager struct {
	findfont     findFont
	brailleFont  *sfnt.Font
	fallbackFont *sfnt.Font
	symbolFont   *sfnt.Font
	cjkFont      *sfnt.Font
	// acts as an IR to have all fonts preloaded upon
	// size, DPI and device scale changes.
	preloaded      []*sfnt.Font
	family         string
	regularFace    font.Face
	boldFace       font.Face
	italicFace     font.Face
	boldItalicFace font.Face
	size           float64
	staticDPI      float64
	staticDevScale float64
	charSize       CharSize
	offset         fixed.Point26_6
	cellOffsetY    float64
	cellOverlapX   int
	cellOverlapY   int

	// Resolved lazily and cached because resolution may parse a large
	// system font collection. emojiResolved guards the one-time resolution
	// even when no face was found.
	emojiFace     *emoji.Face
	emojiSource   string
	emojiResolved bool
	// Overrides the system emoji font candidates; nil means the
	// platform defaults.
	emojiPaths func() []string
}

// CharSize represent a character dimensions in pixels.
type CharSize struct {
	X float64
	Y float64
}

// NewManager allocates storage for a new Manager and initializes it
// with the default font and dpi.
func NewManager(overlapX, overlapY int) (*Manager, error) {
	ret := &Manager{
		size: defaultSizeForScale(deviceScale()),
		// cell pixels start/end overlap by exactly 1 pixel,
		// so we can achieve pixel-perfect text based frame UI.
		// This only works when rendered cell's frame is exactly 1 pixel:
		// example:▕▏
		cellOverlapX: overlapX,
		cellOverlapY: overlapY,
	}
	cwdURI, _ := workspaceapi.CurrentUserHostURI(".")
	fs, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), cwdURI)
	if err != nil {
		return nil, fmt.Errorf("new file scheme: %v", err)
	}
	ret.findfont = systemFindFont{reader: fs}
	ret.brailleFont, err = opentype.Parse(builtinfont.BrailleTTF)
	if err != nil {
		return nil, fmt.Errorf("parse braille font: %w", err)
	}
	ret.fallbackFont, err = opentype.Parse(builtinfont.FallbackTTF)
	if err != nil {
		return nil, fmt.Errorf("parse braille font: %w", err)
	}
	ret.symbolFont, err = opentype.Parse(builtinfont.SymbolTTF)
	if err != nil {
		return nil, fmt.Errorf("parse symbol font: %w", err)
	}
	cjk, err := sfnt.ParseCollection(builtinfont.CJKTTC)
	if err != nil {
		return nil, fmt.Errorf("parse cjk collection: %w", err)
	}
	cjkIndex := cjkFaceIndex()
	ret.cjkFont, err = cjk.Font(cjkIndex)
	if err != nil {
		return nil, fmt.Errorf("select cjk face %d: %w", cjkIndex, err)
	}
	ret.log(log.DebugLevel, "selected cjk face index %d", cjkIndex)

	// compensate default cell overlap
	ret.setOffsetX(float64(overlapX))
	ret.setOffsetY(float64(overlapY))
	return ret, nil
}

// IncreaseSize increases the size of the font by 1.
func (m *Manager) IncreaseSize() error {
	return m.SetSize(m.size + 1)
}

// DecreaseSize decreases the size of the font by 1.
func (m *Manager) DecreaseSize() error {
	if m.size <= 1 {
		return nil
	}
	return m.SetSize(m.size - 1)
}

// DPI returns the configured DPI for the underlying font.
func (m *Manager) DPI() float64 {
	return m.dpi()
}

func (m *Manager) dpi() float64 {
	if m.staticDPI != 0 {
		return m.staticDPI
	}
	return 72.0 * m.DeviceScale()
}

// SetDPI sets the DPI of the configured font. It will
// reload the font with the new DPI and return
// an error if there was a problem reloading font.
//
// Note that after this method is called, the DPI
// won't be adjusted dynamically. This is only meant to be run
// on systems where the device scale detector is not working properly.
func (m *Manager) SetDPI(dpi float64) error {
	if dpi < 0 {
		panic(errors.New("DPI must be >0"))
	}
	if m.staticDPI == dpi {
		return nil
	}
	m.staticDPI = dpi
	return m.ReloadFont()
}

// SetSize sets the size of the configured font.
// It will reload the font with the new DPI and return
// an error if there was a problem reloading the font.
//
// A size of 0 selects the DPI-aware default for the current device
// scale (larger on low-DPI displays). See defaultSizeForScale.
func (m *Manager) SetSize(size float64) error {
	if size == 0 {
		size = defaultSizeForScale(m.DeviceScale())
	}
	if m.size == size {
		return nil
	}
	prev := m.size
	m.size = size
	if err := m.ReloadFont(); err != nil {
		// Reloading at the new size produced invalid metrics (or I/O
		// failure). Revert the input and reload the previous size
		// normally so the manager stays in a consistent state.
		m.size = prev
		if rbErr := m.ReloadFont(); rbErr != nil {
			return fmt.Errorf("set font size %v: %w; "+
				"rollback to %v also failed: %v",
				size, err, prev, rbErr)
		}
		return fmt.Errorf("set font size %v: %w", size, err)
	}
	return nil
}

// defaultSizeForScale maps a device scale factor to a default point
// size, interpolating linearly from 15pt on low-DPI displays
// (scale <= 1) down to 13pt on hi-DPI displays (scale >= 2).
func defaultSizeForScale(scale float64) float64 {
	const (
		lowScale, lowSize = 1.0, 15.0
		hiScale, hiSize   = 2.0, 13.0
	)
	if scale <= lowScale {
		return lowSize
	}
	if scale >= hiScale {
		return hiSize
	}
	return lowSize + (scale-lowScale)*(hiSize-lowSize)/(hiScale-lowScale)
}

// SetOffset sets the x and y offset of the configured font.
// It will reload the font with the new DPI and return
// an error if there was a problem reloading the font.
func (m *Manager) SetOffset(x, y float64) error {
	prevX := fixedToFloat64(m.offset.X)
	prevY := fixedToFloat64(m.offset.Y)
	xok := m.setOffsetX(x)
	yok := m.setOffsetY(y)
	if !xok && !yok {
		return nil
	}
	if err := m.ReloadFont(); err != nil {
		// Reloading at the new offset produced invalid metrics. Revert
		// the input and reload the previous offset normally.
		m.setOffsetX(prevX)
		m.setOffsetY(prevY)
		if rbErr := m.ReloadFont(); rbErr != nil {
			return fmt.Errorf("set font offset (%v,%v): %w; "+
				"rollback to (%v,%v) also failed: %v",
				x, y, err, prevX, prevY, rbErr)
		}
		return fmt.Errorf("set font offset (%v,%v): %w", x, y, err)
	}
	return nil
}

// SetOffsetX sets the x offset of the configured font while preserving
// the current y offset.
func (m *Manager) SetOffsetX(x float64) error {
	return m.SetOffset(x, fixedToFloat64(m.offset.Y))
}

// SetOffsetY sets the y offset of the configured font while preserving
// the current x offset.
func (m *Manager) SetOffsetY(y float64) error {
	return m.SetOffset(fixedToFloat64(m.offset.X), y)
}

// IncreaseCellWidth increases the cell width of the font by 1 pixel.
func (m *Manager) IncreaseCellWidth() error {
	return m.SetOffsetX(fixedToFloat64(m.offset.X) + 1)
}

// DecreaseCellWidth decreases the cell width of the font by 1 pixel.
func (m *Manager) DecreaseCellWidth() error {
	return m.SetOffsetX(fixedToFloat64(m.offset.X) - 1)
}

// IncreaseLineHeight increases the line height of the font by 1 pixel.
func (m *Manager) IncreaseLineHeight() error {
	return m.SetOffsetY(fixedToFloat64(m.offset.Y) + 1)
}

// DecreaseLineHeight decreases the line height of the font by 1 pixel.
func (m *Manager) DecreaseLineHeight() error {
	return m.SetOffsetY(fixedToFloat64(m.offset.Y) - 1)
}

// ReloadFont reloads the font. This can be used
// if a change in DeviceScale is detected to re-adjust
// calcultions and font rendering for the new device scale.
func (m *Manager) ReloadFont() error {
	if len(m.preloaded) == 0 {
		return m.loadFallbackFont()
	}
	if err := m.setPreloaded(m.preloaded); err != nil {
		return fmt.Errorf("reload font: %w", err)
	}
	return nil
}

// SetFontByFamilyName finds the given font installed on the system and
// sets it as the configured font, or returns an error if there was
// a problem loading the given font.
// If name is set to an empty string, the default builtin font is used.
func (m *Manager) SetFontByFamilyName(name string) error {
	if name == m.family {
		return nil
	}
	prevFamily := m.family
	prevPreloaded := m.preloaded
	m.resetFonts()
	if name == "" {
		if err := m.loadFallbackFont(); err != nil {
			return m.rollbackFont(prevFamily, prevPreloaded,
				fmt.Errorf("load fallback font: %w", err))
		}
		m.family = ""
		m.preloaded = nil
		return nil
	}

	fonts, err := m.findAndLoadFont(name)
	if err != nil {
		return m.rollbackFont(prevFamily, prevPreloaded,
			fmt.Errorf("set font family %q: %w", name, err))
	}
	m.preloaded = fonts
	m.family = name
	return nil
}

// rollbackFont restores the previously configured font family after a
// failed font switch by running the normal load path for that family.
// loadErr is the original error that triggered the rollback and is
// always returned to the caller, wrapped with additional context if
// the rollback itself also fails.
func (m *Manager) rollbackFont(
	prevFamily string, prevPreloaded []*sfnt.Font, loadErr error,
) error {
	m.resetFonts()
	if len(prevPreloaded) == 0 {
		if rbErr := m.loadFallbackFont(); rbErr != nil {
			return fmt.Errorf("%w; rollback to builtin font also failed: %v",
				loadErr, rbErr)
		}
		m.family = ""
		m.preloaded = nil
		return loadErr
	}
	if rbErr := m.setPreloaded(prevPreloaded); rbErr != nil {
		// Last-ditch: drop back to the builtin fallback.
		m.resetFonts()
		if fbErr := m.loadFallbackFont(); fbErr != nil {
			return fmt.Errorf("%w; rollback to %q failed: %v; "+
				"builtin fallback also failed: %v",
				loadErr, prevFamily, rbErr, fbErr)
		}
		m.family = ""
		m.preloaded = nil
		return fmt.Errorf("%w; rollback to %q failed: %v",
			loadErr, prevFamily, rbErr)
	}
	m.preloaded = prevPreloaded
	m.family = prevFamily
	return loadErr
}

// FontFamily returns the currently configured font family name, or the
// empty string when the builtin fallback font is in use.
func (m *Manager) FontFamily() string {
	return m.family
}

// SetDeviceScale forces the device scale to the given value.
//
// Note that after this method is called, the device scale
// won't be adjusted dynamically. This is only meant to be run
// on systems where the device scale detector is not working properly.
func (m *Manager) SetDeviceScale(value float64) {
	m.staticDevScale = value
}

// DeviceScale returns the device scale factor of the current screen.
func (m *Manager) DeviceScale() float64 {
	if m.staticDevScale != 0 {
		return m.staticDevScale
	}
	// this cannot be cached otherwise moving window across screens with
	// different DPIs wouldn't adjust the device scale factor.
	return deviceScale()
}

// CharSize returns the character dimensions in pixels
// of the configured font.
func (m *Manager) CharSize() CharSize {
	m.ensureFontLoaded()
	return m.charSize
}

// OffsetY returns the y-offset for rendering a grid of character cells.
func (m *Manager) OffsetY() float64 {
	m.ensureFontLoaded()
	return m.cellOffsetY
}

// CellsWidth returns the total width in cells of the current screen.
func (m *Manager) CellsWidth(width int) int {
	m.ensureFontLoaded()
	return int(math.Max(1, math.Floor(m.cellsWidth(width))))
}

// CellsHeight returns the total height in cells of the current screen.
func (m *Manager) CellsHeight(height int) int {
	m.ensureFontLoaded()
	return int(math.Max(1, math.Floor(m.cellsHeight(height))))
}

// ImageWidth returns the total width in pixels of the current screen.
func (m *Manager) ImageWidth(width int) int {
	m.ensureFontLoaded()
	return int(math.Max(1, math.Floor(m.cellsWidth(width)*m.CharSize().X)))
}

// ImageHeight returns the total height in pixels of the current screen.
func (m *Manager) ImageHeight(height int) int {
	m.ensureFontLoaded()
	return int(math.Max(1, math.Floor(m.cellsHeight(height)*m.CharSize().Y)))
}

// RegularFontFace returns the configured regular font.Face.
func (m *Manager) RegularFontFace() font.Face {
	m.ensureFontLoaded()
	return m.regularFace
}

// PixelY returns the y offset of a pixel corresponding to the y cell offset.
func (m *Manager) PixelY(y int) float64 {
	m.ensureFontLoaded()
	return float64(y)*m.charSize.Y - float64(y)*float64(m.cellOverlapY)
}

// PixelX returns the x offset of a pixel corresponding to the x cell offset.
func (m *Manager) PixelX(x int) float64 {
	m.ensureFontLoaded()
	return m.charSize.X*float64(x) - float64(x)*float64(m.cellOverlapX)
}

// CellY returns the y offset of a cell corresponding to the y pixel offset.
func (m *Manager) CellY(y float64) int {
	m.ensureFontLoaded()
	return int(y / math.Max(1, m.charSize.Y-float64(m.cellOverlapY)))
}

// CellX returns the x offset of a cell corresponding to the x pixel offset.
func (m *Manager) CellX(x float64) int {
	m.ensureFontLoaded()
	return int(x / math.Max(1, m.charSize.X-float64(m.cellOverlapX)))
}

// BoldFontFace returns the configured bold font.Face or the fallback
// if no bold font face was found when loading the font.
func (m *Manager) BoldFontFace() font.Face {
	if m.boldFace == nil {
		return m.RegularFontFace()
	}
	return m.boldFace
}

// ItalicFontFace returns the configured italic font.Face or the fallback
// if no italic font face was found when loading the font.
func (m *Manager) ItalicFontFace() font.Face {
	if m.italicFace == nil {
		return m.RegularFontFace()
	}
	return m.italicFace
}

// BoldItalicFontFace returns the configured bold and italic font.Face or the fallback
// if no bold and italic font face was found when loading the font.
func (m *Manager) BoldItalicFontFace() font.Face {
	if m.boldItalicFace == nil {
		if m.boldFace == nil {
			return m.ItalicFontFace()
		}
		return m.BoldFontFace()
	}
	return m.boldItalicFace
}

// AvailableFontFamilies returns a list of available font families, by family name.
func (m *Manager) AvailableFontFamilies() (iterator.Iterator[string], error) {
	fonts, err := m.findfont.list()
	if err != nil {
		return nil, fmt.Errorf("list fonts: %w", err)
	}
	seen := make(map[string]struct{})
	return iterator.Filter(iterator.Map[metadata, string](fonts, func(f metadata) string {
		return f.family
	}), func(family string) bool {
		_, ok := seen[family]
		if ok {
			return false
		}
		seen[family] = struct{}{}
		return true
	}), nil
}

func (m *Manager) ensureFontLoaded() {
	if m.regularFace == nil {
		err := m.loadFallbackFont()
		if err != nil {
			panic(fmt.Sprintf("could not load the default fonts: %v", err))
		}
	}
}

func (m *Manager) cellsWidth(width int) float64 {
	return float64(width) * m.DeviceScale() / (m.charSize.X - float64(m.cellOverlapX))
}

func (m *Manager) cellsHeight(height int) float64 {
	return float64(height) * m.DeviceScale() / (m.charSize.Y - float64(m.cellOverlapY))
}

// metricsSafeForAllocation reports whether the current charSize is safe
// to use as a denominator when computing cell counts from pixel
// dimensions. Metrics become unsafe when a freshly loaded font reports
// degenerate glyph bounds (e.g. NaN, Inf, or a value at or below the
// cell overlap, which would make the effective cell size non-positive).
// See RUNE-51.
func (m *Manager) metricsSafeForAllocation() bool {
	return isSafeCellDenominator(m.charSize.X-float64(m.cellOverlapX)) &&
		isSafeCellDenominator(m.charSize.Y-float64(m.cellOverlapY))
}

func isSafeCellDenominator(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v > 0
}

func (m *Manager) loadFallbackFont() error {
	regular, err := opentype.Parse(builtinfont.RegularTTF)
	if err != nil {
		return err
	}
	m.regularFace, err = m.createFace(regular, false)
	if err != nil {
		return err
	}

	bold, err := opentype.Parse(builtinfont.BoldTTF)
	if err != nil {
		return err
	}
	m.boldFace, err = m.createFace(bold, true)
	if err != nil {
		return err
	}

	italic, err := opentype.Parse(builtinfont.ItalicTTF)
	if err != nil {
		return err
	}
	m.italicFace, err = m.createFace(italic, false)
	if err != nil {
		return err
	}

	boldItalic, err := opentype.Parse(builtinfont.BoldItalicTTF)
	if err != nil {
		return err
	}
	m.boldItalicFace, err = m.createFace(boldItalic, true)
	if err != nil {
		return err
	}

	return m.setFaceMetrics()
}

func (m *Manager) loadFontAtPath(path string) (fonts []*sfnt.Font, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}

	switch filepath.Ext(path) {
	case ".ttc", ".otc":
		col, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, fmt.Errorf("opentype parse collection: %w", err)
		}
		for i := 0; i < col.NumFonts(); i++ {
			font, err := col.Font(i)
			if err != nil {
				return nil, fmt.Errorf("font %d: %w", i, err)
			}
			fonts = append(fonts, font)
		}
	case ".ttf", ".otf":
		font, serr := opentype.Parse(data)
		if serr != nil {
			return nil, multierr.Append(err, fmt.Errorf("opentype parse font: %w", serr))
		}
		fonts = append(fonts, font)
	}
	if err != nil {
		return nil, err
	}

	var buf sfnt.Buffer
	for _, font := range fonts {
		if lerr := m.setFont(&buf, font); lerr != nil {
			return nil, multierr.Append(err, lerr)
		}
	}
	return
}

func (m *Manager) setFont(buf *sfnt.Buffer, font *sfnt.Font) error {
	subfamily, err := font.Name(buf, sfnt.NameIDSubfamily)
	if err != nil {
		return fmt.Errorf("read font subfamily: %w", err)
	}
	switch subfamily {
	case "Regular":
		face, err := m.createFace(font, false)
		if err != nil {
			return fmt.Errorf("create opentype face: %w", err)
		}
		m.regularFace = face
	case "Bold":
		face, err := m.createFace(font, true)
		if err != nil {
			return fmt.Errorf("create opentype face: %w", err)
		}
		m.boldFace = face
	case "Italic", "Oblique":
		face, err := m.createFace(font, false)
		if err != nil {
			return fmt.Errorf("create opentype face: %w", err)
		}
		m.italicFace = face
	case "Bold Italic", "Bold Oblique":
		face, err := m.createFace(font, true)
		if err != nil {
			return fmt.Errorf("create opentype face: %w", err)
		}
		m.boldItalicFace = face
	default:
		m.log(log.DebugLevel, "skipping subfamily: %q", subfamily)
	}
	return nil
}

func (m *Manager) resetFonts() {
	m.regularFace = nil
	m.boldFace = nil
	m.italicFace = nil
	m.boldItalicFace = nil
}

func (m *Manager) setPreloaded(preloaded []*sfnt.Font) (ret error) {
	m.resetFonts()
	var buf sfnt.Buffer
	for _, font := range preloaded {
		if err := m.setFont(&buf, font); err != nil {
			ret = multierr.Append(ret, err)
		}
	}
	if ret != nil {
		return
	}
	if err := m.setFaceMetrics(); err != nil {
		ret = multierr.Append(ret, err)
	}
	return
}

func (m *Manager) findAndLoadFont(name string) (ret []*sfnt.Font, err error) {
	fonts, err := m.findfont.findByFamily(name)
	if err != nil {
		return nil, fmt.Errorf("find font with family '%s': %w", name, err)
	}

	ctx := context.Background()
	defer fonts.Close()
	for {
		meta, ok := fonts.Next(ctx)
		if !ok {
			if err := fonts.Err(); err != nil {
				return nil, fmt.Errorf("fonts iterator: %v", err)
			}
			break
		}
		fonts, err := m.loadFontAtPath(meta.path)
		if err != nil {
			return nil, fmt.Errorf("load font at path '%s': %w", meta.path, err)
		}
		ret = append(ret, fonts...)
	}

	if m.regularFace == nil {
		return nil, fmt.Errorf("could not find regular style for font family '%s'", name)
	}

	err = m.setFaceMetrics()
	return
}

func (m *Manager) createFace(f *sfnt.Font, bold bool) (font.Face, error) {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    m.size,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new face: %w", err)
	}
	brailleFace, err := opentype.NewFace(m.brailleFont, &opentype.FaceOptions{
		Size:    m.size,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new braille face: %w", err)
	}
	fallbackFace, err := opentype.NewFace(m.fallbackFont, &opentype.FaceOptions{
		Size:    m.size,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new fallback face: %w", err)
	}
	symbolFace, err := opentype.NewFace(m.symbolFont, &opentype.FaceOptions{
		Size:    m.size,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new symbol face: %w", err)
	}
	charSizeX, charSizeY, offsetY := m.calcFaceMetrics(face)
	customFace := newCustomFace(charSizeX, charSizeY, offsetY,
		face, bold, m.cellOverlapX, m.cellOverlapY)
	faces := []font.Face{customFace, face, brailleFace, fallbackFace, symbolFace}
	// CJK must come last: Noto Sans CJK also covers Latin, punctuation and
	// box drawing, so any earlier position would steal those glyphs from
	// Symbola and Meslo and change existing rendering.
	cjkFace, err := m.createCJKFace(charSizeX)
	if err != nil {
		return nil, err
	}
	if cjkFace != nil {
		faces = append(faces, cjkFace)
	}
	face = newMultiFace(1, faces...)
	face = newCacheFace(face)
	return face, nil
}

// createCJKFace builds the CJK fallback scaled so an ideograph advance
// spans exactly two cells, the ic_width adjustment. Returns a nil face
// when the scale cannot be derived, in which case the chain omits CJK.
func (m *Manager) createCJKFace(charSizeX float64) (font.Face, error) {
	if math.IsNaN(charSizeX) || math.IsInf(charSizeX, 0) || charSizeX <= 0 {
		return nil, nil
	}
	probe, err := opentype.NewFace(m.cjkFont, &opentype.FaceOptions{
		Size:    m.size,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new cjk probe face: %w", err)
	}
	icAdvance, ok := probe.GlyphAdvance('水')
	if err := probe.Close(); err != nil {
		return nil, fmt.Errorf("close cjk probe face: %w", err)
	}
	if !ok || icAdvance <= 0 {
		return nil, nil
	}
	scale := (2 * charSizeX) / (float64(icAdvance) / (1 << 6))
	cjkFace, err := opentype.NewFace(m.cjkFont, &opentype.FaceOptions{
		Size:    m.size * scale,
		DPI:     m.dpi(),
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, fmt.Errorf("opentype new cjk face: %w", err)
	}
	return cjkFace, nil
}

func (m *Manager) setFaceMetrics() error {
	// use user/system face for calculating metrics, rather than
	// auxiliary or fallback faces.
	multi := m.regularFace.(*cacheFace).f.(*multi)
	faceForMetrics := multi.faces[multi.preferred]

	m.charSize.X, m.charSize.Y, m.cellOffsetY = m.calcFaceMetrics(faceForMetrics)
	m.log(log.DebugLevel, "calculated font char size: %+v and offset: %f",
		m.charSize, m.cellOffsetY)
	// Reject degenerate glyph metrics. Some fonts on some platforms
	// produce glyph bounds that collapse charSize to 0, negative, NaN
	// or Inf. Accepting those here would propagate into
	// cell.NewBufferWriter and crash the process (RUNE-51). Returning
	// an error here lets the load path roll back to the previous font
	// by running the normal reload flow.
	if !m.metricsSafeForAllocation() {
		return fmt.Errorf(
			"invalid font metrics: char size %+v with cell overlap (%d,%d)",
			m.charSize, m.cellOverlapX, m.cellOverlapY)
	}
	return nil
}

func (m *Manager) calcFaceMetrics(face font.Face) (float64, float64, float64) {
	bounds, advance, _ := face.GlyphBounds('█')

	charSizeX := math.Max(0, float64((advance-bounds.Min.X+m.offset.X)/(1<<6)))
	charSizeY := math.Max(0, float64((bounds.Max.Sub(bounds.Min).Y+m.offset.Y)/(1<<6)))
	cellOffsetY := float64(-bounds.Min.Y / (1 << 6))

	return charSizeX, charSizeY, cellOffsetY
}

func (m *Manager) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "font.Manager",
	}).Logf(level, msg, args...)
}

func (m *Manager) setOffsetX(x float64) bool {
	fixedX := float64ToFixed(x)
	if m.offset.X == fixedX {
		return false
	}
	m.offset.X = fixedX
	return true
}

func (m *Manager) setOffsetY(y float64) bool {
	fixedY := float64ToFixed(y)
	if m.offset.Y == fixedY {
		return false
	}
	m.offset.Y = fixedY
	return true
}

// EmojiFace returns the color-emoji face and a short origin label for
// logging, resolving both once and caching them. It prefers a usable
// system color-emoji font and falls back to the bundled Noto. A nil face
// means no font parsed and the caller must render emoji monochrome.
func (m *Manager) EmojiFace() (*emoji.Face, string) {
	if m.emojiResolved {
		return m.emojiFace, m.emojiSource
	}
	m.emojiResolved = true
	m.emojiFace, m.emojiSource = m.resolveEmojiFace()
	m.log(log.InfoLevel, "emoji font: %s", m.emojiSource)
	return m.emojiFace, m.emojiSource
}

func (m *Manager) resolveEmojiFace() (*emoji.Face, string) {
	candidates := emojiFontPaths
	if m.emojiPaths != nil {
		candidates = m.emojiPaths
	}
	for _, path := range candidates() {
		face, err := emoji.NewFaceFromFile(path)
		if err != nil {
			m.log(log.DebugLevel,
				"emoji font %q unusable, skipping: %v", path, err)
			continue
		}
		return face, "system: " + path
	}

	face, err := emoji.NewFace(builtinfont.EmojiTTF)
	if err != nil {
		m.log(log.WarnLevel, "bundled emoji font failed to parse: %v", err)
		return nil, "unavailable"
	}
	return face, "bundled: Noto Color Emoji"
}
