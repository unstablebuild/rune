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
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// solid returns n RGBA pixels of one colour.
func solid(n int, r, g, b, a byte) []byte {
	out := make([]byte, 0, n*4)
	for range n {
		out = append(out, r, g, b, a)
	}
	return out
}

// newAnimatedImage transmits a 2x2 red root image with id 1.
func newAnimatedImage(t *testing.T) *Storage {
	t.Helper()
	s := NewStorage()
	require.Equal(t, "\x1b_Gi=1;OK\x1b\\", exec(t, s, nil, "a=t,i=1,s=2,v=2", solid(4, 255, 0, 0, 255)))
	return s
}

func TestLoadFrame(t *testing.T) {
	t.Run("responses", func(t *testing.T) {
		tests := []struct {
			name    string
			setup   func(t *testing.T, s *Storage)
			ctl     string
			payload []byte
			want    string
		}{
			{
				name:    "new frame",
				ctl:     "a=f,i=1,s=2,v=2",
				payload: solid(4, 0, 255, 0, 255),
				want:    "\x1b_Gi=1,r=2;OK\x1b\\",
			},
			{
				name:    "no id and no number is ignored",
				ctl:     "a=f,s=2,v=2",
				payload: solid(4, 0, 255, 0, 255),
				want:    "",
			},
			{
				name:    "unknown image",
				ctl:     "a=f,i=7,s=2,v=2",
				payload: solid(4, 0, 255, 0, 255),
				want:    "\x1b_Gi=7;ENOENT:Animation command refers to non-existent image with id: 7 and number: 0\x1b\\",
			},
			{
				name:    "frame wider than image",
				ctl:     "a=f,i=1,s=3,v=1",
				payload: solid(3, 0, 255, 0, 255),
				want:    "\x1b_Gi=1,r=2;EINVAL:Frame width 3 larger than image width: 2\x1b\\",
			},
			{
				name:    "frame taller than image",
				ctl:     "a=f,i=1,s=1,v=3",
				payload: solid(3, 0, 255, 0, 255),
				want:    "\x1b_Gi=1,r=2;EINVAL:Frame height 3 larger than image height: 2\x1b\\",
			},
			{
				name:    "unsupported medium",
				ctl:     "a=f,i=1,s=2,v=2,t=f",
				payload: []byte("/tmp/x"),
				want:    "\x1b_Gi=1,r=2;EBADF:Failed to read image file\x1b\\",
			},
			{
				name:    "base frame must exist",
				ctl:     "a=f,i=1,s=2,v=2,c=5",
				payload: solid(4, 0, 255, 0, 255),
				want:    "\x1b_Gi=1,r=2;EINVAL:No frame with number: 5 found\x1b\\",
			},
			{
				name:    "too much data",
				ctl:     "a=f,i=1,s=1,v=1",
				payload: make([]byte, 4+directSlack+1),
				want:    "\x1b_Gi=1,r=2;EFBIG:Too much data\x1b\\",
			},
			{
				name:    "quiet suppresses ok",
				ctl:     "a=f,i=1,s=2,v=2,q=1",
				payload: solid(4, 0, 255, 0, 255),
				want:    "",
			},
			{
				name: "image that failed to load",
				setup: func(t *testing.T, s *Storage) {
					t.Helper()
					exec(t, s, nil, "a=t,i=2,s=2,v=2", make([]byte, 3))
				},
				ctl:     "a=f,i=2,s=2,v=2",
				payload: solid(4, 0, 255, 0, 255),
				want:    "\x1b_Gi=2,r=2;EINVAL:Frame width 2 larger than image width: 0\x1b\\",
			},
			{
				name: "re-transmission that failed leaves no raster to frame onto",
				setup: func(t *testing.T, s *Storage) {
					t.Helper()
					exec(t, s, nil, "a=t,i=1,s=0,v=0", nil)
				},
				ctl:     "a=f,i=1,s=2,v=2",
				payload: solid(4, 0, 255, 0, 255),
				want:    "\x1b_Gi=1,r=2;EINVAL:Frame width 2 larger than image width: 0\x1b\\",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := newAnimatedImage(t)
				if tt.setup != nil {
					tt.setup(t, s)
				}
				assert.Equal(t, tt.want, exec(t, s, nil, tt.ctl, tt.payload))
			})
		}
	})

	t.Run("frames are composed at image size", func(t *testing.T) {
		s := newAnimatedImage(t)
		// A 1x1 frame at offset (1,1) over a blue background.
		exec(t, s, nil, "a=f,i=1,s=1,v=1,x=1,y=1,Y=65535", solid(1, 0, 255, 0, 255))
		img := s.byClientID(1)
		require.Equal(t, 2, img.frameCount())
		f := img.frameForNumber(2)
		assert.Equal(t, []byte{
			0, 0, 255, 255, 0, 0, 255, 255,
			0, 0, 255, 255, 0, 255, 0, 255,
		}, f.pix.Pix)
		assert.Equal(t, int32(defaultFrameGap), f.gap)
		assert.Equal(t, int64(defaultFrameGap), img.anim.duration)
		assert.Equal(t, 0, img.anim.current, "loading a frame does not display it")
	})

	t.Run("new frame starts from base frame", func(t *testing.T) {
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=1,v=1,c=1,z=100", solid(1, 0, 255, 0, 255))
		f := s.byClientID(1).frameForNumber(2)
		assert.Equal(t, []byte{
			0, 255, 0, 255, 255, 0, 0, 255,
			255, 0, 0, 255, 255, 0, 0, 255,
		}, f.pix.Pix)
		assert.Equal(t, int32(100), f.gap)
	})

	t.Run("alpha blending and replacement", func(t *testing.T) {
		s := newAnimatedImage(t)
		half := solid(1, 0, 128, 0, 128)
		exec(t, s, nil, "a=f,i=1,s=1,v=1,c=1", half)
		blended := s.byClientID(1).frameForNumber(2).pix.Pix[:4]
		assert.Equal(t, []byte{127, 64, 0, 255}, blended)

		exec(t, s, nil, "a=f,i=1,s=1,v=1,c=1,X=1", half)
		replaced := s.byClientID(1).frameForNumber(3).pix.Pix[:4]
		assert.Equal(t, []byte{0, 64, 0, 128}, replaced, "premultiplied source copied verbatim")
	})

	t.Run("edit existing frame", func(t *testing.T) {
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 255, 0, 255))
		img := s.byClientID(1)
		version := img.version
		exec(t, s, nil, "a=f,i=1,r=1,s=1,v=1,z=7", solid(1, 0, 0, 255, 255))
		require.Equal(t, 2, img.frameCount(), "editing does not add a frame")
		assert.Equal(t, []byte{0, 0, 255, 255}, img.frameForNumber(1).pix.Pix[:4])
		assert.Equal(t, int32(7), img.frameForNumber(1).gap)
		assert.Equal(t, int64(defaultFrameGap+7), img.anim.duration)
		assert.Equal(t, version+1, img.version, "the displayed frame changed")
		assert.Same(t, img.pix, img.frameForNumber(1).pix, "the root frame is the image itself")
	})

	t.Run("negative gap means zero", func(t *testing.T) {
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=-1", solid(4, 0, 255, 0, 255))
		assert.Equal(t, int32(0), s.byClientID(1).frameForNumber(2).gap)
	})

	t.Run("chunked", func(t *testing.T) {
		s := newAnimatedImage(t)
		data := solid(4, 0, 255, 0, 255)
		assert.Equal(t, "", exec(t, s, nil, "a=f,i=1,s=2,v=2,m=1", data[:8]))
		assert.Equal(t, "", exec(t, s, nil, "a=f,m=0", data[8:]),
			"like kitty, the last chunk gets no response without an id")
		assert.Equal(t, 2, s.byClientID(1).frameCount())

		assert.Equal(t, "", exec(t, s, nil, "a=f,i=1,s=2,v=2,m=1", data[:8]))
		assert.Equal(t, "\x1b_Gi=1,r=3;OK\x1b\\", exec(t, s, nil, "a=f,i=1,m=0", data[8:]))
		assert.Equal(t, 3, s.byClientID(1).frameCount())
	})

	t.Run("frame cache quota", func(t *testing.T) {
		s := newAnimatedImage(t)
		s.Limit = 20 // both images fit; six 16-byte frames fit in the cache
		exec(t, s, nil, "a=t,i=2,s=1,v=1", solid(1, 1, 1, 1, 255))
		for n := 2; n <= 7; n++ {
			assert.Equal(t, "\x1b_Gi=1,r="+strconv.Itoa(n)+";OK\x1b\\",
				exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 255, 0, 255)))
		}
		require.NotNil(t, s.byClientID(2))
		assert.Equal(t, "\x1b_Gi=1,r=8;ENOSPC:Cache size exceeded cannot add new frames\x1b\\",
			exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 255, 0, 255)))
		assert.Nil(t, s.byClientID(2), "unplaced images are dropped to make room")
	})
}

