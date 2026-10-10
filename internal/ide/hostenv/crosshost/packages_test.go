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

package crosshost

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/workspace"
)

const (
	// toolPkg is a git package that puts its bin directory on PATH and
	// names its own directory in gui.env.
	toolPkg = gitHost + "/e2e/tool"
	// toolLib is where toolPkg is installed, relative to a data directory.
	toolLib = "lib/" + toolPkg
	// installTimeout bounds an install, which clones over the container's
	// loopback.
	installTimeout = 2 * time.Minute
)

func TestRemotePackageInstallKeepsDataDirLiteral(t *testing.T) {
	local := localDataDir(t)
	h := startHost(t)
	h.serveToolPackage(t)
	const dataDir = remoteHome + "/.rune"
	s := h.connect(t, h.workspaceDir(t, "ws"))
	pm, ui := hostPackages(t, s)

	installTool(t, pm)

	assert.Empty(t, ui.asked(), "a package adding new config asks nothing")
	env := h.guiEnv(t, dataDir)
	assert.Equal(t, "$RUNE_DATADIR/"+toolLib+"/bin:$PATH", env["PATH"],
		"the host keeps $RUNE_DATADIR literal in its config")
	assert.Equal(t, "$RUNE_DATADIR/"+toolLib, env["E2E_TOOL_HOME"])
	cfg, _ := h.readFile(t, dataDir+"/config.yaml")
	assert.NotContains(t, cfg, local)

	want := dataDir + "/" + toolLib
	assert.Equal(t, "tool<"+want+">\n", run(t, s, workspaceapi.Cmd{Path: "e2e-tool"}),
		"the host applies the package's gui.env to the commands it starts next")
	assert.Equal(t, want, marked(t, terminal(t, s, "/bin/bash", []string{"-l"}, "e2e-tool; exit\n"), "tool"),
		"and to the terminals it starts next")
}

func TestRemotePackageInstallRespellsExpandedConfigWithoutAsking(t *testing.T) {
	localDataDir(t)
	h := startHost(t)
	h.serveToolPackage(t)
	const dataDir = remoteHome + "/.rune"
	// What releases that expanded $RUNE_DATADIR at install time wrote.
	h.writeFile(t, dataDir+"/config.yaml", fmt.Sprintf(`gui:
  env:
    PATH: %[1]s/%[2]s/bin:/opt/user/bin:$PATH
    E2E_TOOL_HOME: %[1]s/%[2]s
`, dataDir, toolLib), 0o644)
	s := h.connect(t, h.workspaceDir(t, "ws"))
	pm, ui := hostPackages(t, s)

	installTool(t, pm)

	assert.Empty(t, ui.asked(),
		"a value naming the host's own data directory is respelled without asking")
	env := h.guiEnv(t, dataDir)
	assert.Equal(t, "$RUNE_DATADIR/"+toolLib+"/bin:/opt/user/bin:$PATH", env["PATH"],
		"the expanded entry is respelled in place and the user's entries are kept")
	assert.Equal(t, "$RUNE_DATADIR/"+toolLib, env["E2E_TOOL_HOME"])
	assert.Equal(t, "tool<"+dataDir+"/"+toolLib+">\n", run(t, s, workspaceapi.Cmd{Path: "e2e-tool"}))
}

func TestRemotePackageInstallAsksBeforeReplacingAnotherHostsPath(t *testing.T) {
	local := localDataDir(t)
	h := startHost(t)
	h.serveToolPackage(t)
	const dataDir = remoteHome + "/.rune"
	// Spelled with the client's data directory, which names nothing on this
	// host, so it is a different value rather than another spelling.
	original := fmt.Sprintf(`gui:
  env:
    PATH: %[1]s/%[2]s/bin:$PATH
    E2E_TOOL_HOME: %[1]s/%[2]s
`, local, toolLib)
	h.writeFile(t, dataDir+"/config.yaml", original, 0o644)
	s := h.connect(t, h.workspaceDir(t, "ws"))
	pm, ui := hostPackages(t, s)

	installTool(t, pm)

	assert.NotEmpty(t, ui.asked(), "replacing a different value needs the user's approval")
	env := h.guiEnv(t, dataDir)
	assert.Equal(t, local+"/"+toolLib+"/bin:$PATH", env["PATH"], "a denied change is not applied")
	assert.Equal(t, local+"/"+toolLib, env["E2E_TOOL_HOME"])
}

func installTool(t *testing.T, pm idepkg.PackageManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	version, err := pm.LatestVersion(ctx, toolPkg)
	require.NoError(t, err, "resolve the latest version of %s on the host", toolPkg)
	require.NoError(t, pm.InstallPackageVersion(ctx, toolPkg, version, nil),
		"install %s %s on the host", toolPkg, version)
}

