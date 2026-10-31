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

package llamaserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/openai"
)

// ErrServerClosed is returned by CreateCompletion after the Service has been
// closed.
var ErrServerClosed = errors.New("llamaserver: service closed")

// Service implements llmapi.Service by delegating each local-model completion
// to a managed llama-server subprocess. It owns one llama-server process per
// distinct resolved model: processes start on demand, shut down after an idle
// timeout, and are LRU-evicted past the configured MaxServers cap. Models() /
// GetModel() are implemented for completeness but the router supplies the
// local catalog from the llamacpp registry, so they see no traffic in
// production.
type Service struct {
	cfg     Config
	exec    schemeapi.Executor
	locator BinaryLocator
	notis   browserapi.Notifications
	health  httpDoer
	// tokenizer provides an offline tiktoken-based CountTokens estimate. It
	// never issues network requests.
	tokenizer llmapi.Service

	mu      sync.Mutex
	servers map[serverKey]*serverProcess
	closed  bool
}

// CanGenerateImages reports false for all local models without starting a server.
func (s *Service) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

// GenerateImage returns ErrImageGenerationUnsupported without starting a server.
func (s *Service) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

// New constructs a Service. It panics if any dependency is nil, matching the
// no-nil-constructor-dependency convention: the "server not installed" state
// is represented by the locator returning ErrServerNotInstalled at
// CreateCompletion time, never by a nil dependency.
func New(
	cfg Config,
	exec schemeapi.Executor,
	locator BinaryLocator,
	notis browserapi.Notifications,
) *Service {
	if exec == nil {
		panic("llamaserver: New: exec must not be nil")
	}
	if locator == nil {
		panic("llamaserver: New: locator must not be nil")
	}
	if notis == nil {
		panic("llamaserver: New: notis must not be nil")
	}
	return &Service{
		cfg:       cfg.withDefaults(),
		exec:      exec,
		locator:   locator,
		notis:     notis,
		health:    &http.Client{},
		tokenizer: openai.NewClient("-", openai.Config{}),
		servers:   make(map[serverKey]*serverProcess),
	}
}

// CreateCompletion acquires (starting if needed) the server for entry, then
// delegates to its OpenAI-compatible client. The returned iterator releases
// the server reference on Close. ErrServerNotInstalled becomes the install
// instruction.
func (s *Service) CreateCompletion(
	ctx context.Context, entry llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	proc, release, err := s.acquire(ctx, modelFromEntry(entry))
	if err != nil {
		if errors.Is(err, ErrServerNotInstalled) {
			return nil, errors.New(installMessage)
		}
		return nil, err
	}
	it, err := proc.completionClient().CreateCompletion(ctx, entry, req)
	if err != nil {
		release()
		return nil, err
	}
	return &releasingIterator{Iterator: it, release: release}, nil
}

// CountTokens returns the offline tiktoken estimate. An exact count would
// require the loaded model's tokenizer; the router never depends on an exact
// local count.
func (s *Service) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	return s.tokenizer.CountTokens(model, msgs)
}

// Models returns an empty catalog: the router aggregates the llamacpp
// registry for local model discovery.
func (s *Service) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice[llmapi.ModelEntry](nil)
}

// GetModel echoes entry back with the local provider set. The router resolves
// the canonical entry from the registry before dispatch, so this is only a
// fallback for direct callers.
func (s *Service) GetModel(
	_ context.Context, entry llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	if entry.Name == "" {
		return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
	}
	entry.Provider = LLMProvider
	return entry, nil
}

// Close stops every running server and waits for their processes to exit.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	procs := make([]*serverProcess, 0, len(s.servers))
	for k, proc := range s.servers {
		delete(s.servers, k)
		procs = append(procs, proc)
	}
	s.mu.Unlock()
	for _, proc := range procs {
		proc.stop()
	}
	return nil
}

// installMessage is surfaced to the user when a local model is requested but
// the llama-server binary is not installed on the workspace host.
const installMessage = "install llama-server via the console " +
	"(`pkg install llama-server`), or point Rune at your own server " +
	"binary with the `models.local.server_bin_path` config, before " +
	"running a local model"

// serverKey uniquely identifies a running server by the model configuration
// that started it. Two acquire calls with the same key share one process.
type serverKey struct {
	modelPath     string
	projectorPath string
	contextWindow uint32
}

// model carries the per-request model identity resolved from the registry.
type model struct {
	name          string
	modelPath     string
	projectorPath string
	contextWindow uint32
}

func (m model) key() serverKey {
	return serverKey{
		modelPath:     m.modelPath,
		projectorPath: m.projectorPath,
		contextWindow: m.contextWindow,
	}
}

