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

package symboldb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bluebolt "github.com/unstablebuild/blue/document/bolt"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/workspace"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// progressUpdate records one UpdateNotificationProgress call.
type progressUpdate struct {
	id, message     string
	progress, total int64
}

// recordingNotifications captures notifications so tests can assert
// on indexing progress reporting.
type recordingNotifications struct {
	mu       sync.Mutex
	notified []string
	updates  []progressUpdate
}

var _ browserapi.Notifications = (*recordingNotifications)(nil)

func (n *recordingNotifications) Notify(
	_ browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = append(n.notified, fmt.Sprintf(msg, args...))
	return fmt.Sprintf("n%d", len(n.notified)), nil
}

func (n *recordingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if total <= 0 || progress > total {
		return fmt.Errorf("invalid progress %d/%d", progress, total)
	}
	n.updates = append(n.updates, progressUpdate{id, message, progress, total})
	return nil
}

func (n *recordingNotifications) snapshot() ([]string, []progressUpdate) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notified...),
		append([]progressUpdate(nil), n.updates...)
}

// syncTick runs scheduled callbacks inline; recordingNotifications is
// mutex-guarded so inline execution from indexer goroutines stays safe.
var syncTick = func(fn func()) bool { fn(); return true }

// loopStateNotifications mirrors the production notisRouter: every call
// reads state that only the event loop goroutine may touch, so the race
// detector flags notifications issued directly from indexer goroutines.
type loopStateNotifications struct{ state *int }

func (n loopStateNotifications) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	_ = *n.state
	return "id", nil
}

func (n loopStateNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n loopStateNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	_ = *n.state
	return nil
}

func TestScanNotificationsRunOnScheduledTicks(t *testing.T) {
	e := newEnv(t)
	uriA := e.writeFile(t, "a/a.go")
	uriB := e.writeFile(t, "b/b.go")
	e.fake.setGoFile(uriA, goFile{pkg: "mypkg", defs: []string{"A"}})
	e.fake.setGoFile(uriB, goFile{pkg: "mypkg", defs: []string{"B"}})

	state := 0
	ticks := make(chan func(), 1024)
	stop := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case fn := <-ticks:
				state++
				fn()
				state++
			case <-stop:
				return
			}
		}
	}()

	e.notifyOverride = loopStateNotifications{state: &state}
	e.schedule = func(fn func()) bool {
		select {
		case ticks <- fn:
			return true
		default:
			return false
		}
	}
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))
	require.NoError(t, p.Close())
	close(stop)
	<-loopDone
}

