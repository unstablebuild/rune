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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idepkg/gitpkg"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/workspace/walkdir"
)

// Sentinel errors returned by release-manager wrappers below so callers
// can branch on the kind of failure with errors.Is without parsing
// messages. The underlying cdnrelease error is preserved in the wrap
// chain for diagnostics.
var (
	// ErrPackageNotFound is returned when the server reports that a
	// requested package does not exist.
	ErrPackageNotFound = errors.New("package not found")
	// ErrVersionNotFound is returned when the server reports that a
	// requested version of a known package does not exist.
	ErrVersionNotFound = errors.New("package version not found")
	// ErrAlreadyInstalled is returned when a fully-installed version of a
	// package is installed again. Callers that treat re-installs as
	// idempotent no-ops can test for it with errors.Is.
	ErrAlreadyInstalled = errors.New("package version already installed")
	// ErrServerUnavailable is returned for transient (5xx) failures
	// from the package server or the signed-URL download backend.
	ErrServerUnavailable = errors.New("package server unavailable")
	// ErrArtifactMissing is returned when the package server knows a
	// version but its artifact is absent from the download backend. The
	// version is published yet uninstallable, so retrying cannot help and
	// the message must not read as a transient outage.
	ErrArtifactMissing = errors.New("release artifact is missing from the download server")
	// ErrForbidden is returned when the package server rejects the
	// caller with a 403 (no valid token or insufficient subscription).
	// The wrapped message is rendered directly to the user.
	ErrForbidden = errors.New("login first via `login` command and " +
		"ensure you have a valid subscription to download packages")
	// ErrNoReleases is returned by LatestVersion when a package exists
	// but has no published bundles to resolve a latest version from.
	ErrNoReleases = errors.New("package has no releases")
)

// errInstallInFlight is returned by install for an install of the
// package in flight that it does not wait for.
var errInstallInFlight = errors.New("an install of the package is in flight")

// translatePackageErr maps a *cdnrelease.StatusError on a
// package-scoped call into a user-facing wrapped sentinel. Errors
// without a recognisable status (network errors, non-StatusError
// wraps) are returned unchanged.
func translatePackageErr(err error, pkgID string) error {
	var se *cdnrelease.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch {
	case se.Status == http.StatusForbidden:
		return ErrForbidden
	case se.Status == http.StatusNotFound:
		return fmt.Errorf("package %q does not exist: %w", pkgID, ErrPackageNotFound)
	case se.Status >= 500:
		return fmt.Errorf("%w (status %d)", ErrServerUnavailable, se.Status)
	}
	return err
}

func translateVersionErr(err error, pkgID, version string) error {
	var se *cdnrelease.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch {
	case se.URL == "" && se.Status == http.StatusNotFound:
		// Signed-URL download from GCS: a 404 describes the artifact
		// object, not the package server.
		return fmt.Errorf("%q version %q: %w",
			pkgID, version, ErrArtifactMissing)
	case se.URL == "":
		// Signed-URL download from GCS failed
		return fmt.Errorf("download of %q version %q failed: %w (status %d)",
			pkgID, version, ErrServerUnavailable, se.Status)
	case se.Status == http.StatusForbidden:
		return ErrForbidden
	case se.Status == http.StatusNotFound:
		return fmt.Errorf("version %q of package %q does not exist: %w",
			version, pkgID, ErrVersionNotFound)
	case se.Status >= 500:
		return fmt.Errorf("%w (status %d)", ErrServerUnavailable, se.Status)
	}
	return err
}

func translateListErr(err error, pkgID string) error {
	var se *cdnrelease.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch {
	case se.Status == http.StatusForbidden:
		return ErrForbidden
	case se.Status == http.StatusNotFound && pkgID != "":
		return fmt.Errorf("package %q does not exist: %w", pkgID, ErrPackageNotFound)
	case se.Status >= 500:
		return fmt.Errorf("%w (status %d)", ErrServerUnavailable, se.Status)
	}
	return err
}

// NewManager allocates storage for a new Manager and initializes it.
// The dataDir argument will be used to store downloaded bundles
// and manage executables.
func NewManager(
	n browserapi.Notifications, m release.Manager,
	storage storageapi.Service, trust *pkgtrust.Store,
	scheme schemeapi.Scheme, dataDir string,
	configPath string, prompter browserapi.Prompter,
	scheduleNextTick func(func()) bool,
	interrupter term.Interrupter, opts ...Option,
) *Manager {
	if trust == nil {
		panic("idepkg.NewManager: nil trust store")
	}
	if dataDir == "" {
		panic("data directory must not be empty")
	}
	if configPath == "" {
		panic("config path must not be empty")
	}
	schemeURI, _ := scheme.URI(".")
	binDir := makeBinDirname(dataDir)
	ret := &Manager{
		dataDir:          dataDir,
		configPath:       configPath,
		scheme:           scheme,
		binDir:           binDir,
		schemeURI:        schemeURI,
		interrupter:      interrupter,
		frameCharSet:     component.FrameCharSetDefault(),
		prompter:         prompter,
		scheduleNextTick: scheduleNextTick,
		n:                n,
		m:                m,
		storage:          storage,
		trust:            trust,
	}
	ret.iterators.m = make(map[string]*inFlightInstall)
	ret.iterators.waiting = make(map[*installTree]*installTree)
	for _, opt := range opts {
		opt(ret)
	}
	return ret
}

// Manager implements ManagerInterface and creates the managed package dirs
// used to make downloaded executables available via PATH.
//
// Note that OS/system is managed by having a separate Manager that points
// to a different underlying release.Manager.
type Manager struct {
	n           browserapi.Notifications
	m           release.Manager
	interrupter term.Interrupter
	// prompter is called on the event loop, which scheduleNextTick
	// schedules on.
	prompter         browserapi.Prompter
	frameCharSet     component.FrameCharSet
	scheduleNextTick func(func()) bool
	storage          storageapi.Service
	dataDir          string
	configPath       string
	scheme           schemeapi.Scheme
	schemeURI        workspaceapi.URI
	binDir           string
	trust            *pkgtrust.Store

	editorMode string

	// afterConfigWrite, when set, is called after the config a package
	// contributes changed. See WithAfterConfigWrite.
	afterConfigWrite func(pkgID string)
	// afterInstall, when set, is called after a version of a package is
	// installed or put in use. See WithAfterInstall.
	afterInstall func(pkgID string)

	// iterators gates the installs of each package. m holds the install
	// in flight of each package, and waiting holds, for each install
	// tree waiting for one of them, the tree that install belongs to.
	iterators struct {
		sync.Mutex
		m       map[string]*inFlightInstall
		waiting map[*installTree]*installTree
	}
}

// installTree is an install that was not asked for by another install,
// together with the requirement installs it makes, one at a time.
type installTree struct {
	// pkgID is the package of the first install. A non-zero size also
	// keeps distinct trees at distinct addresses.
	pkgID string
}

type installTreeKey struct{}

// inFlightInstall is an install of a package that has not ended. ready
// is held, and done open, until it does.
type inFlightInstall struct {
	ready *sync.Mutex
	done  chan struct{}
	tree  *installTree
}

// LibDir returns an iterator to the lib directory of the given package.
// The paths returned by the iterator are always absolute.
func (m *Manager) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	m.iterators.Lock()
	defer m.iterators.Unlock()

	libDir := makePackageLibDirname(m.dataDir, pkgID)
	f, ok := m.iterators.m[pkgID]
	if ok {
		return newPendingIterator(f.ready, m.scheme, m.schemeURI, libDir), nil
	}

	_, err := os.Stat(libDir)
	if err != nil {
		return nil, ErrNotInstalled
	}

	return newReadyIterator(ctx, m.scheme, m.schemeURI, libDir), nil
}

