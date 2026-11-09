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
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/openai"
)

// healthPollInterval is how often waitReady polls GET /health while a server
// loads its model.
const healthPollInterval = 200 * time.Millisecond

// serverProcess is one running llama-server. Its lifecycle context is derived
// once at construction and cancelled by stop() or the idle timer; the
// per-request context passed to CreateCompletion never tears the process down.
type serverProcess struct {
	svc     *Service
	key     serverKey
	model   model
	binPath string
	baseURL string

	ctx    context.Context
	cancel context.CancelFunc

	// client is the OpenAI-compatible client bound to this server's baseURL.
	// It is set once the server is ready.
	client llmapi.Service

	mu         sync.Mutex
	started    bool
	ready      bool
	readyErr   error
	exited     bool
	inflight   int
	last       time.Time
	idleTimer  *time.Timer
	progressID string
	loadPct    int
	stderrTail []string
	readyCh    chan struct{}
}

// newServerProcess constructs (but does not start) a serverProcess.
func newServerProcess(svc *Service, m model, binPath string) *serverProcess {
	ctx, cancel := context.WithCancel(context.Background())
	return &serverProcess{
		svc:     svc,
		key:     m.key(),
		model:   m,
		binPath: binPath,
		ctx:     ctx,
		cancel:  cancel,
		last:    time.Now(),
		readyCh: make(chan struct{}),
	}
}

// acquire increments in-flight and cancels any pending idle timer. Callers
// hold pool.mu.
func (s *serverProcess) acquire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight++
	s.last = time.Now()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
}

// release decrements in-flight and, at zero, arms the idle timer. Callers
// hold pool.mu.
func (s *serverProcess) release(idle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight > 0 {
		s.inflight--
	}
	s.last = time.Now()
	if s.inflight == 0 && !s.exited {
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		s.idleTimer = time.AfterFunc(idle, func() {
			s.svc.removeIfPresent(s)
			s.stop()
		})
	}
}

func (s *serverProcess) inFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight
}

func (s *serverProcess) lastUsed() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *serverProcess) markExited() {
	s.mu.Lock()
	s.exited = true
	earlyExit := !s.ready && s.readyErr == nil
	tail := append([]string(nil), s.stderrTail...)
	s.mu.Unlock()

	// A process that exits before ready is a load failure. Route it through
	// fail() so the user sees the error immediately instead of waiting out
	// the startup timeout while pollHealth probes a dead process.
	if earlyExit {
		s.fail(fmt.Errorf("llamaserver: %s", loadFailureMessage(s.model.name, tail)))
		return
	}
	s.mu.Lock()
	s.signalReadyLocked()
	s.mu.Unlock()
}

// stop cancels the lifecycle context (killing the child) and stops the idle
// timer. Idempotent.
func (s *serverProcess) stop() {
	s.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.mu.Unlock()
	s.cancel()
}

// signalReadyLocked closes readyCh once. Callers hold s.mu.
func (s *serverProcess) signalReadyLocked() {
	select {
	case <-s.readyCh:
	default:
		close(s.readyCh)
	}
}

// waitReady starts the server on first call and blocks until it reports
// healthy, the startup timeout elapses, or ctx is cancelled.
func (s *serverProcess) waitReady(ctx context.Context) error {
	s.mu.Lock()
	if s.ready {
		s.mu.Unlock()
		return nil
	}
	if s.readyErr != nil {
		err := s.readyErr
		s.mu.Unlock()
		return err
	}
	if !s.started {
		s.started = true
		s.mu.Unlock()
		s.start()
	} else {
		s.mu.Unlock()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readyCh:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.readyErr != nil {
			return s.readyErr
		}
		return nil
	}
}

