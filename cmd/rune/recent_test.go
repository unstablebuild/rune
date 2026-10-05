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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecentLabels(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []recentEntry
	}{
		{
			name: "distinct basenames use the directory name",
			in:   []string{"/home/me/alpha", "/home/me/beta"},
			want: []recentEntry{
				{label: "alpha", path: "/home/me/alpha"},
				{label: "beta", path: "/home/me/beta"},
			},
		},
		{
			name: "collision walks up one segment",
			in:   []string{"/home/app/web", "/home/api/web"},
			want: []recentEntry{
				{label: "app/web", path: "/home/app/web"},
				{label: "api/web", path: "/home/api/web"},
			},
		},
		{
			name: "three-way collision walks up until distinct",
			in:   []string{"/a/x/web", "/b/x/web", "/b/y/web"},
			want: []recentEntry{
				{label: "a/x/web", path: "/a/x/web"},
				{label: "b/x/web", path: "/b/x/web"},
				{label: "y/web", path: "/b/y/web"},
			},
		},
		{
			name: "exact duplicates are collapsed keeping the first",
			in:   []string{"/home/me/proj", "/home/me/proj"},
			want: []recentEntry{
				{label: "proj", path: "/home/me/proj"},
			},
		},
		{
			name: "file uri and trailing slash normalize to the same path",
			in:   []string{"file:///home/me/proj/", "/home/me/proj"},
			want: []recentEntry{
				{label: "proj", path: "/home/me/proj"},
			},
		},
		{
			name: "blank entries are dropped",
			in:   []string{"", "  ", "/home/me/proj"},
			want: []recentEntry{
				{label: "proj", path: "/home/me/proj"},
			},
		},
		{
			name: "remote uris are left intact",
			in:   []string{"ssh://host/srv/proj"},
			want: []recentEntry{
				{label: "ssh://host/srv/proj", path: "ssh://host/srv/proj"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recentLabels(tt.in))
		})
	}
}

func TestRecentLabelsEmpty(t *testing.T) {
	assert.Empty(t, recentLabels(nil))
}

func TestNormalizeWorkspacePath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/home/me/proj", "/home/me/proj"},
		{"/home/me/proj/", "/home/me/proj"},
		{"file:///home/me/proj", "/home/me/proj"},
		{"  /home/me/proj  ", "/home/me/proj"},
		{"ssh://host/srv", "ssh://host/srv"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeWorkspacePath(tt.in))
		})
	}
}

func TestRecentWorkspacesRoundTrip(t *testing.T) {
	storage := newRuneStorage(t.TempDir())
	t.Cleanup(func() { _ = storage.Close() })
	r := newRecentWorkspaces(storage)

	r.record("/a")
	r.record("/b")
	r.record("/a") // re-opening moves it back to the front
	r.record("")   // ignored

	require.Equal(t, []string{"/a", "/b"}, r.paths())
}

func TestRecentWorkspacesEmpty(t *testing.T) {
	storage := newRuneStorage(t.TempDir())
	t.Cleanup(func() { _ = storage.Close() })
	assert.Nil(t, newRecentWorkspaces(storage).paths())
}
