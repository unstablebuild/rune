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

package imageuri_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/goleak"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
)

// The sample picture contained in a 40x10 area. WebP is lossless, so it
// renders exactly like PNG.
const (
	samplePNG40x10 = `
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @@@@#bbb##ccc#7cc;#@@@@@        
        @@@@@cb$@ccc#@;;;#@@@@@@        
        @@@@#cccb#;;;$#;;;@@@@@@        
        @@@@#cc;#;;;;#::::@@@@@@        
        @@@@@1;;;@3:::@::#@@@@@@        
        @@@@#;;:##:::#3+++#@@@@@        
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @#6@#@@#+:#c@#@#@a=#=##@        
        @@@@@@@@@@@@@@@@@@@@@@@@        `
	sampleJPEG40x10 = `
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @@@@#bbb#@ccc#8cc;#@@@@@        
        @@@@#ccW#ccc##;;c##@@@@@        
        @@@@#cccb#;;;$#;;:#@@@@@        
        @@@@W;c;#;;;;#;;::#@@@@@        
        @@@@#2;;;#3::;#+:##@@@@@        
        @@@@#;;:##:::#3+++#@@@@@        
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @#6#####+:#;##@##b+#+##@        
        @@@@@@@@@@@@@@@@@@@@@@@@        `
	sampleGIF40x10 = `
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @@@@#bbb@@ccc@7ccc@@@@@@        
        @@@@@cc$@ccc#@cc;@@@@@@@        
        @@@@@cccb@cc;$@;;;@@@@@@        
        @@@@@ccc@;;;;@::::@@@@@@        
        @@@@@2;;;@4:::@+:@@@@@@@        
        @@@@#;;:@@:::@3+++@@@@@@        
        @@@@@@@@@@@@@@@@@@@@@@@@        
        @@6@@@@@+:@;@#@@@a=@=@#@        
        @@@@@@@@@@@@@@@@@@@@@@@@        `
)

// samplePNG24x6 is the sample picture contained in a 24x6 area of a
// 40x10 writer.
const samplePNG24x6 = `
     @@@@@@@@@@@@@@                     
     @@@b#cc#;;#@@@                     
     @@@c;2;;:::@@@                     
     @@@;;#::#:0@@@                     
     @@@@@@@@@@@@@@                     
     #@@#@#####@##@                     
                                        
                                        
                                        
                                        `

// samplePNGFill40x10 is the sample picture stretched over a 40x10 area.
const samplePNGFill40x10 = `
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@@@@@@@#bbbbbb@@cccccc##cccccc#@@@@@@@@@
@@@@@@@@#bbc$@@cccccb#@a;;;;;#@@@@@@@@@@
@@@@@@@#;ccccc#@c;;;;;$@#;;;;;@@@@@@@@@@
@@@@@@@#;cc;;#@b;;;;;1##:::::;@@@@@@@@@@
@@@@@@@@#;:;;;:@#3+::::@@#::+@@@@@@@@@@@
@@@@@@@#;;;::;@@::::::#@:+++++#@@@@@@@@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@##@:#@##@@:##W+9##=@##=@@=@@=+###+#W#@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@`

const problemArt32x16 = `
                                
                                
          ___                   
         /___/\_                
        _\   \/_/\__            
      __\       \/_/\           
      \   __    __ \ \          
     __\  \_\   \_\ \ \   __    
    /_/\\   __   __  \ \_/_/\   
    \_\/_\__\/\__\/\__\/_\_\/   
       \_\/_/\       /_\_\/     
          \_\/       \_\/       
                                
                                
Uh, Houston, we've had a problem
                                `

// samples are the fixture files every source serves, with the frame each
// one renders in a 40x10 area.
var samples = []struct {
	path, golden string
}{
	{"sample.png", samplePNG40x10},
	{"sample.jpg", sampleJPEG40x10},
	{"sample.gif", sampleGIF40x10},
	{"sample.webp", samplePNG40x10},
}

