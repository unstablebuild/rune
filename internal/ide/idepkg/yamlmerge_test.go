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
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func mustParseYAML(t *testing.T, s string) *yaml.Node {
	t.Helper()
	if s == "" {
		return &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{Kind: yaml.MappingNode, Tag: "!!map"},
			},
		}
	}
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(s), &doc))
	return &doc
}

func nodeToString(t *testing.T, n *yaml.Node) string {
	t.Helper()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	require.NoError(t, enc.Encode(n))
	require.NoError(t, enc.Close())
	return buf.String()
}

func TestMergeYAMLNodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		dst      string
		src      string
		expected string
	}{
		{
			name:     "add new keys",
			dst:      "a: 1\n",
			src:      "b: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "preserve scalar on overlap",
			dst:      "a: 1\n",
			src:      "a: 2\n",
			expected: "a: 1\n",
		},
		{
			name:     "deep merge nested maps preserves overlapping scalars",
			dst:      "top:\n  a: 1\n  b: 2\n",
			src:      "top:\n  b: 3\n  c: 4\n",
			expected: "top:\n  a: 1\n  b: 2\n  c: 4\n",
		},
		{
			name:     "deep merge two levels",
			dst:      "l1:\n  l2:\n    a: 1\n",
			src:      "l1:\n  l2:\n    b: 2\n",
			expected: "l1:\n  l2:\n    a: 1\n    b: 2\n",
		},
		{
			name:     "preserve sequence on overlap",
			dst:      "items:\n  - a\n  - b\n",
			src:      "items:\n  - x\n  - y\n  - z\n",
			expected: "items:\n  - a\n  - b\n",
		},
		{
			name:     "new sequence is added",
			dst:      "a: 1\n",
			src:      "items:\n  - x\n  - y\n",
			expected: "a: 1\nitems:\n  - x\n  - y\n",
		},
		{
			name:     "empty src is no-op",
			dst:      "a: 1\nb: 2\n",
			src:      "",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "empty dst gets all src keys",
			dst:      "",
			src:      "a: 1\nb: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "same values are no-op",
			dst:      "a: 1\nb: 2\n",
			src:      "a: 1\nb: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "map dst preserved against scalar src",
			dst:      "a:\n  nested: 1\n",
			src:      "a: flat\n",
			expected: "a:\n  nested: 1\n",
		},
		{
			name:     "scalar dst preserved against map src",
			dst:      "a: flat\n",
			src:      "a:\n  nested: 1\n",
			expected: "a: flat\n",
		},
		{
			name:     "nested map adds new sub-keys without touching existing",
			dst:      "top:\n  a: 1\n  nested:\n    x: 10\n",
			src:      "top:\n  a: 99\n  nested:\n    x: 99\n    y: 20\n  newkey: 7\n",
			expected: "top:\n  a: 1\n  nested:\n    x: 10\n    y: 20\n  newkey: 7\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dstDoc := mustParseYAML(t, tt.dst)
			srcDoc := mustParseYAML(t, tt.src)
			mergeYAMLNodes(dstDoc.Content[0], srcDoc.Content[0])
			actual := nodeToString(t, dstDoc)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestMergeYAMLNodesPreservesComments(t *testing.T) {
	t.Parallel()
	dst := "# top comment\na: 1 # inline\n"
	src := "b: 2\n"
	dstDoc := mustParseYAML(t, dst)
	srcDoc := mustParseYAML(t, src)
	mergeYAMLNodes(dstDoc.Content[0], srcDoc.Content[0])
	actual := nodeToString(t, dstDoc)
	assert.Contains(t, actual, "# top comment")
	assert.Contains(t, actual, "# inline")
	assert.Contains(t, actual, "b: 2")
}

func TestApplyConfigDiff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		dst      string
		src      string
		expected string
	}{
		// --- additive behavior (shared with mergeYAMLNodes) ---
		{
			name:     "add new top-level key",
			dst:      "a: 1\n",
			src:      "b: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "empty dst gets all src keys",
			dst:      "",
			src:      "a: 1\nb: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "empty src is a no-op",
			dst:      "a: 1\nb: 2\n",
			src:      "",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "both empty stays empty",
			dst:      "",
			src:      "",
			expected: "{}\n",
		},
		{
			name:     "new nested mapping is added whole",
			dst:      "a: 1\n",
			src:      "nested:\n  x: 1\n  y: 2\n",
			expected: "a: 1\nnested:\n  x: 1\n  y: 2\n",
		},
		{
			name:     "new sequence is added whole",
			dst:      "a: 1\n",
			src:      "items:\n  - x\n  - y\n",
			expected: "a: 1\nitems:\n  - x\n  - y\n",
		},

		// --- overwrite behavior (the key difference vs mergeYAMLNodes) ---
		{
			name:     "overlapping scalar is overwritten",
			dst:      "a: 1\n",
			src:      "a: 2\n",
			expected: "a: 2\n",
		},
		{
			name:     "overlapping scalar with same value is idempotent",
			dst:      "a: 1\nb: 2\n",
			src:      "a: 1\nb: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:     "overlapping string scalar is overwritten",
			dst:      "path: /old/value\n",
			src:      "path: /new/value\n",
			expected: "path: /new/value\n",
		},
		{
			name:     "overlapping sequence is overwritten wholesale",
			dst:      "items:\n  - a\n  - b\n",
			src:      "items:\n  - x\n  - y\n  - z\n",
			expected: "items:\n  - x\n  - y\n  - z\n",
		},
		{
			name:     "overlapping sequence shrinks to src length",
			dst:      "items:\n  - a\n  - b\n  - c\n",
			src:      "items:\n  - x\n",
			expected: "items:\n  - x\n",
		},

		// --- type-mismatch overwrites ---
		{
			name:     "map dst overwritten by scalar src",
			dst:      "a:\n  nested: 1\n",
			src:      "a: flat\n",
			expected: "a: flat\n",
		},
		{
			name:     "scalar dst overwritten by map src",
			dst:      "a: flat\n",
			src:      "a:\n  nested: 1\n",
			expected: "a:\n  nested: 1\n",
		},
		{
			name:     "scalar dst overwritten by sequence src",
			dst:      "a: flat\n",
			src:      "a:\n  - x\n  - y\n",
			expected: "a:\n  - x\n  - y\n",
		},
		{
			name:     "sequence dst overwritten by scalar src",
			dst:      "a:\n  - x\n  - y\n",
			src:      "a: flat\n",
			expected: "a: flat\n",
		},
		{
			name:     "map dst overwritten by sequence src",
			dst:      "a:\n  nested: 1\n",
			src:      "a:\n  - x\n",
			expected: "a:\n  - x\n",
		},
		{
			name:     "sequence dst overwritten by map src",
			dst:      "a:\n  - x\n",
			src:      "a:\n  nested: 1\n",
			expected: "a:\n  nested: 1\n",
		},

		// --- recursion into mappings present on both sides ---
		{
			name:     "deep merge adds new sub-keys and overwrites overlapping ones",
			dst:      "top:\n  a: 1\n  b: 2\n",
			src:      "top:\n  b: 3\n  c: 4\n",
			expected: "top:\n  a: 1\n  b: 3\n  c: 4\n",
		},
		{
			name:     "deep merge two levels adds and overwrites",
			dst:      "l1:\n  l2:\n    a: 1\n    b: 2\n",
			src:      "l1:\n  l2:\n    b: 9\n    c: 3\n",
			expected: "l1:\n  l2:\n    a: 1\n    b: 9\n    c: 3\n",
		},
		{
			name:     "nested map overwrites scalar leaf but keeps untouched siblings",
			dst:      "top:\n  a: 1\n  nested:\n    x: 10\n",
			src:      "top:\n  nested:\n    x: 99\n    y: 20\n  newkey: 7\n",
			expected: "top:\n  a: 1\n  nested:\n    x: 99\n    y: 20\n  newkey: 7\n",
		},
		{
			name:     "three-level deep overwrite of a single leaf",
			dst:      "a:\n  b:\n    c:\n      d: old\n      keep: yes\n",
			src:      "a:\n  b:\n    c:\n      d: new\n",
			expected: "a:\n  b:\n    c:\n      d: new\n      keep: yes\n",
		},

		// --- mixed operations in one pass ---
		{
			name: "mixed add, overwrite, recurse, and preserve",
			dst: "keep: untouched\n" +
				"scalar: old\n" +
				"nested:\n  a: 1\n  b: 2\n",
			src: "scalar: new\n" +
				"nested:\n  b: 22\n  c: 3\n" +
				"fresh: added\n",
			expected: "keep: untouched\n" +
				"scalar: new\n" +
				"nested:\n  a: 1\n  b: 22\n  c: 3\n" +
				"fresh: added\n",
		},

		// --- ordering: overwrites stay in place, additions append ---
		{
			name:     "overwrite preserves key position; new key appends",
			dst:      "a: 1\nb: 2\nc: 3\n",
			src:      "b: 22\nd: 4\n",
			expected: "a: 1\nb: 22\nc: 3\nd: 4\n",
		},

		// --- value type fidelity ---
		{
			name:     "bool scalar overwrites",
			dst:      "flag: false\n",
			src:      "flag: true\n",
			expected: "flag: true\n",
		},
		{
			name:     "float scalar overwrites",
			dst:      "ratio: 1.5\n",
			src:      "ratio: 2.25\n",
			expected: "ratio: 2.25\n",
		},
		{
			name:     "null dst overwritten by scalar",
			dst:      "a: null\n",
			src:      "a: 1\n",
			expected: "a: 1\n",
		},
		{
			name:     "scalar dst overwritten by null",
			dst:      "a: 1\n",
			src:      "a: null\n",
			expected: "a: null\n",
		},
		{
			name:     "int overwritten by string of same digits keeps string type",
			dst:      "a: 1\n",
			src:      `a: "1"` + "\n",
			expected: `a: "1"` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dstDoc := mustParseYAML(t, tt.dst)
			srcDoc := mustParseYAML(t, tt.src)
			applyConfigDiff(dstDoc.Content[0], srcDoc.Content[0])
			assert.Equal(t, tt.expected, nodeToString(t, dstDoc))
		})
	}
}

func TestApplyConfigDiffNonMappingGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dst  *yaml.Node
		src  *yaml.Node
	}{
		{
			name: "scalar dst",
			dst:  &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x"},
			src:  &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		},
		{
			name: "scalar src",
			dst:  &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
			src:  &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x"},
		},
		{
			name: "sequence dst",
			dst:  &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"},
			src:  &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := tt.dst.Content
			assert.NotPanics(t, func() {
				applyConfigDiff(tt.dst, tt.src)
			})
			assert.Equal(t, before, tt.dst.Content, "dst must be untouched")
		})
	}
}

func TestApplyConfigDiffClonesSrc(t *testing.T) {
	t.Parallel()
	dstDoc := mustParseYAML(t, "a: 1\n")
	srcDoc := mustParseYAML(t, "a: 2\nb:\n  c: 3\n")
	applyConfigDiff(dstDoc.Content[0], srcDoc.Content[0])
	require.Equal(t, "a: 2\nb:\n  c: 3\n", nodeToString(t, dstDoc))

	// Mutate every scalar in src; dst must remain stable.
	expandNodeValues(srcDoc, func(string) (string, bool) { return "MUTATED", true })
	for i := 1; i < len(srcDoc.Content[0].Content); i += 2 {
		v := srcDoc.Content[0].Content[i]
		if v.Kind == yaml.ScalarNode {
			v.Value = "MUTATED"
		}
	}
	assert.Equal(t, "a: 2\nb:\n  c: 3\n", nodeToString(t, dstDoc),
		"dst must not alias src nodes")
}