// DescribePackage fetches a Package manifest.
func (m *Manager) DescribePackage(ctx context.Context, pkgID string) (release.Package, error) {
	if err := validatePkgID(pkgID); err != nil {
		return release.Package{}, fmt.Errorf("package id: %w", err)
	}
	pkg, err := m.m.GetPackage(ctx, pkgID)
	if err != nil {
		return release.Package{}, translatePackageErr(err, pkgID)
	}
	return pkg, nil
}

// DescribeRelease fetches a release bundle manifest.
func (m *Manager) DescribeRelease(ctx context.Context, pkgID string, version string) (
	release.Bundle, error,
) {
	if err := validatePkgID(pkgID); err != nil {
		return release.Bundle{}, fmt.Errorf("package id: %w", err)
	}
	if err := validatePkgPath(version); err != nil {
		return release.Bundle{}, fmt.Errorf("release version: %w", err)
	}
	b, err := m.m.Get(ctx, pkgID, release.Version(version),
		release.NopProgressWriter(io.Discard))
	if err != nil {
		return release.Bundle{}, translateVersionErr(err, pkgID, version)
	}
	return b, nil
}

// ListPackages lists all packages.
func (m *Manager) ListPackages(ctx context.Context, filters map[string]string) (
	iterator.Iterator[release.Package], error,
) {
	it, err := m.m.ListPackages(ctx, filters)
	if err != nil {
		return nil, translateListErr(err, "")
	}
	return it, nil
}

// ListPackageVersions lists all bundles of a package.
func (m *Manager) ListPackageVersions(ctx context.Context, pkgID string, filters map[string]string) (
	iterator.Iterator[release.Bundle], error,
) {
	if err := validatePkgID(pkgID); err != nil {
		return nil, fmt.Errorf("package id: %w", err)
	}
	it, err := m.m.List(ctx, pkgID, filters)
	if err != nil {
		return nil, translateListErr(err, pkgID)
	}
	return it, nil
}

// LatestVersion resolves the newest published version of a package,
// preferring the server's Latest pointer and falling back to the newest
// bundle by creation time. It returns ErrPackageNotFound when the package
// does not exist and ErrNoReleases when it exists but has no bundles.
func (m *Manager) LatestVersion(ctx context.Context, pkgID string) (release.Version, error) {
	pkg, err := m.DescribePackage(ctx, pkgID)
	if err != nil {
		// The release manager may report a missing package as a bare
		// "not found" rather than a translated StatusError; normalise so
		// callers can branch on ErrPackageNotFound alone.
		if !errors.Is(err, ErrPackageNotFound) && err.Error() == "not found" {
			return "", fmt.Errorf("package %q does not exist: %w", pkgID, ErrPackageNotFound)
		}
		return "", err
	}
	if pkg.Latest != "" {
		return pkg.Latest, nil
	}
	it, err := m.ListPackageVersions(ctx, pkgID, nil)
	if err != nil {
		return "", err
	}
	defer it.Close()
	var newest time.Time
	var version release.Version
	for {
		b, ok := it.Next(ctx)
		if !ok {
			break
		}
		if b.CreatedAt.After(newest) {
			newest = b.CreatedAt
			version = b.Version
		}
	}
	if err := it.Err(); err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("%q: %w", pkgID, ErrNoReleases)
	}
	return version, nil
}

// InstallPackageVersion downloads and installs a package bundle by name
// and version, reporting progress via the ProgressWriter. It blocks
// until the install finishes and returns the first error encountered, so
// callers can report success or failure directly. While another install
// of pkgID is in flight, it returns nil without waiting for it.
func (m *Manager) InstallPackageVersion(
	ctx context.Context, pkgID string, version release.Version,
	pw repl.ProgressWriter,
) error {
	_, err := m.install(ctx, pkgID, version, pw)
	if errors.Is(err, errInstallInFlight) {
		return nil
	}
	return err
}

// install is InstallPackageVersion returning the version that was in use
// before, empty when none was. A requirement install, made by another
// install, waits for an install of pkgID in flight to end and tries
// again, unless that install waits for this one, directly or through
// others. It then returns errInstallInFlight, as any install does for an
// install of pkgID in flight.
func (m *Manager) install(
	ctx context.Context, pkgID string, version release.Version,
	pw repl.ProgressWriter,
) (release.Version, error) {
	if pkgID == "" || version == "" {
		return "", errors.New("package and version must not be empty")
	}
	if pw == nil {
		pw = repl.NopProgressWriter()
	}

	if err := validatePkgID(pkgID); err != nil {
		return "", fmt.Errorf("package id: %w", err)
	}
	if err := validatePkgPath(string(version)); err != nil {
		return "", fmt.Errorf("release version: %w", err)
	}

	tree, nested := ctx.Value(installTreeKey{}).(*installTree)
	m.iterators.Lock()
	for {
		f, ok := m.iterators.m[pkgID]
		if !ok {
			break
		}
		if !nested || m.closesWaitCycleLocked(tree, f.tree) {
			m.log(log.InfoLevel, "there's already an ongoing install of package: %s", pkgID)
			m.iterators.Unlock()
			return "", errInstallInFlight
		}
		m.log(log.InfoLevel, "waiting for the ongoing install of package %s", pkgID)
		m.iterators.waiting[tree] = f.tree
		m.iterators.Unlock()
		<-f.done
		m.iterators.Lock()
		delete(m.iterators.waiting, tree)
	}

	tarfile, err := os.CreateTemp("", "")
	if err != nil {
		m.iterators.Unlock()
		return "", fmt.Errorf("create temp: %w", err)
	}

	key := m.makeDownloadKey(pkgID, version)
	err = m.storage.Create(ctx, key, newPkgVersionValue(pkgID, version))
	if err != nil {
		if errors.Is(err, storageapi.ErrAlreadyExists) {
			var existing pkgVersionValue
			if getErr := m.storage.Get(ctx, key, &existing); getErr == nil && !existing.Complete {
				_ = m.storage.Delete(ctx, key)
				_ = os.RemoveAll(makePackageVersionDirname(m.dataDir, pkgID, version))
				_ = os.RemoveAll(makeStagingDirname(m.dataDir, pkgID, version))
				err = m.storage.Create(ctx, key, newPkgVersionValue(pkgID, version))
			}
			if err != nil {
				m.cleanupFile(tarfile)
				m.iterators.Unlock()
				return "", fmt.Errorf("version %s of package %s has "+
					"already been installed: %w", version, pkgID,
					ErrAlreadyInstalled)
			}
		} else {
			m.cleanupFile(tarfile)
			m.iterators.Unlock()
			return "", fmt.Errorf("store package version: %w", err)
		}
	}

	if !nested {
		tree = &installTree{pkgID: pkgID}
		ctx = context.WithValue(ctx, installTreeKey{}, tree)
	}
	f := &inFlightInstall{ready: new(sync.Mutex), done: make(chan struct{}), tree: tree}
	f.ready.Lock() // block calls to iterator
	m.iterators.m[pkgID] = f
	m.iterators.Unlock()

	// pkgID was validated, so reading its in-use version cannot fail.
	replaced, _, _ := m.PackageVersionInUse(ctx, pkgID)
	return replaced, m.download(ctx, pkgID, version, tarfile, pw, key)
}

// closesWaitCycleLocked reports whether tree waiting for an install in
// flight in owner would close a cycle of install trees waiting for each
// other. That includes owner being tree: a tree makes one install at a
// time, so its install in flight is an ancestor of the asking one.
func (m *Manager) closesWaitCycleLocked(tree, owner *installTree) bool {
	for t := owner; t != nil; t = m.iterators.waiting[t] {
		if t == tree {
			return true
		}
	}
	return false
}

