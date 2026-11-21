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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func wsURI(t *testing.T, dir string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	return uri
}

func TestCompdbSetting(t *testing.T) {
	ctx := context.Background()
	t.Run("round trip", func(t *testing.T) {
		s := newCompdbSetting(storagestub.NewInMemoryService(), wsURI(t, "/ws"))
		_, ok, err := s.get(ctx)
		require.NoError(t, err)
		assert.False(t, ok)

		require.NoError(t, s.set(ctx, "/ws/build"))
		dir, ok, err := s.get(ctx)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "/ws/build", dir)

		require.NoError(t, s.clear(ctx))
		_, ok, err = s.get(ctx)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("workspaces do not share an override", func(t *testing.T) {
		store := storagestub.NewInMemoryService()
		a := newCompdbSetting(store, wsURI(t, "/a"))
		b := newCompdbSetting(store, wsURI(t, "/b"))
		require.NoError(t, a.set(ctx, "/a/build"))
		_, ok, err := b.get(ctx)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("unavailable storage reads empty and refuses to write", func(t *testing.T) {
		s := newCompdbSetting(nil, wsURI(t, "/ws"))
		_, ok, err := s.get(ctx)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Error(t, s.set(ctx, "/ws/build"))
		assert.NoError(t, s.clear(ctx))
	})
}

func TestCompdbEffectiveDir(t *testing.T) {
	ctx := context.Background()
	t.Run("override wins over config", func(t *testing.T) {
		s := newCompdbSetting(storagestub.NewInMemoryService(), wsURI(t, "/ws"))
		require.NoError(t, s.set(ctx, "/ws/out/Debug"))
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "build"})
		dir, err := s.effectiveDir(ctx, cfg, newFakeNotifications())
		require.NoError(t, err)
		assert.Equal(t, "/ws/out/Debug", dir)
	})
	t.Run("relative config resolves against the workspace", func(t *testing.T) {
		s := newCompdbSetting(storagestub.NewInMemoryService(), wsURI(t, "/ws"))
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "build"})
		dir, err := s.effectiveDir(ctx, cfg, newFakeNotifications())
		require.NoError(t, err)
		assert.Equal(t, "/ws/build", dir)
	})
	t.Run("absolute config is kept", func(t *testing.T) {
		s := newCompdbSetting(nil, wsURI(t, "/ws"))
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "/tmp/cc"})
		dir, err := s.effectiveDir(ctx, cfg, newFakeNotifications())
		require.NoError(t, err)
		assert.Equal(t, "/tmp/cc", dir)
	})
	t.Run("nothing set leaves clangd to its own lookup", func(t *testing.T) {
		s := newCompdbSetting(nil, wsURI(t, "/ws"))
		dir, err := s.effectiveDir(ctx, nil, newFakeNotifications())
		require.NoError(t, err)
		assert.Empty(t, dir)
	})
}

func TestCompdbInEffect(t *testing.T) {
	cases := []struct {
		name       string
		files      []string
		override   string
		configured string
		file       string
		want       compdbSource
	}{
		{
			name:  "override first",
			files: []string{"/ws/compile_commands.json"}, override: "/ws/out",
			file: "/ws/src/a.c", want: compdbSource{Kind: "override", Path: "/ws/out"},
		},
		{
			name:  "config second",
			files: []string{"/ws/compile_commands.json"}, configured: "/ws/build",
			file: "/ws/src/a.c", want: compdbSource{Kind: "config", Path: "/ws/build"},
		},
		{
			name:  "nearest compile_commands.json up the tree",
			files: []string{"/ws/compile_commands.json", "/ws/lib/compile_commands.json"},
			file:  "/ws/lib/src/a.c", want: compdbSource{Kind: "compile_commands.json", Path: "/ws/lib"},
		},
		{
			name:  "build sibling counts at each level, as it does for clangd",
			files: []string{"/ws/build/compile_commands.json"},
			file:  "/ws/src/deep/a.cpp", want: compdbSource{Kind: "compile_commands.json", Path: "/ws/build"},
		},
		{
			name:  "compile_commands.json beats compile_flags.txt in the same directory",
			files: []string{"/ws/compile_flags.txt", "/ws/compile_commands.json"},
			file:  "/ws/a.c", want: compdbSource{Kind: "compile_commands.json", Path: "/ws"},
		},
		{
			name:  "compile_flags.txt",
			files: []string{"/ws/compile_flags.txt"},
			file:  "/ws/src/a.c", want: compdbSource{Kind: "compile_flags.txt", Path: "/ws/compile_flags.txt"},
		},
		{
			name: "none",
			file: "/ws/a.c", want: compdbSource{Kind: "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			for _, f := range tc.files {
				fs.addFile(f)
			}
			assert.Equal(t, tc.want, compdbInEffect(fs, tc.override, tc.configured, tc.file))
		})
	}
}

