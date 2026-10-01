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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/crypto/ssh"
	"unstable.build/rune/internal/debug"
)

func TestStdRemoteAuthCallbacks(t *testing.T) {
	t.Run("publickey only with one configured key succeeds without prompts", func(t *testing.T) {
		keyPath, clientPub := generateClientKey(t)

		srvCfg := &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if string(key.Marshal()) == string(clientPub.Marshal()) {
					return nil, nil
				}
				return nil, fmt.Errorf("unknown key")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{}
		cfg := configForCallbackTest(t, srv, []string{keyPath})
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		assert.Empty(t, ui.prompts, "no prompts expected when key is configured")
	})

	t.Run("password-only server prompts via UI and succeeds", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{secrets: []string{"hunter2"}}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		require.Len(t, ui.prompts, 1)
		assert.Contains(t, ui.prompts[0], "secret:")
		assert.Contains(t, ui.prompts[0], "ssh password")
	})

	t.Run("password-only retries up to 3 times on wrong answer", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{secrets: []string{"wrong1", "wrong2", "hunter2"}}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		require.Len(t, ui.prompts, 3, "should have retried 3 times")
	})

	t.Run("URI password skips prompts", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "hunter2")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		assert.Empty(t, ui.prompts)
	})

	t.Run("shared password cache reuses password across dials", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		// Only one queued secret: the second dial must reuse the
		// cached password rather than prompting again (a second
		// prompt would return context.Canceled and fail the dial).
		ui := &recordingUI{secrets: []string{"hunter2"}}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")
		cache := new(passwordCache)

		r1, err := newStdRemote(context.Background(), cfg, uri, ui, cache, nil)
		require.NoError(t, err)
		_ = r1.Close()

		r2, err := newStdRemote(context.Background(), cfg, uri, ui, cache, nil)
		require.NoError(t, err, "reconnect should reuse the cached password")
		_ = r2.Close()

		require.Len(t, ui.prompts, 1,
			"second dial must not re-prompt when a good password is cached")
	})

	t.Run("cached password rejection re-prompts fresh", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		// Pre-seed the cache with a stale (wrong) password. The first
		// callback reuses it, the server rejects it, and the retry
		// path must prompt the user for a fresh one.
		ui := &recordingUI{secrets: []string{"hunter2"}}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")
		cache := new(passwordCache)
		cache.put("stale-wrong")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, cache, nil)
		require.NoError(t, err, "stale cached password must fall back to a fresh prompt")
		_ = r.Close()

		require.Len(t, ui.prompts, 1,
			"exactly one fresh prompt after the cached password is rejected")
		got, ok := cache.get()
		require.True(t, ok)
		assert.Equal(t, "hunter2", got, "cache must be updated with the freshly typed password")
	})

	t.Run("keyboard-interactive prompts for each challenge", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, c ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				answers, err := c("login", "Please answer", []string{"OTP: "}, []bool{false})
				if err != nil {
					return nil, err
				}
				if len(answers) == 1 && answers[0] == "123456" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad otp")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{secrets: []string{"123456"}}
		cfg := configForCallbackTest(t, srv, nil)
		cfg.kbdInteractive = true
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		require.NotEmpty(t, ui.prompts)
		assert.Contains(t, ui.prompts[len(ui.prompts)-1], "OTP")
	})

	t.Run("publickey-only server with no keys returns helpful error", func(t *testing.T) {
		srvCfg := &ssh.ServerConfig{
			PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, fmt.Errorf("nope")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")

		_, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAuthRequiredKey),
			"expected ErrAuthRequiredKey, got %v", err)
	})

	t.Run("network unreachable wraps as ErrHostUnreachable", func(t *testing.T) {
		ui := &recordingUI{}
		cfg := sshConfig{
			timeout:        500 * time.Millisecond,
			knownHostsPath: filepath.Join(t.TempDir(), "known_hosts_empty"),
			insecure:       true,
		}
		require.NoError(t, os.WriteFile(cfg.knownHostsPath, []byte{}, 0o600))
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().String()
		require.NoError(t, l.Close())
		uri, err := workspaceapi.ParseURI("ssh://test@" + addr + "/")
		require.NoError(t, err)

		_, err = newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrHostUnreachable),
			"expected ErrHostUnreachable, got %v", err)
	})
}

