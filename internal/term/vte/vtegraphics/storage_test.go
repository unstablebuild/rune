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
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestTransmitFormats(t *testing.T) {
	tests := []struct {
		name    string
		ctl     string
		payload []byte
		want    []byte
		resp    string
	}{
		{
			name:    "rgba straight alpha is premultiplied",
			ctl:     "a=t,i=1,s=2,v=1",
			payload: []byte{255, 0, 0, 255, 200, 100, 0, 128},
			want:    []byte{255, 0, 0, 255, 100, 50, 0, 128},
			resp:    "\x1b_Gi=1;OK\x1b\\",
		},
		{
			name:    "rgb gets opaque alpha",
			ctl:     "a=t,i=1,s=2,v=1,f=24",
			payload: []byte{1, 2, 3, 4, 5, 6},
			want:    []byte{1, 2, 3, 255, 4, 5, 6, 255},
			resp:    "\x1b_Gi=1;OK\x1b\\",
		},
		{
			name:    "zlib compressed",
			ctl:     "a=t,i=1,s=1,v=2,f=24,o=z",
			payload: deflate([]byte{1, 2, 3, 4, 5, 6}),
			want:    []byte{1, 2, 3, 255, 4, 5, 6, 255},
			resp:    "\x1b_Gi=1;OK\x1b\\",
		},
		{
			name:    "png overrides declared size",
			ctl:     "a=t,i=1,f=100,s=99,v=99",
			payload: pngBytes(t, 2, 1, color.NRGBA{R: 9, G: 8, B: 7, A: 255}),
			want:    []byte{9, 8, 7, 255, 9, 8, 7, 255},
			resp:    "\x1b_Gi=1;OK\x1b\\",
		},
		{
			name:    "up to ten extra bytes are tolerated",
			ctl:     "a=t,i=1,s=1,v=1,f=24",
			payload: []byte{1, 2, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			want:    []byte{1, 2, 3, 255},
			resp:    "\x1b_Gi=1;OK\x1b\\",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStorage()
			resp := exec(t, s, nil, tt.ctl, tt.payload)
			assert.Equal(t, tt.resp, resp)
			img := s.byClientID(1)
			require.NotNil(t, img)
			assert.True(t, img.loaded)
			assert.Equal(t, tt.want, img.pix.Pix)
		})
	}
}

func TestTransmitErrors(t *testing.T) {
	tests := []struct {
		name    string
		ctl     string
		payload []byte
		resp    string
	}{
		{"both id and number", "a=t,i=1,I=2,s=1,v=1", []byte{0, 0, 0, 0},
			"\x1b_Gi=1,I=2;EINVAL:Must not specify both image id and image number\x1b\\"},
		{"zero size", "a=t,i=1", []byte{0, 0, 0, 0},
			"\x1b_Gi=1;EINVAL:Zero width/height not allowed\x1b\\"},
		{"too wide", "a=t,i=1,s=10001,v=1", nil,
			"\x1b_Gi=1;EINVAL:Image too large, width or height greater than 10000\x1b\\"},
		{"unknown format", "a=t,i=1,s=1,v=1,f=8", []byte{0},
			"\x1b_Gi=1;EINVAL:Unknown image format: 8\x1b\\"},
		{"insufficient data", "a=t,i=1,s=2,v=2,f=24", []byte{1, 2, 3},
			"\x1b_Gi=1;ENODATA:Insufficient image data: 3 < 12\x1b\\"},
		{"too much data", "a=t,i=1,s=1,v=1,f=24", bytes.Repeat([]byte{1}, 40),
			"\x1b_Gi=1;EFBIG:Too much data\x1b\\"},
		{"file medium unsupported", "a=t,i=1,s=1,v=1,t=f", []byte("/tmp/x"),
			"\x1b_Gi=1;EBADF:Failed to read image file\x1b\\"},
		{"bad zlib", "a=t,i=1,s=1,v=1,f=24,o=z", []byte{1, 2, 3},
			"\x1b_Gi=1;EINVAL:Failed to inflate image data with error: zlib: invalid header\x1b\\"},
		{"inflated size mismatch", "a=t,i=1,s=1,v=1,f=24,o=z", deflate([]byte{1, 2, 3, 4}),
			"\x1b_Gi=1;EINVAL:Image data size post inflation does not match expected size\x1b\\"},
		{"bad png", "a=t,i=1,f=100", []byte("not a png"),
			"\x1b_Gi=1;EBADPNG:png: invalid format: not a PNG file\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStorage()
			assert.Equal(t, tt.resp, exec(t, s, nil, tt.ctl, tt.payload))
			if img := s.byClientID(1); img != nil {
				assert.False(t, img.loaded)
				// A put on an image whose data failed to load is ENOENT.
				assert.Equal(t, "\x1b_Gi=1;ENOENT:Put command refers to image with id: 1 that could not load its data\x1b\\",
					exec(t, s, nil, "a=p,i=1", nil))
			}
		})
	}
}

