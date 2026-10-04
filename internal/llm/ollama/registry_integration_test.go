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

package ollama_test

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"unstable.build/rune/internal/llm/ollama"
)

func TestIntegrationRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ollamaBin, err := exec.LookPath("ollama")
	if err != nil {
		t.Skip("ollama binary not found; skipping integration test")
	}

	// Use a non-default port so we don't conflict with a user's
	// running instance.
	const port = "19434"
	baseURL := "http://127.0.0.1:" + port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, ollamaBin, "serve")
	cmd.Env = append(cmd.Environ(), "OLLAMA_HOST=127.0.0.1:"+port)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ollama serve: %v", err)
	}
	defer func() {
		cancel()
		cmd.Wait() //nolint:errcheck
	}()

	// Wait for the server to become ready via a direct HTTP probe.
	reg := ollama.NewRegistry(baseURL)
	var ready bool
	client := &http.Client{Timeout: time.Second}
	for range 30 {
		resp, err := client.Get(baseURL + "/api/tags")
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("ollama server did not become ready in time")
	}

	// Verify Models returns entries.
	models := collectModels(t, reg.Models())
	t.Logf("ollama returned %d models", len(models))
	for _, m := range models {
		if m.Name == "" {
			t.Error("model entry has empty name")
		}
		if m.Provider != ollama.LLMProvider {
			t.Errorf("expected provider %q, got %q", ollama.LLMProvider, m.Provider)
		}
		if m.BaseURL != baseURL+"/v1/" {
			t.Errorf("unexpected base URL %q", m.BaseURL)
		}
		if m.ContextWindow == 0 {
			t.Errorf("model %q has zero context window", m.Name)
		}
	}

	// If models are available, verify Get works.
	if len(models) > 0 {
		name := models[0].Name
		e, ok := reg.Get(ctx, name)
		if !ok {
			t.Fatalf("Get(%q) returned false for a model from Models()", name)
		}
		if e.Name != name {
			t.Fatalf("Get returned wrong name: %q vs %q", e.Name, name)
		}
	}

	// Verify Get returns false for a model that doesn't exist.
	_, ok := reg.Get(ctx, "this-model-does-not-exist-"+fmt.Sprint(time.Now().UnixNano()))
	if ok {
		t.Fatal("Get returned true for a non-existent model")
	}

	// Sanity: registry continues to function.
	_ = reg
}
