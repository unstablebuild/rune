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

package idehistory

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc/docpb"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	tcomponent "unstable.build/rune/internal/component"
	localstoragerpc "unstable.build/rune/internal/localstorage/storagerpc"
	"unstable.build/rune/internal/term/vte"
)

func mustURI(t *testing.T, s string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(s)
	require.NoError(t, err)
	return u
}

// countingStorage wraps a storageapi.Service and counts each call.
type countingStorage struct {
	storageapi.Service

	mu      sync.Mutex
	getN    int
	setN    int
	deleteN int
	setErr  error
}

func newCountingStorage() *countingStorage {
	return &countingStorage{Service: storagestub.NewInMemoryService()}
}

func (s *countingStorage) Get(ctx context.Context, id string, doc any) error {
	s.mu.Lock()
	s.getN++
	s.mu.Unlock()
	return s.Service.Get(ctx, id, doc)
}

func (s *countingStorage) Set(ctx context.Context, id string, doc any) error {
	s.mu.Lock()
	s.setN++
	err := s.setErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Service.Set(ctx, id, doc)
}

func (s *countingStorage) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	s.deleteN++
	s.mu.Unlock()
	return s.Service.Delete(ctx, id)
}

func (s *countingStorage) counts() (get, set, del int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getN, s.setN, s.deleteN
}

// failingCloseStorage tracks whether Close was called.
type failingCloseStorage struct {
	storageapi.Service
	closed atomic.Bool
}

func (s *failingCloseStorage) Close() error {
	s.closed.Store(true)
	return nil
}

func sampleState(uri workspaceapi.URI) State {
	return State{
		Files: []File{{
			URI:    uri,
			OpenAt: time.Unix(123, 0).UTC(),
			Cursor: term.Coordinates{X: 1, Y: 2},
		}},
		HasLayout: true,
		Layout:    tcomponent.TileLayout{},
	}
}

func TestStoreLoadRoundTrip(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///roundtrip")
	want := sampleState(uri)

	require.NoError(t, store.StoreWorkspaceState(
		context.Background(), uri, want))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, got.Files, 1)
	assert.Equal(t, want.Files[0].URI, got.Files[0].URI)
	assert.Equal(t, want.Files[0].Cursor, got.Files[0].Cursor)
	assert.True(t, got.HasLayout)
}

func TestStoreNestedLayoutRoundTrip(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///nested-layout")
	layout := tcomponent.TileLayout{
		Split: tcomponent.SplitOrientationVertical,
		Children: []tcomponent.TileLayout{
			{WindowID: 1, Children: []tcomponent.TileLayout{}},
			{
				Split: tcomponent.SplitOrientationHorizontal,
				Children: []tcomponent.TileLayout{
					{WindowID: 2, Children: []tcomponent.TileLayout{}},
					{WindowID: 3, Children: []tcomponent.TileLayout{}},
				},
			},
		},
	}
	require.NoError(t, store.StoreWorkspaceState(
		context.Background(), uri,
		State{HasLayout: true, Layout: layout}))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.True(t, got.HasLayout)
	assert.Equal(t, layout, got.Layout)
}

func TestStoreWorkspaceURIsAreIsolated(t *testing.T) {
	store := New(newCountingStorage())
	uri1 := mustURI(t, "memory:///isolated/one")
	uri2 := mustURI(t, "memory:///isolated/two")

	require.NoError(t, store.StoreWorkspaceState(
		context.Background(), uri1, State{
			Files: []File{{URI: uri1, OpenAt: time.Unix(1, 0)}},
		}))
	require.NoError(t, store.StoreWorkspaceState(
		context.Background(), uri2, State{
			Files: []File{{URI: uri2, OpenAt: time.Unix(2, 0)}},
		}))

	got1, err := store.LoadWorkspaceState(context.Background(), uri1)
	require.NoError(t, err)
	require.Len(t, got1.Files, 1)
	assert.Equal(t, uri1, got1.Files[0].URI)

	got2, err := store.LoadWorkspaceState(context.Background(), uri2)
	require.NoError(t, err)
	require.Len(t, got2.Files, 1)
	assert.Equal(t, uri2, got2.Files[0].URI)
}

func TestClearWorkspaceStateRemoves(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///clear")

	require.NoError(t, store.StoreWorkspaceState(
		context.Background(), uri, sampleState(uri)))
	require.NoError(t, store.ClearWorkspaceState(context.Background(), uri))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.True(t, got.IsEmpty())
}

