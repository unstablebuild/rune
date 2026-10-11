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

//go:build e2e

package ide

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide/console/pkgconsole"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

func TestGitHubPkgExtensionEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping github package e2e in -short mode")
	}
	if os.Getenv("RUNE_GITHUB_PKG_E2E") != "1" {
		t.Skip("set RUNE_GITHUB_PKG_E2E=1 to run the github package e2e " +
			"(builds real python/go/rust toolchain environments)")
	}

	t.Run("go", func(t *testing.T) {
		goBin := lookPathOrSkip(t, "go")
		sdkVersion, sdkDir := goSDKModule(t)
		runGitHubPkgE2E(t, ghE2EFixture{
			lang:        "go",
			owner:       "rune-e2e",
			repo:        "go-demo",
			extID:       "gh-go-demo",
			requirement: "go",
			toolPath:    goBin,
			entrypoint:  "main.go",
			files: map[string]string{
				"go.mod": fmt.Sprintf(
					"module ghgodemo\n\ngo 1.24\n\n"+
						"require github.com/unstablebuild/rune-go-sdk %s\n\n"+
						"replace github.com/unstablebuild/rune-go-sdk => %s\n",
					sdkVersion, sdkDir),
				"main.go": ghGoExtensionMain,
			},
			prepare: func(t *testing.T, repoDir string) {
				// Generate go.sum for the SDK's transitive deps from the
				// host module cache (network fallback on cold caches).
				tidy := exec.Command(goBin, "mod", "tidy")
				tidy.Dir = repoDir
				out, err := tidy.CombinedOutput()
				require.NoError(t, err, "go mod tidy: %s", out)
			},
			waitFor: 5 * time.Minute,
			sentinel: func(ctx context.Context, dataDir, wsDir string) bool {
				return storageSentinelPresent(ctx, dataDir, "gh-go-demo", "go")
			},
		})
	})

	t.Run("python", func(t *testing.T) {
		uvBin := lookPathOrSkip(t, "uv")
		sdkDir := sdkCheckoutOrSkip(t, "RUNE_PYTHON_SDK_DIR", "rune-python-sdk")
		runGitHubPkgE2E(t, ghE2EFixture{
			lang:        "python",
			owner:       "rune-e2e",
			repo:        "python-demo",
			extID:       "gh-python-demo",
			requirement: "python",
			toolPath:    uvBin,
			entrypoint:  "main.py",
			files: map[string]string{
				"pyproject.toml": fmt.Sprintf(
					"[project]\nname = \"gh-python-demo\"\nversion = \"0.0.1\"\n"+
						"requires-python = \">=3.11\"\ndependencies = [\"rune-sdk\"]\n\n"+
						"[tool.uv.sources]\nrune-sdk = { path = %q }\n",
					sdkDir),
				"main.py": ghPythonExtensionMain,
			},
			waitFor: 5 * time.Minute,
			sentinel: func(ctx context.Context, dataDir, wsDir string) bool {
				return storageSentinelPresent(ctx, dataDir, "gh-python-demo", "python")
			},
		})
	})

	t.Run("rust", func(t *testing.T) {
		cargoBin := lookPathOrSkip(t, "cargo")
		sdkDir := sdkCheckoutOrSkip(t, "RUNE_RUST_SDK_DIR", "rune-rust-sdk")
		runGitHubPkgE2E(t, ghE2EFixture{
			lang:        "rust",
			owner:       "rune-e2e",
			repo:        "rust-demo",
			extID:       "gh-rust-demo",
			requirement: "rust",
			toolPath:    cargoBin,
			entrypoint:  "src/main.rs",
			files: map[string]string{
				"Cargo.toml": fmt.Sprintf(
					"[package]\nname = \"gh-rust-demo\"\nversion = \"0.1.0\"\n"+
						"edition = \"2021\"\n\n[workspace]\n\n[dependencies]\n"+
						"rune-extension = { path = %q }\n"+
						"rune-api = { path = %q, features = [\"workspace\"] }\n"+
						"serde_json = \"1\"\n"+
						"tokio = { version = \"1\", features = [\"macros\", \"rt-multi-thread\"] }\n",
					filepath.Join(sdkDir, "crates", "rune-extension"),
					filepath.Join(sdkDir, "crates", "rune-api")),
				"src/main.rs": ghRustExtensionMain,
			},
			waitFor: 10 * time.Minute,
			sentinel: func(ctx context.Context, dataDir, wsDir string) bool {
				_, err := os.Stat(filepath.Join(wsDir, "gh-rust-demo-sentinel"))
				return err == nil
			},
		})
	})
}

