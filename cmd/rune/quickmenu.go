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
	"context"
	"errors"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/quickmenu"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/gui/glassbar"
	"unstable.build/rune/internal/text"
)

const (
	// quickMenuColumnCells is the width of the grid column reserved for
	// the quick menu, and quickMenuTopCell the row it starts at, which is
	// right below the tab bar.
	quickMenuColumnCells = 5
	quickMenuTopCell     = 3

	cmdQuickMenu = "quickmenu"

	// quickMenuImagesPartition caches the images of the quick menu's
	// sprites.
	quickMenuImagesPartition = "quick_menu_images"

	// quickMenuTooltipDelay matches the hover delay of desktop toolkits'
	// tooltips.
	quickMenuTooltipDelay = 500 * time.Millisecond
)

func quickMenuGlassButtons(buttons []ide.QuickMenuButton) []glassbar.Button {
	ret := make([]glassbar.Button, 0, len(buttons))
	for _, button := range buttons {
		ret = append(ret, glassbar.Button{
			ID: button.ID(), Symbol: button.Symbol, Tooltip: button.Title,
		})
	}
	return ret
}

// readQuickMenu reads the quick menu configured at configPath and the
// sounds its Rune-drawn bar plays. It loads the config standalone
// because the reserved grid column must be known before ide.New. The
// first-run presets never set the quick menu, so the config the
// bootstrap wizard writes leaves it as it was. A validation error still
// yields a usable tree with the offending entries neutralised, and the
// IDE reports it to the user, so the valid buttons are kept rather than
// dropping the whole bar.
func readQuickMenu(configPath string) ([]ide.QuickMenuButton, quickmenu.Sounds) {
	cfg, err := ide.Config(configPath, runeDefaultConfig())
	if err != nil {
		log.Warnf("load config for quick menu: %v", err)
	}
	hover, click := ide.QuickMenuSounds(cfg)
	return ide.QuickMenuButtons(cfg), quickmenu.Sounds{Hover: hover, Click: click}
}

// newQuickMenuBar returns the bar Rune draws for menu, or nil when the
// native bar draws it instead, which is when its buttons have no
// sprites. The config allows sprites on every button or on none.
// player plays sounds by name, and activate is called with the ID of the
// clicked button.
func newQuickMenuBar(
	menu []ide.QuickMenuButton, soundNames quickmenu.Sounds,
	storage storageapi.Service, publishEvent func(term.Event) bool,
	player sounds.Player, activate func(id string),
) *quickmenu.Bar {
	if len(menu) == 0 || !menu[0].HasSprite() {
		return nil
	}
	buttons := make([]quickmenu.Button, len(menu))
	for i, button := range menu {
		buttons[i] = quickmenu.Button{
			Title:   button.Title,
			Idle:    button.Sprite.Idle,
			Hover:   button.Sprite.Hover,
			Pressed: button.Sprite.Pressed,
		}
	}
	return quickmenu.New(quickmenu.Config{
		Buttons:      buttons,
		Columns:      quickMenuColumnCells,
		Top:          quickMenuTopCell,
		Storage:      storageapi.WithPartition(storage, quickMenuImagesPartition),
		Interrupter:  redrawInterrupter(publishEvent),
		TooltipDelay: quickMenuTooltipDelay,
		Player:       player,
		Sounds:       soundNames,
		Activate:     func(i int) { activate(menu[i].ID()) },
	})
}

// playSound plays name through the current IDE, whose sound registry
// follows the user config across reloads. It plays nothing before an
// IDE exists.
func (b *bootstrapHandler) playSound(name string) {
	if i := b.currentIDE(); i != nil {
		i.Sounds().Play(name)
	}
}

// visibleQuickMenuBar returns the quick menu Rune draws while it is on
// screen, or nil.
func (b *bootstrapHandler) visibleQuickMenuBar() *quickmenu.Bar {
	if b.quickMenuBar == nil || !b.quickMenuVisible() {
		return nil
	}
	return b.quickMenuBar
}

