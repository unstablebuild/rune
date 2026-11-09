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
	"sync"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/text"
)

type bootstrapFlagOverrides struct {
	httpAddress    string
	grpcAddress    string
	dataPath       string
	configPath     string
	websiteAddress string
}

func overrideBootstrapFlags(t testing.TB, o bootstrapFlagOverrides) func() {
	t.Helper()
	prevHTTP := *flagHTTPAddress
	prevGRPC := *flagGRPCAddress
	prevData := *flagDataPath
	prevConfig := *flagConfigPath
	prevWebsite := *flagWebsiteAddress
	*flagHTTPAddress = o.httpAddress
	if o.grpcAddress != "" {
		*flagGRPCAddress = o.grpcAddress
	}
	*flagDataPath = o.dataPath
	*flagConfigPath = o.configPath
	*flagWebsiteAddress = o.websiteAddress
	return func() {
		*flagHTTPAddress = prevHTTP
		*flagGRPCAddress = prevGRPC
		*flagDataPath = prevData
		*flagConfigPath = prevConfig
		*flagWebsiteAddress = prevWebsite
	}
}

// newBootstrapPublishPump wires the publish-event hook the way runGUI
// does: a pumper goroutine runs EventInterrupt UserFuncs under the
// bootstrap locker. Register the returned stop func with t.Cleanup
// after the handler's Close cleanup so it runs before it (LIFO),
// matching the runtime's pump-then-handler teardown order.
//
// stop fences off further publishes before closing the pump channel:
// the prompt-frame shader keeps interrupting at its own FPS well past
// a fast test's lifetime, and a late publish into a closed channel
// would panic the suite.
func newBootstrapPublishPump(
	mu *sync.Mutex,
) (publish func(term.Event) bool, stop func()) {
	publishCh := make(chan term.Event, 256)
	var publishMu sync.Mutex
	stopped := false
	publish = func(ev term.Event) bool {
		publishMu.Lock()
		defer publishMu.Unlock()
		if stopped {
			return false
		}
		select {
		case publishCh <- ev:
			return true
		default:
			return false
		}
	}
	pumperDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(pumperDone)
		for ev := range publishCh {
			if ev.Type == term.EventInterrupt && ev.UserFunc != nil {
				mu.Lock()
				ev.UserFunc()
				mu.Unlock()
			}
		}
	})
	stop = func() {
		publishMu.Lock()
		stopped = true
		publishMu.Unlock()
		close(publishCh)
		<-pumperDone
	}
	return publish, stop
}

func testE2EExtensionsRunner(
	_ workspaceapi.URI,
	_ map[extensionapi.Permission]extension.ResourceRegistrar,
	_, _ string,
	_ browser.Notifications,
	_, _ schemeapi.Executor,
	_ extension.Grantor,
	_ text.Editor,
	_ ideauthorizer.PromptOpener,
	_ storageapi.Service,
	_ func(func()) bool,
) (extension.Runner, error) {
	return nopE2ERunner{}, nil
}

type nopE2ERunner struct{}

func (nopE2ERunner) Run(string, string, config.Config) error { return nil }
func (nopE2ERunner) Close() error                            { return nil }
func (n nopE2ERunner) SignalReload() int                     { return 0 }

func (n nopE2ERunner) WaitReady(ctx context.Context, id string) error {
	return nil
}
