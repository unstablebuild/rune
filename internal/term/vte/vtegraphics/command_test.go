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

package vtegraphics

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Command
		err   bool
	}{
		{
			name:  "query probe",
			input: "i=31,s=1,v=1,a=q,t=d,f=24;AAAA",
			want: Command{
				Action: 'q', ImageID: 31, Width: 1, Height: 1, Medium: 'd',
				Format: 24, Payload: []byte{0, 0, 0},
			},
		},
		{
			name: "every key",
			input: "a=p,q=2,f=100,t=s,s=10,v=20,S=30,O=40,o=z,m=1,N=1,i=5,I=6,p=7," +
				"x=1,y=2,w=3,h=4,X=5,Y=6,c=7,r=8,z=-9,C=1,U=1,P=11,Q=12,H=-13,V=14,d=Z",
			want: Command{
				Action: 'p', Quiet: 2, Format: 100, Medium: 's', Width: 10, Height: 20,
				DataSize: 30, DataOffset: 40, Compression: 'z', More: true, Usage: 1,
				ImageID: 5, ImageNumber: 6, PlacementID: 7, X: 1, Y: 2, W: 3, H: 4,
				CellX: 5, CellY: 6, Columns: 7, Rows: 8, Z: -9, CursorMovement: 1,
				Unicode: 1, ParentID: 11, ParentPlacementID: 12, OffsetH: -13, OffsetV: 14,
				Delete: 'Z',
			},
		},
		{
			name:  "empty control block with payload",
			input: ";AQID",
			want:  Command{Payload: []byte{1, 2, 3}},
		},
		{
			name:  "empty command",
			input: "",
			want:  Command{},
		},
		{
			name:  "unpadded base64",
			input: "m=0;AQ",
			want:  Command{Payload: []byte{1}},
		},
		{
			name:  "empty payload",
			input: "m=0;",
			want:  Command{Payload: []byte{}},
		},
		{
			name:  "repeated key overwrites",
			input: "i=1,i=2",
			want:  Command{ImageID: 2},
		},
		{
			name:  "max uint32",
			input: "i=4294967295",
			want:  Command{ImageID: 4294967295},
		},
		{
			name:  "min int32",
			input: "z=-2147483648",
			want:  Command{Z: -2147483648},
		},
		{name: "unknown key", input: "a=t,b=1", err: true},
		{name: "missing equals", input: "a", err: true},
		{name: "key without value", input: "a=", err: true},
		{name: "two letter key", input: "ab=1", err: true},
		{name: "bad action flag", input: "a=x", err: true},
		{name: "bad delete flag", input: "d=k", err: true},
		{name: "bad medium flag", input: "t=x", err: true},
		{name: "bad compression flag", input: "o=g", err: true},
		{name: "non numeric", input: "i=abc", err: true},
		{name: "too large", input: "i=4294967296", err: true},
		{name: "eleven digits", input: "i=12345678901", err: true},
		{name: "negative unsigned", input: "i=-1", err: true},
		{name: "trailing comma", input: "a=t,", err: true},
		{name: "junk after value", input: "a=t x", err: true},
		{name: "bad base64", input: "a=t;!!!!", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.input))
			if tt.err {
				require.ErrorIs(t, err, ErrMalformed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestReadMedium pins which commands ReadMedium reads and that Handle
// loads what was read. A read happens only once the checks kitty makes
// before its own have passed (kitty graphics.c handle_add_command,
// load_image_data), so a rejected transmission leaves its file alone.
func TestReadMedium(t *testing.T) {
	rgb := []byte{1, 2, 3}
	tests := []struct {
		name string
		// setup runs first, with its own direct payload.
		setup    string
		ctl      string
		payload  string
		readErr  error
		wantRead string
		want     string
		// loaded says image 1 holds the pixels read or sent.
		loaded bool
	}{
		{
			name:     "file",
			ctl:      "a=t,i=1,s=1,v=1,f=24,t=f",
			payload:  "/img",
			wantRead: "f /img 0 0",
			want:     "\x1b_Gi=1;OK\x1b\\",
			loaded:   true,
		},
		{
			name:     "temp file range",
			ctl:      "a=T,i=1,s=1,v=1,f=24,t=t,O=4,S=3",
			payload:  "/tmp/img",
			wantRead: "t /tmp/img 4 3",
			want:     "\x1b_Gi=1;OK\x1b\\",
			loaded:   true,
		},
		{
			name:     "shared memory query",
			ctl:      "a=q,i=1,s=1,v=1,f=24,t=s",
			payload:  "shm",
			wantRead: "s shm 0 0",
			want:     "\x1b_Gi=1;OK\x1b\\",
		},
		{
			name:     "frame",
			setup:    "a=t,i=1,s=1,v=1,f=24",
			ctl:      "a=f,i=1,s=1,v=1,f=24,t=f",
			payload:  "/frame",
			wantRead: "f /frame 0 0",
			want:     "\x1b_Gi=1,r=2;OK\x1b\\",
		},
		{
			name:     "failed read",
			ctl:      "a=t,i=1,s=1,v=1,f=24,t=f",
			payload:  "/img",
			readErr:  errors.New("unreadable"),
			wantRead: "f /img 0 0",
			want:     "\x1b_Gi=1;EBADF:Failed to read image file\x1b\\",
		},
		{
			name:    "direct",
			ctl:     "a=t,i=1,s=1,v=1,f=24",
			payload: string(rgb),
			want:    "\x1b_Gi=1;OK\x1b\\",
			loaded:  true,
		},
		{
			name:    "query without id",
			ctl:     "a=q,s=1,v=1,f=24,t=f",
			payload: "/img",
		},
		{
			name:    "both id and number",
			ctl:     "a=t,i=1,I=2,s=1,v=1,f=24,t=f",
			payload: "/img",
			want:    "\x1b_Gi=1,I=2;EINVAL:Must not specify both image id and image number\x1b\\",
		},
		{
			name:    "unknown format",
			ctl:     "a=t,i=1,s=1,v=1,f=8,t=f",
			payload: "/img",
			want:    "\x1b_Gi=1;EINVAL:Unknown image format: 8\x1b\\",
		},
		{
			name:    "too large",
			ctl:     "a=t,i=1,s=10001,v=1,t=t",
			payload: "/tmp/img",
			want:    "\x1b_Gi=1;EINVAL:Image too large, width or height greater than 10000\x1b\\",
		},
		{
			name:    "name too long",
			ctl:     "a=t,i=1,s=1,v=1,f=24,t=f",
			payload: "/" + strings.Repeat("x", MaxPathLength),
			want:    "\x1b_Gi=1;EINVAL:Filename too long\x1b\\",
		},
		{
			name:    "delete",
			ctl:     "a=d,t=f",
			payload: "/img",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStorage()
			if tt.setup != "" {
				require.Equal(t, "\x1b_Gi=1;OK\x1b\\", exec(t, s, nil, tt.setup, rgb))
			}
			payload := base64.StdEncoding.EncodeToString([]byte(tt.payload))
			cmd, err := Parse([]byte(tt.ctl + ";" + payload))
			require.NoError(t, err)
			var reads []string
			cmd.ReadMedium(func(medium byte, path string, offset, size int64) ([]byte, error) {
				reads = append(reads, fmt.Sprintf("%c %s %d %d", medium, path, offset, size))
				return rgb, tt.readErr
			})
			if tt.wantRead == "" {
				assert.Empty(t, reads)
			} else {
				assert.Equal(t, []string{tt.wantRead}, reads)
			}

			resp, _ := s.Handle(cmd, &Cursor{}, CellSize{Width: 10, Height: 20})
			assert.Equal(t, tt.want, string(resp))
			if tt.loaded {
				assert.Equal(t, []byte{1, 2, 3, 255}, s.byClientID(1).pix.Pix)
			}
		})
	}
}
