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

package dream

import (
	"context"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

var _ dialoguemanager.Store = (*ephemeralStore)(nil)

// ephemeralStore is an in-memory dialoguemanager.Store for disposable
// conversations. The dream agent's own internal conversation does not
// need to be persisted.
type ephemeralStore struct {
	mu   sync.Mutex
	data map[string]dialoguemanager.Dialogue
}

func newEphemeralStore() *ephemeralStore {
	return &ephemeralStore{data: make(map[string]dialoguemanager.Dialogue)}
}

func (s *ephemeralStore) Health(_ context.Context) error { return nil }

func (s *ephemeralStore) Create(
	_ context.Context, d dialoguemanager.Dialogue,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[d.ID]; ok {
		return storageapi.ErrAlreadyExists
	}
	if d.Version == 0 {
		d.Version = 1
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now()
	}
	s.data[d.ID] = d
	return nil
}

func (s *ephemeralStore) Get(
	_ context.Context, id string,
) (dialoguemanager.Dialogue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[id]
	if !ok {
		return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
	}
	return d, nil
}

func (s *ephemeralStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return nil
}

func (s *ephemeralStore) AppendMessages(
	_ context.Context, d dialoguemanager.Dialogue,
	msgs []llmapi.Message, _ llmapi.DialogueUsage,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.data[d.ID]
	if !ok {
		return storageapi.ErrNotFound
	}
	existing.Messages = append(existing.Messages, msgs...)
	existing.Version++
	existing.UpdatedAt = time.Now()
	s.data[d.ID] = existing
	return nil
}

func (s *ephemeralStore) SetTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[id]
	if !ok {
		return storageapi.ErrNotFound
	}
	d.Title = title
	s.data[id] = d
	return nil
}

func (s *ephemeralStore) List(_ context.Context) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	headers := make([]dialoguemanager.DialogueHeader, 0, len(s.data))
	for _, d := range s.data {
		headers = append(headers, d.Header())
	}
	return iterator.FromSlice(headers), nil
}

func (s *ephemeralStore) ArchiveAndReplace(_ context.Context, p dialoguemanager.ArchiveAndReplaceParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	archived := p.Dialogue
	archived.ID = p.ArchivedDialogueID
	s.data[p.ArchivedDialogueID] = archived
	replaced := p.Dialogue
	replaced.Messages = p.Messages
	replaced.MessageCount = len(p.Messages)
	replaced.Version = 1
	replaced.UpdatedAt = time.Now()
	s.data[p.Dialogue.ID] = replaced
	return nil
}