// httpDoer is the subset of http.Client the service uses for health probes.
// It is a field so tests can point health checks at a stub transport.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// modelFromEntry maps a resolved registry ModelEntry onto the internal model
// identity. BaseURL carries the GGUF blob path; ProjectorPath the mmproj.
func modelFromEntry(entry llmapi.ModelEntry) model {
	return model{
		name:          entry.Name,
		modelPath:     entry.BaseURL,
		projectorPath: entry.ProjectorPath,
		contextWindow: uint32(entry.ContextWindow), // #nosec G115 -- context windows fit in uint32
	}
}

// acquire returns a ready server for m, starting one if necessary, and a
// release func the caller must invoke exactly once when done. release
// decrements the in-flight count and re-arms the idle timer at zero.
func (s *Service) acquire(
	ctx context.Context, m model,
) (*serverProcess, func(), error) {
	binPath, err := s.locator.Locate(ctx)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrServerClosed
	}
	key := m.key()
	proc, ok := s.servers[key]
	if !ok {
		if err := s.evictForRoomLocked(); err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		proc = newServerProcess(s, m, binPath)
		s.servers[key] = proc
	}
	proc.acquire()
	s.mu.Unlock()

	if err := proc.waitReady(ctx); err != nil {
		release := s.releaseFunc(proc)
		release()
		return nil, nil, err
	}
	return proc, s.releaseFunc(proc), nil
}

// releaseFunc returns a one-shot release for proc.
func (s *Service) releaseFunc(proc *serverProcess) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.closed {
				return
			}
			proc.release(s.cfg.IdleTimeout)
		})
	}
}

// evictForRoomLocked stops the least-recently-used idle server when adding a
// new one would exceed MaxServers. Callers hold s.mu.
func (s *Service) evictForRoomLocked() error {
	if len(s.servers) < s.cfg.MaxServers {
		return nil
	}
	var victim *serverProcess
	for _, proc := range s.servers {
		if proc.inFlight() > 0 {
			continue
		}
		if victim == nil || proc.lastUsed().Before(victim.lastUsed()) {
			victim = proc
		}
	}
	if victim == nil {
		return fmt.Errorf(
			"llamaserver: all %d servers busy; cannot start another",
			s.cfg.MaxServers,
		)
	}
	delete(s.servers, victim.key)
	victim.stop()
	return nil
}

// removeIfPresent drops proc from the registry if it is still the registered
// server for its key. Called by the idle timer and crash watcher.
func (s *Service) removeIfPresent(proc *serverProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servers[proc.key] == proc {
		delete(s.servers, proc.key)
	}
}

// allocatePort asks the OS for a free TCP port on host by binding :0 and
// reading back the assigned port. There is a small TOCTOU window between
// closing the listener and llama-server binding it; startup retries once on
// failure to absorb a lost race.
func allocatePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("llamaserver: allocate port: %w", err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("llamaserver: unexpected listener addr %T", l.Addr())
	}
	return addr.Port, nil
}

// buildCmd assembles the workspaceapi.Cmd for a server process. stdout/stderr
// are wired to pipes the caller streams for readiness/progress; watcher
// receives the process exit.
func (s *Service) buildCmd(
	binPath string, m model, port int, watcher chan error, stderr *pipeWriter,
) workspaceapi.Cmd {
	args := s.cfg.serverArgs(
		m.modelPath, m.projectorPath, m.contextWindow, s.cfg.Host, port,
	)
	return workspaceapi.Cmd{
		Path:    binPath,
		Args:    args,
		Stdout:  stderr,
		Stderr:  stderr,
		Watcher: workspaceapi.ChanProcessWatcher(watcher),
	}
}

// spawnWatcher runs a goroutine that evicts proc from the registry when its
// process exits unexpectedly so the next acquire restarts it fresh.
func (s *Service) spawnWatcher(proc *serverProcess, watcher chan error) {
	go debug.CapturePanicReport(func() {
		select {
		case <-proc.ctx.Done():
			return
		case <-watcher:
			proc.markExited()
			s.removeIfPresent(proc)
		}
	})
}

// notify is a nil-safe wrapper over the service's notifications sink.
func (s *Service) notify(
	level browserapi.NotificationLevel, format string, args ...any,
) string {
	id, _ := s.notis.Notify(level, format, args...)
	return id
}

// updateProgress forwards a progress update; total is clamped to a positive
// value so the sink's total!=0 contract is never violated.
func (s *Service) updateProgress(id, msg string, progress, total int64) {
	if id == "" || total <= 0 {
		return
	}
	if progress > total {
		progress = total
	}
	_ = s.notis.UpdateNotificationProgress(id, msg, progress, total)
}

// releasingIterator wraps a completion iterator so the server reference is
// released exactly once when the caller closes the stream.
type releasingIterator struct {
	iterator.Iterator[llmapi.Event]
	release func()
}

func (r *releasingIterator) Close() error {
	err := r.Iterator.Close()
	r.release()
	return err
}

var _ llmapi.Service = (*Service)(nil)
