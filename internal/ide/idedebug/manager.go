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

package idedebug

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"unstable.build/rune/internal/debug"
)

// PkgManager abstracts the ability to resolve package
// directories for a given package identifier.
type PkgManager interface {
	// LibDir returns an iterator of directory paths where
	// the package's binaries may be found.
	LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error)
}

// debugConfig holds the parameters used to spawn a single debug
// adapter. The placeholders {addr}, {host}, and {port} in args are
// replaced at runtime with the TCP endpoint the adapter should
// listen on (e.g. {addr}="127.0.0.1:56789", {host}="127.0.0.1",
// {port}="56789").
type debugConfig struct {
	langID    string
	adapterID string
	command   string
	args      []string
	// launchArgs and attachArgs are static argument templates merged
	// into the DAP launch/attach request payloads. Placeholder tokens
	// (e.g. {program}, {pid}) are substituted at call time; an empty
	// template sends only the SDK-typed overlays, so adapter-specific
	// keys must come from the language package's debugger config.
	launchArgs map[string]string
	attachArgs map[string]string
}

// AdapterConfig describes how to launch the debug adapter for a
// given DAP language ID. The language ID itself is the map key
// in Config.Adapters.
type AdapterConfig struct {
	// Command is the argv template for spawning the adapter.
	// The first element is the executable; the remaining
	// elements are its arguments. Any {addr} placeholder is
	// replaced at runtime with a bound host:port the adapter
	// should listen on; {host} and {port} expand to the
	// components for adapters that take them separately.
	//
	// A single "connect://host:port" element instead dials an
	// adapter that is already listening at that address (e.g. one
	// spawned by `python -m debugpy --listen host:port`) rather
	// than spawning a process.
	Command []string
	// AdapterID is the DAP adapter identifier advertised during
	// the Initialize handshake. Defaults to the language ID.
	AdapterID string
	// LaunchArgs is a static launch-argument template merged into
	// the DAP launch request. Keys are DAP argument names; values
	// may contain placeholders ({program}, {args}, {cwd}, {env},
	// {stopOnEntry}, {noDebug}) substituted at launch time. A dotted
	// key (e.g. "connect.host") nests the value under intermediate
	// objects. When nil, only the SDK-typed overlays are sent and
	// adapter-specific keys (e.g. Delve's mode/outputMode) must be
	// supplied here via the language package's debugger config.
	LaunchArgs map[string]string
	// AttachArgs is a static attach-argument template merged into
	// the DAP attach request. Keys are DAP argument names; values
	// may contain placeholders ({program}, {pid}) substituted at
	// attach time. A dotted key (e.g. "connect.host") nests the value
	// under intermediate objects, as debugpy's attach requires. When
	// nil, only the SDK-typed overlays are sent and adapter-specific
	// keys (e.g. Delve's mode) must be supplied here
	// via the language package's debugger config.
	AttachArgs map[string]string
}

// Config provides configuration for a Manager.
type Config struct {
	MaxRetries        int
	InitializeTimeout time.Duration
	CloseTimeout      time.Duration
	// Adapters is the registry of debug adapters keyed by DAP
	// language ID. Populated from the `debugger` section of
	// rune.star (see ide/config.go).
	Adapters map[string]AdapterConfig
}

// Manager is a multi-session DAP server manager implementing
// debugapi.Debugger. Every call to CreateSession spawns a fresh
// *debugServer for the configured adapter of the requested
// langID and assigns it a unique sessionID. All subsequent
// method calls dispatch via sessionID.
type Manager struct {
	cfg        Config
	rootURI    string
	executor   schemeapi.Executor
	pkgManager PkgManager
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	log        *slog.Logger

	mu       sync.Mutex
	sessions map[string]*debugServer // sessionID -> server
}

var _ debugapi.Debugger = (*Manager)(nil)