func TestCompdbCandidates(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"compile_commands.json",
		"build/compile_commands.json",
		"build-arm/compile_commands.json",
		"cmake-build-debug/compile_commands.json",
		"out/Debug/compile_commands.json",
		"out/Release/compile_commands.json",
		"_build/compile_commands.json",
		"src/compile_commands.json",           // not a build directory
		"build/deep/er/compile_commands.json", // too deep
		"node_modules/compile_commands.json",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("[]"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "builddir"), 0o755))

	got := compdbCandidates(realFS{root: root}, root)
	want := []string{
		root,
		filepath.Join(root, "_build"),
		filepath.Join(root, "build"),
		filepath.Join(root, "build-arm"),
		filepath.Join(root, "cmake-build-debug"),
		filepath.Join(root, "out", "Debug"),
		filepath.Join(root, "out", "Release"),
	}
	assert.Equal(t, want, got)

	assert.Empty(t, compdbCandidates(realFS{root: root}, filepath.Join(root, "node_modules", "missing")))
}

func TestProjectDirOf(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		file  string
		want  string
	}{
		{"no marker falls back to the root", nil, "/ws/a.c", "/ws"},
		{"a file outside the workspace reports the root",
			[]string{"/ws/CMakeLists.txt"}, "/elsewhere/a.c", "/ws"},
		{"the nearest marker roots a leaf project",
			[]string{"/ws/CMakeLists.txt", "/ws/lib/meson.build"}, "/ws/app/main.c", "/ws"},
		{"a nested project of another build system is its own",
			[]string{"/ws/CMakeLists.txt", "/ws/lib/meson.build"}, "/ws/lib/src/a.c", "/ws/lib"},
		{"a recursive Makefile belongs to the autotools root",
			[]string{"/ws/configure.ac", "/ws/Makefile", "/ws/src/Makefile", "/ws/src/Makefile.in"},
			"/ws/src/logd.c", "/ws"},
		{"a vendored dependency built by the root Makefile belongs to it",
			[]string{"/ws/configure.ac", "/ws/Makefile", "/ws/deps/slab/Makefile"},
			"/ws/deps/slab/src/buf.c", "/ws"},
		{"a CMake subdirectory belongs to the top-level CMakeLists.txt",
			[]string{"/ws/CMakeLists.txt", "/ws/src/CMakeLists.txt"}, "/ws/src/a.cpp", "/ws"},
		{"a Bazel package belongs to its module",
			[]string{"/ws/MODULE.bazel", "/ws/cc/BUILD.bazel"}, "/ws/cc/a.cc", "/ws"},
		{"sibling projects in a monorepo stay apart",
			[]string{"/ws/a/CMakeLists.txt", "/ws/a/src/CMakeLists.txt", "/ws/b/CMakeLists.txt"},
			"/ws/a/src/x.cc", "/ws/a"},
		{"a .clangd below a Makefile does not cut the project short",
			[]string{"/ws/Makefile", "/ws/src/.clangd"}, "/ws/src/a.c", "/ws"},
		{"a .clangd alone roots the project",
			[]string{"/ws/tools/.clangd"}, "/ws/tools/a.c", "/ws/tools"},
		{"a workspace-wide .clangd does not merge sibling projects",
			[]string{"/ws/.clangd", "/ws/a/CMakeLists.txt", "/ws/b/meson.build"}, "/ws/a/x.cc", "/ws/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeFS()
			for _, f := range tt.files {
				fs.addFile(f)
			}
			assert.Equal(t, tt.want, projectDirOf(fs, "/ws", tt.file))
		})
	}
}

func TestMarkerNames(t *testing.T) {
	fs := newFakeFS().
		addFile("/ws/.clangd").
		addFile("/ws/compile_flags.txt").
		addFile("/ws/configure").
		addFile("/ws/configure.ac").
		addFile("/ws/Makefile.in").
		addFile("/ws/Makefile")
	assert.Equal(t, []string{"compile_flags.txt", ".clangd", "configure.ac", "Makefile"},
		markerNames(projectMarkers(fs, "/ws")),
		"one name per build system, clangd's own files each on their own")
	assert.Empty(t, markerNames(projectMarkers(fs, "/ws/src")))
}

func TestCompdbHowTo(t *testing.T) {
	tests := []struct {
		name     string
		markers  []string
		contains []string
		excludes []string
	}{
		{"bazel", []string{"MODULE.bazel", "BUILD.bazel"},
			[]string{"bazel run @hedron_compile_commands//:refresh_all"}, []string{"cmake", "bear"}},
		{"no build system", nil,
			[]string{"compile_flags.txt", "one compiler flag per line"}, []string{"cmake", "bear"}},
		{"clangd's own files are no build system", []string{".clangd"},
			[]string{"compile_flags.txt", "one compiler flag per line"}, []string{"cmake", "bear"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeFS()
			for _, m := range tt.markers {
				fs.addFile("/ws/" + m)
			}
			got := compdbHowTo(projectMarkers(fs, "/ws"))
			for _, s := range tt.contains {
				assert.Contains(t, got, s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, got, s)
			}
			assert.Contains(t, got, "ccpp restart", "every recipe says how open files pick the result up")
		})
	}
}
