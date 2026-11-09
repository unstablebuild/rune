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

package lspcmd

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/handler/locationpicker"
)

type locationEntry struct {
	uri     string
	rng     semanticapi.Range
	display string
}

var errNoLocations = errors.New("no location returned")

func navigateTo(
	rootURI workspaceapi.URI,
	e locationEntry, opener browserapi.ResourceOpener,
	wm browserapi.WindowManager,
	editor textapi.Editor, notify browserapi.Notifications,
	scheduleNextTick func(func()) bool,
) {
	scheduleNextTick(func() {
		if err := doNavigate(rootURI, e, opener, wm, editor); err != nil {
			_, _ = notify.Notify(browserapi.LevelError, "navigate: %s", err)
			return
		}
	})
}

func doNavigate(
	rootURI workspaceapi.URI,
	e locationEntry, opener browserapi.ResourceOpener,
	wm browserapi.WindowManager, editor textapi.Editor,
) error {
	uri, err := LspToURI(rootURI, e.uri)
	if err != nil {
		return err
	}
	h, err := opener.Open(uri)
	if err != nil {
		return err
	}
	w, err := wm.Focus()
	if err != nil {
		return err
	}
	if err := wm.SetWindowContent(w, h); err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		return err
	}
	eh, err := editor.Editor(uri)
	if err != nil {
		return err
	}
	target := PosToCoord(e.rng.Start)
	if err := editor.SetCursor(eh, target); err != nil {
		if cur, curErr := editor.Cursor(eh); curErr == nil && cur == target {
			return nil
		}
		return err
	}
	return nil
}

func locationsFromResult(r semanticapi.LocationResult) []locationEntry {
	var entries []locationEntry
	if r.Location != nil {
		entries = append(entries, locationFromLoc(*r.Location))
	}
	for _, loc := range r.Locations {
		entries = append(entries, locationFromLoc(loc))
	}
	for _, ll := range r.LocationLinks {
		entries = append(entries, locationEntry{
			uri: ll.TargetURI,
			rng: ll.TargetSelectionRange,
			display: fmt.Sprintf("%s:%d",
				trimFilePrefix(ll.TargetURI), ll.TargetSelectionRange.Start.Line+1),
		})
	}
	return entries
}

func pickerEntries(rootURI workspaceapi.URI, entries []locationEntry) []locationpicker.Entry {
	ret := make([]locationpicker.Entry, len(entries))
	for i, e := range entries {
		uri, err := LspToURI(rootURI, e.uri)
		if err != nil {
			continue
		}
		ret[i] = locationpicker.Entry{
			URI:     uri,
			Range:   e.rng,
			Display: e.display,
		}
	}
	return ret
}

func locationFromLoc(loc semanticapi.Location) locationEntry {
	return locationEntry{
		uri:     loc.URI,
		rng:     loc.Range,
		display: fmt.Sprintf("%s:%d", trimFilePrefix(loc.URI), loc.Range.Start.Line+1),
	}
}

// presentLocations navigates directly when a single entry remains and
// otherwise floats a location picker. It may be called from any goroutine:
// all UI work is scheduled onto the event loop.
func presentLocations(
	name string, rootURI workspaceapi.URI, entries []locationEntry,
	opener browserapi.ResourceOpener, wm browserapi.WindowManager,
	editor textapi.Editor, notify browserapi.Notifications,
	fs workspaceapi.FileSystem, scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser, cfg locationpicker.Config, log *slog.Logger,
) {
	if len(entries) == 1 {
		navigateTo(rootURI, entries[0], opener, wm, editor, notify, scheduleNextTick)
		return
	}
	scheduleNextTick(func() {
		picker := locationpicker.New(
			pickerEntries(rootURI, entries), fs, scheduleNextTick, parser, cfg, log,
		)
		picker.SetOnSelect(func(idx int) {
			navigateTo(rootURI, entries[idx], opener, wm, editor, notify, scheduleNextTick)
		})
		_, err := wm.Floating(picker, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			_, _ = notify.Notify(browserapi.LevelError, "%s: %s", name, err)
		}
	})
}

func trimFilePrefix(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}

func enrichEntries(
	entries []locationEntry, rootURI workspaceapi.URI,
) []locationEntry {
	for i, e := range entries {
		uri, err := LspToURI(rootURI, e.uri)
		var rel string
		if err == nil {
			rel = workspaceapi.RelPath(rootURI, uri)
		} else {
			rel = trimFilePrefix(e.uri)
		}
		entries[i].display = fmt.Sprintf("%s:%d", rel, e.rng.Start.Line+1)
	}
	return entries
}
