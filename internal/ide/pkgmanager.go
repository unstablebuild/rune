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
	"sync"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/gitpkg"
	"unstable.build/rune/internal/ide/idepkg/multipkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text"
)

const installStorageKey = "autoInstallPrompt"

// pkgManager resolves package lib dirs through pm and, when a package
// is missing, asks the user in this UI before installing it through pm.
// host names the machine pm installs on; it is empty for this machine.
// local is this machine's manager when pm is another machine's, and
// nil otherwise.
type pkgManager struct {
	pm               idepkg.PackageManager
	host             string
	local            idepkg.PackageManager
	n                browserapi.Notifications
	wh               *workspaceManagerHandler
	storage          storageapi.Service
	scheduleNextTick func(func()) bool
	interrupter      term.Interrupter
	pending          sync.Map // map[string]*installGate
	// declined holds the packages the user said no to, so the tooling
	// that retries a lookup does not ask about them again.
	declined    sync.Map // map[string]struct{}
	autoInstall bool
}

// newPkgManager takes ownership of storage, which must be the
// idepkg.StoragePartition partition so every host shares the
// "Yes, Always" answer.
func newPkgManager(
	pm idepkg.PackageManager, host string, local idepkg.PackageManager,
	n browserapi.Notifications, storage storageapi.Service,
	wh *workspaceManagerHandler, scheduleNextTick func(func()) bool,
	interrupter term.Interrupter, autoInstall bool,
) *pkgManager {
	return &pkgManager{
		pm:               pm,
		host:             host,
		local:            local,
		n:                n,
		wh:               wh,
		storage:          storage,
		scheduleNextTick: scheduleNextTick,
		interrupter:      interrupter,
		autoInstall:      autoInstall,
	}
}

// localPkgManager manages the packages of the machine Rune runs on.
type localPkgManager struct {
	*pkgManager
	pkg *idepkg.Manager
	uc  *idepkg.UpdateChecker
}

type installStorageValue struct {
	Value bool // true => always install without prompting
}

func (m *localPkgManager) init(
	n browserapi.Notifications, rm release.Manager,
	wm browserapi.WindowManager,
	rootStorage storageapi.Service, scheme schemeapi.Scheme,
	dataDir, configPath string,
	fcs component.FrameCharSet,
	interrupter term.Interrupter, wh *workspaceManagerHandler,
	scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser,
	editorMode string,
	autoInstall bool,
	afterConfigMerge func(idepkg.ConfigMergeEvent) (idepkg.ConfigMergeResult, error),
	gitRemoteURL func(pkgID string) string,
	trust *pkgtrust.Store,
) {
	storage := storageapi.WithPartition(rootStorage, idepkg.StoragePartition)
	// Compose the official release manager (rm) with a git-backed one so
	// <host>/<path> IDs install straight from their repositories while
	// everything else — including a git package's requirements —
	// resolves through the official distribution.
	var gitOpts []gitpkg.Option
	if gitRemoteURL != nil {
		gitOpts = append(gitOpts, gitpkg.WithRemoteURL(gitRemoteURL))
	}
	composed := multipkg.New(gitpkg.New(gitOpts...), rm)
	opts := []idepkg.Option{
		idepkg.WithFrameCharSet(fcs),
		idepkg.WithSyntaxParser(parser),
		idepkg.WithEditorMode(editorMode),
		idepkg.WithConfigBase(func() map[string]any {
			cfg, err := wh.reloadConfig()
			if err != nil {
				return nil
			}
			return cfg.cfg
		}),
		idepkg.WithAfterConfigMerge(afterConfigMerge),
	}
	m.pkg = idepkg.NewManager(n, composed, storage, trust, scheme, dataDir,
		configPath, wm, scheduleNextTick, interrupter,
		opts...,
	)
	m.uc = idepkg.NewUpdateChecker(m.pkg)
	m.pkgManager = newPkgManager(m.pkg, "", nil, n, storage, wh,
		scheduleNextTick, interrupter, autoInstall)
	m.uc.Start(context.Background())
}

func (m *localPkgManager) Close() error {
	ret := m.uc.Close()
	if err := m.pkgManager.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	return ret
}

