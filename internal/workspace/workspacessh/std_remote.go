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

package workspacessh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	gitknownhosts "github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	"github.com/unstablebuild/blue/bluectx"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/crypto/ssh"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
)

const watcherWaitTimeout = 2 * time.Minute

// passwordCache remembers the last password the user typed at the
// interactive "ssh password:" prompt so that a reconnect (the
// maintainConnection retry loop re-dials, which starts a fresh auth
// exchange) reuses it instead of prompting the user again. It is owned by
// the scheme and therefore lives across reconnects.
//
// The cache is only consulted for the first PasswordCallback of a dial. A
// rejected password causes ssh.RetryableAuthMethod to re-invoke the
// callback; on that path we always prompt fresh and overwrite the cache so
// a stale (wrong) password is never re-served.
type passwordCache struct {
	mu  sync.Mutex
	val string
	set bool
}

type sessionHostKeyPin struct {
	mu       sync.Mutex
	hostPort string
	key      ssh.PublicKey
	set      bool
}

func (c *passwordCache) get() (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, c.set
}

func (c *passwordCache) put(v string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val = v
	c.set = true
}

func (p *sessionHostKeyPin) get(hostPort string) (ssh.PublicKey, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.set || p.hostPort != hostPort {
		return nil, false
	}
	return p.key, true
}

func (p *sessionHostKeyPin) put(hostPort string, key ssh.PublicKey) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostPort = hostPort
	p.key = key
	p.set = true
}

// ErrAuthRequiredKey indicates the server requires publickey authentication
// but the client has no usable signers configured.
var ErrAuthRequiredKey = errors.New(
	"server requires publickey authentication but no client keys are configured. " +
		"Add a path to ssh.private_keys (or store an existing key under ~/.ssh)")

// ErrHostKeyMismatch indicates the server's host key did not match the entry
// recorded in known_hosts. Surfaced as a hard error: never prompts the user,
// because this typically indicates a man-in-the-middle attack.
var ErrHostKeyMismatch = errors.New(
	"host key verification failed: the server's host key does not match the " +
		"entry recorded in known_hosts")

// ErrHostKeyUnknown indicates the server's host key is not recorded in
// known_hosts (trust-on-first-use). Unlike ErrHostKeyMismatch this is
// recoverable: with strict host key checking the user is prompted once to
// trust the key, which is then appended to known_hosts.
var ErrHostKeyUnknown = errors.New(
	"host key verification failed: the server's host key is not recorded in " +
		"known_hosts")

// ErrHostUnreachable wraps low-level network failures (DNS, TCP).
var ErrHostUnreachable = errors.New("could not reach ssh host")

// ErrKnownHostsUnparsable indicates the known_hosts file exists but could
// not be parsed (a malformed line). The host key cannot be verified against
// it, so the user is prompted to trust the presented key for this session
// only; the malformed file is never rewritten.
var ErrKnownHostsUnparsable = errors.New(
	"host key verification failed: known_hosts could not be parsed")

// HostKeyError is returned when host-key verification fails against
// known_hosts. It captures the details needed to prompt the user and, on
// accept, persist the presented key. It wraps ErrHostKeyUnknown (unknown
// host), ErrHostKeyMismatch (changed key), or ErrKnownHostsUnparsable
// (malformed file) so existing errors.Is checks keep working.
type HostKeyError struct {
	// Unknown is true when the host is not recorded at all (first use);
	// false when known_hosts records a different key for the host.
	Unknown bool
	// Unparsable is true when known_hosts could not be parsed. In this
	// case the file is never rewritten: the only recovery is trust-once.
	Unparsable bool
	// KnownHostsPath is the resolved known_hosts file the entry will be
	// written to on accept.
	KnownHostsPath string
	// Host is the hostname (without port) the server presented as.
	Host string
	// HostPort is the host:port dialed, used to normalize known_hosts
	// entries and query pinned keys/algorithms.
	HostPort string
	// Remote is the resolved network address of the server, recorded
	// alongside the hostname in the known_hosts entry.
	Remote net.Addr
	// Presented is the host key the server offered.
	Presented ssh.PublicKey
	// KnownKeys are the keys currently pinned for the host (changed case),
	// shown as the previously-trusted fingerprints in the warning.
	KnownKeys []ssh.PublicKey
	err       error
}

