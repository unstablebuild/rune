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

// Package claudememory imports Claude Code conversation history into the
// dream memory-extraction pipeline.
package claudememory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

var errReadOnly = errors.New("claudememory: read-only store")

// Store is a read-only dialoguemanager.Store that reads Claude Code
// conversation JSONL files from ~/.claude/projects/.
type Store struct {
	claudeHome string
}

// NewStore creates a Store rooted at the given Claude home directory
// (typically ~/.claude).
func NewStore(claudeHome string) *Store {
	return &Store{claudeHome: claudeHome}
}

func (s *Store) projectsDir() string {
	return filepath.Join(s.claudeHome, "projects")
}

// Health checks that the projects directory exists.
func (s *Store) Health(_ context.Context) error {
	_, err := os.Stat(s.projectsDir())
	return err
}

// List walks all project directories and streams dialogues one at a
// time via a lazy iterator. Each JSONL file is parsed only when the
// consumer calls Next, avoiding buffering all conversations in memory.
func (s *Store) List(_ context.Context) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	dir := s.projectsDir()
	projects, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return iterator.FromSlice[dialoguemanager.DialogueHeader](nil), nil
		}
		return nil, fmt.Errorf("read projects dir: %w", err)
	}

	state := &listState{projectsDir: dir, projects: projects}
	return iterator.FromFunc(state.next, func() error { return nil }), nil
}

// listState drives lazy iteration over project directories and their
// JSONL files. It keeps track of the current project and file position
// so each call to next() resumes where the previous one left off.
type listState struct {
	projectsDir string
	projects    []os.DirEntry
	projIdx     int
	curProject  string
	files       []os.DirEntry
	fileIdx     int
}

func (s *listState) next(_ context.Context) (dialoguemanager.DialogueHeader, bool, error) {
	for {
		// Try the next file in the current project.
		for s.fileIdx < len(s.files) {
			f := s.files[s.fileIdx]
			s.fileIdx++

			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}

			session := strings.TrimSuffix(f.Name(), ".jsonl")
			id := s.curProject + "/" + session
			path := filepath.Join(s.projectsDir, s.curProject, f.Name())

			d, err := parseFileHeader(path, id)
			if err != nil {
				continue
			}
			return d, true, nil
		}

		// Advance to the next project directory.
		if !s.advanceProject() {
			return dialoguemanager.DialogueHeader{}, false, nil
		}
	}
}

func (s *listState) advanceProject() bool {
	for s.projIdx < len(s.projects) {
		proj := s.projects[s.projIdx]
		s.projIdx++

		if !proj.IsDir() {
			continue
		}

		projDir := filepath.Join(s.projectsDir, proj.Name())
		files, err := os.ReadDir(projDir)
		if err != nil {
			continue
		}

		s.curProject = proj.Name()
		s.files = files
		s.fileIdx = 0
		return true
	}
	return false
}

// Get loads a single conversation by ID (format: "project/session").
func (s *Store) Get(_ context.Context, id string) (dialoguemanager.Dialogue, error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 {
		return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
	}
	path := filepath.Join(s.projectsDir(), parts[0], parts[1]+".jsonl")
	return parseFile(path, id)
}

// Create is not supported on a read-only store.
func (s *Store) Create(_ context.Context, _ dialoguemanager.Dialogue) error {
	return errReadOnly
}

// Delete is not supported on a read-only store.
func (s *Store) Delete(_ context.Context, _ string) error {
	return errReadOnly
}

// AppendMessages is not supported on a read-only store.
func (s *Store) AppendMessages(
	_ context.Context, _ dialoguemanager.Dialogue,
	_ []llmapi.Message, _ llmapi.DialogueUsage,
) error {
	return errReadOnly
}

// ArchiveAndReplace is not supported on a read-only store.
func (s *Store) ArchiveAndReplace(_ context.Context, _ dialoguemanager.ArchiveAndReplaceParams) error {
	return errReadOnly
}

// SetTitle is not supported on a read-only store.
func (s *Store) SetTitle(_ context.Context, _, _ string) error {
	return errReadOnly
}