// queryGate blocks fake per-file queries for one URI until released,
// signaling reached on the first blocked call.
type queryGate struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newQueryGate() *queryGate {
	return &queryGate{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// fakeParser is a canned syntaxapi.Parser: per-file Query/QueryNode
// results are looked up by URI and query string, and the workspace-wide
// resolve/list entry points return fixed values while counting calls.
type fakeParser struct {
	mu             sync.Mutex
	queryResults   map[string]map[string][]syntaxapi.Result
	nodeResults    map[string][]syntaxapi.Result
	fileCalls      map[string]int
	gates          map[string]*queryGate
	resolveCalls   int
	listCalls      int
	resolveMatches []syntaxapi.Match
	listNames      []string
}

var _ syntaxapi.Parser = (*fakeParser)(nil)

func newFakeParser() *fakeParser {
	return &fakeParser{
		queryResults: make(map[string]map[string][]syntaxapi.Result),
		nodeResults:  make(map[string][]syntaxapi.Result),
		fileCalls:    make(map[string]int),
		gates:        make(map[string]*queryGate),
	}
}

func (f *fakeParser) Query(
	file workspaceapi.URI, query string, _ []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	us := file.String()
	f.mu.Lock()
	f.fileCalls[us]++
	gate := f.gates[us]
	res := f.queryResults[us][query]
	f.mu.Unlock()
	if gate != nil {
		gate.once.Do(func() { close(gate.reached) })
		<-gate.release
	}
	return iterator.FromSlice(res), nil
}

// NewQuerySession implements Backing; the fake querier serves the canned
// per-file results grouped per match.
func (f *fakeParser) NewQuerySession() symbolresolve.QuerySession {
	return &fakeFileQuerier{f: f}
}

type fakeFileQuerier struct{ f *fakeParser }

// QueryMulti groups the canned results: two-capture queries are stored
// as consecutive (first, second) captures per match, single-capture and
// node queries as one capture per match.
func (q *fakeFileQuerier) QueryMulti(
	_ context.Context, file workspaceapi.URI,
	queries []symbolresolve.MultiQuery,
) ([]symbolresolve.MultiResult, error) {
	f := q.f
	us := file.String()
	f.mu.Lock()
	f.fileCalls[us]++
	gate := f.gates[us]
	byQuery := f.queryResults[us]
	nodes := f.nodeResults[us]
	f.mu.Unlock()
	if gate != nil {
		gate.once.Do(func() { close(gate.reached) })
		<-gate.release
	}
	var out []symbolresolve.MultiResult
	for _, mq := range queries {
		if mq.Nodes != 0 {
			for _, r := range nodes {
				out = append(out, symbolresolve.MultiResult{
					QueryID: mq.ID, Match: []syntaxapi.Result{r},
				})
			}
			continue
		}
		res := byQuery[mq.Query]
		if len(mq.Captures) == 2 {
			for i := 0; i+1 < len(res); i += 2 {
				out = append(out, symbolresolve.MultiResult{
					QueryID: mq.ID,
					Match:   []syntaxapi.Result{res[i], res[i+1]},
				})
			}
			continue
		}
		for _, r := range res {
			out = append(out, symbolresolve.MultiResult{
				QueryID: mq.ID, Match: []syntaxapi.Result{r},
			})
		}
	}
	return out, nil
}

func (q *fakeFileQuerier) Close() error { return nil }

func (f *fakeParser) QueryNode(
	file workspaceapi.URI, _ syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	us := file.String()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileCalls[us]++
	return iterator.FromSlice(f.nodeResults[us]), nil
}

func (f *fakeParser) Search(
	string, []string, ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (f *fakeParser) SearchNode(
	syntaxapi.NodeCaptureName, ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (f *fakeParser) Highlight(
	workspaceapi.URI, string,
) (iterator.Iterator[textapi.Location], error) {
	return iterator.Empty[textapi.Location](), nil
}

func (f *fakeParser) ResolveSymbol(
	context.Context, string, syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls++
	return iterator.FromSlice(f.resolveMatches), nil
}

func (f *fakeParser) ListReferencedSymbols(
	context.Context,
) (iterator.Iterator[string], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return iterator.FromSlice(f.listNames), nil
}

func (f *fakeParser) callsFor(uri workspaceapi.URI) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fileCalls[uri.String()]
}

func (f *fakeParser) totalResolveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveCalls
}

func (f *fakeParser) totalListCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

func (f *fakeParser) setGate(uri workspaceapi.URI, g *queryGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gates[uri.String()] = g
}

// goFile describes the canned per-file query results for a Go file.
type goFile struct {
	pkg     string
	imports []string
	aliases map[string]string // import path → explicit alias
	// refs are (pkg, sym) pairs matched by the qualified_type query.
	refs [][2]string
	// selRefs are (pkg, sym) pairs matched by the selector query,
	// which requires the pkg alias to be imported.
	selRefs [][2]string
	defs    []string
	methods [][2]string // (receiver, method)
}

func (f *fakeParser) setGoFile(uri workspaceapi.URI, gf goFile) {
	spec := symbolresolve.Go
	m := make(map[string][]syntaxapi.Result)
	m[spec.PackageClauseQuery] = []syntaxapi.Result{{
		File: uri, Text: gf.pkg,
		CaptureName: spec.PackageClauseCaptures[0],
	}}
	var paths []syntaxapi.Result
	for _, p := range gf.imports {
		paths = append(paths, syntaxapi.Result{
			File: uri, Text: `"` + p + `"`,
			CaptureName: spec.ImportPathCaptures[0],
		})
	}
	m[spec.ImportPathQuery] = paths
	var aliases []syntaxapi.Result
	aliasPaths := make([]string, 0, len(gf.aliases))
	for p := range gf.aliases {
		aliasPaths = append(aliasPaths, p)
	}
	sort.Strings(aliasPaths)
	for _, p := range aliasPaths {
		aliases = append(aliases,
			syntaxapi.Result{
				File: uri, Text: gf.aliases[p],
				CaptureName: spec.ImportAliasCaptures[0],
			},
			syntaxapi.Result{
				File: uri, Text: `"` + p + `"`,
				CaptureName: spec.ImportAliasCaptures[1],
			})
	}
	m[spec.ImportAliasQuery] = aliases
	var refs []syntaxapi.Result
	for i, r := range gf.refs {
		refs = append(refs,
			syntaxapi.Result{
				File: uri, Text: r[0],
				CaptureName: spec.RefQueries[0].Captures[0],
			},
			syntaxapi.Result{
				File: uri, Text: r[1],
				CaptureName: spec.RefQueries[0].Captures[1],
				From:        term.Coordinates{Y: i + 1},
			})
	}
	m[spec.RefQueries[0].Query] = refs
	var sel []syntaxapi.Result
	for i, r := range gf.selRefs {
		sel = append(sel,
			syntaxapi.Result{
				File: uri, Text: r[0],
				CaptureName: spec.RefQueries[1].Captures[0],
			},
			syntaxapi.Result{
				File: uri, Text: r[1],
				CaptureName: spec.RefQueries[1].Captures[1],
				From:        term.Coordinates{Y: 100 + i},
			})
	}
	m[spec.RefQueries[1].Query] = sel
	var methods []syntaxapi.Result
	for i, mm := range gf.methods {
		methods = append(methods,
			syntaxapi.Result{
				File: uri, Text: mm[0],
				CaptureName: spec.MethodDefCaptures[0],
			},
			syntaxapi.Result{
				File: uri, Text: mm[1],
				CaptureName: spec.MethodDefCaptures[1],
				From:        term.Coordinates{Y: 200 + i},
			})
	}
	m[spec.MethodDefQuery] = methods
	var defs []syntaxapi.Result
	for i, d := range gf.defs {
		defs = append(defs, syntaxapi.Result{
			File: uri, Text: d,
			From: term.Coordinates{Y: 300 + i},
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryResults[uri.String()] = m
	f.nodeResults[uri.String()] = defs
}

type env struct {
	dir    string
	fs     workspaceapi.FileSystem
	root   workspaceapi.URI
	db     storageapi.Service
	fake   *fakeParser
	notify *recordingNotifications
	// notifyOverride replaces notify when a test needs a different
	// Notifications implementation.
	notifyOverride browserapi.Notifications
	schedule       func(func()) bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return &env{
		dir:      dir,
		fs:       scheme,
		root:     root,
		db:       storagestub.NewInMemoryService(),
		fake:     newFakeParser(),
		notify:   &recordingNotifications{},
		schedule: syncTick,
	}
}

func (e *env) start(t *testing.T) *Parser {
	t.Helper()
	var notify browserapi.Notifications = e.notify
	if e.notifyOverride != nil {
		notify = e.notifyOverride
	}
	p, err := New(e.fake, e.fs, e.root, e.db, notify, e.schedule)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func (e *env) writeFile(t *testing.T, name string) workspaceapi.URI {
	t.Helper()
	require.NoError(t, os.MkdirAll(
		filepath.Dir(filepath.Join(e.dir, name)), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(e.dir, name), []byte("x"), 0o644))
	uri, err := e.fs.URI(name)
	require.NoError(t, err)
	return uri
}

func (e *env) touch(t *testing.T, name string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, os.Chtimes(
		filepath.Join(e.dir, name), now, now.Add(time.Hour)))
}

func resolve(
	t *testing.T, p *Parser, name string,
) []syntaxapi.Match {
	t.Helper()
	it, err := p.ResolveSymbol(context.Background(), name, nil)
	require.NoError(t, err)
	matches, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return matches
}

func listReferenced(t *testing.T, p *Parser) []string {
	t.Helper()
	it, err := p.ListReferencedSymbols(context.Background())
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	sort.Strings(names)
	return names
}

func TestInitialScanServesFromIndex(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:     "mypkg",
		imports: []string{"github.com/x/iterator"},
		refs:    [][2]string{{"iterator", "Iterator"}},
		defs:    []string{"Widget"},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "iterator.Iterator")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)
	assert.Equal(t, term.Coordinates{Y: 1}, matches[0].Pos)
	assert.Equal(t, "iterator.Iterator", matches[0].Display)

	matches = resolve(t, p, "mypkg.Widget")
	require.Len(t, matches, 1)
	assert.Equal(t, term.Coordinates{Y: 300}, matches[0].Pos)

	assert.Equal(t, []string{"iterator.Iterator", "mypkg.Widget"},
		listReferenced(t, p))
	assert.Zero(t, e.fake.totalResolveCalls())
	assert.Zero(t, e.fake.totalListCalls())
}

func TestMissAfterScanReturnsNotFound(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	e.fake.resolveMatches = []syntaxapi.Match{{URI: "backing", Display: "other.Sym"}}
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "other.Sym")
	assert.Empty(t, matches)
	assert.Zero(t, e.fake.totalResolveCalls())
}

func TestMissBeforeScanPassesThroughToBacking(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	e.fake.resolveMatches = []syntaxapi.Match{{URI: "backing", Display: "other.Sym"}}

	// Gate the file so the initial scan blocks and never marks the
	// index as authoritative while the miss is resolved.
	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	p := e.start(t)
	<-gate.reached

	matches := resolve(t, p, "other.Sym")
	require.Len(t, matches, 1)
	assert.Equal(t, "backing", matches[0].URI)
	assert.Equal(t, 1, e.fake.totalResolveCalls())

	close(gate.release)
	require.NoError(t, p.Wait(context.Background()))
}

func TestResolveNoDot(t *testing.T) {
	e := newEnv(t)
	p := e.start(t)
	_, err := p.ResolveSymbol(context.Background(), "nodot", nil)
	assert.ErrorIs(t, err, syntaxapi.ErrNoDot)
}

func TestStaleServingWhileReindexInFlight(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	e.touch(t, "a.go")
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeFlush, URI: uri,
	})
	<-gate.reached

	matches := resolve(t, p, "iterator.Iterator")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)
	assert.Zero(t, e.fake.totalResolveCalls())

	close(gate.release)
	require.NoError(t, p.Wait(context.Background()))
}