// start allocates a port, launches the process, and spawns the readiness /
// progress goroutine. It never blocks the caller past the exec launch.
func (s *serverProcess) start() {
	host := s.svc.cfg.Host
	port, err := allocatePort(host)
	if err != nil {
		s.fail(err)
		return
	}
	s.baseURL = fmt.Sprintf("http://%s/v1", net.JoinHostPort(host, strconv.Itoa(port)))
	s.client = openai.NewClient("-", openai.Config{
		BaseURL:   s.baseURL,
		DebugHTTP: false,
	})

	pr, pw := io.Pipe()
	stderr := &pipeWriter{w: pw}
	watcher := make(chan error, 1)
	cmd := s.svc.buildCmd(s.binPath, s.model, port, watcher, stderr)

	s.mu.Lock()
	s.progressID = s.svc.notify(browserapi.LevelInfo,
		"Starting local model %s…", s.model.name)
	s.mu.Unlock()

	// Start draining stderr before launching so the child's early load
	// output never blocks on a pipe with no reader.
	s.scanOutput(pr)
	if _, err := s.svc.exec.StartCommand(s.ctx, cmd); err != nil {
		// Unblock and terminate the scanner goroutine.
		_ = pw.Close()
		s.fail(fmt.Errorf("llamaserver: start %s: %w", s.binPath, err))
		return
	}
	s.svc.spawnWatcher(s, watcher)
	s.pollHealth(host, port)
}

// fail records a readiness error, finishes the starting notification,
// which otherwise stays open, and wakes waitReady.
func (s *serverProcess) fail(err error) {
	s.mu.Lock()
	firstWriter := s.readyErr == nil
	if firstWriter {
		s.readyErr = err
	}
	id := s.progressID
	s.signalReadyLocked()
	s.mu.Unlock()
	if firstWriter {
		log.Errorf("%v", err)
	}
	// Only the first writer notifies so a later pollHealth timeout can't
	// double-report an already-surfaced early-exit failure.
	if firstWriter && id != "" {
		s.svc.updateProgress(id, "Could not start local model "+s.model.name, 100, 100)
		_, _ = s.svc.notis.Notify(browserapi.LevelError, "%s", err.Error())
	}
	s.cancel()
}

// markReady records readiness, finalizes the progress notification, and wakes
// waitReady.
func (s *serverProcess) markReady() {
	s.mu.Lock()
	if s.ready || s.readyErr != nil {
		s.mu.Unlock()
		return
	}
	s.ready = true
	id := s.progressID
	s.signalReadyLocked()
	s.mu.Unlock()
	if id != "" {
		s.svc.updateProgress(id, "", 100, 100)
		_, _ = s.svc.notis.Notify(browserapi.LevelSuccess,
			"Local model %s ready", s.model.name)
	}
}

// scanOutput streams the merged stdout/stderr pipe line-by-line to surface
// load progress and keep a tail for error reporting. It also closes pr when
// the lifecycle context is cancelled so the scanner goroutine never leaks
// after the process is stopped.
func (s *serverProcess) scanOutput(pr *io.PipeReader) {
	go debug.CapturePanicReport(func() {
		<-s.ctx.Done()
		_ = pr.Close()
	})
	go debug.CapturePanicReport(func() {
		defer func() { _ = pr.Close() }()
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			log.Debugf("llamaserver: %s: %s", s.model.name, line)
			s.recordLine(line)
			s.mu.Lock()
			if pct, ok := parseLoadProgress(line); ok {
				s.loadPct = pct
			}
			id, pct := s.progressID, s.loadPct
			s.mu.Unlock()
			// Stream every load line as the progress message, keeping the
			// last-seen percentage as the bar value; total stays at 100 so
			// the notification remains an in-flight progress bar.
			s.svc.updateProgress(id, line, int64(pct), 100)
		}
	})
}

// recordLine appends line to the bounded stderr tail used in failure reports.
func (s *serverProcess) recordLine(line string) {
	const maxTail = 20
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stderrTail = append(s.stderrTail, line)
	if len(s.stderrTail) > maxTail {
		s.stderrTail = s.stderrTail[len(s.stderrTail)-maxTail:]
	}
}

