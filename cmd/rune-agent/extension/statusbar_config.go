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

package extension

import (
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/extension/extutil"
)

// defaultStatusBarConfig is what a chat gets when the extension config
// carries no status_bar block at all. The layout and the gauge palette
// are left zero so the bar supplies its own shipped defaults.
var defaultStatusBarConfig = dialoguetui.StatusBarConfig{
	Enabled:          true,
	BackgroundColor:  dialoguetui.DefaultStatusBarBackground,
	ForegroundColor:  dialoguetui.DefaultStatusBarForeground,
	ContextGaugeFill: dialoguetui.DefaultContextGaugeFill,
	CacheGaugeFill:   dialoguetui.DefaultCacheGaugeFill,
}

// statusBarConfig reads the status_bar block from the extension
// config. Every key is optional: a missing or malformed value falls
// back to the built-in default so a bad theme never costs the user
// their status bar. hostConfig is the editor's own config, which is
// what names the colour theme the gauge ramps blend through.
func statusBarConfig(
	pconfig, hostConfig config.Config, noti browserapi.Notifications,
) dialoguetui.StatusBarConfig {
	// The ramps are resolved here rather than where they are read so a
	// config with no status_bar block at all still blends through the
	// theme's colours instead of the W3C ones.
	ret := statusBarBlock(pconfig, noti)
	ret.ContextGaugeFill = themedFill(hostConfig, ret.ContextGaugeFill)
	ret.CacheGaugeFill = themedFill(hostConfig, ret.CacheGaugeFill)
	return ret
}

func statusBarBlock(
	pconfig config.Config, noti browserapi.Notifications,
) dialoguetui.StatusBarConfig {
	ret := defaultStatusBarConfig
	sub, err := pconfig.GetConfig("status_bar")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'status_bar' from extension config", "error", err)
		}
		return ret
	}

	if v, err := sub.GetBool("enabled"); err == nil {
		ret.Enabled = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.enabled' from extension config", "error", err)
	}

	if v, err := sub.GetString("layout"); err == nil {
		layout, perr := dialoguetui.ParseStatusBarLayout(v)
		if perr != nil {
			_, _ = noti.Notify(browserapi.LevelWarn,
				"status_bar: invalid layout: %v", perr)
		} else {
			ret.Layout = layout
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.layout' from extension config", "error", err)
	}

	if v, err := sub.GetInt("gauge_width"); err == nil {
		ret.GaugeWidth = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.gauge_width' from extension config", "error", err)
	}

	if v, err := sub.GetString("shader"); err == nil {
		if dialoguetui.ValidStatusBarShader(v) {
			ret.Shader = v
		} else {
			_, _ = noti.Notify(browserapi.LevelWarn,
				"status_bar: unknown shader %q, expected one of %s",
				v, strings.Join(dialoguetui.StatusBarShaderNames(), ", "))
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.shader' from extension config", "error", err)
	}

	if v, err := sub.GetInt("shader_fps"); err == nil {
		ret.ShaderFPS = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.shader_fps' from extension config", "error", err)
	}

	if v, err := sub.GetString("shader_loop"); err == nil {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			_, _ = noti.Notify(browserapi.LevelWarn,
				"status_bar: invalid shader_loop %q: %v", v, perr)
		} else {
			ret.ShaderLoop = d
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.shader_loop' from extension config", "error", err)
	}

	if v, err := config.GetAttributes(sub, "background_attr"); err == nil {
		ret.BackgroundColor = v.Bg
		ret.ForegroundColor = v.Fg
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.background_attr' from extension config", "error", err)
	}

	if v, err := config.GetAttributes(sub, "gauge_empty_attr"); err == nil {
		ret.GaugeEmptyAttr = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.gauge_empty_attr' from extension config", "error", err)
	}

	if v, err := config.GetAttributes(sub, "gauge_cap_attr"); err == nil {
		ret.GaugeCapAttr = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.gauge_cap_attr' from extension config", "error", err)
	}

	if v, err := sub.GetString("gauge_start_rune"); err == nil {
		ret.GaugeStartRune = firstRune(v)
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.gauge_start_rune' from extension config", "error", err)
	}

	if v, err := sub.GetString("gauge_end_rune"); err == nil {
		ret.GaugeEndRune = firstRune(v)
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.gauge_end_rune' from extension config", "error", err)
	}

	ret.ContextGaugeFill = gaugeFill(
		sub, "context_gauge_fill_attrs", ret.ContextGaugeFill)
	ret.CacheGaugeFill = gaugeFill(
		sub, "cache_gauge_fill_attrs", ret.CacheGaugeFill)

	ret.Statuses = statuses(sub)

	return ret
}

// firstRune reads a bracket. An empty string is how a theme drops one,
// which config.GetRune cannot express.
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// themedFill expands a ramp's stops into the hex values the host draws
// them with, since the gauge blends between stops in this process and a
// blend of the W3C defaults would not meet the themed stops it joins.
func themedFill(
	cfg config.Config, stops []term.Attributes,
) []term.Attributes {
	ret := slices.Clone(stops)
	for i := range ret {
		ret[i].Fg = themedColor(cfg, ret[i].Fg)
		ret[i].Bg = themedColor(cfg, ret[i].Bg)
	}
	return ret
}

func themedColor(cfg config.Config, c term.Color) term.Color {
	ret, err := extutil.GetColorRGB(cfg, c)
	if err != nil {
		slog.Warn("resolve status bar gauge colour against theme",
			"error", err)
	}
	return ret
}

// gaugeFill reads the ordered stops one gauge fills through. A theme
// reverses a gauge by listing the same stops backwards rather than by
// naming a direction.
func gaugeFill(
	sub config.Config, key string, def []term.Attributes,
) []term.Attributes {
	raw, err := sub.GetSlice(key)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get status bar gauge fill from extension config",
				"key", "status_bar."+key, "error", err)
		}
		return def
	}
	ret := make([]term.Attributes, 0, len(raw))
	for i, elem := range raw {
		stop, ok := elem.(map[string]any)
		if !ok {
			slog.Warn("status bar gauge fill stop is not a mapping",
				"key", "status_bar."+key, "index", i)
			continue
		}
		v, err := config.GetAttributes(
			config.MapConfig(map[string]any{key: stop}), key)
		if err != nil {
			slog.Warn("get status bar gauge fill stop from extension config",
				"key", "status_bar."+key, "index", i, "error", err)
			continue
		}
		ret = append(ret, v)
	}
	if len(ret) == 0 {
		return def
	}
	return ret
}

