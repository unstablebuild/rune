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

package standard

import (
	"errors"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/searchbox"
	"unstable.build/rune/internal/text"
)

// searchHandler routes the standard editor's find and replace keys to a
// floating searchbox.Box.
type searchHandler struct {
	text.Handler
	box *searchbox.Box
}

func newSearchHandler(
	inner text.Handler, controller *standardHandler, cfg SearchConfig,
) *searchHandler {
	boxConfig := searchbox.Config{
		WindowManager: cfg.WindowManager,
		Editor:        Editor(WithKeymap(controller.cfg.keymap)),
		Title:         "Find / Replace",
		FindKey:       cfg.FindKey,
		ReplaceKey:    cfg.ReplaceKey,
		// ctrl-f is a legacy alias of the find key in text editors; it must
		// not be aliased in terminals, where it belongs to the shell.
		FindKeyAliases:  []term.KeyComb{{Mod: term.ModCtrl, Ch: 'f'}},
		PaddingTop:      1,
		Attr:            cfg.Attr,
		InputAttr:       cfg.InputAttr,
		PlaceholderAttr: cfg.PlaceholderAttr,
		FrameAttr:       cfg.FrameAttr,
		FocusFrameAttr:  cfg.FocusFrameAttr,
		ButtonAttr:      cfg.ButtonAttr,
		ButtonHoverAttr: cfg.ButtonHoverAttr,
		OnOpenError: func(mode searchbox.Mode, err error) {
			what := "find"
			if mode == searchbox.ModeReplace {
				what = "replace"
			}
			controller.log(log.WarnLevel, "open floating %s: %v", what, err)
			_, _ = controller.cfg.notifications.Notify(browserapi.LevelWarn,
				"Unable to open floating %s: %v", what, err)
			if mode == searchbox.ModeFind {
				controller.startFind()
			}
		},
	}
	return &searchHandler{Handler: inner, box: searchbox.New(controller, boxConfig)}
}

func (h *searchHandler) Handle(ev term.Event) (bool, bool) {
	if h.box.HandleKey(ev) {
		return false, true
	}
	return h.Handler.Handle(ev)
}

func (h *searchHandler) Close() error {
	return errors.Join(h.box.Close(), h.Handler.Close())
}
