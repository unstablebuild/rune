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
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
)

const (
	terminalSessionDocumentKind   = "terminal-session"
	terminalSessionDocumentPrefix = "terminal-sessions:"
)

type terminalSessionDocument struct {
	Kind     string
	Name     string
	Snapshot vte.Snapshot
}

func normalizeTerminalSessionName(args []string) (string, error) {
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		return "", errors.New("expected terminal session name")
	}
	return name, nil
}

func terminalSessionURI(name string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("terminalsession:///" + url.PathEscape(name))
}

func terminalSessionDocumentID(name string) string {
	return terminalSessionDocumentPrefix + url.QueryEscape(name)
}

func (e *ex) terminalSessionStorage() storageapi.Service {
	if e.terminalStorage == nil {
		e.terminalStorage = storageapi.WithPartition(
			e.storage, idehistory.TerminalStatePartition)
	}
	return e.terminalStorage
}

func (e *ex) terminalInFocus() (vtereservoir.VTE, bool) {
	content, err := e.invokeWindow().Content()
	if err != nil {
		return nil, false
	}

	switch h := content.(type) {
	case vtereservoir.VTE:
		return h, true
	case *browser.Tab:
		if s, ok := h.Handler().(vtereservoir.VTE); ok {
			return s, true
		}
	}

	return nil, false
}

func (e *ex) nextTerminalSessionName(ctx context.Context, h vtereservoir.VTE) (string, error) {
	base := "terminal"
	uri := h.URI()
	if uri.Name() != "" {
		base = uri.Name()
	} else if uri.Path() != "" {
		base = strings.Trim(strings.ReplaceAll(uri.Path(), "/", "-"), "-")
	}
	if base == "" {
		base = "terminal"
	}

	for i := 0; ; i++ {
		name := base + "-saved"
		if i != 0 {
			name = fmt.Sprintf("%s-saved-%d", base, i)
		}
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		var doc terminalSessionDocument
		err := e.terminalSessionStorage().Get(ctx, terminalSessionDocumentID(name), &doc)
		cancel()
		if errors.Is(err, storageapi.ErrNotFound) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("check terminal session name %q: %w", name, err)
		}
	}
}

func (e *ex) saveTerminalSession(ctx context.Context, name string, h vtereservoir.VTE) error {
	snapshot, err := h.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot terminal: %w", err)
	}
	if snapshot.Title == "" {
		snapshot.Title = name
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = e.terminalSessionStorage().Set(ctx, terminalSessionDocumentID(name), terminalSessionDocument{
		Kind:     terminalSessionDocumentKind,
		Name:     name,
		Snapshot: snapshot,
	})
	if err != nil {
		return fmt.Errorf("save terminal session %q: %w", name, err)
	}

	_, _ = e.notifications.Notify(browserapi.LevelSuccess,
		"terminal session %q saved", name)
	return nil
}

func (e *ex) terminalsave(ctx context.Context, args ...string) error {
	name, err := normalizeTerminalSessionName(args)
	if err != nil {
		return err
	}
	h, ok := e.terminalInFocus()
	if !ok {
		return errors.New("not a terminal")
	}
	return e.saveTerminalSession(ctx, name, h)
}

func (e *ex) terminalresume(ctx context.Context, args ...string) error {
	name, err := normalizeTerminalSessionName(args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	doc, err := e.loadTerminalSession(ctx, name)
	if err != nil {
		return fmt.Errorf("resume terminal session %q: %w", name, err)
	}
	if doc.Name == "" {
		doc.Name = name
	}

	_, err = e.restoreTerminalSessionTab(doc, e.invokeWindow())
	return err
}

// loadTerminalSession reads a saved session. Sessions saved before they
// had their own partition still live beside the workspace state, where
// every startup listing decodes them; a hit there moves the document.
func (e *ex) loadTerminalSession(
	ctx context.Context, name string,
) (terminalSessionDocument, error) {
	id := terminalSessionDocumentID(name)
	var doc terminalSessionDocument
	err := e.terminalSessionStorage().Get(ctx, id, &doc)
	if !errors.Is(err, storageapi.ErrNotFound) {
		return doc, err
	}
	if err := e.storage.Get(ctx, id, &doc); err != nil {
		return doc, err
	}
	if doc.Kind == "" {
		doc.Kind = terminalSessionDocumentKind
	}
	if err := e.terminalSessionStorage().Set(ctx, id, doc); err == nil {
		_ = e.storage.Delete(ctx, id)
	}
	return doc, nil
}

func (e *ex) newTerminalSessionHandler(
	name string, snapshot vte.Snapshot,
) (vtereservoir.VTE, error) {
	h, err := e.newEmulatorHandler(nil)
	if err != nil {
		return nil, err
	}
	if err := h.RestoreFromSnapshot(snapshot); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("restore terminal session %q: %w", name, err)
	}
	return h, nil
}

func (e *ex) restoreTerminalSessionTab(
	doc terminalSessionDocument, win browser.Window,
) (*browser.Tab, error) {
	if doc.Name == "" {
		return nil, errors.New("terminal session document missing name")
	}

	uri, err := terminalSessionURI(doc.Name)
	if err != nil {
		return nil, err
	}
	if existing, ok := e.comp.Resource(uri); ok {
		if win != nil {
			if err := win.SetContent(existing); err != nil {
				return nil, err
			}
		}
		tab, ok := existing.(*browser.Tab)
		if !ok {
			return nil, fmt.Errorf("terminal session %q resource is not a tab", doc.Name)
		}
		return tab, nil
	}

	h, err := e.newTerminalSessionHandler(doc.Name, doc.Snapshot)
	if err != nil {
		return nil, err
	}
	title := doc.Name
	if doc.Snapshot.Title != "" {
		title = doc.Snapshot.Title
	}
	t, err := e.comp.Tab(uri, e.config.Icons.Terminal, title, h)
	if err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("wm.Tab: %w", err)
	}
	// Route dynamic title updates issued under the handler's own URI
	// to this session-keyed tab.
	e.tabAliases.addAlias(h.URI(), uri)
	tab := t.(*browser.Tab)
	tab.Subscribe((*tabSubscriber)(e))
	if win != nil {
		if err := win.SetContent(t); err != nil {
			_ = t.Close()
			return nil, err
		}
	}
	return tab, nil
}

func (e *ex) completeTerminalSessions(ctx context.Context, _ textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	it, err := e.terminalSessionStorage().List(ctx, []storageapi.Filter{{
		Field: storageapi.Field{FieldPath: []string{"Kind"}, Value: terminalSessionDocumentKind},
		Op:    storageapi.OpEqual,
	}})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()

	var names []string
	for it.HasNext() {
		var doc terminalSessionDocument
		if err := it.NextTo(&doc); err != nil {
			return nil, "", err
		}
		if doc.Name != "" {
			names = append(names, doc.Name)
		}
	}
	sort.Strings(names)
	return iterator.FromSlice(names), "", nil
}
