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

package ideupgrade

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"golang.org/x/mod/semver"
	"unstable.build/rune/internal/debug"
)

// Default tunables for Manager. Exported so callers can derive
// alternate Configs without importing time directly when constructing
// configs from external sources.
const (
	DefaultInitialDelay = 30 * time.Second
	DefaultCheckPeriod  = 24 * time.Hour
	DefaultBackupKeep   = 1

	// DefaultHTTPDialTimeout bounds the TCP/TLS dial of the default
	// HTTPClient. A wedged DNS/SYN can otherwise hang an upgrade
	// check (and any caller that runs it synchronously) for the
	// system-default ~75s.
	DefaultHTTPDialTimeout = 5 * time.Second

	// DefaultHTTPResponseHeaderTimeout bounds the wait between
	// sending the request and receiving the response headers. We do
	// NOT bound full-body reads because artifact downloads on slow
	// links can legitimately exceed any flat timeout.
	DefaultHTTPResponseHeaderTimeout = 15 * time.Second

	storagePartition = "ideupgrade"
	storageKeyState  = "state"
)

// errRestartRequired is returned by Check once this process has
// installed an upgrade, which only takes effect on restart.
var errRestartRequired = errors.New(
	"already upgraded; restart Rune to apply")

// Config is the externally-provided configuration for a Manager.
// All fields except CurrentVersion, ManifestURL and Storage have safe
// defaults applied by New.
type Config struct {
	// CurrentVersion is the version of the running client. When empty,
	// every check is skipped (safety net for development builds where
	// the manifest version always looks "newer").
	CurrentVersion string

	// Arch is the "<os>-<arch>" identifier appended to the manifest URL.
	// Defaults to runtime.GOOS + "-" + runtime.GOARCH.
	Arch string

	// ManifestURL is the base URL the release manifests are published
	// under, without a trailing arch. The Manager appends
	// "/manifest-<arch>.json" to derive the per-arch endpoint.
	ManifestURL string

	// Storage is the persistent storage used to throttle checks and
	// remember user choices. The Manager partitions it under
	// "ideupgrade".
	Storage storageapi.Service

	// HTTPClient overrides the http.Client used for manifest fetches
	// and artifact downloads.
	HTTPClient *http.Client

	// Executable returns the running binary's path. Defaults to
	// os.Executable. Exposed so tests can simulate running from
	// different install layouts.
	Executable func() (string, error)

	// Notifications, when set, announces an available upgrade when
	// there is no Prompter, and reports a failure to start one.
	Notifications browserapi.Notifications

	// Prompter, when set, opens the upgrade prompt. It is called on the
	// event loop that ScheduleNextTick schedules on. Without one, the
	// prompt is dismissed and an available upgrade is only notified.
	Prompter browserapi.Prompter

	// Starter starts the upgrade accepted in the prompt, and is
	// required with Prompter.
	Starter Starter

	// ScheduleNextTick schedules fn to run on the IDE event loop.
	ScheduleNextTick func(func()) bool

	// Now overrides time.Now (for tests).
	Now func() time.Time

	// InitialDelay overrides DefaultInitialDelay (for tests).
	InitialDelay time.Duration

	// CheckPeriod overrides DefaultCheckPeriod (for tests).
	CheckPeriod time.Duration

	// InstallRoot is the directory containing the installed app
	// bundle. Defaults: /Applications on darwin, ~/.local on linux.
	InstallRoot string

	// AppName is the name of the installed app directory inside
	// InstallRoot. Defaults: Rune.app on darwin, rune.app on linux.
	AppName string

	// CLISymlinkPath is the path of the CLI symlink to refresh after
	// a successful upgrade. Defaults to ~/.local/bin/rune.
	CLISymlinkPath string

	// CLIBinaryRelPath is the binary location relative to the app
	// bundle. Defaults: Contents/MacOS/rune on darwin, bin/rune on
	// linux.
	CLIBinaryRelPath string

	// CacheDir is where downloaded artifacts land before
	// installation. Defaults to <user cache>/rune/upgrade.
	CacheDir string

	// BackupRetention is the number of `.bak-*` snapshots to keep
	// after a successful upgrade. Defaults to DefaultBackupKeep.
	BackupRetention int
}

// Starter starts an upgrade the user accepted in the upgrade prompt.
type Starter interface {
	// StartUpgrade is called on the event loop and must not wait for
	// the upgrade to finish.
	StartUpgrade() error
}

// state is the persistent record of what the user has decided about
// the latest available version, plus the throttle timestamp.
type state struct {
	LastCheckedAt   time.Time `json:"last_checked_at"`
	RemindAfter     time.Time `json:"remind_after"`
	SkippedVersions []string  `json:"skipped_versions"`
}

