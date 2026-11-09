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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	htmlcomp "unstable.build/rune/internal/component/html"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
)

const (
	testWidth  = 20
	testHeight = 3
)

func waitFor(t *testing.T, fn func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u
}

// padLines builds the expected StringWriter output by padding each
// line to testWidth with trailing spaces.
func padLines(lines ...string) string {
	padded := make([]string, len(lines))
	for i, l := range lines {
		padded[i] = l + strings.Repeat(" ", testWidth-len(l))
	}
	return strings.Join(padded, "\n")
}

func TestHandleAndDraw(t *testing.T) {
	t.Run("loading", func(t *testing.T) {
		tests := []struct {
			name        string
			keys        string
			wantExit    bool
			wantHandled bool
		}{
			{"esc exits", "<esc>", true, true},
			{"q exits", "q", true, true},
			{"ctrl-c cancels", "<ctrl-c>", false, true},
			{"j not handled", "j", false, false},
			{"unknown key not handled", "x", false, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
				}))
				defer srv.Close()

				h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
				defer func() { _ = h.Close() }()
				h.Resize(testWidth, testHeight)

				keys, err := term.ParseKeys(tt.keys)
				require.NoError(t, err)

				var exit, handled bool
				for _, k := range keys {
					exit, handled = h.Handle(term.Event{
						Type: term.EventKey,
						Ch:   k.Ch, Mod: k.Mod, Key: k.Key,
					})
				}
				assert.Equal(t, tt.wantExit, exit, "exit")
				assert.Equal(t, tt.wantHandled, handled, "handled")
			})
		}
	})

	t.Run("loaded", func(t *testing.T) {
		// Five paragraphs produce 10 rendered lines (trailing blank):
		//   0:AAA 1:_ 2:BBB 3:_ 4:CCC 5:_ 6:DDD 7:_ 8:EEE 9:_
		// Max seek offset = 7 with testHeight=3.
		const scrollHTML = "<p>AAA</p><p>BBB</p><p>CCC</p><p>DDD</p><p>EEE</p>"

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, scrollHTML)
		}))
		defer srv.Close()

		h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
		defer func() { _ = h.Close() }()
		h.Resize(testWidth, testHeight)

		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)

		top := padLines("AAA", "", "BBB")
		down1 := padLines("", "BBB", "")
		down2 := padLines("BBB", "", "CCC")
		page := padLines("", "CCC", "")
		bottom := padLines("", "EEE", "")

		handlertest.RunHandlerSequence(t, h, testWidth, testHeight,
			[]handlertest.SequenceTestCase{
				// Initial render at offset 0.
				{InputSequence: "", Expected: top},

				// ── Single-line scroll ──
				{InputSequence: "j", Expected: down1},
				{InputSequence: "k", Expected: top},
				{InputSequence: "<down>", Expected: down1},
				{InputSequence: "<up>", Expected: top},
				{InputSequence: "jj", Expected: down2},

				// Return to top before next group.
				{InputSequence: "g", Expected: top},

				// ── Top / bottom ──
				{InputSequence: "<shift-g>", Expected: bottom},
				{InputSequence: "g", Expected: top},
				{InputSequence: "<end>", Expected: bottom},
				{InputSequence: "<home>", Expected: top},

				// ── Half-page scroll (height/2 = 1 line) ──
				{InputSequence: "d", Expected: down1},
				{InputSequence: "u", Expected: top},

				// ── Full-page scroll (height = 3 lines) ──
				{InputSequence: "f", Expected: page},
				{InputSequence: "b", Expected: top},
				{InputSequence: "<pgdn>", Expected: page},
				{InputSequence: "<pgup>", Expected: top},
				{InputSequence: "<space>", Expected: page},

				// ── Exit / unknown keys don't change render ──
				{InputSequence: "<esc>", Expected: page},
				{InputSequence: "q", Expected: page},
				{InputSequence: "x", Expected: page},
			},
		)
	})

	t.Run("click link navigates", func(t *testing.T) {
		// Three-page server: / → /page2 → /page3, each with a link
		// to the next page.  This exercises the full mouse pipeline
		// including the MouseRelease that must separate consecutive
		// clicks for mouse.Mouse to recognise them.
		//
		// Rendered layout for every page (width=20, height=3):
		//   row 0: paragraph text
		//   row 1: (blank)
		//   row 2: link text         ← click target
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base := "http://" + r.Host
			switch r.URL.Path {
			case "/":
				_, _ = fmt.Fprintf(w, `<p>Page1</p><p><a href="%s/page2">Next2</a></p>`, base)
			case "/page2":
				_, _ = fmt.Fprintf(w, `<p>Page2</p><p><a href="%s/page3">Next3</a></p>`, base)
			case "/page3":
				_, _ = fmt.Fprint(w, `<p>Page3</p>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
		defer func() { _ = h.Close() }()
		h.Resize(testWidth, testHeight)

		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)
		h.Resize(testWidth, testHeight)

		// ── Verify page 1 renders with the link visible ──
		got := handlertest.DrawHandler(h, testWidth, testHeight)
		assert.Equal(t, padLines("Page1", "", "Next2"), got, "page 1 render")

		// Verify the resolved component actually reports a link at (0,2).
		r := h.current.Resolved()
		require.NotNil(t, r)
		link := r.LinkAt(0, 2)
		require.NotNil(t, link, "LinkAt(0,2) should find a link")
		assert.Contains(t, link.URL, "/page2", "link URL")

		origComponent := h.current

		// ── Click the link ──
		_, handled := h.Handle(term.Event{
			Type: term.EventMouse, Key: term.MouseLeft,
			MouseX: 0, MouseY: 2,
		})
		assert.True(t, handled, "click should be handled")

		// The handler must have swapped h.current to a new component.
		// Use pointer comparison (not assert.Same) to avoid data races
		// from testify's reflection reading the async component's fields.
		assert.True(t, h.current != origComponent,
			"current component should change after link click")

		// Release the mouse button so the next click is recognised.
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})

		// ── Wait for page 2 and verify ──
		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)
		h.Resize(testWidth, testHeight)

		got = handlertest.DrawHandler(h, testWidth, testHeight)
		assert.Equal(t, padLines("Page2", "", "Next3"), got, "page 2 render")

		// ── Click without MouseRelease should NOT navigate ──
		// mouse.Mouse treats a second MouseLeft without an
		// intervening MouseRelease as a drag, not a new click.
		//
		// First MouseLeft is a fresh click (navigates page2→page3).
		// The second MouseLeft WITHOUT a release in between is a drag.
		_, handled = h.Handle(term.Event{
			Type: term.EventMouse, Key: term.MouseLeft,
			MouseX: 0, MouseY: 2,
		})
		assert.True(t, handled)

		afterFirstClick := h.current

		_, handled = h.Handle(term.Event{
			Type: term.EventMouse, Key: term.MouseLeft,
			MouseX: 0, MouseY: 2,
		})
		// Handled as drag/selection, but no navigation occurred.
		assert.True(t, handled)
		assert.True(t, h.current == afterFirstClick,
			"without MouseRelease, second MouseLeft should be a drag, not a navigation")

		// ── Release and verify page 3 loaded ──
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})

		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)
		h.Resize(testWidth, testHeight)

		got = handlertest.DrawHandler(h, testWidth, testHeight)
		assert.Equal(t, padLines("Page3", "", ""), got, "page 3 render")
	})
}

func TestClickSamePageAnchorLink(t *testing.T) {
	// A page with a link whose href is the full URL with a #fragment.
	// Clicking that link should scroll to the anchor instead of
	// fetching a new page.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_, _ = fmt.Fprintf(w,
			`<p><a href="%s/#sec">GoSec</a></p><h2 id="sec">Section</h2>`, base)
	}))
	defer srv.Close()

	h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
	defer func() { _ = h.Close() }()
	h.Resize(testWidth, testHeight)

	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateLoaded
	}, 5*time.Second)
	h.Resize(testWidth, testHeight)

	origComponent := h.current

	// The link renders on row 0.
	r := h.current.Resolved()
	require.NotNil(t, r)
	link := r.LinkAt(0, 0)
	require.NotNil(t, link, "LinkAt(0,0) should find the anchor link")
	assert.Contains(t, link.URL, "#sec")

	// Click the link.
	_, handled := h.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: 0, MouseY: 0,
	})
	assert.True(t, handled, "click should be handled")
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})

	// The component should NOT have changed (same-page navigation).
	assert.True(t, h.current == origComponent,
		"same-page anchor click should not create a new component")

	// The cache should still have only one entry.
	assert.Len(t, h.cache, 1, "no new cache entry for same-page anchor")
}

func TestHandleMouseSetsTheLinkPointer(t *testing.T) {
	// Content layout: the paragraph on row 0, the link on row 2.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<p>Page1</p><p><a href="http://%s/page2">Next2</a></p>`, r.Host)
	}))
	defer srv.Close()

	bar := []Option{WithNavigationBar(BarTop)}
	tests := []struct {
		name string
		opts []Option
		y    int
		want bool
	}{
		{"link", nil, 2, true},
		{"text", nil, 0, false},
		{"link below the navigation bar", bar, barHeight + 2, true},
		{"navigation bar over the link row", bar, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"), tt.opts...)
			defer func() { _ = h.Close() }()
			h.Resize(testWidth, testHeight+barHeight)
			waitFor(t, func() bool {
				return h.current.State() == htmlcomp.StateLoaded
			}, 5*time.Second)
			h.Resize(testWidth, testHeight+barHeight)

			p := new(term.Pointer)
			h.Handle(term.Event{
				Type: term.EventMouse, MouseY: tt.y,
				Context: term.ContextWithPointer(context.Background(), p),
			})
			shape, set := p.Shape()
			assert.Equal(t, tt.want, set)
			if tt.want {
				assert.Equal(t, term.PointerLink, shape)
			}
		})
	}
}

