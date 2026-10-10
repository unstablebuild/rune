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

	"github.com/unstablebuild/blue/release"
)

// requirementsKey is the top-level package config key listing Rune
// package IDs that must be installed before the declaring package. It
// is package metadata: it is consumed at install time and stripped
// before the overlay merges into the user config.
const requirementsKey = "requirements"

// pkgConfigRequirements parses the top-level `requirements` list from a
// package config overlay (config.yaml or config.star). It returns nil
// when the key is absent and an error when the value is not a list of
// non-empty strings.
func pkgConfigRequirements(
	filename string, data []byte,
	pkgID string, pkgVersion release.Version, editorMode string,
) ([]string, error) {
	cfg, err := loadIdePkgConfigOverlay(
		filename, data, map[string]any{},
		pkgID, pkgVersion, editorMode,
	)
	if err != nil {
		return nil, fmt.Errorf("decode package config: %w", err)
	}
	raw, ok := cfg[requirementsKey]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"requirements must be a list of package IDs, got %T", raw)
	}
	reqs := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf(
				"requirements entries must be non-empty strings, got %v (%T)", v, v)
		}
		reqs = append(reqs, s)
	}
	return reqs, nil
}