func TestComponentDraw(t *testing.T) {
	defer goleak.VerifyNone(t)

	type testCase struct {
		name string
		// source is "http" for the test server, "file" for a file URI or
		// "fs" for NewFromFileSystem over a local directory. All serve
		// the samples and page.html; http also serves pending.png, which
		// never responds.
		source, path  string
		width, height int
		// images reports whether the writer can draw images, which it
		// encodes as ASCII art.
		images bool
		// style defaults to viewerStyle.
		style *imageuri.Style
		// under is drawn beneath the component, as a parent overlaying
		// the component on its own content would.
		under string
		steps []step
		// wantFetches counts http requests and file system opens.
		wantFetches int32
	}
	tests := []testCase{
		{
			name:   "draws and fetches nothing until sized",
			source: "http", path: "sample.png",
			width: 7, height: 3,
			steps: []step{
				{expected: `
       
       
       `},
				{do: resize(7, 0), expected: `
       
       
       `},
				{do: resize(0, 3), expected: `
       
       
       `},
			},
			wantFetches: 0,
		},
		{
			name:   "draws and opens nothing until sized",
			source: "fs", path: "sample.png",
			width: 7, height: 3,
			steps: []step{
				{expected: `
       
       
       `},
				{do: resize(7, 0), expected: `
       
       
       `},
			},
			wantFetches: 0,
		},
		{
			name:   "animates centered while the request is in flight",
			source: "http", path: "pending.png",
			width: 7, height: 3,
			steps: []step{
				{do: resize(7, 3), expected: `
       
   ⠃   
       `},
				{do: awaitRequest, expected: `
       
   ⠅   
       `},
				{do: resize(5, 1), expected: `
  ⠆    
       
       `},
			},
			wantFetches: 1,
		},
		{
			name:   "animates over the content around the animation",
			source: "http", path: "pending.png",
			width: 7, height: 1,
			under: "abcdefg",
			steps: []step{
				{do: resize(7, 1), expected: `
abc⠃efg`},
				{do: awaitRequest, expected: `
abc⠅efg`},
			},
			wantFetches: 1,
		},
		{
			name:   "re-encodes the image when resized",
			source: "http", path: "sample.png",
			width: 40, height: 10, images: true,
			steps: []step{
				{do: seq(resize(40, 10), settle), expected: samplePNG40x10},
				{do: resize(24, 6), expected: samplePNG24x6},
				{do: resize(40, 10), expected: samplePNG40x10},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art when the writer cannot draw images",
			source: "http", path: "sample.png",
			width: 32, height: 16,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
				{expected: problemArt32x16},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art when the file is missing",
			source: "file", path: "missing.png",
			width: 32, height: 16, images: true,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
			},
		},
		{
			name:   "draws the problem art when the server responds with an error",
			source: "http", path: "missing.png",
			width: 32, height: 16, images: true,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
				{expected: problemArt32x16},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art when the response is not an image",
			source: "http", path: "page.html",
			width: 32, height: 16, images: true,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art when the file system cannot open the file",
			source: "fs", path: "missing.png",
			width: 32, height: 16, images: true,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
				{expected: problemArt32x16},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art when the opened file is not an image",
			source: "fs", path: "page.html",
			width: 32, height: 16, images: true,
			steps: []step{
				{do: seq(resize(32, 16), settle), expected: problemArt32x16},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the caller's problem art",
			source: "http", path: "missing.png",
			width: 5, height: 3, images: true,
			style: &imageuri.Style{ProblemArt: "✗"},
			steps: []step{
				{do: seq(resize(5, 3), settle), expected: `
     
  ✗  
     `},
			},
			wantFetches: 1,
		},
		{
			name:   "draws the problem art over the content around it",
			source: "http", path: "missing.png",
			width: 5, height: 1, images: true,
			style: &imageuri.Style{ProblemArt: "✗"},
			under: "abcde",
			steps: []step{
				{do: seq(resize(5, 1), settle), expected: `
ab✗de`},
			},
			wantFetches: 1,
		},
		{
			name:   "draws nothing for an empty problem art",
			source: "fs", path: "missing.png",
			width: 5, height: 1,
			style: &imageuri.Style{},
			steps: []step{
				{do: seq(resize(5, 1), settle), expected: `
     `},
			},
			wantFetches: 1,
		},
		{
			name:   "stretches the image to fill its cells",
			source: "fs", path: "sample.png",
			width: 40, height: 10, images: true,
			style: &imageuri.Style{Fit: term.ImageFitFill},
			steps: []step{
				{do: seq(resize(40, 10), settle), expected: samplePNGFill40x10},
			},
			wantFetches: 1,
		},
	}
	for _, source := range []string{"http", "file", "fs"} {
		for _, s := range samples {
			var fetches int32
			if source != "file" {
				fetches = 1
			}
			tests = append(tests, testCase{
				name:   fmt.Sprintf("places %s from %s", s.path, source),
				source: source, path: s.path,
				width: 40, height: 10, images: true,
				steps: []step{
					{do: seq(resize(40, 10), settle), expected: s.golden},
					{expected: s.golden},
				},
				wantFetches: fetches,
			})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := viewerStyle
			if tt.style != nil {
				style = *tt.style
			}
			f := newFixture(t, tt.source, tt.path, tt.width, tt.height, tt.images, style)
			var c tui.Component = f.c
			if tt.under != "" {
				under := component.NewString(tt.under)
				under.Resize(tt.width, tt.height)
				c = overlay{under: under, c: f.c}
			}
			cases := make([]comptest.TestCase, len(tt.steps))
			for i, s := range tt.steps {
				cases[i] = comptest.TestCase{Expected: s.expected, Action: func() {
					if s.do != nil {
						s.do(f)
					}
				}}
			}
			comptest.TestComponent(t, c, f.w, cases)
			assert.Equal(t, tt.wantFetches, f.fetches.Load())
			assert.Equal(t, f.opened.Load(), f.closes.Load(),
				"every opened file is closed")
		})
	}
}