// Manager runs the periodic check loop and orchestrates downloads,
// prompts and upgrades. It is safe for concurrent use; the public
// methods serialize via mu.
type Manager struct {
	cfg Config
	ops platformOps

	upgraded atomic.Bool

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

// New returns a new Manager wired with the production platform
// operations for the current OS. Required Config fields:
// CurrentVersion, ManifestURL, Storage. Other fields have sensible
// defaults applied.
func New(cfg Config) (*Manager, error) {
	// HTTPClient is materialized here (rather than inside
	// newWithPlatformOps) so the production platformOps and the
	// manifest fetcher share the same client.
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}
	// protects against nil notifications and auto-schedules
	// to avoid data races on notifications
	cfg.Notifications = newScheduledNotifications(
		cfg.Notifications, cfg.ScheduleNextTick)
	return newWithPlatformOps(cfg, newPlatformOps(cfg.HTTPClient))
}

// newWithPlatformOps constructs a Manager with the given platformOps.
// It is the seam used by tests inside this package to inject a fake
// implementation; production code goes through New.
func newWithPlatformOps(cfg Config, ops platformOps) (*Manager, error) {
	if ops == nil {
		return nil, errors.New("ideupgrade: platformOps is nil")
	}
	if cfg.Storage == nil {
		return nil, errors.New("ideupgrade: Storage is required")
	}
	if cfg.ManifestURL == "" {
		return nil, errors.New("ideupgrade: ManifestURL is required")
	}
	if err := requireSecureURL(cfg.ManifestURL); err != nil {
		return nil, fmt.Errorf("ideupgrade: ManifestURL: %w", err)
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOOS + "-" + runtime.GOARCH
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.InitialDelay == 0 {
		cfg.InitialDelay = DefaultInitialDelay
	}
	if cfg.CheckPeriod == 0 {
		cfg.CheckPeriod = DefaultCheckPeriod
	}
	if cfg.BackupRetention == 0 {
		cfg.BackupRetention = DefaultBackupKeep
	}
	if cfg.InstallRoot == "" {
		cfg.InstallRoot = defaultInstallRoot()
	}
	if cfg.AppName == "" {
		cfg.AppName = defaultAppName()
	}
	if cfg.CLISymlinkPath == "" {
		cfg.CLISymlinkPath = defaultCLISymlinkPath()
	}
	if cfg.CLIBinaryRelPath == "" {
		cfg.CLIBinaryRelPath = defaultCLIBinaryRelPath()
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = defaultCacheDir()
	}
	if cfg.ScheduleNextTick == nil {
		return nil, fmt.Errorf("ScheduleNextTick is nil")
	}
	if cfg.Prompter != nil && cfg.Starter == nil {
		return nil, errors.New("ideupgrade: Starter is required with Prompter")
	}

	storage, err := cfg.Storage.Partition(storagePartition)
	if err != nil {
		return nil, fmt.Errorf("partition storage: %w", err)
	}
	cfg.Storage = storage

	return &Manager{cfg: cfg, ops: ops}, nil
}

// Start launches the background check loop. Calling Start more than
// once is a no-op.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	ctx, m.cancel = context.WithCancel(ctx)
	m.running = true
	m.mu.Unlock()

	go debug.CapturePanicReport(func() {
		m.run(ctx)
	})
}

// Close cancels the background check loop. Subsequent calls are no-ops.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.running = false
	return nil
}

// CurrentVersion returns the version of the running client.
func (m *Manager) CurrentVersion() string { return m.cfg.CurrentVersion }

// Check performs a manifest fetch outside the regular throttle and
// reports whether the manifest describes an upgrade the user has not
// already skipped or postponed. It fails once this process has
// installed an upgrade, since it is the restart that applies it.
func (m *Manager) Check(ctx context.Context) (Manifest, bool, error) {
	if m.upgraded.Load() {
		return Manifest{}, false, errRestartRequired
	}
	manifest, has, err := m.fetchManifest(ctx)
	if err != nil {
		return Manifest{}, false, err
	}
	m.persistChecked(ctx)
	return manifest, has, nil
}