// guiEnv returns the gui.env block of the user config in dataDir on h.
func (h *remoteHost) guiEnv(t *testing.T, dataDir string) map[string]any {
	t.Helper()
	content, ok := h.readFile(t, dataDir+"/config.yaml")
	require.True(t, ok, "%s/config.yaml exists", dataDir)
	var cfg struct {
		GUI struct {
			Env map[string]any `yaml:"env"`
		} `yaml:"gui"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(content), &cfg), "parse config:\n%s", content)
	return cfg.GUI.Env
}

// hostPackages returns the package manager of the host s runs on, as the
// editor reaches it, and the UI that sees what it asks. The UI answers every
// prompt with approve.
func hostPackages(t *testing.T, s schemeapi.Scheme) (idepkg.PackageManager, *packageUI) {
	t.Helper()
	cc, ok := s.(workspace.PackageHost).HostConn()
	require.True(t, ok, "an ssh workspace reaches its host's package manager")
	ui := &packageUI{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("package prompts: %q\npackage notifications: %q", ui.asked(), ui.notifications())
		}
	})
	return pkgrpc.NewClient(cc, ui), ui
}

// packageUI denies every config change the host asks about and records the
// questions and notifications. It never touches t: the client may deliver
// them from goroutines that outlive the test.
type packageUI struct {
	mu      sync.Mutex
	prompts []string
	notes   []string
}

var _ idepkg.UI = (*packageUI)(nil)

func (u *packageUI) Notify(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.notes = append(u.notes, fmt.Sprintf("%d: %s", level, fmt.Sprintf(msg, args...)))
	return strconv.Itoa(len(u.notes)), nil
}

func (u *packageUI) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return u.Notify(level, msg, args...)
}

func (u *packageUI) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func (u *packageUI) PromptConfig(p idepkg.ConfigPrompt, answer func(bool)) {
	u.mu.Lock()
	u.prompts = append(u.prompts, p.Message)
	u.mu.Unlock()
	answer(false)
}

func (u *packageUI) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.prompts...)
}

func (u *packageUI) notifications() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.notes...)
}

// toolRepoScript publishes toolPkg as a bare repository under /srv/git. Its
// config.yaml spells the data directory the way package authors do.
const toolRepoScript = `set -eu
src=$(mktemp -d)
cd "$src"
git init -q -b main
mkdir bin
printf '%s\n' 'gui:' '  env:' \
	'    PATH: $RUNE_DATADIR/lib/$RUNE_PKG_ID/bin:$PATH' \
	'    E2E_TOOL_HOME: $RUNE_DATADIR/lib/$RUNE_PKG_ID' > config.yaml
printf '%s\n' '#!/bin/sh' 'printf "tool<%s>\n" "$E2E_TOOL_HOME"' > bin/e2e-tool
chmod 755 bin/e2e-tool
git add -A
git -c user.name=crosshost -c user.email=crosshost@rune.test commit -q -m fixture
mkdir -p /srv/git/e2e
git clone -q --bare "$src" /srv/git/e2e/tool
`

// serveToolPackage publishes toolPkg on h over HTTPS at gitHost, with a
// certificate the host trusts, and returns once the server accepts
// connections.
func (h *remoteHost) serveToolPackage(t *testing.T) {
	t.Helper()
	caPEM, certPEM, keyPEM := newTLSChain(t, gitHost)
	h.writeFileAs(t, "root", "/srv/tls/cert.pem", string(certPEM), 0o644)
	h.writeFileAs(t, "root", "/srv/tls/key.pem", string(keyPEM), 0o600)
	h.writeFileAs(t, "root", "/srv/tls/ca.pem", string(caPEM), 0o644)
	h.execRoot(t, "cat /srv/tls/ca.pem >> /etc/ssl/certs/ca-certificates.crt")
	h.execRoot(t, toolRepoScript)

	bin := helperBinary(t)
	if out, err := exec.Command("docker", "cp", bin, h.id+":/usr/local/bin/crosshostd").CombinedOutput(); err != nil {
		t.Fatalf("copy crosshostd into the container: %v: %s", err, out)
	}
	cmd := exec.Command("docker", "exec", "-u", "root", h.id, "/usr/local/bin/crosshostd",
		"-cert", "/srv/tls/cert.pem", "-key", "/srv/tls/key.pem")
	stdout := &lineWatch{line: "crosshostd: ready", seen: make(chan struct{})}
	var stderr syncBuffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	// The docker client's output pipes can outlive it, so Wait must not
	// block on them once it is gone.
	cmd.WaitDelay = 5 * time.Second
	require.NoError(t, cmd.Start(), "start crosshostd")
	exited := make(chan struct{})
	go debug.CapturePanicReport(func() {
		_ = cmd.Wait()
		close(exited)
	})
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(readyTimeout):
			t.Errorf("the crosshostd client did not exit within %s of being killed", readyTimeout)
		}
		if t.Failed() {
			t.Logf("crosshostd stderr:\n%s", stderr.String())
		}
	})
	select {
	case <-stdout.seen:
	case <-exited:
		t.Fatalf("crosshostd exited before serving:\n%s", stderr.String())
	case <-time.After(readyTimeout):
		t.Fatalf("crosshostd did not serve within %s:\n%s", readyTimeout, stderr.String())
	}
}

// lineWatch closes seen once line has been written to it as a whole line.
type lineWatch struct {
	line string
	seen chan struct{}

	mu   sync.Mutex
	buf  []byte
	once sync.Once
}

func (w *lineWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if bytes.Contains(w.buf, []byte(w.line+"\n")) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

var (
	helperOnce sync.Once
	helperPath string
	helperErr  error
)

// helperBinary builds crosshostd for the container once per test binary.
// It needs no cgo, so it cross-compiles in seconds.
func helperBinary(t *testing.T) string {
	t.Helper()
	helperOnce.Do(func() {
		root, err := repoRoot()
		if err != nil {
			helperErr = err
			return
		}
		dir, err := os.MkdirTemp("", "crosshostd")
		if err != nil {
			helperErr = err
			return
		}
		helperPath = filepath.Join(dir, "crosshostd")
		cmd := exec.Command("go", "build", "-o", helperPath,
			"./internal/ide/hostenv/crosshost/cmd/crosshostd")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+containerArch(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			helperErr = fmt.Errorf("go build crosshostd: %w: %s", err, out)
		}
	})
	if helperErr != nil {
		t.Fatal(helperErr)
	}
	return helperPath
}

// newTLSChain returns a CA certificate and a server certificate and key for
// host that it signed, all PEM encoded.
func newTLSChain(t *testing.T, host string) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "crosshost e2e CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
