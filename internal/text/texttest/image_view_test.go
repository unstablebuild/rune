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

package texttest

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

// Frames of an image view tab, named after the file the tab shows, its
// contents when they differ from the file's extension, and the size.
const (
	viewSpinner42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│                                        │
│                                        │
│                                        │
│                                        │
│                   ⠃                    │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
└────────────────────────────────────────┘`
	viewPNG42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb##ccc#7cc;#@@@@@        │
│        @@@@@cb$@ccc#@;;;#@@@@@@        │
│        @@@@#cccb#;;;$#;;;@@@@@@        │
│        @@@@#cc;#;;;;#::::@@@@@@        │
│        @@@@@1;;;@3:::@::#@@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6@#@@#+:#c@#@#@a=#=##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewJPEG42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o sample.jpg                            │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb#@ccc#8cc;#@@@@@        │
│        @@@@#ccW#ccc##;;c##@@@@@        │
│        @@@@#cccb#;;;$#;;:#@@@@@        │
│        @@@@W;c;#;;;;#;;::#@@@@@        │
│        @@@@#2;;;#3::;#+:##@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6#####+:#;##@##b+#+##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewJPEGLong42x14 = `
┌━━━━━━━━━━━━━───────────────────────────┐
│o sample.jpeg                           │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb#@ccc#8cc;#@@@@@        │
│        @@@@#ccW#ccc##;;c##@@@@@        │
│        @@@@#cccb#;;;$#;;:#@@@@@        │
│        @@@@W;c;#;;;;#;;::#@@@@@        │
│        @@@@#2;;;#3::;#+:##@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6#####+:#;##@##b+#+##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewGIF42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o sample.gif                            │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb@@ccc@7ccc@@@@@@        │
│        @@@@@cc$@ccc#@cc;@@@@@@@        │
│        @@@@@cccb@cc;$@;;;@@@@@@        │
│        @@@@@ccc@;;;;@::::@@@@@@        │
│        @@@@@2;;;@4:::@+:@@@@@@@        │
│        @@@@#;;:@@:::@3+++@@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@6@@@@@+:@;@#@@@a=@=@#@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewWebP42x14 = `
┌━━━━━━━━━━━━━───────────────────────────┐
│o sample.webp                           │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb##ccc#7cc;#@@@@@        │
│        @@@@@cb$@ccc#@;;;#@@@@@@        │
│        @@@@#cccb#;;;$#;;;@@@@@@        │
│        @@@@#cc;#;;;;#::::@@@@@@        │
│        @@@@@1;;;@3:::@::#@@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6@#@@#+:#c@#@#@a=#=##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewUpperPNG42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o SAMPLE.PNG                            │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb##ccc#7cc;#@@@@@        │
│        @@@@@cb$@ccc#@;;;#@@@@@@        │
│        @@@@#cccb#;;;$#;;;@@@@@@        │
│        @@@@#cc;#;;;;#::::@@@@@@        │
│        @@@@@1;;;@3:::@::#@@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6@#@@#+:#c@#@#@a=#=##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewPNGAsJPEG42x14 = `
┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @@@@#bbb#@ccc#8cc;#@@@@@        │
│        @@@@#ccW#ccc##;;c##@@@@@        │
│        @@@@#cccb#;;;$#;;:#@@@@@        │
│        @@@@W;c;#;;;;#;;::#@@@@@        │
│        @@@@#2;;;#3::;#+:##@@@@@        │
│        @@@@#;;:##:::#3+++#@@@@@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
│        @#6#####+:#;##@##b+#+##@        │
│        @@@@@@@@@@@@@@@@@@@@@@@@        │
└────────────────────────────────────────┘`
	viewPNG30x10 = `
┌━━━━━━━━━━━━────────────────┐            
│o sample.png                │            
├────────────────────────────┤            
│       @@@@@@@@@@@@@@       │            
│       @@@b#cc#;;#@@@       │            
│       @@@c;2;;:::@@@       │            
│       @@@;;#::#:0@@@       │            
│       @@@@@@@@@@@@@@       │            
│       #@@#@#####@##@       │            
└────────────────────────────┘            
                                          
                                          
                                          
                                          `
	viewPNG34x20 = `
┌━━━━━━━━━━━━────────────────────┐
│o sample.png                    │
├────────────────────────────────┤
│                                │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@│
│@@@@@@#bbb#@@1bcb@@#ccca@@@@@@@@│
│@@@@@@bbbbb@#ccccc@cc;;;#@@@@@@@│
│@@@@@@@###@$cc#@##;;W###@@@@@@@@│
│@@@@@@#cccc6@;;;;;##;;;;@@@@@@@@│
│@@@@@@cccc;@#;;;;;@:::::#@@@@@@@│
│@@@@@@@@@@#?;4@@@#:;@@@@@@@@@@@@│
│@@@@@@c;;;;#@;:::;@@:::+@@@@@@@@│
│@@@@@@;;;::@@::::W@;++++#@@@@@@@│
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@│
│##########@##@#@#@#@@@##@###@#@@│
│@0@:##:@@8#++$++@++@#b@+=#+W*#@@│
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@│
│                                │
│                                │
└────────────────────────────────┘`
	viewPNGProblem34x20 = `
┌━━━━━━━━━━━━────────────────────┐
│o sample.png                    │
├────────────────────────────────┤
│                                │
│                                │
│          ___                   │
│         /___/\_                │
│        _\   \/_/\__            │
│      __\       \/_/\           │
│      \   __    __ \ \          │
│     __\  \_\   \_\ \ \   __    │
│    /_/\\   __   __  \ \_/_/\   │
│    \_\/_\__\/\__\/\__\/_\_\/   │
│       \_\/_/\       /_\_\/     │
│          \_\/       \_\/       │
│                                │
│                                │
│Uh, Houston, we've had a problem│
│                                │
└────────────────────────────────┘`
)

func TestViewImageDraw(t *testing.T) {
	notAnImage := []byte("<html>not an image</html>")

	tests := []struct {
		name          string
		file          string
		data          []byte
		width, height int
		// images reports whether the writer draws images, as ASCII art.
		images bool
		// edit opens the file for editing instead of viewing it.
		edit  bool
		steps []imageViewStep
	}{
		{
			name: "animates while the file is read, then places the image",
			file: "sample.png", data: imageuritest.SamplePNG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{
				{expected: viewSpinner42x14},
				{do: settleImageView, expected: viewPNG42x14},
				{expected: viewPNG42x14},
			},
		},
		{
			name: "edit views the image",
			file: "sample.png", data: imageuritest.SamplePNG,
			width: 42, height: 14, images: true, edit: true,
			steps: []imageViewStep{
				{expected: viewSpinner42x14},
				{do: settleImageView, expected: viewPNG42x14},
			},
		},
		{
			name: "edit views a file that is not an image",
			file: "sample.png", data: notAnImage,
			width: 34, height: 20, images: true, edit: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewPNGProblem34x20}},
		},
		{
			name: "views a jpg",
			file: "sample.jpg", data: imageuritest.SampleJPEG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewJPEG42x14}},
		},
		{
			name: "views a jpeg",
			file: "sample.jpeg", data: imageuritest.SampleJPEG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewJPEGLong42x14}},
		},
		{
			name: "views a gif",
			file: "sample.gif", data: imageuritest.SampleGIF,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewGIF42x14}},
		},
		{
			name: "views a webp",
			file: "sample.webp", data: imageuritest.SampleWebP,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewWebP42x14}},
		},
		{
			name: "views an upper-case extension",
			file: "SAMPLE.PNG", data: imageuritest.SamplePNG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{{do: settleImageView, expected: viewUpperPNG42x14}},
		},
		{
			name: "re-encodes the image when the window is resized",
			file: "sample.png", data: imageuritest.SamplePNG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{
				{do: settleImageView, expected: viewPNG42x14},
				{do: resizeImageView(30, 10), expected: viewPNG30x10},
				{do: resizeImageView(42, 14), expected: viewPNG42x14},
			},
		},
		{
			name: "reload replaces the image with the file's new content",
			file: "sample.png", data: imageuritest.SamplePNG,
			width: 42, height: 14, images: true,
			steps: []imageViewStep{
				{do: settleImageView, expected: viewPNG42x14},
				{
					do:       seqImageView(reloadImageView(imageuritest.SampleJPEG), settleImageView),
					expected: viewPNGAsJPEG42x14,
				},
			},
		},
		{
			name: "reload recovers a file that was not an image",
			file: "sample.png", data: notAnImage,
			width: 34, height: 20, images: true,
			steps: []imageViewStep{
				{do: settleImageView, expected: viewPNGProblem34x20},
				{
					do:       seqImageView(reloadImageView(imageuritest.SamplePNG), settleImageView),
					expected: viewPNG34x20,
				},
			},
		},
		{
			name: "draws the problem art when the writer cannot draw images",
			file: "sample.png", data: imageuritest.SamplePNG,
			width: 34, height: 20,
			steps: []imageViewStep{
				{do: settleImageView, expected: viewPNGProblem34x20},
				{expected: viewPNGProblem34x20},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImageViewFixture(t, tt.file, tt.width, tt.height, tt.images)
			f.loader.openFile = workspace.NewMemoryFile(
				tt.file, 1, 0, tt.data, new(sync.Mutex))
			f.open(!tt.edit)

			cases := make([]comptest.TestCase, len(tt.steps))
			for i, s := range tt.steps {
				cases[i] = comptest.TestCase{Expected: s.expected, Action: func() {
					if s.do != nil {
						s.do(f)
					}
				}}
			}
			comptest.TestComponent(t, f.c, f.w, cases)
		})
	}
}

func TestViewImageTab(t *testing.T) {
	t.Run("records the modification time from before the read", func(t *testing.T) {
		f := newImageViewFixture(t, "sample.png", 42, 14, true)
		f.loader.openFile = &markdownReloadFile{
			File: workspace.NewMemoryFile(
				"sample.png", 1, 0, imageuritest.SamplePNG, new(sync.Mutex)),
			beforeRead: time.Unix(1, 0),
			afterRead:  time.Unix(2, 0),
		}
		h := f.open(true)
		settleImageView(f)

		lastFlush, err := f.c.LastFlush(h)
		require.NoError(t, err)
		assert.Equal(t, time.Unix(1, 0), lastFlush)

		f.loader.openFile = &markdownReloadFile{
			File: workspace.NewMemoryFile(
				"sample.png", 2, 0, imageuritest.SamplePNG, new(sync.Mutex)),
			beforeRead: time.Unix(3, 0),
			afterRead:  time.Unix(4, 0),
		}
		require.NoError(t, awaitErr(f.c.Reload(context.Background(), f.win)))
		lastFlush, err = f.c.LastFlush(h)
		require.NoError(t, err)
		assert.Equal(t, time.Unix(3, 0), lastFlush)
	})

	for _, mode := range []struct {
		name     string
		readOnly bool
	}{{"view", true}, {"edit", false}} {
		t.Run(mode.name+" cannot be saved", func(t *testing.T) {
			f := newImageViewFixture(t, "sample.png", 42, 14, true)
			f.loader.openFile = workspace.NewMemoryFile(
				"sample.png", 1, 0, imageuritest.SamplePNG, new(sync.Mutex))
			f.open(mode.readOnly)

			assert.Equal(t, textapi.ErrInvalidSave,
				awaitErr(f.c.Flush(context.Background(), f.win)))
			assert.Equal(t, textapi.ErrInvalidSave,
				awaitErr(f.c.Overwrite(context.Background(), f.win)))
		})

		t.Run(mode.name+" fails on a missing file", func(t *testing.T) {
			f := newImageViewFixture(t, "sample.png", 42, 14, true)
			_, err := f.c.OpenFileTab(f.uri, mode.readOnly)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

const imageViewTimeout = 5 * time.Second

type imageViewStep struct {
	do       func(*imageViewFixture)
	expected string
}

type imageViewFixture struct {
	t      *testing.T
	c      *text.Component
	loader *testLoader
	uri    workspaceapi.URI
	win    browser.Window
	w      comptest.StringerWriter
	irq    chan struct{}
}

// newImageViewFixture shows memory:///tmp/<file> in a width x height
// text.Component. images selects a writer that draws images as ASCII
// art over one that cannot draw them.
func newImageViewFixture(
	t *testing.T, file string, width, height int, images bool,
) *imageViewFixture {
	f := &imageViewFixture{t: t, irq: make(chan struct{}, 1)}
	cfg := text.DefaultConfig()
	// Coalesces pending interrupts like the host event loop.
	cfg.EventPublisher = func(term.Event) bool {
		select {
		case f.irq <- struct{}{}:
		default:
		}
		return true
	}
	f.c, f.loader = newTestComponentConfig(t, NopEditor(), cfg)
	t.Cleanup(func() { _ = f.c.Close() })

	uri, err := workspaceapi.ParseURI("memory:///tmp/" + file)
	require.NoError(t, err)
	f.uri = uri
	win, err := f.c.Focus()
	require.NoError(t, err)
	f.win = win
	f.c.Resize(width, height)
	f.w = term.NewStringWriter(width, height)
	if images {
		f.w = asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	}
	return f
}

func (f *imageViewFixture) open(readOnly bool) browserapi.Handler {
	h, err := f.c.OpenFileTab(f.uri, readOnly)
	require.NoError(f.t, err)
	require.NoError(f.t, f.win.SetContent(h))
	return h
}

func seqImageView(actions ...func(*imageViewFixture)) func(*imageViewFixture) {
	return func(f *imageViewFixture) {
		for _, a := range actions {
			a(f)
		}
	}
}

func resizeImageView(width, height int) func(*imageViewFixture) {
	return func(f *imageViewFixture) { f.c.Resize(width, height) }
}

// reloadImageView reports failures with Error because comptest runs
// actions in subtests of the test that owns f.t.
func reloadImageView(data []byte) func(*imageViewFixture) {
	return func(f *imageViewFixture) {
		f.loader.openFile = workspace.NewMemoryFile(
			f.uri.Name(), 2, 0, data, new(sync.Mutex))
		assert.NoError(f.t, awaitErr(f.c.Reload(context.Background(), f.win)))
	}
}

var imageViewSpinner = func() string {
	frames, _ := component.ProgressAnimationFrames()
	return strings.Join(frames, "")
}()

// settleImageView redraws on every interrupt, as the host event loop
// does, until the loading animation is gone.
func settleImageView(f *imageViewFixture) {
	timeout := time.After(imageViewTimeout)
	for {
		f.c.Draw(f.w)
		_ = f.w.Flush()
		screen := f.w.String()
		_ = f.w.Clear(term.Attributes{})
		if !strings.ContainsAny(screen, imageViewSpinner) {
			return
		}
		select {
		case <-f.irq:
		case <-timeout:
			f.t.Error("timed out waiting for the image to load")
			return
		}
	}
}