func TestClearMissingDocIsNotAnError(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///clear-missing")
	require.NoError(t, store.ClearWorkspaceState(context.Background(), uri))
}

func TestLoadMissingDocReturnsZeroState(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///missing")

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.True(t, got.IsEmpty())
}

func TestStoreReturnsBackendError(t *testing.T) {
	cs := newCountingStorage()
	cs.setErr = errors.New("boom")
	store := New(cs)
	uri := mustURI(t, "memory:///err")

	err := store.StoreWorkspaceState(
		context.Background(), uri, sampleState(uri))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestLastSessionRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		store []Session
		want  Session
	}{
		{
			name: "never stored",
			want: Session{},
		},
		{
			name: "workspaces and focus",
			store: []Session{{
				Workspaces: []SessionWorkspace{
					{URI: mustURI(t, "memory:///one"), Slot: 0},
					{URI: mustURI(t, "memory:///two"), Slot: 3},
				},
				FocusSlot: 3,
				SavedAt:   time.Unix(1700000000, 0).UTC(),
			}},
			want: Session{
				Workspaces: []SessionWorkspace{
					{URI: mustURI(t, "memory:///one"), Slot: 0},
					{URI: mustURI(t, "memory:///two"), Slot: 3},
				},
				FocusSlot: 3,
				SavedAt:   time.Unix(1700000000, 0).UTC(),
			},
		},
		{
			name: "last write wins",
			store: []Session{
				{Workspaces: []SessionWorkspace{
					{URI: mustURI(t, "memory:///stale"), Slot: 0},
				}},
				{Workspaces: []SessionWorkspace{
					{URI: mustURI(t, "memory:///fresh"), Slot: 1},
				}},
			},
			want: Session{Workspaces: []SessionWorkspace{
				{URI: mustURI(t, "memory:///fresh"), Slot: 1},
			}},
		},
		{
			name: "empty session clears the offer",
			store: []Session{
				{Workspaces: []SessionWorkspace{
					{URI: mustURI(t, "memory:///gone"), Slot: 0},
				}},
				{},
			},
			want: Session{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := New(newCountingStorage())
			for _, s := range tc.store {
				require.NoError(t, store.StoreLastSession(context.Background(), s))
			}
			got, err := store.LoadLastSession(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want.Workspaces, got.Workspaces)
			assert.Equal(t, tc.want.FocusSlot, got.FocusSlot)
			assert.True(t, tc.want.SavedAt.Equal(got.SavedAt))
		})
	}
}

// TestLastSessionSkipsUnparseableURI pins that a document written by a
// future or corrupted version cannot break startup.
func TestLastSessionSkipsUnparseableURI(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	require.NoError(t, cs.Set(context.Background(), lastSessionDocumentID,
		lastSessionDocument{
			Kind: lastSessionDocumentKind,
			Workspaces: []sessionWorkspaceDoc{
				{URI: "not-a-uri", Slot: 0},
				{URI: "memory:///good", Slot: 1},
			},
		}))

	got, err := store.LoadLastSession(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Workspaces, 1)
	assert.Equal(t, mustURI(t, "memory:///good"), got.Workspaces[0].URI)
	assert.Equal(t, 1, got.Workspaces[0].Slot)
}

func TestStoreNotClosedByCloseStore(t *testing.T) {
	backing := &failingCloseStorage{Service: storagestub.NewInMemoryService()}
	_ = New(backing) // never call Close on the backing service
	assert.False(t, backing.closed.Load(),
		"idehistory.Store must not close the underlying storage")
}

func TestStoreWorkspaceStateForClose(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///for-close")

	snap := stubSnapshotter{
		name:      "renamed",
		layout:    tcomponent.TileLayout{WindowID: 42},
		hasLayout: true,
		terminals: []TerminalSession{{Name: "t1"}},
		tasks:     []TaskSession{{Name: "task1", Cmd: "echo"}},
		extensions: []ExtensionTab{{
			URI:      mustURI(t, "fake://host/chat"),
			Icon:     '󱫆',
			Name:     "chat",
			WindowID: 42,
			Focus:    true,
		}},
	}
	require.NoError(t, store.StoreWorkspaceStateForClose(
		context.Background(), uri, snap))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.True(t, got.HasLayout)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "t1", got.Terminals[0].Name)
	require.Len(t, got.Tasks, 1)
	assert.Equal(t, "task1", got.Tasks[0].Name)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, snap.extensions, got.Extensions)
}

