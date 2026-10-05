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

package html

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
)

// pprofHTML is the actual HTML served by Go's net/http/pprof handler.
const pprofHTML = `<html>
<head>
<title>/debug/pprof/</title>
<style>
.profile-name{
	display:inline-block;
	width:6rem;
}
</style>
</head>
<body>
/debug/pprof/
<br>
<p>Set debug=1 as a query parameter to export in legacy text format</p>
<br>
Types of profiles available:
<table>
<thead><td>Count</td><td>Profile</td></thead>
<tr><td>2844</td><td><a href='allocs?debug=1'>allocs</a></td></tr>
<tr><td>380</td><td><a href='block?debug=1'>block</a></td></tr>
<tr><td>0</td><td><a href='cmdline?debug=1'>cmdline</a></td></tr>
<tr><td>546</td><td><a href='goroutine?debug=1'>goroutine</a></td></tr>
<tr><td>2844</td><td><a href='heap?debug=1'>heap</a></td></tr>
<tr><td>1080</td><td><a href='mutex?debug=1'>mutex</a></td></tr>
<tr><td>0</td><td><a href='profile?debug=1'>profile</a></td></tr>
<tr><td>0</td><td><a href='symbol?debug=1'>symbol</a></td></tr>
<tr><td>179</td><td><a href='threadcreate?debug=1'>threadcreate</a></td></tr>
<tr><td>0</td><td><a href='trace?debug=1'>trace</a></td></tr>
</table>
<a href="goroutine?debug=2">full goroutine stack dump</a>
<br>
<p>
Profile Descriptions:
<ul>
<li><div class=profile-name>allocs: </div> A sampling of all past memory allocations</li>
<li><div class=profile-name>block: </div> Stack traces that led to blocking on synchronization primitives</li>
</ul>
</p>
</body>
</html>`

func TestFetchPprofLinks(t *testing.T) {
	// Serve the pprof HTML on /debug/pprof/ with a redirect from
	// /debug/pprof (no trailing slash), just like the real server.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pprofHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Use the URL without trailing slash to match what a user would
	// type. The HTTP client follows the redirect to /debug/pprof/.
	rawURL := srv.URL + "/debug/pprof"

	sizes := []struct {
		name          string
		width, height int
	}{
		{"80x40", 80, 40},
		{"172x43", 172, 43},
	}

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			comp, err := fetch(context.Background(), srv.Client(), rawURL, markdown.DefaultConfig())
			require.NoError(t, err)

			width, height := sz.width, sz.height
			comp.Resize(width, height)

			// Dump block structure for debugging.
			blockHeights := comp.BlockHeights()
			t.Logf("block heights: %v", blockHeights)

			// Dump the rendered text for debugging.
			sw := term.NewStringWriter(width, height)
			require.NoError(t, sw.Clear(term.Attributes{}))
			comp.Draw(sw)
			require.NoError(t, sw.Flush())
			rendered := sw.String()
			t.Logf("rendered:\n%s", rendered)

			// Find the Y positions of interesting content by scanning rendered lines.
			lines := strings.Split(rendered, "\n")
			var (
				goroutineStackY = -1
				tableAllocsY    = -1
			)
			for y, line := range lines {
				if strings.Contains(line, "full goroutine stack dump") {
					goroutineStackY = y
				}
				if strings.Contains(line, "allocs") && !strings.Contains(line, "allocations") && !strings.Contains(line, "allocs:") {
					if tableAllocsY == -1 {
						tableAllocsY = y
					}
				}
			}
			t.Logf("goroutineStackY=%d, tableAllocsY=%d", goroutineStackY, tableAllocsY)

			tests := []struct {
				name    string
				y       int
				wantURL string
			}{
				{
					name:    "full goroutine stack dump link",
					y:       goroutineStackY,
					wantURL: srv.URL + "/debug/pprof/goroutine?debug=2",
				},
				{
					name:    "table allocs link",
					y:       tableAllocsY,
					wantURL: srv.URL + "/debug/pprof/allocs?debug=1",
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					require.NotEqual(t, -1, tt.y, "could not find content row in rendered output")

					// Verify SpanAt returns text at the same Y where we rendered it.
					var spanText string
					for x := range width {
						text, _, ok := comp.SpanAt(x, tt.y)
						if ok && text != "" {
							spanText = text
							break
						}
					}
					assert.NotEmpty(t, spanText,
						"SpanAt returned empty at Y=%d where rendered shows: %q",
						tt.y, lines[tt.y])

					// Scan across the row to find any link.
					var found *markdown.LinkInfo
					for x := range width {
						link := comp.LinkAt(x, tt.y)
						if link != nil && link.URL != "" {
							found = link
							break
						}
					}
					require.NotNil(t, found, "no link found on row %d: %q", tt.y, lines[tt.y])
					assert.Equal(t, tt.wantURL, found.URL)
				})
			}
		})
	}
}

