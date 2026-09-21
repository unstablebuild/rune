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

package main

import (
	_ "embed"
	"errors"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/term/gui"
)

//go:embed rune.star
var docsDefaultStarlarkConfig string

//go:embed themes.star
var guiThemesStarlark string

// defaultStarlarkConfig is the self-contained baseline config evaluated
// at runtime. themes.star binds GUI_THEMES, which rune.star references,
// so the two sources are joined into one script.
var defaultStarlarkConfig = guiThemesStarlark + "\n" + docsDefaultStarlarkConfig

func getGUIFontFamily(browser browser.Browser, cfg config.Config) (ret string) {
	family, err := cfg.GetString("font_family")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.font_family' from config: %v", err)
		}
		return
	}
	ret = family
	return
}

func getGUIFontSize(browser browser.Browser, cfg config.Config) (ret float64) {
	// 0 selects the DPI-aware automatic default in the font manager,
	// which picks a larger point size on low-DPI displays.
	ret = 0
	size, err := cfg.GetFloat("font_size")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.font_size' from config: %v", err)
		}
		return
	}
	ret = size
	return
}

func getGUIScrollMultiplier(browser browser.Browser, cfg config.Config) (ret float64) {
	ret = 3
	multiplier, err := cfg.GetFloat("scroll_multiplier")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.scroll_multiplier' from config: %v", err)
		}
		return
	}
	ret = multiplier
	return
}

func getGUILineHeightOffset(browser browser.Browser, cfg config.Config) (ret float64) {
	ret = -1
	size, err := cfg.GetFloat("line_height_offset")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.line_height_offset' from config: %v", err)
		}
		return
	}
	ret = size
	return
}

func getGUIColumnWidthOffset(browser browser.Browser, cfg config.Config) (ret float64) {
	ret = 0
	size, err := cfg.GetFloat("column_width_offset")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.column_width_offset' from config: %v", err)
		}
		return
	}
	ret = size
	return
}

func getGUIFontDPI(browser browser.Browser, cfg config.Config) (ret float64) {
	ret = 0 // signals that it must be calculated automatically
	dpi, err := cfg.GetFloat("dpi")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.dpi' from config: %v", err)
		}
		return
	}
	ret = dpi
	return
}

func getGUIDefaultColorTheme(browser browser.Browser, cfg config.Config) (
	ret string,
) {
	defaultTheme, err := cfg.GetString("default_theme")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.default_theme' from config: %v", err)
		}
		return
	}
	ret = defaultTheme
	return
}

func getGUIColorThemes(browser browser.Browser, cfg config.Config) (
	ret map[string]gui.Theme,
) {
	themesMap, err := cfg.GetMap("themes")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.themes' from config: %v", err)
		}
		return
	}

	ret = make(map[string]gui.Theme, len(themesMap))
	for name := range themesMap {
		themesCfg := config.MapConfig(themesMap)
		colorsMap, err := themesCfg.GetMap(name)
		if err != nil {
			if err != config.ErrNotFound {
				_, _ = browser.Notify(browserapi.LevelError,
					"Could not load 'gui.themes.%s' from config: %v", name, err)
			}
			return
		}

		theme := gui.Theme{Colors: make(map[tcell.Color]tcell.Color)}
		colorsCfg := config.MapConfig(colorsMap)
		for colorName := range colorsMap {
			switch colorName {
			case "cursor":
				value, err := colorsCfg.GetColor(colorName)
				if err != nil {
					_, _ = browser.Notify(browserapi.LevelWarn,
						"Invalid color value for 'cursor' in "+
							"'gui.themes.%s' config: %v", name, err)
					continue
				}
				theme.Cursor = value.Tcell()
			case "foreground":
				value, err := colorsCfg.GetColor(colorName)
				if err != nil {
					_, _ = browser.Notify(browserapi.LevelWarn,
						"Invalid color value for 'foreground' in "+
							"'gui.themes.%s' config: %v", name, err)
					continue
				}
				theme.Foreground = value.Tcell()
			case "background":
				value, err := colorsCfg.GetColor(colorName)
				if err != nil {
					_, _ = browser.Notify(browserapi.LevelWarn,
						"Invalid color value for 'background' in "+
							"'gui.themes.%s' config: %v", name, err)
					continue
				}
				theme.Background = value.Tcell()
			default:
				color, ok := term.GetColorNames()[colorName]
				if !ok {
					_, _ = browser.Notify(browserapi.LevelWarn,
						"Unknown color '%s' in 'gui.themes.%s' config. "+
							"Name must be a known W3C name.", colorName, name)
					continue
				}

				value, err := colorsCfg.GetColor(colorName)
				if err != nil {
					_, _ = browser.Notify(browserapi.LevelWarn,
						"Invalid color value for color '%s' in "+
							"'gui.themes.%s' config: %v", colorName, name, err)
					continue
				}
				theme.Colors[color.Tcell()] = value.Tcell()
			}
		}
		ret[name] = theme
	}
	return
}