func (e *HostKeyError) Error() string { return e.err.Error() }

func (e *HostKeyError) Unwrap() error { return e.err }

var sigMap = map[syscall.Signal]ssh.Signal{
	syscall.SIGABRT: "ABRT",
	syscall.SIGALRM: "ALRM",
	syscall.SIGFPE:  "FPE",
	syscall.SIGHUP:  "HUP",
	syscall.SIGILL:  "ILL",
	syscall.SIGINT:  "INT",
	syscall.SIGKILL: "KILL",
	syscall.SIGPIPE: "PIPE",
	syscall.SIGQUIT: "QUIT",
	syscall.SIGSEGV: "SEGV",
	syscall.SIGTERM: "TERM",
}

// used to adapt ssh.Client to sshClient
type stdRemote struct {
	parentCtx context.Context
	client    *ssh.Client
	quitCh    chan struct{}
}

// used to adapt ssh.Session to Executor
type goSshSession struct {
	parentCtx context.Context
	ses       *ssh.Session
	quitCh    chan struct{}
	pid       int
}

func newStdRemote(
	ctx context.Context, cfg sshConfig, uri workspaceapi.URI, ui UI, passCache *passwordCache,
	hostKeyPin *sessionHostKeyPin,
) (remote, error) {
	username, err := usernameOrCurrent(uri)
	if err != nil {
		return nil, err
	}
	hostPort := hostPortFromURI(uri)
	pinnedKey, _ := hostKeyPin.get(hostPort)
	r, err := dialWithKeys(ctx, cfg, uri, ui, username, pinnedKey, passCache)
	if err == nil {
		return r, nil
	}

	// Recover from an unknown or changed host key: prompt once (or
	// auto-accept when strict checking is off), then re-dial exactly once.
	// "Trust and connect" persists the key to known_hosts and re-verifies
	// against the updated file; "trust once" pins the presented key in
	// memory for this session without touching known_hosts. insecure
	// bypasses verification, so it never reaches here.
	var hkErr *HostKeyError
	if cfg.insecure || !errors.As(err, &hkErr) {
		return nil, err
	}
	// When strict checking is off, accept without prompting. A malformed
	// known_hosts can't be persisted to, so it downgrades to trust-once.
	decision := hostKeyTrustPersist
	if hkErr.Unparsable {
		decision = hostKeyTrustOnce
	}
	if cfg.strictHostKeyChecking {
		d, promptErr := promptTrustHostKey(ctx, ui, hkErr)
		if promptErr != nil {
			return nil, promptErr
		}
		decision = d
	}
	switch decision {
	case hostKeyReject:
		return nil, err
	case hostKeyTrustOnce:
		hostKeyPin.put(hkErr.HostPort, hkErr.Presented)
		return dialWithKeys(ctx, cfg, uri, ui, username, hkErr.Presented, passCache)
	default:
		if persistErr := persistKnownHostKey(hkErr); persistErr != nil {
			return nil, fmt.Errorf("could not record host key in %s: %w",
				hkErr.KnownHostsPath, persistErr)
		}
		return dialWithKeys(ctx, cfg, uri, ui, username, nil, passCache)
	}
}

