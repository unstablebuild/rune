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

package gitpkg

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"unstable.build/rune/internal/ide/vctrl/testgit"
)

const (
	privateUser     = "rune"
	privatePassword = "secret"
	privateToken    = "ghp_fixture"
)

// Clients a gitHost tells apart by User-Agent.
const (
	clientCLI   = "git"
	clientGoGit = "go-git"
)

// gitHost is a stub HTTPS git host serving every repository under a
// base directory through `git http-backend`. It records which client
// made each request, and both the git CLI and go-git trust its
// certificate until the test ends.
type gitHost struct {
	url     string
	mu      sync.Mutex
	clients []string
}

// newGitHost starts a gitHost for base. wrap, when non-nil, decorates
// the git backend to stage auth, redirects or failures; requests are
// recorded before it runs.
func newGitHost(t *testing.T, base string, wrap func(http.Handler) http.Handler) *gitHost {
	t.Helper()
	h := &gitHost{}
	backend := testgit.HTTPBackend(t, base)
	if wrap != nil {
		backend = wrap(backend)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.record(r.UserAgent())
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	}), 0o644))
	t.Setenv("GIT_SSL_CAINFO", caFile)
	prev, err := transport.Get("https")
	require.NoError(t, err)
	transport.Register("https", githttp.NewTransport(
		&githttp.TransportOptions{Client: srv.Client()}))
	t.Cleanup(func() { transport.Register("https", prev) })
	return h
}

// manager returns a release.Manager resolving git IDs against h.
func (h *gitHost) manager() release.Manager {
	return New(WithRemoteURL(func(pkgID string) string {
		return h.url + "/" + pkgID
	}))
}

func (h *gitHost) record(userAgent string) {
	client := userAgent
	switch {
	case strings.HasPrefix(userAgent, "go-git/"):
		client = clientGoGit
	case strings.HasPrefix(userAgent, "git/"):
		client = clientCLI
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Contains(h.clients, client) {
		h.clients = append(h.clients, client)
	}
}

// takeClients returns the distinct clients that made requests since
// the previous call, in order of their first request.
func (h *gitHost) takeClients() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients := h.clients
	h.clients = nil
	return clients
}

// requireAuth admits requests carrying the private fixture's basic
// credentials or bearer token, like a private GitHub repository.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		basic := ok && user == privateUser && password == privatePassword
		if !basic && r.Header.Get("Authorization") != "Bearer "+privateToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func respondStatus(code int) func(http.Handler) http.Handler {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, http.StatusText(code), code)
		})
	}
}

func rejectUserAgent(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.UserAgent(), prefix) {
				http.Error(w, "client not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type failingHTTPSRoundTripper struct{}

func (failingHTTPSRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.ErrAuthenticationRequired
}

func failGoGitHTTPS(t *testing.T) {
	t.Helper()
	prev, err := transport.Get("https")
	require.NoError(t, err)
	transport.Register("https", githttp.NewTransport(&githttp.TransportOptions{
		Client: &http.Client{Transport: failingHTTPSRoundTripper{}},
	}))
	t.Cleanup(func() { transport.Register("https", prev) })
}

func forGitClients(t *testing.T, run func(*testing.T, func(http.Handler) http.Handler, []string)) {
	t.Helper()
	for _, fallback := range []bool{false, true} {
		name := "go-git"
		var wrap func(http.Handler) http.Handler
		clients := []string{clientGoGit}
		if fallback {
			name = "CLI fallback"
			wrap = rejectUserAgent("go-git/")
			clients = append(clients, clientCLI)
		}
		t.Run(name, func(t *testing.T) { run(t, wrap, clients) })
	}
}

// redirectRepo permanently redirects requests under the from path to
// the same path under to, as GitHub does for renamed repositories.
func redirectRepo(from, to string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rest, ok := strings.CutPrefix(r.URL.Path, from)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			target := to + rest
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
	}
}

// isolateGitConfig points git at an empty home, returned, whose global
// config holds only extra, and disables the system config, so host
// settings (credential helpers, URL rewrites) neither leak in nor get
// written.
func isolateGitConfig(t *testing.T, extra string) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte(extra), 0o644))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	return home
}