func TestQuietAndIDlessResponses(t *testing.T) {
	s := NewStorage()
	assert.Equal(t, "", exec(t, s, nil, "a=t,s=1,v=1", px(1)), "id-less transmit is silent")
	assert.Equal(t, "", exec(t, s, nil, "a=t,i=1,s=1,v=1,q=1", px(1)), "q=1 suppresses OK")
	assert.Equal(t, "\x1b_Gi=1;EINVAL:Zero width/height not allowed\x1b\\",
		exec(t, s, nil, "a=t,i=1,q=1", px(1)), "q=1 keeps errors")
	assert.Equal(t, "", exec(t, s, nil, "a=t,i=1,q=2", px(1)), "q=2 suppresses errors")
	assert.Equal(t, "", exec(t, s, nil, "a=d,d=I,i=99", nil), "delete never responds")
}

func TestQuery(t *testing.T) {
	s := NewStorage()
	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", exec(t, s, nil, "a=t,i=1,s=1,v=1", px(1)))
	before := s.byClientID(1).pix

	assert.Equal(t, "\x1b_Gi=31;OK\x1b\\", exec(t, s, nil, "i=31,s=1,v=1,a=q,t=d,f=24", []byte{0, 0, 0}))
	assert.Nil(t, s.byClientID(31), "a query stores nothing")
	assert.Len(t, s.images, 1)

	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", exec(t, s, nil, "a=q,i=1,s=1,v=1,f=24", []byte{0, 0, 0}))
	assert.Same(t, before, s.byClientID(1).pix, "a query does not replace an existing image")

	assert.Equal(t, "\x1b_Gi=2;ENODATA:Insufficient image data: 0 < 3\x1b\\",
		exec(t, s, nil, "a=q,i=2,s=1,v=1,f=24", nil))
	assert.Equal(t, "", exec(t, s, nil, "a=q,s=1,v=1,f=24", []byte{0, 0, 0}), "query without id is ignored")
	assert.Equal(t, "\x1b_Gi=3;EBADF:Failed to read image file\x1b\\",
		exec(t, s, nil, "a=q,i=3,s=1,v=1,f=24,t=f", []byte("/tmp/x")))
}

func TestChunkedTransmission(t *testing.T) {
	s := NewStorage()
	cur := &Cursor{}
	cell := CellSize{Width: 10, Height: 20}
	// 2x1 RGB, sent in two chunks, placed at the final cursor.
	assert.Equal(t, "", exec(t, s, cur, "a=T,i=1,s=2,v=1,f=24,m=1", []byte{1, 2, 3}))
	assert.NotNil(t, s.pending)
	cur.X = 5
	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", handleAt(t, s, cur, cell, "m=0", []byte{4, 5, 6}))
	img := s.byClientID(1)
	require.NotNil(t, img)
	require.Len(t, img.refs, 1)
	assert.Equal(t, 5, img.refs[0].StartCol)
	assert.Equal(t, 6, cur.X, "cursor moved past the placement")
	assert.Nil(t, s.pending)

	t.Run("later chunk quiet overrides", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		assert.Equal(t, "", exec(t, s, nil, "m=0,q=1", []byte{2, 3}))
		assert.True(t, s.byClientID(2).loaded)
	})
	t.Run("later chunk keys are ignored", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		assert.Equal(t, "\x1b_Gi=2;OK\x1b\\", exec(t, s, nil, "i=7,s=9,m=0", []byte{2, 3}))
		assert.Nil(t, s.byClientID(7))
	})
	t.Run("delete aborts a pending load", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		exec(t, s, nil, "a=d", nil)
		assert.Nil(t, s.pending)
		// The next chunk starts a fresh, id-less transmission.
		assert.Equal(t, "", exec(t, s, nil, "m=0", []byte{2, 3}))
	})
	t.Run("direct transmit while loading is a continuation", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		assert.Equal(t, "\x1b_Gi=2;OK\x1b\\", exec(t, s, nil, "a=t,i=3,s=1,v=1,f=24,t=d", []byte{2, 3}))
		assert.Nil(t, s.byClientID(3))
	})
	t.Run("other medium discards a pending load", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		assert.Equal(t, "\x1b_Gi=3;EBADF:Failed to read image file\x1b\\",
			exec(t, s, nil, "a=t,i=3,s=1,v=1,f=24,t=s", []byte("name")))
		assert.Nil(t, s.pending)
		assert.Nil(t, s.byClientID(2), "the unfinished image is purged")
	})
	t.Run("continuation to a removed image", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=2,s=1,v=1,f=24,m=1", []byte{1})
		s.removeImage(s.byClientID(2))
		s.pending = &pendingLoad{start: Command{ImageID: 2}, image: 999}
		assert.Equal(t, "\x1b_Gi=2;EILSEQ:More payload loading refers to non-existent image\x1b\\",
			exec(t, s, nil, "m=0", []byte{2, 3}))
		assert.Nil(t, s.pending)
	})
}