func TestCloseAbandonsDirtyQueue(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	e.touch(t, "a.go")
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeFlush, URI: uri,
	})
	<-gate.reached

	// The worker is blocked mid-extraction: queued entries accumulate.
	for i := range 20 {
		u, err := e.fs.URI(fmt.Sprintf("b%d.go", i))
		require.NoError(t, err)
		p.Handle(context.Background(), textapi.Event{
			Type: textapi.EventTypeCreate, URI: u,
		})
	}

	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	<-p.ctx.Done()
	close(gate.release)
	require.NoError(t, <-closed)

	p.mu.Lock()
	remaining := len(p.dirty)
	p.mu.Unlock()
	assert.NotZero(t, remaining,
		"Close must abandon the dirty queue, not drain it")
}

func TestCloseDuringInitialScanJoinsWorkers(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	p := e.start(t)
	<-gate.reached

	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	<-p.ctx.Done()
	close(gate.release)
	require.NoError(t, <-closed)
}

func TestFlushReindexesFile(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
		defs: []string{"Widget"},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Gadget"},
	})
	e.touch(t, "a.go")
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeFlush, URI: uri,
	})
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "mypkg.Gadget")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)

	// removed symbols read as an authoritative miss, not a fallback
	before := e.fake.totalResolveCalls()
	assert.Empty(t, resolve(t, p, "iterator.Iterator"))
	assert.Equal(t, before, e.fake.totalResolveCalls())
	assert.Equal(t, []string{"mypkg.Gadget"}, listReferenced(t, p))
}

