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
	"net/http"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/gitpkg"
	"unstable.build/rune/internal/ide/idepkg/multipkg"
	"unstable.build/rune/internal/workspace"
)

func newRemoteReleaseManager(gitOpts ...gitpkg.Option) release.Manager {
	// Release downloads are unauthenticated, matching newAPIClient: the
	// oauth transport fails client-side for logged-out users, which would
	// break installs under the usage-based paywall.
	official := cdnrelease.NewManager(
		&http.Client{},
		idepkg.ReleasesURL(*flagHTTPAddress, idepkg.HostArch()))
	return multipkg.New(gitpkg.New(gitOpts...), official)
}

// loadRemoteConfigAndApplyEnv loads the remote ~/.rune config overlaid with
// the workspace-root .rune/config.yaml and applies its gui.env block to
// host, so the tools the `rune -x` server spawns see the packages installed
// on this host. On any error it warns and continues so serving is never
// blocked.
func loadRemoteConfigAndApplyEnv(
	host *hostenv.Host, scheme schemeapi.Scheme, uri workspaceapi.URI,
) {
	cwd := workspace.NewSchemeWorkspace(uri, scheme,
		func(fn func()) bool { fn(); return true })
	rootCfg, err := ide.ConfigWithOverlays(
		*flagConfigPath, runeDefaultConfig(), cwd,
		[]string{workspaceConfigFilename},
	)
	if err != nil {
		log.Warnf("load remote config: %v", err)
		if rootCfg == nil {
			rootCfg = config.NopConfig()
		}
	}
	applyConfigEnv(host, rootCfg)
}

// applyConfigEnv applies rootCfg's gui.env block to host. Failures are
// logged.
func applyConfigEnv(host *hostenv.Host, rootCfg config.Config) {
	env := config.NopConfig()
	guiCfg, ok, err := getGUIConfig(rootCfg)
	if err != nil {
		log.Warnf("read gui config: %v", err)
	} else if ok {
		if env, err = getGUIEnvVars(guiCfg); err != nil {
			log.Warnf("read gui.env: %v", err)
		}
	}
	if err := host.Apply(env); err != nil {
		log.Warnf("apply gui.env: %v", err)
	}
}

// resolveRemoteEditorMode resolves the user's editor mode from the remote
// ~/.rune config so it can be exposed to package config.star scripts as
// RUNE_EDITOR_MODE during install. It reads only the base config, not the
// workspace-root overlay: editor.mode is a user/home setting. On any load
// error it warns and returns the default (via ide.PkgEditorMode), so the
// mode is always concrete.
func resolveRemoteEditorMode() string {
	cfg, err := ide.Config(*flagConfigPath, runeDefaultConfig())
	if err != nil {
		log.Warnf("resolve editor mode: %v", err)
	}
	return ide.PkgEditorMode(cfg)
}