// TestStdRemoteMultiKeyRetry pins the per-connection retry that lets a
// dial recover after a wrong key is offered first against a server
// that caps MaxAuthTries to 1. The Go ssh client offers every signer
// returned by PublicKeysCallback within the same TCP connection, so
// the only way to survive MaxAuthTries=1 is to redial with the next
// key after a rejection. If newStdRemote ever drops that loop, the
// first wrong key burns the budget and the right key never gets a
// chance — exactly the failure mode the manual scenario 05 covers.
func TestStdRemoteMultiKeyRetry(t *testing.T) {
	rightPath, rightPub := generateClientKey(t)
	wrongPath, _ := generateClientKey(t)

	srvCfg := &ssh.ServerConfig{
		MaxAuthTries: 1,
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(rightPub.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	srv := startInProcessServer(t, srvCfg)

	ui := &recordingUI{}
	cfg := configForCallbackTest(t, srv, []string{wrongPath, rightPath})
	uri := uriForServer(t, srv, "")

	r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
	require.NoError(t, err,
		"with MaxAuthTries=1 the server severs the first connection "+
			"after the wrong key; the dial must retry on a fresh "+
			"connection with the next configured key")
	_ = r.Close()
	assert.Empty(t, ui.prompts,
		"per-key retry must not prompt the user; saw %v", ui.prompts)
}

// TestDefaultIdentityFiles confirms that when ssh.private_keys is empty
// we fall back to canonical ~/.ssh/id_* keys discovered on disk. This is
// the exact scenario where a user expects pubkey auth to "just work"
// against a host that requires it (and where the previous static-auth
// path silently fell back to repeated password prompts).
func TestDefaultIdentityFiles(t *testing.T) {
	t.Run("auto-discovers keys from ~/.ssh when private_keys is empty", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		// Ensure os.UserHomeDir picks up the override even on macOS
		// where USERPROFILE is irrelevant.
		clientPub := writeKeyAt(t, filepath.Join(home, ".ssh", "id_ed25519"))

		srvCfg := &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if string(key.Marshal()) == string(clientPub.Marshal()) {
					return nil, nil
				}
				return nil, fmt.Errorf("unknown key")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{}
		cfg := configForCallbackTest(t, srv, nil) // empty private_keys
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err, "default ~/.ssh/id_ed25519 should be tried automatically")
		_ = r.Close()
		assert.Empty(t, ui.prompts, "no prompts expected when default key exists")
	})

	t.Run("falls back to password prompt when ~/.ssh has no usable key", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		srvCfg := &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if string(password) == "hunter2" {
					return nil, nil
				}
				return nil, fmt.Errorf("bad password")
			},
		}
		srv := startInProcessServer(t, srvCfg)

		ui := &recordingUI{secrets: []string{"hunter2"}}
		cfg := configForCallbackTest(t, srv, nil)
		uri := uriForServer(t, srv, "")

		r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
		require.NoError(t, err)
		_ = r.Close()
		require.Len(t, ui.prompts, 1)
	})
}

