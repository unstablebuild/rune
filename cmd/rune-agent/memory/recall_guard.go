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

package memory

import (
	"context"
	"errors"
	"log/slog"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

const memoryRecallEnabledKey = "memory_recall_enabled"

// GuardedRecaller gates an inner agent.MemoryRecaller behind the
// memory_recall_enabled config property. Recall delegates to the inner
// recaller only when the property is explicitly true; when it is false,
// absent, or unreadable, Recall returns (nil, nil) without invoking the
// inner recaller. The property is resolved on every call, so edits to
// the config take effect without restarting the session.
type GuardedRecaller struct {
	inner   agent.MemoryRecaller
	enabled configedit.Bool
}

// NewGuardedRecaller returns a GuardedRecaller wired to inner and cfg.
// Panics if inner or cfg is nil.
func NewGuardedRecaller(
	inner agent.MemoryRecaller, cfg configedit.Getter,
) *GuardedRecaller {
	if inner == nil {
		panic("memory: GuardedRecaller inner must not be nil")
	}
	if cfg == nil {
		panic("memory: GuardedRecaller cfg must not be nil")
	}
	return &GuardedRecaller{
		inner:   inner,
		enabled: cfg.GetBool(memoryRecallEnabledKey),
	}
}

// Recall implements agent.MemoryRecaller.
func (g *GuardedRecaller) Recall(
	ctx context.Context, files []string, task string, workspaceRoot string,
) ([]agent.Memory, error) {
	v, err := g.enabled.Resolve(ctx)
	if err != nil {
		if !errors.Is(err, configedit.ErrNotFound) {
			slog.Warn("get memory_recall_enabled from config", "error", err)
		}
		return nil, nil
	}
	if !v {
		return nil, nil
	}
	return g.inner.Recall(ctx, files, task, workspaceRoot)
}
