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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

// memConfig is a configedit.Getter that only answers
// memory_recall_enabled, the single key the guard reads.
type memConfig struct {
	enabled bool
	err     error
}

func (m *memConfig) GetBool(key string) configedit.Bool {
	return configedit.NewBool(func(context.Context) (bool, error) {
		if key != memoryRecallEnabledKey {
			return false, configedit.ErrNotFound
		}
		return m.enabled, m.err
	})
}

func (m *memConfig) GetInt(string) configedit.Int             { return configedit.Int{} }
func (m *memConfig) GetFloat(string) configedit.Float         { return configedit.Float{} }
func (m *memConfig) GetString(string) configedit.String       { return configedit.String{} }
func (m *memConfig) GetMap(string) configedit.Map             { return configedit.Map{} }
func (m *memConfig) GetSlice(string) configedit.Slice         { return configedit.Slice{} }
func (m *memConfig) GetRune(string) configedit.Rune           { return configedit.Rune{} }
func (m *memConfig) GetColor(string) configedit.Color         { return configedit.Color{} }
func (m *memConfig) GetAttribute(string) configedit.Attribute { return configedit.Attribute{} }
func (m *memConfig) GetConfig(string) configedit.ConfigValue  { return configedit.ConfigValue{} }
func (m *memConfig) Iterate(func(string, any))                {}

// fakeRecaller is an agent.MemoryRecaller test double.
type fakeRecaller struct {
	memories []agent.Memory
	err      error
	calls    int
}

func (r *fakeRecaller) Recall(_ context.Context, _ []string, _ string, _ string) ([]agent.Memory, error) {
	r.calls++
	return r.memories, r.err
}

func TestNewGuardedRecaller_PanicsOnNil(t *testing.T) {
	t.Run("nil inner", func(t *testing.T) {
		assert.Panics(t, func() {
			NewGuardedRecaller(nil, &memConfig{})
		})
	})
	t.Run("nil cfg", func(t *testing.T) {
		assert.Panics(t, func() {
			NewGuardedRecaller(&fakeRecaller{}, nil)
		})
	})
}

func TestGuardedRecaller_Recall(t *testing.T) {
	someMems := []agent.Memory{
		{ID: "mem-1", Content: "Always test"},
	}
	innerErr := errors.New("boom")

	tests := []struct {
		name       string
		cfg        *memConfig
		inner      *fakeRecaller
		want       []agent.Memory
		wantErr    error
		wantCalled bool
	}{
		{
			name:       "enabled returns inner memories",
			cfg:        &memConfig{enabled: true},
			inner:      &fakeRecaller{memories: someMems},
			want:       someMems,
			wantCalled: true,
		},
		{
			name:       "enabled propagates inner error",
			cfg:        &memConfig{enabled: true},
			inner:      &fakeRecaller{err: innerErr},
			wantErr:    innerErr,
			wantCalled: true,
		},
		{
			name:  "disabled skips inner",
			cfg:   &memConfig{enabled: false},
			inner: &fakeRecaller{memories: someMems},
		},
		{
			name:  "absent skips inner",
			cfg:   &memConfig{err: configedit.ErrNotFound},
			inner: &fakeRecaller{memories: someMems},
		},
		{
			name:  "config read error skips inner",
			cfg:   &memConfig{err: errors.New("bad yaml")},
			inner: &fakeRecaller{memories: someMems},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGuardedRecaller(tt.inner, tt.cfg)

			got, err := g.Recall(context.Background(), nil, "task", "")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCalled, tt.inner.calls > 0)
		})
	}
}