func getGUILigatures(browser browser.Browser, cfg config.Config) (ret bool) {
	ret = false
	ligatures, err := cfg.GetBool("ligatures")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.ligatures' from config: %v", err)
		}
		return
	}
	ret = ligatures
	return
}

func getGUIBackgroundBlur(browser browser.Browser, cfg config.Config) (ret int) {
	radius, err := cfg.GetInt("window_blur_radius")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.window_blur_radius' from config: %v", err)
		}
		return
	}
	ret = radius
	return
}

// getGUIKeyMapping reads the optional 'gui.key_mapping' table, parsing each
// source and target with term.ParseKey into a term.KeyComb remapping. Invalid
// entries are reported and skipped so a single bad mapping does not discard the
// rest. Returns nil when no valid mappings are present.
func getGUIKeyMapping(browser browser.Browser, cfg config.Config) map[term.KeyComb]term.KeyComb {
	m, err := cfg.GetMap("key_mapping")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.key_mapping' from config: %v", err)
		}
		return nil
	}
	ret := make(map[term.KeyComb]term.KeyComb, len(m))
	for src, v := range m {
		dst, ok := v.(string)
		if !ok {
			_, _ = browser.Notify(browserapi.LevelError,
				"Invalid 'gui.key_mapping.%s': expected a string target key", src)
			continue
		}
		from, err := term.ParseKey(src)
		if err != nil {
			_, _ = browser.Notify(browserapi.LevelError,
				"Invalid 'gui.key_mapping' source key '%s': %v", src, err)
			continue
		}
		to, err := term.ParseKey(dst)
		if err != nil {
			_, _ = browser.Notify(browserapi.LevelError,
				"Invalid 'gui.key_mapping' target key '%s': %v", dst, err)
			continue
		}
		ret[from] = to
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

// getGUIAltModifier reads the optional 'gui.alt_modifier'. Absent or invalid
// reserves neither key.
func getGUIAltModifier(browser browser.Browser, cfg config.Config) gui.AltModifier {
	value, err := cfg.GetString("alt_modifier")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.alt_modifier' from config: %v", err)
		}
		return gui.AltModifierNone
	}
	modifier, err := gui.ParseAltModifier(value)
	if err != nil {
		_, _ = browser.Notify(browserapi.LevelError,
			"Invalid 'gui.alt_modifier': %v", err)
	}
	return modifier
}

func getGUIEnvVars(cfg config.Config) (ret config.Config, err error) {
	var env config.Config
	ret = config.NopConfig()
	env, err = cfg.GetConfig("env")
	if err != nil {
		if err == config.ErrNotFound {
			err = nil
		}
		return
	}
	ret = env
	return
}

func getGUITransparentWindow(browser browser.Browser, cfg config.Config) (ret bool) {
	transparent, err := cfg.GetBool("enable_transparent_window")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.enable_transparent_window' from config: %v", err)
		}
		return
	}
	ret = transparent
	return
}

func getGUIWindowOpacity(browser browser.Browser, cfg config.Config) (fg, bg float64) {
	fg, bg = 1, 1
	cfg, err := cfg.GetConfig("window_opacity")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.window_opacity' from config: %v", err)
		}
		return
	}
	fgCfg, err := cfg.GetFloat("fg")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.window_opacity.fg' from config: %v", err)
		}
	} else {
		fg = fgCfg
	}
	bgCfg, err := cfg.GetFloat("bg")
	if err != nil {
		if err != config.ErrNotFound {
			_, _ = browser.Notify(browserapi.LevelError,
				"Could not load 'gui.window_opacity.bg' from config: %v", err)
		}
	} else {
		bg = bgCfg
	}
	return
}

func getGUIConfig(cfg config.Config) (config.Config, bool, error) {
	cfg, err := cfg.GetConfig("gui")
	if errors.Is(err, config.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get 'gui' section from config: %w", err)
	}
	return cfg, true, nil
}

// runeDefaultConfig is the baseline every ide.Config call in this binary must
// use so user overlay configs decode against the same tree the running IDE
// uses. Without it, subscript overrides such as
// `config["terminal"]["initial_reservoir"] = 2` fail with
// `key "terminal" not in dict`.
func runeDefaultConfig() ide.DefaultConfig {
	return ide.StarlarkDefaultConfig(defaultStarlarkConfig, true, true)
}

func resolveInitialThemeAttr(b browser.Browser, rootCfg config.Config) term.Attributes {
	guiCfg, ok, err := getGUIConfig(rootCfg)
	if err != nil {
		_, _ = b.Notify(browserapi.LevelError, "%v", err)
	}
	if !ok {
		guiCfg = config.NopConfig()
	}
	defaultColorTheme := getGUIDefaultColorTheme(b, guiCfg)
	themes := getGUIColorThemes(b, guiCfg)
	t := themes[defaultColorTheme]
	return term.Attributes{
		Fg: term.FromTcellColor(t.Foreground),
		Bg: term.FromTcellColor(t.Background),
	}
}