func TestRemoveDropsContributions(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	require.NoError(t, os.Remove(filepath.Join(e.dir, "a.go")))
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeRemove, URI: uri,
	})
	require.NoError(t, p.Wait(context.Background()))

	before := e.fake.totalResolveCalls()
	assert.Empty(t, resolve(t, p, "iterator.Iterator"))
	assert.Equal(t, before, e.fake.totalResolveCalls())
	assert.Empty(t, listReferenced(t, p))
}

func TestConcurrentScanMergesSharedSymbols(t *testing.T) {
	e := newEnv(t)
	const files = 32
	uris := make([]string, files)
	for i := range files {
		uri := e.writeFile(t, fmt.Sprintf("f%d.go", i))
		e.fake.setGoFile(uri, goFile{
			pkg:  "mypkg",
			refs: [][2]string{{"iterator", "Iterator"}},
			defs: []string{"Widget"},
		})
		uris[i] = uri.String()
	}
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	symbols, err := e.db.Partition(symbolsPartition)
	require.NoError(t, err)
	for _, name := range []string{"iterator.Iterator", "mypkg.Widget"} {
		var doc symbolDoc
		require.NoError(t, symbols.Get(context.Background(), name, &doc))
		got := make([]string, 0, len(doc.Locs))
		for _, l := range doc.Locs {
			got = append(got, l.URI)
		}
		assert.ElementsMatchf(t, uris, got,
			"doc %q must hold every file's contribution", name)
	}
	assert.Equal(t, []string{"iterator.Iterator", "mypkg.Widget"},
		listReferenced(t, p))
}

func TestRemovalTombstonesSymbolDoc(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	gf := goFile{pkg: "mypkg", refs: [][2]string{{"iterator", "Iterator"}}}
	e.fake.setGoFile(uri, gf)
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	require.NoError(t, os.Remove(filepath.Join(e.dir, "a.go")))
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeRemove, URI: uri,
	})
	require.NoError(t, p.Wait(context.Background()))

	ctx := context.Background()
	symbols, err := e.db.Partition(symbolsPartition)
	require.NoError(t, err)
	var doc symbolDoc
	require.NoError(t, symbols.Get(ctx, "iterator.Iterator", &doc),
		"the emptied doc must remain as a tombstone")
	assert.Empty(t, doc.Locs)

	before := e.fake.totalResolveCalls()
	assert.Empty(t, resolve(t, p, "iterator.Iterator"),
		"a tombstone must read as an authoritative miss")
	assert.Equal(t, before, e.fake.totalResolveCalls())
	assert.Empty(t, listReferenced(t, p), "the names marker must be gone")

	// The returning file resurrects the tombstone in place.
	e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, gf)
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCreate, URI: uri,
	})
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "iterator.Iterator")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)
	assert.Equal(t, []string{"iterator.Iterator"}, listReferenced(t, p))
}

func TestEditEventsIgnored(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	assert.NotContains(t, EditorEvents(), textapi.EventTypeEdit)

	calls := e.fake.callsFor(uri)
	e.touch(t, "a.go")
	exit := p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeEdit, URI: uri,
	})
	assert.False(t, exit)
	require.NoError(t, p.Wait(context.Background()))
	assert.Equal(t, calls, e.fake.callsFor(uri))
}

func TestPersistenceSkipsUnchangedFiles(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))
	assert.Positive(t, e.fake.callsFor(uri))
	require.NoError(t, p.Close())

	// a fresh parser over the same storage must not re-parse anything
	fake2 := newFakeParser()
	e.fake = fake2
	p2 := e.start(t)
	require.NoError(t, p2.Wait(context.Background()))
	assert.Zero(t, fake2.callsFor(uri))

	matches := resolve(t, p2, "iterator.Iterator")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)
	assert.Zero(t, fake2.totalResolveCalls())
}

func TestSchemaVersionBumpRebuildsDerivedRecords(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	gf := goFile{
		pkg:     "mypkg",
		refs:    [][2]string{{"iterator", "Iterator"}},
		methods: [][2]string{{"Widget", "Do"}},
	}
	e.fake.setGoFile(uri, gf)
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))
	require.NoError(t, p.Close())

	// Rewrite the database as an older schema would have left it: a
	// version-less scan marker, version-less file records and no names
	// partition, while the records still carry current modification
	// times.
	ctx := context.Background()
	require.NoError(t, e.db.Set(ctx, metaScanID, metaDoc{Complete: true}))
	files, err := e.db.Partition(filesPartition)
	require.NoError(t, err)
	var fd fileDoc
	require.NoError(t, files.Get(ctx, uri.String(), &fd))
	fd.Version = 0
	require.NoError(t, files.Set(ctx, uri.String(), fd))
	names, err := e.db.Partition(namesPartition)
	require.NoError(t, err)
	require.NoError(t, names.(storageapi.DroppableService).Drop(ctx))

	fake2 := newFakeParser()
	fake2.setGoFile(uri, gf)
	e.fake = fake2
	p2 := e.start(t)
	require.NoError(t, p2.Wait(context.Background()))

	// The version bump must override the stat-only restart: the file is
	// re-extracted despite its unchanged mtime, repopulating the names
	// partition (including the method-only name), and the list is served
	// from the index again.
	assert.Positive(t, fake2.callsFor(uri))
	assert.Equal(t, []string{"iterator.Iterator", "mypkg.Widget.Do"},
		listReferenced(t, p2))
	assert.Zero(t, fake2.totalListCalls())
}