func TestExtensionTabsRoundTrip(t *testing.T) {
	uri := mustURI(t, "memory:///extension-tabs")
	chat := ExtensionTab{
		URI:      mustURI(t, "rune-agent://model/rolling-fox"),
		Icon:     '󱫆',
		Name:     "rolling-fox",
		WindowID: 3,
		Focus:    true,
	}
	other := ExtensionTab{
		URI:      mustURI(t, "other://host/view"),
		WindowID: 4,
	}
	tests := []struct {
		name string
		docs []extensionTabDoc
		want []ExtensionTab
	}{
		{
			name: "no extension tabs",
		},
		{
			name: "every field survives",
			docs: []extensionTabDoc{
				{URI: chat.URI.String(), Icon: string(chat.Icon), Name: chat.Name,
					WindowID: chat.WindowID, Focus: chat.Focus},
				{URI: other.URI.String(),
					WindowID: other.WindowID},
			},
			want: []ExtensionTab{chat, other},
		},
		{
			name: "unparsable uri is skipped",
			docs: []extensionTabDoc{
				{URI: "not-a-uri", WindowID: 9},
				{URI: chat.URI.String(), Icon: string(chat.Icon), Name: chat.Name,
					WindowID: chat.WindowID, Focus: chat.Focus},
			},
			want: []ExtensionTab{chat},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newCountingStorage()
			store := New(cs)
			doc := newWorkspaceStateDocument(uri, State{})
			doc.Extensions = tt.docs
			require.NoError(t, cs.Set(context.Background(),
				workspaceStateDocumentID(uri), doc))

			got, err := store.LoadWorkspaceState(context.Background(), uri)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Extensions)

			require.NoError(t, store.StoreWorkspaceState(
				context.Background(), uri, got))
			again, err := store.LoadWorkspaceState(context.Background(), uri)
			require.NoError(t, err)
			assert.Equal(t, tt.want, again.Extensions)
		})
	}
}

func TestStateIsEmpty(t *testing.T) {
	uri := mustURI(t, "memory:///empty")
	tests := []struct {
		name  string
		state State
		want  bool
	}{
		{name: "zero", want: true},
		{name: "layout and name only",
			state: State{Name: "ws", HasLayout: true}, want: true},
		{name: "files", state: State{Files: []File{{URI: uri}}}},
		{name: "terminals",
			state: State{Terminals: []TerminalSession{{Name: "t"}}}},
		{name: "tasks", state: State{Tasks: []TaskSession{{Name: "t"}}}},
		{name: "extension tabs",
			state: State{Extensions: []ExtensionTab{{URI: uri}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.IsEmpty())
		})
	}
}

// stubSnapshotter is a minimal Snapshotter for tracker tests.
type stubSnapshotter struct {
	name       string
	layout     tcomponent.TileLayout
	hasLayout  bool
	terminals  []TerminalSession
	tasks      []TaskSession
	extensions []ExtensionTab
	winIDs     map[string]uint64
}

func (s stubSnapshotter) Name() string                 { return s.name }
func (s stubSnapshotter) Terminals() []TerminalSession { return s.terminals }
func (s stubSnapshotter) Tasks() []TaskSession         { return s.tasks }
func (s stubSnapshotter) ExtensionTabs() []ExtensionTab {
	return s.extensions
}
func (s stubSnapshotter) Layout() (tcomponent.TileLayout, bool) {
	return s.layout, s.hasLayout
}
func (s stubSnapshotter) FileWindowIDs() map[string]uint64 { return s.winIDs }

func TestTrackPersistsOnEditorEvents(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	uri := mustURI(t, "memory:///track")
	fileURI := mustURI(t, "memory:///track/a.go")

	tr := &tracker{
		store: store,
		uri:   uri,
		snap:  stubSnapshotter{},
		ctx:   context.Background(),
		files: make(map[string]File),
		skip:  nil,
	}

	// Open: persists.
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  fileURI,
	})
	state, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, state.Files, 1)

	// Cursor: does NOT persist immediately.
	_, before, _ := cs.counts()
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  fileURI,
		From: term.Coordinates{X: 5, Y: 7},
	})
	_, after, _ := cs.counts()
	assert.Equal(t, before, after, "Cursor event must not write")

	// Edit: marks dirty but does NOT persist.
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeEdit,
		URI:  fileURI,
	})
	_, after2, _ := cs.counts()
	assert.Equal(t, before, after2, "Edit event must not write")

	// Flush event: persists with Dirty cleared.
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeFlush,
		URI:  fileURI,
	})
	state, err = store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, state.Files, 1)
	assert.False(t, state.Files[0].Dirty)

	// Close: persists with file removed.
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeClose,
		URI:  fileURI,
	})
	state, err = store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Empty(t, state.Files)
}

