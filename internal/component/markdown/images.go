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
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/component/imageuri"
)

// imageTTL is how long a downloaded image is reused.
const imageTTL = 24 * time.Hour

// ImagesConfig shows pictures in place of images. When Storage and
// Interrupter are set, a paragraph made only of images, each optionally
// linked, shows the pictures of its https images and of its paths in FS
// once they load; every other image shows its alt text as a link to the
// image. Pictures are only shown when the writer's context carries the
// cell size in pixels, and Height grows as they load. The zero value
// shows no pictures.
type ImagesConfig struct {
	// Storage caches the https images the component downloads. It is used
	// as-is: the caller owns its partitioning and lifetime.
	Storage storageapi.Service
	// Interrupter is signalled whenever a picture changes what is drawn.
	Interrupter term.Interrupter
	// FS reads the pictures that paths name. When nil, only https images
	// are shown.
	FS imageuri.FileSystem
	// Dir is the directory in FS of the markdown, which relative paths
	// resolve against. Absolute paths resolve against the root of FS.
	Dir string
}

// images opens the pictures of a Component's images.
type images struct {
	fs          imageuri.FileSystem
	storage     storageapi.Service
	dir         string
	interrupter term.Interrupter
}

var imageStyle = imageuri.Style{Fit: term.ImageFitContain}

// open returns the picture an image's destination names, or false when
// it is not shown.
func (m images) open(dest string) (picture, bool) {
	uri, file, ok := m.locate(dest)
	switch {
	case !ok:
		return nil, false
	case file != "":
		return imageuri.NewFromFileSystem(m.fs, file, m.interrupter, imageStyle), true
	}
	img, err := imageuri.New(uri, imageTTL, m.storage, m.interrupter, imageStyle)
	if err != nil {
		return nil, false
	}
	return img, true
}

// locate returns the https URI or the file in fs an image's destination
// names. Like GitHub, a relative path resolves against dir and an
// absolute one against the root, so it is returned relative for fs to
// resolve. Paths must name a format imageuri decodes. Every other
// destination, such as an http, data or file URI, is not shown.
func (m images) locate(dest string) (uri, file string, ok bool) {
	u, err := url.Parse(dest)
	if err != nil {
		return "", "", false
	}
	switch {
	case u.Scheme == "https" && u.Host != "":
		return dest, "", true
	case m.fs == nil || u.Scheme != "" || u.Host != "" || u.Opaque != "":
		return "", "", false
	case u.Path == "" || !imageuri.IsImagePath(u.Path):
		return "", "", false
	case !strings.HasPrefix(u.Path, "/"):
		return "", path.Join(m.dir, u.Path), true
	}
	file = strings.TrimPrefix(path.Clean(u.Path), "/")
	if strings.HasPrefix(file, "~") {
		// Workspaces expand a leading ~ to the home directory.
		file = "./" + file
	}
	return "", file, true
}