func TestImageNumbers(t *testing.T) {
	s := NewStorage()
	assert.Equal(t, "\x1b_Gi=1,I=5;OK\x1b\\", exec(t, s, nil, "a=t,I=5,s=1,v=1", px(1)))
	exec(t, s, nil, "a=t,i=2,s=1,v=1", px(1))
	assert.Equal(t, "\x1b_Gi=3,I=5;OK\x1b\\", exec(t, s, nil, "a=t,I=5,s=1,v=1", px(1)),
		"the smallest free id is allocated")
	// A put by number resolves to the newest image and answers with its id.
	assert.Equal(t, "\x1b_Gi=3,I=5;OK\x1b\\", exec(t, s, nil, "a=p,I=5", nil))
	assert.Len(t, s.byClientID(3).refs, 1)
	assert.Empty(t, s.byClientID(1).refs)
}

func TestReplaceImageDropsPlacements(t *testing.T) {
	s := NewStorage()
	exec(t, s, nil, "a=T,i=1,s=1,v=1", px(1))
	require.Len(t, s.byClientID(1).refs, 1)
	id := s.byClientID(1).termID
	exec(t, s, nil, "a=t,i=1,s=1,v=1", px(1))
	assert.Empty(t, s.byClientID(1).refs)
	assert.Equal(t, id, s.byClientID(1).termID, "the writer-side id survives replacement")
	assert.Len(t, s.images, 1)
}

func TestIDlessImagesAreTrimmed(t *testing.T) {
	s := NewStorage()
	exec(t, s, nil, "a=t,s=1,v=1", px(1))
	exec(t, s, nil, "a=T,s=1,v=1", px(1))
	assert.Len(t, s.images, 1, "the unplaced id-less image is purged by the next transmit")
	assert.Len(t, s.images[0].refs, 1)
	// Placement ids are ignored on id-less images, so placements pile up.
	exec(t, s, nil, "a=T,s=1,v=1,p=1", px(1))
	exec(t, s, nil, "a=p,i=0,p=1", nil)
	assert.Len(t, s.images, 2)
}