// dialWithKeys resolves the effective key list, builds the host-key
// callback, and performs the SSH handshake. It is invoked a second time
// after the user trusts a new host key so the freshly written known_hosts
// entry is re-read and verified. A non-nil pinnedKey bypasses known_hosts
// and accepts exactly that key, used for the "trust once" path.
func dialWithKeys(
	ctx context.Context, cfg sshConfig, uri workspaceapi.URI, ui UI,
	username string, pinnedKey ssh.PublicKey, passCache *passwordCache,
) (remote, error) {
	hostkeyCallback, hostKeyAlgos, err := buildHostkeyCallback(cfg, uri, pinnedKey)
	if err != nil {
		return nil, err
	}

	// Resolve the effective key list once. Empty cfg.privateKeys means
	// "try the canonical ~/.ssh/id_* defaults", which mirrors OpenSSH.
	keyPaths := cfg.privateKeys
	if len(keyPaths) == 0 {
		keyPaths = defaultIdentityFiles()
	}

	// Single-attempt path. Preserves the original ordering (URI password
	// → all configured keys in one PublicKeysCallback → password →
	// kbd-interactive) so the chained AuthenticationMethods flow and
	// every other matrix scenario keep behaving exactly as before.
	if len(keyPaths) <= 1 {
		return dialOnce(ctx, cfg, uri, ui, username, hostkeyCallback, hostKeyAlgos,
			keyPaths, true /* includeFallbacks */, passCache, pinnedKey != nil)
	}

	// Two or more keys: redial per key so that server limits like
	// MaxAuthTries=1 don't burn the budget on the first (wrong) key
	// before the right one gets a turn. Interactive fallbacks
	// (password, kbd-interactive) run only on the final attempt to
	// avoid prompting the user once per failed key.
	hostport := hostPortFromURI(uri)
	var lastErr error
	for i, kp := range keyPaths {
		last := i == len(keyPaths)-1
		r, err := dialOnce(ctx, cfg, uri, ui, username, hostkeyCallback, hostKeyAlgos,
			[]string{kp}, last, passCache, pinnedKey != nil)
		if err == nil {
			return r, nil
		}
		if !shouldRetryWithNextKey(err) {
			return nil, err
		}
		lastErr = err
	}
	if lastErr == nil {
		// Defensive: keyPaths was non-empty so the loop must have set
		// lastErr on every miss. Surface a generic auth failure rather
		// than returning nil if this invariant is ever broken.
		return nil, fmt.Errorf("ssh authentication to %s failed: no keys succeeded", hostport)
	}
	return nil, lastErr
}

// dialOnce performs a single TCP+SSH handshake using the given key
// paths. When includeFallbacks is true the password and (optionally)
// keyboard-interactive auth methods are wired in alongside the
// pubkey method, exactly mirroring the original single-attempt flow.
func dialOnce(
	ctx context.Context, cfg sshConfig, uri workspaceapi.URI, ui UI,
	username string, hostkeyCallback ssh.HostKeyCallback, hostKeyAlgos []string,
	keyPaths []string, includeFallbacks bool, passCache *passwordCache, sessionPinned bool,
) (remote, error) {
	auths, err := authMethodsFromURI(ctx, cfg, uri, ui, keyPaths, includeFallbacks, passCache)
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            username,
		HostKeyCallback: hostkeyCallback,
		Auth:            auths,
		Timeout:         cfg.timeout,
	}
	// Pin the host-key algorithms to the ones recorded in known_hosts so
	// negotiation lands on a key type we actually have, instead of a type
	// the server also offers but we never recorded (which would surface as
	// a spurious mismatch). Empty for unknown hosts: leave the default.
	if len(hostKeyAlgos) > 0 {
		conf.HostKeyAlgorithms = hostKeyAlgos
	}
	hostport := hostPortFromURI(uri)
	conn, err := ssh.Dial("tcp", hostport, conf)
	if err != nil {
		return nil, translateDialError(hostport, len(cfg.privateKeys) > 0, sessionPinned, err)
	}
	return &stdRemote{parentCtx: ctx, client: conn, quitCh: make(chan struct{})}, nil
}