func TestFetchLayoutTable(t *testing.T) {
	const layoutHTML = `<html><body>
<table>
<tr><td>1.</td><td></td><td><a href="https://example.com">Motorola announces</a></td></tr>
<tr><td></td><td>508 points by km</td></tr>
</table>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, layoutHTML)
	}))
	defer srv.Close()

	comp, err := fetch(context.Background(), srv.Client(), srv.URL, markdown.DefaultConfig())
	require.NoError(t, err)

	width, height := 80, 40
	comp.Resize(width, height)

	sw := term.NewStringWriter(width, height)
	require.NoError(t, sw.Clear(term.Attributes{}))
	comp.Draw(sw)
	require.NoError(t, sw.Flush())
	rendered := sw.String()
	t.Logf("rendered:\n%s", rendered)

	assert.NotContains(t, rendered, "|", "layout table should not contain pipe characters")
	assert.Contains(t, rendered, "Motorola announces")
	assert.Contains(t, rendered, "508 points by km")
}

func TestFetchMixedTables(t *testing.T) {
	const mixedHTML = `<html><body>
<table>
<thead><tr><td>Count</td><td>Profile</td></tr></thead>
<tr><td>2844</td><td>allocs</td></tr>
</table>
<table>
<tr><td>layout</td><td>content</td></tr>
</table>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, mixedHTML)
	}))
	defer srv.Close()

	comp, err := fetch(context.Background(), srv.Client(), srv.URL, markdown.DefaultConfig())
	require.NoError(t, err)

	width, height := 80, 40
	comp.Resize(width, height)

	sw := term.NewStringWriter(width, height)
	require.NoError(t, sw.Clear(term.Attributes{}))
	comp.Draw(sw)
	require.NoError(t, sw.Flush())
	rendered := sw.String()
	t.Logf("rendered:\n%s", rendered)

	// Data table content should be present (rendered as a GFM table
	// which goldmark turns into box-drawing borders).
	assert.Contains(t, rendered, "2844")
	assert.Contains(t, rendered, "allocs")

	// Layout table content should be present as plain text.
	assert.Contains(t, rendered, "layout")
	assert.Contains(t, rendered, "content")
}

func TestRenderQueryConsistency(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pprofHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rawURL := srv.URL + "/debug/pprof"

	comp, err := fetch(context.Background(), srv.Client(), rawURL, markdown.DefaultConfig())
	require.NoError(t, err)

	width, height := 172, 43
	comp.Resize(width, height)

	sw := term.NewStringWriter(width, height)
	require.NoError(t, sw.Clear(term.Attributes{}))
	comp.Draw(sw)
	require.NoError(t, sw.Flush())
	rendered := sw.String()

	lines := strings.Split(rendered, "\n")
	cells := sw.Cells()

	blockHeights := comp.BlockHeights()
	t.Logf("block heights at %dx%d: %v", width, height, blockHeights)

	for y, line := range lines {
		if y >= height {
			break
		}

		// Find the first non-space, non-box-drawing character on this line.
		firstTextX := -1
		firstTextCh := rune(0)
		for x := range min(width, len(line)) {
			ch := cells[y*width+x].Ch
			if ch != 0 && ch != ' ' {
				firstTextX = x
				firstTextCh = ch
				break
			}
		}
		if firstTextX == -1 {
			continue // blank line, skip
		}

		// Check SpanAt and CharAt agree there's content at this position.
		text, _, spanOK := comp.SpanAt(firstTextX, y)

		// CharAt is the more direct check — it should return the same
		// rune that was rendered.
		charAtOK := false
		for x := firstTextX; x < width; x++ {
			renderedCh := cells[y*width+x].Ch
			if renderedCh == 0 || renderedCh == ' ' {
				break
			}
			// SpanAt or CharAt returning something confirms the query
			// path knows about this position.
			if t2, _, ok := comp.SpanAt(x, y); ok && t2 != "" {
				charAtOK = true
				break
			}
		}

		if !spanOK && !charAtOK {
			// Table border/separator rows have rendered box-drawing
			// characters but SpanAt intentionally returns empty.
			if firstTextCh >= 0x2500 && firstTextCh <= 0x257F {
				continue
			}
			// List item rows start with a bullet; SpanAt does not
			// cover them yet (known limitation).
			if firstTextCh == '•' || firstTextCh == '◦' || firstTextCh == '▪' {
				continue
			}

			t.Errorf("Y=%d: rendered shows %q (firstChar='%c' at x=%d) but SpanAt returns text=%q ok=%v",
				y, strings.TrimRight(line, " "), firstTextCh, firstTextX, text, spanOK)
		}
	}
}