func TestPut(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	newStore := func(t *testing.T) *Storage {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=1,s=25,v=45", make([]byte, 25*45*4))
		return s
	}

	t.Run("natural size footprint moves the cursor", func(t *testing.T) {
		s := newStore(t)
		cur := &Cursor{X: 2, Y: 3}
		assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", handleAt(t, s, cur, cell, "a=p,i=1", nil))
		ref := s.byClientID(1).refs[0]
		assert.Equal(t, 3, ref.effCols)
		assert.Equal(t, 3, ref.effRows)
		assert.Equal(t, Cursor{X: 5, Y: 5}, *cur)
		assert.Equal(t, image.Rect(0, 0, 25, 45), ref.src)
	})
	t.Run("sub-cell offsets widen the footprint and are clamped", func(t *testing.T) {
		s := newStore(t)
		cur := &Cursor{}
		handleAt(t, s, cur, cell, "a=p,i=1,X=6,Y=999", nil)
		ref := s.byClientID(1).refs[0]
		assert.Equal(t, 6, ref.cellX)
		assert.Equal(t, 19, ref.cellY)
		assert.Equal(t, 4, ref.effCols)
		assert.Equal(t, 4, ref.effRows)
	})
	t.Run("explicit columns and rows", func(t *testing.T) {
		s := newStore(t)
		cur := &Cursor{}
		handleAt(t, s, cur, cell, "a=p,i=1,c=4,r=2", nil)
		ref := s.byClientID(1).refs[0]
		assert.Equal(t, 4, ref.effCols)
		assert.Equal(t, 2, ref.effRows)
		assert.Equal(t, Cursor{X: 4, Y: 1}, *cur)
	})
	t.Run("rows derived from columns by aspect ratio", func(t *testing.T) {
		s := newStore(t)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1,c=2", nil)
		ref := s.byClientID(1).refs[0]
		// 2 cols = 20px wide → 36px tall → 2 rows
		assert.Equal(t, 2, ref.effCols)
		assert.Equal(t, 2, ref.effRows)
	})
	t.Run("columns derived from rows by aspect ratio", func(t *testing.T) {
		s := newStore(t)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1,r=9", nil)
		ref := s.byClientID(1).refs[0]
		// 9 rows = 180px tall → 100px wide → 10 cols
		assert.Equal(t, 10, ref.effCols)
		assert.Equal(t, 9, ref.effRows)
	})
	t.Run("source rectangle is clamped to the image", func(t *testing.T) {
		s := newStore(t)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1,x=20,y=40,w=50,h=50", nil)
		ref := s.byClientID(1).refs[0]
		assert.Equal(t, image.Rect(20, 40, 25, 45), ref.src)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1,x=100,y=100", nil)
		assert.True(t, s.byClientID(1).refs[1].src.Empty())
	})
	t.Run("cursor movement disabled", func(t *testing.T) {
		s := newStore(t)
		cur := &Cursor{X: 1, Y: 1}
		handleAt(t, s, cur, cell, "a=p,i=1,C=1", nil)
		assert.Equal(t, Cursor{X: 1, Y: 1}, *cur)
	})
	t.Run("same placement id replaces in place", func(t *testing.T) {
		s := newStore(t)
		assert.Equal(t, "\x1b_Gi=1,p=2;OK\x1b\\", handleAt(t, s, &Cursor{}, cell, "a=p,i=1,p=2", nil))
		handleAt(t, s, &Cursor{X: 7}, cell, "a=p,i=1,p=2,z=3", nil)
		refs := s.byClientID(1).refs
		require.Len(t, refs, 1)
		assert.Equal(t, 7, refs[0].StartCol)
		assert.Equal(t, int32(3), refs[0].Z)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1", nil)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1", nil)
		assert.Len(t, s.byClientID(1).refs, 3, "p=0 always adds a placement")
	})
	t.Run("missing image", func(t *testing.T) {
		s := newStore(t)
		assert.Equal(t, "\x1b_Gi=9;ENOENT:Put command refers to non-existent image with id: 9 and number: 0\x1b\\",
			handleAt(t, s, &Cursor{}, cell, "a=p,i=9", nil))
		assert.Equal(t, "", handleAt(t, s, &Cursor{}, cell, "a=p", nil), "put without id is ignored")
	})
	t.Run("virtual placement", func(t *testing.T) {
		s := newStore(t)
		cur := &Cursor{X: 3, Y: 4}
		assert.Equal(t, "\x1b_Gi=1,p=5;OK\x1b\\", handleAt(t, s, cur, cell, "a=p,i=1,U=1,p=5,c=2,r=2", nil))
		ref := s.byClientID(1).refs[0]
		assert.True(t, ref.Virtual)
		assert.Equal(t, Cursor{X: 3, Y: 4}, *cur, "virtual placements do not move the cursor")
		assert.Equal(t, "\x1b_Gi=1;EINVAL:Put command creating a virtual placement cannot refer to a parent\x1b\\",
			handleAt(t, s, cur, cell, "a=p,i=1,U=1,P=1", nil))
	})
}

