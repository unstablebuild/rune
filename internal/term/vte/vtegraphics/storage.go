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

package vtegraphics

import (
	"image"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// DefaultStorageLimit is the per-screen quota for image pixels
// (spec §14).
const DefaultStorageLimit = 320 * 1024 * 1024

// parentDepthLimit bounds relative placement chains (spec §10).
const parentDepthLimit = 8

// CellSize is the pixel size of one character cell.
type CellSize struct {
	Width, Height int
}

// Cursor is the screen-relative cursor position a command executes at.
// Put commands advance it; the screen wraps and scrolls afterwards.
type Cursor struct {
	X, Y int
}

// Margins are the inclusive scrolling region rows of the screen.
type Margins struct {
	Top, Bottom int
}

// Image is a transmitted raster (spec §5).
type Image struct {
	internalID uint64
	// ClientID is the 'i' id, 0 for id-less images.
	ClientID uint32
	// Number is the 'I' number.
	Number uint32
	termID term.ImageID
	// version changes whenever pix changes so the writer re-uploads.
	version   uint64
	pix       *image.RGBA
	width     int
	height    int
	loaded    bool
	transient bool
	atime     uint64
	used      int
	refs      []*Placement
	anim      *animation
}

// Placement is one display of an image (spec §8). Rows are relative to
// the top of the screen and go negative as the placement scrolls into
// history.
type Placement struct {
	internalID uint64
	// ClientID is the 'p' id, 0 for none.
	ClientID uint32
	// StartRow and StartCol are the anchor cell.
	StartRow, StartCol int
	// src is the displayed sub-rectangle of the image, in pixels.
	src image.Rectangle
	// cellX, cellY are the sub-cell offsets X, Y.
	cellX, cellY int
	// cols, rows are the requested c, r.
	cols, rows int
	// effCols, effRows are the cell footprint (spec §8.4).
	effCols, effRows int
	// Z is the z-index.
	Z int32
	// Virtual placements are placeholder prototypes (spec §9).
	Virtual bool
	// parentImg, parentRef are internal ids; 0 means none.
	parentImg, parentRef uint64
	offX, offY           int32
}

// pendingLoad is an in-progress chunked transmission (spec §4.6).
type pendingLoad struct {
	// start is the first chunk's command with its resolved image id;
	// later chunks only contribute payload, 'm' and 'q'.
	start   Command
	spec    loadSpec
	image   uint64
	frame   *frameLoad
	isQuery bool
	buf     []byte
}

// Storage holds the images and placements of one screen buffer
// (spec §18.3). It is not safe for concurrent use.
type Storage struct {
	// Limit is the storage quota in bytes; 0 disables the quota.
	Limit int

	images  []*Image
	nextID  uint64
	pending *pendingLoad
	used    int
	clock   uint64
}

// readMedium returns what Command.ReadMedium read for a non-direct
// transmission (spec §4.4).
func (s *Storage) readMedium(medium byte, cmd Command) ([]byte, *Error) {
	switch medium {
	case 'f', 't', 's':
	default:
		return nil, errorf("EINVAL", "Unknown transmission type: %c", medium)
	}
	if len(cmd.Payload) > MaxPathLength {
		return nil, errorf("EINVAL", "Filename too long")
	}
	if !cmd.mediumRead {
		return nil, errorf("EBADF", "Failed to read image file")
	}
	return cmd.mediumData, nil
}

// NewStorage returns an empty store with the default quota.
func NewStorage() *Storage {
	return &Storage{Limit: DefaultStorageLimit}
}

// Empty reports whether no image is stored.
func (s *Storage) Empty() bool {
	return len(s.images) == 0
}

// Reset drops every image and placement.
func (s *Storage) Reset() {
	s.images = nil
	s.pending = nil
	s.used = 0
}

// Handle executes one parsed command at the cursor and returns the
// response to write to the client, if any (spec §6). cur is advanced by
// placements that move the cursor; the caller applies wrapping and
// scrolling (spec §8.7).
func (s *Storage) Handle(cmd Command, cur *Cursor, cell CellSize) ([]byte, bool) {
	if cmd.ImageID != 0 && cmd.ImageNumber != 0 {
		return s.response(cmd, false,
			errorf("EINVAL", "Must not specify both image id and image number"))
	}

	switch cmd.Action {
	case 0, 't', 'T', 'q':
		isQuery := cmd.Action == 'q'
		if isQuery && cmd.ImageID == 0 {
			return nil, false
		}
		img, start, loaded, err := s.transmit(cmd, isQuery)
		if cmd.Quiet != 0 {
			start.Quiet = cmd.Quiet
		}
		var resp []byte
		var ok bool
		if isQuery {
			resp, ok = s.response(Command{ImageID: cmd.ImageID, Quiet: cmd.Quiet}, loaded, err)
		} else {
			resp, ok = s.response(start, loaded, err)
		}
		if start.Action == 'T' && img != nil && loaded && !isQuery {
			// kitty builds the response before placing, so a failed
			// placement after a=T is never reported (graphics.c:2472-2473).
			_, _ = s.put(start, cur, cell, img)
		}
		if isQuery {
			s.trimUnreferenced(0)
		}
		var added uint64
		if img != nil {
			added = img.internalID
		}
		if s.Limit > 0 && s.used > s.Limit {
			s.applyQuota(added)
		}
		return resp, ok

	case 'f', 'a':
		return s.handleAnimation(cmd)

	case 'p':
		if cmd.ImageID == 0 && cmd.ImageNumber == 0 {
			return nil, false
		}
		id, err := s.put(cmd, cur, cell, nil)
		cmd.ImageID = id
		return s.response(cmd, true, err)

	case 'd':
		s.delete(cmd, cur)
		return nil, false

	case 'c':
		return s.handleCompose(cmd)
	}
	return nil, false
}

// response formats the reply for cmd (spec §6.1, §6.2).
func (s *Storage) response(cmd Command, loaded bool, err *Error) ([]byte, bool) {
	if cmd.Quiet != 0 && (err == nil || cmd.Quiet > 1) {
		return nil, false
	}
	if cmd.ImageID == 0 && cmd.ImageNumber == 0 {
		return nil, false
	}
	if err == nil && !loaded {
		return nil, false
	}
	out := make([]byte, 0, 64)
	out = append(out, "\x1b_G"...)
	sep := ""
	field := func(key string, v uint32) {
		if v == 0 {
			return
		}
		out = append(out, sep...)
		out = append(out, key...)
		out = strconv.AppendUint(out, uint64(v), 10)
		sep = ","
	}
	field("i=", cmd.ImageID)
	field("I=", cmd.ImageNumber)
	field("p=", cmd.PlacementID)
	if cmd.Action == 'f' || cmd.Action == 'a' {
		field("r=", cmd.Rows)
	}
	out = append(out, ';')
	if err != nil {
		out = append(out, err.Code...)
		out = append(out, ':')
		out = append(out, err.Msg...)
	} else {
		out = append(out, "OK"...)
	}
	out = append(out, "\x1b\\"...)
	return out, true
}

// transmit loads image data (spec §4). It returns the image the data
// belongs to, the first chunk's command with the resolved id (for the
// response), whether the transmission completed, and any error.
func (s *Storage) transmit(cmd Command, isQuery bool) (*Image, Command, bool, *Error) {
	medium := cmd.Medium
	if medium == 0 {
		medium = 'd'
	}
	continuation := medium == 'd' && s.pending != nil && s.pending.frame == nil
	var img *Image
	if !continuation {
		s.pending = nil
		if cmd.Width > MaxImageDimension || cmd.Height > MaxImageDimension {
			return nil, cmd, false, errorf("EINVAL",
				"Image too large, width or height greater than %d", MaxImageDimension)
		}
		s.trimUnreferenced(0)
		iid := cmd.ImageID
		if isQuery {
			iid = 0
		}
		img = s.findOrCreate(iid, cmd.ImageNumber)
		cmd.ImageID = img.ClientID
		spec := newLoadSpec(cmd)
		if err := spec.validate(cmd); err != nil {
			return nil, cmd, false, err
		}
		s.pending = &pendingLoad{start: cmd, spec: spec, image: img.internalID, isQuery: isQuery}
		if medium != 'd' {
			data, err := s.readMedium(medium, cmd)
			if err != nil {
				s.pending = nil
				return img, cmd, false, err
			}
			s.pending.buf = data
			cmd.More = false
			cmd.Payload = nil
		}
	} else {
		img = s.byInternal(s.pending.image)
		if img == nil {
			start := s.pending.start
			s.pending = nil
			return nil, start, false, errorf("EILSEQ",
				"More payload loading refers to non-existent image")
		}
		s.pending.start.More = cmd.More
	}

	p := s.pending
	start := p.start
	if len(p.buf)+len(cmd.Payload) > p.spec.capacity() {
		s.pending = nil
		return img, start, false, errorf("EFBIG", "Too much data")
	}
	p.buf = append(p.buf, cmd.Payload...)
	if cmd.More {
		return img, start, false, nil
	}
	s.pending = nil

	pix, err := p.spec.decode(p.buf)
	if err != nil {
		return img, start, false, err
	}
	if !p.isQuery {
		s.setPixels(img, pix, p.spec.format, start.Usage&UsageTransient != 0)
	}
	img.loaded = true
	return img, start, true, nil
}

// setPixels installs a freshly transmitted root frame.
func (s *Storage) setPixels(img *Image, pix *image.RGBA, format uint32, transient bool) {
	s.used -= img.used
	img.pix = pix
	img.width = pix.Rect.Dx()
	img.height = pix.Rect.Dy()
	bpp := 4
	if format == FormatRGB {
		bpp = 3
	}
	img.used = img.width * img.height * bpp
	s.used += img.used
	img.transient = transient
	img.version++
	img.anim = nil
	img.atime = s.tick()
}

func (s *Storage) tick() uint64 {
	s.clock++
	return s.clock
}

// findOrCreate returns the image with client id iid, wiped for
// re-transmission (spec §5), or a new image.
func (s *Storage) findOrCreate(iid, number uint32) *Image {
	if iid != 0 {
		if img := s.byClientID(iid); img != nil {
			s.freeResources(img)
			img.atime = s.tick()
			return img
		}
	}
	s.nextID++
	img := &Image{
		internalID: s.nextID,
		ClientID:   iid,
		Number:     number,
		termID:     term.NewImageID(),
		atime:      s.tick(),
	}
	if img.ClientID == 0 && img.Number != 0 {
		img.ClientID = s.freeClientID()
	}
	s.images = append(s.images, img)
	return img
}

// freeResources drops an image's placements and pixels ahead of a
// replacement transmission.
func (s *Storage) freeResources(img *Image) {
	img.refs = nil
	s.used -= img.used
	img.used = 0
	img.pix = nil
	img.width = 0
	img.height = 0
	img.loaded = false
	img.anim = nil
	img.version++
}

// freeClientID returns the smallest unused positive client id.
func (s *Storage) freeClientID() uint32 {
	ids := make([]uint32, 0, len(s.images))
	for _, img := range s.images {
		if img.ClientID != 0 {
			ids = append(ids, img.ClientID)
		}
	}
	slices.Sort(ids)
	var ans uint32 = 1
	for _, id := range ids {
		if id > ans {
			break
		}
		if id == ans {
			ans = id + 1
		}
	}
	return ans
}

func (s *Storage) byClientID(id uint32) *Image {
	for _, img := range s.images {
		if img.ClientID == id {
			return img
		}
	}
	return nil
}

// byNumber returns the newest image with the given number.
func (s *Storage) byNumber(number uint32) *Image {
	var ans *Image
	for _, img := range s.images {
		if img.Number == number && (ans == nil || img.internalID > ans.internalID) {
			ans = img
		}
	}
	return ans
}

func (s *Storage) byInternal(id uint64) *Image {
	for _, img := range s.images {
		if img.internalID == id {
			return img
		}
	}
	return nil
}

// lookup resolves the image a command addresses by id or number.
func (s *Storage) lookup(cmd Command) *Image {
	if cmd.ImageID != 0 {
		return s.byClientID(cmd.ImageID)
	}
	if cmd.ImageNumber != 0 {
		return s.byNumber(cmd.ImageNumber)
	}
	return nil
}

func (s *Storage) removeImage(img *Image) {
	s.used -= img.used
	if s.pending != nil && s.pending.image == img.internalID {
		s.pending = nil
	}
	s.images = slices.DeleteFunc(s.images, func(x *Image) bool { return x == img })
	if len(s.images) == 0 {
		s.used = 0
	}
}

// trimUnreferenced removes images that failed to load and id-less
// images without placements, sparing skip (kitty add_trim_predicate).
func (s *Storage) trimUnreferenced(skip uint64) {
	s.removeImages(func(img *Image) bool {
		if img.internalID == skip {
			return false
		}
		return !img.loaded || (img.ClientID == 0 && len(img.refs) == 0)
	})
}

func (s *Storage) removeImages(pred func(*Image) bool) {
	for i := 0; i < len(s.images); {
		if pred(s.images[i]) {
			s.removeImage(s.images[i])
			continue
		}
		i++
	}
}

// applyQuota evicts images until the store is under its limit
// (spec §14): first every unplaced or unloaded image other than the
// one just added, then transient and least recently used images.
func (s *Storage) applyQuota(added uint64) {
	s.removeImages(func(img *Image) bool {
		return img.internalID != added && (!img.loaded || len(img.refs) == 0)
	})
	if s.used <= s.Limit {
		return
	}
	sorted := slices.Clone(s.images)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.transient != b.transient {
			return a.transient
		}
		return a.atime < b.atime
	})
	for _, img := range sorted {
		if s.used <= s.Limit {
			break
		}
		s.removeImage(img)
	}
}

