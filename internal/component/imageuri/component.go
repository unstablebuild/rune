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
	"errors"
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
	"unstable.build/rune/internal/debug"
)

var _ tui.Component = (*Component)(nil)

// Component is a tui.Component that displays an image scaled to fit
// its cell rectangle.
type Component struct {
	name        string
	fetch       func(context.Context) (image.Image, error)
	interrupter term.Interrupter
	style       Style
	id          term.ImageID

	ctx    context.Context
	cancel context.CancelFunc

	// Owned by the event loop.
	width, height int

	mu      sync.Mutex
	started bool
	// loading is set while a fetch is in flight. anim is created by the
	// first Draw while loading, unless the style hides loading, and closed
	// by the fetch when it settles.
	loading bool
	anim    *component.Animation
	img     image.Image
	err     error
	version uint64
}

// Style controls how a Component places its image and what it draws
// when it cannot.
type Style struct {
	// Fit selects how the image is scaled into the component's cells.
	Fit term.ImageFit
	// Layer selects where the image is composited relative to the
	// cells it covers.
	Layer term.ImageLayer
	// ProblemArt is drawn centered when the image cannot be loaded or
	// the writer cannot draw images. An empty ProblemArt draws nothing.
	// Like the loading animation, it only writes the cells it occupies,
	// so the component can overlay other content.
	ProblemArt string
	// HideLoading leaves the cells untouched while the image loads
	// instead of drawing the progress animation.
	HideLoading bool
}

// New returns a component that displays the image at uri. Cached bytes
// younger than ttl are reused across instances. storage is used as-is
// (the caller owns partitioning and its lifetime). interrupter is
// signalled whenever the displayed state changes, including every frame
// of the loading animation. style selects how the image is placed.
//
// It fails when ValidateURI rejects uri, when ttl < 0, or when storage or
// interrupter is nil. The image is fetched on Start or the first Draw
// with a non-empty size, whichever comes first, and, unless
// style.HideLoading, the progress animation is drawn centered until the
// fetch settles. If the image cannot be loaded, or the writer cannot draw
// images, style.ProblemArt is drawn centered instead. The fetch is not
// retried, and an expired image stays on screen until the component is
// recreated.
func New(
	uri string, ttl time.Duration, storage storageapi.Service,
	interrupter term.Interrupter, style Style,
) (*Component, error) {
	if ttl < 0 {
		return nil, fmt.Errorf("imageuri: negative ttl %s", ttl)
	}
	if storage == nil {
		return nil, errors.New("imageuri: nil storage")
	}
	if interrupter == nil {
		return nil, errors.New("imageuri: nil interrupter")
	}
	u, err := parseURI(uri)
	if err != nil {
		return nil, err
	}
	p := newCachedProvider(storage, ttl)
	fetch := func(ctx context.Context) (image.Image, error) {
		return p.image(ctx, u)
	}
	return newComponent(u.Redacted(), fetch, interrupter, style), nil
}

// ValidateURI returns the error New would fail with for uri: it must
// parse, and be either a file URI with a path or an http or https URI
// with a host.
func ValidateURI(uri string) error {
	_, err := parseURI(uri)
	return err
}

// FileSystem is the subset of workspaceapi.FileSystem that
// NewFromFileSystem reads images through.
type FileSystem interface {
	OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error)
}

// NewFromFileSystem returns a component that displays the image at path
// in fs, for files that have no URI New can fetch, such as those in a
// remote workspace. Nothing is cached: path is opened read-only once, on
// Start or the first Draw with a non-empty size, and closed once it is
// read. Close does not interrupt a read that is already in progress, but
// its result is discarded. Drawing follows the same rules as New.
func NewFromFileSystem(
	fs FileSystem, path string, interrupter term.Interrupter, style Style,
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
	return newComponent(path, fetch, interrupter, style)
}

func newComponent(
	name string, fetch func(context.Context) (image.Image, error),
	interrupter term.Interrupter, style Style,
) *Component {
	ctx, cancel := context.WithCancel(context.Background())
	return &Component{
		name:        name,
		fetch:       fetch,
		interrupter: interrupter,
		style:       style,
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.anim != nil {
		c.anim.Resize(width, height)
	}
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if c.width <= 0 || c.height <= 0 {
		return
	}
	c.Start()

	c.mu.Lock()
	img, version, err := c.img, c.version, c.err
	if img == nil && err == nil {
		if c.loading && !c.style.HideLoading {
			if c.anim == nil {
				c.anim = c.newAnimation()
			}
			c.anim.Draw(w)
		}
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	if err != nil || !w.DrawImage(term.Image{
		Src:     img,
		ID:      c.id,
		Version: version,
		Width:   c.width,
		Height:  c.height,
		Fit:     c.style.Fit,
		Layer:   c.style.Layer,
	}) {
		c.drawProblem(w)
	}
}

// Start begins loading the image without drawing it. It does nothing
// once loading has started or after Close.
func (c *Component) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.ctx.Err() != nil {
		return
	}
	c.started, c.loading = true, true
	go debug.CapturePanicReport(c.load)
}

// Size returns the natural size of the image in pixels. settled reports
// whether loading has finished; it stays false until then, and forever
// once Close cancels the load. size is zero until the image has loaded,
// so a settled zero size means loading failed.
func (c *Component) Size() (size image.Point, settled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.img != nil:
		return c.img.Bounds().Size(), true
	case c.err != nil:
		return image.Point{}, true
	}
	return image.Point{}, false
}

// Close cancels an in-flight fetch and stops the loading animation
// without waiting for either. A cancelled fetch leaves the displayed
// state unchanged and does not signal the interrupter.
func (c *Component) Close() error {
	c.cancel()
	return nil
}

func (c *Component) drawProblem(w term.Writer) {
	if c.style.ProblemArt == "" {
		return
	}
	art := centered(c.style.ProblemArt)
	art.Resize(c.width, c.height)
	art.Draw(w)
}

func (c *Component) newAnimation() *component.Animation {
	frames, sequence := component.ProgressAnimationFrames()
	comps := make([]component.WithAttributes, len(frames))
	for i, frame := range frames {
		comps[i] = centered(frame)
	}
	anim := new(component.Animation)
	anim.InitWithComponents(context.Background(), c.interrupter, comps, sequence, 0)
	anim.Resize(c.width, c.height)
	return anim
}

// centered leaves the cells around str untouched, unlike a centered
// component.String, which fills its whole rectangle.
func centered(str string) *component.Span {
	return component.NewSpan(component.NewString(str), component.SpanConfig{
		PadAutoFloating:  true,
		ContentAlignment: component.AlignmentCentered,
	})
}

func (c *Component) load() {
	img, err := c.fetch(c.ctx)

	c.mu.Lock()
	anim := c.anim
	c.anim, c.loading = nil, false
	cancelled := c.ctx.Err() != nil
	if !cancelled {
		if err != nil {
			c.err = err
		} else {
			c.img = img
			c.version++
		}
	}
	c.mu.Unlock()

	// Closed outside the lock: Close waits for the animation's pending
	// Interrupt, which may wait for a Draw that needs the lock.
	if anim != nil {
		_ = anim.Close()
	}
	if cancelled {
		return
	}
	if err != nil {
		slog.Warn("imageuri: load image", "name", c.name, "error", err)
	}
	if err := c.interrupter.Interrupt(c.ctx); err != nil {
		slog.Debug("imageuri: interrupt", "error", err)
	}
}
