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
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ConfigChange describes a reload of the user config: Old is the tree
// that was in effect, New the one just read, both with the defaults
// applied. Source names what caused the reload, for notices.
type ConfigChange struct {
	Old, New map[string]any
	Source   string
}

// ConfigChangeResult is what a config change hook reports back.
type ConfigChangeResult struct {
	// LivePaths lists the config key paths whose new values the running
	// process picked up. Each entry covers the whole subtree under it;
	// changes not covered by any entry need a restart to take effect.
	// Paths are segment slices rather than dotted strings because ids
	// and names may contain dots.
	LivePaths [][]string
}

// HUDLivePaths are the config paths a HUD reload (IDE.LoadHUD, then
// IDE.ApplyHUD) puts into effect, for a config change hook that reloads
// the HUD to report live.
var HUDLivePaths = [][]string{
	{"browser"},
	{"command", "frame"},
	{"command", "separator"},
	{"command", "show_manual"},
	{"gui", "app_icon"},
	{"gui", "default_theme"},
	{"gui", "quick_menu"},
	{"gui", "quick_menu_sounds"},
	{"gui", "themes"},
	{"gui", "volume"},
	{"notifications"},
	{"program_status"},
	{"status"},
	{"status_bar"},
	{"terminal", "bell"},
	{"workspace"},
}

// TouchesPath reports whether the subtree at path differs between Old
// and New.
func (c ConfigChange) TouchesPath(path ...string) bool {
	return !reflect.DeepEqual(configAt(c.Old, path), configAt(c.New, path))
}

// AddedExtensionIDs lists the extensions New gives a path and Old did
// not, sorted: the ones the change makes runnable.
func (c ConfigChange) AddedExtensionIDs() []string {
	oldExts, _ := configAt(c.Old, []string{"extensions"}).(map[string]any)
	newExts, _ := configAt(c.New, []string{"extensions"}).(map[string]any)
	var ids []string
	for id, newExt := range newExts {
		if extensionPath(newExt) != "" && extensionPath(oldExts[id]) == "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// AddedTutorialNames lists the tutorials New declares and Old did not,
// sorted.
func (c ConfigChange) AddedTutorialNames() []string {
	return c.addedKeys("tutorials")
}

// UnchangedExtensionPaths lists the extensions both trees give the same
// path, sorted: the ones a change touched only the config of.
func (c ConfigChange) UnchangedExtensionPaths() []string {
	oldExts, _ := configAt(c.Old, []string{"extensions"}).(map[string]any)
	newExts, _ := configAt(c.New, []string{"extensions"}).(map[string]any)
	var ids []string
	for id, newExt := range newExts {
		if path := extensionPath(newExt); path != "" && path == extensionPath(oldExts[id]) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func extensionPath(ext any) string {
	m, _ := ext.(map[string]any)
	path, _ := m["path"].(string)
	return path
}

func (c ConfigChange) addedKeys(key string) []string {
	oldMap, _ := configAt(c.Old, []string{key}).(map[string]any)
	newMap, _ := configAt(c.New, []string{key}).(map[string]any)
	var added []string
	for k := range newMap {
		if _, ok := oldMap[k]; !ok {
			added = append(added, k)
		}
	}
	slices.Sort(added)
	return added
}

// ChangedPaths lists where Old and New differ, sorted and at most two
// segments deep (extensions.<id>, gui.env), so a notice can name them.
func (c ConfigChange) ChangedPaths() [][]string {
	changed := make(map[string][]string)
	var walk func(oldVal, newVal any, path []string)
	walk = func(oldVal, newVal any, path []string) {
		if reflect.DeepEqual(oldVal, newVal) {
			return
		}
		oldMap, oldIsMap := oldVal.(map[string]any)
		newMap, newIsMap := newVal.(map[string]any)
		if len(path) < 2 && (oldIsMap || oldVal == nil) && (newIsMap || newVal == nil) {
			keys := slices.Collect(maps.Keys(oldMap))
			for k := range newMap {
				if _, ok := oldMap[k]; !ok {
					keys = append(keys, k)
				}
			}
			for _, k := range keys {
				walk(oldMap[k], newMap[k], append(slices.Clip(path), k))
			}
			return
		}
		changed[strings.Join(path, "\x00")] = path
	}
	walk(c.Old, c.New, nil)
	paths := slices.Collect(maps.Values(changed))
	slices.SortFunc(paths, slices.Compare)
	return paths
}

// summarize splits the changed paths into the ones live covers and the
// rest, each as dotted names.
func (c ConfigChange) summarize(live [][]string) (inEffect, pending []string) {
	for _, path := range c.ChangedPaths() {
		name := strings.Join(path, ".")
		if slices.ContainsFunc(live, func(prefix []string) bool {
			return len(prefix) <= len(path) && slices.Equal(path[:len(prefix)], prefix)
		}) {
			inEffect = append(inEffect, name)
		} else {
			pending = append(pending, name)
		}
	}
	return inEffect, pending
}

func configAt(cfg map[string]any, path []string) any {
	var cur any = cfg
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[key]
		if !ok {
			return nil
		}
	}
	return cur
}
