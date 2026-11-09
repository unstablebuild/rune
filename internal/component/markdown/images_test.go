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

package markdown

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/component/imageuri/imageuritest"
)

func TestImagesLocate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// noFS locates without a file system.
		noFS bool
		dest string
		uri  string
		file string
		ok   bool
	}{
		{name: "relative", dest: "img.png", file: "/repo/docs/img.png", ok: true},
		{name: "dot relative", dest: "./img/a.gif", file: "/repo/docs/img/a.gif", ok: true},
		{name: "parent", dest: "../a.png", file: "/repo/a.png", ok: true},
		{name: "root relative", dest: "/a.png", file: "a.png", ok: true},
		{name: "root relative cleaned", dest: "/x/../a.png", file: "a.png", ok: true},
		{name: "root relative tilde", dest: "/~/a.png", file: "./~/a.png", ok: true},
		{name: "escaped", dest: "a%20b.png", file: "/repo/docs/a b.png", ok: true},
		{name: "query and fragment", dest: "a.png?raw=1#x", file: "/repo/docs/a.png", ok: true},
		{name: "upper case extension", dest: "A.PNG", file: "/repo/docs/A.PNG", ok: true},
		{name: "https", dest: "https://h/a", uri: "https://h/a", ok: true},
		{name: "https without host", dest: "https:///a.png"},
		{name: "http", dest: "http://h/a.png"},
		{name: "data", dest: "data:image/png;base64,AAAA"},
		{name: "file", dest: "file:///a.png"},
		{name: "scheme relative", dest: "//h/a.png"},
		{name: "not an image", dest: "notes.txt"},
		{name: "undecodable format", dest: "logo.svg"},
		{name: "no extension", dest: "img"},
		{name: "fragment only", dest: "#a.png"},
		{name: "empty", dest: ""},
		{name: "malformed", dest: "%zz.png"},
		{name: "https without a file system", noFS: true, dest: "https://h/a.png",
			uri: "https://h/a.png", ok: true},
		{name: "a path without a file system", noFS: true, dest: "img.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			imgs := images{fs: pictureFS{}, dir: "/repo/docs"}
			if tt.noFS {
				imgs.fs = nil
			}
			uri, file, ok := imgs.locate(tt.dest)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.uri, uri)
			assert.Equal(t, tt.file, file)
		})
	}
}

func TestNewWithConfigImagesNeedStorageAndInterrupter(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  ImagesConfig
	}{
		{name: "no storage", cfg: ImagesConfig{Interrupter: term.NopInterrupter()}},
		{name: "no interrupter", cfg: ImagesConfig{Storage: storagestub.NewInMemoryService()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ImagesConfig = tt.cfg
			_, err := NewWithConfig("![a](https://h/a.png)", cfg)
			assert.Error(t, err)
		})
	}
}

func TestNewWithConfigShowsPictures(t *testing.T) {
	const remote = "https://example.invalid/b.png"
	storage := storagestub.NewInMemoryService()
	imageuritest.Cache(t, storage, remote, encodePNG(t, cells(3, 1)))
	cfg := DefaultConfig()
	cfg.ImagesConfig = ImagesConfig{
		Storage:     storage,
		Interrupter: term.NopInterrupter(),
		FS:          pictureFS{"docs/a.png": encodePNG(t, cells(2, 1))},
		Dir:         "docs",
	}
	md, err := NewWithConfig("![a](a.png) ![b](<"+remote+"> \"B\") ![c](missing.png)", cfg)
	require.NoError(t, err)
	defer md.Close() //nolint:errcheck

	const width, height = 12, 3
	w := newImageWriter(width, height, true)
	md.Resize(width, height)
	md.Draw(w)
	waitForPictures(t, md)
	comptest.TestComponent(t, md, w, []comptest.TestCase{
		{Action: func() { md.Resize(width, height) }, Expected: `
   ███      
██  B  c    
            `},
	})
}

func TestNewWithConfigWithoutImagesShowsNoPictures(t *testing.T) {
	md, err := NewWithConfig("![a](https://h/a.png)", DefaultConfig())
	require.NoError(t, err)
	require.Len(t, md.blocks, 1)
	assert.IsType(t, &paragraphBlock{}, md.blocks[0])
}

// waitForPictures waits until every picture md opened has settled.
func waitForPictures(t *testing.T, md *Component) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, i := range md.imageBlocks {
			for _, it := range md.blocks[i].(*imageBlock).items {
				if it.img == nil {
					continue
				}
				if _, settled := it.img.Size(); !settled {
					return false
				}
			}
		}
		return true
	}, 10*time.Second, time.Millisecond, "the pictures never settled")
}

// encodePNG is a PNG of a single gray, size pixels wide and tall.
func encodePNG(t *testing.T, size image.Point) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewGray(image.Rectangle{Max: size})))
	return b.Bytes()
}