// DeletePackageVersion deletes a package version from local storage. This method is idempotent.
func (m *Manager) DeletePackageVersion(
	ctx context.Context, pkgID string, version release.Version, force bool,
) (ret error) {
	if pkgID == "" || version == "" {
		return errors.New("package and version must not be empty")
	}
	if err := validatePkgID(pkgID); err != nil {
		return fmt.Errorf("package id: %w", err)
	}
	if err := validatePkgPath(string(version)); err != nil {
		return fmt.Errorf("release version: %w", err)
	}

	key := m.makeDownloadKey(pkgID, version)
	var val pkgVersionValue
	if err := m.storage.Get(ctx, key, &val); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return ErrNotInstalled
		}
		return err
	}

	dirname, libdirname, isInUse, err := m.isPackageVersionInUse(pkgID, version)
	if err != nil {
		ret = multierror.Append(ret, err)
	}
	if isInUse && force {
		// remove lib + executables if package version was being used
		err = os.RemoveAll(libdirname)
		if err != nil {
			ret = multierror.Append(ret, err)
		}
		err = removeExecutables(val.Executables, m.binDir)
		if err != nil {
			ret = multierror.Append(ret, err)
		}
		if err := m.uninstallConfig(pkgID); err != nil {
			ret = multierror.Append(ret, err)
		}
	} else if isInUse {
		return ErrVersionInUse
	}

	if err := m.storage.Delete(ctx, key); err != nil {
		ret = multierror.Append(ret, err)
	}

	err = os.RemoveAll(dirname)
	if err != nil {
		ret = multierror.Append(ret, err)
	}
	if ret != nil {
		// best effort to try to keep delete retryable
		_ = m.storage.Set(ctx, key, val)
	}
	return
}

// DeletePackage deletes all bundles of the given package.
func (m *Manager) DeletePackage(
	ctx context.Context, pkgID string,
) error {
	if pkgID == "" {
		return errors.New("package and version must not be empty")
	}
	if err := validatePkgID(pkgID); err != nil {
		return fmt.Errorf("package id: %w", err)
	}
	it, err := m.ListInstalledPackageVersions(ctx, pkgID)
	if err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	var versions []release.Version
	for {
		next, ok := it.Next(ctx)
		if !ok {
			break
		}

		versions = append(versions, next)
	}
	if err := it.Err(); err != nil {
		return fmt.Errorf("bundles iterator: %w", err)
	}
	if len(versions) == 0 {
		return ErrNotInstalled
	}

	errs := make([]error, len(versions))
	var wg sync.WaitGroup
	wg.Add(len(versions))
	for i := 0; i < len(versions); i++ {
		i := i
		go debug.CapturePanicReport(func() {
			version := versions[i]
			defer wg.Done()
			errs[i] = m.DeletePackageVersion(ctx, pkgID, version, true)
		})
	}
	wg.Wait()

	var ret error
	for _, err := range errs {
		if err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	pkgDirname := filepath.Join(m.dataDir, "pkg", pkgID)
	if err := os.RemoveAll(pkgDirname); err != nil {
		ret = multierror.Append(ret, err)
	}
	// A package whose in-use version was deleted on its own, or whose lib
	// link is gone, still has its config.
	if err := m.uninstallConfig(pkgID); err != nil {
		ret = multierror.Append(ret, err)
	}
	return ret
}

// ListInstalledPackages lists the packages installed.
func (m *Manager) ListInstalledPackages(ctx context.Context) (
	iterator.Iterator[string], error,
) {
	dit, err := m.storage.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	it := iterator.FromDocumentIterator[pkgVersionValue](dit)
	complete := iterator.Filter(it, func(p pkgVersionValue) bool {
		return p.Complete
	})
	mapped := iterator.Map(complete, func(p pkgVersionValue) string {
		return p.Package
	})
	seen := make(map[string]struct{})
	return iterator.Filter(mapped, func(pkg string) bool {
		if _, ok := seen[pkg]; ok {
			return false
		}
		seen[pkg] = struct{}{}
		return true
	}), nil
}

// ListInstalledPackageVersions lists the bundles installed for the given package.
func (m *Manager) ListInstalledPackageVersions(ctx context.Context, pkgID string) (
	iterator.Iterator[release.Version], error,
) {
	if pkgID == "" {
		return nil, errors.New("package must not be empty")
	}
	if err := validatePkgID(pkgID); err != nil {
		return nil, fmt.Errorf("package id: %w", err)
	}
	dit, err := m.storage.List(ctx, []storageapi.Filter{{
		Field: storageapi.Field{
			FieldPath: []string{"Package"},
			Value:     pkgID,
		},
		Op: storageapi.OpEqual,
	}})
	if err != nil {
		return nil, err
	}
	it := iterator.FromDocumentIterator[pkgVersionValue](dit)
	complete := iterator.Filter(it, func(p pkgVersionValue) bool {
		return p.Complete
	})
	return iterator.Map(complete, func(p pkgVersionValue) release.Version {
		return p.Version
	}), nil
}

// UsePackageVersion updates the lib and bin directories to point to the given
// package version.
func (m *Manager) UsePackageVersion(
	ctx context.Context, pkgID string, version release.Version,
) error {
	if err := validatePkgID(pkgID); err != nil {
		return fmt.Errorf("package id: %w", err)
	}
	if err := validatePkgPath(string(version)); err != nil {
		return fmt.Errorf("release version: %w", err)
	}
	key := m.makeDownloadKey(pkgID, version)
	var val pkgVersionValue
	if err := m.storage.Get(ctx, key, &val); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return ErrNotInstalled
		}
		return err
	}
	if !val.Complete {
		return ErrNotInstalled
	}
	_, _, isInUse, err := m.isPackageVersionInUse(pkgID, version)
	if err != nil {
		return err
	}
	if isInUse {
		return ErrVersionInUse
	}
	pkgVersionDirname := makePackageVersionDirname(m.dataDir, pkgID, version)
	if err := m.linkLibCopyBin(pkgID, version, val.Executables, pkgVersionDirname); err != nil {
		return err
	}
	if err := m.installConfig(pkgID, version, pkgVersionDirname); err != nil {
		return fmt.Errorf("install configuration for %s version %s: %w",
			pkgID, version, err)
	}
	if m.afterInstall != nil {
		m.afterInstall(pkgID)
	}
	return nil
}

// PackageVersionInUse returns the in-use version of pkgID by reading the
// `lib/<pkgID>` symlink target, which points at `pkg/<pkgID>/<version>`. It
// never contacts the release server, so it is safe on offline/remote hosts and
// always reflects the local source of truth. It returns false when the package
// has no in-use version installed locally.
func (m *Manager) PackageVersionInUse(
	_ context.Context, pkgID string,
) (release.Version, bool, error) {
	if err := validatePkgID(pkgID); err != nil {
		return "", false, fmt.Errorf("package id: %w", err)
	}
	libDir := makePackageLibDirname(m.dataDir, pkgID)
	target, err := os.Readlink(libDir)
	if err != nil {
		return "", false, nil
	}
	version := filepath.Base(target)
	if version == "" || version == "." || version == string(filepath.Separator) {
		return "", false, nil
	}
	return release.Version(version), true, nil
}

