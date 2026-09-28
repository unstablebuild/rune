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

package idepkg

import (
	"fmt"
	"runtime"

	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/ide/pkgtrust"
)

// StoragePartition is the storage partition every package Manager reads and
// writes. The editor's package manager and any headless provisioning manager
// must share it so remote-provisioned installs are visible to the same
// on-disk state — hence a single exported constant rather than a bare literal
// duplicated across call sites.
const StoragePartition = "idepkg"

// ReleasesURL builds the base URL a release Manager reads package metadata
// from for the given arch, e.g. "<base>/api/releases/linux-amd64". Both the
// editor's release manager and the headless provisioning manager format it
// through here so the shape can never drift between them.
func ReleasesURL(base, arch string) string {
	return base + "/api/releases/" + arch
}

// HostArch returns the "<GOOS>-<GOARCH>" release arch token for the running
// process. The remote `rune -x` server uses its own arch; the local side
// never chooses it.
func HostArch() string {
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}

// NewProvisioningManager builds a Manager configured for headless package
// install/activation with no editor UI: notifications and window management
// are discarded and scheduling is synchronous. It owns the storage partition
// (StoragePartition) and the baseline option set so a headless caller — the
// remote `rune -x` server — cannot drift from the editor's package-manager
// wiring in ide/pkgmanager. It returns the Manager and the partitioned
// storage the caller must Close.
//
// configBase supplies the editor's default config tree used as the `config`
// base when a package's .star config is read during a merge; it must match
// the tree the editor feeds via WithConfigBase or a .star-based gui.env merge
// resolves against a nil base.
//
// The interactive prompt options (WithSyntaxParser, WithFrameCharSet) are
// deliberately omitted: they only drive promptConfigChange, which a headless
// caller never reaches because purely-new keys (a fresh remote's gui.env
// block) take the auto-apply path. Extra opts are appended after the baseline.
func NewProvisioningManager(
	rootStorage storageapi.Service, rm release.Manager, scheme schemeapi.Scheme,
	dataDir, configPath, editorMode string, configBase func() map[string]any,
	trust *pkgtrust.Store, opts ...Option,
) (*Manager, storageapi.Service) {
	if trust == nil {
		panic("idepkg.NewProvisioningManager: nil trust store")
	}
	storage := storageapi.WithPartition(rootStorage, StoragePartition)
	baseOpts := []Option{WithConfigBase(configBase)}
	if editorMode != "" {
		baseOpts = append(baseOpts, WithEditorMode(editorMode))
	}
	mgr := NewManager(
		nopNotifications{}, rm, storage, trust, scheme, dataDir, configPath,
		nopWindowManager{}, syncScheduleNextTick, term.NopInterrupter(),
		append(baseOpts, opts...)...,
	)
	return mgr, storage
}

// syncScheduleNextTick runs fn inline and reports it as scheduled. Headless
// provisioning has no event loop to defer onto.
func syncScheduleNextTick(fn func()) bool { fn(); return true }

// nopNotifications discards package-manager notifications; a headless
// provisioner has no user-facing UI.
type nopNotifications struct{}

func (nopNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

// nopWindowManager satisfies browserapi.WindowManager for a headless package
// manager. Installs on a fresh config take the auto-apply path, so no window
// is ever opened; the methods exist only to satisfy the interface.
type nopWindowManager struct{}

func (nopWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (nopWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return nil, nil
}
func (nopWindowManager) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return nil, nil
}
func (nopWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (nopWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}
func (nopWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (nopWindowManager) CloseWindow(browserapi.Window) error                          { return nil }
func (nopWindowManager) SetTabActivity(workspaceapi.URI, bool) error                  { return nil }