func TestInterruptedScanResumesIncrementally(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	gf := goFile{pkg: "mypkg", refs: [][2]string{{"iterator", "Iterator"}}}
	e.fake.setGoFile(uri, gf)
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))
	require.NoError(t, p.Close())

	// Interrupted run: per-file records survive, the marker never lands.
	require.NoError(t, e.db.Delete(context.Background(), metaScanID))

	fake2 := newFakeParser()
	fake2.setGoFile(uri, gf)
	e.fake = fake2
	p2 := e.start(t)
	require.NoError(t, p2.Wait(context.Background()))

	assert.Zero(t, fake2.callsFor(uri),
		"records written by this schema version must be trusted")
	assert.Equal(t, []string{"iterator.Iterator"}, listReferenced(t, p2))
	assert.Zero(t, fake2.totalListCalls())
}

// opCountingStorage counts write operations per partition path so
// tests can assert how many bolt transactions a scan would commit —
// on the production backend every Set and Delete is an fsync'd
// transaction costing milliseconds.
type opCountingStorage struct {
	storageapi.Service
	path string
	ops  *sync.Map // path+" "+op → *atomic.Int64
}

func (c *opCountingStorage) count(op string) {
	v, _ := c.ops.LoadOrStore(c.path+" "+op, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

func (c *opCountingStorage) opCount(path, op string) int64 {
	v, ok := c.ops.Load(path + " " + op)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (c *opCountingStorage) Partition(name string) (storageapi.Service, error) {
	child, err := c.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &opCountingStorage{
		Service: child, path: c.path + "/" + name, ops: c.ops,
	}, nil
}

// batchingStorage forwards batches to a batch-capable backend, counting
// the round trips and letting a test intercept them.
type batchingStorage struct {
	storageapi.Service
	path  string
	calls *sync.Map // path → *atomic.Int64
	sizes *sync.Map // path → *atomic.Int64
	// intercept answers a batch itself when it returns non-nil.
	intercept func(
		path string, call int64, ops []storageapi.BatchOp,
	) *batchOutcome
}

type batchOutcome struct {
	results []storageapi.BatchOpResult
	err     error
}

func newBatchingStorage(svc storageapi.Service) *batchingStorage {
	return &batchingStorage{
		Service: svc, calls: new(sync.Map), sizes: new(sync.Map),
	}
}

func (b *batchingStorage) Partition(name string) (storageapi.Service, error) {
	child, err := b.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &batchingStorage{
		Service: child, path: b.path + "/" + name,
		calls: b.calls, sizes: b.sizes, intercept: b.intercept,
	}, nil
}

func (b *batchingStorage) counter(m *sync.Map) *atomic.Int64 {
	v, _ := m.LoadOrStore(b.path, new(atomic.Int64))
	return v.(*atomic.Int64)
}

func (b *batchingStorage) callCount(path string) int64 {
	v, ok := b.calls.Load(path)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (b *batchingStorage) opCount(path string) int64 {
	v, ok := b.sizes.Load(path)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (b *batchingStorage) ApplyBatch(
	ctx context.Context, ops []storageapi.BatchOp,
) ([]storageapi.BatchOpResult, error) {
	call := b.counter(b.calls).Add(1)
	b.counter(b.sizes).Add(int64(len(ops)))
	if b.intercept != nil {
		if out := b.intercept(b.path, call, ops); out != nil {
			return out.results, out.err
		}
	}
	writer, ok := b.Service.(storageapi.BatchWriter)
	if !ok {
		return nil, errors.New("backend does not support batching")
	}
	return writer.ApplyBatch(ctx, ops)
}

func (c *opCountingStorage) Set(ctx context.Context, id string, doc any) error {
	c.count("set")
	return c.Service.Set(ctx, id, doc)
}

func (c *opCountingStorage) Create(ctx context.Context, id string, doc any) error {
	c.count("create")
	return c.Service.Create(ctx, id, doc)
}

func (c *opCountingStorage) Delete(ctx context.Context, id string) error {
	c.count("delete")
	return c.Service.Delete(ctx, id)
}

func TestMigrationRestampAvoidsRedundantWrites(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	gf := goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
		defs: []string{"Widget"},
	}
	e.fake.setGoFile(uri, gf)
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))
	require.NoError(t, p.Close())

	// Same content, older schema stamp: only the file record needs the
	// new version, everything else is already correct.
	ctx := context.Background()
	require.NoError(t, e.db.Set(ctx, metaScanID, metaDoc{Complete: true}))
	files, err := e.db.Partition(filesPartition)
	require.NoError(t, err)
	var fd fileDoc
	require.NoError(t, files.Get(ctx, uri.String(), &fd))
	fd.Version = 0
	require.NoError(t, files.Set(ctx, uri.String(), fd))

	counting := &opCountingStorage{Service: e.db, ops: new(sync.Map)}
	e.db = counting
	fake2 := newFakeParser()
	fake2.setGoFile(uri, gf)
	e.fake = fake2
	p2 := e.start(t)
	require.NoError(t, p2.Wait(context.Background()))

	assert.Positive(t, e.fake.callsFor(uri), "the stale record must be re-extracted")
	assert.Zero(t, counting.opCount("/symbols", "set"),
		"unchanged symbol docs must not be rewritten")
	assert.Zero(t, counting.opCount("/names", "set"),
		"existing name markers must not be rewritten")
	assert.Zero(t, counting.opCount("/symbols", "delete"))
	assert.Zero(t, counting.opCount("/names", "delete"))
	assert.Equal(t, int64(1), counting.opCount("/files", "set"),
		"the file record carries the new schema stamp")

	assert.Equal(t, []string{"iterator.Iterator", "mypkg.Widget"},
		listReferenced(t, p2))
}

func TestReindexSkipsCreateForKnownNames(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	counting := &opCountingStorage{Service: e.db, ops: new(sync.Map)}
	e.db = counting
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget"},
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))

	inserts := counting.opCount("/symbols", "create")
	require.Positive(t, inserts, "a cold scan inserts brand-new symbols")

	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget", "Gadget"},
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	e.touch(t, "a.go")
	p.Handle(ctx, textapi.Event{Type: textapi.EventTypeFlush, URI: uri})
	require.NoError(t, p.Wait(ctx))

	assert.Equal(t, inserts+1, counting.opCount("/symbols", "create"),
		"only the name the file had never contributed is inserted")
}