func TestControlAnimation(t *testing.T) {
	setup := func(t *testing.T) (*Storage, *Image) {
		t.Helper()
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 255, 0, 255))
		exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 0, 255, 255))
		return s, s.byClientID(1)
	}

	t.Run("responds only for a missing image", func(t *testing.T) {
		s, _ := setup(t)
		assert.Equal(t, "", exec(t, s, nil, "a=a,i=1,s=3", nil))
		assert.Equal(t, "\x1b_Gi=9;ENOENT:Animation command refers to non-existent image with id: 9 and number: 0\x1b\\",
			exec(t, s, nil, "a=a,i=9,s=3", nil))
		assert.Equal(t, "", exec(t, s, nil, "a=a,s=3", nil))
	})

	t.Run("current frame", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,c=3", nil)
		assert.Equal(t, 2, img.anim.current)
		assert.Same(t, img.frameForNumber(3).pix, img.pix)
		exec(t, s, nil, "a=a,i=1,c=9", nil)
		assert.Equal(t, 2, img.anim.current, "out of range is ignored")
	})

	t.Run("gap", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,r=2,z=500", nil)
		assert.Equal(t, int32(500), img.frameForNumber(2).gap)
		assert.Equal(t, int64(500+defaultFrameGap), img.anim.duration)
		exec(t, s, nil, "a=a,i=1,r=2,z=-5", nil)
		assert.Equal(t, int32(0), img.frameForNumber(2).gap)
		exec(t, s, nil, "a=a,i=1,r=2", nil)
		assert.Equal(t, int32(0), img.frameForNumber(2).gap, "z=0 leaves the gap alone")
	})

	t.Run("state and loops", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,s=3,v=4", nil)
		assert.Equal(t, animationRunning, img.anim.state)
		assert.True(t, img.anim.drawn)
		assert.Equal(t, uint32(3), img.anim.maxLoops)
		exec(t, s, nil, "a=a,i=1,s=2", nil)
		assert.Equal(t, animationLoading, img.anim.state)
		exec(t, s, nil, "a=a,i=1,s=1,v=1", nil)
		assert.Equal(t, animationStopped, img.anim.state)
		assert.Equal(t, uint32(0), img.anim.maxLoops, "v=1 means loop forever")
	})
}