// credentialHelperConfig returns a global git config whose credential
// helper answers with the private fixture's user and password.
func credentialHelperConfig(t *testing.T, password string) string {
	t.Helper()
	helper := writeScript(t, "credential-helper", "#!/bin/sh\n"+
		"test \"$1\" = get || exit 0\n"+
		"echo username="+privateUser+"\n"+
		"echo password="+password+"\n")
	return "[credential]\n\thelper = " + helper + "\n"
}

// writeScript writes an executable script named name into a fresh
// directory and returns its path.
func writeScript(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// prependPath puts the directory holding bin first on PATH.
func prependPath(t *testing.T, bin string) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func requireGitCLI(t *testing.T) string {
	t.Helper()
	gitBin, ok := lookGit()
	if !ok {
		t.Skip("git not available on PATH")
	}
	return gitBin
}

func fixtureHash(t *testing.T, sha string) plumbing.Hash {
	t.Helper()
	h, ok := plumbing.FromHex(sha)
	require.True(t, ok, "invalid sha %q", sha)
	return h
}

func setFixtureRef(t *testing.T, dir string, ref *plumbing.Reference) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(ref))
}

// annotatedTagFixtureRepo creates an annotated tag at the fixture
// repo's HEAD commit.
func annotatedTagFixtureRepo(t *testing.T, dir, tag string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	_, err = repo.CreateTag(tag, head.Hash(), &git.CreateTagOptions{
		Message: tag,
		Tagger: &object.Signature{
			Name: "fixture", Email: "fixture@example.com", When: time.Now(),
		},
	})
	require.NoError(t, err)
}

// versionedFixture is a repository with a lightweight tag v1.0.0 on its
// first commit, an annotated tag v2.0.0 on its second, and a third
// commit at HEAD, each with distinct main.py content.
type versionedFixture struct {
	first, second, head string
}

func newVersionedFixture(t *testing.T, base, pkgID string) versionedFixture {
	t.Helper()
	dir := filepath.Join(base, filepath.FromSlash(pkgID))
	first := initFixtureRepo(t, dir, map[string]fixtureFile{
		"config.yaml": {content: validConfigYAML},
		"main.py":     {content: "print('first')\n"},
	})
	tagFixtureRepo(t, dir, "v1.0.0")
	second := commitFixtureRepo(t, dir, map[string]fixtureFile{
		"main.py": {content: "print('second')\n"},
	})
	annotatedTagFixtureRepo(t, dir, "v2.0.0")
	head := commitFixtureRepo(t, dir, map[string]fixtureFile{
		"main.py": {content: "print('head')\n"},
	})
	return versionedFixture{first: first, second: second, head: head}
}

// fixtureInstall is an install request with the version identity and
// main.py content it must produce.
type fixtureInstall struct {
	name    string
	version release.Version
	stored  release.Version
	mainPy  string
}

func (f versionedFixture) latest() fixtureInstall {
	short := release.Version(f.head[:shortSHALen])
	return fixtureInstall{"latest", release.Latest, short, "print('head')\n"}
}

// installs covers every kind of version a git package resolves.
func (f versionedFixture) installs() []fixtureInstall {
	second := release.Version(f.second[:shortSHALen])
	return []fixtureInstall{
		f.latest(),
		{"lightweight tag", "v1.0.0", "v1.0.0", "print('first')\n"},
		{"annotated tag", "v2.0.0", "v2.0.0", "print('second')\n"},
		{"short commit", second, second, "print('second')\n"},
		{"full commit", release.Version(f.first), release.Version(f.first), "print('first')\n"},
	}
}

// requireInstall installs in.version of pkgID and returns the tarball
// after checking its version identity, commit time, main.py content and
// that no git metadata leaked into it.
func requireInstall(
	t *testing.T, m release.Manager, pkgID string, in fixtureInstall,
) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pw := &bufferProgressWriter{}
	bundle, err := m.Get(ctx, pkgID, in.version, pw)
	require.NoError(t, err, in.name)
	assert.Equal(t, in.stored, bundle.Version, in.name)
	assert.False(t, bundle.CreatedAt.IsZero(), "%s: commit time must be read", in.name)
	assert.Equal(t, in.mainPy, entryContent(t, pw.Bytes(), "main.py"), in.name)
	for name := range untarAll(t, pw.Bytes()) {
		assert.NotContains(t, name, ".git/", in.name)
	}
	return pw.Bytes()
}