func TestBatchWritesSymbolsOfFileInOneRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	batching := newBatchingStorage(e.db)
	e.db = batching
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget", "Gadget", "Sprocket"},
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))

	assert.Equal(t, int64(1), batching.callCount("/symbols"),
		"one file's symbols must cost a single batch")
	assert.Greater(t, batching.opCount("/symbols"), int64(1),
		"that batch must carry every symbol the file defines")
	assert.Equal(t,
		[]string{"iterator.Iterator", "mypkg.Gadget",
			"mypkg.Sprocket", "mypkg.Widget"},
		listReferenced(t, p))
}

func TestBatchRetriesOnlyStaleOperations(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	batching := newBatchingStorage(e.db)
	var retried []string
	batching.intercept = func(
		path string, call int64, ops []storageapi.BatchOp,
	) *batchOutcome {
		if path != "/symbols" {
			return nil
		}
		if call == 1 {
			results := make([]storageapi.BatchOpResult, len(ops))
			results[0].Err = storageapi.ErrPreconditionFailed
			return &batchOutcome{results: results}
		}
		for _, op := range ops {
			retried = append(retried, op.ID)
		}
		return nil
	}
	e.db = batching
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget", "Gadget"},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))

	assert.Equal(t, int64(2), batching.callCount("/symbols"),
		"the rejected operation must be retried in a second batch")
	assert.Len(t, retried, 1,
		"only the rejected operation is retried: %v", retried)
	assert.Equal(t,
		[]string{"mypkg.Gadget", "mypkg.Widget"}, listReferenced(t, p))
}

func TestBatchFallsBackWhenUnimplemented(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	batching := newBatchingStorage(e.db)
	batching.intercept = func(
		path string, _ int64, _ []storageapi.BatchOp,
	) *batchOutcome {
		if path != "/symbols" {
			return nil
		}
		return &batchOutcome{err: status.Error(
			codes.Unimplemented, "method Batch not implemented")}
	}
	e.db = batching
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget", "Gadget"},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))

	assert.Equal(t, int64(1), batching.callCount("/symbols"),
		"a store without batching must not be asked twice")
	assert.Equal(t,
		[]string{"mypkg.Gadget", "mypkg.Widget"}, listReferenced(t, p))
}

func TestUpsertMergesIntoVersionlessDoc(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	files, err := e.db.Partition(filesPartition)
	require.NoError(t, err)
	symbols, err := e.db.Partition(symbolsPartition)
	require.NoError(t, err)
	names, err := e.db.Partition(namesPartition)
	require.NoError(t, err)

	legacy := struct {
		Name string
		Locs []symbolLoc
	}{
		Name: "iterator.Iterator",
		Locs: []symbolLoc{{URI: "file:///ws/a.go", Y: 1, Kind: kindRef}},
	}
	require.NoError(t, symbols.Set(ctx, legacy.Name, legacy))

	p := &Parser{files: files, symbols: symbols, names: names,
		meta: e.db, ctx: ctx}
	p.upsertSymbol(ctx, legacy.Name, "file:///ws/b.go",
		[]symbolLoc{{URI: "file:///ws/b.go", Y: 2, Kind: kindRef}}, false, false)

	var doc symbolDoc
	require.NoError(t, symbols.Get(ctx, legacy.Name, &doc))
	assert.Equal(t, int64(1), doc.Version, "the merge must stamp the counter")
	got := make([]string, 0, len(doc.Locs))
	for _, l := range doc.Locs {
		got = append(got, l.URI)
	}
	assert.ElementsMatch(t, []string{"file:///ws/a.go", "file:///ws/b.go"}, got)
}

// durabilityOp is one write observed by durabilityRecordingStorage.
type durabilityOp struct {
	path, id string
	noSync   bool
}

// durabilityRecordingStorage records whether each write carried the
// bolt relaxed-durability request, in operation order.
type durabilityRecordingStorage struct {
	storageapi.Service
	path string
	mu   *sync.Mutex
	ops  *[]durabilityOp
}

func newDurabilityRecordingStorage(s storageapi.Service) *durabilityRecordingStorage {
	return &durabilityRecordingStorage{
		Service: s, mu: new(sync.Mutex), ops: new([]durabilityOp),
	}
}

func (d *durabilityRecordingStorage) record(ctx context.Context, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*d.ops = append(*d.ops, durabilityOp{
		path: d.path, id: id, noSync: bluebolt.NoSyncRequested(ctx),
	})
}

