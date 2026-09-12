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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/docrelease"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/pkgshell"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/symboldb"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestIDEInitializationIntegration(t *testing.T) {
	t.Run("does not panic with sample config", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)
		err := os.WriteFile(configFile.Name(), []byte(sampleConfig), 0666)
		require.NoError(t, err)

		cwdURI, err := workspaceapi.CurrentUserHostURI(".")
		require.NoError(t, err)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		i := new(IDE)
		err = i.init(cwdURI.String(), configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		require.NotNil(t, i.workspace)
		require.NotNil(t, i.clipboard)

		assert.NoError(t, i.closeResources())
	})

	t.Run("does not panic with empty config", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)

		err := os.WriteFile(configFile.Name(), []byte("{}"), 0666)
		require.NoError(t, err)

		cwdURI, err := workspaceapi.CurrentUserHostURI(".")
		require.NoError(t, err)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		i := new(IDE)
		err = i.init(cwdURI.String(), configFile.Name(),
			dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir), WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		require.NotNil(t, i.workspace)
		require.NotNil(t, i.clipboard)

		assert.NoError(t, i.closeResources())
	})

	t.Run("takes a non-URI as a workspace", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)

		err := os.WriteFile(configFile.Name(), []byte("{}"), 0666)
		require.NoError(t, err)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		i := new(IDE)
		err = i.init(".", configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		require.NotNil(t, i.workspace)
		require.NotNil(t, i.clipboard)

		// addWorkspace launches the workspace install (Phase B) in a
		// goroutine; closeResources must wait for that to finish
		// before tearing down the workspace.Manager, otherwise an
		// in-flight vtereservoir StartCommand races with the manager
		// closing the file scheme's open *os.Files.
		i.WaitWorkspaces()
		assert.NoError(t, i.closeResources())
	})

	t.Run("creates non-existing directories for log file", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		configData := fmt.Sprintf("log_path: %s/bla/bla/bla/debug.log", dir)
		err = os.WriteFile(configFile.Name(), []byte(configData), 0666)
		require.NoError(t, err)

		i := new(IDE)
		err = i.init(".", configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		require.NotNil(t, i.workspace)
		require.NotNil(t, i.clipboard)

		assert.NoError(t, i.closeResources())
	})

	t.Run("is able to initialize without a cwd", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		i := new(IDE)
		err = i.init("", configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		require.NotNil(t, i.root)
		assert.NoError(t, i.closeResources())
	})

	t.Run("init shader is run when passed WithInitShader option", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)
		initShader := new(mockShader)

		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dataDir)
		})

		i := new(IDE)
		err = i.init("", configFile.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)),
			WithInitShader(
				func(_ term.Attributes, _ component.FrameCharSet) shader.Shader {
					return initShader
				},
				30, 1*time.Second,
			))
		require.NoError(t, err)

		i.initRunning()
		i.root.Draw(&term.NoopWriter{})

		assert.True(t, initShader.called)
		assert.NoError(t, i.closeResources())
	})

	t.Run("init shader factory receives attrs set via SetDefaultAttributes before Ready", func(t *testing.T) {
		// Documents RUNE-203 contract: callers MUST invoke
		// SetDefaultAttributes before Ready() so the init-shader
		// factory observes the configured GUI theme background.
		// Calling SetDefaultAttributes after Ready() leaves the
		// shader runner painting with the stale config defAttr.
		configFile, _ := makeTestFiles(t)

		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dataDir)
		})

		var captured term.Attributes
		i, err := New("", configFile.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)),
			WithInitShader(
				func(attr term.Attributes, _ component.FrameCharSet) shader.Shader {
					captured = attr
					return new(mockShader)
				},
				30, 1*time.Second,
			))
		require.NoError(t, err)

		wantAttr := term.Attributes{
			Fg: term.ColorWhite,
			Bg: term.ColorBlack,
		}
		i.SetDefaultAttributes(wantAttr)

		_ = i.Ready()

		assert.Equal(t, wantAttr, captured,
			"init shader factory must observe attrs set via "+
				"SetDefaultAttributes prior to Ready()")
		assert.NoError(t, i.closeResources())
	})

	t.Run("SetDefaultAttributes propagates default fg to home ex before Ready", func(t *testing.T) {
		configFile, _ := makeTestFiles(t)

		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dataDir)
		})

		i := new(IDE)
		err = i.init("", configFile.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
		require.NoError(t, err)

		wantAttr := term.Attributes{
			Fg: term.NewRGBColor(210, 180, 140),
			Bg: term.ColorBlack,
		}
		i.SetDefaultAttributes(wantAttr)

		require.NotNil(t, i.workspaceHandler.empty)
		assert.Equal(t, wantAttr, i.workspaceHandler.empty.defAttr,
			"home ex must observe attrs set via SetDefaultAttributes")
		assert.Equal(t, wantAttr, i.workspaceHandler.defAttr,
			"workspace handler must cache the live default attrs for "+
				"seeding future workspaces")
		assert.NoError(t, i.closeResources())
	})
}

func TestOpen(t *testing.T) {
	t.Parallel()
	assertURI := func(t *testing.T, i *IDE, expected workspaceapi.URI) {
		ex := i.workspaceHandler.exHandler(i.workspaceHandler.focusHandler())
		uri, _, ok := ex.handlerInFocus()
		require.True(t, ok)
		assert.Equal(t, expected, uri)
	}

	t.Run("empty workspace", func(t *testing.T) {
		t.Parallel()
		file, config := makeTestFiles(t)
		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
		i, err := New("", config.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), WithPublishEvent(nopPublishEvent))
		require.NoError(t, err)
		uri, err := workspaceapi.CurrentUserHostURI(file.Name())
		require.NoError(t, err)

		i.workspaceHandler.mu.Lock()
		defer i.workspaceHandler.mu.Unlock()

		require.NoError(t, i.Open(uri))
		assertURI(t, i, uri)
	})

	t.Run("a workspace", func(t *testing.T) {
		t.Parallel()
		file, config := makeTestFiles(t)
		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
		i, err := New(os.TempDir(), config.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), WithPublishEvent(nopPublishEvent))
		require.NoError(t, err)
		uri, err := workspaceapi.CurrentUserHostURI(file.Name())
		require.NoError(t, err)

		i.workspaceHandler.mu.Lock()
		defer i.workspaceHandler.mu.Unlock()

		require.NoError(t, i.Open(uri))
		assertURI(t, i, uri)
	})

	t.Run("syntax enabled, empty workspace", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(
			release.Package{Name: "go", Latest: "3"},
		)
		bundles := idepkgtest.MakeBundles(
			[]release.Bundle{
				{Package: "go", Version: "3"},
			},
		)
		file, config := makeTestFiles(t)
		dataDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		i, err := New("", config.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), WithReleaseManager(rm), WithPublishEvent(nopPublishEvent))
		require.NoError(t, err)
		uri, err := workspaceapi.CurrentUserHostURI(file.Name())
		require.NoError(t, err)

		logrus.SetLevel(logrus.TraceLevel)

		i.workspaceHandler.mu.Lock()
		defer i.workspaceHandler.mu.Unlock()

		require.NoError(t, i.Open(uri))
		assertURI(t, i, uri)

		// allow for syntax to unpack things
		time.Sleep(200 * time.Millisecond)
	})
}

