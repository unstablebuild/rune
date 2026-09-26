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

package dialoguemanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
)

const storeIndexRecordID = "dialogue-index"

var retryStrategy = retry.CombinedStrategy(
	retry.ExponentialStrategy(10*time.Millisecond, 500*time.Millisecond),
	retry.LimitStrategy(10),
)

// Store abstracts dialogue persistence to durable storage.
type Store interface {
	Health(context.Context) error
	Create(context.Context, Dialogue) error
	Get(context.Context, string) (Dialogue, error)
	Delete(context.Context, string) error
	AppendMessages(context.Context, Dialogue, []llmapi.Message, llmapi.DialogueUsage) error
	ArchiveAndReplace(context.Context, ArchiveAndReplaceParams) error
	List(context.Context) (iterator.Iterator[DialogueHeader], error)
	SetTitle(context.Context, string, string) error
}

// ApprovedPlan holds the durable, first-class state for a user-approved plan.
// It is stored separately from the dialogue transcript so compaction and resume
// do not need to recover plan state from message text.
type ApprovedPlan struct {
	Path string
	Body string
}

// NewStore allocates storage for a new Store using the given backend for
// dialogue metadata and the given directory for dialogue message bodies.
func NewStore(backend storageapi.Service, messagesDir string) Store {
	return store{backend: backend, messagesDir: messagesDir}
}

type store struct {
	backend     storageapi.Service
	messagesDir string
}

// Dialogue holds a dialogue's data as stored in durable storage.
type Dialogue struct {
	ID           string
	Title        string
	AgentID      string
	Model        string
	WorkspaceURI string
	ApprovedPlan *ApprovedPlan
	SubAgent     bool
	Version      int64
	MessageCount int
	MessagesPath string
	Messages     []llmapi.Message
	Usage        llmapi.DialogueUsage
	UpdatedAt    time.Time
}

type storedDialogue struct {
	ID                string
	Title             string
	AgentID           string
	Model             string
	WorkspaceURI      string
	ApprovedPlan      *ApprovedPlan
	SubAgent          bool
	Version           int64
	MessageCount      int
	MessagesPath      string
	Usage             llmapi.DialogueUsage
	DialogueUpdatedAt time.Time
	UpdatedAt         time.Time
}

// NamedID formats the header as "title (id)", or the bare ID when untitled.
// ParseNamedID unwraps this form back to the addressable ID.
func (h DialogueHeader) NamedID() string {
	if h.Title == "" {
		return h.ID
	}
	return h.Title + " (" + h.ID + ")"
}

// ParseNamedID extracts the dialogue ID from a NamedID-formatted " (id)"
// suffix; arguments without it are returned unchanged.
func ParseNamedID(arg string) string {
	if strings.HasSuffix(arg, ")") {
		if i := strings.LastIndex(arg, "("); i >= 0 {
			return arg[i+1 : len(arg)-1]
		}
	}
	return arg
}

// DisplayName is the dialogue's user-facing name: its title when set,
// otherwise the generated ID.
func (d Dialogue) DisplayName() string {
	if d.Title != "" {
		return d.Title
	}
	return d.ID
}

// TabURI is the identity of the chat tab hosting a dialogue —
// "rune-agent://<model>/<id>".
func TabURI(id, model string) (workspaceapi.URI, error) {
	model = strings.ReplaceAll(model, "/", "_") // i.e. hf.co/org/model
	model = strings.ReplaceAll(model, ":", "_") // i.e. llama4:scout
	model = url.PathEscape(model)
	return workspaceapi.ParseURI(fmt.Sprintf("rune-agent://%s/%s", model, id))
}

// DialogueHeader holds the metadata of a dialogue without the full message
// history. Use this for listing/filtering dialogues without paying the
// deserialization cost of Messages.
type DialogueHeader struct {
	ID              string
	Title           string
	AgentID         string
	Model           string
	WorkspaceURI    string
	HasApprovedPlan bool
	SubAgent        bool
	Version         int64
	MessageCount    int
	Usage           llmapi.DialogueUsage
	UpdatedAt       time.Time
}

type dialogueIndex struct {
	Headers      map[string]DialogueHeader
	Version      int64
	Bootstrapped bool
}