func (d *durabilityRecordingStorage) snapshot() []durabilityOp {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]durabilityOp(nil), *d.ops...)
}

func (d *durabilityRecordingStorage) Partition(name string) (storageapi.Service, error) {
	child, err := d.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &durabilityRecordingStorage{
		Service: child, path: d.path + "/" + name, mu: d.mu, ops: d.ops,
	}, nil
}

func (d *durabilityRecordingStorage) Set(ctx context.Context, id string, doc any) error {
	d.record(ctx, id)
	return d.Service.Set(ctx, id, doc)
}

func (d *durabilityRecordingStorage) Delete(ctx context.Context, id string) error {
	d.record(ctx, id)
	return d.Service.Delete(ctx, id)
}

func TestScanRelaxesDurabilityUntilMarker(t *testing.T) {
	e := newEnv(t)
	rec := newDurabilityRecordingStorage(e.db)
	e.db = rec
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		refs: [][2]string{{"iterator", "Iterator"}},
		defs: []string{"Widget"},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	ops := rec.snapshot()
	require.NotEmpty(t, ops)
	last := ops[len(ops)-1]
	assert.Equal(t, metaScanID, last.id, "the marker must be the last scan write")
	assert.Equal(t, "", last.path)
	assert.False(t, last.noSync, "the marker is the durability barrier")
	for _, op := range ops[:len(ops)-1] {
		assert.Truef(t, op.noSync,
			"scan write %s %q must request relaxed durability", op.path, op.id)
	}

	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Gadget"}})
	e.touch(t, "a.go")
	before := len(ops)
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeFlush, URI: uri,
	})
	require.NoError(t, p.Wait(context.Background()))
	ops = rec.snapshot()
	require.Greater(t, len(ops), before, "the flush must write")
	reindex := ops[before:]
	fileRecord := reindex[len(reindex)-1]
	assert.Equal(t, uri.String(), fileRecord.id,
		"the file record must be the last write of a re-index")
	assert.False(t, fileRecord.noSync,
		"the file record is the durability barrier of a re-index")
	for _, op := range reindex[:len(reindex)-1] {
		assert.Truef(t, op.noSync,
			"derived write %s %q must request relaxed durability",
			op.path, op.id)
	}
}

func TestTouchedFileReindexedAlone(t *testing.T) {
	e := newEnv(t)
	uriA := e.writeFile(t, "a.go")
	uriB := e.writeFile(t, "b.go")
	e.fake.setGoFile(uriA, goFile{pkg: "mypkg", defs: []string{"A"}})
	e.fake.setGoFile(uriB, goFile{pkg: "mypkg", defs: []string{"B"}})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	callsA, callsB := e.fake.callsFor(uriA), e.fake.callsFor(uriB)
	e.touch(t, "a.go")
	p.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeChange, URI: uriA,
	})
	require.NoError(t, p.Wait(context.Background()))
	assert.Greater(t, e.fake.callsFor(uriA), callsA)
	assert.Equal(t, callsB, e.fake.callsFor(uriB))
}