func (m *Manager) isPackageVersionInUse(
	pkgID string, version release.Version,
) (dirname string, libdirname string, isInUse bool, err error) {
	dirname = makePackageVersionDirname(m.dataDir, pkgID, version)
	libdirname = makePackageLibDirname(m.dataDir, pkgID)
	infodirname, serr := os.Stat(dirname)
	infolibdirname, lerr := os.Stat(libdirname)
	if serr != nil || lerr != nil {
		return
	}
	isInUse = os.SameFile(infodirname, infolibdirname)
	return
}

// versionInstalled reports whether version of pkgID is fully installed.
func (m *Manager) versionInstalled(
	ctx context.Context, pkgID string, version release.Version,
) (bool, error) {
	var val pkgVersionValue
	err := m.storage.Get(ctx, m.makeDownloadKey(pkgID, version), &val)
	if errors.Is(err, storageapi.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val.Complete, nil
}

func (m *Manager) cleanupFile(file *os.File) {
	if err := file.Close(); err != nil {
		m.log(log.WarnLevel, "close temp tar file: %v", err)
	}
	if err := os.Remove(file.Name()); err != nil {
		m.log(log.WarnLevel, "remove temp tar file: %v", err)
	}
}

func (m *Manager) makeDownloadKey(pkgID string, version release.Version) string {
	return fmt.Sprintf("%s:%s", pkgID, version)
}

func newPkgVersionValue(pkgID string, version release.Version) pkgVersionValue {
	return pkgVersionValue{Package: pkgID, Version: version}
}

func (m *Manager) download(
	ctx context.Context, pkgID string, version release.Version, tarfile *os.File,
	pw repl.ProgressWriter, key string,
) error {
	err := m.runDownload(ctx, pkgID, version, tarfile, pw, key)
	if err != nil {
		m.abortDownload(err, pkgID, version)
		return err
	}
	m.finishDownload(pkgID)
	return nil
}

// runDownload performs the fetch, extract, link and config steps for a
// single package version, returning the first error encountered. It owns
// the on-disk cleanup of partial state so download can keep the
// completion bookkeeping in one place. Cancelling ctx does not abort it.
func (m *Manager) runDownload(
	ctx context.Context, pkgID string, version release.Version, tarfile *os.File,
	pw repl.ProgressWriter, key string,
) error {
	ctx = context.WithoutCancel(ctx)
	defer m.cleanupFile(tarfile)

	if err := makePkgDirs(m.dataDir); err != nil {
		return err
	}

	writer := &progressTarWriter{
		Writer: tarfile,
		pw:     pw,
	}
	m.log(log.TraceLevel, "fetching package %s version %s", pkgID, version)
	bundle, err := m.m.Get(ctx, pkgID, version, writer)
	if err != nil {
		return translateVersionErr(err, pkgID, string(version))
	}
	var provenance *ProvenanceRecord
	if !gitpkg.IsGitPkgID(pkgID) {
		verified, err := m.trust.VerifyBundle(bundle, tarfile)
		if err != nil {
			return fmt.Errorf("verify package %s version %s: %w", pkgID, version, err)
		}
		provenance = &ProvenanceRecord{
			KeyID:       verified.KeyID,
			Fingerprint: verified.Fingerprint,
			Identity:    verified.PrimaryIdentity,
			Signature:   verified.Signature,
		}
	}

	m.log(log.TraceLevel, "extracting package %s version %s", pkgID, version)
	// extract to staging dir then atomically rename to final dir
	stagingDir := makeStagingDirname(m.dataDir, pkgID, version)
	_ = os.RemoveAll(stagingDir)
	pkgVersionDirname := makePackageVersionDirname(m.dataDir, pkgID, version)
	stagingConfigFile, executables, manifest, err := m.untar(tarfile, stagingDir, pw)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return err
	}
	if err := m.installRequirements(
		ctx, pkgID, version, stagingConfigFile, pw,
	); err != nil {
		_ = os.RemoveAll(stagingDir)
		return err
	}

	_ = os.RemoveAll(pkgVersionDirname)
	if err := os.Rename(stagingDir, pkgVersionDirname); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("rename staging dir: %w", err)
	}

	if err := m.linkLibCopyBin(pkgID, version, executables, pkgVersionDirname); err != nil {
		_ = os.RemoveAll(pkgVersionDirname)
		return err
	}
	if provenance != nil {
		manifestSHA256, err := manifest.SHA256()
		if err != nil {
			_ = os.RemoveAll(pkgVersionDirname)
			_ = removeExecutables(executables, m.binDir)
			return err
		}
		provenance.ManifestSHA256 = manifestSHA256
		if err := writeManifest(makeManifestFilename(m.dataDir, pkgID, version), manifest); err != nil {
			_ = os.RemoveAll(pkgVersionDirname)
			_ = removeExecutables(executables, m.binDir)
			return err
		}
	}

	updates := []storageapi.Update{
		{FieldPath: []string{"Executables"}, Value: executables},
		{FieldPath: []string{"Complete"}, Value: true},
	}
	if provenance != nil {
		updates = append(updates, storageapi.Update{FieldPath: []string{"Provenance"}, Value: provenance})
	}
	if err := m.storage.Update(ctx, key, updates); err != nil {
		_ = os.RemoveAll(pkgVersionDirname)
		_ = removeExecutables(executables, m.binDir)
		if provenance != nil {
			_ = os.Remove(makeManifestFilename(m.dataDir, pkgID, version))
		}
		return fmt.Errorf("update storage field: %w", err)
	}

	if err := m.installConfig(pkgID, version, pkgVersionDirname); err != nil {
		return fmt.Errorf("install configuration for %s version %s: %w",
			pkgID, version, err)
	}

	if m.afterInstall != nil {
		m.afterInstall(pkgID)
	}
	pw.Progress(1, 1, "done")
	return nil
}

// installRequirements installs, or upgrades to their latest version,
// the Rune packages listed under the top-level `requirements` key of
// the staged package config, and reports each change with
// ReportRequirement. It runs before the dependent package is promoted
// so each requirement's full install — including its config merge and
// gui.env live-apply — completes first and the dependent extension
// starts with the requirement's environment in place. A requirement
// whose latest version is installed but not in use was pinned by the
// user and stays put. A requirement in flight in another install is
// waited for, unless that install waits for this one, as in a cycle of
// requirements. A failed requirement install aborts the dependent
// install.
func (m *Manager) installRequirements(
	ctx context.Context, pkgID string, version release.Version,
	configFile string, pw repl.ProgressWriter,
) error {
	if _, err := os.Stat(configFile); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat staged config: %w", err)
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("read staged config: %w", err)
	}
	reqs, err := pkgConfigRequirements(
		configFile, data, pkgID, version, m.editorMode,
	)
	if err != nil {
		return fmt.Errorf("parse requirements: %w", err)
	}
	for _, req := range reqs {
		if err := validatePkgID(req); err != nil {
			return fmt.Errorf("requirement id: %w", err)
		}
		if req == pkgID {
			continue
		}
		inUse, installed, err := m.PackageVersionInUse(ctx, req)
		if err != nil {
			return fmt.Errorf("requirement %s in use: %w", req, err)
		}
		latest, err := m.LatestVersion(ctx, req)
		if err != nil {
			return fmt.Errorf("resolve requirement %s: %w", req, err)
		}
		if installed && inUse == latest {
			continue
		}
		if installed {
			pinned, err := m.versionInstalled(ctx, req, latest)
			if err != nil {
				return fmt.Errorf("requirement %s installed: %w", req, err)
			}
			if pinned {
				m.log(log.InfoLevel, "leaving requirement %s of package %s at %s: "+
					"%s is installed but not in use", req, pkgID, inUse, latest)
				continue
			}
			m.log(log.InfoLevel, "upgrading requirement %s from %s to %s for package %s",
				req, inUse, latest, pkgID)
		} else {
			m.log(log.InfoLevel, "installing requirement %s version %s of package %s",
				req, latest, pkgID)
		}
		replaced, err := m.install(ctx, req, latest,
			requirementProgressWriter{pkgID: req, pw: pw})
		switch {
		case errors.Is(err, errInstallInFlight), errors.Is(err, ErrAlreadyInstalled):
			// req is in flight in an install waiting for this one, which
			// finishes it, or the install of req this one waited for
			// installed latest and reported it.
			continue
		case err != nil:
			return fmt.Errorf("install requirement %s: %w", req, err)
		}
		ReportRequirement(m.ui(ctx), pw, RequirementChange{
			Package: req, From: replaced, To: latest, RequiredBy: pkgID,
		})
	}
	return nil
}

