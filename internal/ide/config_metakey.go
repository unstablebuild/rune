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
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/keymeta"
)

const (
	keyGUIMetaKey  = "meta_key"
	pathGUIMetaKey = keyGUI + "." + keyGUIMetaKey
)

// hostOS is the OS gui.meta_key is validated for. Tests set goos to load
// another platform's presets.
func (c ideConfig) hostOS() string {
	if c.goos != "" {
		return c.goos
	}
	return runtime.GOOS
}

// metaKey resolves gui.meta_key, the physical key(s) <meta> stands for in
// Rune's own key bindings. A value that does not parse, or that the
// editor is not offered on this OS, is reported and reads as Super.
func (c ideConfig) metaKey() keymeta.Meta {
	m, err := c.parseMetaKey()
	if err != nil {
		c.errors[pathGUIMetaKey] = err
	}
	return m
}

func (c ideConfig) parseMetaKey() (keymeta.Meta, error) {
	if c.cfg == nil {
		return keymeta.Super, nil
	}
	guiCfg, ok := c.cfg[keyGUI].(map[string]any)
	if !ok {
		return keymeta.Super, nil
	}
	spec, err := config.MapConfig(guiCfg).GetString(keyGUIMetaKey)
	if err != nil {
		if err == config.ErrNotFound {
			return keymeta.Super, nil
		}
		return keymeta.Super, err
	}
	m, err := keymeta.Parse(spec)
	if err != nil {
		return keymeta.Super, err
	}
	mode := c.pkgEditorMode()
	options := keymeta.Options(c.hostOS(), mode)
	if !slices.Contains(options, m) {
		specs := make([]string, len(options))
		for i, o := range options {
			specs[i] = o.String()
		}
		return keymeta.Super, fmt.Errorf(
			"%s is not offered for the %s editor on %s, expected one of %s",
			m, mode, c.hostOS(), strings.Join(specs, ", "))
	}
	return m, nil
}

// configMetaKey resolves gui.meta_key from cfg the way the IDE does,
// reading an invalid value as Super. goos "" is the host OS.
func configMetaKey(cfg config.Config, goos string) keymeta.Meta {
	raw := map[string]any{}
	for _, key := range []string{"editor", keyGUI} {
		if m, err := cfg.GetMap(key); err == nil {
			raw[key] = m
		}
	}
	return ideConfig{cfg: raw, errors: map[string]error{}, goos: goos}.metaKey()
}

// parseRuneKey parses a key spec from one of Rune's own key settings and
// gives its <meta> the configured meaning.
func (c ideConfig) parseRuneKey(spec string) (term.KeyComb, error) {
	k, err := term.ParseKey(spec)
	if err != nil {
		return k, err
	}
	return c.metaKey().Apply(k), nil
}

// sequenceSpec spells a key binding, one chord or two, as a config key.
func sequenceSpec(seq handler.Sequence) string {
	if seq.Last == (term.KeyComb{}) {
		return seq.First.String()
	}
	return seq.First.String() + seq.Last.String()
}

// validateMetaKey drops an unusable gui.meta_key so the rest of the
// config still loads with <meta> on Super.
func validateMetaKey(c *ideConfig, cfg map[string]any) error {
	if _, err := c.parseMetaKey(); err != nil {
		delete(cfg[keyGUI].(map[string]any), keyGUIMetaKey)
		return fmt.Errorf("'%s' is invalid: %w; falling back to %s",
			pathGUIMetaKey, err, keymeta.Super)
	}
	return nil
}
