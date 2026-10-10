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

package gitpkg

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

func verifyRepoConfig(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("the repository must contain a config.yaml " +
				"at its root with a config overlay")
		}
		return fmt.Errorf("read config.yaml: %w", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config.yaml: %w", err)
	}
	if len(cfg) == 0 {
		return fmt.Errorf("config.yaml must contain a non-empty config overlay")
	}
	if cfg["extensions"] != nil {
		if err := verifyExtensions(cfg["extensions"], dir); err != nil {
			return err
		}
	}
	return verifyRequirements(cfg["requirements"])
}

func verifyExtensions(raw any, dir string) error {
	if raw == nil {
		return fmt.Errorf("config.yaml must declare a top-level " +
			"extensions map with at least one extension")
	}
	extensions, ok := raw.(map[string]any)
	if !ok || len(extensions) == 0 {
		return fmt.Errorf("config.yaml extensions must be a non-empty "+
			"map of extension IDs, got %T", raw)
	}
	ids := make([]string, 0, len(extensions))
	for id := range extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ext, ok := extensions[id].(map[string]any)
		if !ok {
			return fmt.Errorf("extension %q must be a map, got %T",
				id, extensions[id])
		}
		path, _ := ext["path"].(string)
		if err := verifyExtensionPath(path, dir); err != nil {
			return fmt.Errorf("extension %q: %w", id, err)
		}
	}
	return nil
}

func verifyExtensionPath(path, dir string) error {
	entrypoint := strings.Fields(path)
	if len(entrypoint) == 0 {
		return fmt.Errorf("path must not be empty")
	}
	target := entrypoint[0]
	switch filepath.Ext(target) {
	case ".py", ".rs":
		return nil
	case ".go":
		if filepath.Base(target) == "main.go" {
			return nil
		}
		return fmt.Errorf("go entrypoint must be a main.go, got %s", target)
	}
	if resolved, ok := resolvePackageDir(target, dir); ok {
		if err := verifyGoPackageDir(resolved); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return fmt.Errorf("path must point at a source entrypoint "+
		"(a .py or .rs file, a main.go, or a Go package directory "+
		"with a go.mod), got %s", target)
}

func verifyGoPackageDir(dir string) error {
	modPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return err
	}
	mod, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return fmt.Errorf("parse %s: %w", modPath, err)
	}
	if len(mod.Require) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "go.sum")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("go package directory %s declares module "+
				"requirements but has no go.sum; run `go mod tidy` and "+
				"commit it so the extension can run offline", dir)
		}
		return err
	}
	return nil
}

func resolvePackageDir(target, dir string) (string, bool) {
	for _, prefix := range []string{
		"$RUNE_DATADIR/lib/$RUNE_PKG_ID",
		"${RUNE_DATADIR}/lib/${RUNE_PKG_ID}",
	} {
		if target == prefix {
			return dir, true
		}
		if rest, ok := strings.CutPrefix(target, prefix+"/"); ok {
			return filepath.Join(dir, filepath.FromSlash(rest)), true
		}
	}
	if !strings.Contains(target, "$") {
		return target, true
	}
	return "", false
}

func verifyRequirements(raw any) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("config.yaml requirements must be a list "+
			"of package IDs, got %T", raw)
	}
	for _, v := range list {
		s, ok := v.(string)
		if !ok || s == "" {
			return fmt.Errorf("config.yaml requirements entries must be "+
				"non-empty strings, got %v (%T)", v, v)
		}
	}
	return nil
}