// ghE2EFixture describes one language variant of the end-to-end test.
type ghE2EFixture struct {
	lang        string
	owner, repo string
	extID       string
	// requirement is the official-distribution package the repo's
	// config.yaml requires; its fake tarball delivers toolPath into
	// <dataDir>/bin under the requirement's toolchain binary name.
	requirement string
	toolPath    string
	entrypoint  string
	files       map[string]string
	prepare     func(t *testing.T, repoDir string)
	waitFor     time.Duration
	sentinel    func(ctx context.Context, dataDir, wsDir string) bool
}

func (f ghE2EFixture) pkgID() string {
	return "github.com/" + f.owner + "/" + f.repo
}

// toolBinaryName maps a requirement package to the binary the
// extension runner resolves in <dataDir>/bin for that language.
func (f ghE2EFixture) toolBinaryName() string {
	switch f.requirement {
	case "python":
		return "uv"
	case "rust":
		return "cargo"
	default:
		return f.requirement
	}
}

func runGitHubPkgE2E(t *testing.T, f ghE2EFixture) {
	// Fixture repo: a complete extension project plus the package
	// config declaring the source entrypoint and the toolchain
	// requirement.
	files := map[string]string{
		"config.yaml": "requirements:\n  - " + f.requirement + "\n" +
			"extensions:\n  " + f.extID + ":\n" +
			"    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/" + f.entrypoint + "\n",
	}
	maps.Copy(files, f.files)
	gitBase := t.TempDir()
	repoDir := filepath.Join(gitBase, "github.com", f.owner, f.repo)
	for name, content := range files {
		path := filepath.Join(repoDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o777))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	if f.prepare != nil {
		f.prepare(t, repoDir)
	}
	gitFixtureCommit(t, repoDir, nil)
	ghURL := serveGitFixtures(t, gitBase)

	// The official distribution serves the requirement package whose
	// tarball delivers the host's real toolchain into <dataDir>/bin.
	pkgs := idepkgtest.MakePackages(release.Package{Name: f.requirement, Latest: "1"})
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{{Package: f.requirement, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(f.requirement, toolWrapperTarball(t, f.toolBinaryName(), f.toolPath))

	wsDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	configPath := filepath.Join(t.TempDir(), "rune.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("editor:\n  mode: modal\n"), 0o644))

	// Use the pkg-install harness: a synchronous scheduler and a real
	// extension runner, mirroring TestPkgInstallStartsExtensionWithPackageEnv.
	// A github install's config merge starts the extension live via
	// afterPackageConfigMerge → startInstalledExtensions, which locks the
	// handler mutex itself, so HandleCommand must not be called under it.
	m := newPkgInstallExtHandler(t, configPath, rm, nopExtensions{}, nil, ghURL)
	defer m.Close()
	dataDir := m.sixDir

	// The workspace runner resolves the requirement toolchain under
	// <r.dataDir>/bin (NewRunner's dataDir, not the per-workspace arg),
	// which must match the package manager's data dir. Build it once the
	// handler's sixDir is known and install it before opening a workspace.
	runner, err := extensionv2.NewRunner(context.Background(), m.mu, dataDir)
	require.NoError(t, err)
	m.extensionRunner = runner

	// User extensions never start on the home workspace; open a real one.
	wsURI, err := workspaceapi.CurrentUserHostURI(wsDir)
	require.NoError(t, err)
	require.NoError(t, m.addWorkspace(wsURI, true, false, -1))
	m.quiesce()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", f.pkgID()},
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	// The requirement's toolchain wrapper must be live in bin/.
	_, err = os.Stat(filepath.Join(dataDir, "bin", f.toolBinaryName()))
	require.NoError(t, err, "requirement install must deliver %s into bin/",
		f.toolBinaryName())
	_, reqOK := pkgVersionInUse(t, m.pkgmanager.pkg, f.requirement)
	require.True(t, reqOK, "requirement package %s must be installed", f.requirement)

	version, ok := pkgVersionInUse(t, m.pkgmanager.pkg, f.pkgID())
	require.True(t, ok, "github package must be installed")
	require.Len(t, string(version), 12, "version must be the short commit sha")

	// The extension starts live off the config merge; the sentinel
	// appears only after clone → requirements → toolchain run → SDK
	// handshake → live API call all succeeded.
	ctx := context.Background()
	require.Eventually(t, func() bool {
		return f.sentinel(ctx, dataDir, wsDir)
	}, f.waitFor, time.Second,
		"%s extension must connect through the SDK and write its sentinel", f.lang)
}

// toolWrapperTarball builds a requirement-package tarball delivering a
// single executable wrapper that execs the host's real toolchain
// binary. Installing it exercises the same requirements chain the
// official language packages use: the executable is promoted into
// <dataDir>/bin, where the extension runner resolves it.
func toolWrapperTarball(t *testing.T, name, hostBinary string) []byte {
	t.Helper()
	script := "#!/bin/sh\nexec " + shellQuote(hostBinary) + " \"$@\"\n"
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "bin/" + name,
		Mode: 0o755,
		Size: int64(len(script)),
	}))
	_, err := tw.Write([]byte(script))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

func lookPathOrSkip(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil && name == "cargo" {
		home, herr := os.UserHomeDir()
		if herr == nil {
			fallback := filepath.Join(home, ".cargo", "bin", "cargo")
			if _, serr := os.Stat(fallback); serr == nil {
				return fallback
			}
		}
	}
	if err != nil {
		t.Skipf("%s not available on host", name)
	}
	return path
}

// goSDKModule resolves the rune-go-sdk version and directory pinned by
// this repository, so the fixture's module replace needs no network.
func goSDKModule(t *testing.T) (version, dir string) {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}} {{.Dir}}",
		"github.com/unstablebuild/rune-go-sdk").Output()
	require.NoError(t, err, "resolve rune-go-sdk module")
	fields := strings.SplitN(strings.TrimSpace(string(out)), " ", 2)
	require.Len(t, fields, 2, "go list output: %q", out)
	return fields[0], fields[1]
}

