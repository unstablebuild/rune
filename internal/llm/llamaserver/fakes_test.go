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
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// fakeExecutor is a schemeapi.Executor that, instead of exec'ing
// llama-server, binds an in-process HTTP server to the --host/--port the
// pool requested. The server answers GET /health with 200 and
// POST /v1/chat/completions with a minimal OpenAI-compatible JSON body so a
// real openai.Client can drive it, and POST /v1/embeddings with
// deterministic 8-dim vectors. It records started commands and drives the
// process watcher on context cancellation (i.e. kill).
type fakeExecutor struct {
	mu sync.Mutex
	// starts counts StartCommand invocations.
	starts int
	// startArgs records each started command's arguments.
	startArgs [][]string
	// signals records signals sent via Signal.
	signals []syscall.Signal
	// healthDelay delays /health returning 200, simulating model load time.
	healthDelay time.Duration
	// failStart, when set, makes StartCommand return this error.
	failStart error
	// emitLines are written to the command's stderr before serving.
	emitLines []string
	// exitBeforeReady, when set, makes the fake emit its stderr lines and
	// then fire the process watcher without ever serving a healthy
	// /health, simulating a model that crashes during load.
	exitBeforeReady bool
	// running tracks live servers so tests can assert teardown.
	running int
	nextPid workspaceapi.Pid
	// watchers maps pid to the process watcher channel so tests can
	// simulate an unexpected exit (crash) independent of ctx cancellation.
	watchers map[workspaceapi.Pid]chan error
	// embedRequests counts /v1/embeddings requests; embedInputs records
	// each request's inputs.
	embedRequests int
	embedInputs   [][]string
	// embedMaxBytes, when positive, makes /v1/embeddings answer 400 for a
	// request holding an input longer than this, as llama-server does for
	// an input larger than the physical batch.
	embedMaxBytes int
}

func (f *fakeExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	f.mu.Lock()
	f.starts++
	f.startArgs = append(f.startArgs, append([]string(nil), cmd.Args...))
	if f.failStart != nil {
		err := f.failStart
		f.mu.Unlock()
		return 0, err
	}
	f.nextPid++
	pid := f.nextPid
	delay := f.healthDelay
	lines := append([]string(nil), f.emitLines...)
	exitEarly := f.exitBeforeReady
	f.running++
	if f.watchers == nil {
		f.watchers = make(map[workspaceapi.Pid]chan error)
	}
	if cmd.Watcher != nil {
		f.watchers[pid] = cmd.Watcher.WatchProcess()
	}
	f.mu.Unlock()

	// Simulate a model that crashes during load: emit the stderr tail, then
	// fire the process watcher without ever serving /health, so the pool's
	// early-exit path is exercised (no bound HTTP server, no healthy probe).
	if exitEarly {
		if cmd.Stderr != nil {
			for _, line := range lines {
				_, _ = io.WriteString(cmd.Stderr, line+"\n")
			}
		}
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
		if cmd.Watcher != nil {
			go func() {
				select {
				case <-time.After(20 * time.Millisecond):
					select {
					case cmd.Watcher.WatchProcess() <- nil:
					case <-ctx.Done():
					}
				case <-ctx.Done():
				}
			}()
		}
		return pid, nil
	}

	host := argValue(cmd.Args, "--host")
	port := argValue(cmd.Args, "--port")
	addr := net.JoinHostPort(host, port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
		return 0, err
	}

	start := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if time.Since(start) < delay {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		// Serve a minimal OpenAI-compatible SSE stream so a real openai
		// client parses text deltas and a terminal [DONE].
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, chunk := range chatCompletionSSEChunks {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/v1/embeddings", f.serveEmbeddings)
	srv := &http.Server{Handler: mux} // #nosec G112 -- test-only server

	if cmd.Stderr != nil {
		for _, line := range lines {
			_, _ = io.WriteString(cmd.Stderr, line+"\n")
		}
	}

	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
		if cmd.Watcher != nil {
			select {
			case cmd.Watcher.WatchProcess() <- nil:
			default:
			}
		}
	}()
	return pid, nil
}

func (f *fakeExecutor) Signal(_ workspaceapi.Pid, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, sig)
	return nil
}

