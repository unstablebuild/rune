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
	"image"
	"os"
	"path/filepath"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
)

func newTestImageStore(t *testing.T, maxBytes uint64, stream bool) *imageStore {
	t.Helper()
	st := &imageStore{
		cfg: GraphicsConfig{
			ArenaRoot:    t.TempDir(),
			StreamPixels: stream,
			MaxBytes:     maxBytes,
		},
		enabled: true,
		log:     func(log.Level, string, ...any) {},
	}
	require.NotNil(t, st.graphics(pixelSize{width: 8, height: 16}))
	require.Equal(t, stream, st.root == nil)
	t.Cleanup(st.close)
	return st
}

func (st *imageStore) apply(resp *handlerrpc.DrawStreamResponse) {
	jobs := st.plan(resp)
	readUploads(jobs)
	st.commit(resp, jobs)
}

func (st *imageStore) drawnIDs(t *testing.T, pix []byte) []uint64 {
	t.Helper()
	w := new(handlerrpctest.ImageRecorder)
	st.beginDraw()
	st.draw(w, []*handlerrpc.ImagePlacement{
		{Id: 1, Version: 1, Width: 1, Height: 1},
		{Id: 2, Version: 1, Width: 1, Height: 1},
	})
	var drawn []uint64
	for _, img := range w.Images {
		assert.Equal(t, pix, img.Pix)
		for id, held := range st.images {
			if held.id == img.ID {
				drawn = append(drawn, id)
			}
		}
	}
	return drawn
}

func TestImageStoreChecksUploads(t *testing.T) {
	const w, h = 2, 2
	pix := handlerrpctest.Gradient(w, h, 1).Pix
	valid := &handlerrpc.ImageUpload{Id: 2, Version: 1, Seq: 2, File: "2-0", Width: w, Height: h}

	tests := []struct {
		name     string
		maxBytes uint64
		prepare  func(t *testing.T, dir string) *handlerrpc.ImageUpload
		// drawn defaults to image 2 alone.
		drawn []uint64
	}{
		{
			name: "a regular file",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-1", pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-1", Width: w, Height: h}
			},
			drawn: []uint64{1, 2},
		},
		{
			name: "a name outside the protocol",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-2", pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-2", Width: w, Height: h}
			},
		},
		{
			name: "a path out of the arena",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, filepath.Dir(dir), "1-0", pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "../1-0", Width: w, Height: h}
			},
		},
		{
			name: "a symlink",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "9-0", pix)
				require.NoError(t, os.Symlink(filepath.Join(dir, "9-0"), filepath.Join(dir, "1-0")))
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w, Height: h}
			},
		},
		{
			name: "a directory",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "1-0"), 0o700))
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w, Height: h}
			},
		},
		{
			name: "a missing file",
			prepare: func(*testing.T, string) *handlerrpc.ImageUpload {
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w, Height: h}
			},
		},
		{
			name: "a file shorter than its pixels",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-0", pix[:len(pix)-1])
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w, Height: h}
			},
		},
		{
			name: "no pixels",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-0", pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: 0, Height: h}
			},
		},
		{
			name:     "more pixels than the budget",
			maxBytes: 4*w*h + 1,
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-0", handlerrpctest.Gradient(w+1, h, 1).Pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w + 1, Height: h}
			},
		},
		{
			name:     "more pixels than the budget leaves with the pixels kept",
			maxBytes: 4*w*h + 4*w*h - 1,
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-0", pix)
				return &handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, File: "1-0", Width: w, Height: h}
			},
			drawn: []uint64{1},
		},
		{
			name: "a size whose bytes overflow",
			prepare: func(t *testing.T, dir string) *handlerrpc.ImageUpload {
				upload(t, dir, "1-0", pix)
				return &handlerrpc.ImageUpload{
					Id: 1, Version: 1, Seq: 1, File: "1-0", Width: 1<<31 - 1, Height: 1<<31 - 1,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newTestImageStore(t, tt.maxBytes, false)
			up := tt.prepare(t, st.dir)
			upload(t, st.dir, valid.GetFile(), pix)
			st.apply(&handlerrpc.DrawStreamResponse{
				Uploads: []*handlerrpc.ImageUpload{up, valid},
			})

			want := tt.drawn
			if want == nil {
				want = []uint64{2}
			}
			assert.Equal(t, want, st.drawnIDs(t, pix),
				"an upload that fails its checks must drop only its own image")
			assert.EqualValues(t, 2, st.ack, "every upload must be acked")
		})
	}
}

