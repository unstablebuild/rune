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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

// externalEditorStub is a minimal text.Editor that reports
// IsExternal()==true.
type externalEditorStub struct{}

func (externalEditorStub) IsExternal() bool { return true }

func (externalEditorStub) Edit(
	context.Context, workspaceapi.URI, *cell.Buffer, bool, bool,
) (text.Handler, error) {
	return nil, errors.New("not used")
}

func (externalEditorStub) SubscribeCommand(textapi.CommandManual, text.CommandHandler) error {
	return errors.New("not supported")
}

func (externalEditorStub) RegisterREPLCommand(textapi.CommandManual, textapi.REPLHandler) error {
	return errors.New("not supported")
}

func (externalEditorStub) REPLCommands() []textapi.CommandManual { return nil }

func (externalEditorStub) UnsubscribeCommand(string) error { return errors.New("not supported") }

func (externalEditorStub) UnregisterREPLCommand(string) error {
	return errors.New("not supported")
}

func (externalEditorStub) RegisterResourceOpener(string, textapi.ResourceOpenHandler) error {
	return errors.New("not supported")
}

func (externalEditorStub) UnregisterResourceOpener(string) error {
	return errors.New("not supported")
}

func (externalEditorStub) Editor(workspaceapi.URI) (text.Handler, error) {
	return nil, errors.New("not supported")
}

func (externalEditorStub) SubscribeEvents([]textapi.EventType, text.EventHandler) error {
	return nil
}

func (externalEditorStub) UnsubscribeEvents(text.EventHandler) (bool, error) {
	return false, nil
}

// TestSubscribeAllEventsSkipsAutoSaveForExternalEditor exercises the
// wiring guard in subscribeAllEvents: when the workspace's editor is
// externally managed, the autoSaver must not be constructed even when
// editor.auto_save is true. Otherwise exo-driven external saves race
// the autoSaver and surface noisy ErrStaleData warnings.
func TestSubscribeAllEventsSkipsAutoSaveForExternalEditor(t *testing.T) {
	prev := autoSaverFactory
	t.Cleanup(func() { autoSaverFactory = prev })
	called := false
	autoSaverFactory = func(
		_ autoSaverFlusher, _ browserapi.Notifications,
		_ func(func()) bool, _ time.Duration,
	) *autoSaver {
		called = true
		return nil
	}

	cfg := ideConfig{cfg: map[string]any{
		"editor": map[string]any{
			"mode":      "exo",
			"auto_save": true,
		},
	}, errors: map[string]error{}}

	h := &workspaceManagerHandler{}
	ex := &ex{ed: externalEditorStub{}}

	require.NoError(t, h.subscribeAllEvents(cfg, ex))
	assert.False(t, called,
		"autoSaverFactory must not be invoked for an external editor")
}
