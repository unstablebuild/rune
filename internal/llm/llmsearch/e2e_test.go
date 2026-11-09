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

package llmsearch

// End-to-end suite that runs EmbeddingGemma on a real `llama-server`.
//
// It runs only when RUNE_LLAMASERVER_E2E=1 and `llama-server` is on $PATH
// (or RUNE_LLAMASERVER_E2E_BIN names it). The model is pulled once into
// ${UserCacheDir}/rune/llmsearch-e2e; with RUNE_LLAMASERVER_E2E_OFFLINE=1
// the suite never downloads and skips without a cached model.
//
// testdata/reference_vectors.json holds DigUp's reference vectors, made by
// llama.cpp in process from the same GGUF. When a llama-server update
// moves the vectors, bump llamaRuntime (every index embeds again) and
// write the reference anew with RUNE_LLMSEARCH_WRITE_REFERENCE=1.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llamaserver"
	"unstable.build/rune/internal/workspace"
)

// referenceAgreement is the least cosine a vector may have with its
// reference: less is a change people would see in their results.
const referenceAgreement = 0.99

// e2eEmbedding returns an embedding server pool and a registry holding
// the embedding model.
func e2eEmbedding(t *testing.T) (*llamaserver.Service, *llamacpp.Registry, llmapi.ModelEntry) {
	t.Helper()
	if os.Getenv("RUNE_LLAMASERVER_E2E") != "1" {
		t.Skip("llmsearch e2e: set RUNE_LLAMASERVER_E2E=1 to run")
	}
	bin := os.Getenv("RUNE_LLAMASERVER_E2E_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("llama-server"); err != nil {
			t.Skip("llmsearch e2e: llama-server not found on $PATH (set RUNE_LLAMASERVER_E2E_BIN to override)")
		}
	}
	base, err := os.UserCacheDir()
	require.NoError(t, err)
	reg, err := llamacpp.NewRegistry(filepath.Join(base, "rune", "llmsearch-e2e"))
	require.NoError(t, err)
	ctx := context.Background()
	entry, ok := reg.Get(ctx, testModel)
	if !ok {
		if os.Getenv("RUNE_LLAMASERVER_E2E_OFFLINE") == "1" {
			t.Skipf("llmsearch e2e: offline and %s is not cached in %s", testModel, reg.Root())
		}
		ref, err := llamacpp.ParseReference(testModel)
		require.NoError(t, err)
		t.Logf("llmsearch e2e: downloading %s into %s (first run only)", testModel, reg.Root())
		dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		if _, err := reg.Download(dctx, ref, nil); err != nil {
			t.Skipf("llmsearch e2e: download %s: %v", testModel, err)
		}
		entry, ok = reg.Get(ctx, testModel)
		require.True(t, ok)
	}

	runner := &realExecutor{}
	t.Cleanup(runner.wait)
	svc := llamaserver.New(llamaserver.Config{
		ContextWindow: 2048, BatchSize: 2048, NGPULayers: -1, MaxServers: 1,
		StartupTimeout: 3 * time.Minute, IdleTimeout: time.Hour,
	}, runner, llamaserver.NewFixedLocator(bin), &recordingNotifications{})
	t.Cleanup(func() { _ = svc.Close() })
	return svc, reg, entry
}

type referenceVectors struct {
	MadeWith string `json:"made_with"`
	Vectors  []struct {
		Name   string    `json:"name"`
		Text   string    `json:"text"`
		Vector []float32 `json:"vector"`
	} `json:"vectors"`
}