// TestHomeWorkspaceDoesNotStartExtensions is an end-to-end guard that a
// configured extension is never started on the home/empty workspace. The home
// workspace deliberately runs no extensions because they recursively walk the
// workspace root for .gitignore files at startup, which is ruinously expensive
// when the root is the user's home directory.
func TestHomeWorkspaceDoesNotStartExtensions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(
		"editor:\n  mode: modal\n"+
			"workspace:\n  home: "+dir+"\n"+
			"extensions:\n  rune-agent:\n    path: rune-agent-bin\n"), 0o644))

	recorder := &recordingRunner{}
	mu := new(sync.Mutex)
	i, err := New("", configPath, dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(recordingExtensionsRunner{runner: recorder}),
		WithLocker(mu),
		WithScheduleNextTick(func(fn func()) bool {
			fn()
			return true
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	_ = i.Ready()
	i.WaitWorkspaces()

	// The home runner is built and its extensions, if any, are started from a
	// background goroutine; give that path time to run so a regression that
	// reintroduces the start is caught rather than racing past the assertion.
	time.Sleep(200 * time.Millisecond)

	assert.Empty(t, recorder.runCalls(),
		"no extension may be started on the home workspace")
}

func TestSignedPackageTrustIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real workspace extension process")
	}

	const (
		goodPkgID = "signed-extension"
		badPkgID  = "tampered-signature-extension"
		extID     = "signed-extension"
		version   = release.Version("1")
	)

	entity, err := openpgp.NewEntity("Rune Test Publisher", "", "publisher@example.com", nil)
	require.NoError(t, err)
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))

	testBinary, err := os.Executable()
	require.NoError(t, err)
	entrypoint := "signed-extension"
	configYAML := "extensions:\n  " + extID + ":\n" +
		"    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/" + entrypoint + "\n"
	script := "#!/bin/sh\n" +
		"export RUNE_IDE_PKGTRUST_HELPER=1\n" +
		"export RUNE_IDE_PKGTRUST_FINGERPRINT=" + fingerprint + "\n" +
		"exec " + shellQuote(testBinary) +
		" -test.run=^TestSignedPackageTrustExtensionHelper$\n"
	tarball := signedExtensionTarball(t, configYAML, entrypoint, script)

	rm := docrelease.NewManager(document.NewInMemoryService())
	seedSignedPackage(t, rm, entity, goodPkgID, version, tarball, tarball)
	seedSignedPackage(t, rm, entity, badPkgID, version, tarball,
		append(append([]byte(nil), tarball...), "tampered"...))

	workspaceDir := t.TempDir()
	dataTarget := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(dataTarget, dataDir); err != nil {
		dataDir = dataTarget
	}
	configPath := filepath.Join(t.TempDir(), "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(
		"editor:\n  mode: modal\n"), 0o644))

	mu := new(sync.Mutex)
	schedule, drainSchedule := newTestScheduler(t, mu)
	extensions, err := extensionv2.NewRunner(context.Background(), mu, dataDir)
	require.NoError(t, err)
	keyring := armoredPublicKeyring(t, entity)
	trust := pkgtrust.NewStore(dataDir, func() ([]byte, error) { return keyring, nil })
	i, err := New(workspaceDir, configPath, dataDir, trust, newTestStorage(t, dataDir),
		WithReleaseManager(rm),
		WithExtensionsRunner(extensions),
		WithPublishEvent(nopPublishEvent),
		WithLocker(mu),
		WithScheduleNextTick(schedule),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, i.Close()) })
	_ = i.Ready()
	drainSchedule()
	i.WaitWorkspaces()

	packages := pkgshell.New(pkgshell.Config{
		Manager:       i.workspaceHandler.pkgmanager.pkg,
		UpdateChecker: i.workspaceHandler.pkgmanager.uc,
	})
	install := func(pkgID string) error {
		_, err := packages.HandleCommand(context.Background(), repl.Command{
			Name: pkgshell.CommandName,
			Args: []string{"install", pkgID},
		}, repl.NopProgressWriter())
		return err
	}

	err = install(badPkgID)
	require.ErrorContains(t, err, "verify package")
	_, installed := i.workspaceHandler.pkgmanager.pkg.PackageVersionInUse(badPkgID)
	assert.False(t, installed)
	_, err = os.Stat(filepath.Join(dataDir, "pkg", badPkgID, string(version)))
	assert.True(t, os.IsNotExist(err), "bad signature package must not be installed: %v", err)

	require.NoError(t, install(goodPkgID))
	installedVersion, installed := i.workspaceHandler.pkgmanager.pkg.PackageVersionInUse(goodPkgID)
	require.True(t, installed)
	assert.Equal(t, version, installedVersion)
	_, err = os.Stat(filepath.Join(dataDir, "pkg", goodPkgID,
		".manifest-"+string(version)+".json"))
	require.NoError(t, err)
	installedEntrypoint := filepath.Join(dataDir, "pkg", goodPkgID, string(version), entrypoint)
	verifiedFingerprint, verified := i.workspaceHandler.pkgmanager.pkg.
		VerifyExtensionEntrypoint(installedEntrypoint)
	if !verified {
		manifestPath := filepath.Join(dataDir, "pkg", goodPkgID,
			".manifest-"+string(version)+".json")
		manifest, readErr := os.ReadFile(manifestPath)
		entryInfo, statErr := os.Stat(installedEntrypoint)
		configInfo, configStatErr := os.Stat(filepath.Join(
			dataDir, "pkg", goodPkgID, string(version), "config.yaml"))
		t.Fatalf("installed entrypoint was not verified: manifest=%s manifestErr=%v "+
			"entry=%v entryErr=%v config=%v configErr=%v",
			manifest, readErr, entryInfo, statErr, configInfo, configStatErr)
	}
	assert.Equal(t, fingerprint, verifiedFingerprint)
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	require.NoError(t, i.workspaceHandler.focusRunner().WaitReady(readyCtx, extID))
	readyCancel()

	require.Eventually(t, func() bool {
		return storageSentinelPresent(context.Background(), dataDir, extID, "signed")
	}, 10*time.Second, 20*time.Millisecond)
	assert.Zero(t, countFloatingWindows(i, mu),
		"trusted publisher must bypass the permission prompt")

	deleteExtensionSentinel(t, dataDir, extID)
	f, err := os.OpenFile(installedEntrypoint, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("# tampered\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	mu.Lock()
	restart, ok := i.workspaceHandler.focusEx().comp.REPLCommand("extensions")
	mu.Unlock()
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The restart blocks on the startup authorization prompt for the now
	// tampered binary, so it must run in the background; the test asserts
	// on the prompt window instead of answering it.
	restartDone := make(chan error, 1)
	go func() {
		_, err := restart.HandleCommand(ctx, repl.Command{
			Name: "extensions",
			Args: []string{"restart", extID},
		}, repl.NopProgressWriter())
		restartDone <- err
	}()
	t.Cleanup(func() {
		cancel()
		<-restartDone
	})

	require.Eventually(t, func() bool {
		return countFloatingWindows(i, mu) > 0
	}, 10*time.Second, 20*time.Millisecond,
		"tampered extension must require authorization")
	require.Never(t, func() bool {
		return storageSentinelPresent(context.Background(), dataDir, extID, "signed")
	}, 500*time.Millisecond, 20*time.Millisecond,
		"tampered extension must not access protected storage before authorization")
}

func TestSignedPackageTrustExtensionHelper(t *testing.T) {
	if os.Getenv("RUNE_IDE_PKGTRUST_HELPER") != "1" {
		return
	}
	meta := extensionapi.Metadata{
		DeveloperID:      "rune-test-publisher",
		DeveloperEmail:   "publisher@example.com",
		DeveloperKey:     os.Getenv("RUNE_IDE_PKGTRUST_FINGERPRINT"),
		ExtensionID:      "signed-extension",
		ExtensionName:    "Signed extension",
		ExtensionVersion: "1",
		Permissions:      extensionapi.NewPermissions(extensionapi.PermissionStorage),
	}
	ext := extensionapi.FuncWorkspaceExtension(func(
		ctx context.Context, workspace *extensionapi.Workspace, _ config.Config,
	) error {
		if err := workspace.Storage(ctx).Set(ctx, "sentinel", map[string]any{
			"lang": "signed",
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	require.NoError(t, extensionapi.ServeWorkspaceExtension(ext, meta))
}

func signedExtensionTarball(t *testing.T, configYAML, entrypoint, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, file := range []struct {
		name    string
		content string
		mode    int64
	}{
		{name: "config.yaml", content: configYAML, mode: 0o644},
		{name: entrypoint, content: script, mode: 0o755},
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: file.name, Mode: file.mode, Size: int64(len(file.content)),
		}))
		_, err := tw.Write([]byte(file.content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

func seedSignedPackage(
	t *testing.T, manager release.Manager, entity *openpgp.Entity,
	pkgID string, version release.Version, payload, signedPayload []byte,
) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, manager.Create(ctx, release.Package{Name: pkgID, Latest: version}))
	var signature bytes.Buffer
	require.NoError(t, openpgp.ArmoredDetachSign(
		&signature, entity, bytes.NewReader(signedPayload), nil))
	bundle := release.Bundle{
		Package: pkgID,
		Version: version,
		Metadata: map[string]string{
			pkgtrust.MetadataSigningKeyID: fmt.Sprintf("%016X", entity.PrimaryKey.KeyId),
			pkgtrust.MetadataSignature:    signature.String(),
		},
	}
	require.NoError(t, manager.Upload(ctx, bundle,
		release.NopProgressReader(bytes.NewReader(payload))))
}

func armoredPublicKeyring(t *testing.T, entities ...*openpgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	for _, entity := range entities {
		require.NoError(t, entity.Serialize(w))
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func deleteExtensionSentinel(t *testing.T, dataDir, extensionID string) {
	t.Helper()
	storage := localstorage.New(context.Background(), filepath.Join(dataDir, "extensions"),
		docbson.Marshaler())
	defer func() { require.NoError(t, storage.Close()) }()
	require.NoError(t, storageapi.WithPartition(storage, extensionID).
		Delete(context.Background(), "sentinel"))
}

// TestWonAliasIntegration is an end-to-end test that wires the IDE
// through real configuration to verify the `won` alias from the user's
// `~/.runedev/config.yaml`:
//
//	command:
//	  aliases:
//	    won:
//	      command: workspaceopen
//	      completer:
//	        - '{history}'
//	        - '{file}'
//
// The test goes through the real configuration loader, the real
// command alias parser, the real workspace history (backed by
// localstorage on a temp dir), and the real text.Component completion
// path. After dispatching `:won <repoA>` once, querying the alias
// completion again must surface "<repoA>" as the first match — proving
// that `{history}` is wired correctly all the way from the YAML
// completer chain through search.History.HistoryIterator.
func TestWonAliasIntegration(t *testing.T) {
	dataDir := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()

	// The {file} completer traverses the home workspace root when the
	// last argument is empty, so an unpinned HOME makes this test walk
	// the whole real home directory.
	t.Setenv("HOME", t.TempDir())

	// Real config file with the `won` alias in YAML form, identical to
	// what the user has in ~/.runedev/config.yaml.
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
  aliases:
    won:
      command: workspaceopen
      completer:
        - '{history}'
        - '{file}'
`), 0666))

	// Initial cwd workspace. We use repoB (different from repoA) so
	// the file-based completer's results are clearly distinguishable
	// from the history entries.
	// Seed repoB with a file so we can Open it as the initial
	// workspace; without an opened workspace the IDE root forwards
	// events differently and the command prompt would not even be
	// reachable from the empty root handler.
	repoBFile := filepath.Join(repoB, "seed.txt")
	require.NoError(t, os.WriteFile(repoBFile, nil, 0666))

	mu := new(sync.Mutex)
	i, err := New(repoB, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	root := i.Ready()

	repoBURI, err := workspaceapi.CurrentUserHostURI(repoBFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(repoBURI))
	mu.Unlock()

	// Sanity: alias is wired through configuration.
	wh := i.workspaceHandler
	aliases := i.ideConfig.commandAliases()
	wonAlias, ok := aliases["won"]
	require.True(t, ok, "won alias must be registered from YAML config")
	require.Len(t, wonAlias.Completers, 2,
		"won alias must have two completer factories ({history} + {file})")

	// Dispatch the alias by driving keyboard input through the IDE
	// root handler — the same code path a real user takes. This goes
	// through the command Prompt, which is what records the entered
	// command line into search.History on Enter.
	// term.ParseKeys requires `<space>` rather than literal spaces;
	// the alias name has none, but the command itself needs the
	// space token between `won` and the path argument.
	wonInvocation := ":won<space>" + repoA + "<enter>"
	keys, err := term.ParseKeys(wonInvocation)
	require.NoError(t, err)
	root.Resize(80, 24)
	for _, k := range keys {
		mu.Lock()
		root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		mu.Unlock()
	}

	// Wait for any async completion machinery to settle (the
	// dispatch path runs the alias handler on a goroutine and
	// records history when it returns).
	wh.focusEx().Wait()

	// Now ask the focused text.Component to complete the same alias
	// with no partial last arg. This is exactly the call the prompt
	// issues when the user types `:won ` (alias + space).
	ex := wh.exHandler(wh.focusHandler())
	require.NotNil(t, ex, "expected a focused ex handler after dispatch")
	mu.Lock()
	it, _, err := ex.comp.CompleteCommand(t.Context(),
		textapi.Command{Name: "won", Args: []string{""}})
	mu.Unlock()
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	got, err := iterator.ToSlice(t.Context(), it)
	require.NoError(t, err)
	require.NotEmpty(t, got,
		"won completion must surface at least the prior `won %s` history entry, "+
			"got nothing — `{history}` is not wired correctly", repoA)

	// History must come first per chain order in the YAML config. The
	// HistoryCompleter strips the alias-name prefix, so the entry the
	// user sees back is just the arg that was passed (the repoA path).
	assert.Equal(t, repoA, got[0],
		"first completion must be the prior `won` argument from history; "+
			"got %q. full result: %v", got[0], got)
}

// TestWorkspaceOpenCompletionSurfacesHistory verifies that the built-in
// `workspaceopen` completer surfaces previously opened workspaces from
// command history (history first), in addition to directory completion.
// This replaces the dropped `wopen` alias: opening a workspace via
// `:workspaceopen <path>` records it in history, and completing
// `workspaceopen` then offers that path back as the first match.
func TestWorkspaceOpenCompletionSurfacesHistory(t *testing.T) {
	dataDir := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()

	// Keep the configured workspace home distinct from the OS home so this
	// test catches accidental use of user.Current or $HOME by the completer.
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(
		filepath.Join(home, "projects", "nested"), 0o700))

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
workspace:
  home: `+home+`
`), 0666))

	// Seed repoB so it can be the initial workspace; the command prompt
	// is only reachable once a real workspace is focused.
	repoBFile := filepath.Join(repoB, "seed.txt")
	require.NoError(t, os.WriteFile(repoBFile, nil, 0666))

	mu := new(sync.Mutex)
	i, err := New(repoB, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	root := i.Ready()

	repoBURI, err := workspaceapi.CurrentUserHostURI(repoBFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(repoBURI))
	mu.Unlock()

	wh := i.workspaceHandler

	// Open repoA via the built-in workspaceopen, driving keyboard input
	// through the IDE root handler so the command Prompt records the
	// entered command line into history on Enter.
	invocation := ":workspaceopen<space>" + repoA + "<enter>"
	keys, err := term.ParseKeys(invocation)
	require.NoError(t, err)
	root.Resize(80, 24)
	for _, k := range keys {
		mu.Lock()
		root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		mu.Unlock()
	}
	wh.focusEx().Wait()

	// Completing `workspaceopen ` (command + space, empty last arg) must
	// surface the prior path from history as the first result.
	ex := wh.exHandler(wh.focusHandler())
	require.NotNil(t, ex, "expected a focused ex handler after dispatch")
	mu.Lock()
	it, _, err := ex.comp.CompleteCommand(t.Context(),
		textapi.Command{Name: "workspaceopen", Args: []string{""}})
	mu.Unlock()
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	got, err := iterator.ToSlice(t.Context(), it)
	require.NoError(t, err)
	require.NotEmpty(t, got,
		"workspaceopen completion must surface at least the prior "+
			"`workspaceopen %s` history entry, got nothing", repoA)
	assert.Equal(t, repoA, got[0],
		"first completion must be the prior workspaceopen argument from "+
			"history; got %q. full result: %v", got[0], got)
	assert.Contains(t, got, "projects")
	assert.NotContains(t, got, "projects/nested")

	mu.Lock()
	nested, _, err := ex.comp.CompleteCommand(t.Context(),
		textapi.Command{Name: "workspaceopen", Args: []string{"projects/"}})
	mu.Unlock()
	require.NoError(t, err)
	defer func() { _ = nested.Close() }()
	nestedGot, err := iterator.ToSlice(t.Context(), nested)
	require.NoError(t, err)
	assert.Contains(t, nestedGot, "projects/nested")
}

// TestRecentWorkspaceOpensReflectsPromptHistory asserts the exported
// accessor lists prompt-driven workspaceopen paths most-recent-first
// and de-duplicated, which backs the Open Recent menu.
func TestRecentWorkspaceOpensReflectsPromptHistory(t *testing.T) {
	dataDir := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
`), 0666))

	repoBFile := filepath.Join(repoB, "seed.txt")
	require.NoError(t, os.WriteFile(repoBFile, nil, 0666))

	mu := new(sync.Mutex)
	i, err := New(repoB, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	root := i.Ready()
	root.Resize(80, 24)

	repoBURI, err := workspaceapi.CurrentUserHostURI(repoBFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(repoBURI))
	mu.Unlock()

	wh := i.workspaceHandler
	openViaPrompt := func(path string) {
		keys, err := term.ParseKeys(":workspaceopen<space>" + path + "<enter>")
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
			mu.Unlock()
		}
		wh.focusEx().Wait()
	}

	// Open A, then B, then A again: history is newest-first and deduped,
	// so A should lead and appear once.
	openViaPrompt(repoA)
	openViaPrompt(repoB)
	openViaPrompt(repoA)

	assert.Equal(t, []string{repoA, repoB}, i.RecentWorkspaceOpens())
}

// TestWorkspaceOpenCompletionDispatchesQuotedPath is an end-to-end guard
// for the completion-quoting fix: completing workspaceopen on a directory
// whose name contains characters the prompt tokenizer treats specially
// (spaces and single/double quotes) must select a single, correctly
// quoted entry that, on Enter, opens exactly one workspace at that literal
// path rather than splitting the name into several args. Tabs are covered
// at the tokenizer level in handler/command; they cannot form a workspace
// URI (the path parser rejects control characters) so they are out of
// scope here.
//
// The flow mirrors a real user: with HOME pointing at a directory that
// holds a single adversarially named child, paste that home path, type a
// trailing slash, press Tab to accept the sole completion, then Enter to
// dispatch. syncCommandPrompt runs it on the test goroutine so no settling
// races remain.
func TestWorkspaceOpenCompletionDispatchesQuotedPath(t *testing.T) {
	cases := []struct {
		name    string
		dirName string
	}{
		{"space", "my workspace"},
		{"single quote", "won't stop"},
		{"double quote", `say "hi"`},
		{"quote and space", `o'brien dir`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()

			// HOME holds exactly one (adversarially named) child, making
			// completion from its absolute path deterministic.
			home := t.TempDir()
			t.Setenv("HOME", home)
			target := filepath.Join(home, tc.dirName)
			require.NoError(t, os.MkdirAll(target, 0o700))

			// Seed repoB as the initial workspace; the command prompt is
			// only reachable once a real workspace is focused.
			repoB := t.TempDir()
			repoBFile := filepath.Join(repoB, "seed.txt")
			require.NoError(t, os.WriteFile(repoBFile, nil, 0666))

			configFile, _ := makeTestFiles(t)
			mu := new(sync.Mutex)
			scheduleNextTick, drain := newTestScheduler(t, mu)
			i, err := New(repoB, configFile.Name(), dataDir,
				pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
				WithPublishEvent(nopPublishEvent),
				WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
				WithLocker(mu),
				WithScheduleNextTick(scheduleNextTick),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = i.Close() })
			root := i.Ready()
			mu.Lock()
			root.Resize(80, 24)
			mu.Unlock()
			// Start scheduler dispatch before waiting: the install lands
			// through a scheduled callback.
			drain()
			i.WaitWorkspaces()
			drain()

			repoBURI, err := workspaceapi.CurrentUserHostURI(repoBFile)
			require.NoError(t, err)
			mu.Lock()
			require.NoError(t, i.Open(repoBURI))
			mu.Unlock()
			i.WaitWorkspaces()
			drain()

			wh := i.workspaceHandler

			// Run completion on the test goroutine so Tab observes a
			// settled list.
			mu.Lock()
			wh.focusEx().syncCommandPrompt = true
			mu.Unlock()

			keys, err := term.ParseKeys(":workspaceopen<space>")
			require.NoError(t, err)
			for _, k := range keys {
				mu.Lock()
				root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
				mu.Unlock()
			}
			mu.Lock()
			root.Handle(term.Event{Type: term.EventPasteStart})
			for _, ch := range home {
				root.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			root.Handle(term.Event{Type: term.EventPasteEnd})
			root.Handle(term.Event{Type: term.EventKey, Ch: filepath.Separator})
			mu.Unlock()
			wh.focusEx().Wait()

			// Tab accepts the sole completion (the quoted child path),
			// Enter dispatches workspaceopen with it.
			mu.Lock()
			root.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
			mu.Unlock()
			wh.focusEx().Wait()
			mu.Lock()
			root.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			mu.Unlock()
			i.WaitWorkspaces()
			drain()

			wantURI, err := wh.homeWorkspace.URI(target)
			require.NoError(t, err)
			mu.Lock()
			var found bool
			var gotURIs []string
			for _, w := range wh.workspaces {
				if w == nil {
					continue
				}
				gotURIs = append(gotURIs, w.uri.String())
				if w.uri == wantURI {
					found = true
				}
			}
			mu.Unlock()
			assert.True(t, found,
				"expected a workspace opened at the literal path %q; "+
					"a mis-quoted completion would have split the name into "+
					"several arguments and opened the wrong path. want=%q got=%v",
				target, wantURI.String(), gotURIs)
		})
	}
}

// TestE2EIssueImplementAliasChainOrdering reproduces the user-reported
// `issue-implement` failure. The alias chains a nested alias (standing
// in for `worktreenew`) followed by two `extensionready` steps:
//
//	issue-implement:
//	  command:
//	    - worktreelike $1
//	    - extensionready dummy agent $1
//	    - extensionready dummy chatskill issue-implement $1
//
// `worktreelike` is itself an alias whose body runs a single command
// that records its execution (the stand-in for the real worktree
// creation; we do not need a git worktree to exercise the ordering
// bug). The three steps must run in submission order: the nested alias
// first, then `agent`, then `chatskill`.
//
// The bug: ex.dispatchCommand dispatches each expanded alias step via
// text.Component.DispatchCommand, which only resolves subscribed
// commands — not alias names. So a step whose name is itself an alias
// (`worktreelike`) is silently dropped: its body never runs. In the
// real config that means the worktree workspace is never created and
// the `extensionready` steps run against the wrong workspace.
func TestE2EIssueImplementAliasChainOrdering(t *testing.T) {
	dataDir := t.TempDir()
	repo := t.TempDir()
	seed := filepath.Join(repo, "seed.txt")
	require.NoError(t, os.WriteFile(seed, nil, 0o666))

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
  aliases:
    worktreelike:
      command:
        - recordfirst $1
    issue-implement:
      command:
        - worktreelike $1
        - extensionready dummy agent $1
        - extensionready dummy chatskill issue-implement $1
`), 0o666))

	mu := new(sync.Mutex)
	// Mirror the production event loop: a single consumer runs
	// scheduled callbacks in enqueue order (run.go reads one
	// EventInterrupt at a time from a single channel). A goroutine
	// per call would let two dispatches race for mu and reorder, which
	// production never does.
	scheduleNextTick, drainSched := newTestScheduler(t, mu)
	runner := newPerIDReadyRunner("dummy")
	runnerFn := func(
		_ workspaceapi.URI,
		_ map[extensionapi.Permission]extension.ResourceRegistrar,
		_, _ string, _ browser.Notifications,
		_, _ schemeapi.Executor, _ extension.Grantor, _ text.Editor,
		_ ideauthorizer.PromptOpener, _ storageapi.Service,
		_ func(func()) bool) (extension.Runner, error) {
		return runner, nil
	}

	i, err := New(repo, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(runnerFn)),
		WithScheduleNextTick(scheduleNextTick),
		WithLocker(mu),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	root := i.Ready()
	// The first drain starts scheduler dispatch; before it, callbacks
	// queue so they cannot race New's unsynchronized wiring.
	drainSched()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	i.WaitWorkspaces()

	seedURI, err := workspaceapi.CurrentUserHostURI(seed)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(seedURI))
	mu.Unlock()

	// The dummy extension registers the follow-up commands the alias
	// dispatches; each records its name (plus the nested-alias step).
	var orderMu sync.Mutex
	var order []string
	record := func(name string) text.CommandHandler {
		return text.FuncCommandHandler(
			func(context.Context, textapi.Command) error {
				orderMu.Lock()
				order = append(order, name)
				orderMu.Unlock()
				return nil
			}, nil)
	}
	require.NoError(t, i.workspaceHandler.subscribeCommand(
		textapi.CommandManual{Name: "recordfirst"}, record("first")))
	require.NoError(t, i.workspaceHandler.subscribeCommand(
		textapi.CommandManual{Name: "agent"}, record("agent")))
	require.NoError(t, i.workspaceHandler.subscribeCommand(
		textapi.CommandManual{Name: "chatskill"}, record("chatskill")))

	invocation := ":issue-implement<space>my-branch<enter>"
	keys, err := term.ParseKeys(invocation)
	require.NoError(t, err)
	for _, k := range keys {
		mu.Lock()
		root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		mu.Unlock()
	}
	i.workspaceHandler.focusEx().Wait()

	// Let the extension become ready so the queued extensionready
	// follow-up commands dispatch.
	runner.release("dummy")

	require.Eventually(t, func() bool {
		orderMu.Lock()
		defer orderMu.Unlock()
		return len(order) == 3
	}, 5*time.Second, 20*time.Millisecond,
		"all three alias steps must run: the nested worktreelike alias, "+
			"then agent, then chatskill")

	orderMu.Lock()
	defer orderMu.Unlock()
	assert.Equal(t, []string{"first", "agent", "chatskill"}, order,
		"alias steps must dispatch in submission order; a missing "+
			"\"first\" means the nested worktreelike alias was dropped "+
			"instead of expanded")
}

// TestE2EExtensionReadyChainOrderingNoWorktree is the same scenario
// without the leading nested alias, isolating the extensionready
// ordering on a single workspace:
//
//	issue-implement:
//	  command:
//	    - extensionready dummy agent $1
//	    - extensionready dummy chatskill issue-implement $1
//
// With no pending workspace reservation, both steps run against the
// focused workspace. `chatskill` must dispatch after `agent` — the
// per-extension extensionready queue must preserve submission order
// even when both follow-up commands wait on the same extension.
func TestE2EExtensionReadyChainOrderingNoWorktree(t *testing.T) {
	dataDir := t.TempDir()
	repo := t.TempDir()
	seed := filepath.Join(repo, "seed.txt")
	require.NoError(t, os.WriteFile(seed, nil, 0o666))

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
  aliases:
    issue-implement:
      command:
        - extensionready dummy agent $1
        - extensionready dummy chatskill issue-implement $1
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSched := newTestScheduler(t, mu)
	runner := newPerIDReadyRunner("dummy")
	runnerFn := func(
		_ workspaceapi.URI,
		_ map[extensionapi.Permission]extension.ResourceRegistrar,
		_, _ string, _ browser.Notifications,
		_, _ schemeapi.Executor, _ extension.Grantor, _ text.Editor,
		_ ideauthorizer.PromptOpener, _ storageapi.Service,
		_ func(func()) bool) (extension.Runner, error) {
		return runner, nil
	}

	i, err := New(repo, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(runnerFn)),
		WithScheduleNextTick(scheduleNextTick),
		WithLocker(mu),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	root := i.Ready()
	// The first drain starts scheduler dispatch; before it, callbacks
	// queue so they cannot race New's unsynchronized wiring.
	drainSched()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	i.WaitWorkspaces()

	seedURI, err := workspaceapi.CurrentUserHostURI(seed)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(seedURI))
	mu.Unlock()

	var orderMu sync.Mutex
	var order []string
	record := func(name string) text.CommandHandler {
		return text.FuncCommandHandler(
			func(context.Context, textapi.Command) error {
				orderMu.Lock()
				order = append(order, name)
				orderMu.Unlock()
				return nil
			}, nil)
	}
	require.NoError(t, i.workspaceHandler.subscribeCommand(
		textapi.CommandManual{Name: "agent"}, record("agent")))
	require.NoError(t, i.workspaceHandler.subscribeCommand(
		textapi.CommandManual{Name: "chatskill"}, record("chatskill")))

	invocation := ":issue-implement<space>my-branch<enter>"
	keys, err := term.ParseKeys(invocation)
	require.NoError(t, err)
	for _, k := range keys {
		mu.Lock()
		root.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		mu.Unlock()
	}
	i.workspaceHandler.focusEx().Wait()

	runner.release("dummy")

	require.Eventually(t, func() bool {
		orderMu.Lock()
		defer orderMu.Unlock()
		return len(order) == 2
	}, 5*time.Second, 20*time.Millisecond,
		"both extensionready follow-up commands must run")

	orderMu.Lock()
	defer orderMu.Unlock()
	assert.Equal(t, []string{"agent", "chatskill"}, order,
		"chatskill must dispatch after agent; the per-extension "+
			"extensionready queue must preserve submission order")
}

// TestE2EWorkspaceCloseThenQuit drives :workspaceclose through the real
// IDE command prompt on a second, user-opened workspace that owns a
// live subprocess, then quits via i.Close(). It guards the RUNE async-
// close contract end-to-end on the real event loop:
//
//  1. :workspaceclose returns to the loop immediately (the count drops
//     and the slot is freed) even though the scheme teardown runs in a
//     background goroutine.
//  2. The background teardown really closes the scheme, so the
//     subprocess bound to the scheme ctx dies.
//  3. Quitting afterwards (i.Close) returns cleanly and promptly — the
//     scenario that previously froze the UI for seconds / deadlocked
//     when the whole teardown ran inline on the event loop.
func TestE2EWorkspaceCloseThenQuit(t *testing.T) {
	homeDir := t.TempDir()
	// The second workspace must be a child of the home workspace so
	// the empty-argument :workspaceopen completion resolves it.
	wsDir := filepath.Join(homeDir, "child")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	dataDir := t.TempDir()

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  key: "<c-\\\\>"
workspace:
  auto_restore: false
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(homeDir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = i.Close()
		}
	})

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	wh := i.workspaceHandler

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	// Open the child workspace as a real second workspace slot,
	// exactly as a user would.
	sendKeys("<c-\\\\>workspaceopen<space>" + wsDir + "<enter>")
	i.WaitWorkspaces()

	// Locate the freshly opened child workspace slot (home does not
	// occupy a slot, so the child is the only non-nil entry) and
	// start a long-running subprocess whose lifetime is bound to that
	// workspace's scheme ctx.
	mu.Lock()
	childSlot := -1
	for idx, w := range wh.workspaces {
		if w != nil {
			childSlot = idx
			break
		}
	}
	var (
		switched    bool
		childCwd    workspace.Workspace
		startErr    error
		countBefore int
	)
	ch := make(chan error, 1)
	if childSlot != -1 {
		switched = wh.switchToWorkspace(childSlot)
		childCwd = wh.workspaces[childSlot].cwd
		countBefore = wh.workspaceCount
		if childCwd != nil {
			_, startErr = childCwd.StartCommand(context.Background(),
				workspaceapi.Cmd{
					Path:    "/bin/sh",
					Args:    []string{"-c", "sleep 30"},
					Watcher: workspaceapi.ChanProcessWatcher(ch),
				})
		}
	}
	mu.Unlock()
	require.NotEqual(t, -1, childSlot,
		":workspaceopen must install the child workspace in a slot")
	require.True(t, switched)
	require.NotNil(t, childCwd)
	require.NoError(t, startErr)

	// Drive :workspaceclose through the command prompt, exactly as a
	// user would.
	sendKeys("<c-\\\\>workspaceclose<enter>")

	// The count must drop on the event loop while the background
	// teardown may still be running.
	mu.Lock()
	countAfter := wh.workspaceCount
	mu.Unlock()
	require.Equal(t, countBefore-1, countAfter,
		":workspaceclose must free the slot on the event loop")

	// The background teardown must close the scheme and kill the
	// subprocess bound to its ctx.
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal("subprocess survived :workspaceclose: the scheme ctx " +
			"was not canceled by the background teardown")
	}

	// Drain the background close, then quit. i.Close must return
	// cleanly and promptly — the freeze/deadlock regression guard.
	i.WaitWorkspaces()
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- i.Close() })
	select {
	case cerr := <-done:
		closed = true
		require.NoError(t, cerr, "quitting after :workspaceclose must "+
			"shut down cleanly")
	case <-time.After(30 * time.Second):
		t.Fatal("i.Close deadlocked after :workspaceclose")
	}
}

func TestE2EClipboardPasteIntoNoEchoTerminalRead(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()

	script := filepath.Join(dir, "read-secret.sh")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'password: '
stty -echo
IFS= read -r secret
stty echo
printf '\nRESULT:%s\n' "$secret"
# stay alive so the drop does not remove the terminal mid-assertion
sleep 30
`), 0o755))

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
editor:
  mode: modal