func TestRelativePlacements(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=1,s=10,v=20", make([]byte, 10*20*4))
	exec(t, s, nil, "a=t,i=2,s=10,v=20", make([]byte, 10*20*4))
	assert.Equal(t, "\x1b_Gi=2;ENOPARENT:Put command refers to a parent image with id: 1 that has no placements\x1b\\",
		handleAt(t, s, &Cursor{}, cell, "a=p,i=2,P=1", nil))
	assert.Equal(t, "\x1b_Gi=2;ENOPARENT:Put command refers to a parent image with id: 7 that does not exist\x1b\\",
		handleAt(t, s, &Cursor{}, cell, "a=p,i=2,P=7", nil))

	handleAt(t, s, &Cursor{X: 4, Y: 6}, cell, "a=p,i=1,p=1", nil)
	assert.Equal(t, "\x1b_Gi=2;ENOPARENT:Put command refers to a parent image placement with id: 1 and placement id: 9 that does not exist\x1b\\",
		handleAt(t, s, &Cursor{}, cell, "a=p,i=2,P=1,Q=9", nil))

	cur := &Cursor{X: 1, Y: 1}
	assert.Equal(t, "\x1b_Gi=2,p=1;OK\x1b\\", handleAt(t, s, cur, cell, "a=p,i=2,p=1,P=1,Q=1,H=2,V=-1", nil))
	assert.Equal(t, Cursor{X: 1, Y: 1}, *cur, "relative placements never move the cursor")

	images := s.Visible(View{Width: 80, Height: 24, Cell: cell}, nil)
	require.Len(t, images, 2)
	assert.Equal(t, term.Coordinates{X: 6, Y: 5}, images[1].Pos, "child is offset from its parent")

	t.Run("cycle", func(t *testing.T) {
		assert.Equal(t, "\x1b_Gi=1,p=1;ECYCLE:This parent reference creates a cycle\x1b\\",
			handleAt(t, s, &Cursor{}, cell, "a=p,i=1,p=1,P=2,Q=1", nil))
		assert.Equal(t, "\x1b_Gi=2,p=1;EINVAL:Put command refers to itself as its own parent\x1b\\",
			handleAt(t, s, &Cursor{}, cell, "a=p,i=2,p=1,P=2,Q=1", nil))
	})
	t.Run("too deep", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=1,s=1,v=1", px(1))
		handleAt(t, s, &Cursor{}, cell, "a=p,i=1,p=1", nil)
		for i := 2; i <= 9; i++ {
			resp := handleAt(t, s, &Cursor{}, cell,
				"a=p,i=1,p="+strconv.Itoa(i)+",P=1,Q="+strconv.Itoa(i-1), nil)
			assert.Equal(t, "\x1b_Gi=1,p="+strconv.Itoa(i)+";OK\x1b\\", resp)
		}
		assert.Equal(t, "\x1b_Gi=1,p=10;ETOODEEP:Too many levels of parent references\x1b\\",
			handleAt(t, s, &Cursor{}, cell, "a=p,i=1,p=10,P=1,Q=9", nil))
	})
	t.Run("orphaned children are removed at draw", func(t *testing.T) {
		exec(t, s, nil, "a=d,d=i,i=1", nil)
		s.Visible(View{Width: 80, Height: 24, Cell: cell}, nil)
		assert.Nil(t, s.byClientID(2), "image 2 lost its last placement and was freed")
	})
}

