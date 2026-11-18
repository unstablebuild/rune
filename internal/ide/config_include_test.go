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

package ide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfigFiles writes files, keyed by path relative to dir, and returns
// dir. A key ending in "/" makes a directory; a value starting with "-> "
// makes a symlink to the rest of it, relative to dir.
func writeConfigFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, "/") {
			require.NoError(t, os.MkdirAll(path, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		if target, ok := strings.CutPrefix(content, "-> "); ok {
			require.NoError(t, os.Symlink(filepath.Join(dir, target), path))
			continue
		}
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return dir
}

// loadIncludes layers the config file at configPath onto base with dataDir
// and returns the tree, the files read and the non-fatal problems.
func loadIncludes(
	t *testing.T, configPath, dataDir string, base map[string]any,
) (map[string]any, []string, map[string]error, error) {
	t.Helper()
	c := ideConfig{cfg: base, errors: map[string]error{}}
	err := loadFileConfig(&c, configPath, dataDir)
	return c.cfg, c.includedFiles, c.errors, err
}

func TestIncludeLoader(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		// root is the config file, relative to the temp dir; config.yaml
		// when empty.
		root string
		base map[string]any
		// home, when set, is the file $HOME names, relative to the temp dir.
		home string
		// dataDir is relative to the temp dir; the root's directory when
		// empty.
		dataDir   string
		want      map[string]any
		wantFiles []string
		wantWarn  []string
		wantErr   string
	}{
		{
			name: "included files layer in order under the including file",
			files: map[string]string{
				"config.yaml": "include:\n  - a.yaml\n  - b.yaml\nown: config\nshared: config\n",
				"a.yaml":      "a: 1\nshared: a\nab: a\n",
				"b.yaml":      "b: 2\nab: b\n",
			},
			base: map[string]any{"base": true, "shared": "base"},
			want: map[string]any{
				"base": true, "a": 1, "b": 2, "ab": "b", "own": "config", "shared": "config",
			},
			wantFiles: []string{"config.yaml", "a.yaml", "b.yaml"},
		},
		{
			name: "nested includes resolve relative to the including file",
			files: map[string]string{
				"config.yaml":     "include: [sub/a.yaml]\n",
				"sub/a.yaml":      "include:\n  - deep/b.yaml\na: 1\n",
				"sub/deep/b.yaml": "b: 2\n",
				"deep/b.yaml":     "wrong: true\n",
			},
			want:      map[string]any{"a": 1, "b": 2},
			wantFiles: []string{"config.yaml", "sub/a.yaml", "sub/deep/b.yaml"},
		},
		{
			name: "RUNE_DATADIR and ~ expand in include paths",
			files: map[string]string{
				"config.yaml":     "include:\n  - $RUNE_DATADIR/etc/x.yaml\n  - ~/home.yaml\n",
				"data/etc/x.yaml": "x: 1\n",
				"home/home.yaml":  "home: 1\n",
			},
			home:      "home",
			dataDir:   "data",
			want:      map[string]any{"x": 1, "home": 1},
			wantFiles: []string{"config.yaml", "data/etc/x.yaml", "home/home.yaml"},
		},
		{
			name: "include cycle is an error naming the chain",
			files: map[string]string{
				"config.yaml": "include: [a.yaml]\n",
				"a.yaml":      "include: [b.yaml]\n",
				"b.yaml":      "include: [a.yaml]\n",
			},
			wantErr: "include cycle: ",
		},
		{
			name:    "includes nest no deeper than the cap",
			files:   chainedIncludes(maxIncludeDepth + 1),
			wantErr: "nest deeper than 16 files",
		},
		{
			name: "a missing included file is a warning and the rest loads",
			files: map[string]string{
				"config.yaml": "include:\n  - gone.yaml\n  - here.yaml\nown: 1\n",
				"here.yaml":   "here: 1\n",
			},
			want:      map[string]any{"here": 1, "own": 1},
			wantFiles: []string{"config.yaml", "gone.yaml", "here.yaml"},
			wantWarn:  []string{"include gone.yaml"},
		},
		{
			name: "include must be a list",
			files: map[string]string{
				"config.yaml": "include: a.yaml\n",
			},
			wantErr: "include must be a list of file paths, got string",
		},
		{
			name: "include entries must be non-empty strings",
			files: map[string]string{
				"config.yaml": "include: [1]\n",
			},
			wantErr: "include entries must be non-empty file paths",
		},
		{
			name: "a null include lists no files",
			files: map[string]string{
				"config.yaml": "include:\nown: 1\n",
			},
			want:      map[string]any{"own": 1},
			wantFiles: []string{"config.yaml"},
		},
		{
			name: "an empty file is an empty layer",
			files: map[string]string{
				"config.yaml":  "include: [empty.yaml, comment.yaml]\n",
				"empty.yaml":   "",
				"comment.yaml": "# nothing here\n",
			},
			base:      map[string]any{"base": 1},
			want:      map[string]any{"base": 1},
			wantFiles: []string{"config.yaml", "empty.yaml", "comment.yaml"},
		},
		{
			name: "a decode error in an included file names the chain",
			files: map[string]string{
				"config.yaml": "include: [bad.yaml]\n",
				"bad.yaml":    "a: [\n",
			},
			wantErr: `include "bad.yaml" (from `,
		},
		{
			name: "starlark include layers at the call and later statements win",
			root: "config.star",
			files: map[string]string{
				"config.star": "config[\"before\"] = \"star\"\ninclude(\"a.yaml\")\n" +
					"config[\"after\"] = config[\"a\"] + 1\nconfig[\"shared\"] = \"star\"\n",
				"a.yaml": "a: 1\nshared: a\n",
			},
			base: map[string]any{"base": true},
			want: map[string]any{
				"base": true, "before": "star", "a": 1, "after": 2, "shared": "star",
			},
			wantFiles: []string{"config.star", "a.yaml"},
		},
		{
			name: "starlark rebind after include keeps the included keys",
			root: "config.star",
			files: map[string]string{
				"config.star": "include(\"a.star\")\nconfig = {\"own\": 1}\n",
				"a.star":      "config[\"a\"] = 1\n",
			},
			base:      map[string]any{},
			want:      map[string]any{"a": 1, "own": 1},
			wantFiles: []string{"config.star", "a.star"},
		},
		{
			name: "starlark includes a yaml file that includes starlark",
			root: "config.star",
			files: map[string]string{
				"config.star": "include(\"a.yaml\")\n",
				"a.yaml":      "include: [b.star]\na: 1\n",
				"b.star":      "config[\"b\"] = 2\n",
			},
			base:      map[string]any{},
			want:      map[string]any{"a": 1, "b": 2},
			wantFiles: []string{"config.star", "a.yaml", "b.star"},
		},
		{
			name: "starlark include of a missing file warns",
			root: "config.star",
			files: map[string]string{
				"config.star": "include(\"gone.star\")\nconfig[\"own\"] = 1\n",
			},
			base:      map[string]any{},
			want:      map[string]any{"own": 1},
			wantFiles: []string{"config.star", "gone.star"},
			wantWarn:  []string{"include gone.star"},
		},
		{
			name: "gui.env chains across layers",
			files: map[string]string{
				"config.yaml": "include: [go.yaml, rust.yaml]\ngui:\n  env:\n    PATH: /opt/me:$PATH\n    GOFLAGS: -v $GOFLAGS\n",
				"go.yaml":     "gui:\n  env:\n    PATH: $RUNE_DATADIR/lib/go/bin:$PATH\n    GOFLAGS: -mod=mod\n    GOROOT: $RUNE_DATADIR/lib/go\n",
				"rust.yaml":   "gui:\n  env:\n    PATH: $RUNE_DATADIR/lib/rust/bin:${PATH}\n",
			},
			base: map[string]any{"gui": map[string]any{"env": map[string]any{}}},
			want: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH":    "/opt/me:$RUNE_DATADIR/lib/rust/bin:$RUNE_DATADIR/lib/go/bin:$PATH",
				"GOFLAGS": "-v -mod=mod",
				"GOROOT":  "$RUNE_DATADIR/lib/go",
			}}},
			wantFiles: []string{"config.yaml", "go.yaml", "rust.yaml"},
		},
		{
			name: "gui.env does not chain a value equal to the previous one",
			files: map[string]string{
				"config.yaml": "include: [go.yaml, go.yaml]\n",
				"go.yaml":     "gui:\n  env:\n    PATH: $RUNE_DATADIR/lib/go/bin:$PATH\n",
			},
			base: map[string]any{"gui": map[string]any{"env": map[string]any{}}},
			want: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "$RUNE_DATADIR/lib/go/bin:$PATH",
			}}},
			wantFiles: []string{"config.yaml", "go.yaml", "go.yaml"},
		},
		{
			name: "a package config under etc gets its package globals and expansions",
			files: map[string]string{
				"config.yaml": "include: [$RUNE_DATADIR/etc/tools/mypkg.star]\n",
				"data/etc/tools/mypkg.star": `
config["pkg"] = {
    "id": RUNE_PKG_ID,
    "version": RUNE_PKG_VERSION,
    "mode": RUNE_EDITOR_MODE,
    "datadir": RUNE_DATADIR,
    "path": "$RUNE_DATADIR/lib/$RUNE_PKG_ID/$RUNE_PKG_VERSION/x",
    "list": ["$RUNE_PKG_ID", 1],
}
config["requirements"] = ["go"]
`,
				"data/lib/tools/mypkg": "-> data/pkg/tools/mypkg/1.2.3",
			},
			dataDir: "data",
			base:    map[string]any{"editor": map[string]any{"mode": "helix"}},
			want: map[string]any{
				"editor": map[string]any{"mode": "helix"},
				"pkg": map[string]any{
					"id":      "tools/mypkg",
					"version": "1.2.3",
					"mode":    "helix",
					"datadir": "$RUNE_DATADIR",
					"path":    "$RUNE_DATADIR/lib/tools/mypkg/1.2.3/x",
					"list":    []any{"tools/mypkg", 1},
				},
			},
			wantFiles: []string{"config.yaml", "data/etc/tools/mypkg.star"},
		},
		{
			name: "a package yaml under etc drops requirements and expands its variables",
			files: map[string]string{
				"config.yaml":         "include: [$RUNE_DATADIR/etc/mypkg.yaml]\n",
				"data/etc/mypkg.yaml": "requirements: [go]\nextensions:\n  mypkg:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n    version: $RUNE_PKG_VERSION\n",
			},
			dataDir: "data",
			want: map[string]any{
				"extensions": map[string]any{"mypkg": map[string]any{
					"path": "$RUNE_DATADIR/lib/mypkg",
					// No version is in use, so the reference stays literal.
					"version": "$RUNE_PKG_VERSION",
				}},
			},
			wantFiles: []string{"config.yaml", "data/etc/mypkg.yaml"},
		},
		{
			name: "RUNE_EDITOR_MODE comes from the including file's editor.mode",
			files: map[string]string{
				"config.yaml":         "include: [$RUNE_DATADIR/etc/mypkg.star]\neditor:\n  mode: emacs\n",
				"data/etc/mypkg.star": "config[\"seen\"] = RUNE_EDITOR_MODE\n",
			},
			dataDir: "data",
			base:    map[string]any{"editor": map[string]any{"mode": "vim"}},
			want: map[string]any{
				"editor": map[string]any{"mode": "emacs"},
				"seen":   "emacs",
			},
			wantFiles: []string{"config.yaml", "data/etc/mypkg.star"},
		},
		{
			name: "RUNE_EDITOR_MODE from starlark is the mode at the include call",
			root: "config.star",
			files: map[string]string{
				"config.star":         "config[\"editor\"][\"mode\"] = \"standard\"\ninclude(\"$RUNE_DATADIR/etc/mypkg.star\")\n",
				"data/etc/mypkg.star": "config[\"seen\"] = RUNE_EDITOR_MODE\n",
			},
			dataDir: "data",
			base:    map[string]any{"editor": map[string]any{"mode": "vim"}},
			want: map[string]any{
				"editor": map[string]any{"mode": "standard"},
				"seen":   "standard",
			},
			wantFiles: []string{"config.star", "data/etc/mypkg.star"},
		},
		{
			name: "a file outside etc is not a package config",
			files: map[string]string{
				"config.yaml": "include: [other.yaml]\n",
				"other.yaml":  "requirements: [kept]\np: $RUNE_PKG_ID\n",
			},
			want: map[string]any{
				"requirements": []any{"kept"},
				"p":            "$RUNE_PKG_ID",
			},
			wantFiles: []string{"config.yaml", "other.yaml"},
		},
		{
			name:      "a missing root config leaves the base and is listed",
			files:     map[string]string{},
			base:      map[string]any{"base": 1},
			want:      map[string]any{"base": 1},
			wantFiles: []string{"config.yaml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeConfigFiles(t, t.TempDir(), tt.files)
			dir, err := filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			if tt.home != "" {
				t.Setenv("HOME", filepath.Join(dir, tt.home))
			}
			root := tt.root
			if root == "" {
				root = "config.yaml"
			}
			dataDir := ""
			if tt.dataDir != "" {
				dataDir = filepath.Join(dir, tt.dataDir)
			}
			base := tt.base
			if base == nil {
				base = map[string]any{}
			}
			got, files, warns, err := loadIncludes(t, filepath.Join(dir, root), dataDir, base)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			var wantFiles []string
			for _, f := range tt.wantFiles {
				wantFiles = append(wantFiles, filepath.Join(dir, f))
			}
			assert.Equal(t, wantFiles, files)
			var gotWarn []string
			for k := range warns {
				gotWarn = append(gotWarn, k)
			}
			assert.ElementsMatch(t, tt.wantWarn, gotWarn)
		})
	}
}

