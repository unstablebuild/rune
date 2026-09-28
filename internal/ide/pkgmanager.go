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
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/gitpkg"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/multipkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/text"
)

const installStorageKey = "autoInstallPrompt"

type pkgManager struct {
	pkg              *idepkg.Manager
	n                browserapi.Notifications
	wh               *workspaceManagerHandler
	storage          storageapi.Service
	scheduleNextTick func(func()) bool
	interrupter      term.Interrupter
	pending          sync.Map // map[string]*installGate
	uc               *idepkg.UpdateChecker
	autoInstall      bool
}

type installStorageValue struct {
	Value bool // true => always install without prompting
}

func (m *pkgManager) init(
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
	m.scheduleNextTick = scheduleNextTick
	m.n = n
	m.interrupter = interrupter
	m.wh = wh
	m.storage = storage
	m.autoInstall = autoInstall
	m.uc.Start(context.Background())
}

// LibDir installs package via prompt if not installed yet
func (m *pkgManager) LibDir(ctx context.Context, pkgID string) (
	sdkiterator.Iterator[string], error,
) {
	it, err := m.pkg.LibDir(ctx, pkgID)
	if err == nil || !errors.Is(err, idepkg.ErrNotInstalled) {
		return it, err
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
			return nil, storageapi.ErrNotFound
		}
		return nil, fmt.Errorf("get latest version: %w", err)
	}

	if gate, ok := m.pending.Load(pkgID); ok {
		return newPendingIterator(m.pkg, pkgID, gate.(*installGate)), nil
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

func (m *pkgManager) installLatest(
	ctx context.Context, pkgID string, version release.Version,
) (sdkiterator.Iterator[string], error) {
	pw := text.NewNotifyProgressWriter(m.n, m.interrupter,
		fmt.Sprintf("install %s@%s", pkgID, version), m.scheduleNextTick)
	if err := m.pkg.InstallPackageVersion(ctx, pkgID, version, pw); err != nil {
		return nil, fmt.Errorf("install latest version: %w", err)
	}
	return m.pkg.LibDir(ctx, pkgID)
}

func (m *pkgManager) getLatestVersion(
	ctx context.Context, pack string,
) (release.Version, error) {
	version, err := m.pkg.LatestVersion(ctx, pack)
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

	ctx := context.Background()
	gate := newInstallGate(func() { m.pending.Delete(pkgID) })

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
							return m.pkg.InstallPackageVersion(ctx, pkgID, version, pw)
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
	return newPendingIterator(m.pkg, pkgID, gate), nil
}

func (m *pkgManager) Close() error {
	ret := m.uc.Close()
	if m.storage != nil {
		if err := m.storage.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	return ret
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
	onResolve func()
}

// newInstallGate builds a gate whose onResolve runs exactly once, after
// the outcome is known, so callers can release per-package bookkeeping
// without duplicating it across resolution paths.
func newInstallGate(onResolve func()) *installGate {
	return &installGate{done: make(chan struct{}), onResolve: onResolve}
}

func (g *installGate) resolve(err error) {
	g.err = err
	g.onResolve()
	close(g.done)
}

// install claims the gate and runs run off the event loop, resolving
// with its result when it finishes. It must be called on the event loop
// so the claim happens before the prompt's close callback can cancel.
func (g *installGate) install(run func() error) {
	g.once.Do(func() {
		go debug.CapturePanicReport(func() {
			g.resolve(run())
		})
	})
}

// cancel resolves the gate as declined, unless an install already
// claimed it.
func (g *installGate) cancel() {
	g.once.Do(func() { g.resolve(storageapi.ErrNotFound) })
}

type pkgManagerIterator struct {
	gate  *installGate
	pkg   *idepkg.Manager
	pkgID string

	it  iterator.Iterator[string]
	err error
}

func newPendingIterator(pkg *idepkg.Manager, pkgID string, gate *installGate) *pkgManagerIterator {
	return &pkgManagerIterator{pkg: pkg, pkgID: pkgID, gate: gate}
}

// await blocks until the install resolves, then lazily opens this
// iterator's own LibDir so each waiter iterates independently.
func (l *pkgManagerIterator) await() {
	<-l.gate.done
	if l.err != nil || l.it != nil {
		return
	}
	if l.gate.err != nil {
		l.err = l.gate.err
		return
	}
	l.it, l.err = l.pkg.LibDir(context.Background(), l.pkgID)
}

func (l *pkgManagerIterator) Next(ctx context.Context) (string, bool) {
	l.await()
	if l.err != nil {
		return "", false
	}
	return l.it.Next(ctx)
}

func (l *pkgManagerIterator) Err() error {
	l.await()
	if l.err != nil {
		return l.err
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