func TestDelete(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	// Two images: 1 placed twice (rows 0 and 5, cols 0 and 10, z 0 and 3),
	// 2 placed once at (2, 2) and once virtually; 3 unplaced; an id-less
	// image placed at (9, 9).
	setup := func(t *testing.T) *Storage {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=1,s=10,v=20", make([]byte, 800))
		exec(t, s, nil, "a=t,i=2,s=10,v=20", make([]byte, 800))
		exec(t, s, nil, "a=t,i=3,s=10,v=20", make([]byte, 800))
		handleAt(t, s, &Cursor{X: 0, Y: 0}, cell, "a=p,i=1,p=1", nil)
		handleAt(t, s, &Cursor{X: 10, Y: 5}, cell, "a=p,i=1,p=2,z=3", nil)
		handleAt(t, s, &Cursor{X: 2, Y: 2}, cell, "a=p,i=2,p=1", nil)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=2,p=2,U=1", nil)
		handleAt(t, s, &Cursor{X: 9, Y: 9}, cell, "a=T,s=10,v=20", make([]byte, 800))
		return s
	}
	type want struct {
		refs1, refs2 int
		img1, img2   bool
		img3, idless bool
	}
	tests := []struct {
		name string
		cmd  string
		cur  Cursor
		want want
	}{
		{"a keeps images", "a=d", Cursor{},
			want{0, 1, true, true, true, false}},
		{"A frees images that lost their last placement", "a=d,d=A", Cursor{},
			want{0, 1, false, true, true, false}},
		{"i by id", "a=d,d=i,i=1", Cursor{},
			want{0, 2, true, true, true, true}},
		{"I frees the image", "a=d,d=I,i=1", Cursor{},
			want{0, 2, false, true, true, true}},
		{"i with placement", "a=d,d=i,i=1,p=2", Cursor{},
			want{1, 2, true, true, true, true}},
		{"i deletes virtual placements", "a=d,d=i,i=2", Cursor{},
			want{2, 0, true, true, true, true}},
		{"I frees an unplaced image", "a=d,d=I,i=3", Cursor{},
			want{2, 2, true, true, false, true}},
		{"c at cursor", "a=d,d=c", Cursor{X: 10, Y: 5},
			want{1, 2, true, true, true, true}},
		{"C at cursor frees", "a=d,d=C", Cursor{X: 2, Y: 2},
			want{2, 1, true, true, true, true}},
		{"p point", "a=d,d=p,x=1,y=1", Cursor{},
			want{1, 2, true, true, true, true}},
		{"q point and z", "a=d,d=q,x=11,y=6,z=3", Cursor{},
			want{1, 2, true, true, true, true}},
		{"q point wrong z", "a=d,d=q,x=11,y=6,z=0", Cursor{},
			want{2, 2, true, true, true, true}},
		{"x column", "a=d,d=x,x=3", Cursor{},
			want{2, 1, true, true, true, true}},
		{"y row", "a=d,d=y,y=1", Cursor{},
			want{1, 2, true, true, true, true}},
		{"z index", "a=d,d=z,z=3", Cursor{},
			want{1, 2, true, true, true, true}},
		{"r range", "a=d,d=r,x=2,y=3", Cursor{},
			want{2, 0, true, true, true, true}},
		{"R range frees unplaced", "a=d,d=R,x=2,y=3", Cursor{},
			want{2, 0, true, false, false, true}},
		{"unknown id is a no-op", "a=d,d=i,i=42", Cursor{},
			want{2, 2, true, true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setup(t)
			cur := tt.cur
			handleAt(t, s, &cur, cell, tt.cmd, nil)
			img1, img2, img3 := s.byClientID(1), s.byClientID(2), s.byClientID(3)
			assert.Equal(t, tt.want.img1, img1 != nil, "image 1 present")
			assert.Equal(t, tt.want.img2, img2 != nil, "image 2 present")
			assert.Equal(t, tt.want.img3, img3 != nil, "image 3 present")
			assert.Equal(t, tt.want.idless, s.byClientID(0) != nil, "id-less image present")
			if img1 != nil {
				assert.Len(t, img1.refs, tt.want.refs1, "image 1 placements")
			}
			if img2 != nil {
				assert.Len(t, img2.refs, tt.want.refs2, "image 2 placements")
			}
		})
	}

	t.Run("n by number", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=T,I=4,s=1,v=1", px(1))
		exec(t, s, nil, "a=T,I=4,s=1,v=1", px(1))
		handleAt(t, s, &Cursor{}, cell, "a=d,d=n,I=4", nil)
		assert.Empty(t, s.byClientID(2).refs, "the newest image with the number is targeted")
		assert.Len(t, s.byClientID(1).refs, 1)
		handleAt(t, s, &Cursor{}, cell, "a=d,d=N,I=4", nil)
		assert.Nil(t, s.byClientID(2))
	})
}

