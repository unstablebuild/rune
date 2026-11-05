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

// Package fileexplorertest seeds and snapshots real directory trees for
// tests that check what the file explorer did on disk.
package fileexplorertest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteTree creates tree under root in the format ReadTree returns: each
// key is a slash-separated path mapped to the file's content, and a key
// ending in "/" is an empty directory. Parent directories are created as
// needed.
func WriteTree(t testing.TB, root string, tree map[string]string) {
	t.Helper()
	for f, content := range tree {
		p := filepath.Join(root, filepath.FromSlash(f))
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ReadTree snapshots root: each file maps its slash-separated path to
// its content, and each empty directory maps its path plus a trailing
// "/" to "". Non-empty directories are implied by their entries, so a
// directory left behind empty by a broken move shows up in the snapshot.
func ReadTree(t testing.TB, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.IsDir() {
			data, err := os.ReadFile(p)
			tree[rel] = string(data)
			return err
		}
		entries, err := os.ReadDir(p)
		if len(entries) == 0 {
			tree[rel+"/"] = ""
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