func TestComponentsShareTheCache(t *testing.T) {
	defer goleak.VerifyNone(t)

	tests := []struct {
		name string
		ttl  time.Duration
		// failAfterFirst makes the server respond 500 once the first
		// component has its image.
		failAfterFirst bool
		wantFetches    int32
	}{
		{name: "reuses fresh bytes without fetching", ttl: time.Hour, failAfterFirst: true, wantFetches: 1},
		{name: "refetches expired bytes", ttl: 0, wantFetches: 2},
		{name: "serves expired bytes when the refetch fails", ttl: 0, failAfterFirst: true, wantFetches: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fetches atomic.Int32
			var failing atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					fetches.Add(1)
					if failing.Load() {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = w.Write(imageuritest.SamplePNG)
				}))
			defer srv.Close()

			storage := storagestub.NewInMemoryService()
			for i := range 2 {
				if i == 1 && tt.failAfterFirst {
					failing.Store(true)
				}
				f := newCachedFixture(t, srv.URL+"/sample.png", tt.ttl, storage)
				comptest.TestComponent(t, f.c, f.w, []comptest.TestCase{
					{Action: func() { settle(f) }, Expected: samplePNG40x10},
				})
			}
			assert.Equal(t, tt.wantFetches, fetches.Load())
		})
	}
}

func TestCloseCancelsTheRequest(t *testing.T) {
	defer goleak.VerifyNone(t)

	arrived, cancelled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			close(arrived)
			<-r.Context().Done()
			close(cancelled)
		}))
	defer srv.Close()

	c, err := imageuri.New(srv.URL+"/image.png", time.Hour,
		storagestub.NewInMemoryService(), make(interrupts, 1), viewerStyle)
	require.NoError(t, err)
	c.Resize(3, 1)
	c.Draw(term.NewStringWriter(3, 1))
	receive(t, arrived, "the request")

	require.NoError(t, c.Close())
	receive(t, cancelled, "the request to be cancelled")
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		ttl     time.Duration
		wantErr string
	}{
		{name: "file", uri: "file:///tmp/a.png", ttl: time.Hour},
		{name: "http with zero ttl", uri: "http://example.com/a.png"},
		{name: "upper-case https", uri: "HTTPS://example.com/a.png", ttl: time.Minute},
		{name: "negative ttl", uri: "file:///tmp/a.png", ttl: -time.Second, wantErr: "negative ttl"},
		{name: "unparsable", uri: "http://[::1", wantErr: "imageuri:"},
		{name: "unsupported scheme", uri: "ftp://example.com/a.png", wantErr: `unsupported scheme "ftp"`},
		{name: "no scheme", uri: "/tmp/a.png", wantErr: `unsupported scheme ""`},
		{name: "http without host", uri: "http:///a.png", wantErr: "no host"},
		{name: "file without path", uri: "file:a.png", wantErr: "no path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := imageuri.New(tt.uri, tt.ttl,
				storagestub.NewInMemoryService(), make(interrupts, 1), viewerStyle)
			uriErr := imageuri.ValidateURI(tt.uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, c)
				if tt.ttl >= 0 {
					assert.ErrorContains(t, uriErr, tt.wantErr,
						"ValidateURI rejects what New rejects for the URI")
				}
				return
			}
			require.NoError(t, err)
			assert.NoError(t, uriErr)
			require.NoError(t, c.Close())
		})
	}
}

const testTimeout = 5 * time.Second

// viewerStyle is how the file viewer places images.
var viewerStyle = imageuri.Style{
	Fit: term.ImageFitContain, ProblemArt: tcomponent.ProblemArt,
}

var spinnerFrames, _ = component.ProgressAnimationFrames()

type step struct {
	do       func(*fixture)
	expected string
}