func listVersions(t *testing.T, m release.Manager, pkgID string) []release.Version {
	t.Helper()
	ctx := context.Background()
	it, err := m.List(ctx, pkgID, nil)
	require.NoError(t, err)
	bundles, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	versions := make([]release.Version, len(bundles))
	for i, b := range bundles {
		versions[i] = b.Version
	}
	return versions
}

func TestGitClientsInstallPublicRepository(t *testing.T) {
	requireGitCLI(t)
	forGitClients(t, func(t *testing.T, wrap func(http.Handler) http.Handler, clients []string) {
		const pkgID = "github.com/owner/repo"
		base := t.TempDir()
		fixture := newVersionedFixture(t, base, pkgID)
		isolateGitConfig(t, "")
		host := newGitHost(t, base, wrap)
		m := host.manager()

		t.Run("lists HEAD and tags", func(t *testing.T) {
			pkg, err := m.GetPackage(context.Background(), pkgID)
			require.NoError(t, err)
			assert.Equal(t, fixture.latest().stored, pkg.Latest)
			versions := listVersions(t, m, pkgID)
			assert.Equal(t, release.Latest, versions[0])
			assert.ElementsMatch(t,
				[]release.Version{release.Latest, "v1.0.0", "v2.0.0"}, versions,
				"peeled annotated-tag entries must not be listed as tags")
			assert.Equal(t, clients, host.takeClients())
		})
		for _, in := range fixture.installs() {
			t.Run("installs "+in.name, func(t *testing.T) {
				requireInstall(t, m, pkgID, in)
				assert.Equal(t, clients, host.takeClients())
			})
		}
	})
}

func TestGitClientsInstallRepositoryShapes(t *testing.T) {
	requireGitCLI(t)
	forGitClients(t, func(t *testing.T, wrap func(http.Handler) http.Handler, clients []string) {
		base := t.TempDir()
		isolateGitConfig(t, "")
		host := newGitHost(t, base, func(next http.Handler) http.Handler {
			next = redirectRepo("/github.com/owner/old/", "/github.com/owner/renamed/")(next)
			if wrap != nil {
				next = wrap(next)
			}
			return next
		})
		m := host.manager()

		t.Run("default branch other than master", func(t *testing.T) {
			dir := filepath.Join(base, "github.com", "owner", "trunk")
			master := initFixtureRepo(t, dir, map[string]fixtureFile{
				"config.yaml": {content: validConfigYAML},
				"main.py":     {content: "print('master')\n"},
			})
			setFixtureRef(t, dir, plumbing.NewHashReference(
				plumbing.NewBranchReferenceName("trunk"), fixtureHash(t, master)))
			setFixtureRef(t, dir, plumbing.NewSymbolicReference(
				plumbing.HEAD, plumbing.NewBranchReferenceName("trunk")))
			trunk := commitFixtureRepo(t, dir, map[string]fixtureFile{
				"main.py": {content: "print('trunk')\n"},
			})

			requireInstall(t, m, "github.com/owner/trunk", fixtureInstall{
				"latest", release.Latest, release.Version(trunk[:shortSHALen]),
				"print('trunk')\n",
			})
			assert.Equal(t, clients, host.takeClients())
		})
		t.Run("tag shadowed by a same-named branch", func(t *testing.T) {
			dir := filepath.Join(base, "github.com", "owner", "shadowed")
			initFixtureRepo(t, dir, map[string]fixtureFile{
				"config.yaml": {content: validConfigYAML},
				"main.py":     {content: "print('tag')\n"},
			})
			tagFixtureRepo(t, dir, "v1")
			branch := commitFixtureRepo(t, dir, map[string]fixtureFile{
				"main.py": {content: "print('branch')\n"},
			})
			setFixtureRef(t, dir, plumbing.NewHashReference(
				plumbing.NewBranchReferenceName("v1"), fixtureHash(t, branch)))
			commitFixtureRepo(t, dir, map[string]fixtureFile{
				"main.py": {content: "print('head')\n"},
			})

			requireInstall(t, m, "github.com/owner/shadowed",
				fixtureInstall{"tag", "v1", "v1", "print('tag')\n"})
			assert.Equal(t, clients, host.takeClients())
		})
		t.Run("executable bits and symlinks", func(t *testing.T) {
			dir := filepath.Join(base, "github.com", "owner", "files")
			require.NoError(t, os.MkdirAll(dir, 0o777))
			require.NoError(t, os.Symlink("main.py", filepath.Join(dir, "link.py")))
			sha := initFixtureRepo(t, dir, map[string]fixtureFile{
				"config.yaml":    {content: validConfigYAML},
				"main.py":        {content: "print('files')\n"},
				"scripts/run.sh": {content: "#!/bin/sh\necho hi\n", mode: 0o755},
			})

			tarball := requireInstall(t, m, "github.com/owner/files", fixtureInstall{
				"latest", release.Latest, release.Version(sha[:shortSHALen]),
				"print('files')\n",
			})
			entries := untarAll(t, tarball)
			require.Contains(t, entries, "scripts/run.sh")
			assert.NotZero(t, entries["scripts/run.sh"].FileInfo().Mode()&0o111,
				"exec bit must be preserved")
			assert.Zero(t, entries["main.py"].FileInfo().Mode()&0o111)
			require.Contains(t, entries, "link.py")
			assert.Equal(t, byte(tar.TypeSymlink), entries["link.py"].Typeflag)
			assert.Equal(t, "main.py", entries["link.py"].Linkname)
			assert.Equal(t, clients, host.takeClients())
		})
		t.Run("renamed repository redirect", func(t *testing.T) {
			fixture := newVersionedFixture(t, base, "github.com/owner/renamed")

			requireInstall(t, m, "github.com/owner/old", fixture.latest())
			requireInstall(t, m, "github.com/owner/old", fixture.installs()[2])
			assert.Equal(t, clients, host.takeClients())
		})
	})
}