// put creates or replaces a placement (spec §8, §10). It returns the
// client id of the image for the response.
func (s *Storage) put(cmd Command, cur *Cursor, cell CellSize, img *Image) (uint32, *Error) {
	if cmd.Unicode != 0 && cmd.ParentID != 0 {
		return cmd.ImageID, errorf("EINVAL",
			"Put command creating a virtual placement cannot refer to a parent")
	}
	if img == nil {
		img = s.lookup(cmd)
		if img == nil {
			return cmd.ImageID, errorf("ENOENT",
				"Put command refers to non-existent image with id: %d and number: %d",
				cmd.ImageID, cmd.ImageNumber)
		}
	}
	if !img.loaded {
		return img.ClientID, errorf("ENOENT",
			"Put command refers to image with id: %d that could not load its data", cmd.ImageID)
	}

	var parentImg, parentRef uint64
	if cmd.ParentID != 0 {
		parent := s.byClientID(cmd.ParentID)
		if parent == nil {
			return cmd.ImageID, errorf("ENOPARENT",
				"Put command refers to a parent image with id: %d that does not exist", cmd.ParentID)
		}
		if len(parent.refs) == 0 {
			return cmd.ImageID, errorf("ENOPARENT",
				"Put command refers to a parent image with id: %d that has no placements", cmd.ParentID)
		}
		pref := parent.refs[0]
		if cmd.ParentPlacementID != 0 {
			pref = parent.refByClientID(cmd.ParentPlacementID)
			if pref == nil {
				return cmd.ImageID, errorf("ENOPARENT",
					"Put command refers to a parent image placement with id: %d and placement id: %d that does not exist",
					cmd.ParentID, cmd.ParentPlacementID)
			}
		}
		parentImg, parentRef = parent.internalID, pref.internalID
	}

	var ref *Placement
	if cmd.PlacementID != 0 && img.ClientID != 0 {
		if ref = img.refByClientID(cmd.PlacementID); ref != nil {
			if parentImg == img.internalID && parentRef == ref.internalID {
				return cmd.ImageID, errorf("EINVAL", "Put command refers to itself as its own parent")
			}
			if parentImg != 0 {
				oldImg, oldRef := ref.parentImg, ref.parentRef
				ref.parentImg, ref.parentRef = parentImg, parentRef
				err := s.checkAncestry(ref)
				ref.parentImg, ref.parentRef = oldImg, oldRef
				if err != nil {
					return cmd.ImageID, err
				}
			}
		}
	}
	if ref == nil {
		s.nextID++
		ref = &Placement{internalID: s.nextID}
		img.refs = append(img.refs, ref)
	}

	img.atime = s.tick()
	srcW, srcH := int(cmd.W), int(cmd.H)
	if srcW == 0 {
		srcW = img.width
	}
	if srcH == 0 {
		srcH = img.height
	}
	srcX, srcY := min(int(cmd.X), img.width), min(int(cmd.Y), img.height)
	srcW = min(srcW, img.width-srcX)
	srcH = min(srcH, img.height-srcY)
	ref.src = image.Rect(srcX, srcY, srcX+srcW, srcY+srcH)
	ref.Z = cmd.Z
	ref.StartRow, ref.StartCol = cur.Y, cur.X
	ref.cellX, ref.cellY = int(cmd.CellX), int(cmd.CellY)
	ref.clampOffsets(cell)
	ref.cols, ref.rows = int(cmd.Columns), int(cmd.Rows)
	if img.ClientID != 0 {
		ref.ClientID = cmd.PlacementID
	}
	ref.updateFootprint(cell)
	ref.parentImg, ref.parentRef = parentImg, parentRef
	ref.offX, ref.offY = cmd.OffsetH, cmd.OffsetV
	ref.Virtual = cmd.Unicode != 0
	if ref.Virtual {
		ref.StartRow, ref.StartCol = 0, 0
	}
	if ref.parentImg != 0 {
		if err := s.checkAncestry(ref); err != nil {
			img.removeRef(ref)
			return cmd.ImageID, err
		}
	} else if cmd.CursorMovement != 1 && !ref.Virtual {
		cur.X += ref.effCols
		if ref.effRows > 0 {
			cur.Y += ref.effRows - 1
		}
	}
	return img.ClientID, nil
}