func TestCompose(t *testing.T) {
	setup := func(t *testing.T) (*Storage, *Image) {
		t.Helper()
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2", solid(4, 0, 255, 0, 255))
		return s, s.byClientID(1)
	}

	tests := []struct {
		name string
		ctl  string
		want string
	}{
		{name: "full copy", ctl: "a=c,i=1,r=2,c=1", want: "\x1b_Gi=1;OK\x1b\\"},
		{name: "no id is ignored", ctl: "a=c,r=2,c=1", want: ""},
		{
			name: "unknown image",
			ctl:  "a=c,i=5,r=2,c=1",
			want: "\x1b_Gi=5;ENOENT:Animation command refers to non-existent image with id: 5 and number: 0\x1b\\",
		},
		{
			name: "unknown source",
			ctl:  "a=c,i=1,r=3,c=1",
			want: "\x1b_Gi=1;ENOENT:No source frame number 3 exists in image id: 1\x1b\\",
		},
		{
			name: "unknown destination",
			ctl:  "a=c,i=1,r=2,c=3",
			want: "\x1b_Gi=1;ENOENT:No destination frame number 3 exists in image id: 1\x1b\\",
		},
		{
			name: "destination out of bounds",
			ctl:  "a=c,i=1,r=2,c=1,x=1,w=2",
			want: "\x1b_Gi=1;EINVAL:The destination rectangle is out of bounds\x1b\\",
		},
		{
			name: "source out of bounds",
			ctl:  "a=c,i=1,r=2,c=1,Y=1,h=2",
			want: "\x1b_Gi=1;EINVAL:The source rectangle is out of bounds\x1b\\",
		},
		{
			name: "same frame overlap",
			ctl:  "a=c,i=1,r=2,c=2,x=1,w=1,h=2,X=1",
			want: "\x1b_Gi=1;EINVAL:The source and destination rectangles overlap and the src and destination frames are the same\x1b\\",
		},
		{name: "same frame disjoint", ctl: "a=c,i=1,r=2,c=2,x=1,w=1,h=2,X=0", want: "\x1b_Gi=1;OK\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := setup(t)
			assert.Equal(t, tt.want, exec(t, s, nil, tt.ctl, nil))
		})
	}

	t.Run("copies pixels into the current frame", func(t *testing.T) {
		s, img := setup(t)
		version := img.version
		exec(t, s, nil, "a=c,i=1,r=2,c=1,x=1,y=1,w=1,h=1", nil)
		assert.Equal(t, []byte{
			255, 0, 0, 255, 255, 0, 0, 255,
			255, 0, 0, 255, 0, 255, 0, 255,
		}, img.pix.Pix)
		assert.Equal(t, version+1, img.version)
	})

	t.Run("blends unless C=1", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2,X=1", solid(4, 0, 0, 128, 128))
		exec(t, s, nil, "a=c,i=1,r=3,c=1,w=1,h=1", nil)
		assert.Equal(t, []byte{127, 0, 64, 255}, img.pix.Pix[:4])
		exec(t, s, nil, "a=c,i=1,r=3,c=1,x=1,w=1,h=1,C=1", nil)
		assert.Equal(t, []byte{0, 0, 64, 128}, img.pix.Pix[4:8])
	})
}

