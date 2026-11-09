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
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/ide/hostenv"
)

// loadOrCreateUserConfig reads a YAML file at path into a yaml.Node document.
// If the file does not exist or is empty, it logs a warning and returns an
// empty document containing an empty mapping node.
func loadOrCreateUserConfig(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read user config: %w", err)
	}
	if os.IsNotExist(err) || len(data) == 0 {
		if os.IsNotExist(err) {
			log.Warnf("user config %s does not exist, creating empty config", path)
		} else {
			log.Warnf("user config %s is empty, creating empty config", path)
		}
		return &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{Kind: yaml.MappingNode, Tag: "!!map"},
			},
		}, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal user config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{Kind: yaml.MappingNode, Tag: "!!map"},
			},
		}, nil
	}
	return &doc, nil
}

// mergeYAMLNodes additively merges src into dst. Both must be mapping
// nodes. For each key in src: if the key does not exist in dst, append
// it; if both values are mappings, recurse so genuinely-new sub-keys
// are added; otherwise leave dst's existing value untouched. The merge
// never overwrites a value the user already has set.
func mergeYAMLNodes(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	mergeMappings(dst, src)
}

func mergeMappings(dst, src *yaml.Node) {
	for i := 0; i < len(src.Content)-1; i += 2 {
		srcKey := src.Content[i]
		srcVal := src.Content[i+1]

		dstIdx := findMappingKey(dst, srcKey.Value)
		if dstIdx < 0 {
			dst.Content = append(dst.Content, cloneNode(srcKey), cloneNode(srcVal))
			continue
		}
		dstVal := dst.Content[dstIdx+1]
		if dstVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
			mergeMappings(dstVal, srcVal)
		}
	}
}

// applyConfigDiff merges the approved overlay diff src into dst. Both must
// be mapping nodes. Unlike mergeYAMLNodes, it overwrites an existing dst
// scalar with src's value. src carries only the keys the user agreed to
// apply (the prompt diff: genuinely new keys plus version-dependent keys
// whose resolved value changed, RUNE-225), so overwriting never clobbers
// an unrelated user customization.
func applyConfigDiff(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(src.Content)-1; i += 2 {
		srcKey := src.Content[i]
		srcVal := src.Content[i+1]

		dstIdx := findMappingKey(dst, srcKey.Value)
		if dstIdx < 0 {
			dst.Content = append(dst.Content, cloneNode(srcKey), cloneNode(srcVal))
			continue
		}
		dstVal := dst.Content[dstIdx+1]
		if dstVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
			applyConfigDiff(dstVal, srcVal)
			continue
		}
		newVal := cloneNode(srcVal)
		// The comments around the replaced value are the user's prose,
		// not the package's: only the value itself is overwritten.
		inheritComments(dstVal, newVal)
		dst.Content[dstIdx+1] = newVal
	}
}

func inheritComments(from, to *yaml.Node) {
	if to.HeadComment == "" {
		to.HeadComment = from.HeadComment
	}
	if to.LineComment == "" {
		to.LineComment = from.LineComment
	}
	if to.FootComment == "" {
		to.FootComment = from.FootComment
	}
}

// findMappingKey returns the index of the key node in a mapping's Content
// slice, or -1 if not found.
func findMappingKey(mapping *yaml.Node, key string) int {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// configDiffMappingAtPath descends an applied config diff document following
// the given key path and returns the value node at that path, or nil if the
// path is absent. doc may be a DocumentNode (its first content child is used)
// or a MappingNode. Each intermediate node along the path must be a mapping;
// the final value may be of any kind.
func configDiffMappingAtPath(doc *yaml.Node, path ...string) *yaml.Node {
	if doc == nil || len(path) == 0 {
		return nil
	}
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	for _, key := range path {
		if node.Kind != yaml.MappingNode {
			return nil
		}
		idx := findMappingKey(node, key)
		if idx < 0 {
			return nil
		}
		node = node.Content[idx+1]
	}
	return node
}

// configDiffTouchesPath reports whether an applied config diff includes the
// given nested key path.
func configDiffTouchesPath(doc *yaml.Node, path ...string) bool {
	return configDiffMappingAtPath(doc, path...) != nil
}

// addedExtensionIDs returns the keys added under the top-level "extensions"
// mapping of an applied config diff. It returns nil when the diff does not
// touch "extensions" or that key is not a (non-empty) mapping.
func addedExtensionIDs(doc *yaml.Node) []string {
	node := configDiffMappingAtPath(doc, "extensions")
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var ids []string
	for i := 0; i < len(node.Content)-1; i += 2 {
		ids = append(ids, node.Content[i].Value)
	}
	return ids
}

// addedTutorialNames returns the keys added under the top-level "tutorials"
// mapping of an applied config diff. It returns nil when the diff does not
// touch "tutorials" or that key is not a (non-empty) mapping.
func addedTutorialNames(doc *yaml.Node) []string {
	node := configDiffMappingAtPath(doc, "tutorials")
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var names []string
	for i := 0; i < len(node.Content)-1; i += 2 {
		names = append(names, node.Content[i].Value)
	}
	return names
}

// summarizeConfigDiff splits the leaf key paths of an applied config diff
// into those covered by a prefix in live (inEffect) and the rest (pending).
// A scalar, a sequence or an empty mapping is a leaf. Each reported key is
// the leaf path truncated to its first two segments and joined with ".", e.g.
// gui.themes.redmond95.foreground reports as "gui.themes"; both lists are
// deduplicated and sorted. Live paths that cover no diff leaf are ignored.
func summarizeConfigDiff(doc *yaml.Node, live [][]string) (inEffect, pending []string) {
	root := doc
	if root != nil && root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil
	}

	inEffectSet := make(map[string]struct{})
	pendingSet := make(map[string]struct{})
	var walk func(node *yaml.Node, path []string)
	walk = func(node *yaml.Node, path []string) {
		if node.Kind == yaml.MappingNode && len(node.Content) > 0 {
			for i := 0; i+1 < len(node.Content); i += 2 {
				walk(node.Content[i+1], append(slices.Clip(path), node.Content[i].Value))
			}
			return
		}
		key := strings.Join(path[:min(len(path), 2)], ".")
		if slices.ContainsFunc(live, func(prefix []string) bool {
			return len(prefix) <= len(path) && slices.Equal(path[:len(prefix)], prefix)
		}) {
			inEffectSet[key] = struct{}{}
		} else {
			pendingSet[key] = struct{}{}
		}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		walk(root.Content[i+1], []string{root.Content[i].Value})
	}
	return slices.Sorted(maps.Keys(inEffectSet)), slices.Sorted(maps.Keys(pendingSet))
}

