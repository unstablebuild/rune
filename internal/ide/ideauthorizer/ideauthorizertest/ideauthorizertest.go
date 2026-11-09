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

// Package ideauthorizertest builds command authorizers the way the IDE
// does, so that tests can check what they approve on real workspace
// hosts.
package ideauthorizertest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace"
)

// Prompter answers every permission prompt with the option it is set to
// pick, and records the prompts.
type Prompter struct {
	mu       sync.Mutex
	answer   string
	messages []string
}

// NewPrompter returns a Prompter that picks answer, one of the
// ideauthorizer.PromptOption labels.
func NewPrompter(answer string) *Prompter {
	return &Prompter{answer: answer}
}

// Answer sets the option the prompts shown from now on pick.
func (p *Prompter) Answer(option string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer = option
}

// Prompts returns the messages of the prompts shown so far.
func (p *Prompter) Prompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.messages)
}

// Prompt implements ideauthorizer.PromptOpener. It panics when the
// prompt does not offer the option to pick.
func (p *Prompter) Prompt(
	message string, options []string, _ []term.KeyComb, h handler.PromptHandler,
) browser.Window {
	p.mu.Lock()
	p.messages = append(p.messages, message)
	answer := p.answer
	p.mu.Unlock()
	i := slices.Index(options, answer)
	if i < 0 {
		panic(fmt.Sprintf("prompt offers no %q in %q: %s", answer, options, message))
	}
	h.OnSelect(i, answer)
	return browsertest.NopWindow()
}

// Floating implements ideauthorizer.PromptOpener. It confirms the
// scopes the picker preselects.
func (p *Prompter) Floating(h browser.Floating, _ browserapi.FloatingConfig) (browser.Window, error) {
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	return browsertest.NopWindow(), nil
}

// NewAuthorizer returns an Authorizer that asks prompter about the
// commands extensions start, and binds what is approved to the
// executables they run on host, the scheme of the workspace at uri, as
// the IDE builds it.
func NewAuthorizer(
	t *testing.T, host schemeapi.Scheme, uri workspaceapi.URI, prompter *Prompter,
) *ideauthorizer.Authorizer {
	t.Helper()
	a, err := ideauthorizer.NewAuthorizer(newEditor(t), prompter,
		storagestub.NewInMemoryService(), runNow, nopTabs{},
		pkgtrust.NewStore(), host, uri, ideauthorizer.Config{})
	if err != nil {
		t.Fatalf("new authorizer: %v", err)
	}
	return a
}

// ExtensionContext returns ctx carrying the identity of an extension
// that is not auto-authorized, so that the commands authorized with it
// are asked about.
func ExtensionContext(ctx context.Context) context.Context {
	return blueauth.ContextWithClaims(ctx, blueauth.UserClaims[ideauthorizer.Extension]{
		Extra: ideauthorizer.Extension{Plugin: true, Path: "/bin/extension"},
	})
}

func runNow(fn func()) bool {
	fn()
	return true
}

func newEditor(t *testing.T) text.Editor {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///")
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	if err != nil {
		t.Fatalf("new memory scheme: %v", err)
	}
	t.Cleanup(func() { _ = scheme.Close() })
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = runNow
	comp, err := text.NewComponent(standard.Editor(),
		workspace.NewSchemeWorkspace(uri, scheme, runNow), cfg)
	if err != nil {
		t.Fatalf("new editor: %v", err)
	}
	return comp
}

// nopTabs drops the status of the prompts, which tests read from the
// Prompter.
type nopTabs struct{}

func (nopTabs) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, fmt.Errorf("no tabs")
}

func (nopTabs) SetTabName(workspaceapi.URI, string, term.Attributes) error { return nil }

func (nopTabs) OnTabExit(workspaceapi.URI) bool { return false }

func (nopTabs) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }

func (nopTabs) EndTabStatus(workspaceapi.URI) error { return nil }