func TestImportDedupCollapsesMatches(t *testing.T) {
	e := newEnv(t)
	uriA := e.writeFile(t, "a.go")
	uriB := e.writeFile(t, "b.go")
	e.fake.setGoFile(uriA, goFile{
		pkg:     "mypkg",
		imports: []string{"github.com/x/iterator"},
		refs:    [][2]string{{"iterator", "Iterator"}},
	})
	e.fake.setGoFile(uriB, goFile{
		pkg:     "otherpkg",
		imports: []string{"github.com/x/iterator"},
		refs:    [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "iterator.Iterator")
	require.Len(t, matches, 1)
	assert.Equal(t, "github.com/x/iterator", matches[0].ImportPath)
}

func TestDistinctImportsDisambiguateDisplay(t *testing.T) {
	e := newEnv(t)
	uriA := e.writeFile(t, "a.go")
	uriB := e.writeFile(t, "b.go")
	e.fake.setGoFile(uriA, goFile{
		pkg:     "mypkg",
		imports: []string{"github.com/x/iterator"},
		refs:    [][2]string{{"iterator", "Iterator"}},
	})
	e.fake.setGoFile(uriB, goFile{
		pkg:     "otherpkg",
		imports: []string{"github.com/y/iterator"},
		refs:    [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "iterator.Iterator")
	require.Len(t, matches, 2)
	displays := []string{matches[0].Display, matches[1].Display}
	sort.Strings(displays)
	assert.Equal(t, []string{
		"github.com/x/iterator: iterator.Iterator",
		"github.com/y/iterator: iterator.Iterator",
	}, displays)
}

func TestMethodResolutionAndListing(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:     "httpx",
		defs:    []string{"Client"},
		methods: [][2]string{{"Client", "Do"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	matches := resolve(t, p, "httpx.Client.Do")
	require.Len(t, matches, 1)
	assert.Equal(t, uri.String(), matches[0].URI)
	assert.Equal(t, term.Coordinates{Y: 200}, matches[0].Pos)
	assert.Equal(t, "httpx.Client.Do", matches[0].Display)

	assert.Equal(t, []string{"httpx.Client", "httpx.Client.Do"},
		listReferenced(t, p))
}

func TestListReferencedPassthroughBeforeFirstScan(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	e.fake.listNames = []string{"backing.Name"}
	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	p := e.start(t)

	<-gate.reached
	assert.Equal(t, []string{"backing.Name"}, listReferenced(t, p))
	assert.Equal(t, 1, e.fake.totalListCalls())

	close(gate.release)
	require.NoError(t, p.Wait(context.Background()))
	assert.Equal(t, []string{"mypkg.Widget"}, listReferenced(t, p))
	assert.Equal(t, 1, e.fake.totalListCalls())
}

// countingListStorage counts List calls on a service and every
// partition derived from it.
type countingListStorage struct {
	storageapi.Service
	lists *atomic.Int64
}

func (c *countingListStorage) Partition(name string) (storageapi.Service, error) {
	child, err := c.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &countingListStorage{Service: child, lists: c.lists}, nil
}

func (c *countingListStorage) List(
	ctx context.Context, filters []storageapi.Filter,
) (storageapi.Iterator, error) {
	c.lists.Add(1)
	return c.Service.List(ctx, filters)
}

func TestListReferencedSymbolsConstructionIsLazy(t *testing.T) {
	e := newEnv(t)
	lists := new(atomic.Int64)
	e.db = &countingListStorage{Service: e.db, lists: lists}
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	before := lists.Load()
	it, err := p.ListReferencedSymbols(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, lists.Load(),
		"construction must not scan the names partition")

	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"mypkg.Widget"}, names)
	assert.Equal(t, before+1, lists.Load())
}

func TestListReferencedPassthroughConstructionIsLazy(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	e.fake.listNames = []string{"backing.Name"}
	gate := newQueryGate()
	e.fake.setGate(uri, gate)
	p := e.start(t)
	<-gate.reached
	defer close(gate.release)

	it, err := p.ListReferencedSymbols(context.Background())
	require.NoError(t, err)
	assert.Zero(t, e.fake.totalListCalls(),
		"construction must not start the backing walk")

	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"backing.Name"}, names)
	assert.Equal(t, 1, e.fake.totalListCalls())
}

func TestScanReportsProgressNotifications(t *testing.T) {
	e := newEnv(t)
	rootFile := e.writeFile(t, "a.go")
	subFile := e.writeFile(t, filepath.Join("pkg", "b.go"))
	e.fake.setGoFile(rootFile, goFile{pkg: "mypkg", defs: []string{"Widget"}})
	e.fake.setGoFile(subFile, goFile{pkg: "pkg", defs: []string{"Gadget"}})
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	notified, updates := e.notify.snapshot()
	require.Equal(t, []string{"Indexing workspace symbols"}, notified)
	require.NotEmpty(t, updates)

	var dirs []string
	for _, u := range updates {
		assert.Equal(t, "n1", u.id)
		if dir, ok := strings.CutPrefix(u.message, "Indexing symbols: "); ok {
			dirs = append(dirs, dir)
			assert.Less(t, u.progress, u.total)
		}
	}
	assert.ElementsMatch(t, []string{".", "pkg"}, dirs)

	final := updates[len(updates)-1]
	assert.Equal(t, "Indexed 2 files", final.message)
	assert.Equal(t, final.total, final.progress)
	assert.Equal(t, int64(2), final.total)
}

func TestCleanupWorkspaceHook(t *testing.T) {
	ctx := context.Background()
	ideStorage := storagestub.NewInMemoryService()
	t.Cleanup(func() { _ = ideStorage.Close() })

	e := newEnv(t)
	e.db = workspaceStorage(t, ideStorage, e.root)
	uri := e.writeFile(t, "a.go")
	e.fake.setGoFile(uri, goFile{
		pkg:  "mypkg",
		defs: []string{"Widget"},
		refs: [][2]string{{"iterator", "Iterator"}},
	})
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))
	require.NoError(t, p.Close())

	other, err := workspaceapi.ParseURI(e.root.String() + "-other")
	require.NoError(t, err)
	otherDB := workspaceStorage(t, ideStorage, other)
	require.NoError(t, otherDB.Set(ctx, metaScanID, metaDoc{Complete: true}))

	for _, name := range []string{filesPartition, symbolsPartition, namesPartition} {
		require.NotZero(t, countDocs(t, e.db, name),
			"%s partition must be populated before the drop", name)
	}

	require.NoError(t, CleanupWorkspaceHook(ideStorage)(ctx, e.root))

	for _, name := range []string{filesPartition, symbolsPartition, namesPartition} {
		assert.Zero(t, countDocs(t, e.db, name),
			"%s partition must be empty after the drop", name)
	}
	var m metaDoc
	assert.ErrorIs(t, e.db.Get(ctx, metaScanID, &m), storageapi.ErrNotFound)
	assert.NoError(t, otherDB.Get(ctx, metaScanID, &m),
		"dropping one workspace must not touch another")
}

func workspaceStorage(
	t *testing.T, ideStorage storageapi.Service, root workspaceapi.URI,
) storageapi.Service {
	t.Helper()
	dbs, err := ideStorage.Partition(PartitionName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbs.Close() })
	db, err := dbs.Partition(root.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countDocs(t *testing.T, db storageapi.Service, partition string) int {
	t.Helper()
	part, err := db.Partition(partition)
	require.NoError(t, err)
	defer func() { _ = part.Close() }()

	it, err := part.List(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	var count int
	for it.HasNext() {
		var doc map[string]any
		require.NoError(t, it.NextTo(&doc))
		count++
	}
	return count
}