func (p *Placement) clampOffsets(cell CellSize) {
	if cell.Width > 0 {
		p.cellX = min(p.cellX, cell.Width-1)
	}
	if cell.Height > 0 {
		p.cellY = min(p.cellY, cell.Height-1)
	}
}

// updateFootprint derives the cell footprint (spec §8.4).
func (p *Placement) updateFootprint(cell CellSize) {
	cols, rows := p.cols, p.rows
	srcW, srcH := p.src.Dx(), p.src.Dy()
	if cell.Width <= 0 || cell.Height <= 0 {
		p.effCols, p.effRows = cols, rows
		return
	}
	if cols == 0 {
		if rows == 0 {
			cols = ceilDiv(srcW+p.cellX, cell.Width)
		} else if srcH > 0 {
			heightPx := float64(cell.Height*rows + p.cellY)
			widthPx := heightPx * float64(srcW) / float64(srcH)
			cols = ceilDivF(widthPx, cell.Width)
		}
	}
	if rows == 0 {
		if p.cols == 0 {
			rows = ceilDiv(srcH+p.cellY, cell.Height)
		} else if srcW > 0 {
			widthPx := float64(cell.Width*cols + p.cellX)
			heightPx := widthPx * float64(srcH) / float64(srcW)
			rows = ceilDivF(heightPx, cell.Height)
		}
	}
	p.effCols, p.effRows = cols, rows
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func ceilDivF(a float64, b int) int {
	if a <= 0 {
		return 0
	}
	n := int(a) / b
	if float64(n*b) < a {
		n++
	}
	return n
}

func (img *Image) refByClientID(id uint32) *Placement {
	for _, r := range img.refs {
		if r.ClientID == id {
			return r
		}
	}
	return nil
}

func (img *Image) refByInternalID(id uint64) *Placement {
	for _, r := range img.refs {
		if r.internalID == id {
			return r
		}
	}
	return nil
}

func (img *Image) removeRef(ref *Placement) {
	img.refs = slices.DeleteFunc(img.refs, func(r *Placement) bool { return r == ref })
}

// checkAncestry validates a relative placement's parent chain
// (spec §10).
func (s *Storage) checkAncestry(ref *Placement) *Error {
	r := ref
	depth := 0
	for r.parentImg != 0 {
		if r == ref && depth > 0 {
			return errorf("ECYCLE", "This parent reference creates a cycle")
		}
		if depth >= parentDepthLimit {
			return errorf("ETOODEEP", "Too many levels of parent references")
		}
		depth++
		parent := s.byInternal(r.parentImg)
		if parent == nil {
			return errorf("ENOENT",
				"One of the ancestors of this ref with image id: %d not found", r.parentImg)
		}
		pref := parent.refByInternalID(r.parentRef)
		if pref == nil {
			return errorf("ENOENT",
				"One of the ancestors of this ref with image id: %d and ref id: %d not found",
				r.parentImg, r.parentRef)
		}
		r = pref
	}
	return nil
}

// delete implements a=d (spec §11).
func (s *Storage) delete(cmd Command, cur *Cursor) {
	s.pending = nil
	action := cmd.Delete
	if action == 0 {
		action = 'a'
	}
	if cmd.PlacementID == 0 {
		var img *Image
		switch action {
		case 'I':
			img = s.byClientID(cmd.ImageID)
		case 'N':
			img = s.byNumber(cmd.ImageNumber)
		case 'R':
			s.removeImages(func(img *Image) bool {
				return inIDRange(img, cmd) && len(img.refs) == 0
			})
		}
		if img != nil && len(img.refs) == 0 {
			s.removeImage(img)
			return
		}
	}
	free := action >= 'A' && action <= 'Z'
	switch action {
	case 'a', 'A':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return !r.Virtual && r.StartRow+r.effRows > 0
		})
	case 'i', 'I':
		s.filterRefs(free, true, func(img *Image, r *Placement) bool {
			return cmd.ImageID != 0 && img.ClientID == cmd.ImageID &&
				(cmd.PlacementID == 0 || r.ClientID == cmd.PlacementID)
		})
	case 'r', 'R':
		s.filterRefs(free, true, func(img *Image, _ *Placement) bool {
			return inIDRange(img, cmd)
		})
	case 'p', 'P':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return r.coversPoint(int(cmd.X), int(cmd.Y))
		})
	case 'q', 'Q':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return r.Z == cmd.Z && r.coversPoint(int(cmd.X), int(cmd.Y))
		})
	case 'x', 'X':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return r.coversColumn(int(cmd.X))
		})
	case 'y', 'Y':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return r.coversRow(int(cmd.Y))
		})
	case 'z', 'Z':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return !r.Virtual && r.Z == cmd.Z
		})
	case 'c', 'C':
		s.filterRefs(free, true, func(_ *Image, r *Placement) bool {
			return r.coversPoint(cur.X+1, cur.Y+1)
		})
	case 'n', 'N':
		img := s.byNumber(cmd.ImageNumber)
		if img == nil {
			return
		}
		img.refs = slices.DeleteFunc(img.refs, func(r *Placement) bool {
			return cmd.PlacementID == 0 || r.ClientID == cmd.PlacementID
		})
		if len(img.refs) == 0 && (free || img.ClientID == 0) {
			s.removeImage(img)
		}
	case 'f', 'F':
		if img := s.deleteFrame(cmd); img != nil {
			s.removeImage(img)
		}
	}
}

