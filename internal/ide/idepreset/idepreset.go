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

// Package idepreset renders the editor presets the first-run wizard writes
// as the user's config.
package idepreset

import (
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/ide/keymeta"
)

// Data holds the first-run choices a preset is rendered with.
type Data struct {
	Meta      keymeta.Meta
	Telemetry bool
	// AltModifier is the gui.alt_modifier spelling of the Option key reserved
	// for layout characters, or empty when neither is.
	AltModifier string
}

// footer ends every rendered preset. Its own delimiters match the body's.
const footer = `
telemetry:
  # Report anonymous usage and system information. See the Telemetry
  # page in the Rune docs for the full list of what is reported.
  enabled: [[.Telemetry]]
`

// Render executes a preset body followed by the telemetry footer. Actions
// use [[ ]] delimiters, since presets document the {{ }} layout templates
// of the status bars verbatim. The result is decoded again and must
// carry data's choices: gui.meta_key equal to data.Meta, or absent when
// data.Meta is Super, gui.alt_modifier equal to data.AltModifier, or
// absent when it is empty, and telemetry.enabled equal to data.Telemetry.
func Render(body string, data Data) (string, error) {
	tmpl, err := template.New("preset").
		Delims("[[", "]]").
		Option("missingkey=error").
		Parse(body + footer)
	if err != nil {
		return "", fmt.Errorf("parse preset: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render preset: %w", err)
	}
	out := b.String()
	if err := check(out, data); err != nil {
		return "", fmt.Errorf("rendered preset: %w", err)
	}
	return out, nil
}

func check(out string, data Data) error {
	var got struct {
		GUI struct {
			MetaKey     *string `yaml:"meta_key"`
			AltModifier *string `yaml:"alt_modifier"`
		} `yaml:"gui"`
		Telemetry struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"telemetry"`
	}
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		return err
	}
	switch {
	case got.GUI.MetaKey == nil && data.Meta != keymeta.Super:
		return fmt.Errorf("gui.meta_key is missing, want %s", data.Meta)
	case got.GUI.MetaKey != nil && *got.GUI.MetaKey != data.Meta.String():
		return fmt.Errorf("gui.meta_key is %q, want %s", *got.GUI.MetaKey, data.Meta)
	case got.GUI.AltModifier == nil && data.AltModifier != "":
		return fmt.Errorf("gui.alt_modifier is missing, want %s", data.AltModifier)
	case got.GUI.AltModifier != nil && *got.GUI.AltModifier != data.AltModifier:
		return fmt.Errorf("gui.alt_modifier is %q, want %q",
			*got.GUI.AltModifier, data.AltModifier)
	case got.Telemetry.Enabled == nil || *got.Telemetry.Enabled != data.Telemetry:
		return fmt.Errorf("telemetry.enabled is not %t", data.Telemetry)
	}
	return nil
}
