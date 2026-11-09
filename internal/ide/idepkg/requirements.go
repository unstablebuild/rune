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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
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

// RequirementChange is a requirement an install installed or upgraded
// before the package that requires it.
type RequirementChange struct {
	Package string
	// From is the version that was in use before the change, and empty
	// when the requirement was not installed.
	From release.Version
	To   release.Version
	// RequiredBy is the package whose install made the change.
	RequiredBy string
}

// String describes c as a sentence for the user.
func (c RequirementChange) String() string {
	if c.From == "" {
		return fmt.Sprintf("Installed %s %s, required by %s.", c.Package, c.To, c.RequiredBy)
	}
	return fmt.Sprintf("Upgraded %s from %s to %s, required by %s.",
		c.Package, c.From, c.To, c.RequiredBy)
}

// RequirementReporter is implemented by the progress writer of an
// install whose caller shows the requirement changes of the install
// itself. Without one, each change is shown as a notification.
type RequirementReporter interface {
	ReportRequirement(RequirementChange)
}

// ReportRequirement reports c to the RequirementReporter of the install
// pw is the progress writer of, including when pw is the writer of a
// nested requirement install, and otherwise notifies n of c.
func ReportRequirement(n browserapi.Notifications, pw repl.ProgressWriter, c RequirementChange) {
	for {
		w, ok := pw.(requirementProgressWriter)
		if !ok {
			break
		}
		pw = w.pw
	}
	if r, ok := pw.(RequirementReporter); ok {
		r.ReportRequirement(c)
		return
	}
	_, _ = n.Notify(browserapi.LevelInfo, "%s", c)
}