// sdkCheckoutOrSkip resolves a language SDK checkout from the env
// override or the conventional sibling directory of this repository.
func sdkCheckoutOrSkip(t *testing.T, env, sibling string) string {
	t.Helper()
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	// The test process cwd is <repo>/internal/ide.
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", sibling))
	require.NoError(t, err)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("%s checkout not found at %s (set %s)", sibling, dir, env)
	}
	return dir
}

const ghGoExtensionMain = `package main

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
)

func main() {
	meta := extensionapi.Metadata{
		DeveloperID:      "rune-e2e",
		DeveloperEmail:   "e2e@example.com",
		DeveloperKey:     "rune-e2e-go",
		ExtensionID:      "gh-go-demo",
		ExtensionName:    "gh go demo",
		ExtensionVersion: "0.0.1",
		Permissions:      extensionapi.NewPermissions(extensionapi.PermissionStorage),
	}
	ext := extensionapi.FuncWorkspaceExtension(func(
		ctx context.Context, w *extensionapi.Workspace, _ config.Config,
	) error {
		err := w.Storage(ctx).Set(ctx, "sentinel", map[string]any{"lang": "go"})
		if err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	if err := extensionapi.ServeWorkspaceExtension(ext, meta); err != nil {
		panic(err)
	}
}
`

const ghPythonExtensionMain = `import asyncio
from typing import Any

from rune_sdk.extension import (
    Metadata,
    Permission,
    Workspace,
    serve_workspace_extension,
)

META = Metadata(
    developer_id="rune-e2e",
    developer_email="e2e@example.com",
    developer_key="rune-e2e-python",
    extension_id="gh-python-demo",
    extension_name="gh python demo",
    extension_version="0.0.1",
    permissions=frozenset({Permission.STORAGE}),
)


async def extend(workspace: Workspace, _config: dict[str, Any]) -> None:
    await workspace.storage().set("sentinel", {"lang": "python"})
    while True:
        await asyncio.sleep(3600)


if __name__ == "__main__":
    serve_workspace_extension(extend, META)
`

const ghRustExtensionMain = `use rune_api::workspace;
use rune_extension::{
    BoxError, Metadata, PERMISSION_FILE_SYSTEM, Workspace, new_permissions,
    serve_workspace_extension,
};

#[tokio::main]
async fn main() {
    let meta = Metadata {
        developer_id: "rune-e2e".to_string(),
        developer_email: "e2e@example.com".to_string(),
        developer_key: "rune-e2e-rust".to_string(),
        extension_id: "gh-rust-demo".to_string(),
        extension_name: "gh rust demo".to_string(),
        extension_version: "0.0.1".to_string(),
        permissions: new_permissions([PERMISSION_FILE_SYSTEM]),
    };
    if let Err(err) = serve_workspace_extension(extend, meta).await {
        eprintln!("gh-rust-demo: {err}");
        std::process::exit(1);
    }
}

async fn extend(
    w: Workspace,
    _cfg: serde_json::Map<String, serde_json::Value>,
) -> Result<(), BoxError> {
    let mut fs = workspace::Client::new(w.conn());
    let mut file = fs.create("gh-rust-demo-sentinel").await?;
    file.write(b"rust").await?;
    file.close().await?;
    std::future::pending::<()>().await;
    Ok(())
}
`