// New creates a new Manager with the given dependencies
// and configuration.
func New(
	uri workspaceapi.URI,
	executor schemeapi.Executor,
	pkgManager PkgManager,
	cfg Config,
) *Manager {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.InitializeTimeout == 0 {
		cfg.InitializeTimeout = 10 * time.Second
	}
	if cfg.CloseTimeout == 0 {
		cfg.CloseTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:        cfg,
		log:        slog.With("struct", "idedebug.Manager"),
		rootURI:    convertURI(uri),
		executor:   executor,
		pkgManager: pkgManager,
		sessions:   make(map[string]*debugServer),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Close shuts down all active debug servers and waits
// for all background goroutines to exit.
func (m *Manager) Close() error {
	m.cancel()

	m.mu.Lock()
	servers := make([]*debugServer, 0, len(m.sessions))
	for _, s := range m.sessions {
		servers = append(servers, s)
	}
	m.mu.Unlock()

	var errs []error
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.CloseTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	m.wg.Wait()
	return errors.Join(errs...)
}

// newSessionID mints a random, unique session identifier.
func newSessionID() (string, error) {
	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString(buf[:]), nil
}

// sessionFor looks up the *debugServer for sessionID, returning
// ErrSessionNotFound if none exists.
func (m *Manager) sessionFor(sessionID string) (*debugServer, error) {
	m.mu.Lock()
	srv := m.sessions[sessionID]
	m.mu.Unlock()
	if srv == nil {
		return nil, debugapi.ErrSessionNotFound
	}
	return srv, nil
}

// startSession creates the *debugServer for cfg, performs the
// DAP Initialize handshake, inserts it into the sessions map
// under sessionID, and spawns the watchSession goroutine.
func (m *Manager) startSession(
	ctx context.Context, sessionID string, cfg debugConfig,
	client debugapi.ClientCapabilities, sub debugapi.EventSubscriber,
) (*debugServer, error) {
	if cfg.command == "" {
		return nil, errors.New("debug adapter config with empty command")
	}
	var binPath string
	if strings.HasPrefix(cfg.command, connectScheme) {
		binPath = cfg.command
	} else if filepath.IsAbs(cfg.command) {
		binPath = cfg.command
	} else {
		var err error
		binPath, err = m.findBinary(ctx, &cfg)
		if err != nil {
			m.log.Debug("find debug adapter, falling back to PATH",
				"executable", cfg.command, "error", err)
			binPath = cfg.command
		}
	}

	srv := newDebugServer(m.ctx, cfg, binPath, m.executor, m.rootURI, client, sub)

	ctx, cancel := context.WithTimeout(ctx, m.cfg.InitializeTimeout)
	defer cancel()

	if err := srv.start(ctx); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[sessionID] = srv
	m.mu.Unlock()

	m.wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer m.wg.Done()
		m.watchSession(sessionID, &cfg, srv)
	})
	return srv, nil
}

func (m *Manager) findBinary(
	ctx context.Context, cfg *debugConfig,
) (string, error) {
	files, err := m.pkgManager.LibDir(ctx, cfg.langID)
	if err != nil {
		return "", fmt.Errorf("lib dir: %w", err)
	}
	defer func() { _ = files.Close() }()

	paths, err := iterator.ToSlice(ctx, files)
	if err != nil {
		return "", fmt.Errorf("lib dir iterator: %w", err)
	}

	for _, file := range paths {
		// The path is on the workspace host, which need not be this
		// machine, so it is handed to the executor as listed.
		if filepath.Base(file) == cfg.command {
			return file, nil
		}
	}
	return "", fmt.Errorf("%s not found in any package directory", cfg.command)
}

// watchSession monitors a session's underlying debug server.
// On clean exit, the session ends and the subscriber is closed
// with reason "terminated". On error exit, the server is
// retried up to cfg.MaxRetries; on success, the sessionID is
// preserved and the subscriber continues to receive events. On
// retries-exhausted, the subscriber is closed with reason
// "adapter_failed".
func (m *Manager) watchSession(
	sessionID string, cfg *debugConfig, srv *debugServer,
) {
	defer srv.closeConn()

	ch := srv.watcher
	if ch == nil {
		m.removeSession(sessionID, srv)
		srv.notifyClose("terminated")
		return
	}

	select {
	case <-m.ctx.Done():
		srv.notifyClose("canceled")
		return
	case err := <-ch:
		if err == nil {
			m.log.Debug("server exited gracefully",
				"session", sessionID, "lang", cfg.langID)
			m.removeSession(sessionID, srv)
			srv.notifyClose("terminated")
			return
		}
		m.log.Warn("server crashed",
			"session", sessionID, "lang", cfg.langID, "error", err)
		srv.mu.Lock()
		stopCalled := srv.stopCalled
		srv.mu.Unlock()
		if stopCalled {
			m.removeSession(sessionID, srv)
			srv.notifyClose("disconnected")
			return
		}
	}

	strategy := retry.CombinedStrategy(
		retry.LimitStrategy(uint(m.cfg.MaxRetries)),
		retry.ExponentialStrategy(500*time.Millisecond, 5*time.Second),
	)

	retryErr := retry.Retry(m.ctx, strategy, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, m.cfg.InitializeTimeout)
		defer cancel()

		m.log.Debug("restarting debug adapter",
			"session", sessionID, "lang", cfg.langID)
		newSrv := newDebugServer(m.ctx, srv.cfg, srv.binPath,
			m.executor, m.rootURI, srv.client, srv.eventSub)

		if err := newSrv.start(ctx); err != nil {
			return true, err
		}
		m.mu.Lock()
		m.sessions[sessionID] = newSrv
		m.mu.Unlock()

		m.wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer m.wg.Done()
			m.watchSession(sessionID, cfg, newSrv)
		})
		return false, nil
	})

	if retryErr != nil {
		m.removeSession(sessionID, srv)
		m.log.Error("debug adapter failed after retries",
			"session", sessionID, "adapter", cfg.command,
			"retries", m.cfg.MaxRetries, "error", retryErr)
		srv.notifyClose("adapter_failed")
	}
}

// removeSession deletes sessionID from the sessions map if it
// is still bound to srv. Does nothing if a retry has already
// swapped in a different server.
func (m *Manager) removeSession(sessionID string, srv *debugServer) {
	m.mu.Lock()
	if m.sessions[sessionID] == srv {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
}

func convertURI(u workspaceapi.URI) string {
	return "file://" + u.Path()
}
