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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadPackageJSON(t *testing.T) {
	fs := newFakeFS("/ws").
		writeFile("package.json", `{
			"version": "1.2.3",
			"packageManager": "pnpm@9.1.0",
			"scripts": {"test": "vitest", "build": "tsc -b"},
			"devDependencies": {"vitest": "^3.0.0"}
		}`).
		writeFile("broken/package.json", `{"scripts": `)

	pkg, err := readPackageJSON(fs, "/ws")
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", pkg.Version)
	assert.Equal(t, "pnpm@9.1.0", pkg.PackageManager)
	assert.Equal(t, []string{"build", "test"}, pkg.scriptNames())
	assert.True(t, pkg.hasDependencies())

	_, err = readPackageJSON(fs, "/ws/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)

	_, err = readPackageJSON(fs, "/ws/broken")
	assert.ErrorContains(t, err, "parse /ws/broken/package.json")
}

func TestDetectPackageManager(t *testing.T) {
	tests := []struct {
		name     string
		pinned   string
		lockfile string
		want     string
	}{
		{"no lockfile defaults to npm", "", "", "npm"},
		{"package-lock.json", "", "package-lock.json", "npm"},
		{"npm-shrinkwrap.json", "", "npm-shrinkwrap.json", "npm"},
		{"pnpm-lock.yaml", "", "pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "", "yarn.lock", "yarn"},
		{"bun.lock", "", "bun.lock", "bun"},
		{"bun.lockb", "", "bun.lockb", "bun"},
		{"packageManager wins over the lockfile", "yarn@4.5.0", "package-lock.json", "yarn"},
		{"unknown packageManager falls back to the lockfile", "deno@2.0.0", "pnpm-lock.yaml", "pnpm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeFS("/ws")
			if tt.lockfile != "" {
				fs.addFile(tt.lockfile)
			}
			got := detectPackageManager(fs, "/ws", packageJSON{PackageManager: tt.pinned})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectPackageManagerOfWorkspacePackage(t *testing.T) {
	tests := []struct {
		name string
		fs   func() *fakeFS
		want string
	}{
		{
			name: "workspace lockfile",
			fs:   func() *fakeFS { return newFakeFS("/ws").addFile("pnpm-lock.yaml") },
			want: "pnpm",
		},
		{
			name: "workspace corepack pin",
			fs:   func() *fakeFS { return newFakeFS("/ws").writeFile("package.json", `{"packageManager": "bun@1.4.2"}`) },
			want: "bun",
		},
		{
			name: "the package's own lockfile wins",
			fs:   func() *fakeFS { return newFakeFS("/ws").addFile("pnpm-lock.yaml").addFile("packages/b/yarn.lock") },
			want: "yarn",
		},
		{
			name: "the nearest ancestor wins",
			fs: func() *fakeFS {
				return newFakeFS("/repo/ws").addFile("/repo/bun.lock").addFile("/repo/ws/packages/package-lock.json")
			},
			want: "npm",
		},
		{
			name: "a lockfile above the workspace",
			fs:   func() *fakeFS { return newFakeFS("/repo/ws").addFile("/repo/bun.lock") },
			want: "bun",
		},
		{
			name: "neither anywhere",
			fs:   func() *fakeFS { return newFakeFS("/ws") },
			want: "npm",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := tt.fs()
			dir := fs.root + "/packages/b"
			fs.writeFile(dir+"/package.json", `{"name": "b"}`)
			assert.Equal(t, tt.want, detectPackageManager(fs, dir, packageJSON{}))
		})
	}
}

func TestScriptArgs(t *testing.T) {
	tests := []struct {
		manager string
		args    []string
		want    []string
	}{
		{"npm", nil, []string{"run", "test"}},
		{"npm", []string{"--watch"}, []string{"run", "test", "--", "--watch"}},
		{"pnpm", []string{"--watch"}, []string{"run", "test", "--watch"}},
		{"yarn", []string{"--watch"}, []string{"run", "test", "--watch"}},
		{"bun", []string{"--watch"}, []string{"run", "test", "--watch"}},
	}
	for _, tt := range tests {
		t.Run(tt.manager, func(t *testing.T) {
			assert.Equal(t, tt.want, scriptArgs(tt.manager, "test", tt.args))
		})
	}
}