func TestGitCLIInstallsPrivateRepository(t *testing.T) {
	requireGitCLI(t)
	const pkgID = "github.com/owner/private"
	base := t.TempDir()
	fixture := newVersionedFixture(t, base, pkgID)
	host := newGitHost(t, base, requireAuth)
	m := host.manager()

	credentials := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"credential helper", func(t *testing.T) {
			isolateGitConfig(t, credentialHelperConfig(t, privatePassword))
		}},
		{"netrc", func(t *testing.T) {
			home := isolateGitConfig(t, "")
			require.NoError(t, os.WriteFile(filepath.Join(home, ".netrc"),
				[]byte("machine 127.0.0.1 login "+privateUser+
					" password "+privatePassword+"\n"), 0o600))
		}},
		{"token in http.extraHeader", func(t *testing.T) {
			isolateGitConfig(t,
				"[http]\n\textraHeader = Authorization: Bearer "+privateToken+"\n")
		}},
	}
	for _, c := range credentials {
		t.Run(c.name+" authenticates", func(t *testing.T) {
			c.setup(t)

			pkg, err := m.GetPackage(context.Background(), pkgID)
			require.NoError(t, err)
			assert.Equal(t, fixture.latest().stored, pkg.Latest)
			for _, in := range fixture.installs() {
				requireInstall(t, m, pkgID, in)
			}
			assert.Equal(t, []string{clientGoGit, clientCLI}, host.takeClients())
		})
	}

	failures := []struct {
		name    string
		setup   func(t *testing.T)
		cliErr  string
		clients []string
	}{
		{
			name:    "missing credentials fail instead of prompting",
			setup:   func(t *testing.T) { isolateGitConfig(t, "") },
			cliErr:  "terminal prompts disabled",
			clients: []string{clientGoGit, clientCLI},
		},
		{
			name: "wrong credentials",
			setup: func(t *testing.T) {
				isolateGitConfig(t, credentialHelperConfig(t, "wrong"))
			},
			cliErr:  "Authentication failed",
			clients: []string{clientGoGit, clientCLI},
		},
		{
			name: "go-git without the CLI cannot use credential helpers",
			setup: func(t *testing.T) {
				isolateGitConfig(t, credentialHelperConfig(t, privatePassword))
				t.Setenv("PATH", "")
			},
			clients: []string{clientGoGit},
		},
	}
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			f.setup(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err := m.Get(ctx, pkgID, release.Latest, &bufferProgressWriter{})
			require.ErrorIs(t, err, transport.ErrAuthenticationRequired)
			if f.cliErr != "" {
				assert.ErrorContains(t, err, f.cliErr)
			}
			assert.Equal(t, f.clients, host.takeClients())
		})
	}
}