command:
  key: "<c-\\\\>"
  key_bindings:
    <m-v>: clipboardpaste
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(t *testing.T, seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	snapshotTerminalText := func() (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		var text string
		var ok bool
		ex.comp.Browser().IterateWindows(func(win browser.Window) {
			if ok {
				return
			}
			content, err := win.Content()
			if err != nil {
				return
			}
			vte, isVTE := content.(vtereservoir.VTE)
			if !isVTE {
				return
			}
			snap, err := vte.Snapshot()
			if err != nil {
				return
			}
			text = term.CellsToString(snap.ActiveCells())
			ok = true
		})
		return text, ok
	}

	sendKeys(t, "<c-\\\\>terminalnew<space>"+script+"<enter>")
	var promptText string
	require.Eventually(t, func() bool {
		text, ok := snapshotTerminalText()
		promptText = text
		return ok && strings.Contains(text, "password:")
	}, 10*time.Second, 50*time.Millisecond,
		"terminal did not reach password prompt; screen was:\n%s", promptText)

	require.NoError(t, i.workspaceHandler.clip.Copy(
		clipboard.DefaultRegisterID,
		clipboard.Data{Text: "s3cr3t"},
	))
	sendKeys(t, "<m-v><enter>")

	var resultText string
	require.Eventually(t, func() bool {
		text, ok := snapshotTerminalText()
		resultText = text
		return ok && strings.Contains(text, "RESULT:s3cr3t")
	}, 10*time.Second, 50*time.Millisecond,
		"terminal did not receive pasted secret; screen was:\n%s", resultText)
}

