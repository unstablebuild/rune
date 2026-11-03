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

package ide

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestStatusBarImageIntegration(t *testing.T) {
	const width = 60
	tests := []struct {
		name   string
		zindex int
		// left are more directives for the image on the left.
		left string
		// keys are typed after main.go is opened.
		keys   string
		height int
		// images reports whether the terminal draws images, as ASCII art.
		images   bool
		expected string
	}{
		{
			name: "covers the status and the language", zindex: 1, height: 10, images: true,
			expected: statusBarImageAbove,
		},
		{
			name: "is drawn under the status and the language", zindex: -1, height: 10, images: true,
			expected: statusBarImageBelow,
		},
		{
			name: "terminal without image support", zindex: 1, height: 10,
			expected: statusBarImageAlt,
		},
		{
			name: "overflows over the window below", zindex: 1, height: 16, images: true,
			left:     ` | height 5 | y_offset 2 | overflow`,
			keys:     `<c-\\>windowsplit<space>down<enter>`,
			expected: statusBarImageOverflow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interrupts := make(chan struct{}, 1)
			newTestPublishOverride = func(term.Event) bool {
				select {
				case interrupts <- struct{}{}:
				default:
				}
				return true
			}
			t.Cleanup(func() { newTestPublishOverride = nil })

			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			for name, data := range map[string][]byte{
				"main.go":   []byte("package main\n\nfunc main() {\n\tprintln(\"hud\")\n}\n"),
				"left.png":  imageuritest.StatusBarLeftPNG,
				"right.png": imageuritest.StatusBarRightPNG,
			} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
			}
			fileURI := func(name string) string {
				return (&url.URL{Scheme: "file", Path: filepath.Join(dir, name)}).String()
			}

			cfg := defaultConfigWithWrap(false)
			cfg.cfg["editor"].(map[string]any)["status_bar"] = map[string]any{
				"layout": fmt.Sprintf(
					`{{ .Image | src %q | width 16 | reserve 0 | x_offset 8 | fit "fill" | z_index %d%s }}`+
						` {{ .Status | bold }}  {{ .Filepath }}{{ .ShiftRight }}`+
						`{{ .CursorColumn }}:{{ .CursorLine }}  `+
						`{{ .Image | src %q | width 3 | reserve 0 | x_offset 1 | fit "fill" | z_index %d }}`+
						`{{ .Language }} `,
					fileURI("left.png"), tt.zindex, tt.left, fileURI("right.png"), tt.zindex),
			}
			m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir, nopShutdownShaderConfig())
			t.Cleanup(func() { _ = m.Close() })
			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			require.NoError(t, m.addOrCreateWorkspace(uri))
			m.quiesce()

			h := newSafeHandler(m)
			h.Resize(width, tt.height)
			var w handlertest.Writer = term.NewStringWriter(width, tt.height)
			if tt.images {
				w = asciiart.NewStringWriter(width, tt.height, asciiart.DefaultConfig())
			}
			keys, err := term.ParseKeys(`<c-\\>edit<space>main.go<enter>` + tt.keys)
			require.NoError(t, err)
			for _, key := range keys {
				h.Handle(term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey})
			}
			awaitFrame(t, h, w, interrupts, tt.expected)
		})
	}
}

func TestStatusBarRestyleIntegration(t *testing.T) {
	const width, height = 60, 10
	interrupts := make(chan struct{}, 1)
	newTestPublishOverride = func(term.Event) bool {
		select {
		case interrupts <- struct{}{}:
		default:
		}
		return true
	}
	t.Cleanup(func() { newTestPublishOverride = nil })

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ornament := filepath.Join(dir, "ornament.png")
	writeOrnament := func(c color.Color) {
		img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		for y := range 4 {
			for x := range 4 {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		require.NoError(t, os.WriteFile(ornament, buf.Bytes(), 0o644))
	}
	writeOrnament(color.White)
	for name, contents := range map[string]string{
		"main.go":  "package main\n",
		"other.go": "package other\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644))
	}
	src := (&url.URL{Scheme: "file", Path: ornament}).String()
	layout := func(status string) map[string]any {
		return map[string]any{"layout": fmt.Sprintf(
			`{{ .Image | src %q | width 4 | reserve 0 | x_offset 8 | fit "fill" | z_index -1 }}`+
				` %s{{ .ShiftRight }}{{ .Language }} `, src, status)}
	}

	cfg := defaultConfigWithWrap(false)
	editor := cfg.cfg["editor"].(map[string]any)
	editor["status_bar"] = layout(`{{ .Status }}`)
	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	h := newSafeHandler(m)
	h.Resize(width, height)
	w := &imageRecorder{Writer: asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())}
	typeKeys := func(keys string) {
		parsed, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, key := range parsed {
			h.Handle(term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey})
		}
	}
	draw := func() string {
		w.images = nil
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}
	typeKeys(`<c-\\>edit<space>main.go<enter>`)
	timeout := time.After(10 * time.Second)
	for draw(); !w.drew(color.White); draw() {
		select {
		case <-interrupts:
		case <-time.After(10 * time.Millisecond):
		case <-timeout:
			t.Fatalf("timed out waiting for the ornament, last frame:\n%s", w.String())
		}
	}

	writeOrnament(color.NRGBA{R: 255, A: 255})
	editor["status_bar"] = layout(`[{{ .Status }}]`)
	hud, err := m.loadHUD(context.Background())
	require.NoError(t, err)
	m.mu.Lock()
	m.applyHUD(hud)
	m.mu.Unlock()

	frame := draw()
	assert.True(t, w.drew(color.NRGBA{R: 255, A: 255}),
		"the open editor draws the ornament overwritten in place at once")
	assert.Contains(t, frame, "[ NORMAL]", "the open editor's bar has the new layout")

	typeKeys(`<c-\\>edit<space>other.go<enter>`)
	frame = draw()
	assert.Contains(t, frame, "package other")
	assert.Contains(t, frame, "[ NORMAL]", "an editor opened afterwards has the new layout")
	assert.True(t, w.drew(color.NRGBA{R: 255, A: 255}),
		"an editor opened afterwards draws the new ornament at once")
	assert.False(t, strings.Contains(frame, " NORMAL "), "no bar keeps the old layout")
}

