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

package exofallback

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

// stubEditor is a minimal text.Editor used to observe which child the
// router dispatched to.
type stubEditor struct {
	name string

	editURIs     []string
	editorURIs   []string
	subEvts      int
	unsubCalls   int
	cmdRegs      int
	cmdUnregs    int
	replRegs     int
	replUnregs   int
	openerRegs   int
	openerUnregs int

	editErr     error
	subErr      error
	unsubResult bool
	unsubErr    error
}

func (s *stubEditor) Edit(
	_ context.Context, uri workspaceapi.URI, _ *cell.Buffer, _, _ bool,
) (text.Handler, error) {
	s.editURIs = append(s.editURIs, uri.String())
	if s.editErr != nil {
		return nil, s.editErr
	}
	return stubHandler{name: s.name}, nil
}

func (s *stubEditor) Editor(uri workspaceapi.URI) (text.Handler, error) {
	s.editorURIs = append(s.editorURIs, uri.String())
	return stubHandler{name: s.name}, nil
}

func (s *stubEditor) SubscribeCommand(
	textapi.CommandManual, text.CommandHandler,
) error {
	s.cmdRegs++
	return nil
}

func (s *stubEditor) RegisterREPLCommand(
	textapi.CommandManual, textapi.REPLHandler,
) error {
	s.replRegs++
	return nil
}

func (s *stubEditor) UnsubscribeCommand(string) error {
	s.cmdUnregs++
	return nil
}

func (s *stubEditor) UnregisterREPLCommand(string) error {
	s.replUnregs++
	return nil
}

func (s *stubEditor) RegisterResourceOpener(
	string, textapi.ResourceOpenHandler,
) error {
	s.openerRegs++
	return nil
}

func (s *stubEditor) UnregisterResourceOpener(string) error {
	s.openerUnregs++
	return nil
}

func (s *stubEditor) SubscribeEvents(
	[]textapi.EventType, text.EventHandler,
) error {
	s.subEvts++
	return s.subErr
}

func (s *stubEditor) UnsubscribeEvents(text.EventHandler) (bool, error) {
	s.unsubCalls++
	return s.unsubResult, s.unsubErr
}

// IsExternal: stubs default to false. Tests that need true can use
// the exoEd argument of newWithEditors; the wrapper's IsExternal()
// is the property under test, not the children's.
func (s *stubEditor) IsExternal() bool { return false }

// stubHandler is a no-op text.Handler used only to verify identity.
type stubHandler struct {
	name string
	text.Handler
}

func mustURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return uri
}

// TestNewPanicsOnNilFallback verifies the constructor refuses a
// nil fallback. (exo-side missing arguments panic inside exoeditor.New
// and are exercised by text/exoeditor's own tests.)
func TestNewPanicsOnNilFallback(t *testing.T) {
	assert.Panics(t, func() {
		_ = newWithEditors(&stubEditor{}, nil)
	})
}

// TestIsExternalAlwaysTrue asserts the router always reports
// IsExternal()==true regardless of its children.
func TestIsExternalAlwaysTrue(t *testing.T) {
	r := newWithEditors(&stubEditor{}, &stubEditor{})
	assert.True(t, r.IsExternal())
}

// TestEditRouting drives Edit on a variety of URIs and asserts the
// router dispatched to the right child.
func TestEditRouting(t *testing.T) {
	cases := []struct {
		name   string
		uri    string
		expect string // "exo" or "fallback"
	}{
		{"file", "file:///tmp/a.go", "exo"},
		{"ssh", "ssh://host/tmp/a.go", "exo"},
		{"memory", "memory:///fexplorer", "fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exoEd := &stubEditor{name: "exo"}
			fallback := &stubEditor{name: "fallback"}
			r := newWithEditors(exoEd, fallback)
			uri := mustURI(t, tc.uri)

			h, err := r.Edit(context.Background(), uri, cell.NewBuffer(), false, false)
			require.NoError(t, err)
			sh, ok := h.(stubHandler)
			require.True(t, ok)
			assert.Equal(t, tc.expect, sh.name)
		})
	}
}