func inIDRange(img *Image, cmd Command) bool {
	return img.ClientID != 0 && cmd.X <= img.ClientID && img.ClientID <= cmd.Y
}

// coversColumn reports whether a 1-based column intersects the
// placement.
func (p *Placement) coversColumn(x int) bool {
	return !p.Virtual && p.StartCol <= x-1 && x-1 < p.StartCol+p.effCols
}

func (p *Placement) coversRow(y int) bool {
	return !p.Virtual && p.StartRow <= y-1 && y-1 < p.StartRow+p.effRows
}

func (p *Placement) coversPoint(x, y int) bool {
	return p.coversColumn(x) && p.coversRow(y)
}

// filterRefs removes the placements pred matches. An image left without
// placements is freed when it is id-less or free is set; with
// onlyMatched, only images that lost a placement are considered.
func (s *Storage) filterRefs(free, onlyMatched bool, pred func(*Image, *Placement) bool) {
	for i := 0; i < len(s.images); {
		img := s.images[i]
		matched := false
		img.refs = slices.DeleteFunc(img.refs, func(r *Placement) bool {
			if pred(img, r) {
				matched = true
				return true
			}
			return false
		})
		if (!onlyMatched || matched) && len(img.refs) == 0 && (free || img.ClientID == 0) {
			s.removeImage(img)
			continue
		}
		i++
	}
}