// imageRecorder records the images drawn through it.
type imageRecorder struct {
	handlertest.Writer
	images []term.Image
}

func (w *imageRecorder) DrawImage(img term.Image) bool {
	w.images = append(w.images, img)
	return w.Writer.DrawImage(img)
}

// drew reports whether an image whose top left pixel is c was drawn.
func (w *imageRecorder) drew(c color.Color) bool {
	want := color.NRGBAModel.Convert(c)
	for _, img := range w.images {
		b := img.Src.Bounds()
		if color.NRGBAModel.Convert(img.Src.At(b.Min.X, b.Min.Y)) == want {
			return true
		}
	}
	return false
}

// awaitFrame redraws on every interrupt, as the host event loop does,
// until h draws expected. Syntax highlighting reaches the status bar on
// a scheduled tick rather than an interrupt, so it also redraws
// periodically.
func awaitFrame(
	t *testing.T, h tui.Component, w handlertest.Writer,
	interrupts <-chan struct{}, expected string,
) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		require.NoError(t, w.Flush())
		if w.String() == expected {
			return
		}
		select {
		case <-interrupts:
		case <-tick.C:
		case <-timeout:
			require.Equal(t, expected, w.String())
		}
	}
}

const (
	statusBarImageAbove = `┌━━━━━━━━━─────────────────────────────────────────────────┐
│o main.go                                                 │
├──────────────────────────────────────────────────────────┤
│package main                                              │
│                                                          │
│func main() {                                             │
│   'RMAL  main.go                                 1:1   o │
├──────────────────────────────────────────────────────────┤
│1 1  2 2                                                  │
└─────━━━──────────────────────────────────────────────────┘`
	statusBarImageBelow = `┌━━━━━━━━━─────────────────────────────────────────────────┐
│o main.go                                                 │
├──────────────────────────────────────────────────────────┤
│package main                                              │
│                                                          │
│func main() {                                             │
│  NORMAL  main.go                                 1:1  go │
├──────────────────────────────────────────────────────────┤
│1 1  2 2                                                  │
└─────━━━──────────────────────────────────────────────────┘`
	// The default alt, U+F00D, replaces the cell under the center of
	// each image.
	statusBarImageAlt = "┌━━━━━━━━━─────────────────────────────────────────────────┐\n" +
		"│o main.go                                                 │\n" +
		"├──────────────────────────────────────────────────────────┤\n" +
		"│package main                                              │\n" +
		"│                                                          │\n" +
		"│func main() {                                             │\n" +
		"│  NORMA\uf00d  main.go                                 1:1  g\uf00d │\n" +
		"├──────────────────────────────────────────────────────────┤\n" +
		"│1 1  2 2                                                  │\n" +
		"└─────━━━──────────────────────────────────────────────────┘"
	// The image on the left covers the bar, the frames between the two
	// windows and the first line of the window below.
	statusBarImageOverflow = "┌──────────────────────────────────────────────────────────┐\n" +
		"│o main.go                                                 │\n" +
		"├──────────────────────────────────────────────────────────┤\n" +
		"│package main                                              │\n" +
		"│                                                          │\n" +
		"│func main() {                                             │\n" +
		"│  =ORMAL  main.go                                 1:1   o │\n" +
		"└─,*`+ *;c:=c+;:-──────────────────────────────────────────┘\n" +
		"┌   '──────────────────────────────────────────────────────┐\n" +
		"│   b` !11011?11!                                          │\n" +
		"│                    workspaceWallpaper                    │\n" +
		"│                                                          │\n" +
		"│                                                          │\n" +
		"├──────────────────────────────────────────────────────────┤\n" +
		"│1 1  2 2                                                  │\n" +
		"└─────━━━──────────────────────────────────────────────────┘"
)