// requirementProgressWriter relays a requirement's install progress on
// the dependent install's writer. Samples are labeled with the
// requirement's package ID so the phase reset is attributable, and
// terminal samples are suppressed: notification writers auto-dismiss
// on progress==total, so only the outermost install may complete the
// notification.
type requirementProgressWriter struct {
	pkgID string
	pw    repl.ProgressWriter
}

func (w requirementProgressWriter) Progress(progress, total int64, units string) {
	if total > 0 && progress == total {
		return
	}
	w.pw.Progress(progress, total, units+" ("+w.pkgID+")")
}

// finishDownload releases the per-package install gate after a
// successful install so blocked LibDir iterators can proceed.
func (m *Manager) finishDownload(pkgID string) {
	m.iterators.Lock()
	defer m.iterators.Unlock()

	f, ok := m.iterators.m[pkgID]
	if !ok {
		panic("iterator for package not found")
	}
	delete(m.iterators.m, pkgID)
	f.ready.Unlock()
	close(f.done)
}

func (m *Manager) abortDownload(
	err error, pkgID string, version release.Version,
) {
	m.log(log.WarnLevel, "aborting installation of package %s version %s: %v",
		pkgID, version, err)
	key := m.makeDownloadKey(pkgID, version)
	if err := m.storage.Delete(context.Background(), key); err != nil {
		m.log(log.ErrorLevel, "delete pkg %s version %s "+
			"lock key (%s): %v", pkgID, version, key, err)
	}
	m.finishDownload(pkgID)
}

func (m *Manager) linkLibVersion(pkgID string, version release.Version) error {
	dirname := makePackageVersionDirname(m.dataDir, pkgID, version)
	libdirname := makePackageLibDirname(m.dataDir, pkgID)
	// Nested package IDs (github.com/owner/repo) need the symlink's
	// parent dirs to exist.
	if err := os.MkdirAll(filepath.Dir(libdirname), 0777); err != nil {
		return fmt.Errorf("mkdir lib parent dirs: %w", err)
	}
	tmpLink := libdirname + ".tmp"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(dirname, tmpLink); err != nil {
		return fmt.Errorf("symlink lib dir: %w", err)
	}
	if err := os.Rename(tmpLink, libdirname); err != nil {
		_ = os.Remove(tmpLink)
		return fmt.Errorf("rename lib symlink: %w", err)
	}
	return nil
}

func (m *Manager) linkLibCopyBin(
	pkgID string, version release.Version,
	executables []executableEntry, pkgVersionDirname string,
) error {
	m.log(log.TraceLevel, "linking package %s version %s library", pkgID, version)
	err := m.linkLibVersion(pkgID, version)
	if err != nil {
		return err
	}

	m.log(log.TraceLevel, "copying package %s version %s executables", pkgID, version)
	if err := copyExecutables(executables, pkgVersionDirname, m.binDir); err != nil {
		return err
	}
	return nil
}

// pkgConfigExts are the extensions a package config in the etc directory
// can have.
var pkgConfigExts = []string{".yaml", ".star"}

// installConfig makes the config of version of pkgID, the version in use
// extracted at versionDir, a layer of the user's config: it is copied to
// the etc directory and the user config includes it from there. A version
// without a config leaves the package with none. An include line the user
// commented out stays off. The hook of WithAfterConfigWrite runs last.
func (m *Manager) installConfig(
	pkgID string, version release.Version, versionDir string,
) error {
	ext, err := m.installEtcConfig(pkgID, version, versionDir)
	if err != nil {
		return err
	}
	err = m.editUserConfig(func(src []byte) ([]byte, error) {
		for _, other := range pkgConfigExts {
			if other != ext {
				src = removeInclude(src, m.configPath, includeSpelling(pkgID, other), m.dataDir)
			}
		}
		if ext == "" {
			return src, nil
		}
		return addInclude(src, m.configPath, includeSpelling(pkgID, ext), m.dataDir)
	})
	if err != nil {
		return err
	}
	if m.afterConfigWrite != nil {
		m.afterConfigWrite(pkgID)
	}
	return nil
}

// installEtcConfig writes the config of version of pkgID, read from
// versionDir, to the etc directory, and removes the config of the package
// in the other format. It returns the extension of the file written, empty
// when the version has no config and the package's was removed.
func (m *Manager) installEtcConfig(
	pkgID string, version release.Version, versionDir string,
) (string, error) {
	src := pkgConfigFile(versionDir)
	data, err := os.ReadFile(src)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read package config: %w", err)
		}
		return "", m.removeEtcConfig(pkgID)
	}
	ext := strings.ToLower(filepath.Ext(src))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, etcTrailer(pkgID, version)...)
	path := makePackageEtcPath(m.dataDir, pkgID, ext)
	if err := writeFileAtomic(path, data); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	for _, other := range pkgConfigExts {
		if other == ext {
			continue
		}
		if err := os.Remove(makePackageEtcPath(m.dataDir, pkgID, other)); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("remove stale package config: %w", err)
		}
	}
	m.log(log.InfoLevel, "installed the config of package %s version %s at %s",
		pkgID, version, path)
	return ext, nil
}

// etcTrailer is the comment that ends the copy of a package config in the
// etc directory. It trails the config so the line numbers Starlark errors
// report match the package's own file.
func etcTrailer(pkgID string, version release.Version) string {
	return fmt.Sprintf("\n# Installed by Rune from package %s %s. Rune overwrites this file when\n"+
		"# the package changes; put your own settings in your config instead.\n",
		pkgID, version)
}

