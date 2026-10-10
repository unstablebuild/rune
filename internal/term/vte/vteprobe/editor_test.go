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

package vteprobe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte"
)

// editorFixture is the ground truth recorded for a single
// <sample, editor> combination. It lives in want.json next to the
// capture.
type editorFixture struct {
	// Terminal dimensions that produced screen.ansi.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Want is the expected Result. Tabstop is checked only when
	// non-zero so existing fixtures that omit it stay tolerant.
	Want want `json:"want"`
	// MinConfidence is the threshold passed to New and also asserted
	// against the returned Result.Confidence.
	MinConfidence float64 `json:"minConfidence"`
}

// want mirrors Result for JSON serialization. It is decoupled from the
// public Result so we can omit fields whose value should not be
// asserted on a per-fixture basis.
type want struct {
	CursorAtScroll coords `json:"cursorAtScroll"`
	Scroll         coords `json:"scroll"`
	Tabstop        int    `json:"tabstop"`
	Folded         bool   `json:"folded"`
}

// coords mirrors term.Coordinates so we can JSON-decode it with
// lower-case field names.
type coords struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func TestInferEditorCaptures(t *testing.T) {
	t.Parallel()

	samples, err := discoverSamples("testdata/samples")
	require.NoError(t, err)
	require.NotEmpty(t, samples, "no samples found under testdata/samples")

	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()

			sampleBytes, err := os.ReadFile(s.sampleFile)
			require.NoError(t, err)

			if len(s.editors) == 0 {
				t.Skipf("sample %q has no editor captures yet; "+
					"run testdata/capture.sh %s <editor>",
					s.name, s.name)
				return
			}

			for _, ed := range s.editors {
				t.Run(ed.name, func(t *testing.T) {
					t.Parallel()
					runEditorCase(t, sampleBytes, ed)
				})
			}
		})
	}
}

func runEditorCase(t *testing.T, sampleBytes []byte, ed editorCase) {
	t.Helper()

	fx, err := loadEditorFixture(ed.dir)
	require.NoError(t, err)

	screen, err := os.ReadFile(filepath.Join(ed.dir, "screen.ansi"))
	require.NoError(t, err)
	cur, err := readCursor(filepath.Join(ed.dir, "cursor.txt"))
	require.NoError(t, err)

	data := captureToReplayBytes(screen, fx.Width, fx.Height, cur)
	buf, _, err := vte.Replay(fx.Width, fx.Height, data)
	require.NoError(t, err)

	inf := New([]int{4, 2, 8}, fx.MinConfidence, 8<<20)
	got, err := inf.Infer(buf.RawCells(), cur, cellLines(splitLines(sampleBytes)), nil)
	require.NoError(t, err)
	assert.Equal(t, term.Coordinates(fx.Want.CursorAtScroll), got.CursorAtScroll, "cursorAtScroll")
	assert.Equal(t, term.Coordinates(fx.Want.Scroll), got.Scroll, "scroll")
	if fx.Want.Tabstop > 0 {
		assert.Equal(t, fx.Want.Tabstop, got.Tabstop, "tabstop")
	}
	assert.Equal(t, fx.Want.Folded, got.Folded, "folded")
	assert.GreaterOrEqual(t, got.Confidence, fx.MinConfidence, "confidence")
}

// sample describes a sample file and the editor captures available for
// it on disk.
type sample struct {
	name       string
	sampleFile string
	editors    []editorCase
}

type editorCase struct {
	name string // editor name (vim, nvim, helix, ...)
	dir  string // directory containing screen.ansi, cursor.txt, want.json
}

func discoverSamples(root string) ([]sample, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var samples []sample
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sampleFile := filepath.Join(root, e.Name(), "sample.txt")
		if _, err := os.Stat(sampleFile); err != nil {
			// Directories without a sample.txt are not samples.
			continue
		}
		s := sample{name: e.Name(), sampleFile: sampleFile}

		editorEntries, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, ee := range editorEntries {
			if !ee.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name(), ee.Name())
			if _, err := os.Stat(filepath.Join(dir, "want.json")); err != nil {
				// Not a complete fixture yet; ignore.
				continue
			}
			s.editors = append(s.editors, editorCase{name: ee.Name(), dir: dir})
		}
		sort.Slice(s.editors, func(i, j int) bool {
			return s.editors[i].name < s.editors[j].name
		})
		samples = append(samples, s)
	}
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].name < samples[j].name
	})
	return samples, nil
}

func loadEditorFixture(dir string) (editorFixture, error) {
	data, err := os.ReadFile(filepath.Join(dir, "want.json"))
	if err != nil {
		return editorFixture{}, err
	}
	var f editorFixture
	if err := json.Unmarshal(data, &f); err != nil {
		return editorFixture{}, fmt.Errorf("decode want.json: %w", err)
	}
	if f.Width <= 0 || f.Height <= 0 {
		return editorFixture{}, fmt.Errorf("invalid dimensions: %dx%d", f.Width, f.Height)
	}
	return f, nil
}

func readCursor(path string) (term.Coordinates, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return term.Coordinates{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(raw)), ",", 2)
	if len(parts) != 2 {
		return term.Coordinates{}, fmt.Errorf("expected 'X,Y', got %q", string(raw))
	}
	x, err := strconv.Atoi(parts[0])
	if err != nil {
		return term.Coordinates{}, fmt.Errorf("parse X: %w", err)
	}
	y, err := strconv.Atoi(parts[1])
	if err != nil {
		return term.Coordinates{}, fmt.Errorf("parse Y: %w", err)
	}
	return term.Coordinates{X: x, Y: y}, nil
}

// captureToReplayBytes converts a tmux `capture-pane -p -e` byte stream
// into a Replay-compatible ANSI stream.
//
// tmux emits one rendered terminal row per line, separated by '\n',
// with embedded SGR escapes but without any cursor-position escapes
// between rows. To reproduce the same screen through Replay we
// re-anchor each row with a CUP (cursor position) escape, clear the
// screen up-front, and finally place the cursor where tmux reported it.
//
// We feed exactly `height` rows from the capture (truncating or padding
// with blank rows as needed) so the resulting buffer has the same
// dimensions as the original terminal.
func captureToReplayBytes(
	capture []byte,
	width, height int,
	cursor term.Coordinates,
) []byte {
	rows := splitCaptureRows(capture)
	if len(rows) > height {
		rows = rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	var out strings.Builder
	out.WriteString("\x1b[2J")   // clear screen
	out.WriteString("\x1b[1;1H") // home cursor
	for i, row := range rows {
		fmt.Fprintf(&out, "\x1b[%d;1H", i+1)
		// Reset SGR between rows so styles from one row do not bleed
		// into the next when an editor leaves attributes open at EOL.
		out.WriteString("\x1b[0m")
		out.WriteString(row)
	}
	// Final positioning so subsequent CursorAtScroll reflects tmux's
	// reported cursor location.
	fmt.Fprintf(&out, "\x1b[%d;%dH", cursor.Y+1, cursor.X+1)
	_ = width // width is enforced by vte.Replay's terminal dimensions
	return []byte(out.String())
}

// splitCaptureRows splits a tmux capture-pane output into one entry per
// rendered row. A single trailing newline is stripped so we do not
// produce a spurious empty final row.
func splitCaptureRows(data []byte) []string {
	s := string(data)
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
