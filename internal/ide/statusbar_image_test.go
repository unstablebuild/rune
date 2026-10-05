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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

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
