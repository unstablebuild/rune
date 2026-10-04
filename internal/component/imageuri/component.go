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

package imageuri

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

var _ tui.Component = (*Component)(nil)

// Component is a tui.Component that displays an image scaled to fit
// its cell rectangle.
type Component struct {
	name        string
	fetch       func(context.Context) (image.Image, error)
	interrupter term.Interrupter
	id          term.ImageID

	ctx    context.Context
	cancel context.CancelFunc

	// Owned by the event loop. anim is created when the fetch starts.
	width, height int
	anim          *component.Animation

	mu      sync.Mutex
	img     image.Image
	err     error
	version uint64
}

// New returns a component that displays the image at uri. Cached bytes
// younger than ttl are reused across instances. storage is used as-is
// (the caller owns partitioning and its lifetime). interrupter is
// signalled whenever the displayed state changes, including every frame
// of the loading animation.
//
// It fails on an unparsable URI, an unsupported scheme, or ttl < 0.
// The image is fetched on the first Draw with a non-empty size, and the
// progress animation is drawn centered until the fetch settles. If the
// image cannot be loaded, or the writer cannot draw images,
// component.ProblemArt is drawn centered instead. The fetch is not
// retried, and an expired image stays on screen until the component is
// recreated.
func New(
	uri string, ttl time.Duration, storage storageapi.Service,
	interrupter term.Interrupter,
) (*Component, error) {
	if ttl < 0 {
		return nil, fmt.Errorf("imageuri: negative ttl %s", ttl)
	}
	u, err := parseURI(uri)
	if err != nil {
		return nil, err
	}
	p := newCachedProvider(storage, ttl)
	fetch := func(ctx context.Context) (image.Image, error) {
		return p.image(ctx, u)
	}
	return newComponent(u.Redacted(), fetch, interrupter), nil
}

// FileSystem is the subset of workspaceapi.FileSystem that
// NewFromFileSystem reads images through.
type FileSystem interface {
	OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error)
}

// NewFromFileSystem returns a component that displays the image at path
// in fs, for files that have no URI New can fetch, such as those in a
// remote workspace. Nothing is cached: path is opened read-only once, on
// the first Draw with a non-empty size, and closed once it is read.
// Close does not interrupt a read that is already in progress, but its
// result is discarded. Drawing follows the same rules as New.
func NewFromFileSystem(
	fs FileSystem, path string, interrupter term.Interrupter,
) *Component {
	fetch := func(context.Context) (image.Image, error) {
		f, err := fs.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := readLimited(f, defaultMaxBytes, path)
		if err != nil {
			return nil, err
		}
		return decode(data)
	}
	return newComponent(path, fetch, interrupter)
}

func newComponent(
	name string, fetch func(context.Context) (image.Image, error),
	interrupter term.Interrupter,
) *Component {
	ctx, cancel := context.WithCancel(context.Background())
	return &Component{
		name:        name,
		fetch:       fetch,
		interrupter: interrupter,
		id:          term.NewImageID(),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func parseURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("imageuri: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Path == "" {
			return nil, fmt.Errorf("imageuri: %s has no path", u.Redacted())
		}
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("imageuri: %s has no host", u.Redacted())
		}
	default:
		return nil, fmt.Errorf("imageuri: unsupported scheme %q", u.Scheme)
	}
	return u, nil
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
	if c.anim != nil {
		c.anim.Resize(width, height)
	}
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if c.width <= 0 || c.height <= 0 {
		return
	}

	c.mu.Lock()
	img, version, err := c.img, c.version, c.err
	c.mu.Unlock()

	switch {
	case img != nil:
		if !w.DrawImage(term.Image{
			Src:     img,
			ID:      c.id,
			Version: version,
			Width:   c.width,
			Height:  c.height,
			Fit:     term.ImageFitContain,
			Layer:   term.ImageLayerAboveText,
		}) {
			c.drawProblem(w)
		}
	case err != nil:
		c.drawProblem(w)
	default:
		if c.anim == nil {
			c.startLoad()
		}
		c.anim.Draw(w)
	}
}

// Close cancels an in-flight fetch and stops the loading animation
// without waiting for either. A cancelled fetch leaves the displayed
// state unchanged and does not signal the interrupter.
func (c *Component) Close() error {
	c.cancel()
	return nil
}

func (c *Component) drawProblem(w term.Writer) {
	art := component.NewStringWithConfig(tcomponent.ProblemArt,
		component.StringConfig{Alignment: component.AlignmentCentered})
	art.Resize(c.width, c.height)
	art.Draw(w)
}

func (c *Component) startLoad() {
	frames, sequence := component.ProgressAnimationFrames()
	anim := component.NewAnimation(c.interrupter, frames, sequence, 0)
	anim.Resize(c.width, c.height)
	c.anim = anim
	go debug.CapturePanicReport(func() {
		c.load(anim)
	})
}

func (c *Component) load(anim *component.Animation) {
	img, err := c.fetch(c.ctx)
	_ = anim.Close()
	if c.ctx.Err() != nil {
		return
	}

	c.mu.Lock()
	if err != nil {
		c.err = err
	} else {
		c.img = img
		c.version++
	}
	c.mu.Unlock()

	if err != nil {
		slog.Warn("imageuri: load image", "name", c.name, "error", err)
	}
	if err := c.interrupter.Interrupt(c.ctx); err != nil {
		slog.Debug("imageuri: interrupt", "error", err)
	}
}