// removeEtcConfig removes the config of pkgID from the etc directory, in
// either format.
func (m *Manager) removeEtcConfig(pkgID string) error {
	for _, ext := range pkgConfigExts {
		path := makePackageEtcPath(m.dataDir, pkgID, ext)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}

// uninstallConfig removes the config of pkgID from the etc directory and
// its include from the user config, and runs the hook of
// WithAfterConfigWrite when either was there.
func (m *Manager) uninstallConfig(pkgID string) error {
	hadEtc := false
	for _, ext := range pkgConfigExts {
		if _, err := os.Stat(makePackageEtcPath(m.dataDir, pkgID, ext)); err == nil {
			hadEtc = true
		}
	}
	if err := m.removeEtcConfig(pkgID); err != nil {
		return err
	}
	hadInclude := false
	err := m.editUserConfig(func(src []byte) ([]byte, error) {
		out := src
		for _, ext := range pkgConfigExts {
			out = removeInclude(out, m.configPath, includeSpelling(pkgID, ext), m.dataDir)
		}
		hadInclude = !bytes.Equal(out, src)
		return out, nil
	})
	if err != nil {
		return err
	}
	if (hadEtc || hadInclude) && m.afterConfigWrite != nil {
		m.afterConfigWrite(pkgID)
	}
	return nil
}

// editUserConfig rewrites the user config with edit, atomically, when edit
// changes it. A missing config is edited as an empty one.
func (m *Manager) editUserConfig(edit func(src []byte) ([]byte, error)) error {
	src, err := os.ReadFile(m.configPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read user config: %w", err)
	}
	out, err := edit(src)
	if err != nil {
		return fmt.Errorf("edit user config %s: %w", m.configPath, err)
	}
	if bytes.Equal(out, src) {
		return nil
	}
	if err := writeFileAtomic(m.configPath, out); err != nil {
		return fmt.Errorf("write user config: %w", err)
	}
	m.log(log.InfoLevel, "updated the includes of the user config %s", m.configPath)
	return nil
}

// writeFileAtomic writes data to path through a temporary file in its
// directory, which it creates, so a reader sees the old or the new file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (m *Manager) untar(
	tarfile *os.File, dirname string, pw repl.ProgressWriter,
) (string, []executableEntry, pkgtrust.Manifest, error) {
	if err := os.MkdirAll(dirname, 0777); err != nil {
		err = fmt.Errorf("mkdir: %w", err)
		return "", nil, nil, err
	}
	stat, err := tarfile.Stat()
	if err != nil {
		return "", nil, nil, fmt.Errorf("stat tarball file: %w", err)
	}
	totalBytes := stat.Size()
	_, err = tarfile.Seek(0, 0)
	if err != nil {
		err = fmt.Errorf("seek tarball file: %w", err)
		return "", nil, nil, err
	}

	// Count compressed bytes read from disk so progress tracks
	// against tarfile size (the only total we know up front).
	counter := &countingReader{r: tarfile}
	gzr, err := gzip.NewReader(counter)
	if err != nil {
		err = fmt.Errorf("new gzip reader: %w", err)
		return "", nil, nil, err
	}
	defer func() { _ = gzr.Close() }()

	executables, manifest, err := untar(dirname, gzr, func() {
		// Hold back the terminal extract sample so notification
		// writers that auto-dismiss on progress==total stay alive
		// until install actually completes.
		n := counter.n
		if n >= totalBytes {
			n = totalBytes - 1
		}
		scaledP, scaledT, unit := scaleBytes(n, totalBytes)
		pw.Progress(scaledP, scaledT, unit+" extracted")
	})
	if err != nil {
		err = fmt.Errorf("untar into %s: %w", dirname, err)
		return "", nil, nil, err
	}

	configFile := pkgConfigFile(dirname)
	return configFile, executables, manifest, nil
}

// pkgConfigFile returns the path to the package's settings file inside dir.
// Packages may ship either config.yaml (legacy) or config.star (mode-aware);
// when both exist, config.yaml wins. The returned path may not exist on disk —
// callers must stat it themselves.
//
// Some tarballs nest everything under a single top-level wrapper directory
// rather than the bundle contents, so the config ends up at
// <dir>/<wrapper>/config.{yaml,star}. When no top-level config is present,
// descend into a lone subdirectory to find it.
func pkgConfigFile(dir string) string {
	if path, ok := configFileIn(dir); ok {
		return path
	}
	if sub, ok := loneSubdir(dir); ok {
		if path, ok := configFileIn(sub); ok {
			return path
		}
	}
	return filepath.Join(dir, "config.yaml")
}

// configFileIn reports the package config file directly inside dir, preferring
// config.yaml over config.star, and whether one exists.
func configFileIn(dir string) (string, bool) {
	yamlPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath, true
	}
	starPath := filepath.Join(dir, "config.star")
	if _, err := os.Stat(starPath); err == nil {
		return starPath, true
	}
	return "", false
}

// loneSubdir returns the path of dir's single subdirectory when dir contains
// exactly one entry and that entry is a directory.
func loneSubdir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return "", false
	}
	return filepath.Join(dir, entries[0].Name()), true
}

// StarlarkParams returns the predeclared globals of a package's
// config.star: RUNE_DATADIR, RUNE_PKG_ID, RUNE_PKG_VERSION and
// RUNE_EDITOR_MODE. RUNE_DATADIR is bound to its own placeholder so a
// script that builds paths from it yields "$RUNE_DATADIR/..." and the host
// that uses the value resolves it. Empty pkgID, pkgVersion and editorMode
// leave their globals undefined.
func StarlarkParams(
	pkgID string, pkgVersion release.Version, editorMode string,
) map[string]any {
	params := map[string]any{"RUNE_DATADIR": "$" + hostenv.DataDirVar}
	if pkgID != "" {
		params["RUNE_PKG_ID"] = pkgID
	}
	if pkgVersion != "" {
		params["RUNE_PKG_VERSION"] = string(pkgVersion)
	}
	if editorMode != "" {
		params["RUNE_EDITOR_MODE"] = editorMode
	}
	return params
}

func newReadyIterator(
	ctx context.Context, scheme schemeapi.Scheme,
	schemeURI workspaceapi.URI, libDir string,
) iterator.Iterator[string] {
	it, err := walkdir.ListFiles(ctx, scheme, libDir)
	if err != nil {
		return iterator.Error[string](fmt.Errorf("list files: %v", err))
	}
	// make paths absolute
	return iterator.Map(it, func(filename string) string {
		path, _ := workspaceapi.ExpandPathWithURI(filename, schemeURI)
		return path
	})
}

func (m *Manager) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "idepkg.Manager").Logf(level, msg, args...)
}

type progressTarWriter struct {
	io.Writer
	pw repl.ProgressWriter
}

func (w *progressTarWriter) Progress(progress, total int64, units string) {
	if total <= 0 || progress > total {
		return
	}
	if progress == total {
		return
	}
	scaledP, scaledT, unit := scaleBytes(progress, total)
	w.pw.Progress(scaledP, scaledT, unit+" downloaded")
}

// scaleBytes picks a human-readable byte unit based on total and
// returns progress/total scaled to that unit. The unit is picked
// from total so it stays stable across successive samples.
func scaleBytes(progress, total int64) (int64, int64, string) {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
		tib = gib * 1024
	)
	switch {
	case total >= tib:
		return progress / tib, total / tib, "TiB"
	case total >= gib:
		return progress / gib, total / gib, "GiB"
	case total >= mib:
		return progress / mib, total / mib, "MiB"
	case total >= kib:
		return progress / kib, total / kib, "KiB"
	default:
		return progress, total, "B"
	}
}

// countingReader wraps an io.Reader to track the total number of
// bytes consumed. It is used to report extraction progress against
// the on-disk tarfile size — the only total known up front, since
// tar headers don't expose entry counts.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func isExecutable(info fs.FileInfo) bool {
	// check owner/group/other exec bits, if any
	// match then the file is an executable.
	return info.Mode()&os.ModeType == 0 && info.Mode()&0111 != 0
}

func untar(dst string, r io.Reader, onProgress func()) ([]executableEntry, pkgtrust.Manifest, error) {
	tr := tar.NewReader(r)

	var executables []executableEntry
	var manifest pkgtrust.Manifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("tar next: %w", err)
		}
		if onProgress != nil {
			onProgress()
		}

		target := filepath.Join(dst, filepath.Clean(hdr.Name))
		entry := pkgtrust.Entry{Path: filepath.ToSlash(hdr.Name), Mode: hdr.FileInfo().Mode()}
		if isExecutable(hdr.FileInfo()) && isPackageBin(hdr.Name) {
			executables = append(executables, executableEntry{
				Name: hdr.Name,
				Mode: hdr.Mode,
			})
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode()); err != nil {
				return nil, nil, fmt.Errorf("make dir %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return nil, nil, fmt.Errorf("make parent dirs for symlink %s: %w", target, err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return nil, nil, fmt.Errorf("symlink %s -> %s: %w", target, hdr.Linkname, err)
			}
			entry.Link = hdr.Linkname
		case tar.TypeLink:
			/* hard links are ignored */
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return nil, nil, fmt.Errorf("make parent dirs: %w", err)
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC,
				hdr.FileInfo().Mode())
			if err != nil {
				return nil, nil, fmt.Errorf("create file %s: %w", target, err)
			}
			hash := sha256.New()
			_, err = io.Copy(io.MultiWriter(f, hash), tr)
			_ = f.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("write file %s: %w", target, err)
			}
			entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
		}
		manifest = append(manifest, entry)
	}
	return executables, manifest, nil
}

