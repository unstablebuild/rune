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

package llmrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/llm"
)

// closedRootStorage mimics a firstmover-backed storage whose root Service
// has been closed: every Get returns the exact sentinel the host surfaces.
type closedRootStorage struct {
	storageapi.Service
}

func (closedRootStorage) Get(context.Context, string, any) error {
	return errors.New("firstmover: Partition on closed Service")
}

func TestGetModel_PropagatesClosedStorageError(t *testing.T) {
	cfg := llm.DefaultConfig()
	cfg.Gemini.APIKey = "test-key"
	cfg.Gemini.BaseURL = "http://127.0.0.1:1"
	r, err := New(cfg, t.TempDir(),
		closedRootStorage{Service: storagestub.NewInMemoryService()},
		&fakeLocalService{})
	require.NoError(t, err)

	_, err = r.GetModel(context.Background(), llmapi.ModelEntry{Name: llmapi.DefaultModel})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Partition on closed Service",
		"a closed host alias-store must surface to GetModel so the agent's "+
			"startup model resolution fails with the firstmover sentinel")
}