// Clear implements the erase-display interaction (spec §15): every
// placement when all is set, otherwise only those not entirely in
// history. Images left without placements are freed.
func (s *Storage) Clear(all bool) {
	s.filterRefs(true, false, func(_ *Image, r *Placement) bool {
		if r.Virtual {
			return false
		}
		return all || r.StartRow+r.effRows > 0
	})
}

// ClearHistory removes the placements that lie entirely in the
// scrollback, for an erase of the saved lines that keeps the screen.
func (s *Storage) ClearHistory() {
	s.filterRefs(true, false, func(_ *Image, r *Placement) bool {
		return !r.Virtual && r.StartRow+r.effRows <= 0
	})
}

// Scroll moves placements by amt rows (negative is up, the direction of
// a linefeed) and drops those that end up above limit, the negative
// depth of the scrollback (spec §15). With margins, only placements
// entirely inside the region move and they are clipped to it.
func (s *Storage) Scroll(amt, limit int, margins *Margins, cell CellSize) {
	if len(s.images) == 0 {
		return
	}
	var pred func(*Image, *Placement) bool
	if margins == nil {
		pred = func(_ *Image, r *Placement) bool {
			if r.Virtual {
				return false
			}
			r.StartRow += amt
			return r.StartRow+r.effRows <= limit
		}
	} else {
		pred = func(_ *Image, r *Placement) bool {
			return r.scrollWithinMargins(amt, *margins, cell)
		}
	}
	for i := 0; i < len(s.images); {
		img := s.images[i]
		img.refs = slices.DeleteFunc(img.refs, func(r *Placement) bool { return pred(img, r) })
		if len(img.refs) == 0 && img.ClientID == 0 && img.Number == 0 {
			s.removeImage(img)
			continue
		}
		i++
	}
}

func (p *Placement) withinRegion(m Margins) bool {
	return p.StartRow >= m.Top && p.StartRow+p.effRows-1 <= m.Bottom
}

func (p *Placement) outsideRegion(m Margins) bool {
	return p.StartRow+p.effRows <= m.Top || p.StartRow > m.Bottom
}

// scrollWithinMargins ports kitty's scroll_filter_margins_func and
// reports whether the placement should be removed.
func (p *Placement) scrollWithinMargins(amt int, m Margins, cell CellSize) bool {
	if p.Virtual || !p.withinRegion(m) {
		return false
	}
	p.StartRow += amt
	if p.outsideRegion(m) {
		return true
	}
	if p.StartRow < m.Top {
		clippedRows := m.Top - p.StartRow
		clipPx := cell.Height * clippedRows
		if p.src.Dy() <= clipPx {
			return true
		}
		p.src.Min.Y += clipPx
		p.effRows -= clippedRows
		p.StartRow += clippedRows
	} else if p.StartRow+p.effRows-1 > m.Bottom {
		clippedRows := p.StartRow + p.effRows - 1 - m.Bottom
		clipPx := cell.Height * clippedRows
		if p.src.Dy() <= clipPx {
			return true
		}
		p.src.Max.Y -= clipPx
		p.effRows -= clippedRows
	}
	return p.outsideRegion(m)
}

// Rows appends the buffer row every non-virtual placement starts on,
// given the buffer row the screen starts at. SetRows consumes the
// result in the same order.
func (s *Storage) Rows(screenTop int, out []int) []int {
	for _, img := range s.images {
		for _, r := range img.refs {
			if !r.Virtual {
				out = append(out, r.StartRow+screenTop)
			}
		}
	}
	return out
}

// SetRows moves every non-virtual placement to the buffer row at the
// matching index of rows, which must come from Rows.
func (s *Storage) SetRows(screenTop int, rows []int) {
	i := 0
	for _, img := range s.images {
		for _, r := range img.refs {
			if r.Virtual {
				continue
			}
			if i >= len(rows) {
				return
			}
			r.StartRow = rows[i] - screenTop
			i++
		}
	}
}

// Rescale recomputes footprints for a new cell size (spec §15).
func (s *Storage) Rescale(cell CellSize) {
	for _, img := range s.images {
		for _, r := range img.refs {
			if r.Virtual {
				continue
			}
			r.clampOffsets(cell)
			r.updateFootprint(cell)
		}
	}
}

// resolvePosition resolves a relative placement's anchor (spec §10).
// It reports false when the chain cannot be resolved; hasVirtual is
// then set when a virtual ancestor is the reason.
func (s *Storage) resolvePosition(ref *Placement, cellRefs cellRefLookup) (row, col int, ok, hasVirtual bool) {
	var x, y int32
	depth := 0
	r := ref
	for r.parentImg != 0 {
		if depth >= parentDepthLimit {
			return 0, 0, false, hasVirtual
		}
		depth++
		img := s.byInternal(r.parentImg)
		if img == nil {
			return 0, 0, false, hasVirtual
		}
		parent := img.refByInternalID(r.parentRef)
		if parent == nil {
			return 0, 0, false, hasVirtual
		}
		if parent.Virtual {
			hasVirtual = true
			prow, pcol, found := cellRefs(img.ClientID, parent)
			if !found {
				return 0, 0, false, hasVirtual
			}
			parent = &Placement{StartRow: prow, StartCol: pcol}
		}
		x += r.offX
		y += r.offY
		r = parent
	}
	return r.StartRow + int(y), r.StartCol + int(x), true, hasVirtual
}

func (img *Image) ensureAnimation() *animation {
	if img.anim == nil {
		img.anim = &animation{frames: []*frame{{pix: img.pix}}}
	}
	return img.anim
}