// expandNodeValues walks all scalar nodes in the tree and applies
// hostenv.ExpandVars with the given lookup function. Only string-tagged
// scalars are expanded (int, float, bool, null are skipped). Variables
// the lookup does not recognize are left literal so they can be
// expanded later at Rune startup.
func expandNodeValues(n *yaml.Node, lookup func(string) (string, bool)) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
		for _, child := range n.Content {
			expandNodeValues(child, lookup)
		}
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int", "!!float", "!!bool", "!!null":
			return
		}
		n.Value = hostenv.ExpandVars(n.Value, lookup)
	}
}

// expandMapValues walks cfg in place, applying hostenv.ExpandVars to every
// string value using the given lookup function. Nested maps and slices
// are traversed recursively; non-string scalars are left untouched.
// Variables the lookup does not recognize are left literal.
func expandMapValues(cfg map[string]any, lookup func(string) (string, bool)) {
	for k, v := range cfg {
		cfg[k] = expandAnyValue(v, lookup)
	}
}

func expandAnyValue(v any, lookup func(string) (string, bool)) any {
	switch t := v.(type) {
	case string:
		return hostenv.ExpandVars(t, lookup)
	case map[string]any:
		expandMapValues(t, lookup)
		return t
	case []any:
		for i, elem := range t {
			t[i] = expandAnyValue(elem, lookup)
		}
		return t
	default:
		return v
	}
}

// backupUserConfig copies path to path+".backup" before any write.
// It is a no-op if the source file does not exist.
func backupUserConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read for backup: %w", err)
	}
	backupPath := path + ".backup"
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return backupPath, nil
}

// writeYAMLAtomic writes doc to path atomically. It creates a temp file in
// the same directory, encodes the document, verifies that the written content
// contains all expected keys from expected, then renames.
func writeYAMLAtomic(path string, doc *yaml.Node, expected *yaml.Node) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("close encoder: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}

	// Verify round-trip
	readBack, err := os.ReadFile(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("read back: %w", err)
	}
	var written yaml.Node
	if err := yaml.Unmarshal(readBack, &written); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("unmarshal read back: %w", err)
	}
	if written.Kind != yaml.DocumentNode || len(written.Content) == 0 {
		_ = os.Remove(tmpName)
		return fmt.Errorf("written file has unexpected structure")
	}
	if err := verifyMerge(written.Content[0], expected); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("verification failed: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// verifyMerge recursively walks expected mapping keys and confirms each
// is present in written. Scalar values are not compared because the
// additive merge intentionally preserves any value the user already
// has set even when the package overlay declares a different value.
func verifyMerge(written, expected *yaml.Node) error {
	if expected.Kind != yaml.MappingNode {
		return nil
	}
	if written.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node, got kind %d", written.Kind)
	}
	for i := 0; i < len(expected.Content)-1; i += 2 {
		key := expected.Content[i].Value
		expVal := expected.Content[i+1]

		wIdx := findMappingKey(written, key)
		if wIdx < 0 {
			return fmt.Errorf("key %q missing from written config", key)
		}
		wVal := written.Content[wIdx+1]

		if expVal.Kind == yaml.MappingNode && wVal.Kind == yaml.MappingNode {
			if err := verifyMerge(wVal, expVal); err != nil {
				return fmt.Errorf("key %q: %w", key, err)
			}
		}
	}
	return nil
}

// cloneNode returns a deep copy of a yaml.Node tree.
func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	clone := &yaml.Node{
		Kind:        n.Kind,
		Style:       n.Style,
		Tag:         n.Tag,
		Value:       n.Value,
		Anchor:      n.Anchor,
		Alias:       cloneNode(n.Alias),
		HeadComment: n.HeadComment,
		LineComment: n.LineComment,
		FootComment: n.FootComment,
		Line:        n.Line,
		Column:      n.Column,
	}
	if len(n.Content) > 0 {
		clone.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			clone.Content[i] = cloneNode(child)
		}
	}
	return clone
}