func TestTrackDropsSkippedURIEvents(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///explorer")
	skippedURI := mustURI(t, "memory:///skip-me")

	tr := &tracker{
		store: store,
		uri:   uri,
		snap:  stubSnapshotter{},
		ctx:   context.Background(),
		files: make(map[string]File),
		skip:  []workspaceapi.URI{skippedURI},
	}
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  skippedURI,
	})
	state, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Empty(t, state.Files,
		"skip-listed URI must not enter persisted state")
}

// TestTrackDropsURIsUnderSkippedPrefix covers pseudo-buffers that mint a
// resource per invocation, such as the :gitshow diff popup. Their URIs
// cannot be enumerated up front, so the skip entry names the namespace
// and every resource under it must stay out of the persisted state —
// otherwise a reload tries to reopen a buffer that was never a file.
func TestTrackDropsURIsUnderSkippedPrefix(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///explorer")

	tr := &tracker{
		store: store,
		uri:   uri,
		snap:  stubSnapshotter{},
		ctx:   context.Background(),
		files: make(map[string]File),
		skip:  []workspaceapi.URI{mustURI(t, "memory:///gitshow")},
	}
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  mustURI(t, "memory:///gitshow/a/b.go.diff?n=2"),
	})
	state, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Empty(t, state.Files,
		"a resource under a skipped namespace must not be persisted")

	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  mustURI(t, "memory:///gitshowcase/a.go"),
	})
	state, err = store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Len(t, state.Files, 1,
		"a sibling that merely shares a textual prefix must still be tracked")
}

// oldMaxMessageSize is firstmover's historical gRPC frame cap. A workspace
// state document whose marshaled size exceeded it used to fail on load with
// codes.ResourceExhausted — exactly the symptom RUNE-245 fixes by streaming
// the payload in sub-cap chunks.
const oldMaxMessageSize = 4 << 20

// cappedGRPCStorage starts an in-process gRPC storage server and client whose
// frames are bounded by oldMaxMessageSize, mirroring the firstmover
// leader/follower wiring that surfaced the load failure.
func cappedGRPCStorage(t *testing.T) storageapi.Service {
	t.Helper()
	marshaler := docbson.Marshaler()
	backend := storagestub.NewInMemoryServiceWithMarshaler(marshaler)

	gsrv := grpc.NewServer(
		grpc.MaxSendMsgSize(oldMaxMessageSize),
		grpc.MaxRecvMsgSize(oldMaxMessageSize),
	)
	docpb.RegisterDocumentStoreServer(gsrv, localstoragerpc.NewServer(backend, marshaler))

	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	go func() { _ = gsrv.Serve(lis) }()

	client, err := storagerpc.NewClient(lis.Addr(), marshaler,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(oldMaxMessageSize),
			grpc.MaxCallRecvMsgSize(oldMaxMessageSize),
		),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
		gsrv.Stop()
		_ = lis.Close()
	})
	return client
}

// largeTerminalSnapshot builds a terminal snapshot whose marshaled size
// exceeds oldMaxMessageSize so the round-trip exercises the streamed path.
func largeTerminalSnapshot() vte.Snapshot {
	return terminalSnapshot(256, 1024)
}

func terminalSnapshot(rows, cols int) vte.Snapshot {
	cells := make([][]term.Cell, rows)
	for y := range cells {
		row := make([]term.Cell, cols)
		for x := range row {
			row[x] = term.Cell{Ch: rune('A' + (x+y)%26), Width: 1, Bytes: 1}
		}
		cells[y] = row
	}
	return vte.Snapshot{
		Title:  "big",
		Width:  cols,
		Height: rows,
		Primary: vte.ScreenSnapshot{
			Cursor: term.Coordinates{X: 1, Y: 2},
			Cells:  cells,
		},
	}
}