func TestApplyConfigDiffPreservesUntouchedComments(t *testing.T) {
	t.Parallel()
	dst := "# top comment\nkeep: 1 # inline\nchange: old\n"
	src := "change: new\nadd: 2\n"
	dstDoc := mustParseYAML(t, dst)
	srcDoc := mustParseYAML(t, src)
	applyConfigDiff(dstDoc.Content[0], srcDoc.Content[0])
	actual := nodeToString(t, dstDoc)
	assert.Contains(t, actual, "# top comment")
	assert.Contains(t, actual, "# inline")
	assert.Contains(t, actual, "change: new")
	assert.Contains(t, actual, "add: 2")
}

func TestExpandNodeValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		mapping  func(string) (string, bool)
		expected string
	}{
		{
			name:  "expand string scalar",
			input: "path: $HOME/bin\n",
			mapping: func(key string) (string, bool) {
				if key == "HOME" {
					return "/usr", true
				}
				return "", false
			},
			expected: "path: /usr/bin\n",
		},
		{
			name:  "skip int",
			input: "port: 8080\n",
			mapping: func(string) (string, bool) {
				return "REPLACED", true
			},
			expected: "port: 8080\n",
		},
		{
			name:  "skip bool",
			input: "enabled: true\n",
			mapping: func(string) (string, bool) {
				return "REPLACED", true
			},
			expected: "enabled: true\n",
		},
		{
			name:  "skip null",
			input: "val: null\n",
			mapping: func(string) (string, bool) {
				return "REPLACED", true
			},
			expected: "val: null\n",
		},
		{
			name:  "expand nested values",
			input: "top:\n  inner:\n    path: $DIR/file\n",
			mapping: func(key string) (string, bool) {
				if key == "DIR" {
					return "/tmp", true
				}
				return "", false
			},
			expected: "top:\n  inner:\n    path: /tmp/file\n",
		},
		{
			name:  "unknown vars preserved literally",
			input: "path: $UNKNOWN/rest\n",
			mapping: func(string) (string, bool) {
				return "", false
			},
			expected: "path: $UNKNOWN/rest\n",
		},
		{
			name:  "skip float",
			input: "rate: 3.14\n",
			mapping: func(string) (string, bool) {
				return "REPLACED", true
			},
			expected: "rate: 3.14\n",
		},
		{
			name:  "known vars expand and PATH preserved",
			input: "path: $RUNE_DATADIR/python/bin:$RUNE_DATADIR/bin:$PATH\n",
			mapping: func(key string) (string, bool) {
				if key == "RUNE_DATADIR" {
					return "/data", true
				}
				return "", false
			},
			expected: "path: /data/python/bin:/data/bin:$PATH\n",
		},
		{
			name:  "brace form expands",
			input: "ver: ${RUNE_PKG_VERSION}-rc\n",
			mapping: func(key string) (string, bool) {
				if key == "RUNE_PKG_VERSION" {
					return "1.2.3", true
				}
				return "", false
			},
			expected: "ver: 1.2.3-rc\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := mustParseYAML(t, tt.input)
			expandNodeValues(doc, tt.mapping)
			actual := nodeToString(t, doc)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestExpandMapValues(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{
		"path": "$RUNE_DATADIR/bin:$PATH",
		"nested": map[string]any{
			"id":  "$RUNE_PKG_ID",
			"ver": "${RUNE_PKG_VERSION}",
		},
		"list": []any{
			"$RUNE_DATADIR/a",
			"$PATH",
			42,
			true,
		},
		"count":   7,
		"enabled": false,
	}
	expandMapValues(cfg, func(key string) (string, bool) {
		switch key {
		case "RUNE_DATADIR":
			return "/data", true
		case "RUNE_PKG_ID":
			return "python", true
		case "RUNE_PKG_VERSION":
			return "1.2.3", true
		}
		return "", false
	})

	assert.Equal(t, "/data/bin:$PATH", cfg["path"])
	nested := cfg["nested"].(map[string]any)
	assert.Equal(t, "python", nested["id"])
	assert.Equal(t, "1.2.3", nested["ver"])
	list := cfg["list"].([]any)
	assert.Equal(t, "/data/a", list[0])
	assert.Equal(t, "$PATH", list[1])
	assert.Equal(t, 42, list[2])
	assert.Equal(t, true, list[3])
	assert.Equal(t, 7, cfg["count"])
	assert.Equal(t, false, cfg["enabled"])
}

func TestLoadOrCreateUserConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string) string
		expectEmpty bool
		expectErr   bool
	}{
		{
			name: "file missing creates empty doc",
			setup: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "nonexistent.yaml")
			},
			expectEmpty: true,
		},
		{
			name: "file exists",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "config.yaml")
				require.NoError(t, os.WriteFile(p, []byte("key: value\n"), 0644))
				return p
			},
		},
		{
			name: "empty file",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "empty.yaml")
				require.NoError(t, os.WriteFile(p, []byte{}, 0644))
				return p
			},
			expectEmpty: true,
		},
		{
			name: "invalid YAML",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "bad.yaml")
				require.NoError(t, os.WriteFile(p, []byte(":\n  :\n  - {["), 0644))
				return p
			},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := tt.setup(t, dir)
			doc, err := loadOrCreateUserConfig(path)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, doc)
			require.Equal(t, yaml.DocumentNode, doc.Kind)
			require.Len(t, doc.Content, 1)
			require.Equal(t, yaml.MappingNode, doc.Content[0].Kind)
			if tt.expectEmpty {
				assert.Empty(t, doc.Content[0].Content)
			} else {
				assert.NotEmpty(t, doc.Content[0].Content)
			}
		})
	}
}

