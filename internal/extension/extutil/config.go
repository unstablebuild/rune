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

package extutil

import (
	"errors"
	"fmt"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

// Tabspaces extract editor.tabspaces from the given cfg.
func Tabspaces(cfg config.Config) (int, error) {
	editorConfig, err := cfg.GetConfig("editor")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'editor' from config: %v", err)
			return 0, err
		}
		return component.DefaultTabspaces, nil
	}

	ret, err := editorConfig.GetInt("tabspaces")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'tabspaces' from config: %v", err)
			return 0, err
		}
		ret = component.DefaultTabspaces
	}
	return ret, nil
}

// WindowManagerFrame extract browser.window_manager.frame from the given cfg.
func WindowManagerFrame(cfg config.Config) (bool, error) {
	browserConfig, err := cfg.GetConfig("browser")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'browser' from config: %v", err)
			return false, err
		}
		return browser.DefaultConfig().Frame, nil
	}

	wmConfig, err := browserConfig.GetConfig("window_manager")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'window_manager' from config: %v", err)
			return false, err
		}
		return browser.DefaultConfig().Frame, nil
	}

	ret, err := wmConfig.GetBool("frame")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'frame' from config: %v", err)
			return false, err
		}
		ret = browser.DefaultConfig().Frame
	}
	return ret, nil
}

// GetColorRGB expands c into the RGB value the host draws it with under
// the theme named by gui.default_theme.
//
// An extension hands the host named colors and lets it resolve them
// against the theme, so this is only for color arithmetic, which has to
// happen on hex values. Blending the W3C defaults this process would
// otherwise resolve a name to produces colors that clash with the
// themed ones drawn beside them. A host with no theme configured leaves
// c alone, since nothing here knows better than the terminal does.
func GetColorRGB(cfg config.Config, c term.Color) (term.Color, error) {
	if !c.Valid() || c.IsRGB() {
		return c, nil
	}
	theme, err := colorTheme(cfg)
	if err != nil || theme == nil {
		return c, err
	}
	for _, name := range colorNames(c) {
		value, err := theme.GetColor(name)
		if err != nil {
			if err != config.ErrNotFound {
				return c.TrueColor(), fmt.Errorf(
					"failed to get '%s' from theme config: %v", name, err)
			}
			continue
		}
		if value.Valid() {
			// A theme slot naming another color takes that color's own
			// value rather than whatever the theme maps it to, matching
			// how the host applies a theme in one pass.
			return value.TrueColor(), nil
		}
	}
	return c.TrueColor(), nil
}

// colorNames returns every W3C name for c, sorted. A palette slot can
// carry several ("gray" and "grey", "aqua" and "cyan") and a theme only
// has to name one of them, so the caller tries all of them in an order
// that does not change from run to run.
func colorNames(c term.Color) []string {
	var ret []string
	for name, named := range term.GetColorNames() {
		if named == c {
			ret = append(ret, name)
		}
	}
	slices.Sort(ret)
	return ret
}

// colorTheme returns the gui.themes entry named by gui.default_theme,
// or nil when the host has no theme configured.
func colorTheme(cfg config.Config) (config.Config, error) {
	guiConfig, err := cfg.GetConfig("gui")
	if err != nil {
		if err != config.ErrNotFound {
			return nil, fmt.Errorf("failed to get 'gui' from config: %v", err)
		}
		return nil, nil
	}

	name, err := guiConfig.GetString("default_theme")
	if err != nil {
		if err != config.ErrNotFound {
			return nil, fmt.Errorf(
				"failed to get 'default_theme' from config: %v", err)
		}
		return nil, nil
	}
	if name == "" {
		return nil, nil
	}

	themes, err := guiConfig.GetConfig("themes")
	if err != nil {
		if err != config.ErrNotFound {
			return nil, fmt.Errorf("failed to get 'themes' from config: %v", err)
		}
		return nil, nil
	}

	theme, err := themes.GetConfig(name)
	if err != nil {
		if err != config.ErrNotFound {
			return nil, fmt.Errorf(
				"failed to get 'themes.%s' from config: %v", name, err)
		}
		return nil, nil
	}
	return theme, nil
}