func TestStoreLoadRoundTripOversizedTerminalSnapshot(t *testing.T) {
	store := New(cappedGRPCStorage(t))
	uri := mustURI(t, "memory:///oversized")

	snap := largeTerminalSnapshot()
	// Sanity-check the document is actually beyond the historical cap so the
	// test would have reproduced the original load failure.
	require.Greater(t,
		len(storageapi.Encode(docbson.Marshaler(),
			map[string]any{"snap": snap}, false)),
		oldMaxMessageSize)

	want := State{
		Files: []File{{URI: uri, OpenAt: time.Unix(1, 0).UTC()}},
		Terminals: []TerminalSession{{
			Name:     "term-1",
			Snapshot: snap,
			Visible:  true,
			Focus:    true,
			WindowID: 7,
		}},
	}

	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, want))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, want.Terminals[0].Name, got.Terminals[0].Name)
	assert.Equal(t, want.Terminals[0].WindowID, got.Terminals[0].WindowID)
	assert.Equal(t, snap.Width, got.Terminals[0].Snapshot.Width)
	assert.Equal(t, snap.Height, got.Terminals[0].Snapshot.Height)
	assert.Equal(t, snap.Primary.Cells, got.Terminals[0].Snapshot.Primary.Cells)
}

// rawDocs decodes every document in svc's own partition as a map.
func rawDocs(t *testing.T, svc storageapi.Service) []map[string]any {
	t.Helper()
	it, err := svc.List(context.Background(), nil)
	require.NoError(t, err)
	defer it.Close()
	var docs []map[string]any
	for it.HasNext() {
		var raw map[string]any
		require.NoError(t, it.NextTo(&raw))
		docs = append(docs, raw)
	}
	return docs
}

// TestTerminalSnapshotsLiveOutsideTheWorkspaceStatePartition pins the
// storage shape ListWorkspaceURIs depends on: listing a partition
// decodes every document in it to evaluate filters, so a multi-MB
// terminal snapshot must never share the partition with the documents
// the scavenger seeds from at startup.
func TestTerminalSnapshotsLiveOutsideTheWorkspaceStatePartition(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	uri := mustURI(t, "memory:///partitioned")
	snap := terminalSnapshot(24, 80)

	require.NoError(t, store.StoreWorkspaceStateForClose(
		context.Background(), uri, stubSnapshotter{
			terminals: []TerminalSession{{Name: "big", Snapshot: snap, WindowID: 7}},
			tasks:     []TaskSession{{Name: "task1", Cmd: "echo"}},
		}))

	docs := rawDocs(t, cs)
	require.Len(t, docs, 1)
	assert.NotContains(t, docs[0], "terminals")
	assert.Less(t, len(storageapi.Encode(docbson.Marshaler(), docs[0], false)), 16<<10,
		"the listed workspace-state document must stay small")

	terminals := rawDocs(t, storageapi.WithPartition(cs, TerminalStatePartition))
	require.Len(t, terminals, 1)
	assert.Contains(t, terminals[0], "terminals")

	uris, err := store.ListWorkspaceURIs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []workspaceapi.URI{uri}, uris)

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "big", got.Terminals[0].Name)
	assert.Equal(t, uint64(7), got.Terminals[0].WindowID)
	assert.Equal(t, snap.Primary.Cells, got.Terminals[0].Snapshot.Primary.Cells)
	require.Len(t, got.Tasks, 1)
}

// TestLoadFallsBackToInlineTerminals covers documents written before
// terminals moved to their own partition: the snapshots they carry
// inline must still restore, and the next full store rewrites the
// state in the new shape.
func TestLoadFallsBackToInlineTerminals(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	uri := mustURI(t, "memory:///legacy")
	require.NoError(t, cs.Set(context.Background(), workspaceStateDocumentID(uri),
		workspaceStateDocument{
			Kind:         workspaceStateDocumentKind,
			WorkspaceURI: uri.String(),
			Files:        []fileDoc{{URI: uri.String()}},
			Terminals:    []terminalDoc{{Name: "inline", WindowID: 3}},
		}))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "inline", got.Terminals[0].Name)
	assert.Equal(t, uint64(3), got.Terminals[0].WindowID)

	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, got))
	docs := rawDocs(t, cs)
	require.Len(t, docs, 1)
	assert.NotContains(t, docs[0], "terminals",
		"a full store must migrate the terminals out of the legacy document")
	got, err = store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "inline", got.Terminals[0].Name)
}