func (img *Image) frameCount() int {
	if img.anim == nil {
		return 1
	}
	return len(img.anim.frames)
}

// frameForNumber resolves a 1-based frame number.
func (img *Image) frameForNumber(n uint32) *frame {
	if n == 0 || int(n) > img.frameCount() {
		return nil
	}
	return img.ensureAnimation().frames[n-1]
}

func (img *Image) currentFrame() *frame {
	if img.anim == nil || img.anim.current >= len(img.anim.frames) {
		return nil
	}
	return img.anim.frames[img.anim.current]
}

// showCurrentFrame makes the current frame the displayed pixels.
func (img *Image) showCurrentFrame(now time.Time) {
	f := img.currentFrame()
	if f == nil {
		return
	}
	img.pix = f.pix
	img.version++
	img.anim.shownAt = now
}

// frameBytes is the memory charged to animation frames beyond the root.
func (s *Storage) frameBytes() int {
	total := 0
	for _, img := range s.images {
		if img.anim == nil {
			continue
		}
		for _, f := range img.anim.frames[1:] {
			total += len(f.pix.Pix)
		}
	}
	return total
}

// handleAnimation dispatches a=f and a=a (spec §13.1, §13.2).
func (s *Storage) handleAnimation(cmd Command) ([]byte, bool) {
	pendingFrame := s.pending != nil && s.pending.frame != nil
	if cmd.ImageID == 0 && cmd.ImageNumber == 0 && !pendingFrame {
		return nil, false
	}
	var img *Image
	if pendingFrame {
		img = s.byInternal(s.pending.image)
	} else {
		img = s.lookup(cmd)
	}
	if img == nil {
		s.pending = nil
		return s.response(cmd, false, errorf("ENOENT",
			"Animation command refers to non-existent image with id: %d and number: %d",
			cmd.ImageID, cmd.ImageNumber))
	}
	if cmd.Action == 'a' {
		s.controlAnimation(cmd, img)
		return nil, false
	}
	start, loaded, err := s.loadFrame(cmd, img)
	// Unlike a=t, kitty answers a=f with the command just received rather
	// than the one that started a chunked load, so a chunked frame gets no
	// response unless the last chunk repeats the id (kitty
	// graphics.c:2492-2498).
	resp := cmd
	resp.Rows = start.Rows
	if resp.Quiet == 0 {
		resp.Quiet = start.Quiet
	}
	return s.response(resp, loaded, err)
}

// loadFrame implements a=f. It returns the command to respond with,
// whether the frame completed, and any error.
func (s *Storage) loadFrame(cmd Command, img *Image) (Command, bool, *Error) {
	medium := cmd.Medium
	if medium == 0 {
		medium = 'd'
	}
	continuation := medium == 'd' && s.pending != nil && s.pending.frame != nil &&
		s.pending.image == img.internalID
	if !continuation {
		s.pending = nil
		number := cmd.Rows
		total := uint32(img.frameCount())
		if number == 0 || number > total+1 {
			number = total + 1
		}
		cmd.Rows = number
		if cmd.Width > MaxImageDimension || cmd.Height > MaxImageDimension {
			return cmd, false, errorf("EINVAL",
				"Image too large, width or height greater than %d", MaxImageDimension)
		}
		spec := newLoadSpec(cmd)
		if err := spec.validate(cmd); err != nil {
			return cmd, false, err
		}
		s.pending = &pendingLoad{
			start: cmd, spec: spec, image: img.internalID,
			frame: &frameLoad{number: number, isNew: number == total+1},
		}
		if medium != 'd' {
			data, err := s.readMedium(medium, cmd)
			if err != nil {
				s.pending = nil
				return cmd, false, err
			}
			s.pending.buf = data
			cmd.More = false
			cmd.Payload = nil
		}
	} else {
		s.pending.start.More = cmd.More
	}

	p := s.pending
	start := p.start
	if len(p.buf)+len(cmd.Payload) > p.spec.capacity() {
		s.pending = nil
		return start, false, errorf("EFBIG", "Too much data")
	}
	p.buf = append(p.buf, cmd.Payload...)
	if cmd.More {
		return start, false, nil
	}
	s.pending = nil

	data, err := p.spec.decode(p.buf)
	if err != nil {
		return start, false, err
	}
	if data.Rect.Dx() > img.width {
		return start, false, errorf("EINVAL", "Frame width %d larger than image width: %d",
			data.Rect.Dx(), img.width)
	}
	if data.Rect.Dy() > img.height {
		return start, false, errorf("EINVAL", "Frame height %d larger than image height: %d",
			data.Rect.Dy(), img.height)
	}
	if err := s.installFrame(start, img, p.frame, data); err != nil {
		return start, false, err
	}
	return start, true, nil
}

// installFrame composes decoded frame data into a new or existing frame.
func (s *Storage) installFrame(cmd Command, img *Image, fl *frameLoad, data *image.RGBA) *Error {
	anim := img.ensureAnimation()
	// kitty reads the composition mode from C; the docs say X (spec §17.1).
	compose := cmd.CursorMovement
	if compose == 0 {
		compose = cmd.CellX
	}
	blend := compose != 1 && cmd.Format != FormatRGB
	transient := cmd.Usage&UsageTransient != 0
	offset := image.Pt(int(cmd.X), int(cmd.Y))
	gap := int32(defaultFrameGap)
	switch {
	case cmd.Z > 0:
		gap = cmd.Z
	case cmd.Z < 0:
		gap = 0
	}

	if fl.isNew {
		frameSize := img.width * img.height * 4
		if s.Limit > 0 && s.frameBytes()+frameSize > s.Limit*frameCacheMultiplier {
			s.removeImages(func(x *Image) bool {
				return x != img && (!x.loaded || len(x.refs) == 0)
			})
			if s.frameBytes()+frameSize > s.Limit*frameCacheMultiplier {
				return errorf("ENOSPC", "Cache size exceeded cannot add new frames")
			}
		}
		canvas := image.NewRGBA(image.Rect(0, 0, img.width, img.height))
		if cmd.Columns != 0 {
			base := img.frameForNumber(cmd.Columns)
			if base == nil {
				return errorf("EINVAL", "No frame with number: %d found", cmd.Columns)
			}
			copy(canvas.Pix, base.pix.Pix)
			transient = transient || base.transient
		} else if cmd.CellY != 0 {
			fillColor(canvas, cmd.CellY)
		}
		composeOnto(canvas, data, offset, blend)
		anim.frames = append(anim.frames, &frame{pix: canvas, gap: gap, transient: transient})
		anim.duration += int64(gap)
		return nil
	}

	f := img.frameForNumber(fl.number)
	if f == nil {
		return errorf("EINVAL", "No frame with number: %d found", fl.number)
	}
	if cmd.Z != 0 {
		anim.changeGap(f, gap)
	}
	f.transient = f.transient || transient
	composeOnto(f.pix, data, offset, blend)
	if f == img.currentFrame() {
		img.version++
	}
	return nil
}

