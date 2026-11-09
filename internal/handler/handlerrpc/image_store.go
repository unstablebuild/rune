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

package handlerrpc

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"runtime"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// DefaultMaxImageBytes is used when Graphics.MaxBytes is zero.
const DefaultMaxImageBytes = 64 << 20

// GraphicsConfig enables images on a ClientStream. They are advertised while the
// frames it draws carry a term.CellPixelSizeFromContext.
type GraphicsConfig struct {
	// ArenaRoot is where arenas are created. Empty means /dev/shm on Linux and
	// streaming elsewhere: on macOS, rewriting a file reaches the disk on every
	// frame. Images are disabled if the arena cannot be created.
	ArenaRoot string
	// StreamPixels streams pixels even where there is an arena.
	StreamPixels bool
	// MaxBytes bounds the pixel bytes each stream keeps; zero means
	// DefaultMaxImageBytes.
	MaxBytes uint64
}

type imageStore struct {
	cfg     GraphicsConfig
	enabled bool
	// root confines reads to the arena, which the extension can write to.
	root       *os.Root
	dir        string
	advertised bool
	closed     bool
	ack        uint64
	bytes      uint64
	images     map[uint64]*hostImage
	// drawn are the buffers of the last Draw, which its writers may read until
	// their next Clear.
	drawn  []*image.RGBA
	warned bool
	log    func(level log.Level, msg string, args ...any)
}

type hostImage struct {
	// id is process-unique: writers key retained pixels by it, and every
	// extension numbers its images from scratch.
	id                     term.ImageID
	version                uint64
	pixels                 *image.RGBA
	spare                  *image.RGBA
	latestBytes, prevBytes uint64
}

type uploadJob struct {
	upload  *handlerrpc.ImageUpload
	records []*handlerrpc.ImageUpload
	root    *os.Root
	dst     *image.RGBA
	err     error
}

type pixelSize struct {
	width, height float64
}

// streamedUploads holds the records of the responses marked more until
// the response they come with.
type streamedUploads struct {
	records []*handlerrpc.ImageUpload
	bytes   uint64
}

// add keeps the records of resp, a response marked more. Past maxBytes
// their pixels are dropped, which fails the checks of their upload, so
// that an extension cannot make the host buffer more.
func (u *streamedUploads) add(resp *handlerrpc.DrawStreamResponse, maxBytes uint64) {
	for _, up := range resp.GetUploads() {
		u.bytes += uint64(len(up.GetData()))
		if u.bytes > maxBytes {
			up.Data = nil
		}
	}
	u.records = append(u.records, resp.GetUploads()...)
}

// attach puts the records kept before the uploads of resp, the response
// they come with.
func (u *streamedUploads) attach(resp *handlerrpc.DrawStreamResponse) {
	if len(u.records) > 0 {
		resp.Uploads = append(u.records, resp.GetUploads()...)
	}
	u.records, u.bytes = nil, 0
}

func pixelSizeOf(ctx context.Context) pixelSize {
	width, height, _ := term.CellPixelSizeFromContext(ctx)
	return pixelSize{width: width, height: height}
}

func (st *imageStore) maxBytes() uint64 {
	if st.cfg.MaxBytes == 0 {
		return DefaultMaxImageBytes
	}
	return st.cfg.MaxBytes
}

func (st *imageStore) graphics(cell pixelSize) *handlerrpc.Graphics {
	if !st.enabled || cell == (pixelSize{}) || st.closed {
		return nil
	}
	if !st.advertised {
		if err := st.openArena(); err != nil {
			st.enabled = false
			st.log(log.WarnLevel, "extension images disabled: %v", err)
			return nil
		}
		st.advertised = true
	}
	return &handlerrpc.Graphics{
		CellWidth:  cell.width,
		CellHeight: cell.height,
		ArenaDir:   st.dir,
		MaxBytes:   st.maxBytes(),
	}
}

