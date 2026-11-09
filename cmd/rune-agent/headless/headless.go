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

package headless

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/debug"
)

// Options configures a single headless run.
type Options struct {
	// InstructionsFile is the path to the file whose contents are sent
	// to the agent as the task prompt.
	InstructionsFile string
	// Model is a model argument resolved host-side. Empty means
	// llmapi.DefaultModel.
	Model string
	// Effort overrides the reasoning effort. Empty uses the
	// provider/config default.
	Effort string
	// Stdout receives the JSONL event stream. Defaults to os.Stdout.
	Stdout io.Writer
}

// Result reports the outcome of a headless run.
type Result struct {
	// Trajectory is the ATIF document written as the final stdout line.
	Trajectory Trajectory
	// AgentError is non-nil when the agent loop itself failed. It is
	// distinct from the error returned by Run, which reports a failure
	// to start.
	AgentError error
}

// Run executes one non-interactive agent run against the Rune host that
// launched this process. A non-nil error means the run could not start;
// a failure inside the agent loop is reported through Result.AgentError
// so the trajectory is still emitted.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.InstructionsFile == "" {
		return Result{}, errors.New("instructions file is required")
	}
	if opts.Model == "" {
		opts.Model = llmapi.DefaultModel
	}
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}

	instructions, err := os.ReadFile(opts.InstructionsFile)
	if err != nil {
		return Result{}, fmt.Errorf("read instructions: %w", err)
	}
	sum := sha256.Sum256(instructions)

	w, err := Connect(ExtensionID)
	if err != nil {
		return Result{}, err
	}

	runID := uuid.NewString()
	sess, err := bootstrap(ctx, w, opts, runID)
	if err != nil {
		return Result{}, err
	}
	defer sess.cleanup()

	startedAt := time.Now().UTC()
	builder := NewBuilder(BuilderConfig{
		AgentVersion:    debug.Tag,
		SessionID:       runID,
		ModelName:       sess.model.Name,
		ReasoningEffort: opts.Effort,
		ToolDefinitions: sess.registry.AllTools(),
		Extra: map[string]any{
			"provider":            sess.model.Provider,
			"reasoning_effort":    opts.Effort,
			"workspace":           sess.cwd.String(),
			"commit":              debug.Commit,
			"instructions_file":   opts.InstructionsFile,
			"instructions_sha256": hex.EncodeToString(sum[:]),
			"started_at":          startedAt.Format(time.RFC3339Nano),
		},
	})
	stream := newStreamer(out, builder)

	res := Result{}
	if err := stream.instructions(string(instructions)); err != nil {
		return res, fmt.Errorf("write instructions event: %w", err)
	}
	res.AgentError = drive(ctx, sess.agent, runID, string(instructions), stream)

	builder.cfg.Extra["ended_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	traj, err := stream.finish()
	res.Trajectory = traj
	if err != nil {
		return res, err
	}
	return res, nil
}

// drive consumes the agent event stream to completion, forwarding every
// event to the JSONL stream and the ATIF builder.
func drive(
	ctx context.Context, ag *agent.Agent,
	dialogueID, message string, stream *streamer,
) error {
	it := ag.Run(ctx, dialogueID, message)
	defer func() { _ = it.Close() }()

	var agentErr error
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if err := stream.event(ev); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
		if ev.Type == agent.EventError && agentErr == nil {
			agentErr = ev.Error
		}
	}
	if err := it.Err(); err != nil && agentErr == nil {
		agentErr = err
	}
	return agentErr
}