// controlAnimation implements a=a (spec §13.2). It never responds.
func (s *Storage) controlAnimation(cmd Command, img *Image) {
	if !img.loaded {
		return
	}
	anim := img.ensureAnimation()
	if cmd.Rows != 0 && cmd.Z != 0 {
		if f := img.frameForNumber(cmd.Rows); f != nil {
			anim.changeGap(f, cmd.Z)
		}
	}
	if cmd.Columns != 0 {
		idx := int(cmd.Columns) - 1
		if idx != anim.current && idx < len(anim.frames) {
			anim.current = idx
			img.showCurrentFrame(time.Now())
		}
	}
	if cmd.Width != 0 {
		old := anim.state
		switch cmd.Width {
		case 1:
			anim.state = animationStopped
		case 2:
			anim.state = animationLoading
		case 3:
			anim.state = animationRunning
		}
		if anim.state != animationStopped && old == animationStopped {
			anim.shownAt = time.Now()
			anim.drawn = true
		}
		anim.currentLoop = 0
	}
	if cmd.Height != 0 {
		anim.maxLoops = cmd.Height - 1
	}
}

// deleteFrame implements d=f and d=F (spec §13.4). It returns the image
// when the whole image must be removed.
func (s *Storage) deleteFrame(cmd Command) *Image {
	if cmd.ImageID == 0 && cmd.ImageNumber == 0 {
		return nil
	}
	img := s.lookup(cmd)
	if img == nil {
		return nil
	}
	total := img.frameCount()
	number := min(total, int(cmd.Rows))
	if number == 0 {
		number = 1
	}
	if total == 1 {
		if cmd.Delete == 'F' {
			return img
		}
		return nil
	}
	anim := img.anim
	idx := number - 1
	removed := anim.frames[idx]
	anim.frames = append(anim.frames[:idx], anim.frames[idx+1:]...)
	if int64(removed.gap) < anim.duration {
		anim.duration -= int64(removed.gap)
	} else {
		anim.duration = 0
	}
	switch {
	case anim.current >= len(anim.frames):
		anim.current = len(anim.frames) - 1
		img.showCurrentFrame(time.Now())
	case idx == anim.current:
		img.showCurrentFrame(time.Now())
	case idx < anim.current:
		anim.current--
	}
	return nil
}

// handleCompose implements a=c (spec §13.3).
func (s *Storage) handleCompose(cmd Command) ([]byte, bool) {
	if cmd.ImageID == 0 && cmd.ImageNumber == 0 {
		return nil, false
	}
	img := s.lookup(cmd)
	if img == nil {
		return s.response(cmd, false, errorf("ENOENT",
			"Animation command refers to non-existent image with id: %d and number: %d",
			cmd.ImageID, cmd.ImageNumber))
	}
	return s.response(cmd, true, s.compose(cmd, img))
}

func (s *Storage) compose(cmd Command, img *Image) *Error {
	if !img.loaded {
		return errorf("ENOENT", "No source frame number %d exists in image id: %d", cmd.Rows, img.ClientID)
	}
	src := img.frameForNumber(cmd.Rows)
	if src == nil {
		return errorf("ENOENT", "No source frame number %d exists in image id: %d", cmd.Rows, img.ClientID)
	}
	dst := img.frameForNumber(cmd.Columns)
	if dst == nil {
		return errorf("ENOENT", "No destination frame number %d exists in image id: %d", cmd.Columns, img.ClientID)
	}
	width, height := uint64(cmd.W), uint64(cmd.H)
	if width == 0 {
		width = uint64(img.width)
	}
	if height == 0 {
		height = uint64(img.height)
	}
	dstX, dstY := uint64(cmd.X), uint64(cmd.Y)
	srcX, srcY := uint64(cmd.CellX), uint64(cmd.CellY)
	if dstX+width > uint64(img.width) || dstY+height > uint64(img.height) {
		return errorf("EINVAL", "The destination rectangle is out of bounds")
	}
	if srcX+width > uint64(img.width) || srcY+height > uint64(img.height) {
		return errorf("EINVAL", "The source rectangle is out of bounds")
	}
	if src == dst {
		xOverlaps := max(srcX, dstX) < min(srcX, dstX)+width
		yOverlaps := max(srcY, dstY) < min(srcY, dstY)+height
		if xOverlaps && yOverlaps {
			return errorf("EINVAL",
				"The source and destination rectangles overlap and the src and destination frames are the same")
		}
	}
	composeRect(dst.pix, image.Pt(int(dstX), int(dstY)), src.pix, image.Pt(int(srcX), int(srcY)),
		image.Pt(int(width), int(height)), cmd.CursorMovement == 0)
	dst.transient = dst.transient || src.transient
	if dst == img.currentFrame() {
		img.version++
	}
	return nil
}

