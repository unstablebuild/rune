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
	"path/filepath"
	"strings"

	"github.com/unstablebuild/blue/release"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/ide/hostenv"
)

type configChangePlan struct {
	// prompt is true only when conflictCfg is non-empty.
	prompt      bool
	missingYAML []byte
	// userDoc is the user config exactly as parsed from disk, so merging
	// into it keeps comments, key order and formatting the user (or the
	// shipped preset) wrote. It is nil for starlark configs, which are
	// merged through their own writer.
	userDoc      *yaml.Node
	pkgDoc       *yaml.Node
	autoApplyDoc *yaml.Node
	pathChanges  []extensionPathChange
}

type extensionPathChange struct {
	extensionID   string
	currentPath   string
	installedPath string
	pkgDoc        *yaml.Node
}

func planConfigChange(
	pkgConfigFile string, pkgConfigData []byte,
	userCfg map[string]any, userDoc *yaml.Node,
	pkgID string, pkgVersion release.Version,
	dataDir, editorMode string, promptExtensionPaths bool,
) (configChangePlan, error) {
	// RUNE_DATADIR is not expanded here: it names the data directory of the
	// host that uses the value, which is not necessarily this one.
	runeVarMapping := func(key string) (string, bool) {
		switch key {
		case "RUNE_PKG_ID":
			return pkgID, true
		case "RUNE_PKG_VERSION":
			return string(pkgVersion), true
		}
		return "", false
	}

	pkgOverlayCfg, err := loadIdePkgConfigOverlay(
		pkgConfigFile, pkgConfigData, map[string]any{},
		pkgID, pkgVersion, editorMode,
	)
	if err != nil {
		return configChangePlan{}, fmt.Errorf("decode package config: %w", err)
	}
	// requirements is package metadata consumed at install time; it
	// must never reach the user config.
	delete(pkgOverlayCfg, requirementsKey)
	if len(pkgOverlayCfg) == 0 {
		return configChangePlan{}, nil
	}

	versionDependent, err := versionDependentOverlayKeys(
		pkgConfigFile, pkgConfigData, pkgOverlayCfg,
		pkgID, editorMode,
	)
	if err != nil {
		return configChangePlan{}, err
	}
	expandMapValues(pkgOverlayCfg, runeVarMapping)
	var pathChanges []extensionPathChange
	if promptExtensionPaths {
		pathChanges, err = extractExtensionPathChanges(userCfg, pkgOverlayCfg, dataDir)
		if err != nil {
			return configChangePlan{}, err
		}
	}

	newCfg, conflictCfg := idePkgConfigDiff(
		userCfg, pkgOverlayCfg, versionDependent, dataDir, nil,
	)
	if newCfg == nil && conflictCfg == nil && len(pathChanges) == 0 {
		return configChangePlan{}, nil
	}

	plan := configChangePlan{userDoc: userDoc, pathChanges: pathChanges}

	if newCfg != nil {
		autoApplyDoc, err := mapToYAMLDocument(newCfg)
		if err != nil {
			return configChangePlan{}, fmt.Errorf("auto-apply config to yaml: %w", err)
		}
		plan.autoApplyDoc = autoApplyDoc
	}

	if conflictCfg != nil {
		pkgDoc, err := mapToYAMLDocument(conflictCfg)
		if err != nil {
			return configChangePlan{}, fmt.Errorf("package config to yaml: %w", err)
		}
		missingYAML, err := yaml.Marshal(conflictCfg)
		if err != nil {
			return configChangePlan{}, fmt.Errorf("marshal conflicting keys: %w", err)
		}
		plan.prompt = true
		plan.pkgDoc = pkgDoc
		plan.missingYAML = missingYAML
	}

	return plan, nil
}

func extractExtensionPathChanges(
	userCfg, pkgOverlayCfg map[string]any, dataDir string,
) ([]extensionPathChange, error) {
	userExtensions, ok := userCfg["extensions"].(map[string]any)
	if !ok {
		return nil, nil
	}
	pkgExtensions, ok := pkgOverlayCfg["extensions"].(map[string]any)
	if !ok {
		return nil, nil
	}

	var changes []extensionPathChange
	for id, pkgExtensionValue := range pkgExtensions {
		pkgExtension, ok := pkgExtensionValue.(map[string]any)
		if !ok {
			continue
		}
		installedPath, ok := pkgExtension["path"].(string)
		if !ok || !pathWithinDir(hostenv.ExpandDataDir(installedPath, dataDir), dataDir) {
			continue
		}
		userExtension, ok := userExtensions[id].(map[string]any)
		if !ok {
			continue
		}
		currentPath, ok := userExtension["path"].(string)
		if !ok || equivalentExtensionPath(currentPath, installedPath, dataDir) {
			continue
		}

		pkgDoc, err := mapToYAMLDocument(map[string]any{
			"extensions": map[string]any{
				id: map[string]any{"path": installedPath},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("extension path change to yaml: %w", err)
		}
		changes = append(changes, extensionPathChange{
			extensionID:   id,
			currentPath:   currentPath,
			installedPath: installedPath,
			pkgDoc:        pkgDoc,
		})
		delete(pkgExtension, "path")
	}
	return changes, nil
}

func equivalentExtensionPath(currentPath, installedPath, dataDir string) bool {
	currentPath = hostenv.ExpandDataDir(currentPath, dataDir)
	installedPath = hostenv.ExpandDataDir(installedPath, dataDir)
	return filepath.Clean(currentPath) == filepath.Clean(installedPath)
}

func pathWithinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type mergedConfig struct {
	yamlDoc *yaml.Node
	// starDiff is the approved diff to deep-merge into the managed
	// rune_config section. It is only populated on the .star path.
	starDiff map[string]any
}

func buildMergedConfig(
	userDoc, pkgDoc *yaml.Node, starConfig bool,
) (mergedConfig, error) {
	if starConfig {
		diff, err := loadIdePkgConfigFromYAMLDoc(pkgDoc)
		if err != nil {
			return mergedConfig{}, fmt.Errorf("decode package diff doc: %w", err)
		}
		return mergedConfig{starDiff: diff}, nil
	}
	applyConfigDiff(userDoc.Content[0], pkgDoc.Content[0])
	return mergedConfig{yamlDoc: userDoc}, nil
}
