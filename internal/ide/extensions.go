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
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/text"
)

// ExtensionsRunner abstracts the ability to construct extension.Runner.
type ExtensionsRunner interface {
	WorkspaceExtensionsRunner(
		uri workspaceapi.URI, res map[extensionapi.Permission]extension.ResourceRegistrar,
		authorizer extensionv2.ServerAuthorizer,
		trust extensionv2.TrustVerifier,
		dataDir, installDir string, notifications browser.Notifications,
		executor, extExecutor schemeapi.Executor,
		grantor extension.Grantor,
		editor text.Editor,
		promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
		scheduleNextTick func(func()) bool,
	) (extension.Runner, error)
}

// FuncExtensionsRunner wraps fn to satisfy Extensions by invoking in calls to Runner.
func FuncExtensionsRunner(
	fn func(workspaceapi.URI,
		map[extensionapi.Permission]extension.ResourceRegistrar, string, string,
		browser.Notifications, schemeapi.Executor, schemeapi.Executor,
		extension.Grantor,
		text.Editor,
		ideauthorizer.PromptOpener, storageapi.Service,
		func(func()) bool) (extension.Runner, error),
) ExtensionsRunner {
	return fnExtensions{fn: fn}
}

type fnExtensions struct {
	fn func(workspaceapi.URI,
		map[extensionapi.Permission]extension.ResourceRegistrar, string, string,
		browser.Notifications, schemeapi.Executor, schemeapi.Executor,
		extension.Grantor,
		text.Editor,
		ideauthorizer.PromptOpener, storageapi.Service,
		func(func()) bool) (extension.Runner, error)
}

func (f fnExtensions) WorkspaceExtensionsRunner(
	uri workspaceapi.URI,
	res map[extensionapi.Permission]extension.ResourceRegistrar,
	authorizer extensionv2.ServerAuthorizer,
	trust extensionv2.TrustVerifier,
	dataDir, installDir string, n browser.Notifications, exec, extExec schemeapi.Executor,
	grantor extension.Grantor,
	editor text.Editor,
	promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
	scheduleNextTick func(func()) bool,
) (extension.Runner, error) {
	return f.fn(uri, res, dataDir, installDir, n, exec, extExec, grantor, editor,
		promptOpener, storage, scheduleNextTick)
}
