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

package config

import (
	"errors"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
)

// GetFrameCharset is a helper which extracts and parses a component.FrameCharSet.
// Slots missing from the config keep their value in def, including the
// image URIs of the "graphics" table, where an empty string clears the
// slot, and its "mode", "replace" or "overlay". If any graphics entry is
// invalid, it returns def and every error.
func GetFrameCharset(c config.Config, key string, def component.FrameCharSet) (
	component.FrameCharSet, error,
) {
	cfg, err := c.GetConfig(key)
	if err != nil {
		return def, err
	}

	cs := def
	r, err := cfg.GetRune("topleft")
	if err == nil {
		cs.TopLeft = r
	}
	r, err = cfg.GetRune("topright")
	if err == nil {
		cs.TopRight = r
	}
	r, err = cfg.GetRune("bottomleft")
	if err == nil {
		cs.BottomLeft = r
	}
	r, err = cfg.GetRune("bottomright")
	if err == nil {
		cs.BottomRight = r
	}
	r, err = cfg.GetRune("horizontaltop")
	if err == nil {
		cs.HorizontalTop = r
	}
	r, err = cfg.GetRune("horizontalbottom")
	if err == nil {
		cs.HorizontalBottom = r
	}
	r, err = cfg.GetRune("verticalleft")
	if err == nil {
		cs.VerticalLeft = r
	}
	r, err = cfg.GetRune("verticalright")
	if err == nil {
		cs.VerticalRight = r
	}

	g := def.Graphics
	if err := GetGraphics(cfg, "graphics", &g.Overlay,
		GraphicsSlot{"topleft", &g.TopLeft},
		GraphicsSlot{"topright", &g.TopRight},
		GraphicsSlot{"bottomleft", &g.BottomLeft},
		GraphicsSlot{"bottomright", &g.BottomRight},
		GraphicsSlot{"horizontaltop", &g.HorizontalTop},
		GraphicsSlot{"horizontalbottom", &g.HorizontalBottom},
		GraphicsSlot{"verticalleft", &g.VerticalLeft},
		GraphicsSlot{"verticalright", &g.VerticalRight},
	); err != nil {
		return def, err
	}
	cs.Graphics = g
	return cs, nil
}

// GraphicsSlot is an image URI that GetGraphics reads from Key.
type GraphicsSlot struct {
	Key string
	URI *string
}

// GetGraphics reads the graphics table at key: the URI of each slot from
// its Key, where an empty string clears the slot, and into overlay its
// "mode", "replace" or "overlay". What the table leaves out keeps its
// value, and nothing changes when key is missing. If any entry is
// invalid, nothing changes and it returns every error.
func GetGraphics(
	c config.Config, key string, overlay *bool, slots ...GraphicsSlot,
) error {
	gcfg, err := c.GetConfig(key)
	if errors.Is(err, config.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	mode := *overlay
	var errs []error
	switch m, err := gcfg.GetString("mode"); {
	case errors.Is(err, config.ErrNotFound):
	case err != nil:
		errs = append(errs, fmt.Errorf("%s.mode: %w", key, err))
	case m == "replace" || m == "overlay":
		mode = m == "overlay"
	default:
		errs = append(errs, fmt.Errorf(
			`%s.mode: expected "replace" or "overlay", got %q`, key, m))
	}
	uris := make([]string, len(slots))
	for i, slot := range slots {
		uris[i] = *slot.URI
		uri, err := gcfg.GetString(slot.Key)
		if errors.Is(err, config.ErrNotFound) {
			continue
		}
		if err == nil && uri != "" {
			err = imageuri.ValidateURI(uri)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s.%s: %w", key, slot.Key, err))
			continue
		}
		uris[i] = uri
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	*overlay = mode
	for i, slot := range slots {
		*slot.URI = uris[i]
	}
	return nil
}

// GetKey is a helper which extracts and parses a term.KeyComb as a string
// from a Config.
func GetKey(c config.Config, key string) (term.KeyComb, error) {
	s, err := c.GetString(key)
	if err != nil {
		return term.KeyComb{}, err
	}
	return term.ParseKey(s)
}