func TestDeleteFrame(t *testing.T) {
	setup := func(t *testing.T) (*Storage, *Image) {
		t.Helper()
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=10", solid(4, 0, 255, 0, 255))
		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=20", solid(4, 0, 0, 255, 255))
		return s, s.byClientID(1)
	}

	t.Run("removes the frame and fixes the current index", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,c=3", nil)
		exec(t, s, nil, "a=d,d=f,i=1,r=2", nil)
		require.Equal(t, 2, img.frameCount())
		assert.Equal(t, 1, img.anim.current)
		assert.Equal(t, int32(20), img.frameForNumber(2).gap)
		assert.Equal(t, int64(20), img.anim.duration, "the root frame gap is 0 until set")
	})

	t.Run("deleting the current frame shows its successor", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,c=2", nil)
		exec(t, s, nil, "a=d,d=f,i=1,r=2", nil)
		assert.Equal(t, 1, img.anim.current)
		assert.Same(t, img.frameForNumber(2).pix, img.pix)
	})

	t.Run("deleting the last current frame shows the new last", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,c=3", nil)
		exec(t, s, nil, "a=d,d=f,i=1,r=3", nil)
		assert.Equal(t, 1, img.anim.current)
		assert.Same(t, img.frameForNumber(2).pix, img.pix)
	})

	t.Run("r=0 deletes the first frame", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=d,d=f,i=1", nil)
		assert.Equal(t, 2, img.frameCount())
		assert.Equal(t, []byte{0, 255, 0, 255}, img.pix.Pix[:4])
	})

	t.Run("single frame is only freed by F", func(t *testing.T) {
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=d,d=f,i=1", nil)
		assert.NotNil(t, s.byClientID(1))
		exec(t, s, nil, "a=d,d=F,i=1", nil)
		assert.Nil(t, s.byClientID(1))
	})

	t.Run("unknown image is ignored", func(t *testing.T) {
		s, _ := setup(t)
		exec(t, s, nil, "a=d,d=f,i=4", nil)
		exec(t, s, nil, "a=d,d=f", nil)
		assert.Equal(t, 3, s.byClientID(1).frameCount())
	})
}