func copyExecutables(files []executableEntry, dirname, targetdirname string) error {
	var ret error
	for _, executable := range files {
		// Install records written by older releases list executables
		// found anywhere in the package.
		if !isPackageBin(executable.Name) {
			continue
		}
		name := filepath.Clean(executable.Name)
		orig := filepath.Join(dirname, name)
		origfile, err := os.OpenFile(orig, os.O_RDONLY, 0)
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("open executable: %w", err))
			continue
		}
		if err := swapExecutable(origfile, targetdirname, name, executable.Mode); err != nil {
			ret = multierror.Append(ret, err)
		}
		_ = origfile.Close()
	}
	return ret
}

func swapExecutable(orig *os.File, targetdirname, name string, mode int64) error {
	target := filepath.Join(targetdirname, filepath.Base(name))
	tmp, err := os.CreateTemp(targetdirname, filepath.Base(name)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp executable for %s: %w", target, err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, orig); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("copy executable %s: %w", target, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp executable for %s: %w", target, err)
	}
	if err := os.Chmod(tmpName, os.FileMode(mode)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod executable %s: %w", target, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename executable %s: %w", target, err)
	}
	if err := clearQuarantine(target); err != nil {
		return fmt.Errorf("clear quarantine on %s: %w", target, err)
	}
	return nil
}

func isPackageBin(name string) bool {
	dir, file := path.Split(path.Clean(name))
	return dir == "bin/" && !strings.HasPrefix(file, ".")
}

func removeExecutables(files []executableEntry, targetdirname string) error {
	var ret error
	for _, executable := range files {
		name := filepath.Clean(executable.Name)
		target := filepath.Join(targetdirname, filepath.Base(name))
		err := os.Remove(target)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			ret = multierror.Append(ret, fmt.Errorf("remove executable %s: %w", target, err))
			continue
		}
	}
	return ret
}

func makeBinDirname(dataDir string) string {
	return filepath.Join(dataDir, "bin")
}

func makeLibDirname(dataDir string) string {
	return filepath.Join(dataDir, "lib")
}

func makePackageVersionDirname(
	dataDir, pkgID string, version release.Version,
) string {
	return filepath.Join(dataDir, "pkg", pkgID, string(version))
}

func makePkgDirs(dataDir string) error {
	binDir := makeBinDirname(dataDir)
	libDir := makeLibDirname(dataDir)
	targets := []string{binDir, libDir}
	for _, target := range targets {
		err := os.MkdirAll(target, 0777)
		if err != nil {
			return fmt.Errorf("mkdir dir %s: %w", target, err)
		}
	}
	return nil
}

func makePackageLibDirname(
	dataDir, pkgID string,
) string {
	return filepath.Join(dataDir, "lib", pkgID)
}

// PackageLibDir returns the path of the symlink that names the in-use
// version of pkgID, <dataDir>/lib/<pkgID>. The symlink is absent when no
// version is in use.
func PackageLibDir(dataDir, pkgID string) string {
	return makePackageLibDirname(dataDir, pkgID)
}

// EtcDir returns the directory the package manager writes the config of
// each installed package to, <dataDir>/etc. The config of a package is at
// <pkgID>.yaml or <pkgID>.star under it.
func EtcDir(dataDir string) string {
	return makeEtcDirname(dataDir)
}

func makeEtcDirname(dataDir string) string {
	return filepath.Join(dataDir, "etc")
}

func makePackageEtcPath(dataDir, pkgID, ext string) string {
	return filepath.Join(makeEtcDirname(dataDir), pkgID+ext)
}

func makeStagingDirname(dataDir, pkgID string, version release.Version) string {
	return filepath.Join(dataDir, "pkg", pkgID, ".staging-"+string(version))
}

type pkgVersionValue struct {
	Package     string
	Version     release.Version
	Executables []executableEntry
	Complete    bool
	Provenance  *ProvenanceRecord
}

// ProvenanceRecord stores the host-verified origin of an installed bundle.
type ProvenanceRecord struct {
	KeyID          string
	Fingerprint    string
	Identity       string
	Signature      string
	ManifestSHA256 string
}

func makeManifestFilename(dataDir, pkgID string, version release.Version) string {
	return filepath.Join(dataDir, "pkg", pkgID, ".manifest-"+string(version)+".json")
}

func writeManifest(path string, manifest pkgtrust.Manifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal package manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write package manifest: %w", err)
	}
	return nil
}

// VerifyExtensionEntrypoint returns the trusted signing-key fingerprint for a
// verified installed extension entrypoint. It verifies only extension identity
// code; toolchain executables are command inputs and are intentionally omitted.
func (m *Manager) VerifyExtensionEntrypoint(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		m.log(log.WarnLevel, "resolve extension entrypoint %s: %v", path, err)
		return "", false
	}
	// Package configs point at the shared bin copy
	// ($RUNE_DATADIR/bin/<name>), which lives outside the package
	// version dir, so entrypoints are matched by manifest path there too.
	sharedBin, err := filepath.EvalSymlinks(makeBinDirname(m.dataDir))
	if err != nil {
		sharedBin = ""
	}
	dit, err := m.storage.List(context.Background(), nil)
	if err != nil {
		m.log(log.WarnLevel, "list package provenance: %v", err)
		return "", false
	}
	entries, err := iterator.ToSlice(context.Background(), iterator.FromDocumentIterator[pkgVersionValue](dit))
	if err != nil {
		m.log(log.WarnLevel, "read package provenance: %v", err)
		return "", false
	}
	for _, installed := range entries {
		if !installed.Complete || installed.Provenance == nil {
			continue
		}
		dir := makePackageVersionDirname(m.dataDir, installed.Package, installed.Version)
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		var entryPath string
		var sharedCopy bool
		rel, err := filepath.Rel(resolvedDir, resolved)
		switch {
		case err == nil && rel != "." && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)):
			entryPath = filepath.ToSlash(rel)
		case sharedBin != "" && filepath.Dir(resolved) == sharedBin:
			// The install step that writes the shared copy also
			// repoints the package's lib symlink, so only the
			// lib-linked version can vouch for the copy; superseded
			// versions still installed must not veto it.
			if _, _, inUse, uerr := m.isPackageVersionInUse(
				installed.Package, installed.Version); uerr != nil || !inUse {
				continue
			}
			entryPath = "bin/" + filepath.Base(resolved)
			sharedCopy = true
		default:
			continue
		}
		manifest, err := readManifest(makeManifestFilename(m.dataDir, installed.Package, installed.Version))
		if err != nil {
			m.log(log.WarnLevel, "read package manifest for %s: %v", installed.Package, err)
			return "", false
		}
		digest, err := manifest.SHA256()
		if err != nil || digest != installed.Provenance.ManifestSHA256 {
			m.log(log.WarnLevel, "package manifest provenance mismatch for %s", installed.Package)
			return "", false
		}
		configPath, err := filepath.Rel(dir, pkgConfigFile(dir))
		if err != nil {
			return "", false
		}
		configPath = filepath.ToSlash(configPath)
		wanted := make(map[string]pkgtrust.Entry)
		for _, entry := range manifest {
			normalized := manifestEntryPath(entry.Path)
			if normalized == entryPath || normalized == configPath ||
				isLibraryEntry(normalized) {
				wanted[normalized] = entry
			}
		}
		if _, ok := wanted[entryPath]; !ok {
			if sharedCopy {
				// The copy may belong to any other installed package.
				continue
			}
			m.log(log.WarnLevel, "extension entrypoint %s is absent from package manifest", resolved)
			return "", false
		}
		if _, err := os.Stat(pkgConfigFile(dir)); err != nil {
			m.log(log.WarnLevel, "package config is unavailable for %s: %v", installed.Package, err)
			return "", false
		}
		if _, ok := wanted[configPath]; !ok {
			m.log(log.WarnLevel, "package config is absent from manifest for %s", installed.Package)
			return "", false
		}
		if err := pkgtrust.VerifyEntries(dir, manifestEntries(wanted)); err != nil {
			m.log(log.WarnLevel, "package integrity check failed for %s: %v", installed.Package, err)
			return "", false
		}
		// Trusting the copy requires it to still match the signed
		// original it was made from; its manifest path is relative to
		// the datadir, where the copy lives.
		if sharedCopy {
			if err := pkgtrust.VerifyEntries(m.dataDir,
				[]pkgtrust.Entry{wanted[entryPath]}); err != nil {
				m.log(log.WarnLevel, "bin copy integrity check failed for %s: %v",
					installed.Package, err)
				return "", false
			}
		}
		return installed.Provenance.Fingerprint, true
	}
	return "", false
}