// run is the goroutine body for Start. It first waits InitialDelay,
// then ticks every CheckPeriod.
func (m *Manager) run(ctx context.Context) {
	timer := time.NewTimer(m.cfg.InitialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	m.tick(ctx)

	ticker := time.NewTicker(m.cfg.CheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	if m.upgraded.Load() || m.isThrottled(ctx) {
		return
	}
	manifest, has, err := m.fetchManifest(ctx)
	if err != nil {
		log.WithError(err).Warn("ideupgrade: fetch manifest")
		return
	}
	m.persistChecked(ctx)
	if !has {
		return
	}
	m.showPrompt(ctx, manifest)
}

// fetchManifest downloads the manifest and returns it along with a
// bool indicating whether it represents an upgrade the user has not
// already skipped or postponed.
func (m *Manager) fetchManifest(ctx context.Context) (Manifest, bool, error) {
	if m.cfg.CurrentVersion == "" {
		return Manifest{}, false, nil
	}

	endpoint, err := manifestEndpoint(m.cfg.ManifestURL, m.cfg.Arch)
	if err != nil {
		return Manifest{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Manifest{}, false, fmt.Errorf("new request: %w", err)
	}
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return Manifest{}, false, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	// GCS public buckets serve missing objects as 403 (not 404)
	// because the anonymous caller doesn't have permission to learn
	// the object exists. Treat both as "no manifest published yet"
	// — the more common case during initial rollout — so the
	// `:upgrade` UI shows "Rune is up to date" instead of a noisy
	// HTTP error.
	if resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusForbidden {
		return Manifest{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, false,
			fmt.Errorf("manifest fetch %s: status %d", endpoint, resp.StatusCode)
	}
	var manifest Manifest
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, false, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.Version == "" || manifest.URL == "" || manifest.SHA256 == "" {
		return Manifest{}, false, errors.New("manifest missing required fields")
	}
	if err := requireSecureURL(manifest.URL); err != nil {
		return Manifest{}, false, fmt.Errorf("manifest artifact url: %w", err)
	}

	// MinSupportedVersion is enforced before the same/older check so
	// a client that is too old to upgrade in place still learns it
	// must take action, even when the manifest version happens to
	// match the current one.
	if min := manifest.MinSupportedVersion; min != "" &&
		compareVersions(m.cfg.CurrentVersion, min) < 0 {
		return Manifest{}, false, fmt.Errorf(
			"installed version %s is below manifest min_supported_version %s; manual upgrade required",
			m.cfg.CurrentVersion, min)
	}

	// Refuse to "upgrade" to the same or an older version. The same
	// branch also covers a notarized-but-vulnerable older artifact
	// served by a compromised CDN.
	if compareVersions(manifest.Version, m.cfg.CurrentVersion) <= 0 {
		return manifest, false, nil
	}

	st := m.loadState(ctx)
	if slices.Contains(st.SkippedVersions, manifest.Version) {
		return manifest, false, nil
	}
	if !st.RemindAfter.IsZero() && m.cfg.Now().Before(st.RemindAfter) {
		return manifest, false, nil
	}

	return manifest, true, nil
}

// manifestEndpoint joins base with `manifest-<arch>.json` and validates
// the URL. Manifests are GitHub release assets published alongside the
// artifacts by dist.sh, one per arch at `<base>/manifest-<arch>.json` —
// there is no server-side proxy. The name is flat because release asset
// names cannot contain a path separator.
//
// Any path prefix on base is preserved so callers can host the CDN
// under a sub-path (e.g. https://example.test/cdn/...) for local
// testing without rewriting the URL.
func manifestEndpoint(base, arch string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse manifest base: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/manifest-" + arch + ".json"
	endpoint := u.String()
	if err := requireSecureURL(endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}

// requireSecureURL rejects rawURL when it is not https://, unless the
// scheme is exactly "https". There is no loopback exemption: an
// attacker who can bind a service on localhost (a malicious package
// running as the user, a compromised dev tool, a port-forward from a
// hostile network) must not be able to silently feed the upgrader a
// poisoned manifest forever. Tests stand up TLS servers via
// httptest.NewTLSServer and pass srv.Client() through Config.HTTPClient.
func requireSecureURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url %q is not https", rawURL)
	}
	return nil
}

// describeSuffix matches the `-<commits>-g<hash>[-dirty]` suffix that
// `git describe --tags` appends to the nearest tag.
var describeSuffix = regexp.MustCompile(`^(.+)-(\d+)-g[0-9a-f]{6,}(?:-dirty)?$`)

// splitDescribe separates a git-describe version into its base tag and
// the number of commits built on top of that tag.
func splitDescribe(v string) (string, int) {
	m := describeSuffix.FindStringSubmatch(v)
	if m == nil {
		return v, 0
	}
	ahead, err := strconv.Atoi(m[2])
	if err != nil {
		return v, 0
	}
	return m[1], ahead
}

// compareVersions orders release and dev-build versions. Semver alone
// is not enough: git describe output like "v1.2.0-4-gabcdef" parses as
// a *pre-release* of v1.2.0, so a client built four commits past the
// tag would be offered an "upgrade" to the release it already
// contains. Commits past a shared base tag therefore break the tie.
func compareVersions(a, b string) int {
	aBase, aAhead := splitDescribe(a)
	bBase, bAhead := splitDescribe(b)
	if c := compareTags(aBase, bBase); c != 0 {
		return c
	}
	return cmp.Compare(aAhead, bAhead)
}

// compareTags returns semver.Compare(a, b) but falls back to string
// equality when either side is not a valid semver.
func compareTags(a, b string) int {
	if !semver.IsValid(a) || !semver.IsValid(b) {
		if a == b {
			return 0
		}
		// When we cannot compare, behave as if a is older so the
		// downgrade check (a <= b) returns "no upgrade" rather than
		// "yes upgrade" on garbage input. This is the safer default.
		return -1
	}
	return semver.Compare(a, b)
}

func (m *Manager) isThrottled(ctx context.Context) bool {
	st := m.loadState(ctx)
	if st.LastCheckedAt.IsZero() {
		return false
	}
	return m.cfg.Now().Sub(st.LastCheckedAt) < m.cfg.CheckPeriod
}

func (m *Manager) persistChecked(ctx context.Context) {
	st := m.loadState(ctx)
	st.LastCheckedAt = m.cfg.Now()
	m.saveState(ctx, st)
}

// remindLater pushes the next prompt out by one CheckPeriod.
func (m *Manager) remindLater(ctx context.Context) {
	st := m.loadState(ctx)
	st.RemindAfter = m.cfg.Now().Add(m.cfg.CheckPeriod)
	m.saveState(ctx, st)
}

// skipVersion records that the user does not want to be reminded
// about this specific version again.
func (m *Manager) skipVersion(ctx context.Context, version string) {
	st := m.loadState(ctx)
	if !slices.Contains(st.SkippedVersions, version) {
		st.SkippedVersions = append(st.SkippedVersions, version)
	}
	m.saveState(ctx, st)
}

func (m *Manager) loadState(ctx context.Context) state {
	var st state
	if err := m.cfg.Storage.Get(ctx, storageKeyState, &st); err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			log.WithError(err).Warn("ideupgrade: load state")
		}
		return state{}
	}
	return st
}

func (m *Manager) saveState(ctx context.Context, st state) {
	if err := m.cfg.Storage.Set(ctx, storageKeyState, st); err != nil {
		log.WithError(err).Warn("ideupgrade: save state")
	}
}

// Upgrade downloads and installs manifest, reporting progress to pw.
// It runs synchronously on the caller's goroutine.
//
// The install root targeted by the upgrade is *not* taken from
// Config; it is derived from the path of the running binary via
// detectRunningInstall so we upgrade the install that actually
// produced this process. When the running binary is not part of a
// managed install (dev build, sandbox, etc.) it returns
// ErrUpgradeNotSupported instead of touching anything.
func (m *Manager) Upgrade(
	ctx context.Context, manifest Manifest, pw repl.ProgressWriter,
) error {
	detected, err := detectRunningInstall(m.cfg)
	if err != nil {
		return err
	}
	if err := runUpgrade(ctx, upgradeOpts{
		manifest:         manifest,
		currentVersion:   m.cfg.CurrentVersion,
		cacheDir:         m.cfg.CacheDir,
		installRoot:      detected.InstallRoot,
		appName:          detected.AppName,
		cliSymlinkPath:   m.cfg.CLISymlinkPath,
		cliBinaryRelPath: detected.CLIBinaryRelPath,
		backupRetention:  m.cfg.BackupRetention,
		ops:              m.ops,
		pw:               pw,
	}); err != nil {
		return err
	}
	m.upgraded.Store(true)
	return nil
}

// homeDir returns the user's home directory or "" on error. Used to
// derive the default install paths.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func defaultCacheDir() string {
	if cd, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cd, "rune", "upgrade")
	}
	return filepath.Join(os.TempDir(), "rune-upgrade")
}

// defaultHTTPClient returns the http.Client used when the caller of
// New doesn't pass one. The transport bounds dial and
// response-header reads but deliberately leaves the body read
// unbounded so that slow artifact downloads can complete.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   DefaultHTTPDialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   DefaultHTTPDialTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: DefaultHTTPResponseHeaderTimeout,
		},
	}
}

func defaultCLISymlinkPath() string {
	return filepath.Join(homeDir(), ".local", "bin", "rune")
}