// TestListWorkspaceURIsThroughACappedFollower reproduces the startup
// failure on a datadir led by another process: a legacy document whose
// inline snapshots exceed the gRPC frame cap must not abort the listing
// the scavenger seeds from, since List, unlike Get, is not chunked.
func TestListWorkspaceURIsThroughACappedFollower(t *testing.T) {
	svc := cappedGRPCStorage(t)
	store := New(svc)
	small := mustURI(t, "memory:///small")
	legacy := mustURI(t, "memory:///legacy")
	require.NoError(t, store.StoreWorkspaceState(context.Background(), small,
		State{Files: []File{{URI: small}}}))
	require.NoError(t, svc.Set(context.Background(), workspaceStateDocumentID(legacy),
		workspaceStateDocument{
			Kind:         workspaceStateDocumentKind,
			WorkspaceURI: legacy.String(),
			Files:        []fileDoc{{URI: legacy.String()}},
			Terminals: []terminalDoc{{
				Name: "inline", Snapshot: largeTerminalSnapshot(),
			}},
		}))

	uris, err := store.ListWorkspaceURIs(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []workspaceapi.URI{small, legacy}, uris)

	// The listing is a read: what it cannot carry over the wire it must
	// leave for the workspace's own reopen to migrate.
	got, err := store.LoadWorkspaceState(context.Background(), legacy)
	require.NoError(t, err)
	require.Len(t, got.Files, 1)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "inline", got.Terminals[0].Name)
}

func TestStoreWithoutTerminalsClearsThePreviousTerminalState(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///no-terminals")
	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, State{
		Terminals: []TerminalSession{{Name: "t1"}},
	}))
	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, State{
		Files: []File{{URI: uri}},
	}))

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Empty(t, got.Terminals)
	assert.Len(t, got.Files, 1)
}

func TestClearWorkspaceStateRemovesTerminalState(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	uri := mustURI(t, "memory:///clear-terminals")
	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, State{
		Terminals: []TerminalSession{{Name: "t1"}},
	}))
	require.NoError(t, store.ClearWorkspaceState(context.Background(), uri))

	assert.Empty(t, rawDocs(t, storageapi.WithPartition(cs, TerminalStatePartition)))
	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.True(t, got.IsEmpty())
}

// terminalCountingSnapshotter fails the test if Terminals is called:
// snapshotting every open terminal on each editor event is the stall
// this shape exists to avoid.
type terminalCountingSnapshotter struct {
	stubSnapshotter
	t *testing.T
}

func (s terminalCountingSnapshotter) Terminals() []TerminalSession {
	s.t.Error("tracker must not snapshot terminals on editor events")
	return nil
}

func TestTrackerDoesNotSnapshotTerminalsOnEditorEvents(t *testing.T) {
	cs := newCountingStorage()
	store := New(cs)
	uri := mustURI(t, "memory:///no-terminal-snapshot")
	require.NoError(t, store.StoreWorkspaceState(context.Background(), uri, State{
		Terminals: []TerminalSession{{Name: "from-close"}},
	}))

	tr := &tracker{
		store: store,
		uri:   uri,
		snap:  terminalCountingSnapshotter{t: t, stubSnapshotter: stubSnapshotter{name: "ws"}},
		ctx:   context.Background(),
		files: make(map[string]File),
	}
	for _, typ := range []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeFlush, textapi.EventTypeClose,
	} {
		tr.Handle(context.Background(), textapi.Event{
			Type: typ, URI: mustURI(t, "memory:///no-terminal-snapshot/a.go"),
		})
	}

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Equal(t, "ws", got.Name)
	require.Len(t, got.Terminals, 1, "editor events must leave the terminal state untouched")
	assert.Equal(t, "from-close", got.Terminals[0].Name)
}

// A crash skips the close-time snapshot, so the extension tabs must ride
// along with the per-editor-event writes.
func TestTrackerPersistsExtensionTabsOnEditorEvents(t *testing.T) {
	store := New(newCountingStorage())
	uri := mustURI(t, "memory:///track-extension-tabs")
	tabs := []ExtensionTab{{
		URI:      mustURI(t, "rune-agent://model/rolling-fox"),
		WindowID: 2,
	}}
	tr := &tracker{
		store: store,
		uri:   uri,
		snap:  stubSnapshotter{extensions: tabs},
		ctx:   context.Background(),
		files: make(map[string]File),
	}
	tr.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  mustURI(t, "memory:///track-extension-tabs/a.go"),
	})

	got, err := store.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	assert.Equal(t, tabs, got.Extensions)
}
