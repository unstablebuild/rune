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
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/gitpkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/ide/starlarkconfig"
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
	configPath string, wm browserapi.WindowManager,
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
		wm:               wm,
		scheduleNextTick: scheduleNextTick,
		n:                n,
		m:                m,
		storage:          storage,
		trust:            trust,
	}
	ret.iterators.m = make(map[string]*sync.Mutex)
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
	n                browserapi.Notifications
	m                release.Manager
	interrupter      term.Interrupter
	wm               browserapi.WindowManager
	parser           syntaxapi.Parser
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

	// configBase returns the editor's default config tree, used as the
	// predeclared `config` base when reading the user's .star config so
	// overlay-style mutations (config[...] = ...) resolve. May be nil.
	configBase func() map[string]any

	// afterConfigMerge, when set, is invoked after a package config merge is
	// written to the user config file. See WithAfterConfigMerge.
	afterConfigMerge func(ConfigMergeEvent) (ConfigMergeResult, error)

	iterators struct {
		sync.Mutex
		m map[string]*sync.Mutex
	}
}

// LibDir returns an iterator to the lib directory of the given package.
// The paths returned by the iterator are always absolute.
func (m *Manager) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	m.iterators.Lock()
	defer m.iterators.Unlock()

	libDir := makePackageLibDirname(m.dataDir, pkgID)
	ready, ok := m.iterators.m[pkgID]
	if ok {
		return newPendingIterator(ready, m.scheme, m.schemeURI, libDir), nil
	}

	_, err := os.Stat(libDir)
	if err != nil {
		return nil, ErrNotInstalled
	}

	return newReadyIterator(ctx, m.scheme, m.schemeURI, libDir), nil
}

