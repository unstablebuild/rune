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

package symboldb

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		name        string
		run         func(Progress)
		wantNotify  []string
		wantUpdates []string
	}{
		{
			name: "nothing to parse",
			run:  func(p Progress) { p.Done(0) },
		},
		{
			name: "the first file of each folder",
			run: func(p Progress) {
				p.Start(4)
				p.File(0, 4, "a/x.go")
				p.File(1, 4, "b/z.go")
				p.File(2, 4, "a/y.go")
				p.File(3, 4, "c.go")
				p.Done(4)
			},
			wantNotify: []string{"Indexing workspace symbols"},
			wantUpdates: []string{
				"Indexing symbols: a 0/4",
				"Indexing symbols: b 1/4",
				"Indexing symbols: . 3/4",
				"Indexed the symbols of 4 files 4/4",
			},
		},
		{
			name: "stopped",
			run: func(p Progress) {
				p.Start(1)
				p.Stop(errors.New("disk full"))
			},
			wantNotify:  []string{"Indexing workspace symbols"},
			wantUpdates: []string{"Indexing symbols stopped: disk full 1/1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &recordingNotifications{}
			tt.run(NotifyProgress(n, syncTick))
			notified, updates := n.snapshot()
			var got []string
			for _, u := range updates {
				got = append(got, fmt.Sprintf("%s %d/%d", u.message, u.progress, u.total))
			}
			assert.Equal(t, tt.wantNotify, notified)
			assert.Equal(t, tt.wantUpdates, got)
		})
	}
}

type recordingProgressWriter struct {
	samples []string
}

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.samples = append(w.samples, fmt.Sprintf("%d/%d %s", progress, total, units))
}

func TestREPLProgress(t *testing.T) {
	w := &recordingProgressWriter{}
	p := REPLProgress(w)
	p.Start(2)
	p.File(0, 2, "a.go")
	p.File(1, 2, "b.go")
	p.Done(2)
	p.Stop(errors.New("ignored"))
	assert.Equal(t, []string{"0/2 files", "1/2 files", "2/2 files"}, w.samples)
}
