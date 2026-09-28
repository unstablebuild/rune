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

package debug_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// debugLDFlag matches `-X <importpath ending in debug>.<Name>=` in build
// scripts. The linker silently ignores -X for a symbol it cannot resolve, so a
// typo in the import path produces a binary that reports the source-level
// defaults ("development (HEAD is HEAD)") instead of failing the build.
var debugLDFlag = regexp.MustCompile(`-X (unstable\.build/rune[A-Za-z0-9_./-]*debug)\.([A-Za-z0-9_]+)=`)

func TestBuildScriptsReferenceDebugPackage(t *testing.T) {
	const wantPkg = "unstable.build/rune/internal/debug"

	knownVars := map[string]bool{
		"Tag":        true,
		"Package":    true,
		"Commit":     true,
		"ReportsDir": true,
		"DebugBuild": true,
		"BuildDate":  true,
		"OSPackaged": true,
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "target", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !isBuildScript(d.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, m := range debugLDFlag.FindAllStringSubmatch(string(content), -1) {
			if m[1] != wantPkg {
				t.Errorf("%s: -X references %q, want %q", rel, m[1], wantPkg)
			}
			if !knownVars[m[2]] {
				t.Errorf("%s: -X sets unknown variable %s.%s", rel, m[1], m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isBuildScript(name string) bool {
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	switch {
	case name == "Makefile" || name == "PKGBUILD" || name == "rules":
		return true
	case strings.HasPrefix(name, "Dockerfile"):
		return true
	case strings.HasSuffix(name, ".sh") || strings.HasSuffix(name, ".mk"),
		strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"):
		return true
	}
	return false
}
