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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

func TestService_Acquire_FailsFastOnEarlyExit(t *testing.T) {
	exec := &fakeExecutor{
		exitBeforeReady: true,
		emitLines: []string{
			"llama_model_loader: loaded meta data",
			"load_tensors: loading model tensors",
			"error loading model: missing tensor 'blk.64.ssm_conv1d.weight'",
		},
	}
	// A deliberately long startup timeout proves the failure surfaces via the
	// early-exit path, not by waiting out the deadline.
	svc, notis := newTestService(t, exec, Config{StartupTimeout: 2 * time.Minute})

	start := time.Now()
	_, _, err := svc.acquire(context.Background(), testModel("qwen"))
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 10*time.Second,
		"early exit must fail fast, not wait out the startup timeout")
	assert.Contains(t, err.Error(), "failed to load model qwen")
	assert.Contains(t, err.Error(),
		"missing tensor 'blk.64.ssm_conv1d.weight'")

	require.Eventually(t, func() bool {
		return len(notis.errorMessages()) >= 1
	}, 2*time.Second, 10*time.Millisecond, "an error notification is emitted")
	msgs := notis.errorMessages()
	require.Len(t, msgs, 1, "exactly one error notification")
	assert.Contains(t, msgs[0], "failed to load model qwen")
	assert.Contains(t, msgs[0], "missing tensor 'blk.64.ssm_conv1d.weight'")
	assert.NotContains(t, notis.levels(), browserapi.LevelSuccess)
}

func TestService_Acquire_EarlyExit_NoDoubleNotify(t *testing.T) {
	exec := &fakeExecutor{
		exitBeforeReady: true,
		emitLines:       []string{"error loading model: bad file"},
	}
	// A very short timeout invites the pollHealth deadline to also fire.
	svc, notis := newTestService(t, exec, Config{StartupTimeout: 50 * time.Millisecond})

	_, _, err := svc.acquire(context.Background(), testModel("m"))
	require.Error(t, err)

	// Give any lingering pollHealth deadline a chance to (wrongly) notify.
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, notis.errorMessages(), 1,
		"exit and timeout paths must not double-notify")
}

func TestSummarizeLoadFailure(t *testing.T) {
	tests := []struct {
		name string
		tail []string
		want string
	}{
		{
			name: "missing tensor is the salient line",
			tail: []string{
				"llama_model_loader: loaded meta data",
				"error loading model: missing tensor 'blk.64.ssm_conv1d.weight'",
				"llama_load_model_from_file: failed to load model",
			},
			want: "error loading model: missing tensor 'blk.64.ssm_conv1d.weight'",
		},
		{
			name: "unknown architecture",
			tail: []string{
				"llama_model_loader: loaded meta data",
				"error loading model: unknown model architecture: 'qwen35'",
			},
			want: "error loading model: unknown model architecture: 'qwen35'",
		},
		{
			name: "generic failure falls back to matching marker",
			tail: []string{
				"system info: BLAS = 1",
				"failed to load model",
			},
			want: "failed to load model",
		},
		{
			name: "no marker falls back to last non-empty line",
			tail: []string{"system info: BLAS = 1", "some other line", ""},
			want: "some other line",
		},
		{
			name: "empty tail yields generic message",
			tail: nil,
			want: "server exited before ready",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, summarizeLoadFailure(tt.tail))
		})
	}
}
