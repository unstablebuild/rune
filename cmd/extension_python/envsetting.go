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

package main

import (
	"context"
	"errors"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"unstable.build/rune/internal/extension/langext"
)

// envPolicyDoc records whether the user let Rune manage the Python
// environment of a single project root. Scoping the answer to the root
// rather than the workspace keeps monorepos able to mix a Rune-managed
// project with one the user drives themselves.
type envPolicyDoc struct {
	Root      string
	Managed   bool
	UpdatedAt time.Time
}

// envSetting persists the per-root environment policy. The extension
// storage service is process-wide and shared with every other
// extension, so it is partitioned before any document is written.
type envSetting struct {
	store storageapi.Service
}

func newEnvSetting(store storageapi.Service) *envSetting {
	if store == nil {
		return &envSetting{}
	}
	return &envSetting{store: storageapi.WithPartition(store, "python")}
}

func envPolicyID(root langext.Root) string { return "env-policy:" + root.URI }

// get reports the policy stored for root. A known=false result means the
// user has not answered yet and should be asked; storage being
// unavailable degrades to the same answer so the extension still runs
// when the storage permission is denied.
func (s *envSetting) get(
	ctx context.Context, root langext.Root,
) (managed, known bool, err error) {
	if s == nil || s.store == nil {
		return false, false, nil
	}
	var doc envPolicyDoc
	if err := s.store.Get(ctx, envPolicyID(root), &doc); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	return doc.Managed, true, nil
}

func (s *envSetting) set(ctx context.Context, root langext.Root, managed bool) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Set(ctx, envPolicyID(root), &envPolicyDoc{
		Root:    root.URI,
		Managed: managed,
	})
}