func TestAnimate(t *testing.T) {
	setup := func(t *testing.T) (*Storage, *Image) {
		t.Helper()
		s := newAnimatedImage(t)
		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=10", solid(4, 0, 255, 0, 255))
		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=20", solid(4, 0, 0, 255, 255))
		return s, s.byClientID(1)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("stopped animations do not run", func(t *testing.T) {
		s, _ := setup(t)
		changed, _, running := s.Animate(start)
		assert.False(t, changed)
		assert.False(t, running)
	})

	t.Run("advances through frames and loops", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,r=1,z=40,s=3", nil)
		img.anim.shownAt = start

		changed, next, running := s.Animate(start)
		assert.False(t, changed, "the root frame gap has not elapsed")
		assert.True(t, running)
		assert.Equal(t, 40*time.Millisecond, next)

		now := start.Add(40 * time.Millisecond)
		changed, next, running = s.Animate(now)
		assert.True(t, changed)
		assert.True(t, running)
		assert.Equal(t, 1, img.anim.current)
		assert.Equal(t, 10*time.Millisecond, next)
		assert.Same(t, img.frameForNumber(2).pix, img.pix)

		now = now.Add(10 * time.Millisecond)
		changed, next, _ = s.Animate(now)
		assert.True(t, changed)
		assert.Equal(t, 2, img.anim.current)
		assert.Equal(t, 20*time.Millisecond, next)

		now = now.Add(20 * time.Millisecond)
		changed, _, running = s.Animate(now)
		assert.True(t, changed)
		assert.True(t, running)
		assert.Equal(t, 0, img.anim.current, "loops back to the root")
		assert.Equal(t, uint32(1), img.anim.currentLoop)
	})

	t.Run("loading waits at the last frame", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,s=2,c=3", nil)
		img.anim.shownAt = start
		changed, _, running := s.Animate(start.Add(time.Second))
		assert.False(t, changed)
		assert.False(t, running)
		assert.Equal(t, 2, img.anim.current)

		exec(t, s, nil, "a=f,i=1,s=2,v=2,z=5", solid(4, 9, 9, 9, 255))
		changed, next, running := s.Animate(start.Add(2 * time.Second))
		assert.True(t, changed, "a newly loaded frame resumes the animation")
		assert.True(t, running)
		assert.Equal(t, 3, img.anim.current)
		assert.Equal(t, 5*time.Millisecond, next)
	})

	t.Run("finite loops stop", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,s=3,v=2", nil)
		img.anim.shownAt = start
		now := start
		for range 3 {
			now = now.Add(time.Second)
			s.Animate(now)
		}
		assert.Equal(t, 2, img.anim.current, "stays on the last frame after one loop")
		_, _, running := s.Animate(now.Add(time.Second))
		assert.False(t, running)
	})

	t.Run("zero gap frames are skipped", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,r=2,z=-1", nil)
		exec(t, s, nil, "a=a,i=1,s=3", nil)
		img.anim.shownAt = start
		s.Animate(start.Add(time.Second))
		assert.Equal(t, 2, img.anim.current)
	})

	t.Run("all zero gaps never run", func(t *testing.T) {
		s, img := setup(t)
		exec(t, s, nil, "a=a,i=1,r=1,z=-1", nil)
		exec(t, s, nil, "a=a,i=1,r=2,z=-1", nil)
		exec(t, s, nil, "a=a,i=1,r=3,z=-1", nil)
		exec(t, s, nil, "a=a,i=1,s=3", nil)
		img.anim.shownAt = start
		_, _, running := s.Animate(start.Add(time.Second))
		assert.False(t, running)
	})
}
