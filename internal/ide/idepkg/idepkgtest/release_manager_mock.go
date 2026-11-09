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

package idepkgtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

// ReleaseManager is a release.Manager for testing idepkg.Manager.
type ReleaseManager struct {
	mu                   sync.Mutex
	packages             map[string]release.Package
	versions             map[string][]release.Bundle
	err                  error
	progressUnits        string
	missProgressComplete bool
	hook                 func()
	tarballs             map[string][]byte
	signatures           map[string]presignature
}

type presignature struct {
	keyID     string
	signature string
}

// NewReleaseManager returns a new instance of ReleaseManager.
func NewReleaseManager(
	packages map[string]release.Package,
	versions map[string][]release.Bundle,
) *ReleaseManager {
	return &ReleaseManager{
		packages:      packages,
		versions:      versions,
		progressUnits: "chunks",
	}
}

// ExpectReturnErr signals this ReleaseManager to return error in one of the next
// calls to its release.Manager API.
func (t *ReleaseManager) ExpectReturnErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.err = err
}

// SetTarball registers a custom gzipped-tar payload to return when Get is
// called for the given pkgID. Overrides the embedded fixtures.
func (t *ReleaseManager) SetTarball(pkgID string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tarballs == nil {
		t.tarballs = make(map[string][]byte)
	}
	t.tarballs[pkgID] = data
	delete(t.signatures, pkgID)
}

// SetSignedTarball registers a gzipped-tar payload that Get returns with the
// given signing key ID and armored detached signature instead of signing it
// with the shared test entity, so tests can serve bundles signed by keys
// whose private half they do not hold.
func (t *ReleaseManager) SetSignedTarball(pkgID string, data []byte, keyID, signature string) {
	t.SetTarball(pkgID, data)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.signatures == nil {
		t.signatures = make(map[string]presignature)
	}
	t.signatures[pkgID] = presignature{keyID: keyID, signature: signature}
}

// SetHook sets a hook to be called before any of the methods return
func (t *ReleaseManager) SetHook(hook func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hook = hook
}

// GetPackage satisfies release.Manager.
func (t *ReleaseManager) GetPackage(
	ctx context.Context, pkgID string,
) (release.Package, error) {
	defer t.callHook()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return release.Package{}, t.err
	}
	pkg, ok := t.packages[pkgID]
	if !ok {
		return release.Package{}, errors.New("not found")
	}
	return pkg, nil
}

// ListPackages satisfies release.Manager.
func (t *ReleaseManager) ListPackages(
	ctx context.Context, _ map[string]string,
) (iterator.Iterator[release.Package], error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}
	var pkgs []release.Package
	for _, pkg := range t.packages {
		pkgs = append(pkgs, pkg)
	}
	return iterator.FromSlice(pkgs), nil
}

// List satisfies release.Manager.
func (t *ReleaseManager) List(
	ctx context.Context, pkgID string, _ map[string]string,
) (iterator.Iterator[release.Bundle], error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}
	bundles, ok := t.versions[pkgID]
	if !ok {
		return nil, errors.New("not found")
	}
	return iterator.FromSlice(bundles), nil
}