// buildHostkeyCallback builds the host-key verification callback along
// with the host-key algorithms pinned in known_hosts for the target host.
// The callback wraps the go-git/x-crypto knownhosts verifier: on failure
// it classifies the error as unknown-host or changed-key and surfaces a
// *HostKeyError carrying the details needed to prompt and persist.
func buildHostkeyCallback(
	cfg sshConfig, uri workspaceapi.URI, pinnedKey ssh.PublicKey,
) (ssh.HostKeyCallback, []string, error) {
	if cfg.insecure {
		return ssh.InsecureIgnoreHostKey(), nil, nil
	}
	// "Trust once": accept exactly the key the user just approved without
	// consulting or modifying known_hosts.
	if pinnedKey != nil {
		// Pin the algorithm to the approved key's type so negotiation
		// lands on it rather than another type the server also offers,
		// which FixedHostKey would then reject as a mismatch.
		fixed := ssh.FixedHostKey(pinnedKey)
		callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if err := fixed(hostname, remote, key); err != nil {
				return fmt.Errorf("%w: %v", ErrHostKeyMismatch, err)
			}
			return nil
		}
		return callback, sessionHostKeyAlgorithms(pinnedKey), nil
	}
	knownHostsPath, err := resolveKnownHostsPath(cfg.knownHostsPath)
	if err != nil {
		return nil, nil, err
	}
	hostport := hostPortFromURI(uri)

	// A missing known_hosts file (e.g. never connected before) is not an
	// error: every host is simply unknown. NewDB fails to open it, so
	// synthesize an empty verifier that reports unknown for every host.
	db, err := gitknownhosts.NewDB(knownHostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return unknownHostCallback(knownHostsPath, hostport), nil, nil
		}
		// The file exists but a line could not be parsed. We can't verify
		// against it and can't safely rewrite it, so surface a recoverable
		// error that prompts the user to trust the key for this session.
		return unparsableHostCallback(knownHostsPath, hostport, err), nil, nil
	}

	inner := db.HostKeyCallback()
	var known []ssh.PublicKey
	for _, k := range db.HostKeys(hostport) {
		known = append(known, k.PublicKey)
	}
	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := inner(hostname, remote, key); err != nil {
			return classifyHostKeyError(knownHostsPath, hostport, hostname,
				remote, key, known, err)
		}
		return nil
	}
	return cb, db.HostKeyAlgorithms(hostport), nil
}

func sessionHostKeyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoRSA,
		}
	}
	return []string{key.Type()}
}

// unknownHostCallback returns a callback that treats every host as unknown,
// used when the known_hosts file does not yet exist.
func unknownHostCallback(knownHostsPath, hostport string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return &HostKeyError{
			Unknown:        true,
			KnownHostsPath: knownHostsPath,
			Host:           hostname,
			HostPort:       hostport,
			Remote:         remote,
			Presented:      key,
			err: fmt.Errorf("%w: %s (%s)", ErrHostKeyUnknown,
				hostname, ssh.FingerprintSHA256(key)),
		}
	}
}

// unparsableHostCallback returns a callback used when known_hosts could not
// be parsed: it captures the presented key and surfaces a recoverable
// *HostKeyError so the user can trust it for this session (never persisted).
func unparsableHostCallback(knownHostsPath, hostport string, parseErr error) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return &HostKeyError{
			Unparsable:     true,
			KnownHostsPath: knownHostsPath,
			Host:           hostname,
			HostPort:       hostport,
			Remote:         remote,
			Presented:      key,
			err:            fmt.Errorf("%w (%s): %v", ErrKnownHostsUnparsable, knownHostsPath, parseErr),
		}
	}
}

// classifyHostKeyError converts a knownhosts verification failure into a
// typed *HostKeyError, distinguishing an unknown host from a changed key.
func classifyHostKeyError(
	knownHostsPath, hostport, hostname string, remote net.Addr,
	key ssh.PublicKey, known []ssh.PublicKey, err error,
) error {
	switch {
	case gitknownhosts.IsHostUnknown(err):
		return &HostKeyError{
			Unknown:        true,
			KnownHostsPath: knownHostsPath,
			Host:           hostname,
			HostPort:       hostport,
			Remote:         remote,
			Presented:      key,
			err:            fmt.Errorf("%w: %v", ErrHostKeyUnknown, err),
		}
	case gitknownhosts.IsHostKeyChanged(err):
		return &HostKeyError{
			Unknown:        false,
			KnownHostsPath: knownHostsPath,
			Host:           hostname,
			HostPort:       hostport,
			Remote:         remote,
			Presented:      key,
			KnownKeys:      known,
			err:            fmt.Errorf("%w: %v", ErrHostKeyMismatch, err),
		}
	default:
		return err
	}
}