// DescribePackage fetches a Package manifest.
func (m *Manager) DescribePackage(ctx context.Context, pkgID string) (release.Package, error) {
	if err := validatePkgPath(pkgID); err != nil {
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
	if err := validatePkgPath(pkgID); err != nil {
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
	if err := validatePkgPath(pkgID); err != nil {
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
// callers can report success or failure directly.
func (m *Manager) InstallPackageVersion(
	ctx context.Context, pkgID string, version release.Version,
	pw repl.ProgressWriter,
) error {
	if pkgID == "" || version == "" {
		return errors.New("package and version must not be empty")
	}
	if pw == nil {
		pw = repl.NopProgressWriter()
	}

	if err := validatePkgPath(pkgID); err != nil {
		return fmt.Errorf("package id: %w", err)
	}
	if err := validatePkgPath(string(version)); err != nil {
		return fmt.Errorf("release version: %w", err)
	}

	m.iterators.Lock()
	_, ok := m.iterators.m[pkgID]
	if ok {
		m.log(log.InfoLevel, "there's already an ongoing install of package: %s", pkgID)
		m.iterators.Unlock()
		return nil
	}

	tarfile, err := os.CreateTemp("", "")
	if err != nil {
		m.iterators.Unlock()
		return fmt.Errorf("create temp: %w", err)
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
				return fmt.Errorf("version %s of package %s has "+
					"already been installed: %w", version, pkgID,
					ErrAlreadyInstalled)
			}
		} else {
			m.cleanupFile(tarfile)
			m.iterators.Unlock()
			return fmt.Errorf("store package version: %w", err)
		}
	}

	mu := new(sync.Mutex)
	m.iterators.m[pkgID] = mu
	mu.Lock() // block calls to iterator
	m.iterators.Unlock()

	return m.download(pkgID, version, tarfile, pw, key)
}

// DeletePackageVersion deletes a package version from local storage. This method is idempotent.
func (m *Manager) DeletePackageVersion(
	ctx context.Context, pkgID string, version release.Version, force bool,
) (ret error) {
	if pkgID == "" || version == "" {
		return errors.New("package and version must not be empty")
	}
	if err := validatePkgPath(pkgID); err != nil {
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
	if err := validatePkgPath(pkgID); err != nil {
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

// ProcessInstalledSettings processes settings by packages.
func (m *Manager) ProcessInstalledSettings(ctx context.Context) (ret error) {
	dit, err := m.storage.List(ctx, nil)
	if err != nil {
		return err
	}
	it := iterator.FromDocumentIterator[pkgVersionValue](dit)
	slice, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return err
	}
	for _, pkv := range slice {
		if !pkv.Complete {
			continue
		}
		_, _, isInUse, err := m.isPackageVersionInUse(pkv.Package, pkv.Version)
		if err != nil {
			err = fmt.Errorf("could not check if package %s version %s is in use: %v",
				pkv.Package, pkv.Version, err)
			ret = errors.Join(ret, err)
			continue
		}
		if !isInUse {
			continue
		}
		dir := makePackageVersionDirname(m.dataDir, pkv.Package, pkv.Version)
		configFile := pkgConfigFile(dir)
		err = m.processConfig(pkv.Package, pkv.Version, configFile)
		if err != nil {
			ret = errors.Join(ret, fmt.Errorf("process %s: %w", configFile, err))
		}
	}
	return ret
}

// ListInstalledPackageVersions lists the bundles installed for the given package.
func (m *Manager) ListInstalledPackageVersions(ctx context.Context, pkgID string) (
	iterator.Iterator[release.Version], error,
) {
	if pkgID == "" {
		return nil, errors.New("package must not be empty")
	}
	if err := validatePkgPath(pkgID); err != nil {
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
	if err := validatePkgPath(pkgID); err != nil {
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

	configFile := pkgConfigFile(pkgVersionDirname)
	err = m.processConfig(pkgID, version, configFile)
	if err != nil {
		return err
	}
	return m.linkLibCopyBin(pkgID, version, val.Executables, pkgVersionDirname)
}

// PackageVersionInUse returns the in-use version of pkgID by reading the
// `lib/<pkgID>` symlink target, which points at `pkg/<pkgID>/<version>`. It
// never contacts the release server, so it is safe on offline/remote hosts and
// always reflects the local source of truth. It returns false when the package
// has no in-use version installed locally.
func (m *Manager) PackageVersionInUse(pkgID string) (release.Version, bool) {
	if validatePkgPath(pkgID) != nil {
		return "", false
	}
	libDir := makePackageLibDirname(m.dataDir, pkgID)
	target, err := os.Readlink(libDir)
	if err != nil {
		return "", false
	}
	version := filepath.Base(target)
	if version == "" || version == "." || version == string(filepath.Separator) {
		return "", false
	}
	return release.Version(version), true
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
	pkgID string, version release.Version, tarfile *os.File,
	pw repl.ProgressWriter, key string,
) error {
	err := m.runDownload(pkgID, version, tarfile, pw, key)
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
// completion bookkeeping in one place.
func (m *Manager) runDownload(
	pkgID string, version release.Version, tarfile *os.File,
	pw repl.ProgressWriter, key string,
) error {
	ctx := context.Background()
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

	configFile := pkgConfigFile(pkgVersionDirname)

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

	if err := m.processInstalledConfig(pkgID, version, configFile); err != nil {
		return fmt.Errorf("process configuration for %s version %s: %w",
			pkgID, version, err)
	}

	pw.Progress(1, 1, "done")
	return nil
}

// installRequirements installs the Rune packages listed under the
// top-level `requirements` key of the staged package config. It runs
// before the dependent package is promoted so each requirement's full
// install — including its config merge and gui.env live-apply —
// completes first and the dependent extension starts with the
// requirement's environment in place. A failed requirement install
// aborts the dependent install.
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
		configFile, data, pkgID, version, m.dataDir, m.editorMode,
	)
	if err != nil {
		return fmt.Errorf("parse requirements: %w", err)
	}
	for _, req := range reqs {
		if err := validatePkgPath(req); err != nil {
			return fmt.Errorf("requirement id: %w", err)
		}
		if req == pkgID {
			continue
		}
		if _, installed := m.PackageVersionInUse(req); installed {
			continue
		}
		reqVersion, err := m.LatestVersion(ctx, req)
		if err != nil {
			return fmt.Errorf("resolve requirement %s: %w", req, err)
		}
		m.log(log.InfoLevel, "installing requirement %s version %s of package %s",
			req, reqVersion, pkgID)
		err = m.InstallPackageVersion(ctx, req, reqVersion,
			requirementProgressWriter{pkgID: req, pw: pw})
		if err != nil && !errors.Is(err, ErrAlreadyInstalled) {
			return fmt.Errorf("install requirement %s: %w", req, err)
		}
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

	ready, ok := m.iterators.m[pkgID]
	if !ok {
		panic("iterator for package not found")
	}
	delete(m.iterators.m, pkgID)
	ready.Unlock()
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

func (m *Manager) promptConfigChange(
	pkgID string, pkgVersion release.Version, configYAML []byte,
	userDoc, pkgDoc *yaml.Node,
) error {
	message := fmt.Sprintf(
		"Extension %s (version %s) wants to **update** your configuration "+
			"with the following settings:\n\n```yaml\n%s\n```\n\nDo you want to allow this?",
		pkgID, pkgVersion, string(configYAML))
	return m.promptConfigMerge(
		pkgID, pkgVersion, message,
		[]string{"    Allow    ", "    Deny    "},
		[]term.KeyComb{{Ch: 'a'}, {Ch: 'd'}}, userDoc, pkgDoc,
	)
}

func (m *Manager) promptExtensionPathChange(
	pkgID string, pkgVersion release.Version, userDoc *yaml.Node,
	change extensionPathChange,
) error {
	message := fmt.Sprintf(
		"Package **%s** wants to install extension **%s** at:\n\n`%s`\n\n"+
			"An extension with the same ID is already registered at:\n\n`%s`\n\n"+
			"Do you want to replace it?",
		pkgID, change.extensionID, change.installedPath, change.currentPath)
	return m.promptConfigMergeWithResult(
		pkgID, pkgVersion, message,
		[]string{"    Yes    ", "    No    "},
		[]term.KeyComb{{Ch: 'y'}, {Ch: 'n'}}, userDoc, change.pkgDoc,
		false,
		80,
		func(_ ConfigMergeResult) {
			_, _ = m.n.Notify(browserapi.LevelSuccess,
				"updated %s extension path. Restart the program to load the changes.", pkgID)
		},
	)
}

func (m *Manager) promptConfigMerge(
	pkgID string, pkgVersion release.Version, message string,
	options []string, bindings []term.KeyComb, userDoc, pkgDoc *yaml.Node,
) error {
	return m.promptConfigMergeWithResult(
		pkgID, pkgVersion, message, options, bindings, userDoc, pkgDoc,
		true,
		0,
		func(result ConfigMergeResult) {
			m.notifyConfigApplied(browserapi.LevelSuccess, pkgID, result)
		},
	)
}

func (m *Manager) promptConfigMergeWithResult(
	pkgID string, pkgVersion release.Version, message string,
	options []string, bindings []term.KeyComb, userDoc, pkgDoc *yaml.Node,
	runAfterMerge bool,
	maxWidth int,
	onApplied func(ConfigMergeResult),
) error {
	newMessage := markdownOrFallback(m.parser, m.scheduleNextTick)
	if maxWidth > 0 {
		newMessage = boundedFloatingMessage(newMessage, maxWidth)
	}

	prompt := handler.NewPrompt(handler.PromptConfig{
		HighlightAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorRed,
		},
		OptionAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorGray,
		},
		OptionBindings: bindings,
		PromptConfig: component.PromptConfig{
			Message:    message,
			Options:    options,
			NewMessage: newMessage,
		},
		PromptHandler: handler.FuncPromptHandler(func(idx int, _ string) {
			allowed := idx == 0
			if !allowed {
				return
			}
			var result ConfigMergeResult
			var err error
			if runAfterMerge {
				result, err = m.applyConfigMerge(pkgID, pkgVersion, userDoc, pkgDoc)
			} else {
				err = m.writeConfigMerge(userDoc, pkgDoc)
			}
			if err != nil {
				_, _ = m.n.Notify(browserapi.LevelError, "apply configuration: %s", err)
				return
			}
			onApplied(result)
		}, func() error { return nil }),
	})

	ok := m.scheduleNextTick(func() {
		_, err := m.wm.Floating(prompt, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			_, _ = m.n.Notify(browserapi.LevelError, "show config prompt: %s", err)
		}
	})
	if !ok {
		m.log(log.ErrorLevel, "idepkg config prompt: could not schedule")
		return nil
	}

	return nil
}

func boundedFloatingMessage(
	newMessage func(string) component.Floating, maxWidth int,
) func(string) component.Floating {
	return func(message string) component.Floating {
		return &maxWidthFloating{
			Floating: newMessage(message),
			maxWidth: maxWidth,
		}
	}
}

type maxWidthFloating struct {
	component.Floating
	maxWidth int
}

func (f *maxWidthFloating) Dimensions() (width, height int) {
	width, height = f.Floating.Dimensions()
	if width <= f.maxWidth {
		return width, height
	}
	return f.maxWidth, f.Floating.(component.Responsive).Height(f.maxWidth)
}

// ConfigMergeEvent describes a package config merge that was just written to
// the user config file. The applied diff (Diff) carries only the keys that
// were merged. Consumers inspect it to decide whether any live reload is
// possible; the idepkg package itself is unaware of what the keys mean.
type ConfigMergeEvent struct {
	PkgID      string
	PkgVersion release.Version
	// Diff is the YAML document node for the applied diff. Its first content
	// child is the mapping of merged keys.
	Diff *yaml.Node
}

// TouchesPath reports whether the applied diff includes the given nested key
// path, e.g. TouchesPath("gui", "env").
func (e ConfigMergeEvent) TouchesPath(path ...string) bool {
	return configDiffTouchesPath(e.Diff, path...)
}

// AddedExtensionIDs returns the ids added under the top-level "extensions"
// key of the applied diff, or nil when the diff did not add any.
func (e ConfigMergeEvent) AddedExtensionIDs() []string {
	return addedExtensionIDs(e.Diff)
}

// AddedTutorialNames returns the names added under the top-level "tutorials"
// key of the applied diff, or nil when the diff did not add any.
func (e ConfigMergeEvent) AddedTutorialNames() []string {
	return addedTutorialNames(e.Diff)
}

// ConfigMergeResult reports what a post-merge hook did. LiveApplied is true
// when the hook applied changes to the running process such that a full
// restart is not required for new work to observe them.
type ConfigMergeResult struct {
	LiveApplied bool
}

// applyConfigMerge deep-merges addDoc into userDoc and writes the result to
// the user config file atomically, after backing up the existing file. It is
// shared by the auto-apply path (purely-new keys) and the prompt's Allow path
// (version-dependent conflicts the user approved). On success it invokes the
// post-merge hook (if configured) and returns its result.
func (m *Manager) applyConfigMerge(
	pkgID string, pkgVersion release.Version, userDoc, addDoc *yaml.Node,
) (ConfigMergeResult, error) {
	if err := m.writeConfigMerge(userDoc, addDoc); err != nil {
		return ConfigMergeResult{}, err
	}
	return m.runAfterConfigMerge(pkgID, pkgVersion, addDoc)
}

func (m *Manager) writeConfigMerge(userDoc, addDoc *yaml.Node) error {
	starConfig := m.starUserConfig()
	merged, err := buildMergedConfig(userDoc, addDoc, starConfig)
	if err != nil {
		return err
	}

	backup, err := backupUserConfig(m.configPath)
	if err != nil {
		return fmt.Errorf("backup user config: %w", err)
	}

	m.log(log.InfoLevel, "created config backup "+
		"before applying package updates: %s", backup)

	if starConfig {
		if err := starlarkconfig.WriteManagedConfigFileAtomic(m.configPath, merged.starDiff); err != nil {
			return fmt.Errorf("write starlark config: %w", err)
		}
		return nil
	}
	if err := writeYAMLAtomic(m.configPath, merged.yamlDoc, addDoc.Content[0]); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (m *Manager) runAfterConfigMerge(
	pkgID string, pkgVersion release.Version, addDoc *yaml.Node,
) (ConfigMergeResult, error) {
	if m.afterConfigMerge == nil {
		return ConfigMergeResult{}, nil
	}
	return m.afterConfigMerge(ConfigMergeEvent{
		PkgID:      pkgID,
		PkgVersion: pkgVersion,
		Diff:       addDoc,
	})
}

// notifyConfigApplied reports a successful config merge. When the post-merge
// hook live-applied changes, no further action is requested from the user;
// otherwise it keeps the restart-oriented wording.
func (m *Manager) notifyConfigApplied(
	level browserapi.NotificationLevel, pkgID string, result ConfigMergeResult,
) {
	if result.LiveApplied {
		_, _ = m.n.Notify(level, "applied %s configuration updates. ", pkgID)
		return
	}
	_, _ = m.n.Notify(level, "applied %s configuration updates. "+
		"Restart the program to load the changes.", pkgID)
}

func (m *Manager) processConfig(
	pkgID string, pkgVersion release.Version, pkgConfigFile string,
) error {
	return m.processConfigFile(pkgID, pkgVersion, pkgConfigFile, false)
}

func (m *Manager) processInstalledConfig(
	pkgID string, pkgVersion release.Version, pkgConfigFile string,
) error {
	return m.processConfigFile(pkgID, pkgVersion, pkgConfigFile, true)
}

func (m *Manager) processConfigFile(
	pkgID string, pkgVersion release.Version, pkgConfigFile string,
	promptExtensionPaths bool,
) error {
	_, err := os.Stat(pkgConfigFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %v", err)
	}
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(pkgConfigFile)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	// A fresh datadir has no user config yet. Seed an empty one so the
	// package's env/settings still merge; otherwise a first install (e.g. a
	// remote `rune -x` provisioning into a brand-new ~/.rune) never gets
	// GOROOT and the toolchain fails with "cannot find GOROOT directory".
	if _, statErr := os.Stat(m.configPath); os.IsNotExist(statErr) {
		if err := os.MkdirAll(filepath.Dir(m.configPath), 0o777); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		if err := os.WriteFile(m.configPath, nil, 0o644); err != nil {
			return fmt.Errorf("seed empty user config: %w", err)
		}
	}

	userCfg, err := loadIdePkgConfigFile(m.configPath, m.configBaseTree())
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}

	userDoc, err := m.userConfigDocument()
	if err != nil {
		return fmt.Errorf("load user config document: %w", err)
	}

	plan, err := planConfigChange(
		pkgConfigFile, data, userCfg, userDoc,
		pkgID, pkgVersion, m.dataDir, m.editorMode, promptExtensionPaths,
	)
	if err != nil {
		return err
	}

	if plan.autoApplyDoc != nil {
		result, err := m.applyConfigMerge(pkgID, pkgVersion, plan.userDoc, plan.autoApplyDoc)
		if err != nil {
			return fmt.Errorf("auto-apply config change: %w", err)
		}
		m.notifyConfigApplied(browserapi.LevelInfo, pkgID, result)
	}
	for _, change := range plan.pathChanges {
		if err := m.promptExtensionPathChange(
			pkgID, pkgVersion, plan.userDoc, change,
		); err != nil {
			return fmt.Errorf("prompt extension path change: %w", err)
		}
	}

	if plan.prompt {
		err = m.promptConfigChange(
			pkgID, pkgVersion, plan.missingYAML, plan.userDoc, plan.pkgDoc,
		)
		if err != nil {
			return fmt.Errorf("prompt config change: %w", err)
		}
	}

	return nil
}

func (m *Manager) starUserConfig() bool {
	return strings.HasSuffix(strings.ToLower(m.configPath), ".star")
}

// userConfigDocument parses the user config into a yaml document so a
// package merge only rewrites the keys it adds: the decoded config map
// used for diffing has already lost comments, key order and formatting.
func (m *Manager) userConfigDocument() (*yaml.Node, error) {
	if m.starUserConfig() {
		return nil, nil
	}
	return loadOrCreateUserConfig(m.configPath)
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

func loadIdePkgConfigFile(path string, base map[string]any) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadIdePkgConfigFromBytes(path, data, base, "", "", "", "")
}

// configBaseTree returns the editor's default config tree to predeclare as
// `config` when reading the user's .star config, or nil when unavailable.
func (m *Manager) configBaseTree() map[string]any {
	if m.configBase == nil {
		return nil
	}
	return m.configBase()
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

func idePkgStarlarkParams(
	pkgID string, pkgVersion release.Version, dataDir string,
	editorMode string,
) map[string]any {
	params := map[string]any{}
	if dataDir != "" {
		params["RUNE_DATADIR"] = dataDir
	}
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

func loadIdePkgConfigFromBytes(
	filename string, data []byte, base map[string]any,
	pkgID string, pkgVersion release.Version, dataDir string,
	editorMode string,
) (map[string]any, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".star") {
		// User configs are authored as overlays that mutate a
		// predeclared `config` (config[...] = ...) and never bind it,
		// matching how the editor loads them. Decode in overlay mode
		// against the editor's default config tree so subscript
		// mutations on nested keys — and Rune's appended managed block's
		// reference to `config` — resolve without "undefined: config".
		// A nil base still predeclares `config` as an empty dict, so
		// empty or comments-only files yield an empty configuration.
		cfg, err := starlarkconfig.Decode(starlarkconfig.Source{
			Src:      data,
			Filename: filename,
			Params:   idePkgStarlarkParams(pkgID, pkgVersion, dataDir, editorMode),
			Base:     base,
		})
		if errors.Is(err, starlarkconfig.ErrMissingConfig) {
			return map[string]any{}, nil
		}
		return cfg, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return normalizeIdePkgConfig(cfg).(map[string]any), nil
}

func loadIdePkgConfigOverlay(
	filename string, data []byte, base map[string]any,
	pkgID string, pkgVersion release.Version, dataDir string,
	editorMode string,
) (map[string]any, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".star") {
		return starlarkconfig.Decode(starlarkconfig.Source{
			Src:      data,
			Filename: filename,
			Params:   idePkgStarlarkParams(pkgID, pkgVersion, dataDir, editorMode),
			Base:     base,
		})
	}
	return loadIdePkgConfigFromBytes(filename, data, base, pkgID, pkgVersion,
		dataDir, editorMode)
}

func normalizeIdePkgConfig(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeIdePkgConfig(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k.(string)] = normalizeIdePkgConfig(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeIdePkgConfig(val)
		}
		return out
	default:
		return t
	}
}

// idePkgConfigDiff classifies overlay keys against the user config into two
// disjoint subsets:
//
//   - newCfg: overlay key paths absent from the user config. These can be
//     auto-applied without prompting.
//   - conflictCfg: overlay scalar leaves that already exist in the user config
//     with a different value and must be approved. Outside gui.env only
//     version-dependent leaves qualify (RUNE-225); under gui.env every
//     differing scalar qualifies, with gui.env.PATH merged rather than
//     overridden. All other differing scalars, type mismatches, and differing
//     lists preserve the user value (RUNE-187).
//
// Either returned map is nil when its subset is empty.
func idePkgConfigDiff(
	user, overlay map[string]any, versionDependent map[string]any,
	keyPath []string,
) (newCfg, conflictCfg map[string]any) {
	for key, overlayVal := range overlay {
		userVal, ok := user[key]
		if !ok {
			if newCfg == nil {
				newCfg = map[string]any{}
			}
			newCfg[key] = overlayVal
			continue
		}
		overlayMap, overlayIsMap := overlayVal.(map[string]any)
		userMap, userIsMap := userVal.(map[string]any)
		if !overlayIsMap || !userIsMap {
			if isScalar(userVal) && isScalar(overlayVal) {
				conflictVal, conflict := scalarConflict(
					append(keyPath, key),
					isVersionDependentScalar(versionDependent, key),
					userVal, overlayVal,
				)
				if conflict {
					if conflictCfg == nil {
						conflictCfg = map[string]any{}
					}
					conflictCfg[key] = conflictVal
				}
			}
			continue
		}
		nestedVersionDependent, _ := versionDependent[key].(map[string]any)
		nestedNew, nestedConflict := idePkgConfigDiff(
			userMap, overlayMap, nestedVersionDependent, append(keyPath, key),
		)
		if nestedNew != nil {
			if newCfg == nil {
				newCfg = map[string]any{}
			}
			newCfg[key] = nestedNew
		}
		if nestedConflict != nil {
			if conflictCfg == nil {
				conflictCfg = map[string]any{}
			}
			conflictCfg[key] = nestedConflict
		}
	}
	return newCfg, conflictCfg
}

func scalarConflict(
	keyPath []string, versionDependent bool, userVal, overlayVal any,
) (any, bool) {
	if isGUIEnvPathLeaf(keyPath) {
		merged, changed := mergePathValue(
			fmt.Sprint(userVal), fmt.Sprint(overlayVal),
		)
		if !changed {
			return nil, false
		}
		return merged, true
	}
	if fmt.Sprint(overlayVal) == fmt.Sprint(userVal) {
		return nil, false
	}
	if isGUIEnvLeaf(keyPath) || versionDependent {
		return overlayVal, true
	}
	return nil, false
}

func isGUIEnvLeaf(keyPath []string) bool {
	return len(keyPath) == 3 && keyPath[0] == "gui" && keyPath[1] == "env"
}

func isVersionDependentScalar(versionDependent map[string]any, key string) bool {
	dep, ok := versionDependent[key].(bool)
	return ok && dep
}

func isGUIEnvPathLeaf(keyPath []string) bool {
	return isGUIEnvLeaf(keyPath) && keyPath[2] == "PATH"
}

func isScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

func mergePathValue(userPath, pkgPath string) (string, bool) {
	if pkgPath == "" {
		return userPath, false
	}
	userChunks := strings.Split(userPath, ":")
	present := make(map[string]struct{}, len(userChunks))
	for _, chunk := range userChunks {
		present[chunk] = struct{}{}
	}
	var missing []string
	for chunk := range strings.SplitSeq(pkgPath, ":") {
		if _, ok := present[chunk]; ok {
			continue
		}
		present[chunk] = struct{}{}
		missing = append(missing, chunk)
	}
	if len(missing) == 0 {
		return userPath, false
	}
	if userPath == "" {
		return strings.Join(missing, ":"), true
	}
	return strings.Join(missing, ":") + ":" + userPath, true
}

func versionDependentKeys(overlay map[string]any) map[string]any {
	var out map[string]any
	for key, val := range overlay {
		switch t := val.(type) {
		case map[string]any:
			nested := versionDependentKeys(t)
			if nested == nil {
				continue
			}
			if out == nil {
				out = map[string]any{}
			}
			out[key] = nested
		case string:
			if !referencesPkgVersion(t) {
				continue
			}
			if out == nil {
				out = map[string]any{}
			}
			out[key] = true
		}
	}
	return out
}

// versionDependentSentinel is an implausible package version used to
// re-resolve a .star overlay so that leaves whose value depends on
// $RUNE_PKG_VERSION can be detected by comparison.
const versionDependentSentinel = "\x00rune-version-sentinel\x00"

func versionDependentOverlayKeys(
	filename string, data []byte, overlay map[string]any,
	pkgID string, dataDir, editorMode string,
) (map[string]any, error) {
	if !strings.HasSuffix(strings.ToLower(filename), ".star") {
		return versionDependentKeys(overlay), nil
	}
	sentinel, err := loadIdePkgConfigOverlay(
		filename, data, map[string]any{},
		pkgID, versionDependentSentinel, dataDir, editorMode,
	)
	if err != nil {
		return nil, fmt.Errorf("decode package config (version probe): %w", err)
	}
	return versionDependentByDecode(overlay, sentinel), nil
}

func versionDependentByDecode(real, sentinel map[string]any) map[string]any {
	var out map[string]any
	for key, realVal := range real {
		sentinelVal, ok := sentinel[key]
		if !ok {
			continue
		}
		realMap, realIsMap := realVal.(map[string]any)
		sentinelMap, sentinelIsMap := sentinelVal.(map[string]any)
		if realIsMap && sentinelIsMap {
			nested := versionDependentByDecode(realMap, sentinelMap)
			if nested == nil {
				continue
			}
			if out == nil {
				out = map[string]any{}
			}
			out[key] = nested
			continue
		}
		if realIsMap || sentinelIsMap {
			continue
		}
		if fmt.Sprint(realVal) == fmt.Sprint(sentinelVal) {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[key] = true
	}
	return out
}

func referencesPkgVersion(s string) bool {
	var found bool
	os.Expand(s, func(name string) string {
		if name == "RUNE_PKG_VERSION" {
			found = true
		}
		return ""
	})
	return found
}

func mapToYAMLDocument(cfg map[string]any) (*yaml.Node, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func loadIdePkgConfigFromYAMLDoc(doc *yaml.Node) (map[string]any, error) {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return normalizeIdePkgConfig(cfg).(map[string]any), nil
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
		if isExecutable(hdr.FileInfo()) && !isHidden(hdr.Name) {
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

func isHidden(file string) bool {
	return strings.HasPrefix(filepath.Base(file), ".")
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
