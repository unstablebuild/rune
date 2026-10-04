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

package idepkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"gopkg.in/yaml.v3"
)

func TestPlanConfigChange(t *testing.T) {
	t.Parallel()

	const (
		pkgID      = "testpkg"
		dataDir    = "/data"
		editorMode = "vim"
	)

	tests := []struct {
		name          string
		pkgConfig     string
		userCfg       map[string]any
		version       release.Version
		wantPrompt    bool
		wantConflict  map[string]any
		wantAutoApply map[string]any
		wantPathID    string
		wantPathFrom  string
		wantPathTo    string
		wantErr       bool
	}{
		{
			name:       "empty overlay yields no prompt",
			pkgConfig:  "{}\n",
			userCfg:    map[string]any{},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:       "all keys present, static and equal, no prompt",
			pkgConfig:  "a: 1\nb: hello\n",
			userCfg:    map[string]any{"a": 1, "b": "hello"},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:          "new top-level key auto-applies without prompt",
			pkgConfig:     "a: 1\nc: 3\n",
			userCfg:       map[string]any{"a": 1},
			version:       "1",
			wantPrompt:    false,
			wantAutoApply: map[string]any{"c": 3},
		},
		{
			name:          "new nested sub-key auto-applies nested diff without prompt",
			pkgConfig:     "env:\n  A: 1\n  B: 2\n",
			userCfg:       map[string]any{"env": map[string]any{"A": 1}},
			version:       "1",
			wantPrompt:    false,
			wantAutoApply: map[string]any{"env": map[string]any{"B": 2}},
		},
		{
			name:       "static differing scalar present on both, no prompt (RUNE-187)",
			pkgConfig:  "a: package\n",
			userCfg:    map[string]any{"a": "user"},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:         "version-dependent scalar changed value prompts (RUNE-225)",
			pkgConfig:    "goroot: /data/pkg/testpkg/$RUNE_PKG_VERSION/go\n",
			userCfg:      map[string]any{"goroot": "/data/pkg/testpkg/1/go"},
			version:      "2",
			wantPrompt:   true,
			wantConflict: map[string]any{"goroot": "/data/pkg/testpkg/2/go"},
		},
		{
			name:       "version-dependent scalar unchanged value, no prompt",
			pkgConfig:  "goroot: /data/pkg/testpkg/$RUNE_PKG_VERSION/go\n",
			userCfg:    map[string]any{"goroot": "/data/pkg/testpkg/1/go"},
			version:    "1",
			wantPrompt: false,
		},
		{
			name: "version-dependent conflicts prompt, static preserved, new keys auto-apply",
			pkgConfig: "env:\n" +
				"  GOROOT: /data/pkg/testpkg/$RUNE_PKG_VERSION/go\n" +
				"  STATIC: package\n" +
				"  NEW: added\n",
			userCfg: map[string]any{"env": map[string]any{
				"GOROOT": "/data/pkg/testpkg/1/go",
				"STATIC": "user",
			}},
			version:    "2",
			wantPrompt: true,
			wantConflict: map[string]any{"env": map[string]any{
				"GOROOT": "/data/pkg/testpkg/2/go",
			}},
			wantAutoApply: map[string]any{"env": map[string]any{
				"NEW": "added",
			}},
		},
		{
			name:         "brace form of RUNE_PKG_VERSION detected",
			pkgConfig:    "goroot: /data/pkg/testpkg/${RUNE_PKG_VERSION}/go\n",
			userCfg:      map[string]any{"goroot": "/data/pkg/testpkg/1/go"},
			version:      "2",
			wantPrompt:   true,
			wantConflict: map[string]any{"goroot": "/data/pkg/testpkg/2/go"},
		},
		{
			name:       "non-version var differing, no prompt",
			pkgConfig:  "datadir: $RUNE_DATADIR/cache\n",
			userCfg:    map[string]any{"datadir": "/old/cache"},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "installed extension with stale path prompts",
			pkgConfig: "extensions:\n  testpkg:\n    path: $RUNE_DATADIR/bin/testpkg\n",
			userCfg: map[string]any{"extensions": map[string]any{
				"testpkg": map[string]any{"path": "/old/testpkg"},
			}},
			version:      "1",
			wantPathID:   "testpkg",
			wantPathFrom: "/old/testpkg",
			wantPathTo:   "/data/bin/testpkg",
		},
		{
			name:      "extension path using data directory variable does not prompt",
			pkgConfig: "extensions:\n  testpkg:\n    path: $RUNE_DATADIR/bin/testpkg\n",
			userCfg: map[string]any{"extensions": map[string]any{
				"testpkg": map[string]any{"path": "$RUNE_DATADIR/bin/testpkg"},
			}},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "extension path outside data directory does not prompt",
			pkgConfig: "extensions:\n  testpkg:\n    path: /usr/local/bin/testpkg\n",
			userCfg: map[string]any{"extensions": map[string]any{
				"testpkg": map[string]any{"path": "/old/testpkg"},
			}},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:       "int/bool coercions compared via fmt.Sprint, no prompt",
			pkgConfig:  "count: 5\nenabled: true\n",
			userCfg:    map[string]any{"count": int64(5), "enabled": true},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "gui.env.PATH surfaces merged value, prepending package chunk",
			pkgConfig: "gui:\n  env:\n    PATH: /pkg/bin:/shared/bin:$PATH\n",
			userCfg: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "/user/bin:/shared/bin:$PATH",
			}}},
			version:    "1",
			wantPrompt: true,
			wantConflict: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "/pkg/bin:/user/bin:/shared/bin:$PATH",
			}}},
		},
		{
			name:      "gui.env.PATH fully contained produces no prompt",
			pkgConfig: "gui:\n  env:\n    PATH: /shared/bin:$PATH\n",
			userCfg: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "/user/bin:/shared/bin:$PATH",
			}}},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "differing gui.env non-PATH scalar prompts with override",
			pkgConfig: "gui:\n  env:\n    RUSTUP_HOME: /pkg/rustup\n",
			userCfg: map[string]any{"gui": map[string]any{"env": map[string]any{
				"RUSTUP_HOME": "/user/rustup",
			}}},
			version:    "1",
			wantPrompt: true,
			wantConflict: map[string]any{"gui": map[string]any{"env": map[string]any{
				"RUSTUP_HOME": "/pkg/rustup",
			}}},
		},
		{
			name:      "equal gui.env scalar produces no prompt",
			pkgConfig: "gui:\n  env:\n    RUSTUP_HOME: /same/rustup\n",
			userCfg: map[string]any{"gui": map[string]any{"env": map[string]any{
				"RUSTUP_HOME": "/same/rustup",
			}}},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "differing non-gui.env scalar under env, no prompt (RUNE-187)",
			pkgConfig: "env:\n  RUSTUP_HOME: /pkg/rustup\n",
			userCfg: map[string]any{"env": map[string]any{
				"RUSTUP_HOME": "/user/rustup",
			}},
			version:    "1",
			wantPrompt: false,
		},
		{
			name:      "malformed yaml returns error",
			pkgConfig: "a: [unterminated\n",
			userCfg:   map[string]any{},
			version:   "1",
			wantErr:   true,
		},
		{
			name:          "requirements stripped from auto-apply",
			pkgConfig:     "requirements:\n  - python\nsettings:\n  a: 1\n",
			userCfg:       map[string]any{},
			version:       "1",
			wantPrompt:    false,
			wantAutoApply: map[string]any{"settings": map[string]any{"a": 1}},
		},
		{
			name:       "requirements-only overlay yields no plan",
			pkgConfig:  "requirements:\n  - python\n",
			userCfg:    map[string]any{},
			version:    "1",
			wantPrompt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userDoc, err := mapToYAMLDocument(tt.userCfg)
			require.NoError(t, err)
			plan, err := planConfigChange(
				"config.yaml", []byte(tt.pkgConfig), tt.userCfg, userDoc,
				pkgID, tt.version, dataDir, editorMode, true,
			)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPrompt, plan.prompt)

			if tt.wantConflict != nil {
				require.True(t, plan.prompt)
				require.NotNil(t, plan.pkgDoc)
				require.NotNil(t, plan.userDoc)
				require.NotEmpty(t, plan.missingYAML)
				assert.Equal(t, normalizeYAML(t, tt.wantConflict), docToMap(t, plan.pkgDoc))
			} else {
				assert.False(t, plan.prompt)
				assert.Nil(t, plan.pkgDoc)
			}

			if tt.wantAutoApply != nil {
				require.NotNil(t, plan.autoApplyDoc)
				require.NotNil(t, plan.userDoc)
				assert.Equal(t, normalizeYAML(t, tt.wantAutoApply), docToMap(t, plan.autoApplyDoc))
			} else {
				assert.Nil(t, plan.autoApplyDoc)
			}

			if tt.wantPathTo != "" {
				require.Len(t, plan.pathChanges, 1)
				assert.Equal(t, tt.wantPathID, plan.pathChanges[0].extensionID)
				assert.Equal(t, tt.wantPathFrom, plan.pathChanges[0].currentPath)
				assert.Equal(t, tt.wantPathTo, plan.pathChanges[0].installedPath)
				assert.Equal(t, normalizeYAML(t, map[string]any{
					"extensions": map[string]any{
						"testpkg": map[string]any{"path": tt.wantPathTo},
					},
				}), docToMap(t, plan.pathChanges[0].pkgDoc))
			} else {
				assert.Empty(t, plan.pathChanges)
			}

			if tt.wantConflict == nil && tt.wantAutoApply == nil && tt.wantPathTo == "" {
				assert.Nil(t, plan.userDoc)
			}
		})
	}
}

func TestBuildMergedConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		user     string
		pkg      string
		want     map[string]any
		wantDiff map[string]any
	}{
		{
			name:     "append new key",
			user:     "a: 1\n",
			pkg:      "b: 2\n",
			want:     map[string]any{"a": 1, "b": 2},
			wantDiff: map[string]any{"b": 2},
		},
		{
			name:     "overwrite version-dependent key",
			user:     "goroot: /old\n",
			pkg:      "goroot: /new\n",
			want:     map[string]any{"goroot": "/new"},
			wantDiff: map[string]any{"goroot": "/new"},
		},
		{
			name:     "preserve untouched user keys",
			user:     "a: 1\nkeep: yes\n",
			pkg:      "b: 2\n",
			want:     map[string]any{"a": 1, "keep": "yes", "b": 2},
			wantDiff: map[string]any{"b": 2},
		},
		{
			name:     "nested merge adds sub-key without clobbering siblings",
			user:     "env:\n  A: 1\n",
			pkg:      "env:\n  B: 2\n",
			want:     map[string]any{"env": map[string]any{"A": 1, "B": 2}},
			wantDiff: map[string]any{"env": map[string]any{"B": 2}},
		},
		{
			name: "merged gui.env.PATH overwrites single key in place",
			user: "gui:\n  env:\n    PATH: /user/bin:/shared/bin:$PATH\n",
			pkg:  "gui:\n  env:\n    PATH: /pkg/bin:/user/bin:/shared/bin:$PATH\n",
			want: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "/pkg/bin:/user/bin:/shared/bin:$PATH",
			}}},
			wantDiff: map[string]any{"gui": map[string]any{"env": map[string]any{
				"PATH": "/pkg/bin:/user/bin:/shared/bin:$PATH",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run("yaml/"+tt.name, func(t *testing.T) {
			t.Parallel()
			userDoc := mustParseYAML(t, tt.user)
			pkgDoc := mustParseYAML(t, tt.pkg)
			merged, err := buildMergedConfig(userDoc, pkgDoc, false)
			require.NoError(t, err)
			require.NotNil(t, merged.yamlDoc)
			assert.Nil(t, merged.starDiff)
			assert.Equal(t, normalizeYAML(t, tt.want), docToMap(t, merged.yamlDoc))
		})
		t.Run("star/"+tt.name, func(t *testing.T) {
			t.Parallel()
			userDoc := mustParseYAML(t, tt.user)
			pkgDoc := mustParseYAML(t, tt.pkg)
			merged, err := buildMergedConfig(userDoc, pkgDoc, true)
			require.NoError(t, err)
			require.NotNil(t, merged.starDiff)
			assert.Equal(t, normalizeYAML(t, tt.wantDiff), merged.starDiff)
		})
	}
}

func docToMap(t *testing.T, doc *yaml.Node) map[string]any {
	t.Helper()
	cfg, err := loadIdePkgConfigFromYAMLDoc(doc)
	require.NoError(t, err)
	return cfg
}

func TestMergePathValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		userPath    string
		pkgPath     string
		want        string
		wantChanged bool
	}{
		{
			name:        "new chunk prepended before user value",
			userPath:    "A:B:$PATH",
			pkgPath:     "C:B:$PATH",
			want:        "C:A:B:$PATH",
			wantChanged: true,
		},
		{
			name:        "fully contained yields no change",
			userPath:    "A:B:$PATH",
			pkgPath:     "B:$PATH",
			want:        "A:B:$PATH",
			wantChanged: false,
		},
		{
			name:        "multiple new chunks preserve package order",
			userPath:    "B:$PATH",
			pkgPath:     "C:D:B:$PATH",
			want:        "C:D:B:$PATH",
			wantChanged: true,
		},
		{
			name:        "empty user value takes package chunks",
			userPath:    "",
			pkgPath:     "C:$PATH",
			want:        "C:$PATH",
			wantChanged: true,
		},
		{
			name:        "empty package value yields no change",
			userPath:    "A:$PATH",
			pkgPath:     "",
			want:        "A:$PATH",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, changed := mergePathValue(tt.userPath, tt.pkgPath)
			assert.Equal(t, tt.wantChanged, changed)
			assert.Equal(t, tt.want, got)
		})
	}
}