// shouldRetryWithNextKey reports whether a failed dial attempt is the
// kind of failure that another key might recover from. Hard errors
// (host key mismatch, host unreachable, server requires keys but we
// have none, context cancellation) are not retryable: the next key
// would only reproduce the same failure.
func shouldRetryWithNextKey(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrHostKeyMismatch),
		errors.Is(err, ErrHostKeyUnknown),
		errors.Is(err, ErrKnownHostsUnparsable),
		errors.Is(err, ErrHostUnreachable),
		errors.Is(err, ErrAuthRequiredKey),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return false
	}
	return true
}

func (s *goSshSession) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	if s.pid != 0 {
		panic("Command called more than once on an ssh session")
	}
	if cmd.Path == "" {
		return 0, errors.New("no command")
	}

	s.pid++

	s.ses.Stdout = cmd.Stdout
	s.ses.Stderr = cmd.Stderr
	s.ses.Stdin = cmd.Stdin

	err := s.ses.Start(
		fmt.Sprintf("%s %s", cmd.Path, strings.Join(cmd.Args, " ")))
	if err != nil {
		return 0, err
	}

	// ensure that at least one of the ctxs passed to First
	// is canceled after the command is done.
	ctx, cancel := bluectx.First(s.parentCtx, ctx)

	// wait and dispatch error to watcher
	go debug.CapturePanicReport(func() {
		defer cancel()

		err := s.ses.Wait()
		if cmd.Watcher != nil && cmd.Watcher.WatchProcess() != nil {
			// avoid buggy watchers to cause this goroutine to block forever,
			// so the timeout should be in the order of minutes.
			ctx, cancelTimeout := context.WithTimeout(
				context.Background(), watcherWaitTimeout)
			defer cancelTimeout()
			select {
			case <-ctx.Done():
			case cmd.Watcher.WatchProcess() <- err:
			}
		}
	})

	// kill command if context is done
	go debug.CapturePanicReport(func() {
		select {
		case <-ctx.Done():
			s.ses.Close()
		case <-s.quitCh:
		}
	})

	return workspaceapi.Pid(s.pid), nil
}

func (s *goSshSession) Signal(_ workspaceapi.Pid, sig syscall.Signal) error {
	signal, ok := sigMap[sig]
	if !ok {
		return errors.New("unknown signal")
	}
	return s.ses.Signal(signal)
}

func (r *goSshSession) Close() error {
	return r.ses.Close()
}

func (r *stdRemote) NewSession() (schemeapi.Executor, error) {
	ses, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	ret := &goSshSession{parentCtx: r.parentCtx, ses: ses, quitCh: r.quitCh}
	return ret, nil
}

func (r *stdRemote) Close() error {
	close(r.quitCh)
	return r.client.Close()
}