// handleQuickMenuMouse offers ev to bar before the IDE, except for a
// motion, which the IDE sees first: the bar never consumes one, and
// handling it last makes the bar's pointer shape win.
func (b *bootstrapHandler) handleQuickMenuMouse(
	bar *quickmenu.Bar, ev term.Event,
) (exit, handled bool) {
	if ev.Key == 0 {
		exit, handled = b.inner.Handle(ev)
		bar.Handle(ev)
		return exit, handled
	}
	if _, handled := bar.Handle(ev); handled {
		return false, true
	}
	return b.inner.Handle(ev)
}

// redrawInterrupter wakes the event loop for a redraw through publish.
type redrawInterrupter func(term.Event) bool

// Interrupt satisfies term.Interrupter.
func (publish redrawInterrupter) Interrupt(context.Context) error {
	publish(term.Event{Type: term.EventInterrupt})
	return nil
}

// quickMenuAvailable reports whether the quick menu can be shown at all:
// the user configured buttons for it, and either Rune draws them or the
// platform does.
func (b *bootstrapHandler) quickMenuAvailable() bool {
	return len(b.quickMenu) > 0 && (b.quickMenuBar != nil || glassbar.Supported())
}

// quickMenuVisible reports whether the bar is currently on screen.
func (b *bootstrapHandler) quickMenuVisible() bool {
	return b.quickMenuAvailable() && !b.quickMenuOff
}

// quickMenuCells is the reserved column width, zero while the bar is
// hidden so the editor gets the space back.
func (b *bootstrapHandler) quickMenuCells() int {
	if !b.quickMenuVisible() {
		return 0
	}
	return quickMenuColumnCells
}

// quickMenuButtons are the buttons to install in the native bar, empty
// while it is hidden or Rune draws the menu, so installing clears it.
func (b *bootstrapHandler) quickMenuButtons() []ide.QuickMenuButton {
	if !b.quickMenuVisible() || b.quickMenuBar != nil {
		return nil
	}
	return b.quickMenu
}

// setQuickMenuVisible shows or hides the bar and gives the reserved
// column back to the editor, republishing the app menu so its Quick
// Menu checkmark follows. It must run on the event loop.
func (b *bootstrapHandler) setQuickMenuVisible(visible bool) {
	if !b.quickMenuAvailable() || visible == b.quickMenuVisible() {
		return
	}
	b.quickMenuOff = !visible
	if i := b.currentIDE(); i != nil {
		i.SetRightInset(b.quickMenuCells())
	}
	b.publishQuickMenuInstall()
	b.publishAppMenuInstall()
}

// subscribeQuickMenuCommand registers the toggle on i. The command only
// exists where a quick menu can be drawn, so a config that binds it
// elsewhere fails loudly rather than silently doing nothing.
func (b *bootstrapHandler) subscribeQuickMenuCommand(i *ide.IDE) error {
	if !glassbar.Supported() && b.quickMenuBar == nil {
		return nil
	}
	man := textapi.CommandManual{
		Name: cmdQuickMenu,
		Summary: "Shows or hides the quick menu, the vertical bar of " +
			"buttons along the right edge of the window. Hiding it gives the " +
			"column it reserves back to the editor.",
		Synopsis: "[on|off]",
	}
	handler := func(_ context.Context, cmd textapi.Command) error {
		visible := !b.quickMenuVisible()
		if len(cmd.Args) > 0 {
			switch cmd.Args[0] {
			case "on":
				visible = true
			case "off":
				visible = false
			default:
				return fmt.Errorf("expected 'on' or 'off' but found %q", cmd.Args[0])
			}
		}
		if !b.quickMenuAvailable() {
			return errors.New("no quick menu buttons are configured; " +
				"set gui.quick_menu in your configuration")
		}
		b.setQuickMenuVisible(visible)
		return nil
	}
	return i.SubscribeCommand(man, text.FuncCommandHandler(handler, nil))
}