// LibDir installs package via prompt if not installed yet. Once the
// user declines a package it reports storageapi.ErrNotFound without
// asking again.
func (m *pkgManager) LibDir(ctx context.Context, pkgID string) (
	sdkiterator.Iterator[string], error,
) {
	it, err := m.pm.LibDir(ctx, pkgID)
	if errors.Is(err, pkgrpc.ErrUnsupported) {
		m.notifyUnsupported()
		return nil, storageapi.ErrNotFound
	}
	if err == nil || !errors.Is(err, idepkg.ErrNotInstalled) {
		return it, err
	}
	if _, ok := m.declined.Load(pkgID); ok {
		return nil, storageapi.ErrNotFound
	}

	version, err := m.getLatestVersion(ctx, pkgID)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) {
			_, _ = m.n.NotifyOnce(browserapi.LevelWarn,
				"Some language packages require authentication. "+
					"Run the `login` command to enable them.")
			return nil, storageapi.ErrNotFound
		}
		if errors.Is(err, storageapi.ErrNotFound) || errors.Is(err, document.ErrNotFound) {
			m.notifyUnavailable(ctx, pkgID)
			return nil, storageapi.ErrNotFound
		}
		return nil, fmt.Errorf("get latest version: %w", err)
	}

	if gate, ok := m.pending.Load(pkgID); ok {
		return newPendingIterator(m.pm, pkgID, gate.(*installGate)), nil
	}

	// Onboarding stands in for the operator opt-in so the install
	// prompt does not fight the tutorial overlay.
	if m.autoInstall || m.onboardingActive() {
		return m.installLatest(ctx, pkgID, version)
	}

	var val installStorageValue
	// A legacy stored "never" (Value false) is ignored so those users
	// are prompted again — with the option gone there would otherwise
	// be no way to undo it.
	if err := m.storage.Get(ctx, installStorageKey, &val); err == nil && val.Value {
		return m.installLatest(ctx, pkgID, version)
	}
	return m.openInstallPrompt(pkgID, version)
}

// notifyUnsupported tells the user that the host runs a Rune too old
// to install packages on.
func (m *pkgManager) notifyUnsupported() {
	_, _ = m.n.NotifyOnce(browserapi.LevelWarn, "%s", pkgrpc.UpdateHostMessage(m.host))
}

// notifyUnavailable tells the user that a package they rely on here
// cannot be installed on the host. A package not installed here is not
// one they expect, so its absence there falls back to PATH silently.
func (m *pkgManager) notifyUnavailable(ctx context.Context, pkgID string) {
	if m.local == nil {
		return
	}
	_, inUse, err := m.local.PackageVersionInUse(ctx, pkgID)
	if err != nil || !inUse {
		return
	}
	_, _ = m.n.NotifyOnce(browserapi.LevelWarn,
		"The %s package is not available for %s's platform. "+
			"Install its tools on %s's PATH to use them there.", pkgID, m.host, m.host)
}

func (m *pkgManager) installLatest(
	ctx context.Context, pkgID string, version release.Version,
) (sdkiterator.Iterator[string], error) {
	pw := text.NewNotifyProgressWriter(m.n, m.interrupter,
		fmt.Sprintf("install %s@%s", pkgID, version), m.scheduleNextTick)
	// A concurrent caller may have completed the install since LibDir
	// reported the package missing.
	err := m.pm.InstallPackageVersion(ctx, pkgID, version, pw)
	if err != nil && !errors.Is(err, idepkg.ErrAlreadyInstalled) {
		return nil, fmt.Errorf("install latest version: %w", err)
	}
	return m.pm.LibDir(ctx, pkgID)
}

func (m *pkgManager) getLatestVersion(
	ctx context.Context, pack string,
) (release.Version, error) {
	version, err := m.pm.LatestVersion(ctx, pack)
	if err != nil {
		if errors.Is(err, idepkg.ErrPackageNotFound) {
			return "", storageapi.ErrNotFound
		}
		return "", err
	}
	return version, nil
}

func (m *pkgManager) setAutoInstall() error {
	return m.storage.Set(context.Background(),
		installStorageKey, installStorageValue{Value: true})
}

func (m *pkgManager) onboardingActive() bool {
	return m.wh.onboardingActive != nil && m.wh.onboardingActive()
}

func (m *pkgManager) openInstallPrompt(pkgID string, version release.Version) (
	iterator.Iterator[string], error,
) {
	const (
		yes       = "   Yes   "
		yesAlways = "   Yes, Always   "
		no        = "   No   "
	)

	msg := fmt.Sprintf("Do you want to install package **%q**?", pkgID)
	if m.host != "" {
		msg = fmt.Sprintf("Do you want to install package **%q** on **%s**?", pkgID, m.host)
	}

	ctx := context.Background()
	gate := newInstallGate(func(declined bool) {
		if declined {
			m.declined.Store(pkgID, struct{}{})
		}
		m.pending.Delete(pkgID)
	})

	m.scheduleNextTick(func() {
		m.wh.focusEx().comp.Prompt(msg, []string{yes, yesAlways, no},
			[]term.KeyComb{{Ch: 'Y'}, {Ch: 'A'}, {Ch: 'N'}},
			handler.FuncPromptHandler(
				func(i int, opt string) {
					switch opt {
					case yesAlways:
						_ = m.storage.Set(ctx, installStorageKey, installStorageValue{Value: true})
						fallthrough
					case yes:
						pw := text.NewNotifyProgressWriter(m.n, m.interrupter,
							fmt.Sprintf("install %s@%s", pkgID, version), m.scheduleNextTick)
						gate.install(func() error {
							return m.pm.InstallPackageVersion(ctx, pkgID, version, pw)
						})
					case no:
						gate.cancel()
					}
				},
				func() error {
					// Bare dismissal (e.g. ESC): a no-op if a selection
					// already claimed the gate, including a yes that
					// handed ownership to its install goroutine.
					gate.cancel()
					return nil
				}))
	})

	m.pending.Store(pkgID, gate)
	return newPendingIterator(m.pm, pkgID, gate), nil
}