// manifestEntryPath normalizes a manifest path for comparison with paths
// built from the filesystem: published tarballs are packed with
// `tar -c .`, so their entries carry a "./" prefix.
func manifestEntryPath(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "./")
}

func readManifest(path string) (pkgtrust.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest pkgtrust.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func manifestEntries(entries map[string]pkgtrust.Entry) []pkgtrust.Entry {
	result := make([]pkgtrust.Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	return result
}

func isLibraryEntry(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".so" || ext == ".dylib" || ext == ".dll"
}

type executableEntry struct {
	Name string
	Mode int64
}

// validatePkgPath rejects package ids and versions that could escape the
// datadir once joined into on-disk paths (pkg/<id>/<version>, lib/<id>).
// Slashes are allowed so hierarchical ids such as github.com/owner/repo
// nest as real directories and $RUNE_PKG_ID expands to the real id; only
// empty, "." and ".." segments are traversal-unsafe and rejected.
func validatePkgPath(val string) error {
	if val == "" {
		return errors.New("must not be empty")
	}
	for seg := range strings.SplitSeq(val, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path segment %q in %q", seg, val)
		}
	}
	return nil
}

// validatePkgID is validatePkgPath for package ids, rejecting a git
// remote URL with the package id to use instead: a URL fails
// validatePkgPath with an opaque segment error or, in the scp-like SSH
// form, passes it and is looked up as an official package.
func validatePkgID(id string) error {
	if err := gitpkg.RemoteURLError(id); err != nil {
		return err
	}
	return validatePkgPath(id)
}

// Reconcile cleans up incomplete installs left by a previous crash.
// It should be called once at startup, before any new installs.
func (m *Manager) Reconcile(ctx context.Context) error {
	pkgRoot := filepath.Join(m.dataDir, "pkg")
	dit, err := m.storage.List(ctx, nil)
	if err != nil {
		return fmt.Errorf("list storage entries: %w", err)
	}
	it := iterator.FromDocumentIterator[pkgVersionValue](dit)
	entries, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return fmt.Errorf("read storage entries: %w", err)
	}
	manifests := make(map[string]struct{})
	versionDirs := make(map[string]struct{})
	for _, pkv := range entries {
		if pkv.Complete && pkv.Provenance != nil {
			manifests[makeManifestFilename(m.dataDir, pkv.Package, pkv.Version)] = struct{}{}
		}
		if pkv.Package != "" && pkv.Version != "" {
			versionDirs[makePackageVersionDirname(m.dataDir, pkv.Package, pkv.Version)] = struct{}{}
		}
	}
	// Staging dirs and manifests live next to the version dirs under
	// pkg/<id>/; the version dirs themselves are whole toolchains (tens of
	// thousands of entries) that the sweep has no business descending into.
	_ = filepath.WalkDir(pkgRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if _, ok := versionDirs[path]; ok {
			return filepath.SkipDir
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			_ = os.RemoveAll(path)
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".manifest-") {
			if _, ok := manifests[path]; !ok {
				_ = os.Remove(path)
			}
		}
		return nil
	})

	for _, pkv := range entries {
		if pkv.Package == "" || pkv.Version == "" {
			continue
		}
		key := m.makeDownloadKey(pkv.Package, pkv.Version)
		dirname := makePackageVersionDirname(m.dataDir, pkv.Package, pkv.Version)
		if !pkv.Complete {
			_ = os.RemoveAll(dirname)
			// The depth-1 staging sweep above cannot see staging dirs of
			// nested package IDs (github.com/owner/repo); remove them here.
			_ = os.RemoveAll(makeStagingDirname(m.dataDir, pkv.Package, pkv.Version))
			// Remove lib symlink if it points to the stale version.
			libdirname := makePackageLibDirname(m.dataDir, pkv.Package)
			if target, lerr := os.Readlink(libdirname); lerr == nil {
				if target == dirname {
					_ = os.Remove(libdirname)
					_ = removeExecutables(pkv.Executables, m.binDir)
				}
			}
			_ = m.storage.Delete(ctx, key)
			_ = os.Remove(makeManifestFilename(m.dataDir, pkv.Package, pkv.Version))
			continue
		}
		// Phase 4: Verify complete entries — if dir is missing, delete storage.
		if _, serr := os.Stat(dirname); os.IsNotExist(serr) {
			_ = m.storage.Delete(ctx, key)
			_ = os.Remove(makeManifestFilename(m.dataDir, pkv.Package, pkv.Version))
		}
	}

	// Phase 3: Clean stale .tmp symlinks in lib/.
	libRoot := makeLibDirname(m.dataDir)
	if libEntries, err := os.ReadDir(libRoot); err == nil {
		for _, entry := range libEntries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				_ = os.Remove(filepath.Join(libRoot, entry.Name()))
			}
		}
	}

	return nil
}

type libDirIterator struct {
	ready     *sync.Mutex
	it        iterator.Iterator[string]
	scheme    schemeapi.Scheme
	schemeURI workspaceapi.URI
	libDir    string
}

func newPendingIterator(
	mu *sync.Mutex, scheme schemeapi.Scheme,
	schemeURI workspaceapi.URI, libDir string,
) *libDirIterator {
	return &libDirIterator{
		ready:     mu,
		scheme:    scheme,
		schemeURI: schemeURI,
		libDir:    libDir,
	}
}

func (l *libDirIterator) Next(ctx context.Context) (string, bool) {
	l.ready.Lock()
	defer l.ready.Unlock()
	if l.it == nil {
		l.it = newReadyIterator(context.Background(), l.scheme, l.schemeURI, l.libDir)
	}
	return l.it.Next(ctx)
}

func (l *libDirIterator) Err() error {
	l.ready.Lock()
	defer l.ready.Unlock()
	if l.it == nil {
		l.it = newReadyIterator(context.Background(), l.scheme, l.schemeURI, l.libDir)
	}
	return l.it.Err()
}

func (l *libDirIterator) Close() error {
	l.ready.Lock()
	defer l.ready.Unlock()
	if l.it == nil {
		return nil
	}
	return l.it.Close()
}