func (st *imageStore) openArena() error {
	parent := st.cfg.ArenaRoot
	if parent == "" && runtime.GOOS == "linux" {
		parent = "/dev/shm"
	}
	if parent == "" || st.cfg.StreamPixels {
		return nil
	}
	dir, err := os.MkdirTemp(parent, "rune-img-*")
	if err != nil {
		return fmt.Errorf("create arena: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("open arena: %w", err)
	}
	st.root, st.dir = root, dir
	return nil
}

func (st *imageStore) close() {
	st.closed = true
	if st.root == nil {
		return
	}
	_ = st.root.Close()
	if err := os.RemoveAll(st.dir); err != nil {
		st.log(log.WarnLevel, "remove image arena: %v", err)
	}
	st.root = nil
}

func (st *imageStore) beginDraw() {
	clear(st.drawn)
	st.drawn = st.drawn[:0]
}

func (st *imageStore) plan(resp *handlerrpc.DrawStreamResponse) []uploadJob {
	var uploads [][]*handlerrpc.ImageUpload
	all := resp.GetUploads()
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].GetSeq() == all[start].GetSeq() {
			end++
		}
		uploads = append(uploads, all[start:end])
		start = end
	}
	if len(uploads) == 0 {
		return nil
	}
	last := make(map[uint64]int, len(uploads))
	for i, records := range uploads {
		last[records[0].GetId()] = i
	}
	bytes := st.bytes
	jobs := make([]uploadJob, 0, len(last))
	for i, records := range uploads {
		up := records[0]
		if last[up.GetId()] != i {
			continue
		}
		job := uploadJob{upload: up, records: records, root: st.root}
		job.err = st.check(records, &bytes)
		if job.err == nil {
			job.dst = st.reusable(up)
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func (st *imageStore) check(records []*handlerrpc.ImageUpload, bytes *uint64) error {
	if !st.advertised {
		return errors.New("images were not advertised")
	}
	up := records[0]
	width, height := up.GetWidth(), up.GetHeight()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid upload size %dx%d", width, height)
	}
	limit := st.maxBytes()
	if uint64(width)*uint64(height) > limit/4 {
		return fmt.Errorf("upload of %dx%d exceeds %d bytes", width, height, limit)
	}
	size := 4 * uint64(width) * uint64(height)
	var prev uint64
	if img := st.images[up.GetId()]; img != nil {
		prev = img.prevBytes
	}
	if *bytes-prev+size > limit {
		return fmt.Errorf("upload of %dx%d exceeds %d bytes with the pixels kept",
			width, height, limit)
	}
	if st.root != nil {
		if len(records) != 1 {
			return fmt.Errorf("arena upload split into %d records", len(records))
		}
		if !validUploadName(up.GetFile()) {
			return fmt.Errorf("invalid upload file name %q", up.GetFile())
		}
	} else if err := checkStreamed(records, size); err != nil {
		return err
	}
	*bytes = *bytes - prev + size
	return nil
}

func checkStreamed(records []*handlerrpc.ImageUpload, size uint64) error {
	first := records[0]
	var n uint64
	for _, r := range records {
		if r.GetId() != first.GetId() || r.GetVersion() != first.GetVersion() ||
			r.GetWidth() != first.GetWidth() || r.GetHeight() != first.GetHeight() {
			return errors.New("the streamed records of an upload disagree")
		}
		if r.GetOffset() != n {
			return fmt.Errorf("streamed record at offset %d, want %d", r.GetOffset(), n)
		}
		n += uint64(len(r.GetData()))
	}
	if n != size {
		return fmt.Errorf("streamed upload holds %d bytes, want %d", n, size)
	}
	return nil
}

func (st *imageStore) reusable(up *handlerrpc.ImageUpload) *image.RGBA {
	img := st.images[up.GetId()]
	if img == nil || img.spare == nil || slices.Contains(st.drawn, img.spare) {
		return nil
	}
	width, height := int(up.GetWidth()), int(up.GetHeight())
	size := 4 * width * height
	if cap(img.spare.Pix) < size {
		return nil
	}
	if img.spare.Rect.Dx() == width && img.spare.Rect.Dy() == height {
		return img.spare
	}
	return &image.RGBA{
		Pix:    img.spare.Pix[:size],
		Stride: 4 * width,
		Rect:   image.Rect(0, 0, width, height),
	}
}

// readUploads runs without the stream lock, so a frame being read never
// stalls the UI; it must not touch the store.
func readUploads(jobs []uploadJob) {
	for i := range jobs {
		job := &jobs[i]
		if job.err != nil {
			continue
		}
		if job.dst == nil {
			job.dst = image.NewRGBA(image.Rect(0, 0,
				int(job.upload.GetWidth()), int(job.upload.GetHeight())))
		}
		if job.root == nil {
			off := 0
			for _, r := range job.records {
				off += copy(job.dst.Pix[off:], r.GetData())
			}
			continue
		}
		job.err = readUpload(job.root, job.upload.GetFile(), job.dst.Pix)
	}
}

func readUpload(root *os.Root, name string, dst []byte) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("upload %s is not a regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("upload %s was replaced while opening it", name)
	}
	if opened.Size() < int64(len(dst)) {
		return fmt.Errorf("upload %s holds %d bytes, want %d",
			name, opened.Size(), len(dst))
	}
	if _, err := f.ReadAt(dst, 0); err != nil {
		return fmt.Errorf("read upload %s: %w", name, err)
	}
	return nil
}

func (st *imageStore) commit(resp *handlerrpc.DrawStreamResponse, jobs []uploadJob) {
	for _, up := range resp.GetUploads() {
		st.ack = max(st.ack, up.GetSeq())
	}
	for _, job := range jobs {
		id := job.upload.GetId()
		if job.err != nil {
			if !st.warned {
				st.warned = true
				st.log(log.WarnLevel, "drop extension image %d: %v", id, job.err)
			}
			st.drop(id)
			continue
		}
		img := st.images[id]
		if img == nil {
			if st.images == nil {
				st.images = make(map[uint64]*hostImage)
			}
			img = &hostImage{id: term.NewImageID()}
			st.images[id] = img
		}
		size := uint64(len(job.dst.Pix))
		st.bytes = st.bytes - img.prevBytes + size
		img.prevBytes, img.latestBytes = img.latestBytes, size
		img.spare, img.pixels = img.pixels, job.dst
		img.version = job.upload.GetVersion()
	}
	for _, id := range resp.GetReleased() {
		st.drop(id)
	}
}

func (st *imageStore) drop(id uint64) {
	img := st.images[id]
	if img == nil {
		return
	}
	st.bytes -= img.latestBytes + img.prevBytes
	delete(st.images, id)
}

func (st *imageStore) draw(w term.Writer, placements []*handlerrpc.ImagePlacement) {
	for _, p := range placements {
		img := st.images[p.GetId()]
		if img == nil || img.pixels == nil || img.version != p.GetVersion() {
			continue
		}
		placed := p.ToModel()
		placed.ID = img.id
		placed.Src = img.pixels
		st.drawn = append(st.drawn, img.pixels)
		if !w.DrawImage(placed) {
			return
		}
	}
}

func validUploadName(name string) bool {
	prefix, slot, ok := strings.Cut(name, "-")
	if !ok || (slot != "0" && slot != "1") || prefix == "" || len(prefix) > 20 {
		return false
	}
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
