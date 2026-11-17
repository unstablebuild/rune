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

package vte

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// relayedPair is a canonical emulator and a replica parsing the same
// stream, with the canonical one relaying what it reads for file-backed
// transmissions the way a workspace server does.
type relayedPair struct {
	canonical, replica *graphicsHarness
}

func newRelayedPair(t *testing.T, maxBytes int) (*relayedPair, string) {
	t.Helper()
	fs, dir := localFS(t)
	canonical := newGraphicsHarnessFS(t, testCell, fs)
	// Each test gets its own temporary directory, so temporary-file
	// transmissions of tests running at once cannot collide.
	canonical.ph.tempDir = dir
	// The replica has no filesystem: a replica on another machine could
	// not reach the file, so it must never need to.
	replica := newGraphicsHarness(t, testCell)
	replica.ph.observer = true
	canonical.ph.relay.tee(maxBytes, replica.ph.relay.push)
	return &relayedPair{canonical: canonical, replica: replica}, dir
}

// write feeds both emulators the same bytes, the canonical one first,
// which is the order a workspace server tees them in.
func (p *relayedPair) write(s string) {
	p.canonical.write(s)
	p.replica.write(s)
}

func graphicsAPC(ctl string, payload []byte) string {
	return "\x1b_G" + ctl + ";" + base64.StdEncoding.EncodeToString(payload) + "\x1b\\"
}

// TestGraphicsRelayKeepsReplicasInStep reproduces a replica drifting
// from the terminal it replicates on a file-backed transmission. The
// canonical emulator deletes a temporary file once it read it, so a
// replica reading after it failed where the canonical one succeeded,
// and only one of them moved the cursor past the image.
func TestGraphicsRelayKeepsReplicasInStep(t *testing.T) {
	// 30x40 pixels are 3x2 cells, so a placement moves the cursor.
	image := make([]byte, 30*40*4)
	tests := []struct {
		desc     string
		medium   string
		name     func(dir string) string
		maxBytes int
		placed   bool
		reply    string
		kept     bool
	}{
		{
			desc:   "temporary file",
			medium: "t",
			name: func(dir string) string {
				return filepath.Join(dir, "tty-graphics-protocol-relay.rgba")
			},
			placed: true,
			reply:  "\x1b_Gi=1;OK\x1b\\",
		},
		{
			desc:   "regular file",
			medium: "f",
			name:   func(dir string) string { return filepath.Join(dir, "img.rgba") },
			placed: true,
			reply:  "\x1b_Gi=1;OK\x1b\\",
			kept:   true,
		},
		{
			desc:   "missing file",
			medium: "f",
			name:   func(dir string) string { return filepath.Join(dir, "missing.rgba") },
			reply:  "\x1b_Gi=1;EBADF:Failed to read image file\x1b\\",
		},
		{
			desc:     "file too large to relay",
			medium:   "f",
			name:     func(dir string) string { return filepath.Join(dir, "img.rgba") },
			maxBytes: len(image) - 1,
			reply:    "\x1b_Gi=1;EBADF:Failed to read image file\x1b\\",
			kept:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			pair, dir := newRelayedPair(t, tt.maxBytes)
			name := tt.name(dir)
			if tt.desc != "missing file" {
				require.NoError(t, os.WriteFile(name, image, 0o600))
				t.Cleanup(func() { _ = os.Remove(name) })
			}

			pair.write(graphicsAPC("a=T,i=1,s=30,v=40,t="+tt.medium, []byte(name)) + "x")

			assert.Equal(t, tt.reply, pair.canonical.responses())
			assert.Empty(t, pair.replica.responses(), "a replica never answers")
			assert.Equal(t, pair.canonical.cursor(), pair.replica.cursor())
			assert.Len(t, pair.replica.visible(0), len(pair.canonical.visible(0)))
			want := term.Coordinates{X: 1}
			if tt.placed {
				want = term.Coordinates{X: 4, Y: 1}
				assert.Len(t, pair.replica.visible(0), 1)
			}
			assert.Equal(t, want, pair.replica.cursor())
			if tt.desc != "missing file" {
				_, err := os.Stat(name)
				assert.Equal(t, tt.kept, err == nil, "stat %s: %v", name, err)
			}
		})
	}
}

// TestGraphicsRelayOrder pins that a replica takes relayed outcomes in
// the order the canonical emulator read them, and fails a transmission
// it was given nothing for rather than reading the file itself.
func TestGraphicsRelayOrder(t *testing.T) {
	pair, dir := newRelayedPair(t, 0)
	small := filepath.Join(dir, "small.rgba")
	require.NoError(t, os.WriteFile(small, make([]byte, 10*20*4), 0o600))
	wide := filepath.Join(dir, "wide.rgba")
	require.NoError(t, os.WriteFile(wide, make([]byte, 50*20*4), 0o600))

	pair.write(graphicsAPC("a=T,i=1,s=10,v=20,t=f", []byte(small)) +
		graphicsAPC("a=T,i=2,s=50,v=20,t=f", []byte(wide)))
	assert.Equal(t, term.Coordinates{X: 6}, pair.replica.cursor())
	assert.Equal(t, pair.canonical.cursor(), pair.replica.cursor())

	pair.replica.write(graphicsAPC("a=T,i=3,s=10,v=20,t=f", []byte(small)))
	assert.Equal(t, term.Coordinates{X: 6}, pair.replica.cursor(),
		"nothing was relayed for the transmission")
	assert.FileExists(t, small)
}

// TestGraphicsRelaySurvivesReset pins that a full reset keeps a
// replica taking the outcomes queued for it: the transmissions after
// the reset are still the ones they were relayed for.
func TestGraphicsRelaySurvivesReset(t *testing.T) {
	pair, dir := newRelayedPair(t, 0)
	p := filepath.Join(dir, "img.rgba")
	require.NoError(t, os.WriteFile(p, make([]byte, 10*20*4), 0o600))

	pair.canonical.write("\x1bc" + graphicsAPC("a=T,i=1,s=10,v=20,t=f", []byte(p)))
	pair.replica.write("\x1bc" + graphicsAPC("a=T,i=1,s=10,v=20,t=f", []byte(p)))
	assert.Equal(t, term.Coordinates{X: 1}, pair.replica.cursor())
	assert.Len(t, pair.replica.visible(0), 1)
}