func newDialogueIndex() dialogueIndex {
	return dialogueIndex{Headers: make(map[string]DialogueHeader), Version: 1}
}

// Workspace parses WorkspaceURI and returns the resulting URI.
// The second return value is false when WorkspaceURI is empty or
// cannot be parsed.
func (d Dialogue) Workspace() (workspaceapi.URI, bool) {
	if d.WorkspaceURI == "" {
		return workspaceapi.URI{}, false
	}
	u, err := workspaceapi.ParseURI(d.WorkspaceURI)
	if err != nil {
		return workspaceapi.URI{}, false
	}
	return u, true
}

// Workspace parses WorkspaceURI and returns the resulting URI.
// The second return value is false when WorkspaceURI is empty or
// cannot be parsed.
func (d DialogueHeader) Workspace() (workspaceapi.URI, bool) {
	if d.WorkspaceURI == "" {
		return workspaceapi.URI{}, false
	}
	u, err := workspaceapi.ParseURI(d.WorkspaceURI)
	if err != nil {
		return workspaceapi.URI{}, false
	}
	return u, true
}

// Header returns a DialogueHeader from this Dialogue.
func (d Dialogue) Header() DialogueHeader {
	messageCount := d.MessageCount
	if messageCount == 0 {
		messageCount = len(d.Messages)
	}
	return DialogueHeader{
		ID:              d.ID,
		Title:           d.Title,
		AgentID:         d.AgentID,
		Model:           d.Model,
		WorkspaceURI:    d.WorkspaceURI,
		HasApprovedPlan: d.ApprovedPlan != nil,
		SubAgent:        d.SubAgent,
		Version:         d.Version,
		MessageCount:    messageCount,
		Usage:           d.Usage,
		UpdatedAt:       d.UpdatedAt,
	}
}