// ── Non-Handle/Draw tests ──────────────────────────────────────────

func TestNew(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<h1>Hello</h1>")
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL))
	defer func() { _ = h.Close() }()

	require.NotNil(t, h)
	assert.NotNil(t, h.current)
	assert.Len(t, h.cache, 1)
}

func TestResolvedAfterLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<h1>Title</h1><p>Content</p>")
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL))
	defer func() { _ = h.Close() }()

	h.Resize(40, 10)

	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateLoaded
	}, 5*time.Second)

	assert.NotNil(t, h.current.Resolved())
}

func TestCursorNotShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<h1>Hello</h1>")
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL))
	defer func() { _ = h.Close() }()

	_, _, show := h.Cursor()
	assert.False(t, show)
}

func TestSelectionBeforeLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL))
	defer func() { _ = h.Close() }()

	text, ok := h.Selection()
	assert.False(t, ok)
	assert.Empty(t, text)
}

func TestScrollBeforeLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL))
	defer func() { _ = h.Close() }()

	h.Resize(40, 10)

	assert.False(t, h.SeekUp())
	assert.False(t, h.SeekDown())
	assert.Equal(t, 0, h.SeekOffset())
	assert.Equal(t, 0, h.MaxSeekOffset())
	assert.False(t, h.ScrollUp(1))
	assert.False(t, h.ScrollDown(1))
}

func TestWithOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<h1>Hello</h1>")
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL),
		WithHTTPClient(client),
		WithSelectionAttrs(term.Attributes{Attrs: term.AttrBold}),
	)
	defer func() { _ = h.Close() }()

	require.NotNil(t, h)
	assert.Equal(t, client, h.httpClient)
	assert.Equal(t, term.Attributes{Attrs: term.AttrBold}, h.selectionAttrs)
}

func TestCacheReuse(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_, _ = fmt.Fprint(w, "<h1>Hello</h1>")
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	u := mustParseURL(t, srv.URL)
	h := New(interrupter, u)
	defer func() { _ = h.Close() }()

	h.Resize(40, 10)

	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateLoaded
	}, 5*time.Second)

	first := h.current

	// Navigate to the same URL should reuse the cached component.
	h.navigateTo(u)
	assert.Same(t, first, h.current)
	assert.Len(t, h.cache, 1)
}

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

func TestClickPprofTableLink(t *testing.T) {
	// Serve the pprof HTML on /debug/pprof/ with a redirect from
	// /debug/pprof (no trailing slash), just like the real server.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pprofHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sizes := []struct {
		name          string
		width, height int
	}{
		{"80x40", 80, 40},
		{"172x43", 172, 43},
	}

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			h := New(term.NopInterrupter(),
				mustParseURL(t, srv.URL+"/debug/pprof"))
			defer func() { _ = h.Close() }()
			h.Resize(sz.width, sz.height)

			waitFor(t, func() bool {
				return h.current.State() == htmlcomp.StateLoaded
			}, 5*time.Second)
			h.Resize(sz.width, sz.height)

			// Draw to find where "allocs" renders.
			rendered := handlertest.DrawHandler(h, sz.width, sz.height)
			lines := strings.Split(rendered, "\n")

			allocsY := -1
			for y, line := range lines {
				if strings.Contains(line, "allocs") &&
					!strings.Contains(line, "allocations") &&
					!strings.Contains(line, "allocs:") {
					allocsY = y
					break
				}
			}
			require.NotEqual(t, -1, allocsY, "could not find 'allocs' in rendered output")
			t.Logf("allocs rendered at Y=%d", allocsY)

			// Find X position of "allocs" text on that line.
			allocsX := strings.Index(lines[allocsY], "allocs")
			require.NotEqual(t, -1, allocsX)
			t.Logf("allocs rendered at X=%d, Y=%d", allocsX, allocsY)

			// Verify that LinkAt at the rendered position finds the link.
			r := h.current.Resolved()
			link := r.LinkAt(allocsX, allocsY)
			require.NotNil(t, link, "LinkAt(%d, %d) should find the allocs link", allocsX, allocsY)
			assert.Contains(t, link.URL, "/debug/pprof/allocs?debug=1")

			// Also verify SpanAt returns content at that position.
			text, _, spanOK := r.SpanAt(allocsX, allocsY)
			assert.True(t, spanOK, "SpanAt should find content at (%d, %d)", allocsX, allocsY)
			assert.Contains(t, text, "allocs", "SpanAt text should contain 'allocs'")

			origComponent := h.current

			// Click on the allocs link using the rendered coordinates.
			_, handled := h.Handle(term.Event{
				Type:   term.EventMouse,
				Key:    term.MouseLeft,
				MouseX: allocsX,
				MouseY: allocsY,
			})
			assert.True(t, handled, "click at allocs position should be handled")

			// The handler should have navigated to the allocs URL.
			assert.True(t, h.current != origComponent,
				"handler should navigate to new page after clicking allocs link")
		})
	}
}

func TestCloseClosesAllCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "<h1>%s</h1>", r.URL.Path)
	}))
	defer srv.Close()

	interrupter := term.NopInterrupter()
	h := New(interrupter, mustParseURL(t, srv.URL+"/a"))
	h.Resize(40, 10)

	// Add a second entry to the cache.
	h.navigateTo(mustParseURL(t, srv.URL+"/b"))

	assert.Len(t, h.cache, 2)
	require.NotPanics(t, func() { _ = h.Close() })
}

func TestHistoryNavigation(t *testing.T) {
	// Three-page server: /p1, /p2, /p3, each showing their page name.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/p1":
			_, _ = fmt.Fprintf(w, `<p>Page1</p><p><a href="%s/p2">GoP2</a></p>`, base)
		case "/p2":
			_, _ = fmt.Fprintf(w, `<p>Page2</p><p><a href="%s/p3">GoP3</a></p>`, base)
		case "/p3":
			_, _ = fmt.Fprint(w, `<p>Page3</p>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/p1"))
	defer func() { _ = h.Close() }()
	h.Resize(testWidth, testHeight)

	waitLoaded := func() {
		t.Helper()
		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)
		h.Resize(testWidth, testHeight)
	}
	waitLoaded()

	page1 := padLines("Page1", "", "GoP2")
	page2 := padLines("Page2", "", "GoP3")
	page3 := padLines("Page3", "", "")

	assert.Equal(t, page1, handlertest.DrawHandler(h, testWidth, testHeight), "initial page 1")

	// Navigate to page 2 by clicking the link.
	clickLink := func() {
		t.Helper()
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 2})
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})
	}
	clickLink()
	waitLoaded()
	assert.Equal(t, page2, handlertest.DrawHandler(h, testWidth, testHeight), "page 2")

	// Navigate to page 3.
	clickLink()
	waitLoaded()
	assert.Equal(t, page3, handlertest.DrawHandler(h, testWidth, testHeight), "page 3")

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		}
	}

	// ── Test all four back/forward key bindings ──

	// Alt+Left → back to page 2.
	sendKeys("<alt-left>")
	waitLoaded()
	assert.Equal(t, page2, handlertest.DrawHandler(h, testWidth, testHeight), "alt-left: back to page 2")

	// H → back to page 1.
	sendKeys("<shift-h>")
	waitLoaded()
	assert.Equal(t, page1, handlertest.DrawHandler(h, testWidth, testHeight), "H: back to page 1")

	// Alt+Right → forward to page 2.
	sendKeys("<alt-right>")
	waitLoaded()
	assert.Equal(t, page2, handlertest.DrawHandler(h, testWidth, testHeight), "alt-right: forward to page 2")

	// L → forward to page 3.
	sendKeys("<shift-l>")
	waitLoaded()
	assert.Equal(t, page3, handlertest.DrawHandler(h, testWidth, testHeight), "L: forward to page 3")

	// ── Back at end does nothing ──
	sendKeys("<alt-right>")
	assert.Equal(t, page3, handlertest.DrawHandler(h, testWidth, testHeight), "forward at end is no-op")

	// ── Go all the way back, forward at beginning does nothing ──
	sendKeys("<alt-left><alt-left>")
	waitLoaded()
	assert.Equal(t, page1, handlertest.DrawHandler(h, testWidth, testHeight), "back to page 1")
	sendKeys("<alt-left>")
	assert.Equal(t, page1, handlertest.DrawHandler(h, testWidth, testHeight), "back at beginning is no-op")

	// ── Navigating forward from mid-history truncates forward history ──
	sendKeys("<alt-right>") // page 2
	waitLoaded()
	assert.Equal(t, page2, handlertest.DrawHandler(h, testWidth, testHeight), "forward to page 2")

	// Click the link on page 2 (goes to page 3 via a new navigation,
	// which should truncate the old forward entry for page 3).
	clickLink()
	waitLoaded()
	assert.Equal(t, page3, handlertest.DrawHandler(h, testWidth, testHeight), "navigate to page 3 from page 2")

	// Forward should do nothing because the old forward history was truncated.
	sendKeys("<alt-right>")
	assert.Equal(t, page3, handlertest.DrawHandler(h, testWidth, testHeight), "no forward after new navigation")
}

func TestNavigateBackFromError(t *testing.T) {
	// Page 1 works, but its link points to a URL that returns 404.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/ok":
			_, _ = fmt.Fprintf(w, `<p>Good</p><p><a href="%s/bad">GoBad</a></p>`, base)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/ok"))
	defer func() { _ = h.Close() }()
	h.Resize(testWidth, testHeight)

	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateLoaded
	}, 5*time.Second)
	h.Resize(testWidth, testHeight)

	good := padLines("Good", "", "GoBad")
	assert.Equal(t, good, handlertest.DrawHandler(h, testWidth, testHeight), "page renders")

	// Click the link to /bad (which 404s).
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 2})
	h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})

	// Wait for the error state.
	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateError
	}, 5*time.Second)

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		}
	}

	tests := []struct {
		name string
		keys string
	}{
		{"Alt+Left", "<alt-left>"},
		{"H", "<shift-h>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Navigate back to the good page.
			sendKeys(tt.keys)
			waitFor(t, func() bool {
				return h.current.State() == htmlcomp.StateLoaded
			}, 5*time.Second)
			h.Resize(testWidth, testHeight)
			assert.Equal(t, good, handlertest.DrawHandler(h, testWidth, testHeight), "back to good page")

			// Navigate forward to the error page again for the next subtest.
			sendKeys("<alt-right>")
			waitFor(t, func() bool {
				return h.current.State() == htmlcomp.StateError
			}, 5*time.Second)
		})
	}
}

func TestSlashSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<p>hello world hello</p>")
	}))
	defer srv.Close()

	h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
	defer func() { _ = h.Close() }()
	h.Resize(testWidth, testHeight)

	waitFor(t, func() bool {
		return h.current.State() == htmlcomp.StateLoaded
	}, 5*time.Second)
	h.Resize(testWidth, testHeight)

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		}
	}

	// '/' opens search prompt, cursor should appear.
	sendKeys("/")
	_, _, show := h.Cursor()
	assert.True(t, show, "cursor visible during search input")

	// Type "hello" and confirm.
	sendKeys("hello<enter>")

	// Cursor should hide.
	_, _, show = h.Cursor()
	assert.False(t, show, "cursor hidden after search confirm")

	// 'n' advances, 'N' goes back.
	sendKeys("n")
	sendKeys("<shift-n>")

	// Esc during search input should not exit.
	sendKeys("/h<esc>")
	_, _, show = h.Cursor()
	assert.False(t, show, "cursor hidden after search cancel")

	// Esc now should exit handler.
	exit, _ := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
}

func TestRefresh(t *testing.T) {
	// Server that changes its response on every request so we can
	// detect that a refresh actually re-fetched.
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		_, _ = fmt.Fprintf(w, "<p>V%d</p>", reqCount)
	}))
	defer srv.Close()

	h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"))
	defer func() { _ = h.Close() }()
	h.Resize(testWidth, testHeight)

	waitLoaded := func() {
		t.Helper()
		waitFor(t, func() bool {
			return h.current.State() == htmlcomp.StateLoaded
		}, 5*time.Second)
		h.Resize(testWidth, testHeight)
	}
	waitLoaded()

	v1 := padLines("V1", "", "")
	assert.Equal(t, v1, handlertest.DrawHandler(h, testWidth, testHeight), "initial fetch")

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		}
	}

	// Refresh with 'r' key.
	sendKeys("r")
	waitLoaded()
	v2 := padLines("V2", "", "")
	assert.Equal(t, v2, handlertest.DrawHandler(h, testWidth, testHeight), "after r refresh")

	// Refresh with F5.
	sendKeys("<f5>")
	waitLoaded()
	v3 := padLines("V3", "", "")
	assert.Equal(t, v3, handlertest.DrawHandler(h, testWidth, testHeight), "after F5 refresh")
}

func TestHandlerPointerLeaves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_, _ = fmt.Fprint(w, "<h1>Title</h1>")
		for i := range 60 {
			_, _ = fmt.Fprintf(w, `<p>%s <a href="%s/p%d">link %d</a> <a href="#a%d">anchor</a></p>`,
				strings.Repeat("a long paragraph line ", i%7+1), base, i, i, i)
		}
	}))
	defer srv.Close()

	for _, opts := range [][]Option{nil, {WithNavigationBar(BarTop)}, {WithNavigationBar(BarBottom)}} {
		for _, size := range [][2]int{{60, 15}, {1, 1}} {
			h := New(term.NopInterrupter(), mustParseURL(t, srv.URL+"/"), opts...)
			h.Resize(size[0], size[1])
			waitFor(t, func() bool { return h.current.State() == htmlcomp.StateLoaded }, 5*time.Second)
			h.Resize(size[0], size[1])
			h.ScrollDown(5)
			loaded, offset := h.current, h.SeekOffset()

			for _, out := range []term.Coordinates{{X: -1, Y: 2}, {X: 2, Y: -40}, {X: 400, Y: 400}} {
				h.Handle(term.Event{Type: term.EventMouse, MouseX: 1, MouseY: 1})
				h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
			}
			assert.Same(t, loaded, h.current, "a leave does not follow a link")
			assert.Equal(t, offset, h.SeekOffset(), "a leave does not scroll")
			_, selected := h.Selection()
			assert.False(t, selected, "a leave does not select")

			rhandlertest.RunPointerLeaves(t, h, size[0], size[1])
			require.NoError(t, h.Close())
		}
	}
}