func TestImageStoreChecksStreamedUploads(t *testing.T) {
	const w, h = 2, 2
	pix := handlerrpctest.Gradient(w, h, 1).Pix
	part := func(from, to int) *handlerrpc.ImageUpload {
		return &handlerrpc.ImageUpload{
			Id: 1, Version: 1, Seq: 1, Width: w, Height: h,
			Offset: uint64(from), Data: pix[from:to],
		}
	}
	valid := &handlerrpc.ImageUpload{Id: 2, Version: 1, Seq: 2, Width: w, Height: h, Data: pix}

	tests := []struct {
		name     string
		maxBytes uint64
		records  []*handlerrpc.ImageUpload
		// drawn defaults to image 2 alone.
		drawn []uint64
	}{
		{
			name:    "one record",
			records: []*handlerrpc.ImageUpload{part(0, len(pix))},
			drawn:   []uint64{1, 2},
		},
		{
			name:    "records in offset order",
			records: []*handlerrpc.ImageUpload{part(0, 5), part(5, 9), part(9, len(pix))},
			drawn:   []uint64{1, 2},
		},
		{
			name:    "a gap between records",
			records: []*handlerrpc.ImageUpload{part(0, 5), part(6, len(pix))},
		},
		{
			name:    "overlapping records",
			records: []*handlerrpc.ImageUpload{part(0, 6), part(5, len(pix))},
		},
		{
			name:    "fewer bytes than its pixels",
			records: []*handlerrpc.ImageUpload{part(0, len(pix)-1)},
		},
		{
			name: "more bytes than its pixels",
			records: []*handlerrpc.ImageUpload{part(0, len(pix)), {
				Id: 1, Version: 1, Seq: 1, Width: w, Height: h,
				Offset: uint64(len(pix)), Data: []byte{0},
			}},
		},
		{
			name: "records that disagree on the version",
			records: []*handlerrpc.ImageUpload{part(0, 5), {
				Id: 1, Version: 2, Seq: 1, Width: w, Height: h,
				Offset: 5, Data: pix[5:],
			}},
		},
		{
			name: "a file instead of data",
			records: []*handlerrpc.ImageUpload{{
				Id: 1, Version: 1, Seq: 1, Width: w, Height: h, File: "1-0",
			}},
		},
		{
			name:     "more pixels than the budget",
			maxBytes: 4*w*h + 1,
			records: []*handlerrpc.ImageUpload{{
				Id: 1, Version: 1, Seq: 1, Width: w + 1, Height: h,
				Data: handlerrpctest.Gradient(w+1, h, 1).Pix,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newTestImageStore(t, tt.maxBytes, true)
			st.apply(&handlerrpc.DrawStreamResponse{
				Uploads: append(tt.records, valid),
			})
			want := tt.drawn
			if want == nil {
				want = []uint64{2}
			}
			assert.Equal(t, want, st.drawnIDs(t, pix),
				"an upload that fails its checks must drop only its own image")
			assert.EqualValues(t, 2, st.ack, "every upload must be acked")
		})
	}
}

func TestImageStoreBuffers(t *testing.T) {
	st := newTestImageStore(t, 0, false)
	place := []*handlerrpc.ImagePlacement{{Id: 1, Width: 1, Height: 1}}
	var seq uint64
	send := func(version uint64, size int) {
		seq++
		file := []string{"1-0", "1-1"}[seq%2]
		upload(t, st.dir, file, handlerrpctest.Gradient(size, size, version).Pix)
		st.apply(&handlerrpc.DrawStreamResponse{Uploads: []*handlerrpc.ImageUpload{{
			Id: 1, Version: version, Seq: seq, File: file,
			Width: int32(size), Height: int32(size),
		}}})
	}
	drawn := func(version uint64) *image.RGBA {
		t.Helper()
		place[0].Version = version
		w := new(handlerrpctest.ImageRecorder)
		st.beginDraw()
		st.draw(w, place)
		require.Len(t, w.Images, 1)
		return w.Images[0].Src.(*image.RGBA)
	}

	send(1, 2)
	b1 := drawn(1)
	send(2, 2)
	send(3, 2)
	assert.Equal(t, handlerrpctest.Gradient(2, 2, 1).Pix, b1.Pix,
		"uploads must never overwrite the buffer handed out in the last draw")
	b3 := drawn(3)
	assert.NotSame(t, b1, b3)
	assert.Equal(t, handlerrpctest.Gradient(2, 2, 3).Pix, b3.Pix)

	send(4, 2)
	b4 := drawn(4)
	assert.NotSame(t, b3, b4)
	send(5, 2)
	b5 := drawn(5)
	assert.Same(t, b3, b5, "a buffer no writer reads must be reused")
	assert.Equal(t, handlerrpctest.Gradient(2, 2, 5).Pix, b5.Pix)

	send(6, 1)
	b6 := drawn(6)
	assert.Equal(t, image.Rect(0, 0, 1, 1), b6.Rect)
	assert.Equal(t, &b4.Pix[0], &b6.Pix[0], "a smaller version must reuse the spare buffer")
	assert.EqualValues(t, 4*2*2+4*1*1, st.bytes)

	st.apply(&handlerrpc.DrawStreamResponse{Released: []uint64{1}})
	assert.Empty(t, st.images)
	assert.Zero(t, st.bytes)
}