type fixture struct {
	t       *testing.T
	c       *imageuri.Component
	w       comptest.StringerWriter
	irq     interrupts
	fetches atomic.Int32
	opened  atomic.Int32
	closes  atomic.Int32
	arrived chan struct{}
}

func newFixture(
	t *testing.T, source, path string, width, height int, images bool,
	style imageuri.Style,
) *fixture {
	f := &fixture{t: t, irq: make(interrupts, 1), arrived: make(chan struct{}, 1)}

	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"sample.png":  imageuritest.SamplePNG,
		"sample.jpg":  imageuritest.SampleJPEG,
		"sample.gif":  imageuritest.SampleGIF,
		"sample.webp": imageuritest.SampleWebP,
		"page.html":   []byte("<html><body>not an image</body></html>"),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(dir)))
	mux.HandleFunc("/pending.png", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			f.fetches.Add(1)
			select {
			case f.arrived <- struct{}{}:
			default:
			}
			mux.ServeHTTP(w, r)
		}))
	t.Cleanup(srv.Close)

	switch source {
	case "fs":
		f.c = imageuri.NewFromFileSystem(dirFS{f: f, dir: dir}, path, f.irq, style)
	default:
		uri := srv.URL + "/" + path
		if source == "file" {
			uri = (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, path))}).String()
		}
		c, err := imageuri.New(uri, time.Hour, storagestub.NewInMemoryService(), f.irq, style)
		require.NoError(t, err)
		f.c = c
	}
	// Registered after srv.Close so it runs first and releases pending
	// requests the server would otherwise wait for.
	t.Cleanup(func() { assert.NoError(t, f.c.Close()) })

	f.w = term.NewStringWriter(width, height)
	if images {
		f.w = asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	}
	return f
}

// newCachedFixture places the image at uri, cached in storage for ttl,
// over a 40x10 ASCII art writer.
func newCachedFixture(
	t *testing.T, uri string, ttl time.Duration, storage storageapi.Service,
) *fixture {
	f := &fixture{t: t, irq: make(interrupts, 1)}
	c, err := imageuri.New(uri, ttl, storage, f.irq, viewerStyle)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, c.Close()) })
	f.c = c
	f.c.Resize(40, 10)
	f.w = asciiart.NewStringWriter(40, 10, asciiart.DefaultConfig())
	return f
}

func resize(width, height int) func(*fixture) {
	return func(f *fixture) { f.c.Resize(width, height) }
}

func seq(actions ...func(*fixture)) func(*fixture) {
	return func(f *fixture) {
		for _, a := range actions {
			a(f)
		}
	}
}

func awaitRequest(f *fixture) {
	receive(f.t, f.arrived, "the request")
}

// settle redraws on every interrupt, as the host event loop does, until
// the component stops drawing the loading animation.
func settle(f *fixture) {
	timeout := time.After(testTimeout)
	for {
		f.c.Draw(f.w)
		_ = f.w.Flush()
		screen := f.w.String()
		_ = f.w.Clear(term.Attributes{})
		if !slices.Contains(spinnerFrames, strings.TrimSpace(screen)) {
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

// receive uses Error rather than FailNow because comptest runs actions
// in subtests of the test that owns t.
func receive[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Errorf("timed out waiting for %s", what)
	}
}

// interrupts is a term.Interrupter that coalesces pending interrupts
// like the host event loop.
type interrupts chan struct{}

func (i interrupts) Interrupt(context.Context) error {
	select {
	case i <- struct{}{}:
	default:
	}
	return nil
}

// overlay draws c over under. Only c is resized.
type overlay struct {
	under component.String
	c     *imageuri.Component
}

func (o overlay) Resize(width, height int) { o.c.Resize(width, height) }

func (o overlay) Draw(w term.Writer) {
	o.under.Draw(w)
	o.c.Draw(w)
}

// dirFS is an imageuri.FileSystem over dir that counts opens and closes
// in its fixture. It refuses to open files for writing.
type dirFS struct {
	f   *fixture
	dir string
}

func (fs dirFS) OpenFile(
	path string, flag int, mode os.FileMode,
) (workspaceapi.File, error) {
	fs.f.fetches.Add(1)
	if flag != os.O_RDONLY {
		return nil, fmt.Errorf("open %s: flag %#x is not read-only", path, flag)
	}
	file, err := os.OpenFile(filepath.Join(fs.dir, path), flag, mode)
	if err != nil {
		return nil, err
	}
	fs.f.opened.Add(1)
	return countingFile{File: file, closes: &fs.f.closes}, nil
}

type countingFile struct {
	workspaceapi.File
	closes *atomic.Int32
}

func (c countingFile) Close() error {
	c.closes.Add(1)
	return c.File.Close()
}