// Animate advances running animations to now (spec §13.2). It reports
// whether any displayed frame changed and, when an animation is still
// running, how long until the next frame is due.
func (s *Storage) Animate(now time.Time) (changed bool, next time.Duration, running bool) {
	for _, img := range s.images {
		anim := img.anim
		if anim == nil || !img.animatable() {
			continue
		}
		f := img.currentFrame()
		if f == nil {
			continue
		}
		due := anim.shownAt.Add(time.Duration(f.gap) * time.Millisecond)
		if !now.Before(due) {
			if img.advanceFrame() {
				changed = true
				img.showCurrentFrame(now)
				f = img.currentFrame()
				due = now.Add(time.Duration(f.gap) * time.Millisecond)
			} else {
				continue
			}
		}
		wait := due.Sub(now)
		if !running || wait < next {
			next = wait
		}
		running = true
	}
	return changed, next, running
}

func (img *Image) animatable() bool {
	a := img.anim
	return a != nil && a.state != animationStopped && len(a.frames) > 1 && a.drawn &&
		a.duration != 0 && (a.maxLoops == 0 || a.currentLoop < a.maxLoops)
}

// advanceFrame moves to the next frame with a non-zero gap, reporting
// false when the animation must wait at its end.
func (img *Image) advanceFrame() bool {
	a := img.anim
	for {
		next := (a.current + 1) % len(a.frames)
		if next == 0 {
			if a.state == animationLoading {
				return false
			}
			a.currentLoop++
			if a.maxLoops != 0 && a.currentLoop >= a.maxLoops {
				return false
			}
		}
		a.current = next
		if a.frames[next].gap != 0 {
			return true
		}
	}
}

// Visible returns the placements to draw for v in draw order: by
// z-index, then image, then placement (spec §8.5, §16). runs are the
// placeholder runs of the visible rows (spec §9); they are drawn as
// well and anchor relative placements with virtual parents (spec §10).
// Relative placements whose parent chain no longer resolves are
// removed, along with images that lose their last placement.
func (s *Storage) Visible(v View, runs []PlaceholderRun) []term.Image {
	if len(s.images) == 0 {
		return nil
	}
	clip := image.Rect(0, 0, v.Width, v.Height)
	lookup := func(imageID uint32, virt *Placement) (int, int, bool) {
		return runsAnchor(runs, imageID, virt)
	}
	var items []drawItem
	for i := 0; i < len(s.images); {
		img := s.images[i]
		if img.anim != nil {
			img.anim.drawn = false
		}
		refRemoved := false
		for j := 0; j < len(img.refs); {
			ref := img.refs[j]
			if ref.Virtual {
				j++
				continue
			}
			row, col := ref.StartRow, ref.StartCol
			if ref.parentImg != 0 {
				var ok, hasVirtual bool
				row, col, ok, hasVirtual = s.resolvePosition(ref, lookup)
				if !ok {
					if !hasVirtual {
						img.refs = append(img.refs[:j], img.refs[j+1:]...)
						refRemoved = true
					} else {
						j++
					}
					continue
				}
			}
			j++
			row += v.ScrolledBy
			if row >= v.Height || row+ref.effRows <= 0 || ref.src.Empty() || img.pix == nil {
				continue
			}
			if img.anim != nil {
				img.anim.drawn = true
			}
			items = append(items, drawItem{
				img: term.Image{
					Src:     img.pix,
					ID:      img.termID,
					Version: img.version,
					Crop:    ref.src,
					Pos:     term.Coordinates{X: col, Y: row},
					Offset:  image.Pt(ref.cellX, ref.cellY),
					Width:   ref.effCols,
					Height:  ref.effRows,
					Fit:     term.ImageFitFill,
					Layer:   layerForZ(ref.Z),
					Clip:    clip,
				},
				z: ref.Z, image: img.internalID, ref: ref.internalID,
			})
		}
		if refRemoved && len(img.refs) == 0 {
			s.removeImage(img)
			continue
		}
		i++
	}
	for _, run := range runs {
		if item, ok := s.cellImage(run, v); ok {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.z != b.z {
			return a.z < b.z
		}
		if a.image != b.image {
			return a.image < b.image
		}
		return a.ref < b.ref
	})
	out := make([]term.Image, len(items))
	for i, it := range items {
		out[i] = it.img
	}
	return out
}

// virtualPlacement finds the virtual placement a placeholder run shows.
func (img *Image) virtualPlacement(placementID uint32) *Placement {
	for _, r := range img.refs {
		if r.Virtual && (placementID == 0 || r.ClientID == placementID) {
			return r
		}
	}
	return nil
}

// cellImage builds the draw item for a placeholder run: the image is
// fitted into the virtual placement's cell box preserving its aspect
// ratio and centred, and the run shows its own cells of that box
// (kitty grman_put_cell_image). Cell images draw at z = -1 so text
// stays on top.
func (s *Storage) cellImage(run PlaceholderRun, v View) (drawItem, bool) {
	img := s.byClientID(run.ImageID)
	if img == nil || img.pix == nil || !img.loaded {
		return drawItem{}, false
	}
	virt := img.virtualPlacement(run.PlacementID)
	if virt == nil {
		return drawItem{}, false
	}
	cols, rows := virt.cols, virt.rows
	if cols == 0 {
		if v.Cell.Width <= 0 {
			return drawItem{}, false
		}
		cols = ceilDiv(img.width, v.Cell.Width)
	}
	if rows == 0 {
		if v.Cell.Height <= 0 {
			return drawItem{}, false
		}
		rows = ceilDiv(img.height, v.Cell.Height)
	}
	if run.ImgRow >= rows || run.ImgCol >= cols {
		return drawItem{}, false
	}
	if img.anim != nil {
		img.anim.drawn = true
	}
	img.atime = s.tick()
	box := image.Rect(run.Col, run.Row, run.Col+run.Len, run.Row+1).
		Intersect(image.Rect(0, 0, v.Width, v.Height))
	if box.Empty() {
		return drawItem{}, false
	}
	return drawItem{
		img: term.Image{
			Src:     img.pix,
			ID:      img.termID,
			Version: img.version,
			Pos:     term.Coordinates{X: run.Col - run.ImgCol, Y: run.Row - run.ImgRow},
			Width:   cols,
			Height:  rows,
			Fit:     term.ImageFitContain,
			Clip:    box,
		},
		z: -1, image: img.internalID, ref: virt.internalID,
	}, true
}