func TestE2E_ReferenceVectors(t *testing.T) {
	svc, _, entry := e2eEmbedding(t)
	path := filepath.Join("testdata", "reference_vectors.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var ref referenceVectors
	require.NoError(t, json.Unmarshal(data, &ref))
	texts := make([]string, len(ref.Vectors))
	for i, v := range ref.Vectors {
		texts[i] = v.Text
	}

	made, err := NewLlamaEmbedderFactory(svc)(entry).EmbedTexts(context.Background(), texts)
	require.NoError(t, err)
	require.Len(t, made, len(texts))

	if os.Getenv("RUNE_LLMSEARCH_WRITE_REFERENCE") == "1" {
		ref.MadeWith = "llama-server (" + llamaRuntime + "), " + testModel
		for i := range ref.Vectors {
			ref.Vectors[i].Vector = made[i]
		}
		out, err := json.MarshalIndent(ref, "", " ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(out, '\n'), 0o644))
		return
	}
	for i, v := range ref.Vectors {
		require.Len(t, made[i], dims, v.Name)
		cosine := 0.0
		for j := range made[i] {
			cosine += float64(made[i][j] * v.Vector[j])
		}
		t.Logf("%s: cosine %.5f", v.Name, cosine)
		assert.GreaterOrEqual(t, cosine, referenceAgreement,
			"%s moved from the reference (%s): bump llamaRuntime and write the reference anew", v.Name, ref.MadeWith)
	}
}

type codeEvals struct {
	Queries []struct {
		Q      string   `json:"q"`
		Expect []string `json:"expect"`
		Focus  string   `json:"focus"`
	} `json:"queries"`
}

func TestE2E_CodeEvals(t *testing.T) {
	svc, reg, _ := e2eEmbedding(t)
	ctx := context.Background()
	data, err := os.ReadFile(filepath.Join("testdata", "evals", "code.json"))
	require.NoError(t, err)
	var evals codeEvals
	require.NoError(t, json.Unmarshal(data, &evals))

	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	fsys, err := workspace.NewFileScheme(ctx, config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsys.Close() })
	opts := DefaultOptions()
	opts.ExcludedFolders = []string{"testdata"}
	search := New(Config{Enabled: true, Model: testModel, Options: opts}, NewLlamaEmbedderFactory(svc), reg, nopCloser{})
	t.Cleanup(func() { _ = search.Close() })
	idx, err := search.OpenIndex(IndexConfig{Root: root, FS: fsys, DB: storagestub.NewInMemoryService(),
		Notify: &recordingNotifications{}, Schedule: syncTick, Options: opts})
	require.NoError(t, err)
	wctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	require.NoError(t, idx.Wait(wctx))
	st := idx.Status()
	require.Empty(t, st.Stalled)
	require.Positive(t, st.Vectors)

	model, err := search.EmbeddingModel(ctx)
	require.NoError(t, err)
	reciprocal := 0.0
	for _, q := range evals.Queries {
		it, st, err := search.Search(ctx, root, searchapi.SemanticRequest{Query: q.Q, Model: model, Limit: 5, Meaning: true})
		require.NoError(t, err)
		require.Zero(t, st, "a finished index with the model answers by meaning")
		hits, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		rank := 0
		for i, h := range hits {
			if slices.Contains(q.Expect, h.Path) {
				rank = i + 1
				if q.Focus != "" {
					assert.Contains(t, lineAt(t, filepath.Join(dir, h.Path), h.Focus), q.Focus, q.Q)
				}
				break
			}
		}
		t.Logf("%-70q rank %d of %v", q.Q, rank, paths(hits))
		if !assert.Positive(t, rank, "%q: none of %v in the top 5", q.Q, q.Expect) {
			continue
		}
		reciprocal += 1 / float64(rank)
	}
	t.Logf("MRR %.3f over %d queries", reciprocal/float64(len(evals.Queries)), len(evals.Queries))
}

// lineAt is line n (from 1) of the file at path.
func lineAt(t *testing.T, path string, n int) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	s.Buffer(nil, 1<<20)
	for i := 1; s.Scan(); i++ {
		if i == n {
			return s.Text()
		}
	}
	require.NoError(t, s.Err())
	return ""
}

// realExecutor is a minimal os/exec schemeapi.Executor: it starts real
// child processes (llama-server), kills them when the lifecycle context
// ends, and reports each exit to the command's Watcher.
type realExecutor struct {
	mu    sync.Mutex
	next  workspaceapi.Pid
	procs map[workspaceapi.Pid]*exec.Cmd
	wg    sync.WaitGroup
}

func (e *realExecutor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.Command(cmd.Path, cmd.Args...) // #nosec G204 -- test-only, args from our config
	c.Dir, c.Env = cmd.Dir, cmd.Env
	c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return 0, err
	}
	e.mu.Lock()
	if e.procs == nil {
		e.procs = map[workspaceapi.Pid]*exec.Cmd{}
	}
	e.next++
	pid := e.next
	e.procs[pid] = c
	e.mu.Unlock()

	var watch chan error
	if cmd.Watcher != nil {
		watch = cmd.Watcher.WatchProcess()
	}
	e.wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer e.wg.Done()
		exited := make(chan error, 1)
		go debug.CapturePanicReport(func() { exited <- c.Wait() })
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			<-exited
		case err := <-exited:
			if watch != nil {
				select {
				case watch <- err:
				default:
				}
			}
		}
		e.mu.Lock()
		delete(e.procs, pid)
		e.mu.Unlock()
	})
	return pid, nil
}

func (e *realExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	e.mu.Lock()
	c := e.procs[pid]
	e.mu.Unlock()
	if c == nil || c.Process == nil {
		return nil
	}
	return c.Process.Signal(sig)
}

func (*realExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (*realExecutor) Close() error { return nil }

// wait kills what is still running and waits for every process to exit.
func (e *realExecutor) wait() {
	e.mu.Lock()
	for _, c := range e.procs {
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	}
	e.mu.Unlock()
	e.wg.Wait()
}