// TestPasswordRetryNotifiesOnFailure confirms that when the password
// callback is re-invoked because the previous password was rejected the
// user is notified, so they understand why the prompt keeps reappearing
// instead of seeing a silent retry loop.
func TestPasswordRetryNotifiesOnFailure(t *testing.T) {
	srvCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == "hunter2" {
				return nil, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	srv := startInProcessServer(t, srvCfg)

	// First two attempts wrong; third is right.
	ui := &recordingUI{secrets: []string{"wrong1", "wrong2", "hunter2"}}
	cfg := configForCallbackTest(t, srv, nil)
	uri := uriForServer(t, srv, "")

	r, err := newStdRemote(context.Background(), cfg, uri, ui, nil, nil)
	require.NoError(t, err)
	_ = r.Close()

	require.Len(t, ui.prompts, 3, "expected 3 password prompts")
	// Two failure notifications: one before each retry.
	require.GreaterOrEqual(t, len(ui.notifs), 2,
		"expected user to be notified after each password rejection, got %v", ui.notifs)
	for _, n := range ui.notifs[:2] {
		assert.Contains(t, n, "password rejected")
	}
}

// recordingUI captures prompts and replies with canned answers.
type recordingUI struct {
	mu           sync.Mutex
	prompts      []string
	secrets      []string
	texts        []string
	choice       int
	choices      []int // queued PromptChoice results; falls back to choice
	choiceCancel bool  // when true PromptChoice returns context.Canceled
	notifs       []string
	levels       []NotificationLevel
	progress     []progressUpdate
}

// progressUpdate records one UpdateNotificationProgress call.
type progressUpdate struct {
	id       string
	message  string
	progress int
	total    int
}

func (u *recordingUI) PromptSecret(_ context.Context, label string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "secret:"+label)
	if len(u.secrets) == 0 {
		return "", context.Canceled
	}
	ans := u.secrets[0]
	u.secrets = u.secrets[1:]
	return ans, nil
}

func (u *recordingUI) PromptText(_ context.Context, label, _ string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "text:"+label)
	if len(u.texts) == 0 {
		return "", context.Canceled
	}
	ans := u.texts[0]
	u.texts = u.texts[1:]
	return ans, nil
}

func (u *recordingUI) PromptChoice(_ context.Context, label string, _ []string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "choice:"+label)
	if u.choiceCancel {
		return -1, context.Canceled
	}
	if len(u.choices) > 0 {
		c := u.choices[0]
		u.choices = u.choices[1:]
		return c, nil
	}
	return u.choice, nil
}

func (u *recordingUI) Notify(level NotificationLevel, msg string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.notifs = append(u.notifs, msg)
	u.levels = append(u.levels, level)
	return "noti-" + msg
}

func (u *recordingUI) UpdateNotificationProgress(id, message string, progress, total int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.progress = append(u.progress, progressUpdate{
		id: id, message: message, progress: progress, total: total,
	})
}

// progressUpdates returns a copy of the recorded progress updates.
func (u *recordingUI) progressUpdates() []progressUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.progress)
}

// notifications returns a copy of the recorded (level, message) notifications.
func (u *recordingUI) notifications() ([]NotificationLevel, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.levels), slices.Clone(u.notifs)
}

// inProcessServer drives the server side of one ssh handshake, stopping
// after authentication (or after returning the configured rejection).
type inProcessServer struct {
	listener net.Listener
	cfg      *ssh.ServerConfig
	hostKey  ssh.Signer
	done     chan struct{}
}

func newServerSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer
}

func startInProcessServer(t *testing.T, cfg *ssh.ServerConfig) *inProcessServer {
	t.Helper()
	return startInProcessServerWithHostKey(t, cfg, newServerSigner(t))
}