func TestGitCLIAppliesUserGitConfig(t *testing.T) {
	gitBin := requireGitCLI(t)
	const pkgID = "github.com/owner/repo"
	base := t.TempDir()
	fixture := newVersionedFixture(t, base, pkgID)

	t.Run("insteadOf rewrites the package URL", func(t *testing.T) {
		host := newGitHost(t, base, nil)
		failGoGitHTTPS(t)
		isolateGitConfig(t, fmt.Sprintf("[url %q]\n\tinsteadOf = https://github.com/\n",
			host.url+"/github.com/"))
		// The production ID-to-URL mapping, rewritten by the user's config.
		m := New()

		for _, in := range fixture.installs() {
			requireInstall(t, m, pkgID, in)
		}
		assert.Equal(t, []string{clientCLI}, host.takeClients())
	})
	t.Run("insteadOf rewrites the package URL to SSH", func(t *testing.T) {
		failGoGitHTTPS(t)
		sshLog := filepath.Join(t.TempDir(), "ssh.log")
		// Serves `git@github.com:<path>` from the fixture base, standing
		// in for the user's ssh and the remote's upload-pack.
		fakeSSH := writeScript(t, "ssh", "#!/bin/sh\n"+
			"echo \"$@\" >> "+shellQuote(sshLog)+"\n"+
			"for last; do :; done\n"+
			"cd "+shellQuote(filepath.Join(base, "github.com"))+
			" && eval \"exec "+shellQuote(gitBin)+" ${last#git-}\"\n")
		isolateGitConfig(t, "[url \"git@github.com:\"]\n\tinsteadOf = https://github.com/\n"+
			"[core]\n\tsshCommand = "+fakeSSH+"\n[ssh]\n\tvariant = simple\n")

		for _, in := range fixture.installs() {
			requireInstall(t, New(), pkgID, in)
		}
		log, err := os.ReadFile(sshLog)
		require.NoError(t, err)
		assert.Contains(t, string(log), "git@github.com git-upload-pack 'owner/repo'")
	})
	t.Run("core.autocrlf does not rewrite installed files", func(t *testing.T) {
		host := newGitHost(t, base, rejectUserAgent("go-git/"))
		isolateGitConfig(t, "[core]\n\tautocrlf = true\n")

		for _, in := range fixture.installs() {
			requireInstall(t, host.manager(), pkgID, in)
		}
		assert.Equal(t, []string{clientGoGit, clientCLI}, host.takeClients())
	})
}