// TestE2ETerminalSearchCopiesHighlightedMatch drives the terminal
// scrollback search the way a reader does: <meta-f> over a live shell,
// a query that lands on a match, <esc> to put the box away, and then
// clipboardcopy, which must yield the match that is still highlighted
// on screen.
func TestE2ETerminalSearchCopiesHighlightedMatch(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
editor:
  mode: modal
command:
  key: "<c-\\\\>"
  key_bindings:
    <m-c>: clipboardcopy
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil),
		newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			ev := term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key}
			if ev.Key == term.KeySpace {
				ev.Key, ev.Ch = 0, ' '
			}
			// the event loop fills Raw for printable keys, and the
			// terminal writes exactly that to the shell
			if ev.Ch != 0 && ev.Mod == 0 {
				ev.Raw = []byte(string(ev.Ch))
			}
			mu.Lock()
			root.Handle(ev)
			mu.Unlock()
			i.WaitInflight()
		}
	}

	terminalText := func() (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		var text string
		var ok bool
		i.workspaceHandler.focusEx().comp.Browser().IterateWindows(func(win browser.Window) {
			if ok {
				return
			}
			content, err := win.Content()
			if err != nil {
				return
			}
			emulator, isVTE := content.(vtereservoir.VTE)
			if !isVTE {
				return
			}
			snap, err := emulator.Snapshot()
			if err != nil {
				return
			}
			text, ok = term.CellsToString(snap.ActiveCells()), true
		})
		return text, ok
	}

	sendKeys("<c-\\\\>terminalnew<space>sh<enter>")
	sendKeys("echo<space>needle<enter>")
	var screen string
	require.Eventually(t, func() bool {
		text, ok := terminalText()
		screen = text
		return ok && strings.Count(text, "needle") >= 2
	}, 10*time.Second, 50*time.Millisecond,
		"terminal never printed the searched word; screen was:\n%s", screen)

	// search the scrollback, then dismiss the box to read the match
	sendKeys("<m-f>needle<esc>")
	sendKeys("<m-c>")

	data, err := i.workspaceHandler.clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, "needle", data.Text,
		"clipboardcopy must copy the highlighted match; screen was:\n%s", screen)
}

// TestE2ETerminalSearchCopiesMatchWhileBoxStaysOpen pins the other half
// of the same flow: pressing <meta-c> while the find box is still open
// (no <esc> yet), with nothing selected inside the box itself, must copy
// the highlighted match on the terminal — not the query text, and not
// whatever used to be in the clipboard before the box opened.
func TestE2ETerminalSearchCopiesMatchWhileBoxStaysOpen(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
editor:
  mode: modal
command:
  key: "<c-\\\\>"
  key_bindings:
    <m-c>: clipboardcopy
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil),
		newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			ev := term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key}
			if ev.Key == term.KeySpace {
				ev.Key, ev.Ch = 0, ' '
			}
			if ev.Ch != 0 && ev.Mod == 0 {
				ev.Raw = []byte(string(ev.Ch))
			}
			mu.Lock()
			root.Handle(ev)
			mu.Unlock()
			i.WaitInflight()
		}
	}

	// stale selection made before the box ever opens, which must not be
	// what ends up in the clipboard
	pasteStale := func() {
		mu.Lock()
		defer mu.Unlock()
		require.NoError(t, i.workspaceHandler.clip.Copy(
			clipboard.DefaultRegisterID, clipboard.Data{Text: "stale"}))
	}
	pasteStale()

	terminalText := func() (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		var text string
		var ok bool
		i.workspaceHandler.focusEx().comp.Browser().IterateWindows(func(win browser.Window) {
			if ok {
				return
			}
			content, err := win.Content()
			if err != nil {
				return
			}
			emulator, isVTE := content.(vtereservoir.VTE)
			if !isVTE {
				return
			}
			snap, err := emulator.Snapshot()
			if err != nil {
				return
			}
			text, ok = term.CellsToString(snap.ActiveCells()), true
		})
		return text, ok
	}

	sendKeys("<c-\\\\>terminalnew<space>sh<enter>")
	sendKeys("echo<space>needle<enter>")
	var screen string
	require.Eventually(t, func() bool {
		text, ok := terminalText()
		screen = text
		return ok && strings.Count(text, "needle") >= 2
	}, 10*time.Second, 50*time.Millisecond,
		"terminal never printed the searched word; screen was:\n%s", screen)

	// open the box and search, but leave it open (no <esc>)
	sendKeys("<m-f>needle")

	sendKeys("<m-c>")

	data, err := i.workspaceHandler.clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, "needle", data.Text,
		"clipboardcopy while the box is still open must copy the highlighted "+
			"match, not the stale clipboard entry, and not be swallowed by the "+
			"query input's own empty-selection-copies-the-line shortcut; "+
			"screen was:\n%s", screen)
}

// stageTreeSitterGo installs the committed Go tree-sitter artifacts
// into the idepkg layout (<dataDir>/lib/go) so the syntax parser can
// load the language without a package download.
func stageTreeSitterGo(t *testing.T, dataDir string) {
	t.Helper()
	src := filepath.Join("idelsp", "symbolresolve", "go")
	dst := filepath.Join(dataDir, "lib", "go")
	require.NoError(t, os.MkdirAll(dst, 0o755))
	for _, path := range grammarfixture.LibDir(t, src) {
		data, err := os.ReadFile(path)
		require.NoErrorf(t, err, "missing tree-sitter fixture %s", path)
		require.NoError(t, os.WriteFile(
			filepath.Join(dst, filepath.Base(path)), data, 0o644))
	}
}

// TestE2EWorkspaceSymbolDBIndexing exercises the workspace.symboldb
// flag end-to-end, like production does through rune.star: opening a
// workspace with the flag enabled wraps the syntax parser with the
// persistent symbol database, indexes the workspace's Go files with
// the real tree-sitter artifacts, and serves symbol queries from the
// index.
func TestE2EWorkspaceSymbolDBIndexing(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mylib"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "mylib", "mylib.go"),
		[]byte("package mylib\n\nfunc MyFunc(s string) string { return s }\n"),
		0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "main.go"),
		[]byte("package main\n\nimport \"example.com/e2e/mylib\"\n\n"+
			"func main() { _ = mylib.MyFunc(\"x\") }\n"),
		0o644))

	dataDir := t.TempDir()
	stageTreeSitterGo(t, dataDir)
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
workspace:
  symboldb: true
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	mu.Lock()
	wh := i.workspaceHandler.workspaces[i.workspaceHandler.focus]
	mu.Unlock()
	require.NotNil(t, wh)
	sdb, ok := wh.symbolDBCloser.(*symboldb.Parser)
	require.True(t, ok,
		"workspace parser must be wrapped with the symbol database")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, sdb.Wait(ctx))

	it, err := sdb.ListReferencedSymbols(ctx)
	require.NoError(t, err)
	names, err := sdkiterator.ToSlice(ctx, it)
	require.NoError(t, err)
	// The index emits each qualified name exactly once, unlike the
	// backing parser which streams one entry per occurrence
	// (mylib.MyFunc has both a reference and a definition), so a
	// single occurrence proves the listing was served from the index.
	occurrences := 0
	for _, name := range names {
		if name == "mylib.MyFunc" {
			occurrences++
		}
	}
	require.Equalf(t, 1, occurrences,
		"mylib.MyFunc must be listed exactly once from the index; got %v", names)

	matches, err := sdkiterator.ToSlice(ctx, mustResolve(t, ctx, sdb, "mylib.MyFunc"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.True(t, strings.HasSuffix(matches[0].URI, "/main.go"),
		"mylib.MyFunc must resolve to its reference site; got %q", matches[0].URI)
}

func mustResolve(
	t *testing.T, ctx context.Context, p *symboldb.Parser, name string,
) sdkiterator.Iterator[syntaxapi.Match] {
	t.Helper()
	it, err := p.ResolveSymbol(ctx, name, nil)
	require.NoError(t, err)
	return it
}

// TestWorkspaceSymbolDBDisabledByDefault pins that workspaces do not
// pay for symbol indexing unless workspace.symboldb is enabled.
func TestWorkspaceSymbolDBDisabledByDefault(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "alpha.go"), []byte("package a\n"), 0o644))

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	mu.Lock()
	wh := i.workspaceHandler.workspaces[i.workspaceHandler.focus]
	mu.Unlock()
	require.NotNil(t, wh)
	assert.Nil(t, wh.symbolDBCloser,
		"symbol database must not be built when workspace.symboldb is unset")
}