func storedDialogueFromDialogue(d Dialogue) storedDialogue {
	return storedDialogue{
		ID:                d.ID,
		Title:             d.Title,
		AgentID:           d.AgentID,
		Model:             d.Model,
		WorkspaceURI:      d.WorkspaceURI,
		ApprovedPlan:      d.ApprovedPlan,
		SubAgent:          d.SubAgent,
		Version:           d.Version,
		MessageCount:      d.MessageCount,
		MessagesPath:      d.MessagesPath,
		Usage:             d.Usage,
		DialogueUpdatedAt: d.UpdatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}

func dialogueFromStoredDialogue(d storedDialogue) Dialogue {
	updatedAt := d.DialogueUpdatedAt
	if updatedAt.IsZero() {
		updatedAt = d.UpdatedAt
	}
	return Dialogue{
		ID:           d.ID,
		Title:        d.Title,
		AgentID:      d.AgentID,
		Model:        d.Model,
		WorkspaceURI: d.WorkspaceURI,
		ApprovedPlan: d.ApprovedPlan,
		SubAgent:     d.SubAgent,
		Version:      d.Version,
		MessageCount: d.MessageCount,
		MessagesPath: d.MessagesPath,
		Usage:        d.Usage,
		UpdatedAt:    updatedAt,
	}
}

func encodedDialogueID(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func (s store) messagesPathForID(id string) string {
	return filepath.Join(s.messagesDir, encodedDialogueID(id)+".json")
}

func (s store) ensureMessagesDir() error {
	if s.messagesDir == "" {
		return fmt.Errorf("messages dir is empty")
	}
	return os.MkdirAll(s.messagesDir, 0o700)
}

func (s store) readMessages(path string) ([]llmapi.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dialogue messages %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var msgs []llmapi.Message
	if err := json.NewDecoder(f).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("decode dialogue messages %q: %w", path, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return msgs, nil
}

func (s store) writeMessages(path string, msgs []llmapi.Message) error {
	if path == "" {
		return fmt.Errorf("messages path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create dialogue messages dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp messages file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := json.NewEncoder(tmp).Encode(msgs); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encode dialogue messages: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp messages file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp messages file: %w", err)
	}
	return nil
}

func (s store) getStoredDialogue(
	ctx context.Context, id string,
) (Dialogue, error) {
	var stored storedDialogue
	err := s.backend.Get(ctx, id, &stored)
	if err != nil {
		return Dialogue{}, fmt.Errorf("document service get: %w", err)
	}
	doc := dialogueFromStoredDialogue(stored)
	if doc.Version == 0 {
		doc.Version = 1
	}
	if doc.MessageCount == 0 {
		doc.MessageCount = len(doc.Messages)
	}
	return doc, nil
}

func (s store) loadDialogueMessages(d Dialogue) ([]llmapi.Message, error) {
	if d.MessagesPath == "" {
		if len(d.Messages) == 0 {
			return nil, nil
		}
		return append([]llmapi.Message(nil), d.Messages...), nil
	}
	return s.readMessages(d.MessagesPath)
}

func (s store) rollbackMessages(path string, msgs []llmapi.Message) {
	if path == "" {
		return
	}
	if len(msgs) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = s.writeMessages(path, msgs)
}

// ArchiveAndReplaceParams holds the parameters for an ArchiveAndReplace
// operation. The caller provides the current Dialogue (which it already
// has in memory) and only specifies the new messages. Stable metadata
// (AgentID, Model, WorkspaceURI, Usage) is copied from the provided Dialogue.
type ArchiveAndReplaceParams struct {
	// Dialogue is the current dialogue being compacted/cleared.
	// Its full content is archived under ArchivedDialogueID.
	Dialogue Dialogue
	// ArchivedDialogueID is the ID under which the old messages are archived.
	ArchivedDialogueID string
	// Messages is the replacement message slice for the active dialogue.
	Messages []llmapi.Message
	// ApprovedPlan is the replacement approved-plan state for the active dialogue.
	// When nil, the active dialogue will have no approved plan metadata.
	ApprovedPlan *ApprovedPlan
}

func (s store) Health(ctx context.Context) error {
	if err := s.ensureMessagesDir(); err != nil {
		return fmt.Errorf("messages dir: %w", err)
	}
	err := s.backend.Delete(ctx, "IDThatWillNeverExist")
	if err != nil {
		return fmt.Errorf("backend: %w", err)
	}
	return nil
}

func (s store) getIndex(ctx context.Context) (dialogueIndex, bool, error) {
	var idx dialogueIndex
	err := s.backend.Get(ctx, storeIndexRecordID, &idx)
	if err == nil {
		if idx.Headers == nil {
			idx.Headers = make(map[string]DialogueHeader)
		}
		if idx.Version == 0 {
			idx.Version = 1
		}
		return idx, true, nil
	}
	if !errors.Is(err, storageapi.ErrNotFound) {
		return dialogueIndex{}, false, fmt.Errorf("document service get index: %w", err)
	}
	return newDialogueIndex(), false, nil
}

func (s store) loadLegacyHeaders(ctx context.Context) (map[string]DialogueHeader, error) {
	it, err := s.backend.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("document service list legacy dialogues: %w", err)
	}
	stored, err := iterator.ToSlice(ctx, iterator.FromDocumentIterator[storedDialogue](it))
	if err != nil {
		return nil, fmt.Errorf("collect legacy dialogues: %w", err)
	}
	headers := make(map[string]DialogueHeader)
	for _, record := range stored {
		d := dialogueFromStoredDialogue(record)
		if !isLegacyDialogue(d) || d.ID == storeIndexRecordID {
			continue
		}
		headers[d.ID] = d.Header()
	}
	return headers, nil
}

func (s store) ensureIndex(ctx context.Context) (dialogueIndex, error) {
	return s.ensureIndexMode(ctx, true)
}

func (s store) ensureIndexWithoutBootstrap(ctx context.Context) (dialogueIndex, error) {
	return s.ensureIndexMode(ctx, false)
}

func (s store) ensureIndexMode(ctx context.Context, bootstrap bool) (dialogueIndex, error) {
	idx, exists, err := s.getIndex(ctx)
	if err != nil {
		return dialogueIndex{}, err
	}
	if exists && idx.Bootstrapped {
		return idx, nil
	}
	if !bootstrap {
		if !exists {
			idx = newDialogueIndex()
			if err := s.backend.Create(ctx, storeIndexRecordID, &idx); err == nil {
				return idx, nil
			} else if !errors.Is(err, storageapi.ErrAlreadyExists) {
				return dialogueIndex{}, fmt.Errorf("document service create index: %w", err)
			}
			idx, _, err = s.getIndex(ctx)
			if err != nil {
				return dialogueIndex{}, err
			}
		}
		return idx, nil
	}

	legacyHeaders, err := s.loadLegacyHeaders(ctx)
	if err != nil {
		return dialogueIndex{}, err
	}

	if !exists {
		idx = newDialogueIndex()
		idx.Bootstrapped = true
		for id, header := range legacyHeaders {
			idx.Headers[id] = header
		}
		if err := s.backend.Create(ctx, storeIndexRecordID, &idx); err == nil {
			return idx, nil
		} else if !errors.Is(err, storageapi.ErrAlreadyExists) {
			return dialogueIndex{}, fmt.Errorf("document service create index: %w", err)
		}
	}

	err = storageapi.ConsistentUpdate(ctx, s.backend, storeIndexRecordID, &idx, retryStrategy,
		func() ([]storageapi.Update, []storageapi.Precondition) {
			if idx.Headers == nil {
				idx.Headers = make(map[string]DialogueHeader)
			}
			for id, header := range legacyHeaders {
				if _, ok := idx.Headers[id]; !ok {
					idx.Headers[id] = header
				}
			}
			return []storageapi.Update{
					{FieldPath: []string{"Headers"}, Value: idx.Headers},
					{FieldPath: []string{"Bootstrapped"}, Value: true},
					{FieldPath: []string{"Version"}, Value: idx.Version + 1},
				}, []storageapi.Precondition{
					{FieldPath: []string{"Version"}, Value: idx.Version},
				}
		})
	if err != nil {
		return dialogueIndex{}, fmt.Errorf("document service bootstrap index: %w", err)
	}
	idx, _, err = s.getIndex(ctx)
	if err != nil {
		return dialogueIndex{}, err
	}
	return idx, nil
}

func (s store) updateIndex(ctx context.Context, fn func(dialogueIndex) dialogueIndex) error {
	idx, err := s.ensureIndexWithoutBootstrap(ctx)
	if err != nil {
		return err
	}
	return storageapi.ConsistentUpdate(ctx, s.backend, storeIndexRecordID, &idx, retryStrategy,
		func() ([]storageapi.Update, []storageapi.Precondition) {
			idx = fn(idx)
			return []storageapi.Update{
					{FieldPath: []string{"Headers"}, Value: idx.Headers},
					{FieldPath: []string{"Bootstrapped"}, Value: idx.Bootstrapped},
					{FieldPath: []string{"Version"}, Value: idx.Version + 1},
				}, []storageapi.Precondition{
					{FieldPath: []string{"Version"}, Value: idx.Version},
				}
		})
}

func isLegacyDialogue(d Dialogue) bool {
	return d.ID != "" && !d.UpdatedAt.IsZero()
}

func (s store) Create(ctx context.Context, d Dialogue) error {
	if d.Version == 0 {
		d.Version = 1
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now()
	}
	d.MessageCount = len(d.Messages)
	if d.MessagesPath == "" {
		d.MessagesPath = s.messagesPathForID(d.ID)
	}
	stored := storedDialogueFromDialogue(d)
	err := s.backend.Create(ctx, d.ID, &stored)
	if err != nil {
		return fmt.Errorf("document service create: %w", err)
	}
	if err := s.writeMessages(d.MessagesPath, d.Messages); err != nil {
		_ = s.backend.Delete(ctx, d.ID)
		_ = os.Remove(d.MessagesPath)
		return err
	}
	err = s.updateIndex(ctx, func(idx dialogueIndex) dialogueIndex {
		idx.Headers[d.ID] = d.Header()
		return idx
	})
	if err != nil {
		_ = s.backend.Delete(ctx, d.ID)
		_ = os.Remove(d.MessagesPath)
		return err
	}
	return nil
}

func (s store) ArchiveAndReplace(ctx context.Context, p ArchiveAndReplaceParams) error {
	archived := p.Dialogue
	archived.ID = p.ArchivedDialogueID
	replaced := p.Dialogue
	archived.MessageCount = len(archived.Messages)
	if archived.MessagesPath == "" || archived.MessagesPath == replaced.MessagesPath {
		archived.MessagesPath = s.messagesPathForID(archived.ID)
	}

	replaced.Messages = append([]llmapi.Message(nil), p.Messages...)
	replaced.ApprovedPlan = p.ApprovedPlan
	replaced.MessageCount = len(replaced.Messages)
	replaced.Version = 1
	replaced.UpdatedAt = time.Now()
	if replaced.MessagesPath == "" {
		replaced.MessagesPath = s.messagesPathForID(replaced.ID)
	}

	if err := s.writeMessages(archived.MessagesPath, archived.Messages); err != nil {
		return fmt.Errorf("archive-and-replace archive write: %w", err)
	}
	storedArchived := storedDialogueFromDialogue(archived)
	err := s.backend.Create(ctx, archived.ID, &storedArchived)
	if err != nil {
		_ = os.Remove(archived.MessagesPath)
		return fmt.Errorf("archive-and-replace archive: document service create: %w", err)
	}
	if err := s.writeMessages(replaced.MessagesPath, replaced.Messages); err != nil {
		_ = s.backend.Delete(ctx, archived.ID)
		_ = os.Remove(archived.MessagesPath)
		return fmt.Errorf("archive-and-replace replace write: %w", err)
	}
	err = s.backend.Update(ctx, replaced.ID, []storageapi.Update{
		{FieldPath: []string{"ApprovedPlan"}, Value: replaced.ApprovedPlan},
		{FieldPath: []string{"MessageCount"}, Value: replaced.MessageCount},
		{FieldPath: []string{"MessagesPath"}, Value: replaced.MessagesPath},
		{FieldPath: []string{"Version"}, Value: replaced.Version},
		{FieldPath: []string{"UpdatedAt"}, Value: replaced.UpdatedAt},
		{FieldPath: []string{"DialogueUpdatedAt"}, Value: replaced.UpdatedAt},
	})
	if errors.Is(err, storageapi.ErrNotFound) {
		storedReplaced := storedDialogueFromDialogue(replaced)
		err = s.backend.Set(ctx, replaced.ID, &storedReplaced)
	}
	if err != nil {
		s.rollbackMessages(replaced.MessagesPath, p.Dialogue.Messages)
		_ = s.backend.Delete(ctx, archived.ID)
		_ = os.Remove(archived.MessagesPath)
		return fmt.Errorf("archive-and-replace replace: document service update: %w", err)
	}
	return s.updateIndex(ctx, func(idx dialogueIndex) dialogueIndex {
		idx.Headers[archived.ID] = archived.Header()
		header := replaced.Header()
		if prev, ok := idx.Headers[replaced.ID]; ok {
			header.Title = prev.Title
		}
		idx.Headers[replaced.ID] = header
		return idx
	})
}

func (s store) Get(
	ctx context.Context, ID string,
) (Dialogue, error) {
	doc, err := s.getStoredDialogue(ctx, ID)
	if err != nil {
		return Dialogue{}, err
	}
	msgs, err := s.loadDialogueMessages(doc)
	if err != nil {
		return Dialogue{}, fmt.Errorf("load dialogue messages: %w", err)
	}
	doc.Messages = msgs
	if doc.MessagesPath != "" {
		doc.MessageCount = len(msgs)
	}
	return doc, nil
}

func (s store) Delete(
	ctx context.Context, ID string,
) error {
	d, err := s.getStoredDialogue(ctx, ID)
	if err != nil && !errors.Is(err, storageapi.ErrNotFound) {
		return err
	}
	err = s.backend.Delete(ctx, ID)
	if err != nil {
		return fmt.Errorf("document service delete: %w", err)
	}
	if d.MessagesPath != "" {
		_ = os.Remove(d.MessagesPath)
	}
	_ = os.Remove(s.messagesPathForID(ID))
	err = s.updateIndex(ctx, func(idx dialogueIndex) dialogueIndex {
		delete(idx.Headers, ID)
		return idx
	})
	if err != nil {
		return err
	}
	return nil
}

// SetTitle patches only the Title field so a rename cannot clobber a
// concurrent AppendMessages; the index header follows through the same
// CAS path other mutations use.
func (s store) SetTitle(ctx context.Context, id, title string) error {
	var stored storedDialogue
	err := storageapi.ConsistentUpdate(ctx, s.backend, id, &stored, retryStrategy,
		func() ([]storageapi.Update, []storageapi.Precondition) {
			if stored.Title == title {
				return nil, nil
			}
			return []storageapi.Update{
					{FieldPath: []string{"Title"}, Value: title},
					{FieldPath: []string{"Version"}, Value: stored.Version + 1},
				}, []storageapi.Precondition{
					{FieldPath: []string{"Version"}, Value: stored.Version},
				}
		})
	if err != nil {
		return fmt.Errorf("set title: document service update: %w", err)
	}
	return s.updateIndex(ctx, func(idx dialogueIndex) dialogueIndex {
		if h, ok := idx.Headers[id]; ok {
			h.Title = title
			idx.Headers[id] = h
		}
		return idx
	})
}

func (s store) AppendMessages(
	ctx context.Context, d Dialogue, msgs []llmapi.Message, usage llmapi.DialogueUsage,
) error {
	updated := Dialogue{}
	current, err := s.getStoredDialogue(ctx, d.ID)
	if err != nil {
		return fmt.Errorf("append messages: %w", err)
	}
	currentMessages, err := s.loadDialogueMessages(current)
	if err != nil {
		if current.MessagesPath != "" && errors.Is(err, os.ErrNotExist) {
			currentMessages = nil
		} else {
			return fmt.Errorf("append messages: load existing dialogue messages: %w", err)
		}
	}
	updated = current
	updated.Messages = append(append([]llmapi.Message(nil), currentMessages...), msgs...)
	updated.MessageCount = len(updated.Messages)
	updated.UpdatedAt = time.Now()
	updated.Version = current.Version + 1
	if updated.MessagesPath == "" {
		updated.MessagesPath = s.messagesPathForID(updated.ID)
	}
	updated.Usage = current.Usage
	updated.Usage.TokensSent += usage.TokensSent
	updated.Usage.TokensReceived += usage.TokensReceived
	updated.Usage.TokensReasoned += usage.TokensReasoned
	updated.Usage.TokensCached += usage.TokensCached
	updated.Usage.TokensCacheCreated += usage.TokensCacheCreated
	updated.Usage.Completions += usage.Completions
	updated.Usage.ToolCalls += usage.ToolCalls
	updated.Usage.TotalDuration += usage.TotalDuration
	updated.Usage.InferenceDuration += usage.InferenceDuration
	updated.Usage.ToolCallDuration += usage.ToolCallDuration

	if err := s.writeMessages(updated.MessagesPath, updated.Messages); err != nil {
		return fmt.Errorf("append messages: write dialogue messages: %w", err)
	}
	err = s.backend.Update(ctx, updated.ID, []storageapi.Update{
		{FieldPath: []string{"Usage"}, Value: updated.Usage},
		{FieldPath: []string{"MessageCount"}, Value: updated.MessageCount},
		{FieldPath: []string{"MessagesPath"}, Value: updated.MessagesPath},
		{FieldPath: []string{"Version"}, Value: updated.Version},
		{FieldPath: []string{"UpdatedAt"}, Value: updated.UpdatedAt},
		{FieldPath: []string{"DialogueUpdatedAt"}, Value: updated.UpdatedAt},
	})
	if err != nil {
		s.rollbackMessages(updated.MessagesPath, currentMessages)
		return fmt.Errorf("append messages: document service update: %w", err)
	}
	err = s.updateIndex(ctx, func(idx dialogueIndex) dialogueIndex {
		header := updated.Header()
		if prev, ok := idx.Headers[updated.ID]; ok {
			header.Title = prev.Title
		}
		idx.Headers[updated.ID] = header
		return idx
	})
	if err != nil {
		return err
	}
	return nil
}

func (s store) List(ctx context.Context) (iterator.Iterator[DialogueHeader], error) {
	idx, err := s.ensureIndex(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]DialogueHeader, 0, len(idx.Headers))
	for _, header := range idx.Headers {
		all = append(all, header)
	}
	// Sort by UpdatedAt descending (most recently updated first / LIFO).
	slices.SortFunc(all, func(a, b DialogueHeader) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return iterator.FromSlice(all), nil
}