func TestSuccessfulGoGitDoesNotInvokeCLI(t *testing.T) {
	gitBin := requireGitCLI(t)
	const pkgID = "github.com/owner/repo"
	base := t.TempDir()
	fixture := newVersionedFixture(t, base, pkgID)

	cases := []struct {
		name    string
		fakeGit string
		clients []string
	}{
		{
			name:    "working git on PATH",
			fakeGit: "exec " + shellQuote(gitBin) + " \"$@\"\n",
			clients: []string{clientGoGit},
		},
		{
			name:    "git exits with an error",
			fakeGit: "echo 'xcrun: error: invalid active developer path' >&2\nexit 1\n",
			clients: []string{clientGoGit},
		},
		{name: "no git on PATH", clients: []string{clientGoGit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitConfig(t, "")
			host := newGitHost(t, base, nil)
			log := filepath.Join(t.TempDir(), "git.log")
			if tc.name == "no git on PATH" {
				t.Setenv("PATH", "")
			} else {
				prependPath(t, writeScript(t, "git", "#!/bin/sh\necho invoked >> "+shellQuote(log)+"\n"+tc.fakeGit))
			}
			m := host.manager()
			assert.Len(t, listVersions(t, m, pkgID), 3)

			for _, in := range fixture.installs() {
				requireInstall(t, m, pkgID, in)
				assert.Equal(t, tc.clients, host.takeClients(), in.name)
			}
			_, err := os.Stat(log)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestGoGitOperationsFallBackIndependently(t *testing.T) {
	gitBin := requireGitCLI(t)
	const pkgID = "github.com/owner/repo"
	for _, operation := range []string{"refs", "clone"} {
		t.Run(operation, func(t *testing.T) {
			base := t.TempDir()
			fixture := newVersionedFixture(t, base, pkgID)
			isolateGitConfig(t, "")
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			log := filepath.Join(t.TempDir(), "git.log")
			prependPath(t, writeScript(t, "git", "#!/bin/sh\necho \"$@\" >> "+shellQuote(log)+
				"\nexec "+shellQuote(gitBin)+" \"$@\"\n"))
			partial := make(chan bool, 1)
			var listed atomic.Bool
			host := newGitHost(t, base, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.UserAgent(), "go-git/") {
						if operation == "refs" && !listed.Swap(true) {
							http.Error(w, "reject refs", http.StatusForbidden)
							return
						}
						if operation == "clone" && r.Method == http.MethodPost {
							dirs, err := filepath.Glob(filepath.Join(tmp, "gitpkg-*", ".git"))
							partial <- err == nil && len(dirs) == 1
							http.Error(w, "reject fetch", http.StatusForbidden)
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			requireInstall(t, host.manager(), pkgID, fixture.latest())
			assert.Equal(t, []string{clientGoGit, clientCLI}, host.takeClients())
			commands, err := os.ReadFile(log)
			require.NoError(t, err)
			if operation == "refs" {
				assert.Contains(t, string(commands), "ls-remote")
				assert.NotContains(t, string(commands), "clone")
			} else {
				assert.Contains(t, string(commands), "clone")
				assert.NotContains(t, string(commands), "ls-remote")
				select {
				case exists := <-partial:
					require.True(t, exists, "go-git must leave a partial repository for CLI cleanup")
				default:
					t.Fatal("go-git must attempt fetching before CLI fallback")
				}
			}
			dirs, err := filepath.Glob(filepath.Join(tmp, "gitpkg-*"))
			require.NoError(t, err)
			assert.Empty(t, dirs)
		})
	}
}

func TestGitCLIAndGoGitFailuresAreBothReported(t *testing.T) {
	requireGitCLI(t)
	base := t.TempDir()
	newVersionedFixture(t, base, "github.com/owner/repo")

	cases := []struct {
		name     string
		pkgID    string
		wrap     func(http.Handler) http.Handler
		cliErr   string
		goGitErr string
		fakeGit  string
	}{
		{
			name:     "missing repository",
			pkgID:    "github.com/owner/missing",
			cliErr:   "fatal: repository",
			goGitErr: transport.ErrRepositoryNotFound.Error(),
		},
		{
			name:     "forbidden",
			pkgID:    "github.com/owner/repo",
			wrap:     respondStatus(http.StatusForbidden),
			cliErr:   "returned error: 403",
			goGitErr: transport.ErrAuthorizationFailed.Error(),
		},
		{
			name:     "server error",
			pkgID:    "github.com/owner/repo",
			wrap:     respondStatus(http.StatusInternalServerError),
			cliErr:   "returned error: 500",
			goGitErr: "status code: 500",
		},
		{
			name:  "clone fails with both clients",
			pkgID: "github.com/owner/repo",
			wrap: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					if r.Method == http.MethodPost && !bytes.Contains(body, []byte("ls-refs")) {
						http.Error(w, "reject fetch", http.StatusForbidden)
						return
					}
					next.ServeHTTP(w, r)
				})
			},
			cliErr:   "403",
			goGitErr: transport.ErrAuthorizationFailed.Error(),
		},
		{
			name:     "broken git fallback",
			pkgID:    "github.com/owner/repo",
			wrap:     rejectUserAgent("go-git/"),
			fakeGit:  "#!/bin/sh\necho 'invalid active developer path' >&2\nexit 1\n",
			cliErr:   "invalid active developer path",
			goGitErr: transport.ErrAuthorizationFailed.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitConfig(t, "")
			host := newGitHost(t, base, tc.wrap)
			if tc.fakeGit != "" {
				prependPath(t, writeScript(t, "git", tc.fakeGit))
			}
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			_, err := host.manager().Get(context.Background(), tc.pkgID,
				release.Latest, &bufferProgressWriter{})
			require.Error(t, err)
			goGitPart, cliPart, ok := strings.Cut(err.Error(), "; git CLI fallback: ")
			require.True(t, ok, "error must carry both failures: %v", err)
			assert.Contains(t, cliPart, tc.cliErr)
			assert.Contains(t, goGitPart, tc.goGitErr)
			clients := []string{clientGoGit}
			if tc.fakeGit == "" {
				clients = append(clients, clientCLI)
			}
			assert.Equal(t, clients, host.takeClients())
			dirs, err := filepath.Glob(filepath.Join(tmp, "gitpkg-*"))
			require.NoError(t, err)
			assert.Empty(t, dirs)
		})
	}
}

func TestGitClientsCancellation(t *testing.T) {
	requireGitCLI(t)
	forGitClients(t, func(t *testing.T, wrap func(http.Handler) http.Handler, clients []string) {
		const pkgID = "github.com/owner/repo"
		base := t.TempDir()
		newVersionedFixture(t, base, pkgID)

		cases := []struct {
			name string
			hang func(r *http.Request, body []byte) bool
		}{
			{"while listing refs", func(*http.Request, []byte) bool { return true }},
			{"while cloning", func(r *http.Request, body []byte) bool {
				// Protocol v2 lists refs with an ls-refs POST; every other
				// POST to upload-pack is a fetch.
				return r.Method == http.MethodPost && !bytes.Contains(body, []byte("ls-refs"))
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				isolateGitConfig(t, "")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				disconnected := make(chan struct{}, 16)
				unblock := make(chan struct{})
				host := newGitHost(t, base, func(next http.Handler) http.Handler {
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(body))
						if !tc.hang(r, body) {
							next.ServeHTTP(w, r)
							return
						}
						cancel()
						select {
						case <-r.Context().Done():
							disconnected <- struct{}{}
						case <-unblock:
						}
					})
					if wrap != nil {
						return wrap(handler)
					}
					return handler
				})
				// Registered after the server's Close so it runs first.
				t.Cleanup(func() { close(unblock) })

				_, err := host.manager().Get(ctx, pkgID, release.Latest, &bufferProgressWriter{})
				require.ErrorIs(t, err, context.Canceled)
				if wrap == nil {
					assert.NotContains(t, err.Error(), "git CLI fallback")
				}
				select {
				case <-disconnected:
				case <-time.After(10 * time.Second):
					t.Fatal("git's transport helper must die with git and drop its connection")
				}
				assert.Equal(t, clients, host.takeClients())
			})
		}
	})
}