// TestEditErrorNotCached verifies a failed Edit does not record a
// route so a subsequent Editor() lookup re-dispatches by scheme.
func TestEditErrorNotCached(t *testing.T) {
	exoEd := &stubEditor{name: "exo", editErr: errors.New("boom")}
	fallback := &stubEditor{name: "fallback"}
	r := newWithEditors(exoEd, fallback)
	uri := mustURI(t, "file:///x")

	_, err := r.Edit(context.Background(), uri, cell.NewBuffer(), false, false)
	require.Error(t, err)

	_, err = r.Editor(uri)
	require.NoError(t, err)
	assert.Equal(t, []string{"file:///x"}, exoEd.editorURIs)
	assert.Empty(t, fallback.editorURIs)
}

// TestEditorLookupUsesRecordedRoute proves a successful Edit makes
// subsequent Editor() lookups hit the same child even if accepts()
// would have picked differently.
func TestEditorLookupUsesRecordedRoute(t *testing.T) {
	exoEd := &stubEditor{name: "exo"}
	fallback := &stubEditor{name: "fallback"}
	r := newWithEditors(exoEd, fallback)

	uri := mustURI(t, "memory:///fexplorer")
	_, err := r.Edit(context.Background(), uri, cell.NewBuffer(), false, false)
	require.NoError(t, err)

	_, err = r.Editor(uri)
	require.NoError(t, err)
	assert.Empty(t, exoEd.editorURIs)
	assert.Equal(t, []string{"memory:///fexplorer"}, fallback.editorURIs)
}

// TestEditorLookupUnseenURIUsesScheme verifies the accepts() rule
// applies when no Edit has been called for the URI yet.
func TestEditorLookupUnseenURIUsesScheme(t *testing.T) {
	exoEd := &stubEditor{name: "exo"}
	fallback := &stubEditor{name: "fallback"}
	r := newWithEditors(exoEd, fallback)

	_, err := r.Editor(mustURI(t, "file:///never-edited"))
	require.NoError(t, err)
	assert.Equal(t, []string{"file:///never-edited"}, exoEd.editorURIs)

	_, err = r.Editor(mustURI(t, "memory:///never-edited"))
	require.NoError(t, err)
	assert.Equal(t, []string{"memory:///never-edited"}, fallback.editorURIs)
}

// TestSubscribeAndUnsubscribeForwardToExo verifies SubscribeEvents
// and UnsubscribeEvents forward only to the exo child. The fallback
// only serves IDE-owned pseudo-URIs (memory://) whose events the IDE
// does not react to.
func TestSubscribeAndUnsubscribeForwardToExo(t *testing.T) {
	exoEd := &stubEditor{unsubResult: true}
	fallback := &stubEditor{unsubResult: true}
	r := newWithEditors(exoEd, fallback)

	h := text.FuncEventHandler(
		func(context.Context, textapi.Event) bool { return false })
	require.NoError(t, r.SubscribeEvents(nil, h))
	assert.Equal(t, 1, exoEd.subEvts)
	assert.Equal(t, 0, fallback.subEvts)

	ok, err := r.UnsubscribeEvents(h)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 1, exoEd.unsubCalls)
	assert.Equal(t, 0, fallback.unsubCalls)
}

// TestCommandAndREPLForwardToFallback proves command, REPL and resource
// opener register/unregister calls hit only the fallback (exo returns
// "not supported" for these by design).
func TestCommandAndREPLForwardToFallback(t *testing.T) {
	exoEd := &stubEditor{}
	fallback := &stubEditor{}
	r := newWithEditors(exoEd, fallback)

	require.NoError(t, r.SubscribeCommand(textapi.CommandManual{}, nil))
	require.NoError(t, r.RegisterREPLCommand(textapi.CommandManual{}, nil))
	require.NoError(t, r.UnsubscribeCommand(""))
	require.NoError(t, r.UnregisterREPLCommand(""))
	require.NoError(t, r.RegisterResourceOpener("", nil))
	require.NoError(t, r.UnregisterResourceOpener(""))

	assert.Equal(t, 0, exoEd.cmdRegs)
	assert.Equal(t, 0, exoEd.replRegs)
	assert.Equal(t, 0, exoEd.cmdUnregs)
	assert.Equal(t, 0, exoEd.replUnregs)
	assert.Equal(t, 0, exoEd.openerRegs)
	assert.Equal(t, 0, exoEd.openerUnregs)
	assert.Equal(t, 1, fallback.cmdRegs)
	assert.Equal(t, 1, fallback.replRegs)
	assert.Equal(t, 1, fallback.cmdUnregs)
	assert.Equal(t, 1, fallback.replUnregs)
	assert.Equal(t, 1, fallback.openerRegs)
	assert.Equal(t, 1, fallback.openerUnregs)
}