// pollHealth blocks until GET /health returns 200, the startup timeout
// elapses, or the lifecycle context is cancelled, then records the outcome.
func (s *serverProcess) pollHealth(host string, port int) {
	go debug.CapturePanicReport(func() {
		url := fmt.Sprintf("http://%s/health", net.JoinHostPort(host, strconv.Itoa(port)))
		deadline := time.Now().Add(s.svc.cfg.StartupTimeout)
		ticker := time.NewTicker(healthPollInterval)
		defer ticker.Stop()
		for {
			if s.healthy(url) {
				s.markReady()
				return
			}
			// If the process already exited, markExited has surfaced (or
			// will surface) the load failure; stop probing a dead process
			// rather than waiting out the startup timeout.
			s.mu.Lock()
			exited := s.exited
			s.mu.Unlock()
			if exited {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if time.Now().After(deadline) {
					s.fail(fmt.Errorf(
						"llamaserver: model %s not ready after %s",
						s.model.name, s.svc.cfg.StartupTimeout))
					return
				}
			}
		}
	})
}

// healthy issues a single GET /health and reports whether it returned 200.
func (s *serverProcess) healthy(url string) bool {
	ctx, cancel := context.WithTimeout(s.ctx, healthPollInterval)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := s.svc.health.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// completionClient returns the ready OpenAI-compatible client for this server.
func (s *serverProcess) completionClient() llmapi.Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// embeddingsURL returns the ready server's OpenAI-compatible embeddings
// endpoint.
func (s *serverProcess) embeddingsURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL + "/embeddings"
}

// pipeWriter adapts an *io.PipeWriter to workspaceapi.Cmd's io.Writer stdout /
// stderr fields.
type pipeWriter struct {
	w *io.PipeWriter
}

func (p *pipeWriter) Write(b []byte) (int, error) { return p.w.Write(b) }

// parseLoadProgress extracts a 0..100 percentage from a llama-server load
// line. Returns ok=false for lines with no recognizable percentage.
func parseLoadProgress(line string) (int, bool) {
	idx := strings.LastIndexByte(line, '%')
	if idx <= 0 {
		return 0, false
	}
	start := idx - 1
	for start >= 0 && (line[start] == '.' || (line[start] >= '0' && line[start] <= '9')) {
		start--
	}
	numStr := line[start+1 : idx]
	if numStr == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, false
	}
	pct := int(f)
	pct = max(pct, 0)
	pct = min(pct, 100)
	return pct, true
}

// loadFailureMessage builds a concise, cause-first failure message for a
// model that exited before becoming ready. It leads with the salient
// llama.cpp error and appends the raw stderr tail below only when it adds
// detail beyond that headline.
func loadFailureMessage(name string, tail []string) string {
	salient := summarizeLoadFailure(tail)
	msg := fmt.Sprintf("failed to load model %s: %s", name, salient)
	joined := strings.TrimSpace(strings.Join(tail, "\n"))
	if joined != "" && joined != salient {
		msg = fmt.Sprintf("%s\n%s", msg, joined)
	}
	return msg
}

// loadFailureMarkers are salient llama.cpp load-failure lines in priority
// order: the earliest match becomes the failure headline.
var loadFailureMarkers = []string{
	"error loading model",
	"missing tensor",
	"unknown model architecture",
	"failed to load model",
	"error:",
}

// summarizeLoadFailure scans a stderr tail for the most specific llama.cpp
// load-failure line and returns it as the headline. When no marker matches it
// falls back to the last non-empty line, or a generic message for an empty
// tail.
func summarizeLoadFailure(tail []string) string {
	for _, marker := range loadFailureMarkers {
		for _, line := range tail {
			if strings.Contains(strings.ToLower(line), marker) {
				return strings.TrimSpace(line)
			}
		}
	}
	for i := len(tail) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(tail[i]); line != "" {
			return line
		}
	}
	return "server exited before ready"
}