func (f *fakeExecutor) serveEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.embedRequests++
	f.embedInputs = append(f.embedInputs, req.Input)
	limit := f.embedMaxBytes
	f.mu.Unlock()
	type datum struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]datum, len(req.Input))
	for i, text := range req.Input {
		if limit > 0 && len(text) > limit {
			http.Error(w, `{"error":{"message":"input is too large to process"}}`, http.StatusBadRequest)
			return
		}
		// Reverse order so callers must sort by index.
		data[len(req.Input)-1-i] = datum{Index: i, Embedding: fakeVector(text)}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// fakeVector is a deterministic unit vector of 8 dims derived from text.
func fakeVector(text string) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum64()
	v := make([]float32, 8)
	var sum float64
	for i := range v {
		seed = seed*6364136223846793005 + 1442695040888963407
		v[i] = float32(int64(seed>>33)%1000+1) / 1000
		sum += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(sum))
	}
	return v
}

func (f *fakeExecutor) embedStats() (int, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.embedRequests, append([][]string(nil), f.embedInputs...)
}

func (*fakeExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (f *fakeExecutor) Close() error { return nil }

func (f *fakeExecutor) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeExecutor) runningCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// crash simulates an unexpected process exit for pid: it signals the process
// watcher without cancelling the lifecycle context, exercising the pool's
// crash-eviction path.
func (f *fakeExecutor) crash(pid workspaceapi.Pid) {
	f.mu.Lock()
	ch := f.watchers[pid]
	f.mu.Unlock()
	if ch != nil {
		select {
		case ch <- nil:
		default:
		}
	}
}

// chatCompletionSSEChunks is a minimal OpenAI-compatible chat-completion SSE
// stream: one content delta then a stop chunk and [DONE].
var chatCompletionSSEChunks = []string{
	"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"local\"," +
		"\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}," +
		"\"finish_reason\":null}]}\n\n",
	"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"local\"," +
		"\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
	"data: [DONE]\n\n",
}

// recordingNotifications records every Notify / progress update so tests can
// assert startup progress reporting.
type recordingNotifications struct {
	mu       sync.Mutex
	notifies []notifyRecord
	progress []progressRecord
	nextID   int
}

type notifyRecord struct {
	level browserapi.NotificationLevel
	msg   string
}

type progressRecord struct {
	id              string
	message         string
	progress, total int64
}

func (r *recordingNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	r.notifies = append(r.notifies, notifyRecord{
		level: level, msg: fmt.Sprintf(msg, args...),
	})
	return "id", nil
}

func (r *recordingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return r.Notify(level, msg, args...)
}

func (r *recordingNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, progressRecord{
		id: id, message: message, progress: progress, total: total,
	})
	return nil
}

func (r *recordingNotifications) levels() []browserapi.NotificationLevel {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]browserapi.NotificationLevel, len(r.notifies))
	for i, n := range r.notifies {
		out[i] = n.level
	}
	return out
}

// errorMessages returns the formatted message of every error-level Notify, in
// order, so tests can assert what failure the user saw.
func (r *recordingNotifications) errorMessages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, n := range r.notifies {
		if n.level == browserapi.LevelError {
			out = append(out, n.msg)
		}
	}
	return out
}

func (r *recordingNotifications) progressRecords() []progressRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]progressRecord(nil), r.progress...)
}

// progressMessages returns the non-empty message of every progress update, in
// order, so tests can assert which server output lines reached the UI.
func (r *recordingNotifications) progressMessages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.progress {
		if rec.message != "" {
			out = append(out, rec.message)
		}
	}
	return out
}

// testModel builds a pool model with a unique path per name.
func testModel(name string) model {
	return model{name: name, modelPath: "/models/" + name + ".gguf"}
}