// authMethodsFromURI builds the list of ssh.AuthMethod values offered to
// the server. Methods are arranged so that passive credentials (URI
// password, configured private keys) are tried first, with interactive
// callbacks (password, keyboard-interactive) falling back to ui prompts.
//
// The Go ssh client only invokes a callback when the server's
// methodsAllowed list advertises the corresponding method, so the user
// only sees prompts that can succeed.
func authMethodsFromURI(
	ctx context.Context, cfg sshConfig, uri workspaceapi.URI, ui UI,
	keyPaths []string, includeFallbacks bool, passCache *passwordCache,
) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod

	// 1. URI-embedded password takes precedence: no prompt needed.
	if pass, ok := uri.Password(); ok {
		auths = append(auths, ssh.Password(pass))
	}

	// 2. Configured private keys (with on-demand passphrase prompts).
	//    Skip when no keys are available so we don't advertise an empty
	//    pubkey method to a chained-auth server.
	if len(keyPaths) > 0 {
		auths = append(auths, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			return gatherSigners(ctx, keyPaths, ui)
		}))
	}

	if !includeFallbacks {
		return auths, nil
	}

	// 3. Interactive password fallback, retried up to 3 times. We track
	//    whether the previous attempt succeeded by observing two
	//    consecutive callbacks: PasswordCallback is only re-invoked by
	//    ssh.RetryableAuthMethod when the prior credential was rejected,
	//    so on the 2nd+ call we can confidently surface a notification
	//    explaining why the user is being prompted again.
	var passAttempts int
	auths = append(auths, ssh.RetryableAuthMethod(
		ssh.PasswordCallback(func() (string, error) {
			// First callback of this dial: reuse the last-good password
			// (e.g. after a reconnect) so we don't re-prompt the user.
			if passAttempts == 0 {
				if pass, ok := passCache.get(); ok {
					passAttempts++
					return pass, nil
				}
			} else {
				// Prior credential was rejected: prompt fresh below.
				ui.Notify(NotificationError, fmt.Sprintf(
					"ssh: password rejected (attempt %d). Try again or press esc to cancel.",
					passAttempts))
			}
			passAttempts++
			pass, err := ui.PromptSecret(ctx, "ssh password: ")
			if err != nil {
				return "", err
			}
			passCache.put(pass)
			return pass, nil
		}),
		3,
	))

	// 4. Optional keyboard-interactive (PAM-style) challenges, retried up
	//    to 3 times. NOTE: the Go ssh client unconditionally invokes this
	//    AuthMethod when the server's USERAUTH_FAILURE reply lists
	//    "keyboard-interactive"; OpenSSH advertises it even when no
	//    challenge is configured, which surfaces as "unexpected message
	//    type 51 (expected 60)". Off by default; opt in via the
	//    `kbd_interactive` config key.
	if cfg.kbdInteractive {
		auths = append(auths, ssh.RetryableAuthMethod(
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				return askKbd(ctx, ui, name, instruction, questions, echos)
			}),
			3,
		))
	}

	return auths, nil
}