// TestE2EFileExplorerRefreshDoesNotClobberClipboard reproduces the bug
// where switching git branches (which changes files under the workspace
// root and fires FS-watcher events) clobbers the user's system clipboard
// with the file explorer's directory listing.
//
// The explorer reuses the standard editor, whose copy-on-delete
// subscriber (text.WithCopyDelete) copies any deleted buffer content to
// the default register. When an FS event drives refreshTree ->
// Component.Refresh -> rewriteBufferFromTree, the old tree text is
// deleted from the shared cell.Buffer and was being copied into the
// default register. A programmatic refresh must not touch the clipboard;
// only a real user delete should.
func TestE2EFileExplorerRefreshDoesNotClobberClipboard(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "alpha.go"), []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "beta.go"), []byte("package b\n"), 0o644))

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
editor:
  mode: modal
command:
  key: "<c-\\\\>"
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	// Open the file explorer via the real :fexplorer command path.
	mu.Lock()
	ex := i.workspaceHandler.focusEx()
	require.NoError(t, ex.fexplorer(context.Background()))
	require.NotNil(t, ex.fileExplorerWin, "explorer window should be open")
	explorer := ex.fileExplorerHandler
	require.NotNil(t, explorer)
	mu.Unlock()

	// Prime the default register with a sentinel the user "copied"
	// earlier. A programmatic refresh must leave this untouched.
	const sentinel = "user-copied-sentinel"
	mu.Lock()
	require.NoError(t, ex.clip.Copy(
		clipboard.DefaultRegisterID, clipboard.Data{Text: sentinel}))
	mu.Unlock()

	// Add a sibling file on disk and drive the explorer's real
	// FS-event handler, exactly as the workspace watcher does when a
	// branch switch changes the working tree.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "gamma.go"), []byte("package g\n"), 0o644))
	gammaURI, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, "gamma.go"))
	require.NoError(t, err)

	mu.Lock()
	explorer.onFSEvent(context.Background(),
		textapi.Event{Type: textapi.EventTypeCreate, URI: gammaURI})
	mu.Unlock()
	i.WaitInflight()

	mu.Lock()
	data, err := ex.clip.Paste(clipboard.DefaultRegisterID)
	mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, sentinel, data.Text,
		"file explorer FS-driven refresh must not clobber the clipboard; "+
			"got:\n%s", data.Text)
}

// TestE2EFileExplorerEnterOpensFileAfterReload reproduces the bug
// where, after opening files and running :workspacereload, re-opening
// the file explorer and pressing <enter> on a file does nothing.
//
// A reload discards the old ex and restores the previous session's
// files into windows. The freshly re-opened explorer captures a
// different window as its target, so opening an already-restored file
// hit browser.Window.SetContent's ErrTabNotFree (the tab is already
// rendered in its restored window). editFileURILocal swallowed that
// error, so pressing <enter> silently did nothing. The fix focuses
// the window that already owns the tab.
func TestE2EFileExplorerEnterOpensFileAfterReload(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "alpha.txt"), []byte("alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "beta.txt"), []byte("beta\n"), 0o644))

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  key: "<c-\\\\>"
workspace:
  auto_restore: true
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)
	startupCallback := make(chan struct{})
	require.True(t, scheduleNextTick(func() { close(startupCallback) }))

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	select {
	case <-startupCallback:
		t.Fatal("scheduled callbacks must wait until IDE startup finishes")
	default:
	}
	mu.Lock()
	root.Resize(120, 40)
	mu.Unlock()
	drainSchedule()
	select {
	case <-startupCallback:
	default:
		t.Fatal("scheduled callbacks must run once the event loop starts")
	}
	i.WaitWorkspaces()

	sendKeys := func(t *testing.T, seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	focusedURI := func() string {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		win, _ := ex.comp.Focus()
		if win == nil {
			return ""
		}
		content, cerr := win.Content()
		if cerr != nil || content == nil {
			return ""
		}
		tab, ok := content.(*browser.Tab)
		if !ok {
			return ""
		}
		return tab.URI().String()
	}

	// Open alpha.txt on the left, split a second window on the right
	// and open beta.txt there, then open the file explorer. The
	// explorer's target becomes the right window, distinct from the
	// window that holds alpha.txt.
	sendKeys(t, "<c-\\\\>edit<space>"+filepath.Join(dir, "alpha.txt")+"<enter>")
	sendKeys(t, "<c-\\\\>windownew<space>right<enter>")
	sendKeys(t, "<c-\\\\>edit<space>"+filepath.Join(dir, "beta.txt")+"<enter>")
	sendKeys(t, "<c-\\\\>fexplorer<enter>")

	// Reload the workspace. It tears down the ex and restores the two
	// files asynchronously.
	sendKeys(t, "<c-\\\\>workspacereload<enter>")
	i.WaitWorkspaces()
	i.WaitInflight()
	require.Eventually(t, func() bool {
		mu.Lock()
		ex := i.workspaceHandler.focusEx()
		var files int
		if ex != nil {
			for _, tab := range ex.comp.Browser().Tabs() {
				if strings.HasSuffix(tab.URI().String(), ".txt") {
					files++
				}
			}
		}
		mu.Unlock()
		return files == 2
	}, 30*time.Second, 50*time.Millisecond,
		"workspace reload did not restore both files")

	// Re-open the explorer. Its target window is not the window that
	// holds alpha.txt, so opening alpha.txt must focus alpha.txt's
	// existing window rather than silently doing nothing.
	sendKeys(t, "<c-\\\\>fexplorer<enter>")

	// The explorer renders the workspace tree; the cursor starts on
	// the first entry. Walk down until alpha.txt is focused. Each
	// <enter> on a directory expands/collapses it; on a file it opens
	// it. With a small flat tree alpha.txt is reached within a few
	// rows.
	var opened bool
	for range 8 {
		sendKeys(t, "<enter>")
		if strings.HasSuffix(focusedURI(), "/alpha.txt") {
			opened = true
			break
		}
		sendKeys(t, "<down>")
	}
	require.True(t, opened,
		"pressing enter on alpha.txt in the file explorer after a "+
			"workspacereload must focus the window showing alpha.txt; "+
			"focusedURI=%q", focusedURI())
}