func TestScroll(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=1,s=10,v=60", make([]byte, 10*60*4))
	handleAt(t, s, &Cursor{Y: 2}, cell, "a=p,i=1,p=1", nil)
	handleAt(t, s, &Cursor{Y: 2}, cell, "a=p,i=1,p=2,U=1", nil)
	ref := s.byClientID(1).refs[0]

	s.Scroll(-1, -10, nil, cell)
	assert.Equal(t, 1, ref.StartRow)
	assert.Equal(t, 0, s.byClientID(1).refs[1].StartRow, "virtual placements do not scroll")
	s.Scroll(-3, -10, nil, cell)
	assert.Equal(t, -2, ref.StartRow, "placements live on in history")
	s.Scroll(2, -10, nil, cell)
	assert.Equal(t, 0, ref.StartRow)
	s.Scroll(-13, -10, nil, cell)
	assert.Len(t, s.byClientID(1).refs, 1, "dropped once entirely past the scrollback")

	t.Run("id-less images vanish with their placements", func(t *testing.T) {
		s := NewStorage()
		handleAt(t, s, &Cursor{}, cell, "a=T,s=10,v=20", make([]byte, 800))
		s.Scroll(-1, 0, nil, cell)
		assert.Empty(t, s.images)
	})

	t.Run("margins", func(t *testing.T) {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=1,s=10,v=60", make([]byte, 10*60*4))
		handleAt(t, s, &Cursor{Y: 5}, cell, "a=p,i=1,p=1", nil) // rows 5..7
		handleAt(t, s, &Cursor{Y: 0}, cell, "a=p,i=1,p=2", nil) // rows 0..2, outside
		m := &Margins{Top: 4, Bottom: 8}
		s.Scroll(-1, 0, m, cell)
		inside, outside := s.byClientID(1).refs[0], s.byClientID(1).refs[1]
		assert.Equal(t, 4, inside.StartRow)
		assert.Equal(t, 0, outside.StartRow, "placements outside the margins stay put")
		s.Scroll(-1, 0, m, cell)
		assert.Equal(t, 4, inside.StartRow, "clipped instead of moved above the top margin")
		assert.Equal(t, 2, inside.effRows)
		assert.Equal(t, 20, inside.src.Min.Y)
		s.Scroll(-2, 0, m, cell)
		assert.Len(t, s.byClientID(1).refs, 1, "removed once nothing is left inside")
	})
}

func TestClear(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	setup := func(t *testing.T) *Storage {
		s := NewStorage()
		exec(t, s, nil, "a=t,i=1,s=10,v=20", make([]byte, 800))
		exec(t, s, nil, "a=t,i=2,s=10,v=20", make([]byte, 800))
		exec(t, s, nil, "a=t,i=3,s=10,v=20", make([]byte, 800))
		handleAt(t, s, &Cursor{Y: 3}, cell, "a=p,i=1", nil)
		handleAt(t, s, &Cursor{Y: 3}, cell, "a=p,i=2", nil)
		handleAt(t, s, &Cursor{Y: 3}, cell, "a=p,i=3,U=1", nil)
		s.Scroll(-4, -100, nil, cell)
		handleAt(t, s, &Cursor{Y: 3}, cell, "a=p,i=2", nil)
		return s
	}
	t.Run("visible only", func(t *testing.T) {
		s := setup(t)
		s.Clear(false)
		assert.Len(t, s.byClientID(1).refs, 1, "history placement survives")
		assert.Len(t, s.byClientID(2).refs, 1)
		assert.Len(t, s.byClientID(3).refs, 1, "virtual placements survive")
	})
	t.Run("all", func(t *testing.T) {
		s := setup(t)
		s.Clear(true)
		assert.Nil(t, s.byClientID(1), "images left without placements are freed")
		assert.Nil(t, s.byClientID(2))
		assert.NotNil(t, s.byClientID(3))
	})
	t.Run("history", func(t *testing.T) {
		s := setup(t)
		s.ClearHistory()
		assert.Nil(t, s.byClientID(1))
		assert.Len(t, s.byClientID(2).refs, 1)
	})
}