func startInProcessServerWithHostKey(
	t *testing.T, cfg *ssh.ServerConfig, hostKey ssh.Signer,
) *inProcessServer {
	t.Helper()
	cfg.AddHostKey(hostKey)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &inProcessServer{listener: l, cfg: cfg, hostKey: hostKey, done: make(chan struct{})}
	go func() {
		defer close(srv.done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			// Run handshake; we don't service channels.
			go func(c net.Conn) {
				conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					_ = c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				go func() {
					for ch := range chans {
						_ = ch.Reject(ssh.UnknownChannelType, "no")
					}
				}()
				_ = conn.Close()
			}(c)
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return srv
}

func (s *inProcessServer) addr() string {
	return s.listener.Addr().String()
}

// configForCallbackTest builds a workspace ssh config pointing at the
// in-process server, with a temp known_hosts entry for that host.
func configForCallbackTest(t *testing.T, srv *inProcessServer, privateKeys []string) sshConfig {
	t.Helper()
	khFile := filepath.Join(t.TempDir(), "known_hosts")
	host, port, err := net.SplitHostPort(srv.addr())
	require.NoError(t, err)
	entry := knownHostsLine(t, host, port, srv.hostKey.PublicKey())
	require.NoError(t, os.WriteFile(khFile, []byte(entry), 0o600))
	return sshConfig{
		privateKeys:    privateKeys,
		timeout:        2 * time.Second,
		knownHostsPath: khFile,
	}
}

func knownHostsLine(t *testing.T, host, port string, pub ssh.PublicKey) string {
	t.Helper()
	addr := host
	if port != "22" {
		addr = fmt.Sprintf("[%s]:%s", host, port)
	}
	return fmt.Sprintf("%s %s\n", addr, string(ssh.MarshalAuthorizedKey(pub)))
}

func uriForServer(t *testing.T, srv *inProcessServer, withPassword string) workspaceapi.URI {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.addr())
	require.NoError(t, err)
	uriStr := fmt.Sprintf("ssh://test@%s:%s/", host, port)
	if withPassword != "" {
		uriStr = fmt.Sprintf("ssh://test:%s@%s:%s/", withPassword, host, port)
	}
	uri, err := workspaceapi.ParseURI(uriStr)
	require.NoError(t, err)
	return uri
}

// generateClientKey writes a fresh ed25519 private key to a file and returns
// the path plus the corresponding ssh.PublicKey.
func generateClientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600))
	signer, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	_ = base64.StdEncoding // keep import even if removed elsewhere later
	return keyPath, signer
}

// writeKeyAt copies a freshly generated ed25519 private key to the given
// destination path and returns its public key.
func writeKeyAt(t *testing.T, dst string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o700))
	require.NoError(t, os.WriteFile(dst, pem.EncodeToMemory(block), 0o600))
	signer, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return signer
}

func TestGoSshSessionSignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sig     syscall.Signal
		want    ssh.Signal
		wantErr bool
	}{
		{name: "term", sig: syscall.SIGTERM, want: "TERM"},
		{name: "kill", sig: syscall.SIGKILL, want: "KILL"},
		{name: "unknown", sig: syscall.Signal(0), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sentSSHSignal(t, tc.sig)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// sentSSHSignal calls goSshSession.Signal on a session opened against an
// in-process server and returns the signal name the server received.
func sentSSHSignal(t *testing.T, sig syscall.Signal) (ssh.Signal, error) {
	t.Helper()
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(newServerSigner(t))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	received := make(chan ssh.Signal, 1)
	go debug.CapturePanicReport(func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, chans, reqs, err := ssh.NewServerConn(c, cfg)
		if err != nil {
			return
		}
		go debug.CapturePanicReport(func() { ssh.DiscardRequests(reqs) })
		for newCh := range chans {
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				return
			}
			defer ch.Close()
			go debug.CapturePanicReport(func() {
				for req := range chReqs {
					var msg struct{ Signal string }
					if req.Type == "signal" && ssh.Unmarshal(req.Payload, &msg) == nil {
						received <- ssh.Signal(msg.Signal)
					}
					if req.WantReply {
						_ = req.Reply(false, nil)
					}
				}
			})
		}
	})

	client, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	ses, err := client.NewSession()
	require.NoError(t, err)

	if err := (&goSshSession{ses: ses}).Signal(0, sig); err != nil {
		return "", err
	}
	select {
	case got := <-received:
		return got, nil
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive a signal request")
		return "", nil
	}
}