// chainedIncludes returns n files, config.yaml including f1.yaml including
// f2.yaml and so on.
func chainedIncludes(n int) map[string]string {
	files := map[string]string{}
	name := "config.yaml"
	for i := 1; i <= n; i++ {
		next := "f" + strings.Repeat("x", i) + ".yaml"
		files[name] = "include: [" + next + "]\n"
		name = next
	}
	files[name] = "end: true\n"
	return files
}

func TestIncludeCycleNamesTheChain(t *testing.T) {
	dir := writeConfigFiles(t, t.TempDir(), map[string]string{
		"config.yaml": "include: [a.yaml]\n",
		"a.yaml":      "include: [b.yaml]\n",
		"b.yaml":      "include: [a.yaml]\n",
	})
	dir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	_, _, _, err = loadIncludes(t, filepath.Join(dir, "config.yaml"), "", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(dir, "config.yaml")+" -> "+
		filepath.Join(dir, "a.yaml")+" -> "+filepath.Join(dir, "b.yaml")+" -> "+
		filepath.Join(dir, "a.yaml"))
}

func TestWorkspaceConfigRejectsInclude(t *testing.T) {
	t.Run("yaml", func(t *testing.T) {
		_, err := decodeOverlayConfigFile(strings.NewReader("include: [a.yaml]\n"),
			".rune/config.yaml", map[string]any{})
		require.Error(t, err)
		assert.EqualError(t, err, "include is not allowed in .rune/config.yaml")
	})
	t.Run("starlark", func(t *testing.T) {
		_, err := decodeOverlayConfigFile(strings.NewReader("include(\"a.star\")\n"),
			".rune/config.star", map[string]any{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "undefined: include")
	})
}

func TestOverlayLayerChainsGUIEnv(t *testing.T) {
	tests := []struct {
		name  string
		dst   map[string]any
		layer map[string]any
		want  map[string]any
	}{
		{
			name: "a reference to the variable itself is the previous value",
			dst: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "a:$PATH", "X": "1",
			}}},
			layer: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "b:$PATH", "X": "${X}2", "Y": "$Y",
			}}},
			want: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "b:a:$PATH", "X": "12", "Y": "$Y",
			}}},
		},
		{
			name: "other references and non-strings are left alone",
			dst: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "a:$PATH", "N": 1,
			}}},
			layer: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "$HOME/bin:$RUNE_DATADIR/bin", "N": 2,
			}}},
			want: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "$HOME/bin:$RUNE_DATADIR/bin", "N": 2,
			}}},
		},
		{
			name:  "no gui.env in dst",
			dst:   map[string]any{"gui": "not a map"},
			layer: map[string]any{"gui": map[string]any{"env": map[string]any{"PATH": "b:$PATH"}}},
			want:  map[string]any{"gui": map[string]any{"env": map[string]any{"PATH": "b:$PATH"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlayLayer(tt.dst, tt.layer)
			assert.Equal(t, tt.want, tt.dst)
		})
	}
}