func (m *pkgManager) Close() error {
	return m.storage.Close()
}

// installGate is a one-shot readiness signal shared by every LibDir
// caller waiting on the same package install. install and cancel both
// resolve the gate through the same sync.Once, so the first to run wins:
// a "yes" selection claims the gate via install synchronously on the
// event loop (then finishes the download off it), so the prompt's later
// close callback calling cancel is a harmless no-op and cannot preempt
// the install. done closes exactly once; writing err before close
// establishes a happens-before with the receive, so no locking is
// needed. Each waiter opens its own LibDir iterator after done closes,
// so concurrent callers do not share a single underlying iterator.
type installGate struct {
	once      sync.Once
	done      chan struct{}
	err       error
	onResolve func(declined bool)
}

// newInstallGate builds a gate whose onResolve runs exactly once, after
// the outcome is known, so callers can release per-package bookkeeping
// without duplicating it across resolution paths.
func newInstallGate(onResolve func(declined bool)) *installGate {
	return &installGate{done: make(chan struct{}), onResolve: onResolve}
}

func (g *installGate) resolve(err error, declined bool) {
	g.err = err
	g.onResolve(declined)
	close(g.done)
}

// install claims the gate and runs run off the event loop, resolving
// with its result when it finishes. It must be called on the event loop
// so the claim happens before the prompt's close callback can cancel.
func (g *installGate) install(run func() error) {
	g.once.Do(func() {
		go debug.CapturePanicReport(func() {
			g.resolve(run(), false)
		})
	})
}

// cancel resolves the gate as declined, unless an install already
// claimed it.
func (g *installGate) cancel() {
	g.once.Do(func() { g.resolve(storageapi.ErrNotFound, true) })
}

type pkgManagerIterator struct {
	gate  *installGate
	pm    idepkg.PackageManager
	pkgID string

	it  iterator.Iterator[string]
	err error
}

func newPendingIterator(
	pm idepkg.PackageManager, pkgID string, gate *installGate,
) *pkgManagerIterator {
	return &pkgManagerIterator{pm: pm, pkgID: pkgID, gate: gate}
}

// await blocks until the install resolves or ctx is done, then lazily
// opens this iterator's own LibDir so each waiter iterates
// independently. A cancelled wait is sticky so Err reports it.
func (l *pkgManagerIterator) await(ctx context.Context) {
	if l.err != nil || l.it != nil {
		return
	}
	select {
	case <-l.gate.done:
	case <-ctx.Done():
		l.err = ctx.Err()
		return
	}
	if l.gate.err != nil {
		l.err = l.gate.err
		return
	}
	l.it, l.err = l.pm.LibDir(context.Background(), l.pkgID)
}

func (l *pkgManagerIterator) Next(ctx context.Context) (string, bool) {
	l.await(ctx)
	if l.err != nil {
		return "", false
	}
	return l.it.Next(ctx)
}

// Err reports only what Next has encountered. It never waits on the
// install decision, so it returns nil before the first Next.
func (l *pkgManagerIterator) Err() error {
	if l.err != nil {
		return l.err
	}
	if l.it == nil {
		return nil
	}
	return l.it.Err()
}

func (l *pkgManagerIterator) Close() error {
	// Close only releases an iterator this consumer actually opened.
	// It must not block on the install decision or open a LibDir just
	// to close it: a never-iterated iterator has nothing to release.
	if l.it == nil {
		return nil
	}
	return l.it.Close()
}

// extensionPackages serves the packages of a workspace's host to its
// extensions. pkgManager.LibDir reports every way a lookup ends without
// the package, each of which it already told the user about, as
// storageapi.ErrNotFound; extensions see pkgapi.ErrNotInstalled.
type extensionPackages struct {
	pkgs idelsp.PkgManager
}

var _ pkgapi.Manager = extensionPackages{}

func (p extensionPackages) LibDir(
	ctx context.Context, pkgID string,
) (sdkiterator.Iterator[string], error) {
	it, err := p.pkgs.LibDir(ctx, pkgID)
	if err != nil {
		return nil, notInstalled(pkgID, err)
	}
	return notInstalledIterator{Iterator: it, pkgID: pkgID}, nil
}

type notInstalledIterator struct {
	sdkiterator.Iterator[string]
	pkgID string
}

func (it notInstalledIterator) Err() error {
	if err := it.Iterator.Err(); err != nil {
		return notInstalled(it.pkgID, err)
	}
	return nil
}

func notInstalled(pkgID string, err error) error {
	if errors.Is(err, storageapi.ErrNotFound) {
		return fmt.Errorf("%s: %w", pkgID, pkgapi.ErrNotInstalled)
	}
	return err
}