func requireGitHub(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "github.com:443", 10*time.Second)
	if err != nil {
		t.Skipf("github.com is unreachable: %v", err)
	}
	_ = conn.Close()
}

func TestInstallsFromGitHub(t *testing.T) {
	requireGitHub(t)
	const (
		pkgID   = "github.com/ernestrc/rune-theme-redmond95"
		missing = "github.com/ernestrc/rune-gitpkg-e2e-missing"
	)
	clients := []struct {
		name       string
		setup      func(t *testing.T)
		missingErr string
	}{
		{
			name: "git CLI",
			setup: func(t *testing.T) {
				requireGitCLI(t)
				isolateGitConfig(t, "")
				failGoGitHTTPS(t)
			},
			missingErr: "terminal prompts disabled",
		},
		{
			name: "go-git",
			setup: func(t *testing.T) {
				isolateGitConfig(t, "")
				t.Setenv("PATH", "")
			},
		},
	}
	for _, c := range clients {
		t.Run(c.name+" installs a public theme package", func(t *testing.T) {
			c.setup(t)
			m := New()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			pkg, err := m.GetPackage(ctx, pkgID)
			require.NoError(t, err)
			assert.Regexp(t, `^[0-9a-f]{12}$`, string(pkg.Latest))
			assert.Contains(t, listVersions(t, m, pkgID), release.Latest)

			// latest clones shallow; the commit takes the full history.
			for _, version := range []release.Version{release.Latest, pkg.Latest} {
				pw := &bufferProgressWriter{}
				bundle, err := m.Get(ctx, pkgID, version, pw)
				require.NoError(t, err, version)
				assert.Equal(t, pkg.Latest, bundle.Version, version)
				assert.False(t, bundle.CreatedAt.IsZero(), version)
				entries := untarAll(t, pw.Bytes())
				for _, name := range []string{"config.yaml", "LICENSE", "README.md"} {
					assert.Contains(t, entries, name, version)
				}
				for name := range entries {
					assert.NotContains(t, name, ".git/", version)
				}
				assert.Contains(t, entryContent(t, pw.Bytes(), "config.yaml"),
					"redmond95:", version)
			}
		})
		t.Run(c.name+" fails fast on a missing repository", func(t *testing.T) {
			c.setup(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			// GitHub answers anonymous requests for missing and private
			// repositories alike with 401.
			_, err := New().GetPackage(ctx, missing)
			require.ErrorIs(t, err, transport.ErrAuthenticationRequired)
			if c.missingErr != "" {
				assert.ErrorContains(t, err, c.missingErr)
			}
		})
	}
}