// normalizeYAML round-trips want through YAML so int/string scalar types
// match the decoded representation produced by docToMap.
func normalizeYAML(t *testing.T, want map[string]any) map[string]any {
	t.Helper()
	data, err := yaml.Marshal(want)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, yaml.Unmarshal(data, &out))
	return normalizeIdePkgConfig(out).(map[string]any)
}

func TestVersionDependentByDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		real, sentinel map[string]any
		want           map[string]any
	}{
		{
			name:     "no difference",
			real:     map[string]any{"a": "x"},
			sentinel: map[string]any{"a": "x"},
			want:     nil,
		},
		{
			name:     "top-level scalar differs",
			real:     map[string]any{"a": "v1", "b": "same"},
			sentinel: map[string]any{"a": "v0", "b": "same"},
			want:     map[string]any{"a": true},
		},
		{
			name:     "nested scalar differs keeps sibling untouched",
			real:     map[string]any{"env": map[string]any{"GOROOT": "/1", "STATIC": "x"}},
			sentinel: map[string]any{"env": map[string]any{"GOROOT": "/0", "STATIC": "x"}},
			want:     map[string]any{"env": map[string]any{"GOROOT": true}},
		},
		{
			name:     "key missing in sentinel is ignored",
			real:     map[string]any{"a": "v1"},
			sentinel: map[string]any{},
			want:     nil,
		},
		{
			name:     "type change map vs scalar is not version-dependent",
			real:     map[string]any{"a": map[string]any{"b": 1}},
			sentinel: map[string]any{"a": "scalar"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, versionDependentByDecode(tt.real, tt.sentinel))
		})
	}
}