func TestWriteYAMLAtomic(t *testing.T) {
	t.Parallel()
	t.Run("round-trip write and read", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		doc := mustParseYAML(t, "key: value\nnested:\n  a: 1\n")
		expected := doc.Content[0]

		require.NoError(t, writeYAMLAtomic(path, doc, expected))

		readDoc, err := loadOrCreateUserConfig(path)
		require.NoError(t, err)
		idx := findMappingKey(readDoc.Content[0], "key")
		require.GreaterOrEqual(t, idx, 0)
		assert.Equal(t, "value", readDoc.Content[0].Content[idx+1].Value)
	})

	t.Run("creates parent dirs", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sub", "deep", "config.yaml")
		doc := mustParseYAML(t, "a: 1\n")
		expected := doc.Content[0]

		require.NoError(t, writeYAMLAtomic(path, doc, expected))

		_, err := os.Stat(path)
		require.NoError(t, err)
	})

	t.Run("verification catches corruption", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		// Write a doc that doesn't contain the expected keys
		doc := mustParseYAML(t, "other: stuff\n")
		expected := &yaml.Node{
			Kind: yaml.MappingNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "required_key"},
				{Kind: yaml.ScalarNode, Value: "required_value"},
			},
		}

		err := writeYAMLAtomic(path, doc, expected)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verification failed")

		// File should not exist since verification failed
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr))
	})
}