// Clipboard returns the configured clipboard.
func Clipboard(cfg config.Config) (clipboard.Register, error) {
	sys, err := cfg.GetString("clipboard")
	if err != nil && err != config.ErrNotFound {
		err = fmt.Errorf("failed to get 'clipboard' from config: %v", err)
		return nil, err
	}

	if err == config.ErrNotFound {
		sys = "memory"
	}

	switch sys {
	case "memory":
		return clipboard.NewInMemory(), nil
	case "system":
		return text.NewSystemClipboard(), nil
	default:
		return nil, errors.New("unknown clipboard")
	}
}

// Editor returns the editor implementation as configured.
func Editor(clipboard clipboard.Register, cfg config.Config) (text.Editor, error) {
	mode, err := editorMode(cfg)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "vim":
		return viEditor(clipboard), nil
	case "helix":
		return helixEditor(clipboard), nil
	case "emacs":
		return emacsEditor(clipboard), nil
	case "exo":
		fallback, err := exoFallback(cfg)
		if err != nil {
			return nil, err
		}
		switch fallback {
		case "vim":
			return viEditor(clipboard), nil
		case "helix":
			return helixEditor(clipboard), nil
		case "emacs":
			return emacsEditor(clipboard), nil
		}
		return standardEditor(clipboard), nil
	default:
		return standardEditor(clipboard), nil
	}
}

// EditorModal reports whether the configured compose editor is modal
// (vi- or helix-style). It mirrors the resolution Editor performs,
// including the editor.exo fallback. A bare <Enter> submits in modal
// normal mode and inserts a newline in insert mode, so callers gate
// submission on this.
func EditorModal(cfg config.Config) (bool, error) {
	mode, err := editorMode(cfg)
	if err != nil {
		return false, err
	}
	switch mode {
	case "vim", "helix":
		return true, nil
	case "exo":
		fallback, err := exoFallback(cfg)
		if err != nil {
			return false, err
		}
		return fallback == "vim" || fallback == "helix", nil
	default:
		return false, nil
	}
}

// viEditor builds a vi editor with all chrome bars disabled so an
// extension-hosted compose buffer shows only the text area.
func viEditor(clipboard clipboard.Register) text.Editor {
	return vi.Editor(
		vi.WithClipboard(clipboard),
		vi.WithWrap(true),
		vi.WithSearch(false),
		vi.WithStatusBarConfig(false, text.StatusBarConfig{}),
		vi.WithAuxiliaryBar(false, text.AuxBarConfig{}),
		vi.WithIconsBar(false, text.IconsBarConfig{}),
		vi.WithGitBar(false, text.IconsBarConfig{}),
	)
}

// standardEditor builds a standard editor with all chrome bars disabled
// so an extension-hosted compose buffer shows only the text area.
func standardEditor(clipboard clipboard.Register) text.Editor {
	return standard.Editor(
		standard.WithClipboard(clipboard),
		standard.WithWrap(true),
		standard.WithStatusBarConfig(false, text.StatusBarConfig{}),
		standard.WithAuxiliaryBar(false, text.AuxBarConfig{}),
		standard.WithIconsBar(false, text.IconsBarConfig{}),
		standard.WithGitBar(false, text.IconsBarConfig{}),
	)
}

// emacsEditor builds an emacs editor with all chrome bars disabled so an
// extension-hosted compose buffer shows only the text area.
func emacsEditor(clipboard clipboard.Register) text.Editor {
	return emacs.Editor(
		emacs.WithClipboard(clipboard),
		emacs.WithWrap(true),
		emacs.WithStatusBarConfig(false, text.StatusBarConfig{}),
		emacs.WithAuxiliaryBar(false, text.AuxBarConfig{}),
		emacs.WithIconsBar(false, text.IconsBarConfig{}),
		emacs.WithGitBar(false, text.IconsBarConfig{}),
	)
}