// TestE2ECursorHistoryIntoFileExplorerIsSilent drives <ctrl-o>/<ctrl-i>
// (cursorhistory prev/next) after visiting the file explorer and
// reproduces the bug where the explorer's pseudo-resource
// (memory:///fexplorer) leaked into the cursor history. Navigating
// back onto it re-opened the pseudo-URI as a regular tab, which
// re-registered the explorer's per-file commands ("command already
// registered") and re-locked its swap file, surfacing an
// "is already open by another process" recovery prompt.
//
// After the fix the pseudo-URI is never recorded, so ctrl-o/ctrl-i is
// a silent no-op with respect to the explorer: no recovery prompt
// (floating window) appears and focus never lands on
// memory:///fexplorer.
func TestE2ECursorHistoryIntoFileExplorerIsSilent(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "alpha.txt"), []byte("alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "beta.txt"), []byte("beta\n"), 0o644))

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  key: "<c-\\\\>"
  key_bindings:
    <c-o>: cursorhistory prev
    <c-i>: cursorhistory next
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(120, 40)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(t *testing.T, seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	focusedURI := func() string {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		win, _ := ex.comp.Focus()
		if win == nil {
			return ""
		}
		content, cerr := win.Content()
		if cerr != nil || content == nil {
			return ""
		}
		tab, ok := content.(*browser.Tab)
		if !ok {
			return ""
		}
		return tab.URI().String()
	}

	fexplorerTabs := func() int {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		n := 0
		for _, tab := range ex.comp.Browser().Tabs() {
			if tab.URI().String() == "memory:///fexplorer" {
				n++
			}
		}
		return n
	}

	// Open alpha.txt (a real navigable location recorded in history),
	// then open the file explorer. Focus lands on the explorer window,
	// whose content is the memory:///fexplorer pseudo-buffer.
	sendKeys(t, "<c-\\\\>edit<space>"+filepath.Join(dir, "alpha.txt")+"<enter>")
	sendKeys(t, "<c-\\\\>fexplorer<enter>")
	explorerFocused := func() bool {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		win, _ := ex.comp.Focus()
		return win != nil && win == ex.fileExplorerWin
	}
	require.True(t, explorerFocused(),
		"file explorer must be focused after :fexplorer")

	// Open a file from the explorer with <enter>. This is the real
	// user flow that switches the focused URI away from
	// memory:///fexplorer and, before the fix, recorded the explorer's
	// pseudo-URI as a cursor-history entry. Opening a file moves focus
	// off the explorer, so we re-focus and open a second file to build
	// at least two real history entries around the explorer visit.
	sendKeys(t, "<enter>")
	require.NotEmpty(t, focusedURI(),
		"pressing <enter> in the file explorer must open a file")
	require.NotEqual(t, "memory:///fexplorer", focusedURI())
	firstOpened := focusedURI()

	sendKeys(t, "<c-\\\\>fexplorer<enter>")
	require.True(t, explorerFocused())
	sendKeys(t, "<down><enter>")
	require.NotEqual(t, "memory:///fexplorer", focusedURI())
	require.NotEqual(t, firstOpened, focusedURI(),
		"second explorer open should focus a different file, building "+
			"a history that brackets the explorer pseudo-URI")

	// Walk back and forth through the cursor history. Before the fix,
	// one of these lands on memory:///fexplorer and re-opens it, which
	// pops the "already open by another process" recovery prompt (a
	// floating window) and/or focuses the pseudo-URI.
	for range 6 {
		sendKeys(t, "<c-o>")
		require.NotEqual(t, "memory:///fexplorer", focusedURI(),
			"ctrl-o must never navigate into the file explorer buffer")
		require.Equal(t, 0, countFloatingWindows(i, mu),
			"ctrl-o into the file explorer must not raise a recovery prompt")
		require.Equal(t, 0, fexplorerTabs(),
			"ctrl-o must not re-open the file explorer as a tab")
	}
	for range 6 {
		sendKeys(t, "<c-i>")
		require.NotEqual(t, "memory:///fexplorer", focusedURI(),
			"ctrl-i must never navigate into the file explorer buffer")
		require.Equal(t, 0, countFloatingWindows(i, mu),
			"ctrl-i into the file explorer must not raise a recovery prompt")
		require.Equal(t, 0, fexplorerTabs(),
			"ctrl-i must not re-open the file explorer as a tab")
	}
}

type mockShader struct {
	called bool
	frames []int
}

func (s *mockShader) Shade(frame, total int, in [][]term.Cell) {
	s.called = true
	s.frames = append(s.frames, frame)
}

func makeTestFiles(t *testing.T) (*os.File, *os.File) {
	configFile, err := os.CreateTemp("", "six_ide_test.*.yaml")
	require.NoError(t, err)

	_, err = configFile.WriteString("{}")
	require.NoError(t, err)

	require.NoError(t, configFile.Close())

	file, err := os.CreateTemp("", "six_ide_test.*.go")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	t.Cleanup(func() {
		_ = os.Remove(configFile.Name())
		_ = os.Remove(file.Name())
	})

	return configFile, file
}

// newTestStorage returns the localstorage flavor every IDE test uses.
// Tests pass it as the storage argument to ide.New / IDE.init.
// IDE.Close treats the storage as borrowed and does not close it, so
// the test owns its lifetime; without this cleanup every test leaks a
// firstmover gRPC server and its listener for the whole package run.
func newTestStorage(t *testing.T, dataDir string) storageapi.Service {
	t.Helper()
	storage := localstorage.New(context.Background(), dataDir, docbson.Marshaler())
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

func testRunnerFn(
	uri workspaceapi.URI,
	res map[extensionapi.Permission]extension.ResourceRegistrar,
	dataDir, installDir string, n browser.Notifications,
	exec, extExec schemeapi.Executor,
	grantor extension.Grantor,
	editor text.Editor,
	promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
	scheduleNextTick func(func()) bool) (extension.Runner, error) {
	return testRunner{}, nil
}

type testRunner struct {
}

func (r testRunner) Run(extensionID, path string, config config.Config) error {
	return nil
}

func (r testRunner) Close() error {
	return nil
}

func (r testRunner) WaitReady(ctx context.Context, id string) error {
	return nil
}

// TestIDEExoMisconfigurationFallsBackToDefault is an end-to-end
// guard against exoeditor.New panics when the user's config selects
// `editor.mode = "exo"` but does not supply both required fields
// (`editor.exo.command` containing {file}, and `editor.exo.goto`).
// validateExo rewrites the mode back to "modal" so the IDE boots
// with the built-in modal editor; this test asserts that the
// rewrite actually happens at the config layer so the workspace
// handler never reaches exoeditor.New on a misconfigured input.
//
// Reproduces the panic chain that motivated this guard:
//
//	exoeditor.New: command is required
//	exoeditor.New: invalid gotoTemplate: ...
//
// Either panic would crash the IDE on startup when a user
// previously experimented with `editor.mode = "exo"` and removed
// only part of the exo block.
func TestIDEExoMisconfigurationFallsBackToDefault(t *testing.T) {
	cases := []struct {
		name   string
		exo    string // YAML body inserted under editor:exo
		hasKey bool   // when false, omit the exo block entirely
	}{
		{
			name:   "no exo block at all",
			hasKey: false,
		},
		{
			name: "empty command, valid goto",
			exo: `    command: ""
    goto: "<esc>:{line}<enter>{col}|"`,
			hasKey: true,
		},
		{
			name: "command without {file}, valid goto",
			exo: `    command: "vim"
    goto: "<esc>:{line}<enter>{col}|"`,
			hasKey: true,
		},
		{
			name: "valid command, empty goto",
			exo: `    command: "vim {file}"
    goto: ""`,
			hasKey: true,
		},
		{
			name:   "valid command, missing goto field",
			exo:    `    command: "vim {file}"`,
			hasKey: true,
		},
		{
			name: "valid command, invalid goto",
			exo: `    command: "vim {file}"
    goto: "<bogus-key>"`,
			hasKey: true,
		},
		{
			name: "empty command, empty goto",
			exo: `    command: ""
    goto: ""`,
			hasKey: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configFile, _ := makeTestFiles(t)
			cfg := "editor:\n  mode: exo\n"
			if tc.hasKey {
				cfg += "  exo:\n" + tc.exo + "\n"
			}
			require.NoError(t,
				os.WriteFile(configFile.Name(), []byte(cfg), 0666))

			dir, err := os.MkdirTemp("", "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(dir) })

			cwdURI, err := workspaceapi.CurrentUserHostURI(".")
			require.NoError(t, err)

			// Use init() rather than New() so we can introspect
			// the post-load ideConfig before any workspace
			// handler reaches exoeditor.New. init() must not panic
			// for any of these inputs: validateExo rewrites
			// the mode back to "modal" before the workspace
			// handler instantiates the editor.
			i := new(IDE)
			require.NotPanics(t, func() {
				err = i.init(cwdURI.String(),
					configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
					WithPublishEvent(nopPublishEvent),
					WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
					WithLocker(new(sync.Mutex)))
			}, "IDE init must not panic for misconfigured exo; "+
				"validateExo must rewrite editor.mode to a "+
				"safe fallback before reaching exoeditor.New")
			require.NoError(t, err,
				"IDE init must still succeed for "+
					"misconfigured exo; validateExo "+
					"surfaces a non-fatal config error and "+
					"the IDE boots with the fallback mode")

			assert.NotEqual(t, "exo", i.ideConfig.editorMode(),
				"after validateExo, editor.mode must not "+
					"remain exo; got %q",
				i.ideConfig.editorMode())
			assert.Equal(t, "modal", i.ideConfig.editorMode(),
				"validateExo falls back to the safe "+
					"default mode (modal); a different "+
					"value means the validator regressed "+
					"or a new code path skipped the "+
					"rewrite")

			assert.NoError(t, i.closeResources())
		})
	}
}

// TestIDEExoWellFormedConfigDoesNotFallBack guards against an
// over-eager validateExo that would rewrite legitimate exo
// configurations back to "modal". This is the positive
// counterexample to TestIDEExoMisconfigurationFallsBackToDefault.
func TestIDEExoWellFormedConfigDoesNotFallBack(t *testing.T) {
	configFile, _ := makeTestFiles(t)
	const cfg = `editor:
  mode: exo
  exo:
    command: "vim {file}"
    goto: "<esc>:{line}<enter>{col}|"
    quit: "<esc>:qa!<enter>"
`
	require.NoError(t,
		os.WriteFile(configFile.Name(), []byte(cfg), 0666))

	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	cwdURI, err := workspaceapi.CurrentUserHostURI(".")
	require.NoError(t, err)

	i := new(IDE)
	require.NotPanics(t, func() {
		err = i.init(cwdURI.String(),
			configFile.Name(), dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(new(sync.Mutex)))
	})
	require.NoError(t, err)

	assert.Equal(t, "exo", i.ideConfig.editorMode(),
		"a complete exo config (command + goto) must be "+
			"preserved through validateExo")
	assert.Equal(t, "vim {file}", i.ideConfig.exoCommand())
	assert.Equal(t, "<esc>:{line}<enter>{col}|", i.ideConfig.exoGoto())

	assert.NoError(t, i.closeResources())
}

// minimalStarTutorial is a self-contained Starlark tutorial that
// renders a single floating window. It is enough for the runner to
// install an overlay once dispatched.
const minimalStarTutorial = `
def run():
    floating_window(title="welcome", text="hello")
tutorial(entry=run)
`

// TestIDEStartingTutorialDispatchesOnReady verifies that
// WithStartingTutorial schedules a `:tutorial start <name>` dispatch on
// the event loop once the IDE is ready, and that an unknown name is a
// no-op.
func TestIDEStartingTutorialDispatchesOnReady(t *testing.T) {
	cases := []struct {
		name           string
		starting       string
		withInitShader bool
		wantScheduled  bool
		wantActive     string
	}{
		{
			name:          "known tutorial runs",
			starting:      "basics",
			wantScheduled: true,
			wantActive:    "basics",
		},
		{
			name:           "known tutorial deferred until init shader finishes",
			starting:       "basics",
			withInitShader: true,
			wantScheduled:  true,
			wantActive:     "basics",
		},
		{
			name:          "unknown tutorial is a no-op",
			starting:      "missing",
			wantScheduled: false,
		},
		{
			name:          "empty starting tutorial is a no-op",
			starting:      "",
			wantScheduled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configFile, _ := makeTestFiles(t)
			dataDir := t.TempDir()

			mu := new(sync.Mutex)
			var scheduled []func()
			scheduleNextTick := func(fn func()) bool {
				scheduled = append(scheduled, fn)
				return true
			}

			opts := []Option{
				WithPublishEvent(nopPublishEvent),
				WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
				WithLocker(mu),
				WithScheduleNextTick(scheduleNextTick),
				WithStarlarkTutorial("basics", minimalStarTutorial),
			}
			if tc.starting != "" {
				opts = append(opts, WithStartingTutorial(tc.starting))
			}
			if tc.withInitShader {
				opts = append(opts, WithInitShader(
					func(_ term.Attributes, _ component.FrameCharSet) shader.Shader {
						return new(mockShader)
					},
					30, 1*time.Second,
				))
			}

			i, err := New("", configFile.Name(), dataDir,
				pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), opts...)
			require.NoError(t, err)
			t.Cleanup(func() { _ = i.Close() })

			// Capture the deferred init-shader timer so the test can
			// fire it deterministically instead of waiting in real time.
			var afterDuration time.Duration
			var afterCb func()
			i.options.afterFunc = func(d time.Duration, fn func()) *time.Timer {
				afterDuration = d
				afterCb = fn
				return nil
			}

			root := i.Ready()
			mu.Lock()
			root.Resize(80, 24)
			mu.Unlock()
			i.WaitWorkspaces()

			if !tc.wantScheduled {
				for _, fn := range scheduled {
					mu.Lock()
					fn()
					mu.Unlock()
				}
				assert.Nil(t, i.tutorial.overlay,
					"no tutorial overlay should be active")
				return
			}

			if tc.withInitShader {
				assert.Empty(t, scheduled,
					"tutorial dispatch must be deferred until the init shader finishes")
				require.NotNil(t, afterCb,
					"a deferred timer should be registered for the init shader")
				assert.Equal(t, 1*time.Second+tutorialInitShaderBuffer, afterDuration,
					"tutorial delay must be the init shader duration plus the buffer")
				afterCb()
			}

			require.Len(t, scheduled, 1,
				"exactly one tutorial dispatch should be scheduled")
			mu.Lock()
			scheduled[0]()
			mu.Unlock()

			assert.NotNil(t, i.tutorial.overlay,
				"tutorial overlay should be active after dispatch")
			assert.Equal(t, tc.wantActive, i.tutorial.activeName)
		})
	}
}