// Get satisfies release.Manager.
func (t *ReleaseManager) Get(
	ctx context.Context, pkgID string,
	version release.Version, writer release.ProgressWriter,
) (release.Bundle, error) {
	defer t.callHook()
	t.mu.Lock()
	if t.err != nil {
		t.mu.Unlock()
		return release.Bundle{}, t.err
	}
	bundles, ok := t.versions[pkgID]
	if !ok {
		t.mu.Unlock()
		return release.Bundle{}, errors.New("not found")
	}

	var found *release.Bundle
	for _, bundle := range bundles {
		if bundle.Version == version {
			found = new(release.Bundle)
			*found = bundle
			break
		}
	}
	if found == nil {
		t.mu.Unlock()
		return release.Bundle{}, errors.New("version not found")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go debug.CapturePanicReport(func() {

		defer wg.Done()
		if t.missProgressComplete {
			return
		}
		const n = 100
		for i := range int64(n - 1) {
			t.mu.Lock()
			writer.Progress(i, n, t.progressUnits)
			t.mu.Unlock()
		}

	})

	var err error
	go debug.CapturePanicReport(func() {

		defer wg.Done()
		t.mu.Lock()
		defer t.mu.Unlock()
		var payload []byte
		if buf, ok := t.tarballs[pkgID]; ok {
			payload = buf
		} else {
			switch pkgID {
			case "go":
				payload = goTar
			case "testpkg":
				payload = testPkgTar
			case "six":
				payload = testPkgTar
			case "configpkg":
				if version == "2" {
					payload = configPkgV2Tar
				} else {
					payload = configPkgTar
				}
			case "configpkgstar":
				payload, err = makeConfigPkgStarTar()
				if err != nil {
					return
				}
			}
		}
		if payload == nil {
			return
		}
		sig, presigned := t.signatures[pkgID]
		keyID, signature := sig.keyID, sig.signature
		if !presigned {
			// Sign the payload with the shared test entity and record the
			// provenance metadata so mandatory bundle verification passes
			// for every non-git package install without a production backdoor.
			var signErr error
			keyID, signature, signErr = signTestPayload(payload)
			if signErr != nil {
				err = signErr
				return
			}
		}
		found.Metadata = cloneMetadata(found.Metadata)
		found.Metadata[pkgtrust.MetadataSigningKeyID] = keyID
		found.Metadata[pkgtrust.MetadataSignature] = signature
		_, err = writer.Write(payload)

	})

	t.mu.Unlock()
	wg.Wait()
	if err != nil {
		return release.Bundle{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.missProgressComplete {
		writer.Progress(1, 1, t.progressUnits)
	}
	return *found, nil
}

// Upload is not implemented.
func (t *ReleaseManager) Upload(
	context.Context, release.Bundle, release.ProgressReader,
) error {
	panic("unimplemented")
}

// Delete is not implemented.
func (t *ReleaseManager) Delete(context.Context, string, release.Version) error {
	panic("unimplemented")
}

// Create is not implemented.
func (t *ReleaseManager) Create(context.Context, release.Package) error {
	panic("unimplemented")
}

// UpdatePackageMetadata is not implemented.
func (t *ReleaseManager) UpdatePackageMetadata(
	context.Context, string, map[string]string,
) error {
	panic("unimplemented")
}

// DeletePackage is not implemented.
func (t *ReleaseManager) DeletePackage(context.Context, string) error {
	panic("unimplemented")
}

// SetMissProgressComplete signals this ReleaseManager to not complete
// the download notification via UpdateNotificationProgress.
func (t *ReleaseManager) SetMissProgressComplete(doIt bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.missProgressComplete = doIt
}

func (t *ReleaseManager) callHook() {
	t.mu.Lock()
	hook := t.hook
	t.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// Download is the download of a version of a package.
type Download struct {
	Package string
	Version release.Version
}

// HeldReleaseManager serves the bundles of a ReleaseManager, holding
// the first download of each held version until it is released, which
// keeps the install of that version in flight.
type HeldReleaseManager struct {
	*ReleaseManager
	held map[Download]*heldDownload
}

type heldDownload struct {
	once    sync.Once
	started chan struct{}
	release chan error
}

// NewHeldReleaseManager returns a HeldReleaseManager serving the bundles
// of rm that holds the first download of each of held.
func NewHeldReleaseManager(rm *ReleaseManager, held ...Download) *HeldReleaseManager {
	h := &HeldReleaseManager{ReleaseManager: rm, held: make(map[Download]*heldDownload, len(held))}
	for _, d := range held {
		h.held[d] = &heldDownload{started: make(chan struct{}), release: make(chan error, 1)}
	}
	return h
}

// Get satisfies release.Manager. The first download of a held version
// blocks until it is released, and fails with the error it is released
// with.
func (h *HeldReleaseManager) Get(
	ctx context.Context, pkgID string, version release.Version, w release.ProgressWriter,
) (release.Bundle, error) {
	var err error
	if d, ok := h.held[Download{Package: pkgID, Version: version}]; ok {
		d.once.Do(func() {
			close(d.started)
			err = <-d.release
		})
	}
	if err != nil {
		return release.Bundle{}, err
	}
	return h.ReleaseManager.Get(ctx, pkgID, version, w)
}

// AwaitStarted fails t unless the first download of d starts soon.
func (h *HeldReleaseManager) AwaitStarted(t testing.TB, d Download) {
	t.Helper()
	select {
	case <-h.held[d].started:
	case <-time.After(10 * time.Second):
		t.Fatalf("the download of %s@%s did not start", d.Package, d.Version)
	}
}

// Release lets the first download of d go on, or fail with err when it
// is not nil.
func (h *HeldReleaseManager) Release(d Download, err error) {
	h.held[d].release <- err
}

func makeConfigPkgStarTar() ([]byte, error) {
	files := map[string]string{
		"config.star": `if "env" not in config:
    config["env"] = {}
config["env"]["GOROOT"] = RUNE_DATADIR + "/pkg/" + RUNE_PKG_ID + "/" + RUNE_PKG_VERSION + "/go"

if "settings" not in config:
    config["settings"] = {}
config["settings"]["theme"] = "dark"
config["settings"]["indent"] = 4
`,
		"lib/readme.txt": "# lib placeholder\n",
	}

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			return nil, fmt.Errorf("write tar header %s: %w", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			return nil, fmt.Errorf("write tar content %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		_ = gzw.Close()
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return buf.Bytes(), nil
}
