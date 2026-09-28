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

package browser

import (
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/handler"
)

// DefaultConfig returns the default Config.
func DefaultConfig() Config {
	return Config{
		Wallpaper:             NopWallpaper(),
		FocusTabAttr:          term.Attributes{Fg: term.ColorWhite},
		NonFocusTabAttr:       term.Attributes{Fg: term.ColorRed},
		FocusTabIconAttr:      term.Attributes{},
		NonFocusTabIconAttr:   term.Attributes{},
		FocusTabHighlightAttr: term.Attributes{Fg: term.ColorYellow},
		FocusTabHighlightChar: '━',
		FrameUnionCharSet:     component.DefaultFrameUnionCharSet(),
		WindowManagerConfig:   handler.DefaultWindowManagerConfig(),
		FrameUnion:            handler.DefaultWindowManagerConfig().Frame,
		PromptConfig: PromptConfig{
			TextAttr:       term.Attributes{},
			HighlightAttr:  term.Attributes{Bg: term.ColorRed, Fg: term.ColorWhite},
			BackgroundAttr: term.Attributes{},
			MinWidth:       60,
		},
		Notifications: logNotifications{},
	}
}

// PromptConfig holds configuration for the browser's Prompt component.
type PromptConfig struct {
	TextAttr       term.Attributes
	HighlightAttr  term.Attributes
	BackgroundAttr term.Attributes
	MinWidth       int
}

// Wallpaper is a tui.Component wallpaper factory.
// NOTE: we might want to move it to the component package
// and call it a Factory.
type Wallpaper struct {
	BackgroundAttr term.Attributes
	NewComponent   func() tui.Component
}

// NopWallpaper is a Wallpaper of component.Nop.
func NopWallpaper() Wallpaper {
	return Wallpaper{
		NewComponent: func() tui.Component {
			return component.Nop()
		},
	}
}

// Config holds configuration for an browser.Component.
type Config struct {
	Notifications
	Wallpaper Wallpaper

	FocusTabAttr          term.Attributes
	NonFocusTabAttr       term.Attributes
	FocusTabIconAttr      term.Attributes
	NonFocusTabIconAttr   term.Attributes
	FocusTabHighlightAttr term.Attributes
	FocusTabHighlightChar rune
	// TabOverrideIcon, when non-zero, forces every tab icon
	// rendered by this Component to this rune, regardless of the
	// icon passed to NewTab by callers.
	TabOverrideIcon  rune
	TabBarOffset     int
	TabBarHeight     int
	TabNameSeparator string
	FrameUnion       bool
	OnTabsClick      func(int) bool
	// OnTabIconClick, when set, receives clicks on a tab's icon in the
	// tab bar in place of the usual tab click, so the tab is neither
	// focused nor shown. A press dragged off the icon is not a click.
	OnTabIconClick func(*Tab)

	// ActiveTabShader names the continuous effect run over the labels of
	// tabs marked active via Component.SetTabActivity. Empty disables
	// it. ActiveTabShaderFPS and ActiveTabShaderLoop tune the effect and
	// fall back to the catalog defaults when zero.
	ActiveTabShader     string
	ActiveTabShaderFPS  int
	ActiveTabShaderLoop time.Duration
	// OnTabActivity is called whenever a tab's activity changes,
	// including when an active tab is removed.
	OnTabActivity func()

	// RightInset reserves a column of this width in cells to the right
	// of the window manager, leaving room for another UI element to
	// float over it. The tab bar still spans the full width, so the
	// reserved column starts right below it.
	RightInset int

	// DropTargetAttr styles the veil drawn over the window under the
	// cursor while files are dragged over this browser.
	DropTargetAttr term.Attributes
	// DropTargetLabelAttr styles the message centered in the
	// drop-target veil. The veil's own background is kept.
	DropTargetLabelAttr term.Attributes
	// DropTargetLabels maps a tab URI scheme to the message centered in
	// the drop-target veil. The empty key is the fallback for windows
	// whose scheme has no entry.
	DropTargetLabels map[string]string

	PromptConfig

	component.FrameUnionCharSet
	handler.WindowManagerConfig
}

type logNotifications struct {
}

func (n logNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	var l log.Level
	switch level {
	case browserapi.LevelWarn:
		l = log.WarnLevel
	case browserapi.LevelError:
		l = log.ErrorLevel
	case browserapi.LevelInfo:
		l = log.InfoLevel
	case browserapi.LevelSuccess:
		l = log.InfoLevel
	}
	log.WithField(logging.KeyClass, "notifications").Logf(l, msg, args...)
	return "", nil
}

func (n logNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n logNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}