// TestIDEOnboardingActiveGate verifies the authorizer onboarding gate
// wired into the workspace handler: a session started with a starting
// tutorial is onboarding for its whole lifetime — including before the
// deferred tutorial dispatch, when extensions boot and ask to run
// commands — and no other session ever is.
func TestIDEOnboardingActiveGate(t *testing.T) {
	cases := []struct {
		name         string
		withStarting bool
	}{
		{name: "starting tutorial session", withStarting: true},
		{name: "session without starting tutorial", withStarting: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configFile, _ := makeTestFiles(t)
			dataDir := t.TempDir()
			mu := new(sync.Mutex)
			opts := []Option{
				WithPublishEvent(nopPublishEvent),
				WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
				WithLocker(mu),
				WithScheduleNextTick(func(fn func()) bool {
					fn()
					return true
				}),
				WithStarlarkTutorial("basics", minimalStarTutorial),
			}
			if tc.withStarting {
				opts = append(opts, WithStartingTutorial("basics"))
			}
			i, err := New("", configFile.Name(), dataDir,
				pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), opts...)
			require.NoError(t, err)
			t.Cleanup(func() { _ = i.Close() })

			gate := i.workspaceHandler.onboardingActive
			require.NotNil(t, gate)
			assert.Equal(t, tc.withStarting, gate(),
				"gate must be active before the tutorial is dispatched")

			mu.Lock()
			require.NoError(t, i.tutorial.HandleCommand(context.Background(),
				textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
			mu.Unlock()
			assert.Equal(t, tc.withStarting, gate(),
				"gate must stay active while the tutorial runs")

			mu.Lock()
			require.NoError(t, i.tutorial.HandleCommand(context.Background(),
				textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
			mu.Unlock()
			assert.Equal(t, tc.withStarting, gate(),
				"gate must survive the gap between playlist tutorials")
		})
	}
}

func TestIDEPlaylistPromptsForNextTutorial(t *testing.T) {
	newIDE := func(t *testing.T, registerNavigation bool) (*IDE, *sync.Mutex) {
		t.Helper()
		configFile, _ := makeTestFiles(t)
		dataDir := t.TempDir()
		mu := new(sync.Mutex)
		opts := []Option{
			WithPublishEvent(nopPublishEvent),
			WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
			WithLocker(mu),
			WithScheduleNextTick(func(fn func()) bool {
				fn()
				return true
			}),
			WithStarlarkTutorial("basics", `
def run():
    pass
tutorial(entry=run)
`),
			WithTutorialPlaylist(
				TutorialPlaylistItem{Name: "basics", Description: "Learn the basics."},
				TutorialPlaylistItem{Name: "navigation", Description: "Navigate code."},
			),
		}
		if registerNavigation {
			opts = append(opts, WithStarlarkTutorial("navigation", minimalStarTutorial))
		}
		i, err := New("", configFile.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir), opts...)
		require.NoError(t, err)
		t.Cleanup(func() { _ = i.Close() })
		root := i.Ready()
		mu.Lock()
		root.Resize(80, 24)
		mu.Unlock()
		i.WaitWorkspaces()
		i.options.afterFunc = func(_ time.Duration, fn func()) *time.Timer {
			fn()
			return nil
		}
		return i, mu
	}

	completeBasics := func(t *testing.T, i *IDE, mu sync.Locker) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		require.NoError(t, i.tutorial.HandleCommand(context.Background(),
			textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
		_, _ = i.tutorial.Handle(term.Event{Type: term.EventInterrupt})
	}

	prompt := func(t *testing.T, i *IDE, mu sync.Locker) *sdkhandler.Prompt {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		var floating browser.Window
		i.Browser().IterateWindows(func(w browser.Window) {
			if w.IsFloating() {
				floating = w
			}
		})
		require.NotNil(t, floating)
		content, err := floating.Content()
		require.NoError(t, err)
		ret, ok := content.(*sdkhandler.Prompt)
		require.True(t, ok)
		return ret
	}

	promptText := func(t *testing.T, p *sdkhandler.Prompt) string {
		t.Helper()
		width, height := p.Dimensions()
		p.Resize(width, height)
		w := term.NewStringWriter(width, height)
		p.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}

	t.Run("Yes starts the configured next tutorial", func(t *testing.T) {
		i, mu := newIDE(t, true)
		completeBasics(t, i, mu)
		p := prompt(t, i, mu)
		assert.Contains(t, promptText(t, p),
			"Do you want to do the navigation tutorial now?")
		assert.Contains(t, promptText(t, p), "Navigate code.")

		mu.Lock()
		_, handled := p.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
		assert.True(t, handled)
		assert.Equal(t, "navigation", i.tutorial.activeName)
		mu.Unlock()
	})

	t.Run("No leaves no tutorial active", func(t *testing.T) {
		i, mu := newIDE(t, true)
		completeBasics(t, i, mu)
		p := prompt(t, i, mu)

		mu.Lock()
		_, handled := p.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
		assert.True(t, handled)
		assert.Nil(t, i.tutorial.overlay)
		assert.Empty(t, i.tutorial.activeName)
		mu.Unlock()
	})

	t.Run("prompt waits before opening", func(t *testing.T) {
		i, mu := newIDE(t, true)
		var (
			delay time.Duration
			fire  func()
		)
		i.options.afterFunc = func(d time.Duration, fn func()) *time.Timer {
			delay = d
			fire = fn
			return nil
		}

		completeBasics(t, i, mu)
		assert.Equal(t, 0, countFloatingWindows(i, mu))
		assert.Equal(t, 3*time.Second, delay)
		require.NotNil(t, fire)

		fire()
		assert.Equal(t, 1, countFloatingWindows(i, mu))
	})

	t.Run("last and unavailable entries do not prompt", func(t *testing.T) {
		i, mu := newIDE(t, true)
		mu.Lock()
		i.onTutorialCompleted("navigation")
		mu.Unlock()
		assert.Equal(t, 0, countFloatingWindows(i, mu))

		i, mu = newIDE(t, false)
		completeBasics(t, i, mu)
		assert.Equal(t, 0, countFloatingWindows(i, mu))
	})
}

// TestIDECloseCommandPrompt pins the public CloseCommandPrompt seam the
// native menu bar uses before dispatching a command: it dismisses an
// open prompt in the focused workspace and is a no-op when none is
// open. Without it, a menu command that opens its own picker would sit
// behind the always-on-top command prompt overlay.
func TestIDECloseCommandPrompt(t *testing.T) {
	configFile, _ := makeTestFiles(t)
	dataDir := t.TempDir()

	mu := new(sync.Mutex)
	scheduleNextTick, drain := newTestScheduler(t, mu)

	i, err := New("", configFile.Name(), dataDir,
		pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	// Start scheduler dispatch before waiting: the install lands
	// through a scheduled callback.
	drain()
	i.WaitWorkspaces()
	drain()

	mu.Lock()
	i.workspaceHandler.focusEx().openCommandPrompt()
	cmd := i.workspaceHandler.focusEx().cmd
	mu.Unlock()
	require.NotNil(t, cmd, "the command prompt should be open")

	mu.Lock()
	err = i.CloseCommandPrompt()
	mu.Unlock()
	require.NoError(t, err)

	mu.Lock()
	cmd = i.workspaceHandler.focusEx().cmd
	mu.Unlock()
	assert.Nil(t, cmd, "CloseCommandPrompt must dismiss the open prompt")

	// Idempotent: closing again with no prompt open still succeeds.
	mu.Lock()
	err = i.CloseCommandPrompt()
	mu.Unlock()
	assert.NoError(t, err)
}

// TestCloseDoesNotCloseBorrowedStorage verifies that closing an IDE does
// not propagate Close to the storage service it borrowed from the caller.
//
// The storage handle is owned by the embedder (cmd/rune's bootstrap handler
// and main, which create it via localstorage.New and close it once at
// shutdown). It is shared: the bootstrap flow builds a pre-config IDE and a
// configured IDE over the same storage, and the configured IDE's LLM router
// keeps reading aliases from it. If IDE.Close closes the borrowed storage,
// closing the pre-config IDE during the bootstrap swap tears the storage
// down underneath the live configured IDE, so the next read fails with
// "firstmover: Partition on closed Service" (observed on fresh installs as
// the rune-agent extension failing to start after :workspacereload).
func TestCloseDoesNotCloseBorrowedStorage(t *testing.T) {
	t.Parallel()
	_, config := makeTestFiles(t)
	dataDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	store := &closeCountingService{Service: newTestStorage(t, dataDir)}
	t.Cleanup(func() { _ = store.Service.Close() })

	i, err := New("", config.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), store,
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)))
	require.NoError(t, err)
	_ = i.Ready()
	i.WaitWorkspaces()

	require.NoError(t, i.Close())

	assert.Equal(t, int32(0), store.closeCount.Load(),
		"IDE.Close must not close the borrowed shared storage")

	// The borrowed storage must remain usable after the IDE is closed:
	// a partition must not error with "Partition on closed Service".
	_, perr := store.Partition("after-close")
	require.NoError(t, perr,
		"borrowed storage must stay usable after IDE.Close")
	require.NoError(t, store.Set(context.Background(), "probe",
		map[string]any{"k": "v"}))
}

// closeCountingService wraps a storageapi.Service and counts Close calls so
// the test can assert that a borrowed service is not closed by the IDE.
type closeCountingService struct {
	storageapi.Service
	closeCount atomic.Int32
}

func (s *closeCountingService) Close() error {
	s.closeCount.Add(1)
	return s.Service.Close()
}

// TestSharedStorageSurvivesPreIDEClose reproduces the fresh-install bootstrap
// swap: cmd/rune builds a pre-config IDE and a configured IDE over the same
// borrowed storage, then closes the pre-config IDE. Closing the first IDE must
// not tear down the storage that the second IDE still uses, otherwise the
// configured IDE's next storage read fails with
// "firstmover: Partition on closed Service".
func TestSharedStorageSurvivesPreIDEClose(t *testing.T) {
	t.Parallel()
	_, config := makeTestFiles(t)
	dataDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	shared := newTestStorage(t, dataDir)
	trust := pkgtrust.NewStore(dataDir, nil)
	t.Cleanup(func() { _ = shared.Close() })

	opts := []Option{
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(new(sync.Mutex)),
	}

	preIDE, err := New("", config.Name(), dataDir, trust, shared, opts...)
	require.NoError(t, err)
	_ = preIDE.Ready()
	preIDE.WaitWorkspaces()

	configuredIDE, err := New("", config.Name(), dataDir, trust, shared, opts...)
	require.NoError(t, err)
	_ = configuredIDE.Ready()
	configuredIDE.WaitWorkspaces()
	t.Cleanup(func() { _ = configuredIDE.Close() })

	require.NoError(t, preIDE.Close())

	// The configured IDE (and the caller) must still be able to partition
	// the shared storage after the pre-config IDE has been closed. Partition
	// is the operation that fails with "firstmover: Partition on closed
	// Service" when the shared root has been torn down; it is the path the
	// host's storagerpc bridge and the LLM router's alias store exercise.
	_, perr := shared.Partition("after-pre-ide-close")
	require.NoError(t, perr,
		"shared storage must stay partitionable after pre-config IDE.Close")
}

// TestIDEOpenDoesNotReadProtectedDirs asserts that opening the IDE on the
// user's home never reads app data under ~/Library.
func TestIDEOpenDoesNotReadProtectedDirs(t *testing.T) {
	usr, err := user.Current()
	require.NoError(t, err)
	if usr.HomeDir == "" {
		t.Skip("no home directory for current user")
	}
	home := filepath.Clean(usr.HomeDir)

	forbidden := []string{
		filepath.Join(home, "Library"),
	}

	tracker := &readTracker{home: home, forbidden: forbidden}
	const scheme = "trackhome"
	homeURI := scheme + "://" + home

	configFile, _ := makeTestFiles(t)
	dataDir := t.TempDir()

	mu := new(sync.Mutex)
	scheduleNextTick, drain := newTestScheduler(t, mu)
	i, err := New(homeURI, configFile.Name(), dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithScheme(scheme, tracker.newScheme),
	)
	require.NoError(t, err)

	_ = i.Ready()
	// Start scheduler dispatch before waiting: the install lands
	// through a scheduled callback, and dispatch stays parked until
	// the first drain so callbacks cannot race New's wiring.
	drain()
	i.WaitWorkspaces()
	drain()

	i.workspaceHandler.mu.Lock()
	var cwd workspace.Workspace
	for _, wh := range i.workspaceHandler.workspaces {
		if wh != nil && wh.cwd != nil {
			cwd = wh.cwd
			break
		}
	}
	i.workspaceHandler.mu.Unlock()
	require.NotNil(t, cwd, "cwd workspace must be installed")

	// LoadGitignore is the workspace-open tree walk run by the FS
	// monitor and task manager; drive it directly so the assertion is
	// deterministic rather than racing those detached goroutines.
	tracker.reset()
	_, err = vctrl.LoadGitignore(cwd)
	require.NoError(t, err)

	require.NoError(t, i.closeResources())

	reads := tracker.snapshot()
	require.NotEmpty(t, reads, "LoadGitignore must walk the workspace root")
	for _, p := range reads {
		for _, root := range forbidden {
			assert.Falsef(t, p == root || strings.HasPrefix(p, root+string(filepath.Separator)),
				"opening the IDE must not read protected dir: %q", p)
		}
	}
}

type readTracker struct {
	home      string
	forbidden []string

	mu    sync.Mutex
	reads []string
}

func (rt *readTracker) newScheme(
	ctx context.Context, cfg config.Config, _ workspaceapi.URI,
) (schemeapi.Scheme, error) {
	uri, err := workspaceapi.ParseURI("file://" + rt.home)
	if err != nil {
		return nil, err
	}
	inner, err := workspace.NewFileScheme(ctx, cfg, uri)
	if err != nil {
		return nil, err
	}
	return &trackingScheme{Scheme: inner, rt: rt}, nil
}

func (rt *readTracker) record(abs string) {
	rt.mu.Lock()
	rt.reads = append(rt.reads, abs)
	rt.mu.Unlock()
}

func (rt *readTracker) snapshot() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]string, len(rt.reads))
	copy(out, rt.reads)
	return out
}

func (rt *readTracker) reset() {
	rt.mu.Lock()
	rt.reads = nil
	rt.mu.Unlock()
}

type trackingScheme struct {
	schemeapi.Scheme
	rt *readTracker
}

func (s *trackingScheme) abs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(s.rt.home, p)
}

func (s *trackingScheme) ReadDir(path string) ([]fs.DirEntry, error) {
	abs := s.abs(path)
	s.rt.record(abs)
	if abs == s.rt.home {
		// Synthesize a home listing of only the protected dirs so the
		// walk stays bounded: a correct matcher prunes all four and
		// recurses nowhere, while any descent records a violation.
		entries := make([]fs.DirEntry, len(s.rt.forbidden))
		for i, root := range s.rt.forbidden {
			entries[i] = protectedDirEntry(filepath.Base(root))
		}
		return entries, nil
	}
	return s.Scheme.ReadDir(path)
}

func (s *trackingScheme) Open(filename string) (workspaceapi.File, error) {
	s.rt.record(s.abs(filename))
	return s.Scheme.Open(filename)
}

func (s *trackingScheme) OpenFile(
	filename string, flag int, perm fs.FileMode,
) (workspaceapi.File, error) {
	s.rt.record(s.abs(filename))
	return s.Scheme.OpenFile(filename, flag, perm)
}

func (s *trackingScheme) Stat(filename string) (fs.FileInfo, error) {
	s.rt.record(s.abs(filename))
	return s.Scheme.Stat(filename)
}

