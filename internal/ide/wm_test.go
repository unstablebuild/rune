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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestCurrentWorkspaceWindowManagerPrompt(t *testing.T) {
	m, _ := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	m.mu.Lock()
	defer m.mu.Unlock()

	wm := currentWorkspaceWindowManager{root: m.workspaceManagerHandler}
	b := m.focusEx().comp.Browser()
	floats := b.FloatingWindows()
	selected := -1
	h := handler.FuncPromptHandler(func(i int, _ string) { selected = i },
		func() error { return nil })
	cfg := browserapi.PromptConfig{Message: "Proceed?", Options: []string{"Yes", "No"}}

	_, err := wm.Prompt(browserapi.PromptConfig{Message: "Proceed?"}, h)
	require.Error(t, err, "a prompt without options")
	assert.Equal(t, floats, b.FloatingWindows())

	win, err := wm.Prompt(cfg, h)
	require.NoError(t, err)
	assert.Equal(t, floats+1, b.FloatingWindows(), "the prompt opens in the focused workspace")
	again, err := wm.Prompt(cfg, handler.NopPromptHandler())
	require.NoError(t, err)
	assert.Equal(t, win.WindowID(), again.WindowID(), "the open prompt is returned")
	assert.Equal(t, floats+1, b.FloatingWindows())

	content, err := win.(interface {
		Content() (browserapi.Handler, error)
	}).Content()
	require.NoError(t, err)
	content.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.Equal(t, 0, selected)
}