func TestQuota(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	s.Limit = 1000 // 2.5 images of 400 bytes
	exec(t, s, nil, "a=t,i=1,s=10,v=10", make([]byte, 400))
	handleAt(t, s, &Cursor{}, cell, "a=p,i=1", nil)
	exec(t, s, nil, "a=t,i=2,s=10,v=10", make([]byte, 400))
	assert.Equal(t, "\x1b_Gi=3;OK\x1b\\", exec(t, s, nil, "a=t,i=3,s=10,v=10", make([]byte, 400)))
	assert.Nil(t, s.byClientID(2), "the unplaced image is evicted first")
	assert.NotNil(t, s.byClientID(1))
	assert.NotNil(t, s.byClientID(3), "the image just added is spared")
	assert.Equal(t, 800, s.used)

	handleAt(t, s, &Cursor{}, cell, "a=p,i=3", nil)
	exec(t, s, nil, "a=t,i=4,s=10,v=10,N=1", make([]byte, 400))
	assert.Nil(t, s.byClientID(4), "a transient image is evicted first, even the one just added")
	assert.Equal(t, 800, s.used)
	exec(t, s, nil, "a=t,i=5,s=10,v=10", make([]byte, 400))
	assert.Nil(t, s.byClientID(1), "then the least recently used placed image")
	assert.NotNil(t, s.byClientID(3))
	assert.NotNil(t, s.byClientID(5))
	assert.LessOrEqual(t, s.used, 1000)
}

func TestVisible(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=1,s=20,v=40", make([]byte, 20*40*4))
	exec(t, s, nil, "a=t,i=2,s=20,v=40", make([]byte, 20*40*4))
	handleAt(t, s, &Cursor{X: 1, Y: 1}, cell, "a=p,i=2,z=5", nil)
	handleAt(t, s, &Cursor{X: 3, Y: 22}, cell, "a=p,i=1,z=-1", nil)
	handleAt(t, s, &Cursor{X: 0, Y: 0}, cell, "a=p,i=1,p=9,x=5,y=5,w=10,h=10,c=1,r=1", nil)
	handleAt(t, s, &Cursor{X: 0, Y: 30}, cell, "a=p,i=1", nil)
	handleAt(t, s, &Cursor{X: 0, Y: 0}, cell, "a=p,i=2,U=1", nil)

	view := View{Width: 80, Height: 24, Cell: cell}
	images := s.Visible(view, nil)
	require.Len(t, images, 3, "the off-screen and virtual placements are culled")
	clip := image.Rect(0, 0, 80, 24)
	assert.Equal(t, term.Coordinates{X: 3, Y: 22}, images[0].Pos, "z=-1 first")
	assert.Equal(t, clip, images[0].Clip)
	assert.Equal(t, term.ImageFitFill, images[0].Fit)
	assert.Equal(t, image.Rect(5, 5, 15, 15), images[1].Crop)
	assert.Equal(t, 1, images[1].Width)
	assert.Equal(t, term.Coordinates{X: 1, Y: 1}, images[2].Pos, "z=5 last")
	assert.Equal(t, s.byClientID(2).termID, images[2].ID)

	view.ScrolledBy = 3
	images = s.Visible(view, nil)
	assert.Equal(t, term.Coordinates{X: 1, Y: 4}, images[len(images)-1].Pos, "scrolling back moves placements down")
	assert.Len(t, images, 2, "the bottom placement scrolled out of view")
}

// exec parses ctl (with payload appended, base64 encoded) and executes
// it at the origin with a 10x20 cell.
func exec(t *testing.T, s *Storage, cur *Cursor, ctl string, payload []byte) string {
	t.Helper()
	if cur == nil {
		cur = &Cursor{}
	}
	return handleAt(t, s, cur, CellSize{Width: 10, Height: 20}, ctl, payload)
}

func handleAt(t *testing.T, s *Storage, cur *Cursor, cell CellSize, ctl string, payload []byte) string {
	t.Helper()
	data := ctl
	if payload != nil {
		data += ";" + base64.StdEncoding.EncodeToString(payload)
	}
	cmd, err := Parse([]byte(data))
	require.NoError(t, err)
	resp, ok := s.Handle(cmd, cur, cell)
	if !ok {
		return ""
	}
	return string(resp)
}

// px returns n opaque RGBA pixels.
func px(n int) []byte {
	out := make([]byte, 0, n*4)
	for range n {
		out = append(out, 1, 2, 3, 255)
	}
	return out
}

func deflate(data []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func pngBytes(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		switch i % 4 {
		case 0:
			img.Pix[i] = c.R
		case 1:
			img.Pix[i] = c.G
		case 2:
			img.Pix[i] = c.B
		case 3:
			img.Pix[i] = c.A
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
