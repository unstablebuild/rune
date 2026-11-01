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
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/term/programstatus"
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
			HoverAttr:      term.Attributes{Bg: term.ColorSilver, Fg: term.ColorWhite},
			ClickedAttr:    term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorWhite},
			BackgroundAttr: term.Attributes{},
			MinWidth:       60,
		},
		Notifications: logNotifications{},
		TabStatus:     DefaultTabStatusConfig(),
	}
}

// DefaultTabStatusConfig returns the default TabStatusConfig. Its icons
// are Nerd Font glyphs.
func DefaultTabStatusConfig() TabStatusConfig {
	return TabStatusConfig{
		Spinner:    []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"),
		SpinnerFPS: 10,
		BlockedIcons: map[programstatus.Kind]rune{
			programstatus.KindNone:       '󰹇',
			programstatus.KindPermission: '󰹇',
			programstatus.KindQuestion:   '󰋗',
			programstatus.KindAuth:       '󰗒',
		},
		DoneIcon:  '󰗠',
		ErrorIcon: '󰅙',
		ContentTab: TabStatusStyle{
			IdleIconAttr:    term.Attributes{Fg: term.ColorGray},
			WorkingIconAttr: term.Attributes{Fg: term.ColorAqua},
			WorkingDoneAttr: term.Attributes{Fg: term.ColorAqua},
			BlockedAttr:     term.Attributes{Fg: term.ColorYellow},
			BlockedIconAttr: term.Attributes{Fg: term.ColorYellow},
			DoneAttr:        term.Attributes{Fg: term.ColorGreen},
			DoneIconAttr:    term.Attributes{Fg: term.ColorGreen},
			ErrorAttr:       term.Attributes{Fg: term.ColorRed},
			ErrorIconAttr:   term.Attributes{Fg: term.ColorRed},
		},
		WorkspaceTab: TabStatusStyle{
			WorkingDoneAttr: term.Attributes{Fg: term.ColorAqua},
			BlockedAttr:     term.Attributes{Fg: term.ColorYellow},
			DoneAttr:        term.Attributes{Fg: term.ColorGreen},
			ErrorAttr:       term.Attributes{Fg: term.ColorRed},
		},
	}
}

// TabStatusConfig styles the decorations a Component draws on a tab to
// reflect the status its program reported via Component.SetTabStatus,
// and those the workspace bar draws from the most pressing status among
// the tabs of each workspace.
type TabStatusConfig struct {
	// Spinner holds the frames cycled, at SpinnerFPS frames per second,
	// as the icon of working tabs. Empty leaves the icon alone; a zero
	// SpinnerFPS falls back to 10.
	Spinner    []rune
	SpinnerFPS int
	// BlockedIcons maps the kind of a blocked status to the icon shown
	// for it. programstatus.KindNone is the fallback for kinds without
	// an entry; a missing fallback leaves the icon alone.
	BlockedIcons map[programstatus.Kind]rune
	// DoneIcon and ErrorIcon replace the icon of done and error tabs.
	// Zero leaves the icon alone.
	DoneIcon  rune
	ErrorIcon rune

	// ContentTab styles the tabs of a Component. WorkspaceTab styles
	// the tabs of the workspace bar, which a Component never draws.
	ContentTab   TabStatusStyle
	WorkspaceTab TabStatusStyle
}

// TabStatusStyle styles one kind of tab from its status. Every
// attribute is layered over the tab's usual one, so colors left at
// term.ColorDefault keep the configured ones, and a zero attribute
// leaves the tab's own alone.
type TabStatusStyle struct {
	// Progress enables a completed prefix on working tab labels. When
	// false, activity animation covers the full label and reported
	// progress remains available in the tab's status.
	Progress bool

	// IdleIconAttr styles the icon of a tab whose program reported
	// idle, i.e. that it waits on the user; a tab that reported
	// nothing is left alone.
	IdleIconAttr term.Attributes
	// WorkingIconAttr styles the spinner. WorkingDoneAttr paints the
	// completed prefix of the name of a working tab that reported
	// progress, when Progress is enabled.
	WorkingIconAttr term.Attributes
	WorkingDoneAttr term.Attributes
	// BlockedAttr, DoneAttr and ErrorAttr style the name of a tab in the
	// respective state, and their *IconAttr counterparts its icon.
	BlockedAttr     term.Attributes
	BlockedIconAttr term.Attributes
	DoneAttr        term.Attributes
	DoneIconAttr    term.Attributes
	ErrorAttr       term.Attributes
	ErrorIconAttr   term.Attributes
}

// BarAttrs returns the name and icon attributes that reflect status:
// the pair configured for its state, only the icon one for working
// and for a reported idle, and zero when nothing was reported.
func (c TabStatusStyle) BarAttrs(status programstatus.Summary) (name, icon term.Attributes) {
	switch status.State {
	case programstatus.Idle:
		if len(status.Records) > 0 {
			return term.Attributes{}, c.IdleIconAttr
		}
	case programstatus.Working:
		return term.Attributes{}, c.WorkingIconAttr
	case programstatus.Blocked:
		return c.BlockedAttr, c.BlockedIconAttr
	case programstatus.Done:
		return c.DoneAttr, c.DoneIconAttr
	case programstatus.Error:
		return c.ErrorAttr, c.ErrorIconAttr
	}
	return term.Attributes{}, term.Attributes{}
}

// PromptConfig holds configuration for the browser's Prompt component.
type PromptConfig struct {
	TextAttr      term.Attributes
	HighlightAttr term.Attributes
	// HoverAttr styles the tutorial tile's buttons under the mouse
	// pointer.
	HoverAttr term.Attributes
	// ClickedAttr styles a button from the mouse press until the
	// release that selects it.
	ClickedAttr    term.Attributes
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
	// tabs whose program reported working via Component.SetTabStatus.
	// Empty disables it. ActiveTabShaderFPS and ActiveTabShaderLoop tune
	// the effect and fall back to the catalog defaults when zero.
	// ActiveTabShaderParams tunes the colour and strength of the effects
	// that blend text toward a colour.
	ActiveTabShader       string
	ActiveTabShaderFPS    int
	ActiveTabShaderLoop   time.Duration
	ActiveTabShaderParams shaderloop.Params
	// OnTabStatus is called whenever a tab's status changes, with the
	// status it had before and the one it has now. A tab removed with a
	// status reports a change to idle.
	OnTabStatus func(uri workspaceapi.URI, prev, status programstatus.Summary)
	// OnTabRemoved is called once a tab is removed, before OnTabStatus
	// reports the loss of its status, if it had one, so whoever lists
	// tabs by status can drop the tab rather than see it go idle first.
	// The tabs closed along with the Component are not reported.
	OnTabRemoved func(uri workspaceapi.URI)
	// OnContentSeen is called on the event loop each time the user
	// comes to see the focus window: when that window gains focus, or
	// when the Component regains focus. It receives the window's
	// content or, for a window showing a tab, the tab's handler. What
	// the program behind that content reported has been seen.
	OnContentSeen func(content browserapi.Handler)
	// TabStatus styles the decorations that reflect a tab's status.
	TabStatus TabStatusConfig

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
	// WindowManagerConfig's Interrupter also animates the tab status
	// spinner, the ActiveTabShader and the drop-target veil. Without
	// one they hold still and only repaint when the host redraws for
	// another reason.
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