func parseFile(path, id string) (dialoguemanager.Dialogue, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
		}
		return dialoguemanager.Dialogue{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return dialoguemanager.Dialogue{}, err
	}

	msgs, err := parseConversation(f)
	if err != nil {
		return dialoguemanager.Dialogue{}, err
	}

	return dialoguemanager.Dialogue{
		ID:           id,
		Version:      int64(len(msgs)),
		MessageCount: len(msgs),
		Messages:     msgs,
		UpdatedAt:    info.ModTime(),
	}, nil
}

// parseFileHeader returns only the header metadata for a JSONL conversation
// file, counting messages without retaining them in memory.
func parseFileHeader(path, id string) (dialoguemanager.DialogueHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dialoguemanager.DialogueHeader{}, storageapi.ErrNotFound
		}
		return dialoguemanager.DialogueHeader{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return dialoguemanager.DialogueHeader{}, err
	}

	count, err := countConversationMessages(f)
	if err != nil {
		return dialoguemanager.DialogueHeader{}, err
	}

	return dialoguemanager.DialogueHeader{
		ID:           id,
		Version:      int64(count),
		MessageCount: count,
		UpdatedAt:    info.ModTime(),
	}, nil
}

// countConversationMessages counts user/assistant messages in a Claude
// Code JSONL stream using the same filtering rules as parseConversation
// (skip sidechains, non-conversational types, empty user texts, and
// deduplicate assistant chunks by message ID) but without retaining
// message content in memory.
func countConversationMessages(r io.Reader) (int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var count int
	var pendingAssistantID string
	hasPendingText := false

	flushPending := func() {
		if hasPendingText {
			count++
		}
		pendingAssistantID = ""
		hasPendingText = false
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry jsonlEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		if entry.IsSidechain {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		var msg jsonlMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		switch entry.Type {
		case "user":
			flushPending()
			text := extractUserText(msg.Content)
			if text != "" {
				count++
			}

		case "assistant":
			if pendingAssistantID != "" && pendingAssistantID != msg.ID {
				flushPending()
			}

			var blocks []contentBlock
			if err := json.Unmarshal(msg.Content, &blocks); err != nil {
				continue
			}

			if pendingAssistantID == "" {
				pendingAssistantID = msg.ID
			}
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					hasPendingText = true
					break // one text block is enough to count
				}
			}
		}
	}
	flushPending()

	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

// jsonlEntry represents a line in the Claude Code JSONL format.
type jsonlEntry struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

// jsonlMessage represents the message field in a JSONL entry.
type jsonlMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock represents an assistant content block.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseConversation reads a Claude Code JSONL stream and extracts
// user/assistant messages, skipping sidechains, thinking, tool_use,
// and non-conversational entry types.
func parseConversation(r io.Reader) ([]llmapi.Message, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max line

	type pendingAssistant struct {
		id    string
		texts []string
	}

	var msgs []llmapi.Message
	var pending *pendingAssistant

	flushPending := func() {
		if pending != nil && len(pending.texts) > 0 {
			msgs = append(msgs, llmapi.Message{
				Role:    llmapi.RoleAssistant,
				Content: strings.Join(pending.texts, ""),
			})
		}
		pending = nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry jsonlEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		if entry.IsSidechain {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		var msg jsonlMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		switch entry.Type {
		case "user":
			flushPending()
			text := extractUserText(msg.Content)
			if text == "" {
				continue
			}
			msgs = append(msgs, llmapi.Message{
				Role:    llmapi.RoleUser,
				Content: text,
			})

		case "assistant":
			if pending != nil && pending.id != msg.ID {
				flushPending()
			}

			var blocks []contentBlock
			if err := json.Unmarshal(msg.Content, &blocks); err != nil {
				continue
			}

			if pending == nil {
				pending = &pendingAssistant{id: msg.ID}
			}
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					pending.texts = append(pending.texts, b.Text)
				}
			}
		}
	}
	flushPending()

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// extractUserText extracts text from user message content, which may
// be a JSON string or an array of content blocks.
func extractUserText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}
