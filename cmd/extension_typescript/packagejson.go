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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// maxManifestBytes bounds how much of a package.json is read, so a
// corrupt or generated manifest cannot exhaust memory.
const maxManifestBytes = 4 << 20

// packageJSON is the part of a package.json manifest the extension reads.
type packageJSON struct {
	Version         string            `json:"version"`
	PackageManager  string            `json:"packageManager"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// readPackageJSON parses dir/package.json. A missing manifest yields an
// error wrapping os.ErrNotExist.
func readPackageJSON(fs workspaceapi.FileSystem, dir string) (packageJSON, error) {
	path := filepath.Join(dir, "package.json")
	f, err := fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return packageJSON{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes))
	if err != nil {
		return packageJSON{}, fmt.Errorf("read %s: %w", path, err)
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return pkg, nil
}

// hasDependencies reports whether installing the package would create
// a node_modules.
func (p packageJSON) hasDependencies() bool {
	return len(p.Dependencies)+len(p.DevDependencies) > 0
}

// scriptNames returns the package's script names in sorted order.
func (p packageJSON) scriptNames() []string {
	names := make([]string, 0, len(p.Scripts))
	for name := range p.Scripts {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// projectTypeScript finds the typescript package node resolves from dir,
// walking up to the filesystem root, and returns its version and
// directory.
func projectTypeScript(fs workspaceapi.FileSystem, dir string) (version, at string, ok bool) {
	for d := dir; ; d = filepath.Dir(d) {
		at := filepath.Join(d, "node_modules", "typescript")
		if pkg, err := readPackageJSON(fs, at); err == nil && pkg.Version != "" {
			return pkg.Version, at, true
		}
		if d == filepath.Dir(d) {
			return "", "", false
		}
	}
}

func isFile(fs workspaceapi.FileSystem, path string) bool {
	info, err := fs.Stat(path)
	return err == nil && !info.IsDir()
}

var packageManagers = []string{"npm", "pnpm", "yarn", "bun"}

// lockfiles maps each lockfile to the package manager that writes it,
// in the order they are probed.
var lockfiles = []struct{ name, manager string }{
	{"bun.lock", "bun"},
	{"bun.lockb", "bun"},
	{"pnpm-lock.yaml", "pnpm"},
	{"yarn.lock", "yarn"},
	{"package-lock.json", "npm"},
	{"npm-shrinkwrap.json", "npm"},
}

// detectPackageManager returns the package manager of the package in
// dir, whose manifest is pkg: the one corepack's packageManager field
// pins, else the one whose lockfile is there. A workspace package has
// neither, as its workspace root keeps both, so the ancestors are looked
// at in turn; with neither anywhere, it is npm.
func detectPackageManager(fs workspaceapi.FileSystem, dir string, pkg packageJSON) string {
	for d := dir; ; d = filepath.Dir(d) {
		if d != dir {
			pkg, _ = readPackageJSON(fs, d)
		}
		if name, _, _ := strings.Cut(pkg.PackageManager, "@"); slices.Contains(packageManagers, name) {
			return name
		}
		for _, l := range lockfiles {
			if isFile(fs, filepath.Join(d, l.name)) {
				return l.manager
			}
		}
		if d == filepath.Dir(d) {
			return "npm"
		}
	}
}

// scriptArgs returns the arguments that make manager run script with
// args. npm alone reads flags after the script name as its own unless
// they follow a `--`.
func scriptArgs(manager, script string, args []string) []string {
	argv := []string{"run", script}
	if manager == "npm" && len(args) > 0 {
		argv = append(argv, "--")
	}
	return append(argv, args...)
}