// gatherSigners parses the given private key paths into ssh.Signer values.
// When a key file is encrypted, ui.PromptSecret is used to collect the
// passphrase. Keys that fail to parse are notified to the user but skipped,
// so a single broken key does not prevent the others from being tried.
func gatherSigners(ctx context.Context, keyPaths []string, ui UI) ([]ssh.Signer, error) {
	var signers []ssh.Signer
	for _, keyPath := range keyPaths {
		signer, err := signerForKey(ctx, keyPath, ui)
		if err != nil {
			ui.Notify(NotificationWarning,
				fmt.Sprintf("ssh: skipping key %q: %v", keyPath, err))
			continue
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

// defaultIdentityFiles returns the list of canonical SSH identity files
// under the current user's ~/.ssh/ directory that exist on disk. The
// order mirrors OpenSSH's default IdentityFile preference.
func defaultIdentityFiles() []string {
	// os.UserHomeDir honours the HOME env var, which makes this function
	// straightforward to drive from tests without having to mock the
	// user database.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{
		"id_ed25519",
		"id_ecdsa",
		"id_ecdsa_sk",
		"id_ed25519_sk",
		"id_rsa",
		"id_dsa",
	}
	var found []string
	for _, name := range candidates {
		p := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return found
}

// signerForKey reads and parses a single private key file, prompting for a
// passphrase via ui when needed.
func signerForKey(ctx context.Context, keyPath string, ui UI) (ssh.Signer, error) {
	privateKey, err := workspace.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("could not read private key file: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err == nil {
		return signer, nil
	}
	if _, ok := err.(*ssh.PassphraseMissingError); !ok {
		return nil, fmt.Errorf("could not parse private key: %w", err)
	}
	passphrase, err := ui.PromptSecret(ctx,
		fmt.Sprintf("passphrase for ssh key %s: ", keyPath))
	if err != nil {
		return nil, fmt.Errorf("passphrase prompt: %w", err)
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("could not parse private key with passphrase: %w", err)
	}
	return signer, nil
}

// askKbd answers a single round of keyboard-interactive challenges. Each
// non-empty question is forwarded to ui.PromptSecret (echo=false) or
// ui.PromptText (echo=true).
func askKbd(
	ctx context.Context, ui UI,
	name, instruction string, questions []string, echos []bool,
) ([]string, error) {
	answers := make([]string, len(questions))
	for i, q := range questions {
		label := q
		if instruction != "" && i == 0 {
			label = instruction + "\n" + q
		}
		_ = name
		var (
			ans string
			err error
		)
		if i < len(echos) && echos[i] {
			ans, err = ui.PromptText(ctx, label, "")
		} else {
			ans, err = ui.PromptSecret(ctx, label)
		}
		if err != nil {
			return nil, err
		}
		answers[i] = ans
	}
	return answers, nil
}

// translateDialError maps a raw ssh.Dial error to a user-friendly typed
// error. The Go ssh library wraps server-rejection failures in errors with
// messages like "ssh: handshake failed: ssh: unable to authenticate, ..."
// and includes the list of methods the server advertised.
func translateDialError(hostport string, hasKeys, sessionPinned bool, err error) error {
	if err == nil {
		return nil
	}
	// The host-key callback already produced a typed *HostKeyError
	// (wrapping ErrHostKeyUnknown or ErrHostKeyMismatch). Surface it
	// unchanged so newStdRemote can drive the trust-on-first-use / rekey
	// recovery flow and errors.Is checks keep matching.
	var hkErr *HostKeyError
	if errors.As(err, &hkErr) {
		return hkErr
	}
	var negotiationErr *ssh.AlgorithmNegotiationError
	if sessionPinned && errors.As(err, &negotiationErr) && negotiationErr.What == "host key" {
		return fmt.Errorf("%w: %v", ErrHostKeyMismatch, err)
	}
	// Network-level failures (no route, DNS, refused connection) surface as
	// *net.OpError without an "ssh:" prefix in the message.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("%w (%s): %v", ErrHostUnreachable, hostport, err)
	}
	msg := err.Error()
	if strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "unable to authenticate") ||
		// Go's ssh client surfaces "unexpected message type 51" when the
		// server replies with USERAUTH_FAILURE during the kbd-interactive
		// flow without ever sending an INFO_REQUEST (i.e. the server
		// doesn't actually support kbd-interactive). For our purposes
		// that is just an authentication failure.
		strings.Contains(msg, "unexpected message type 51") {
		methods := parseAdvertisedMethods(msg)
		if !hasKeys && (len(methods) == 0 || len(methods) == 1 && methods[0] == "publickey") {
			return fmt.Errorf("%w (host %s)", ErrAuthRequiredKey, hostport)
		}
		if len(methods) > 0 {
			return fmt.Errorf(
				"ssh authentication to %s failed (server allowed: %s): %w",
				hostport, strings.Join(methods, ","), err,
			)
		}
		return fmt.Errorf("ssh authentication to %s failed: %w", hostport, err)
	}
	return fmt.Errorf("ssh dial %s: %w", hostport, err)
}

// parseAdvertisedMethods extracts the comma-separated list of methods the
// server advertised from a Go ssh failure message of the form
//
//	"...attempted methods [<tried>], no supported methods remain"
//
// or
//
//	"ssh: handshake failed: ssh: unable to authenticate, attempted methods [...], no supported methods remain"
//
// We don't have direct access to the failure packet from a public API, so
// the message text is the only source of this information.
func parseAdvertisedMethods(msg string) []string {
	const marker = "attempted methods ["
	i := strings.Index(msg, marker)
	if i < 0 {
		return nil
	}
	rest := msg[i+len(marker):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return nil
	}
	parts := strings.Split(rest[:end], " ")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "none" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// resolveKnownHostsPath resolves the known_hosts file to use: the explicit
// override when set, otherwise ~/.ssh/known_hosts.
func resolveKnownHostsPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := currentHomePath()
	if err != nil {
		return "", err
	}
	return path.Join(home, ".ssh/known_hosts"), nil
}

func getCurrentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("failed to get default user: %s", err)
	}
	return u.Username, nil
}

func usernameOrCurrent(uri workspaceapi.URI) (string, error) {
	if uri.User() != "" {
		return uri.User(), nil
	}
	return getCurrentUser()
}

func hostPortFromURI(uri workspaceapi.URI) string {
	hostname, port := uri.Hostname(), uri.Port()
	if port == "" {
		port = "22"
	}
	return fmt.Sprintf("%s:%s", hostname, port)
}

func currentHomePath() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("failed to lookup current username: %s", err)
	}
	return u.HomeDir, nil
}