func (s *trackingScheme) Lstat(filename string) (fs.FileInfo, error) {
	s.rt.record(s.abs(filename))
	return s.Scheme.Lstat(filename)
}

func (s *trackingScheme) Watch(
	string, chan<- schemeapi.EventInfo, ...schemeapi.Event,
) (int, error) {
	return 0, nil
}

func (s *trackingScheme) StopWatch(int) error { return nil }

type protectedDirEntry string

func (e protectedDirEntry) Name() string               { return string(e) }
func (e protectedDirEntry) IsDir() bool                { return true }
func (e protectedDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e protectedDirEntry) Info() (fs.FileInfo, error) { return protectedDirInfo(e), nil }

type protectedDirInfo string

func (i protectedDirInfo) Name() string       { return string(i) }
func (i protectedDirInfo) Size() int64        { return 0 }
func (i protectedDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (i protectedDirInfo) ModTime() time.Time { return time.Time{} }
func (i protectedDirInfo) IsDir() bool        { return true }
func (i protectedDirInfo) Sys() any           { return nil }

// TestCommandPromptKeyBindingHintsIntegration renders the command
// prompt with key hints resolved from the shipped modal preset
// binding. The <alt-enter> echo prefill must surface as a right-aligned
// hint on the windowconverttab row.
func TestCommandPromptKeyBindingHintsIntegration(t *testing.T) {
	const echoKey = "<alt-enter>"

	// pin the test to the shipped default: the modal preset must keep
	// binding <alt-enter> to the windowconverttab prompt prefill.
	preset, err := os.ReadFile("../../cmd/rune/preset_modal.yaml")
	require.NoError(t, err)
	presetCfg, err := decodeOverlayConfigFile(
		bytes.NewReader(preset), "preset_modal.yaml", map[string]any{})
	require.NoError(t, err)
	presetCmd, ok := presetCfg["command"].(map[string]any)
	require.True(t, ok, "preset_modal.yaml: missing `command` section")
	presetBindings, ok := presetCmd["key_bindings"].(map[string]any)
	require.True(t, ok, "preset_modal.yaml: missing `command.key_bindings`")
	echoBody, ok := presetBindings[echoKey].(string)
	require.Truef(t, ok, "preset_modal.yaml: missing %q key binding", echoKey)
	require.Equal(t, "echo {prompt}windowconverttab<space>", echoBody)

	cfg := defaultConfigWithWrap(false)
	cfg.cfg["command"].(map[string]any)["key_bindings"].(map[string]any)[echoKey] = echoBody

	m := newTestWorkspaceManagerHandlerWithDirs(t, cfg, "/tmp", "",
		nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{InputSequence: ":", Expected: `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
┌──────────────────────────────────────┐
│ ▐                                    │
│ !                                    │
│ !!                                   │
│ addBlaBla                            │
│ cheatsheet                           │
│ clipboardcopy                        │
│ clipboardpaste                       │
│ console                              │
│ cursorhistory                        │
│ debugger                             │
└──────────────────────────────────────┘`},
		{InputSequence: "windowconverttab", Expected: `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
┌──────────────────────────────────────┐
│ windowconverttab▐                    │
│ windowconverttab         <alt-enter> │
│                                      │
└──────────────────────────────────────┘
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 40, 20, cases)

	require.NoError(t, m.Close())
}

// The console mouse-selection tests below run in both prompt-editor
// modes ("modeless" standard, "modal" vi in insert mode). Mouse
// events are injected directly because handlertest input sequences
// cannot carry mouse coordinates.

// consoleSelBaseFrame is the console tab after running `lines`.
const consoleSelBaseFrame = "┌━━━━━━━━━─────────┐\n" +
	"│\ue691 console         │\n" +
	"├──────────────────┤\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│> lines           │\n" +
	"│alpha bravo charli│\n" +
	"│e                 │\n" +
	"│> ▐               │\n" +
	"└──────────────────┘"

// consoleSelTypedFrame is consoleSelBaseFrame after typing 40 digits.
const consoleSelTypedFrame = "┌━━━━━━━━━─────────┐\n" +
	"│\ue691 console         │\n" +
	"├──────────────────┤\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│                  │\n" +
	"│> lines           │\n" +
	"│> 0123456789012345│\n" +
	"│678901234567890123│\n" +
	"│456789▐           │\n" +
	"└──────────────────┘"

// consoleSelRecalledFrame is the console after submitting
// `lines 0123...` and recalling it with <up>.
const consoleSelRecalledFrame = "┌━━━━━━━━━─────────┐\n" +
	"│\ue691 console         │\n" +
	"├──────────────────┤\n" +
	"│alpha bravo charli│\n" +
	"│e                 │\n" +
	"│> lines 0123456789│\n" +
	"│012345678901234567│\n" +
	"│890123456789      │\n" +
	"│> lines 0123456789│\n" +
	"│012345678901234567│\n" +
	"│890123456789▐     │\n" +
	"└──────────────────┘"

// consoleSelRecalledDragFrame is consoleSelRecalledFrame after
// dragging (2,9)->(10,9).
const consoleSelRecalledDragFrame = "┌━━━━━━━━━─────────┐\n" +
	"│\ue691 console         │\n" +
	"├──────────────────┤\n" +
	"│alpha bravo charli│\n" +
	"│e                 │\n" +
	"│> lines 0123456789│\n" +
	"│012345678901234567│\n" +
	"│890123456789      │\n" +
	"│> lines 0123456789│\n" +
	"│012345678▐01234567│\n" +
	"│890123456789      │\n" +
	"└──────────────────┘"

const consoleSelNoReverseRow = "...................."

// consoleLinesREPL emits fixed output lines.
type consoleLinesREPL struct{ lines []string }

func (c *consoleLinesREPL) HandleCommand(
	_ context.Context, _ repl.Command, _ repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	out := make([]component.Responsive, len(c.lines))
	for i, l := range c.lines {
		out[i] = component.NewResponsiveString(l, component.StringResponsiveConfig{})
	}
	return sdkiterator.FromSlice(out), nil
}

func (*consoleLinesREPL) Complete(
	context.Context, string, []string,
) (sdkiterator.Iterator[string], error) {
	return sdkiterator.Empty[string](), nil
}

func (*consoleLinesREPL) Help(
	context.Context, []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

// newConsoleSelectionEx opens the companion console and runs `lines`.
func newConsoleSelectionEx(t *testing.T, modal bool) (testEx, *term.StringWriter) {
	t.Helper()
	workspaceURI, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	b := newExForTestingWithWorkspace(t, w, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	t.Cleanup(func() { _ = b.Close() })

	if modal {
		b.ex.promptEditor = viPromptEditor{
			tabspaces:        4,
			scheduleNextTick: cfg.ScheduleNextTick,
			clipboard:        clipboard.NewInMemory(),
		}
		b.ex.consoleCfg = consoleConfig{modal: true, modalStartInsert: true}
	}

	require.NoError(t, b.comp.RegisterREPLCommand(
		textapi.CommandManual{Name: "lines", Summary: "emit lines"},
		&consoleLinesREPL{lines: []string{"alpha bravo charlie"}},
	))

	writer := term.NewStringWriter(20, 12)
	writer.BackgroundCh = '#'
	handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
		[]handlertest.SequenceTestCase{{
			InputSequence: "<c-\\\\>console<enter>lines<enter>",
			Expected:      consoleSelBaseFrame,
		}})
	return b, writer
}

func consoleMouse(b testEx, key term.Key, x, y int) {
	b.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y})
}

// consoleDrag presses at (x1,y1), drags to (x2,y2) and releases.
func consoleDrag(b testEx, x1, y1, x2, y2 int) {
	consoleMouse(b, term.MouseLeft, x1, y1)
	consoleMouse(b, term.MouseLeft, x2, y2)
	consoleMouse(b, term.MouseRelease, x2, y2)
}

// reverseVideoMap projects AttrReverse cells into a '#' map, since
// StringWriter substitution only visualizes Bg/Fg colors.
func reverseVideoMap(writer *term.StringWriter, width, height int) string {
	cells := writer.Cells()
	rows := make([]string, height)
	for y := range height {
		var sb strings.Builder
		for x := range width {
			if cells[y*width+x].Attrs&term.AttrReverse != 0 {
				sb.WriteByte('#')
			} else {
				sb.WriteByte('.')
			}
		}
		rows[y] = sb.String()
	}
	return strings.Join(rows, "\n")
}

func consoleSelReverseMap(rows map[int]string) string {
	out := make([]string, 12)
	for y := range out {
		if r, ok := rows[y]; ok {
			out[y] = r
		} else {
			out[y] = consoleSelNoReverseRow
		}
	}
	return strings.Join(out, "\n")
}

var consoleSelModes = []struct {
	name  string
	modal bool
}{{"modeless", false}, {"modal", true}}

// TestConsoleMouseSelectionOutputEmptyPrompt: dragging over command
// output with an empty prompt selects and highlights the text.
func TestConsoleMouseSelectionOutputEmptyPrompt(t *testing.T) {
	for _, tc := range consoleSelModes {
		t.Run(tc.name, func(t *testing.T) {
			b, writer := newConsoleSelectionEx(t, tc.modal)

			consoleDrag(b, 1, 8, 11, 8)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: "",
					Expected:      consoleSelBaseFrame,
				}})
			assert.Equal(t,
				consoleSelReverseMap(map[int]string{8: ".###########........"}),
				reverseVideoMap(writer, 20, 12))
			sel, ok := b.Selection()
			require.True(t, ok, "output drag must produce a selection")
			assert.Equal(t, "alpha bravo", sel)
		})
	}
}

// TestConsoleMouseSelectionOutputWithWrappedInput: dragging over
// output with a wrapped un-submitted command still selects it.
func TestConsoleMouseSelectionOutputWithWrappedInput(t *testing.T) {
	for _, tc := range consoleSelModes {
		t.Run(tc.name, func(t *testing.T) {
			b, writer := newConsoleSelectionEx(t, tc.modal)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: strings.Repeat("0123456789", 4),
					Expected:      consoleSelTypedFrame,
				}})

			consoleDrag(b, 3, 7, 7, 7)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: "",
					Expected:      consoleSelTypedFrame,
				}})
			assert.Equal(t,
				consoleSelReverseMap(map[int]string{7: "...#####............"}),
				reverseVideoMap(writer, 20, 12))
			sel, ok := b.Selection()
			require.True(t, ok, "output drag must produce a selection")
			assert.Equal(t, "lines", sel)
		})
	}
}

// TestConsoleMouseSelectionInputBandText: dragging over the prompt
// band selects and highlights the dragged range. The editors differ
// by one cell: standard is end-exclusive, vi includes the cursor cell.
func TestConsoleMouseSelectionInputBandText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		modal   bool
		wantSel string
		wantRev string
	}{
		{"modeless", false, "12345678", "..########.........."},
		{"modal", true, "123456789", "..#########........."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, writer := newConsoleSelectionEx(t, tc.modal)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: "lines<space>" + strings.Repeat("0123456789", 4) +
						"<enter><up>",
					Expected: consoleSelRecalledFrame,
				}})

			// (2,9)->(10,9) spans buffer columns 17..25.
			consoleDrag(b, 2, 9, 10, 9)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: "",
					Expected:      consoleSelRecalledDragFrame,
				}})
			assert.Equal(t,
				consoleSelReverseMap(map[int]string{9: tc.wantRev}),
				reverseVideoMap(writer, 20, 12))
			sel, _ := b.Selection()
			assert.Equal(t, tc.wantSel, sel,
				"input-band drag must select the dragged command text")
		})
	}
}

// TestConsoleMouseSelectionOutputAfterInputBandClick: an input-band
// click must not latch mouse routing away from the output band.
func TestConsoleMouseSelectionOutputAfterInputBandClick(t *testing.T) {
	for _, tc := range consoleSelModes {
		t.Run(tc.name, func(t *testing.T) {
			b, writer := newConsoleSelectionEx(t, tc.modal)

			consoleMouse(b, term.MouseLeft, 3, 10)
			consoleMouse(b, term.MouseRelease, 3, 10)
			consoleDrag(b, 1, 8, 11, 8)

			handlertest.RunHandlerSequenceWriter(t, writer, b, 20, 12,
				[]handlertest.SequenceTestCase{{
					InputSequence: "",
					Expected:      consoleSelBaseFrame,
				}})
			assert.Equal(t,
				consoleSelReverseMap(map[int]string{8: ".###########........"}),
				reverseVideoMap(writer, 20, 12))
			sel, ok := b.Selection()
			require.True(t, ok, "output drag after an input-band click must select")
			assert.Equal(t, "alpha bravo", sel)
		})
	}
}