// helixEditor builds a helix editor with all chrome bars disabled so an
// extension-hosted compose buffer shows only the text area.
func helixEditor(clipboard clipboard.Register) text.Editor {
	return helix.Editor(
		helix.WithClipboard(clipboard),
		helix.WithWrap(true),
		helix.WithSearch(false),
		helix.WithStatusBarConfig(false, text.StatusBarConfig{}),
		helix.WithAuxiliaryBar(false, text.AuxBarConfig{}),
		helix.WithIconsBar(false, text.IconsBarConfig{}),
		helix.WithGitIcons(false),
	)
}

// Wrap returns the current editor implementation is configured with wrap mode.
func Wrap(cfg config.Config) (bool, error) {
	var def bool
	edConfig, err := cfg.GetConfig("editor")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'editor' from config: %v", err)
			return false, err
		}
		return false, nil
	}

	mode, err := editorMode(cfg)
	if err != nil {
		return false, err
	}
	// The vim editor's settings were named editor.modal before editor.mode
	// "modal" became "vim". An editor that predates the rename serves only
	// that spelling, so fall back to it.
	sections := []string{mode}
	if mode == "vim" {
		sections = []string{"vim", "modal"}
	}
	for _, section := range sections {
		modeConfig, err := edConfig.GetConfig(section)
		if err != nil {
			if err != config.ErrNotFound {
				err = fmt.Errorf("failed to get '%s' from editor config: %v", section, err)
				return false, err
			}
			continue
		}
		wrap, err := modeConfig.GetBool("wrap")
		if err != nil {
			if err != config.ErrNotFound {
				err = fmt.Errorf("failed to get 'wrap' from editor config: %v", err)
				return false, err
			}
			continue
		}
		return wrap, nil
	}
	return def, nil
}

// editorMode resolves editor.mode to its canonical spelling, mapping the
// deprecated "modal" and "modeless" aliases to "vim" and "standard".
func editorMode(cfg config.Config) (string, error) {
	def := "vim"
	edConfig, err := cfg.GetConfig("editor")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'editor' from config: %v", err)
			return "", err
		}
		return def, nil
	}

	mode, err := edConfig.GetString("mode")
	if err != nil {
		if err != config.ErrNotFound {
			err = fmt.Errorf("failed to get 'mode' from editor config: %v", err)
			return "", err
		}
		mode = def
	}
	switch mode {
	case "modal":
		return "vim", nil
	case "modeless":
		return "standard", nil
	}
	return mode, nil
}

// exoFallback returns the Rune-native fallback editor used when
// editor.mode is "exo". The full external editor is not viable inside
// an extension process, so compose input uses this fallback. Valid
// values are "vim", "helix", "standard", or "emacs"; the deprecated
// "modal" and "modeless" aliases resolve to "vim" and "standard".
// Defaults to "standard".
func exoFallback(cfg config.Config) (string, error) {
	def := "standard"
	edConfig, err := cfg.GetConfig("editor")
	if err != nil {
		if err != config.ErrNotFound {
			return "", fmt.Errorf("failed to get 'editor' from config: %v", err)
		}
		return def, nil
	}

	exoConfig, err := edConfig.GetConfig("exo")
	if err != nil {
		if err != config.ErrNotFound {
			return "", fmt.Errorf("failed to get 'exo' from editor config: %v", err)
		}
		return def, nil
	}

	fallback, err := exoConfig.GetString("fallback")
	if err != nil {
		if err != config.ErrNotFound {
			return "", fmt.Errorf("failed to get 'fallback' from editor.exo config: %v", err)
		}
		return def, nil
	}

	switch fallback {
	case "vim", "helix", "standard", "emacs":
		return fallback, nil
	case "modal":
		return "vim", nil
	case "modeless":
		return "standard", nil
	}
	return def, nil
}