// statuses overlays the status_bar.status block on the shipped
// palette, so naming one phase does not drop the colours or the
// animations of the rest. A phase that names only one of the two keeps
// the shipped value of the other.
func statuses(sub config.Config) map[string]dialoguetui.StatusBarStatusConfig {
	statusSub, err := sub.GetConfig("status")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'status_bar.status' from extension config", "error", err)
		}
		return nil
	}
	ret := maps.Clone(dialoguetui.DefaultStatuses)
	statusSub.Iterate(func(k string, _ any) {
		entry, err := statusSub.GetConfig(k)
		if err != nil {
			slog.Warn("get 'status_bar.status' entry from extension config",
				"status", k, "error", err)
			return
		}
		status := ret[k]
		if v, err := config.GetAttributes(entry, "attr"); err == nil {
			status.Attrs = v
		} else if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'status_bar.status' attributes from extension config",
				"status", k, "error", err)
		}
		if v, err := entry.GetConfig("animation"); err == nil {
			status.Animation = animation(v, k)
		} else if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'status_bar.status' animation from extension config",
				"status", k, "error", err)
		}
		ret[k] = status
	})
	return ret
}

// animation reads one status's spinner: the frames it cycles and the
// attributes they override the status's own with.
func animation(sub config.Config, status string) dialoguetui.StatusBarAnimation {
	var ret dialoguetui.StatusBarAnimation
	if v, err := sub.GetString("ch"); err == nil {
		ret.Frames = dialoguetui.SpinnerFrames(v)
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.status' animation frames from extension config",
			"status", status, "error", err)
	}
	if v, err := config.GetAttributes(sub, "attr"); err == nil {
		ret.Attrs = v
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'status_bar.status' animation attributes from extension config",
			"status", status, "error", err)
	}
	return ret
}