func TestVerifyMerge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		written   string
		expected  string
		expectErr string
	}{
		{
			name:     "matching docs pass",
			written:  "a: 1\nb: 2\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:      "missing key fails",
			written:   "a: 1\n",
			expected:  "a: 1\nb: 2\n",
			expectErr: `key "b" missing from written config`,
		},
		{
			name:     "different scalar value passes",
			written:  "a: 1\nb: wrong\n",
			expected: "a: 1\nb: 2\n",
		},
		{
			name:      "nested mismatch fails",
			written:   "top:\n  a: 1\n",
			expected:  "top:\n  a: 1\n  b: 2\n",
			expectErr: `key "top": key "b" missing from written config`,
		},
		{
			name:     "written has extra keys and still passes",
			written:  "a: 1\nb: 2\nextra: 3\n",
			expected: "a: 1\nb: 2\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wDoc := mustParseYAML(t, tt.written)
			eDoc := mustParseYAML(t, tt.expected)
			err := verifyMerge(wDoc.Content[0], eDoc.Content[0])
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCloneNode(t *testing.T) {
	t.Parallel()
	original := mustParseYAML(t, "a: 1\nnested:\n  b: 2\n")
	clone := cloneNode(original)

	// Mutate clone
	clone.Content[0].Content[1].Value = "changed"
	clone.Content[0].Content[3].Content[1].Value = "mutated"

	// Original should be unchanged
	assert.Equal(t, "1", original.Content[0].Content[1].Value)
	assert.Equal(t, "2", original.Content[0].Content[3].Content[1].Value)
}

func TestBackupUserConfig(t *testing.T) {
	t.Parallel()
	t.Run("creates backup", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("original: content\n"), 0644))

		_, err := backupUserConfig(path)
		require.NoError(t, err)

		data, err := os.ReadFile(path + ".backup")
		require.NoError(t, err)
		assert.Equal(t, "original: content\n", string(data))
	})

	t.Run("overwrites previous backup", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		backupPath := path + ".backup"
		require.NoError(t, os.WriteFile(backupPath, []byte("old backup\n"), 0644))
		require.NoError(t, os.WriteFile(path, []byte("new content\n"), 0644))

		_, err := backupUserConfig(path)
		require.NoError(t, err)

		data, err := os.ReadFile(backupPath)
		require.NoError(t, err)
		assert.Equal(t, "new content\n", string(data))
	})

	t.Run("returns error if file does not exist", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "nonexistent.yaml")

		_, err := backupUserConfig(path)
		require.Error(t, err)
	})
}

func TestConfigDiffPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		doc       string
		path      []string
		wantTouch bool
		wantValue string
	}{
		{
			name:      "gui.env present extracts mapping",
			doc:       "gui:\n  env:\n    FOO: bar\n",
			path:      []string{"gui", "env"},
			wantTouch: true,
			wantValue: "FOO: bar\n",
		},
		{
			name:      "top-level env alone is not gui.env",
			doc:       "env:\n  FOO: bar\n",
			path:      []string{"gui", "env"},
			wantTouch: false,
		},
		{
			name:      "gui without env",
			doc:       "gui:\n  font_size: 12\n",
			path:      []string{"gui", "env"},
			wantTouch: false,
		},
		{
			name:      "gui.env scalar (non-mapping) still detected at path",
			doc:       "gui:\n  env: nonsense\n",
			path:      []string{"gui", "env"},
			wantTouch: true,
			wantValue: "nonsense\n",
		},
		{
			name:      "empty doc",
			doc:       "",
			path:      []string{"gui", "env"},
			wantTouch: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := mustParseYAML(t, tt.doc)
			assert.Equal(t, tt.wantTouch, configDiffTouchesPath(doc, tt.path...))
			node := configDiffMappingAtPath(doc, tt.path...)
			if tt.wantTouch {
				require.NotNil(t, node)
				assert.Equal(t, tt.wantValue, nodeToString(t, node))
			} else {
				assert.Nil(t, node)
			}
		})
	}
}

func TestConfigDiffMappingAtPathNilAndEmpty(t *testing.T) {
	t.Parallel()
	assert.Nil(t, configDiffMappingAtPath(nil, "gui", "env"))
	doc := mustParseYAML(t, "gui:\n  env:\n    FOO: bar\n")
	assert.Nil(t, configDiffMappingAtPath(doc))
}

func TestAddedExtensionIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "single extension added",
			doc:  "extensions:\n  rune-agent:\n    path: rune-agent\n",
			want: []string{"rune-agent"},
		},
		{
			name: "extension with path and nested config object",
			doc: "extensions:\n" +
				"  rune-agent:\n" +
				"    path: rune-agent\n" +
				"    config:\n" +
				"      model: sonnet\n" +
				"      provider:\n" +
				"        name: anthropic\n" +
				"        timeout: 30\n",
			want: []string{"rune-agent"},
		},
		{
			name: "multiple extensions added",
			doc:  "extensions:\n  rune-agent:\n    path: a\n  go-lsp:\n    path: b\n",
			want: []string{"go-lsp", "rune-agent"},
		},
		{
			name: "extensions alongside other keys",
			doc:  "gui:\n  env:\n    FOO: bar\nextensions:\n  rune-agent:\n    path: a\n",
			want: []string{"rune-agent"},
		},
		{
			name: "gui.env only is not an extension",
			doc:  "gui:\n  env:\n    FOO: bar\n",
			want: nil,
		},
		{
			name: "no extensions key",
			doc:  "settings:\n  theme: dark\n",
			want: nil,
		},
		{
			name: "empty doc",
			doc:  "",
			want: nil,
		},
		{
			name: "extensions present but empty mapping",
			doc:  "extensions: {}\n",
			want: nil,
		},
		{
			name: "extensions scalar (non-mapping) yields nothing",
			doc:  "extensions: nonsense\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := mustParseYAML(t, tt.doc)
			got := addedExtensionIDs(doc)
			sort.Strings(got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConfigMergeEventAddedExtensionIDs(t *testing.T) {
	t.Parallel()
	doc := mustParseYAML(t, "extensions:\n  rune-agent:\n    path: rune-agent\n")
	e := ConfigMergeEvent{Diff: doc}
	assert.Equal(t, []string{"rune-agent"}, e.AddedExtensionIDs())
}

func TestAddedTutorialNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "single tutorial added",
			doc:  "tutorials:\n  go-intro: go-intro.star\n",
			want: []string{"go-intro"},
		},
		{
			name: "multiple tutorials added",
			doc:  "tutorials:\n  go-intro: a.star\n  rust-intro: b.star\n",
			want: []string{"go-intro", "rust-intro"},
		},
		{
			name: "tutorials alongside other keys",
			doc:  "extensions:\n  rune-agent:\n    path: a\ntutorials:\n  go-intro: a.star\n",
			want: []string{"go-intro"},
		},
		{
			name: "extensions only is not a tutorial",
			doc:  "extensions:\n  rune-agent:\n    path: a\n",
			want: nil,
		},
		{
			name: "no tutorials key",
			doc:  "settings:\n  theme: dark\n",
			want: nil,
		},
		{
			name: "empty doc",
			doc:  "",
			want: nil,
		},
		{
			name: "tutorials present but empty mapping",
			doc:  "tutorials: {}\n",
			want: nil,
		},
		{
			name: "tutorials scalar (non-mapping) yields nothing",
			doc:  "tutorials: nonsense\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := mustParseYAML(t, tt.doc)
			got := addedTutorialNames(doc)
			sort.Strings(got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConfigMergeEventAddedTutorialNames(t *testing.T) {
	t.Parallel()
	doc := mustParseYAML(t, "tutorials:\n  go-intro: go-intro.star\n")
	e := ConfigMergeEvent{Diff: doc}
	assert.Equal(t, []string{"go-intro"}, e.AddedTutorialNames())
}

func TestSummarizeConfigDiff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		doc          string
		live         [][]string
		wantInEffect []string
		wantPending  []string
	}{
		{
			name:         "all live",
			doc:          "gui:\n  env:\n    FOO: bar\n    BAZ: qux\n",
			live:         [][]string{{"gui", "env"}},
			wantInEffect: []string{"gui.env"},
		},
		{
			name:         "partial",
			doc:          "gui:\n  env:\n    FOO: bar\n  font_size: 14\n",
			live:         [][]string{{"gui", "env"}},
			wantInEffect: []string{"gui.env"},
			wantPending:  []string{"gui.font_size"},
		},
		{
			name:        "none live",
			doc:         "settings:\n  theme: dark\ngui:\n  env:\n    FOO: bar\n",
			wantPending: []string{"gui.env", "settings.theme"},
		},
		{
			name: "nested theme leaves covered by gui.themes",
			doc: "gui:\n  themes:\n    redmond95:\n      foreground: '#000000'\n" +
				"      background: '#c0c0c0'\n      colors:\n        red: '#800000'\n",
			live:         [][]string{{"gui", "themes"}},
			wantInEffect: []string{"gui.themes"},
		},
		{
			name:         "extension path covered by its id",
			doc:          "extensions:\n  rune.agent:\n    path: /bin/agent\n  other:\n    path: /bin/other\n",
			live:         [][]string{{"extensions", "rune.agent"}},
			wantInEffect: []string{"extensions.rune.agent"},
			wantPending:  []string{"extensions.other"},
		},
		{
			name:        "empty mapping is a leaf",
			doc:         "gui:\n  themes: {}\n",
			wantPending: []string{"gui.themes"},
		},
		{
			name:         "sequence is a leaf",
			doc:          "gui:\n  fonts:\n    - a\n    - b\n",
			live:         [][]string{{"gui", "fonts"}},
			wantInEffect: []string{"gui.fonts"},
		},
		{
			name:        "top-level scalar",
			doc:         "default_shell: zsh\n",
			wantPending: []string{"default_shell"},
		},
		{
			name:        "live path not in diff is ignored",
			doc:         "settings:\n  theme: dark\n",
			live:        [][]string{{"gui", "env"}},
			wantPending: []string{"settings.theme"},
		},
		{
			name:         "live path deeper than the leaf does not cover it",
			doc:          "gui:\n  themes: {}\n",
			live:         [][]string{{"gui", "themes", "redmond95"}},
			wantPending:  []string{"gui.themes"},
			wantInEffect: nil,
		},
		{
			name: "empty doc",
			doc:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inEffect, pending := summarizeConfigDiff(mustParseYAML(t, tt.doc), tt.live)
			assert.Equal(t, tt.wantInEffect, inEffect)
			assert.Equal(t, tt.wantPending, pending)
		})
	}
}
