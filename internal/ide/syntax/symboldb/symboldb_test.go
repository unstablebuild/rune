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
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
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
	// grammars maps language IDs to their grammar fingerprint; a
	// language without one is not installed. Go is installed unless a
	// test removes it.
	grammars     map[string]string
	grammarCalls map[string]int
}

var _ syntaxapi.Parser = (*fakeParser)(nil)

func newFakeParser() *fakeParser {
	return &fakeParser{
		queryResults: make(map[string]map[string][]syntaxapi.Result),
		nodeResults:  make(map[string][]syntaxapi.Result),
		fileCalls:    make(map[string]int),
		gates:        make(map[string]*queryGate),
		grammars:     map[string]string{"go": "v0"},
		grammarCalls: make(map[string]int),
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

func (f *fakeParser) GrammarFingerprint(_ context.Context, langID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grammarCalls[langID]++
	sum, ok := f.grammars[langID]
	if !ok {
		return "", fmt.Errorf("language %q not installed", langID)
	}
	return sum, nil
}

func (f *fakeParser) grammarCallsFor(langID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grammarCalls[langID]
}

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
			File: uri, Text: d, CaptureName: "local.definition.function",
			From: term.Coordinates{Y: 300 + i},
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryResults[uri.String()] = m
	f.nodeResults[uri.String()] = defs
}

// setNodeDefs cans only definition captures for a file, for specs whose
// definitions come from the node query alone.
func (f *fakeParser) setNodeDefs(uri workspaceapi.URI, names ...string) {
	defs := make([]syntaxapi.Result, len(names))
	for i, d := range names {
		defs[i] = syntaxapi.Result{
			File: uri, Text: d, CaptureName: "local.definition.function", From: term.Coordinates{Y: 300 + i},
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
	p, err := New(Config{
		Root: e.root, FS: e.fs, Backing: e.fake, Indexer: e.fake, DB: e.db,
		Progress: func() Progress { return NotifyProgress(notify, e.schedule) },
	})
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
	remaining := len(p.pending)
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

func TestGrammarFingerprintGatesReextraction(t *testing.T) {
	tests := []struct {
		name string
		// first and second are the grammar fingerprints installed for
		// each run; empty means the grammar is not installed.
		first, second string
		wantReextract bool
		wantListed    []string
	}{
		{
			name:  "unchanged grammar skips",
			first: "v1", second: "v1",
			wantListed: []string{"mypkg.Widget"},
		},
		{
			name:  "upgraded grammar re-extracts",
			first: "v1", second: "v2",
			wantReextract: true,
			wantListed:    []string{"mypkg.New", "mypkg.Widget"},
		},
		{
			name:  "grammar installed after the first scan re-extracts",
			first: "", second: "v1",
			wantReextract: true,
			wantListed:    []string{"mypkg.New", "mypkg.Widget"},
		},
		{
			name:  "grammar still missing reads nothing",
			first: "", second: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			uri := e.writeFile(t, "a.go")
			e.fake.grammars["go"] = tt.first
			if tt.first == "" {
				delete(e.fake.grammars, "go")
			}
			e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
			p := e.start(t)
			require.NoError(t, p.Wait(ctx))
			require.NoError(t, p.Close())

			// The second run's extraction would add a definition, so the
			// listed names reveal whether the file was re-extracted.
			fake2 := newFakeParser()
			fake2.grammars["go"] = tt.second
			if tt.second == "" {
				delete(fake2.grammars, "go")
			}
			fake2.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget", "New"}})
			e.fake = fake2
			p2 := e.start(t)
			require.NoError(t, p2.Wait(ctx))

			assert.Equal(t, tt.wantReextract, fake2.callsFor(uri) > 0)
			if tt.wantListed == nil {
				assert.Empty(t, listReferenced(t, p2))
			} else {
				assert.Equal(t, tt.wantListed, listReferenced(t, p2))
			}
		})
	}
}

func TestGrammarFingerprintComputedOncePerLanguage(t *testing.T) {
	e := newEnv(t)
	e.fake.grammars["go"] = "v1"
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		e.fake.setGoFile(e.writeFile(t, name), goFile{pkg: "mypkg"})
	}
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	assert.Equal(t, 1, e.fake.grammarCallsFor("go"))
}

func TestHostSpecGrammarCoversInjectedLanguages(t *testing.T) {
	all := map[string]string{"svelte": "v1", "typescript": "v1", "javascript": "v1"}
	with := func(overrides map[string]string) map[string]string {
		m := maps.Clone(all)
		for k, v := range overrides {
			if v == "" {
				delete(m, k)
				continue
			}
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name string
		// first and second are the fingerprints installed for each run,
		// by language; a missing language is not installed.
		first, second map[string]string
		wantReextract bool
	}{
		{
			name:  "unchanged grammars skip",
			first: all, second: all,
		},
		{
			name:          "host grammar upgraded re-extracts",
			first:         all,
			second:        with(map[string]string{"svelte": "v2"}),
			wantReextract: true,
		},
		{
			name:          "injected grammar upgraded re-extracts",
			first:         all,
			second:        with(map[string]string{"typescript": "v2"}),
			wantReextract: true,
		},
		{
			name:          "injected grammar installed after the first scan re-extracts",
			first:         with(map[string]string{"javascript": ""}),
			second:        all,
			wantReextract: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			uri := e.writeFile(t, "Button.svelte")
			e.fake.grammars = maps.Clone(tt.first)
			e.fake.setNodeDefs(uri, "click")
			p := e.start(t)
			require.NoError(t, p.Wait(ctx))
			require.NoError(t, p.Close())

			fake2 := newFakeParser()
			fake2.grammars = maps.Clone(tt.second)
			fake2.setNodeDefs(uri, "click", "focus")
			e.fake = fake2
			p2 := e.start(t)
			require.NoError(t, p2.Wait(ctx))

			assert.Equal(t, tt.wantReextract, fake2.callsFor(uri) > 0)
			want := []string{"Button.click"}
			if tt.wantReextract {
				want = []string{"Button.click", "Button.focus"}
			}
			assert.Equal(t, want, listReferenced(t, p2))
		})
	}
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
	// Same content, older stamp: only the file record needs the new
	// stamp, everything else is already correct.
	tests := []struct {
		name   string
		marker metaDoc
		stale  func(fd *fileDoc)
	}{
		{
			name:   "older schema version",
			marker: metaDoc{Complete: true},
			stale:  func(fd *fileDoc) { fd.Version = 0 },
		},
		{
			name:   "older grammar fingerprint",
			marker: metaDoc{Complete: true, Version: schemaVersion},
			stale:  func(fd *fileDoc) { fd.Grammar = "v0" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.fake.grammars["go"] = "v1"
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

			ctx := context.Background()
			require.NoError(t, e.db.Set(ctx, metaScanID, tt.marker))
			files, err := e.db.Partition(filesPartition)
			require.NoError(t, err)
			var fd fileDoc
			require.NoError(t, files.Get(ctx, uri.String(), &fd))
			tt.stale(&fd)
			require.NoError(t, files.Set(ctx, uri.String(), fd))

			counting := &opCountingStorage{Service: e.db, ops: new(sync.Map)}
			e.db = counting
			fake2 := newFakeParser()
			fake2.grammars["go"] = "v1"
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
				"the file record carries the new stamp")

			require.NoError(t, files.Get(ctx, uri.String(), &fd))
			assert.Equal(t, schemaVersion, fd.Version)
			assert.Equal(t, "v1", fd.Grammar)
			assert.Equal(t, []string{"iterator.Iterator", "mypkg.Widget"},
				listReferenced(t, p2))
		})
	}
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

func TestScanWritesSharedSymbolsOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	batching := newBatchingStorage(e.db)
	e.db = batching
	const files = 20
	uris := make([]string, files)
	for i := range files {
		uri := e.writeFile(t, fmt.Sprintf("f%d.go", i))
		e.fake.setGoFile(uri, goFile{
			pkg:  "mypkg",
			refs: [][2]string{{"iterator", "Iterator"}},
			defs: []string{fmt.Sprintf("Widget%d", i)},
		})
		uris[i] = uri.String()
	}
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))

	assert.Equal(t, int64(1), batching.callCount("/symbols"),
		"the symbols of every file must cost a single batch")
	assert.Equal(t, int64(files+1), batching.opCount("/symbols"),
		"a name shared by every file is written once")
	assert.Equal(t, int64(1), batching.callCount("/names"))
	assert.Equal(t, int64(1), batching.callCount("/files"))
	assert.Equal(t, int64(files), batching.opCount("/files"))

	symbols, err := e.db.Partition(symbolsPartition)
	require.NoError(t, err)
	var doc symbolDoc
	require.NoError(t, symbols.Get(ctx, "iterator.Iterator", &doc))
	got := make([]string, 0, len(doc.Locs))
	for _, l := range doc.Locs {
		got = append(got, l.URI)
	}
	assert.ElementsMatch(t, uris, got)
	assert.Len(t, listReferenced(t, p), files+1)
}

func TestBurstOfFlushesDrainsInOneWindow(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	batching := newBatchingStorage(e.db)
	e.db = batching
	const files = 10
	uris := make([]workspaceapi.URI, files)
	for i := range files {
		uris[i] = e.writeFile(t, fmt.Sprintf("f%d.go", i))
		e.fake.setGoFile(uris[i], goFile{pkg: "mypkg", defs: []string{"Widget"}})
	}
	p := e.start(t)
	require.NoError(t, p.Wait(ctx))
	paths := []string{"/symbols", "/names", "/files"}
	before := map[string]int64{}
	for _, path := range paths {
		before[path] = batching.callCount(path)
	}

	want := []string{"mypkg.Widget"}
	for i, uri := range uris {
		gadget := fmt.Sprintf("Gadget%d", i)
		want = append(want, "mypkg."+gadget)
		e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget", gadget}})
		e.touch(t, fmt.Sprintf("f%d.go", i))
	}
	// Back to back, so that they land within the settle delay of the
	// first, as the saves of a checkout do.
	for _, uri := range uris {
		p.Handle(ctx, textapi.Event{Type: textapi.EventTypeFlush, URI: uri})
	}
	require.NoError(t, p.Wait(ctx))

	for _, path := range paths {
		assert.Equalf(t, before[path]+1, batching.callCount(path),
			"%s: a burst of saves must cost one batch", path)
	}
	sort.Strings(want)
	assert.Equal(t, want, listReferenced(t, p))
	symbols, err := e.db.Partition(symbolsPartition)
	require.NoError(t, err)
	var doc symbolDoc
	require.NoError(t, symbols.Get(ctx, "mypkg.Widget", &doc))
	assert.Len(t, doc.Locs, files, "the shared name keeps every file's location")
}

func TestMergeLocsReplacesSeveralFiles(t *testing.T) {
	a1 := symbolLoc{URI: "a", Y: 1, Kind: kindRef}
	a2 := symbolLoc{URI: "a", Y: 2, Kind: kindDef}
	b1 := symbolLoc{URI: "b", Y: 1, Kind: kindRef}
	c1 := symbolLoc{URI: "c", Y: 1, Kind: kindRef}
	d1 := symbolLoc{URI: "d", Y: 1, Kind: kindRef}
	tests := []struct {
		name    string
		stored  []symbolLoc
		replace map[string][]symbolLoc
		want    []symbolLoc
	}{
		{
			name:    "nothing stored",
			replace: map[string][]symbolLoc{"a": {a1}},
			want:    []symbolLoc{a1},
		},
		{
			name:    "one file replaced",
			stored:  []symbolLoc{a1, b1},
			replace: map[string][]symbolLoc{"a": {a2}},
			want:    []symbolLoc{b1, a2},
		},
		{
			name:    "one file dropped",
			stored:  []symbolLoc{a1, b1},
			replace: map[string][]symbolLoc{"a": nil},
			want:    []symbolLoc{b1},
		},
		{
			name:    "several files, in file order",
			stored:  []symbolLoc{c1, a1, b1},
			replace: map[string][]symbolLoc{"d": {d1}, "c": nil, "a": {a2}},
			want:    []symbolLoc{b1, a2, d1},
		},
		{
			name:    "every file dropped",
			stored:  []symbolLoc{a1, b1},
			replace: map[string][]symbolLoc{"a": nil, "b": nil},
			want:    []symbolLoc{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, mergeLocs(tt.stored, tt.replace))
		})
	}
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

// batchDurabilityRecordingStorage is a durabilityRecordingStorage whose
// partitions take batches, recording every operation of one.
type batchDurabilityRecordingStorage struct{ *durabilityRecordingStorage }

func (d batchDurabilityRecordingStorage) Partition(name string) (storageapi.Service, error) {
	child, err := d.durabilityRecordingStorage.Partition(name)
	if err != nil {
		return nil, err
	}
	return batchDurabilityRecordingStorage{child.(*durabilityRecordingStorage)}, nil
}

func (d batchDurabilityRecordingStorage) ApplyBatch(
	ctx context.Context, ops []storageapi.BatchOp,
) ([]storageapi.BatchOpResult, error) {
	for _, op := range ops {
		d.record(ctx, op.ID)
	}
	return d.Service.(storageapi.BatchWriter).ApplyBatch(ctx, ops)
}

func TestScanRelaxesDurabilityUntilMarker(t *testing.T) {
	tests := []struct {
		name string
		wrap func(*durabilityRecordingStorage) storageapi.Service
	}{
		{"one operation at a time", func(d *durabilityRecordingStorage) storageapi.Service { return d }},
		{"in batches", func(d *durabilityRecordingStorage) storageapi.Service {
			return batchDurabilityRecordingStorage{d}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := newDurabilityRecordingStorage(e.db)
			e.db = tt.wrap(rec)
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

			// A burst of saves is re-indexed together: the file records
			// are the only synced writes, and come after the rest.
			others := []workspaceapi.URI{e.writeFile(t, "b.go"), e.writeFile(t, "c.go")}
			for i, other := range others {
				e.fake.setGoFile(other, goFile{pkg: "mypkg", defs: []string{fmt.Sprintf("Other%d", i)}})
			}
			e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Gadget"}})
			e.touch(t, "a.go")
			before := len(ops)
			p.Handle(context.Background(), textapi.Event{
				Type: textapi.EventTypeFlush, URI: uri,
			})
			for _, other := range others {
				p.Handle(context.Background(), textapi.Event{
					Type: textapi.EventTypeCreate, URI: other,
				})
			}
			require.NoError(t, p.Wait(context.Background()))
			ops = rec.snapshot()
			require.Greater(t, len(ops), before, "the flush must write")
			reindex := ops[before:]
			var fileRecords []string
			for _, op := range reindex {
				if op.path == "/"+filesPartition {
					fileRecords = append(fileRecords, op.id)
					assert.Falsef(t, op.noSync,
						"the file record %q is a durability barrier of a re-index", op.id)
					continue
				}
				assert.Truef(t, op.noSync,
					"derived write %s %q must request relaxed durability", op.path, op.id)
			}
			assert.ElementsMatch(t, []string{uri.String(), others[0].String(), others[1].String()}, fileRecords)
			assert.Equal(t, "/"+filesPartition, reindex[len(reindex)-1].path,
				"a file record must be the last write of a re-index")
			assert.Equal(t, []string{"mypkg.Gadget", "mypkg.Other0", "mypkg.Other1"}, listReferenced(t, p))
		})
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
	tests := []struct {
		name      string
		files     []string
		wantDirs  []string
		wantFinal string
	}{
		{
			name:      "one file",
			files:     []string{"a.go"},
			wantDirs:  []string{"."},
			wantFinal: "Indexed the symbols of 1 file",
		},
		{
			name:      "two files",
			files:     []string{"a.go", filepath.Join("pkg", "b.go")},
			wantDirs:  []string{".", "pkg"},
			wantFinal: "Indexed the symbols of 2 files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			for _, name := range tt.files {
				uri := e.writeFile(t, name)
				e.fake.setGoFile(uri, goFile{pkg: "mypkg", defs: []string{"Widget"}})
			}
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
			assert.ElementsMatch(t, tt.wantDirs, dirs)

			final := updates[len(updates)-1]
			assert.Equal(t, tt.wantFinal, final.message)
			assert.Equal(t, final.total, final.progress)
			assert.Equal(t, int64(len(tt.files)), final.total)
		})
	}
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

// fixturePkgs serves the checked-in grammars of the installed languages.
type fixturePkgs struct {
	mu    sync.Mutex
	files map[string][]string
}

func (p *fixturePkgs) install(t *testing.T, langs ...string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lang := range langs {
		dir, err := filepath.Abs(filepath.Join("..", "syntaxtest", lang))
		require.NoError(t, err)
		p.files[lang] = grammarfixture.LibDir(t, dir)
	}
}

func (p *fixturePkgs) LibDir(_ context.Context, lang string) (iterator.Iterator[string], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	files, ok := p.files[lang]
	if !ok {
		return nil, errors.New("not installed")
	}
	return iterator.FromSlice(files), nil
}

// countingParser records the files it parses, and salts the grammar
// fingerprints of languages as if their grammar changed. As the backing
// parser, it counts the lookups it serves.
type countingParser struct {
	treesitter.Parser
	mu     sync.Mutex
	parsed []string
	salt   map[string]string
	// missing are languages reported as not installed.
	missing map[string]bool
	// failing are the base names of files that fail to parse.
	failing map[string]bool
	gate    *parseGate
	// resolves and lists count the lookups served as the backing parser.
	resolves, lists int
}

func (c *countingParser) NewQuerySession() symbolresolve.QuerySession {
	return &countingSession{QuerySession: c.Parser.NewQuerySession(), c: c}
}

func (c *countingParser) GrammarFingerprint(ctx context.Context, lang string) (string, error) {
	sum, err := c.Parser.GrammarFingerprint(ctx, lang)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.missing[lang] {
		return "", fmt.Errorf("%s is not installed", lang)
	}
	return sum + c.salt[lang], err
}

func (c *countingParser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	c.mu.Lock()
	c.resolves++
	c.mu.Unlock()
	return c.Parser.ResolveSymbol(ctx, name, progress)
}

func (c *countingParser) ListReferencedSymbols(ctx context.Context) (iterator.Iterator[string], error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Parser.ListReferencedSymbols(ctx)
}

// backingCalls returns how many lookups went to the backing parser.
func (c *countingParser) backingCalls() (resolves, lists int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolves, c.lists
}

func (c *countingParser) setSalt(lang, salt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.salt[lang] = salt
}

func (c *countingParser) setMissing(lang string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.missing[lang] = true
}

func (c *countingParser) setFailing(file string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failing[file] = true
}

// hold has the next parse of the file named file wait for the gate it
// returns to open, or for the parse's context to end.
func (c *countingParser) hold(t *testing.T, file string) *parseGate {
	t.Helper()
	g := &parseGate{file: file, entered: make(chan struct{}), release: make(chan struct{})}
	c.mu.Lock()
	c.gate = g
	c.mu.Unlock()
	t.Cleanup(g.open)
	return g
}

type parseGate struct {
	file    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *parseGate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s was never parsed", g.file)
	}
}

func (g *parseGate) open() { g.once.Do(func() { close(g.release) }) }

// takeParsed returns the base names of the files parsed since it was
// last called, sorted.
func (c *countingParser) takeParsed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := slices.Sorted(slices.Values(c.parsed))
	c.parsed = nil
	return out
}

type countingSession struct {
	symbolresolve.QuerySession
	c *countingParser
}

func (s *countingSession) QueryMulti(
	ctx context.Context, file workspaceapi.URI, queries []symbolresolve.MultiQuery,
) ([]symbolresolve.MultiResult, error) {
	base := filepath.Base(file.Path())
	s.c.mu.Lock()
	s.c.parsed = append(s.c.parsed, base)
	failing := s.c.failing[base]
	g := s.c.gate
	if g != nil && g.file == base {
		s.c.gate = nil
	} else {
		g = nil
	}
	s.c.mu.Unlock()
	if g != nil {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failing {
		return nil, errors.New("the parser crashed")
	}
	return s.QuerySession.QueryMulti(ctx, file, queries)
}

// defsEnv is a workspace parsed with the checked-in grammars of the
// languages it installs.
type defsEnv struct {
	root    string
	rootURI workspaceapi.URI
	fs      workspaceapi.FileSystem
	pkgs    *fixturePkgs
	parser  *countingParser
	db      storageapi.Service
}

func newDefsEnv(t *testing.T, files map[string]string, langs ...string) *defsEnv {
	t.Helper()
	root := t.TempDir()
	rootURI, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), rootURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	pkgs := &fixturePkgs{files: map[string][]string{}}
	pkgs.install(t, langs...)
	e := &defsEnv{
		root: root, rootURI: rootURI, fs: scheme, pkgs: pkgs,
		parser: &countingParser{
			Parser: treesitter.NewParser(scheme, pkgs, rootURI),
			salt:   map[string]string{}, missing: map[string]bool{}, failing: map[string]bool{},
		},
		db: storagestub.NewInMemoryService(),
	}
	for rel, content := range files {
		e.write(t, rel, content)
	}
	return e
}

// write writes rel with a modification time later than any before it.
func (e *defsEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(e.root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	var prev time.Time
	if info, err := os.Stat(p); err == nil {
		prev = info.ModTime()
	}
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	mtime := time.Now().Add(time.Second)
	if !mtime.After(prev) {
		mtime = prev.Add(time.Second)
	}
	require.NoError(t, os.Chtimes(p, mtime, mtime))
}

func (e *defsEnv) uri(t *testing.T, rel string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI("file://" + filepath.Join(e.root, rel))
	require.NoError(t, err)
	return u
}

func (e *defsEnv) config(withDB bool) Config {
	cfg := Config{Root: e.rootURI, FS: e.fs, Backing: e.parser, Indexer: e.parser}
	if withDB {
		cfg.DB = e.db
	}
	return cfg
}

func (e *defsEnv) open(t *testing.T, cfg Config) *Parser {
	t.Helper()
	p := e.start(t, cfg)
	require.NoError(t, p.Wait(context.Background()))
	return p
}

// start opens the index without waiting for its scan.
func (e *defsEnv) start(t *testing.T, cfg Config) *Parser {
	t.Helper()
	p, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func (e *defsEnv) handle(t *testing.T, p *Parser, tpe textapi.EventType, rel string) bool {
	t.Helper()
	return p.Handle(context.Background(), textapi.Event{Type: tpe, URI: e.uri(t, rel)})
}

func (e *defsEnv) fingerprint(t *testing.T, lang string) string {
	t.Helper()
	sum, err := e.parser.GrammarFingerprint(context.Background(), lang)
	require.NoError(t, err)
	return sum
}

func wait(t *testing.T, p *Parser) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, p.Wait(ctx))
}

func definitions(t *testing.T, p *Parser, q DefinitionQuery) []Definition {
	t.Helper()
	it, err := p.Definitions(context.Background(), q)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return out
}

func defNames(defs []Definition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

const goSource = `package pkg

type Invoice struct{}

func NewInvoice() *Invoice { return nil }

func (i *Invoice) Total() int { return 0 }

var rate = 1
`

func TestDefinitionsOfEachLanguage(t *testing.T) {
	tests := []struct {
		lang, file, source string
		// injected are the languages installed for what lang injects.
		injected []string
		want     []Definition
	}{
		{
			lang: "go", file: "a.go", source: goSource,
			want: []Definition{
				{Path: "a.go", Line: 3, Col: 6, Kind: KindType, Lang: "go", Name: "Invoice"},
				{Path: "a.go", Line: 5, Col: 6, Kind: KindFunction, Lang: "go", Name: "NewInvoice"},
				{Path: "a.go", Line: 7, Col: 19, Kind: KindMethod, Lang: "go", Name: "Total"},
				{Path: "a.go", Line: 9, Col: 5, Kind: KindVar, Lang: "go", Name: "rate"},
			},
		},
		{
			lang: "python", file: "app.py",
			source: "class Cart:\n    pass\n\ndef checkout():\n    pass\n\nrate = 1\n",
			want: []Definition{
				{Path: "app.py", Line: 1, Col: 7, Kind: KindType, Lang: "python", Name: "Cart"},
				{Path: "app.py", Line: 4, Col: 5, Kind: KindFunction, Lang: "python", Name: "checkout"},
				{Path: "app.py", Line: 7, Col: 1, Kind: KindVar, Lang: "python", Name: "rate"},
			},
		},
		{
			lang: "rust", file: "lib.rs", source: "struct Point {}\n\nfn area() -> i32 { 0 }\n",
			want: []Definition{
				{Path: "lib.rs", Line: 1, Col: 8, Kind: KindType, Lang: "rust", Name: "Point"},
				{Path: "lib.rs", Line: 3, Col: 4, Kind: KindFunction, Lang: "rust", Name: "area"},
			},
		},
		{
			lang: "typescript", file: "a.ts", source: "interface Shape {}\n\nfunction draw() {}\n",
			want: []Definition{
				{Path: "a.ts", Line: 1, Col: 11, Kind: KindType, Lang: "typescript", Name: "Shape"},
				{Path: "a.ts", Line: 3, Col: 10, Kind: KindFunction, Lang: "typescript", Name: "draw"},
			},
		},
		{
			lang: "tsx", file: "b.tsx", source: "function App() { return <div />; }\n",
			want: []Definition{
				{Path: "b.tsx", Line: 1, Col: 10, Kind: KindFunction, Lang: "tsx", Name: "App"},
			},
		},
		{
			lang: "javascript", file: "c.js", source: "function run() {}\n",
			want: []Definition{
				{Path: "c.js", Line: 1, Col: 10, Kind: KindFunction, Lang: "javascript", Name: "run"},
			},
		},
		{
			lang: "zig", file: "d.zig", source: "fn add() void {}\n",
			want: []Definition{
				{Path: "d.zig", Line: 1, Col: 4, Kind: KindFunction, Lang: "zig", Name: "add"},
			},
		},
		{
			lang: "svelte", file: "Counter.svelte", injected: []string{"typescript", "javascript"},
			source: "<script lang=\"ts\">\n  export function increment(): number { return 0; }\n" +
				"  export class Store {}\n</script>\n\n<button>{count}</button>\n",
			want: []Definition{
				{Path: "Counter.svelte", Line: 2, Col: 19, Kind: KindFunction, Lang: "svelte", Name: "increment"},
				{Path: "Counter.svelte", Line: 3, Col: 16, Kind: KindType, Lang: "svelte", Name: "Store"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			e := newDefsEnv(t, map[string]string{tt.file: tt.source}, append([]string{tt.lang}, tt.injected...)...)
			p := e.open(t, e.config(true))
			got := definitions(t, p, DefinitionQuery{})
			assert.Subset(t, got, tt.want)
			assert.True(t, slices.IsSortedFunc(got, func(a, b Definition) int { return a.Line - b.Line }))
			st := p.Status()
			assert.True(t, st.Scanned)
			assert.Equal(t, 1, st.Files)
			assert.Equal(t, len(got), st.Records)
			assert.Empty(t, st.Skipped)
		})
	}
}

func TestDefinitionsAndSymbolsShareOneParse(t *testing.T) {
	e := newDefsEnv(t, map[string]string{"pkg/a.go": goSource}, "go")
	p := e.open(t, e.config(true))

	assert.Equal(t, []string{"a.go"}, e.parser.takeParsed())
	matches := resolve(t, p, "pkg.Invoice")
	require.Len(t, matches, 1)
	assert.Equal(t, e.uri(t, "pkg/a.go").String(), matches[0].URI)
	assert.Equal(t, []string{"Invoice", "NewInvoice", "i", "Total", "rate"},
		defNames(definitions(t, p, DefinitionQuery{})))
}

func TestLanguageWithoutSpecListsDefinitions(t *testing.T) {
	e := newEnv(t)
	uri := e.writeFile(t, "lib/app.rb")
	e.writeFile(t, "lib/other.rb")
	e.fake.grammars["ruby"] = "v0"
	e.fake.nodeResults[uri.String()] = []syntaxapi.Result{
		{File: uri, Text: "Cart", CaptureName: "local.definition.type", From: term.Coordinates{X: 6}},
		{File: uri, Text: "checkout", CaptureName: "local.definition.method", From: term.Coordinates{X: 6, Y: 1}},
		{File: uri, Text: "x", CaptureName: "local.reference", From: term.Coordinates{Y: 2}},
	}
	p := e.start(t)
	require.NoError(t, p.Wait(context.Background()))

	assert.Equal(t, []Definition{
		{Path: "lib/app.rb", Line: 1, Col: 7, Kind: KindType, Lang: "ruby", Name: "Cart"},
		{Path: "lib/app.rb", Line: 2, Col: 7, Kind: KindMethod, Lang: "ruby", Name: "checkout"},
	}, definitions(t, p, DefinitionQuery{}))
	assert.Empty(t, listReferenced(t, p), "a language without a spec qualifies no names")
	assert.Equal(t, 2, p.Status().Files)
}

func TestDefinitionsReconcileOnOpen(t *testing.T) {
	files := map[string]string{
		"a.go":   goSource,
		"b.go":   "package pkg\n\nfunc Other() {}\n",
		"app.py": "def checkout():\n    pass\n",
	}
	tests := []struct {
		name       string
		change     func(t *testing.T, e *defsEnv)
		wantParsed []string
		wantNames  []string
	}{
		{
			name:       "unchanged",
			change:     func(*testing.T, *defsEnv) {},
			wantNames:  []string{"Invoice", "NewInvoice", "i", "Total", "rate", "Other", "checkout"},
			wantParsed: []string{},
		},
		{
			name:       "modified",
			change:     func(t *testing.T, e *defsEnv) { e.write(t, "b.go", "package pkg\n\nfunc Renamed() {}\n") },
			wantParsed: []string{"b.go"},
			wantNames:  []string{"Invoice", "NewInvoice", "i", "Total", "rate", "Renamed", "checkout"},
		},
		{
			name:       "removed",
			change:     func(t *testing.T, e *defsEnv) { require.NoError(t, os.Remove(filepath.Join(e.root, "b.go"))) },
			wantParsed: []string{},
			wantNames:  []string{"Invoice", "NewInvoice", "i", "Total", "rate", "checkout"},
		},
		{
			name:       "grammar changed",
			change:     func(_ *testing.T, e *defsEnv) { e.parser.setSalt("python", "-2") },
			wantParsed: []string{"app.py"},
			wantNames:  []string{"Invoice", "NewInvoice", "i", "Total", "rate", "Other", "checkout"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, files, "go", "python")
			first := e.open(t, e.config(true))
			require.NoError(t, first.Close())
			assert.Equal(t, []string{"a.go", "app.py", "b.go"}, e.parser.takeParsed())

			tt.change(t, e)
			p := e.open(t, e.config(true))
			assert.Equal(t, tt.wantParsed, append([]string{}, e.parser.takeParsed()...))
			assert.ElementsMatch(t, tt.wantNames, defNames(definitions(t, p, DefinitionQuery{})))
		})
	}
}

func TestDefinitionsFollowEvents(t *testing.T) {
	tests := []struct {
		name      string
		change    func(t *testing.T, e *defsEnv) string
		wantNames []string
	}{
		{
			name: "file created",
			change: func(t *testing.T, e *defsEnv) string {
				e.write(t, "b.go", "package pkg\n\nfunc Added() {}\n")
				return "b.go"
			},
			wantNames: []string{"Run", "Lib", "Added"},
		},
		{
			name: "file modified",
			change: func(t *testing.T, e *defsEnv) string {
				e.write(t, "a.go", "package pkg\n\nfunc Walk() {}\n")
				return "a.go"
			},
			wantNames: []string{"Walk", "Lib"},
		},
		{
			name: "file removed",
			change: func(t *testing.T, e *defsEnv) string {
				require.NoError(t, os.Remove(filepath.Join(e.root, "a.go")))
				return "a.go"
			},
			wantNames: []string{"Lib"},
		},
		{
			name: "folder created",
			change: func(t *testing.T, e *defsEnv) string {
				e.write(t, "sub/c.go", "package sub\n\nfunc Nested() {}\n")
				return "sub"
			},
			wantNames: []string{"Run", "Lib", "Nested"},
		},
		{
			name: "folder removed",
			change: func(t *testing.T, e *defsEnv) string {
				require.NoError(t, os.RemoveAll(filepath.Join(e.root, "lib")))
				return "lib"
			},
			wantNames: []string{"Run"},
		},
		{
			name: "hidden file left out",
			change: func(t *testing.T, e *defsEnv) string {
				e.write(t, ".hidden/d.go", "package hidden\n\nfunc Secret() {}\n")
				return ".hidden/d.go"
			},
			wantNames: []string{"Run", "Lib"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, map[string]string{
				"a.go":     "package pkg\n\nfunc Run() {}\n",
				"lib/l.go": "package lib\n\nfunc Lib() {}\n",
			}, "go")
			p := e.open(t, e.config(true))
			rel := tt.change(t, e)
			p.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeChange, URI: e.uri(t, rel)})
			require.NoError(t, p.Wait(context.Background()))
			assert.ElementsMatch(t, tt.wantNames, defNames(definitions(t, p, DefinitionQuery{})))

			// What the events wrote persists.
			require.NoError(t, p.Close())
			e.parser.takeParsed()
			reopened := e.open(t, e.config(true))
			assert.Empty(t, e.parser.takeParsed())
			assert.ElementsMatch(t, tt.wantNames, defNames(definitions(t, reopened, DefinitionQuery{})))
		})
	}
}

func TestDefinitionsQuery(t *testing.T) {
	e := newDefsEnv(t, map[string]string{
		"a.go":       goSource,
		"sub/b.go":   "package sub\n\nfunc Deep() {}\n",
		"sub-x/c.go": "package x\n\ntype Sibling int\n",
		"app.py":     "def checkout():\n    pass\n",
	}, "go", "python")
	p := e.open(t, e.config(true))
	tests := []struct {
		name string
		q    DefinitionQuery
		want []string
	}{
		{
			name: "everything in path order",
			want: []string{"Invoice", "NewInvoice", "i", "Total", "rate", "checkout", "Deep", "Sibling"},
		},
		{
			name: "functions and methods", q: DefinitionQuery{Kinds: []Kind{KindFunction, KindMethod}},
			want: []string{"NewInvoice", "Total", "checkout", "Deep"},
		},
		{name: "types", q: DefinitionQuery{Kinds: []Kind{KindType}}, want: []string{"Invoice", "Sibling"}},
		{name: "language", q: DefinitionQuery{Langs: []string{"python"}}, want: []string{"checkout"}},
		{name: "root", q: DefinitionQuery{Roots: []string{"sub"}}, want: []string{"Deep"}},
		{name: "file root", q: DefinitionQuery{Roots: []string{"sub-x/c.go"}}, want: []string{"Sibling"}},
		{
			name: "several roots in path order", q: DefinitionQuery{Roots: []string{"sub", "app.py"}},
			want: []string{"checkout", "Deep"},
		},
		{
			name: "roots in one another", q: DefinitionQuery{Roots: []string{"sub/b.go", "sub", "./sub/"}},
			want: []string{"Deep"},
		},
		{name: "a root that is not there", q: DefinitionQuery{Roots: []string{"missing"}}, want: []string{}},
		{name: "a root that prefixes another", q: DefinitionQuery{Roots: []string{"sub-x"}}, want: []string{"Sibling"}},
		{
			name: "kinds in a language", q: DefinitionQuery{Kinds: []Kind{KindFunction}, Langs: []string{"go"}},
			want: []string{"NewInvoice", "Deep"},
		},
		{name: "a language without files", q: DefinitionQuery{Langs: []string{"rust"}}, want: []string{}},
		{name: "a kind without definitions", q: DefinitionQuery{Kinds: []Kind{KindVar}, Roots: []string{"sub"}}, want: []string{}},
		{
			name: "limit", q: DefinitionQuery{Kinds: []Kind{KindFunction}, Limit: 2},
			want: []string{"NewInvoice", "checkout"},
		},
		{name: "limit within a file", q: DefinitionQuery{Limit: 2}, want: []string{"Invoice", "NewInvoice"}},
		{
			name: "limit beyond what there is", q: DefinitionQuery{Kinds: []Kind{KindType}, Limit: 10},
			want: []string{"Invoice", "Sibling"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.parser.takeParsed()
			assert.Equal(t, tt.want, defNames(definitions(t, p, tt.q)))
			assert.Empty(t, e.parser.takeParsed(), "the index serves the search")
		})
	}
}

func TestDefinitionsOfLeftOutRootsAreParsed(t *testing.T) {
	e := newDefsEnv(t, map[string]string{
		"a.go":            "package pkg\n\nfunc Run() {}\n",
		".hidden/d.go":    "package hidden\n\nfunc Secret() {}\n",
		"vendor/v/v.go":   "package v\n\nfunc Vendored() {}\n",
		"vendor/v/w/w.go": "package w\n\nfunc Deeper() {}\n",
	}, "go")
	cfg := e.config(true)
	cfg.ExcludedFolders = []string{"vendor"}
	p := e.open(t, cfg)
	require.Equal(t, 1, p.Status().Files, "the index leaves out what searches do")
	assert.Empty(t, resolve(t, p, "v.Vendored"))
	tests := []struct {
		name       string
		q          DefinitionQuery
		wantNames  []string
		wantParsed []string
	}{
		{name: "the workspace", wantNames: []string{"Run"}, wantParsed: []string{}},
		{
			name:       "a hidden folder",
			q:          DefinitionQuery{Roots: []string{".hidden"}},
			wantNames:  []string{"Secret"},
			wantParsed: []string{"d.go"},
		},
		{
			name:       "a file in a hidden folder",
			q:          DefinitionQuery{Roots: []string{".hidden/d.go"}},
			wantNames:  []string{"Secret"},
			wantParsed: []string{"d.go"},
		},
		{
			name:       "an excluded folder",
			q:          DefinitionQuery{Roots: []string{"vendor"}},
			wantNames:  []string{"Vendored", "Deeper"},
			wantParsed: []string{"v.go", "w.go"},
		},
		{
			name:       "an excluded folder and one in it",
			q:          DefinitionQuery{Roots: []string{"vendor/v", "vendor"}},
			wantNames:  []string{"Vendored", "Deeper"},
			wantParsed: []string{"v.go", "w.go"},
		},
		{
			name:       "the workspace and a hidden folder",
			q:          DefinitionQuery{Roots: []string{".", ".hidden"}},
			wantNames:  []string{"Secret", "Run"},
			wantParsed: []string{"d.go"},
		},
		{
			name:       "a hidden folder in another language",
			q:          DefinitionQuery{Roots: []string{".hidden"}, Langs: []string{"python"}},
			wantNames:  []string{},
			wantParsed: []string{},
		},
		{
			name:       "an excluded folder, of a kind",
			q:          DefinitionQuery{Roots: []string{"vendor"}, Kinds: []Kind{KindType}},
			wantNames:  []string{},
			wantParsed: []string{"v.go", "w.go"},
		},
		{
			name:       "an excluded folder, up to a limit",
			q:          DefinitionQuery{Roots: []string{"vendor"}, Limit: 1},
			wantNames:  []string{"Vendored"},
			wantParsed: []string{"v.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.parser.takeParsed()
			assert.Equal(t, tt.wantNames, defNames(definitions(t, p, tt.q)))
			assert.Equal(t, tt.wantParsed, append([]string{}, e.parser.takeParsed()...),
				"only what the index leaves out is parsed, as the search reads it")
		})
	}
}

// twoFuncs is a workspace of two Go files, and a Python one parsed only
// once a test installs its grammar.
var twoFuncs = map[string]string{
	"a.go":   "package pkg\n\nfunc Run() {}\n",
	"b.go":   "package pkg\n\nfunc Walk() {}\n",
	"app.py": "def checkout():\n    pass\n",
}

func TestDefinitionsByIndexState(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		noDB bool
		// state brings the index to the state described and returns it.
		state     func(t *testing.T, e *defsEnv) *Parser
		ctx       context.Context
		wantErr   error
		wantIter  error
		wantNames []string
		// wantParsed are the files the search parses; nil leaves them
		// unchecked.
		wantParsed []string
	}{
		{
			name:       "served by the index",
			wantNames:  []string{"Run", "Walk"},
			wantParsed: []string{},
		},
		{
			name: "while the index is first built",
			state: func(t *testing.T, e *defsEnv) *Parser {
				g := e.parser.hold(t, "b.go")
				p := e.start(t, e.config(true))
				g.waitEntered(t)
				return p
			},
			wantNames: []string{},
		},
		{
			name:       "parsed without an index",
			noDB:       true,
			wantNames:  []string{"Run", "Walk"},
			wantParsed: []string{"a.go", "b.go"},
		},
		{
			name: "parsed once removed",
			state: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				require.NoError(t, p.Remove(context.Background()))
				return p
			},
			wantNames:  []string{"Run", "Walk"},
			wantParsed: []string{"a.go", "b.go"},
		},
		{
			name: "fails once closed",
			state: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				require.NoError(t, p.Close())
				return p
			},
			wantErr:    errClosed,
			wantParsed: []string{},
		},
		{
			name:       "stops when canceled, with an index",
			ctx:        canceled,
			wantIter:   context.Canceled,
			wantParsed: []string{},
		},
		{
			name:       "stops when canceled, without an index",
			noDB:       true,
			ctx:        canceled,
			wantErr:    context.Canceled,
			wantParsed: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, twoFuncs, "go")
			var p *Parser
			if tt.state != nil {
				p = tt.state(t, e)
			} else {
				p = e.open(t, e.config(!tt.noDB))
			}
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx
			}
			e.parser.takeParsed()

			it, err := p.Definitions(ctx, DefinitionQuery{})
			require.ErrorIs(t, err, tt.wantErr)
			if err == nil {
				defs, err := iterator.ToSlice(ctx, it)
				require.NoError(t, it.Close())
				require.ErrorIs(t, err, tt.wantIter)
				if err == nil {
					assert.Equal(t, tt.wantNames, defNames(defs))
				}
			}
			if tt.wantParsed != nil {
				assert.Equal(t, tt.wantParsed, append([]string{}, e.parser.takeParsed()...))
			}
		})
	}
}

func TestHandle(t *testing.T) {
	files := map[string]string{
		"a.go":     "package pkg\n\nfunc Run() {}\n",
		"lib/l.go": "package lib\n\nfunc Lib() {}\n",
	}
	walk := func(t *testing.T, e *defsEnv) { e.write(t, "a.go", "package pkg\n\nfunc Walk() {}\n") }
	on := func(tpe textapi.EventType, rel string) func(*testing.T, *defsEnv) textapi.Event {
		return func(t *testing.T, e *defsEnv) textapi.Event { return textapi.Event{Type: tpe, URI: e.uri(t, rel)} }
	}
	outside, err := workspaceapi.ParseURI("file:///elsewhere/a.go")
	require.NoError(t, err)
	tests := []struct {
		name   string
		noDB   bool
		closes bool
		state  func(t *testing.T, p *Parser)
		change func(t *testing.T, e *defsEnv)
		ev     func(t *testing.T, e *defsEnv) textapi.Event
		// wantPending is how many files the index has to look at once
		// the event is handled.
		wantPending int
		wantNames   []string
	}{
		{
			name: "a file flushed", change: walk, ev: on(textapi.EventTypeFlush, "a.go"),
			wantPending: 1, wantNames: []string{"Walk", "Lib"},
		},
		{
			name: "a file changed on disk", change: walk, ev: on(textapi.EventTypeChange, "a.go"),
			wantPending: 1, wantNames: []string{"Walk", "Lib"},
		},
		{
			name:   "a file created",
			change: func(t *testing.T, e *defsEnv) { e.write(t, "b.go", "package pkg\n\nfunc Added() {}\n") },
			ev:     on(textapi.EventTypeCreate, "b.go"), wantPending: 1, wantNames: []string{"Run", "Added", "Lib"},
		},
		{
			name:   "a file removed",
			change: func(t *testing.T, e *defsEnv) { require.NoError(t, os.Remove(filepath.Join(e.root, "a.go"))) },
			ev:     on(textapi.EventTypeRemove, "a.go"), wantPending: 1, wantNames: []string{"Lib"},
		},
		{
			name: "a file renamed",
			change: func(t *testing.T, e *defsEnv) {
				require.NoError(t, os.Rename(filepath.Join(e.root, "a.go"), filepath.Join(e.root, "c.go")))
			},
			ev: on(textapi.EventTypeRename, "a.go"), wantPending: 1, wantNames: []string{"Lib"},
		},
		{
			name:   "a folder created",
			change: func(t *testing.T, e *defsEnv) { e.write(t, "sub/c.go", "package sub\n\nfunc Nested() {}\n") },
			ev:     on(textapi.EventTypeCreate, "sub"), wantPending: 1, wantNames: []string{"Run", "Lib", "Nested"},
		},
		{
			name:   "a file searches leave out",
			change: func(t *testing.T, e *defsEnv) { e.write(t, ".hidden/d.go", "package hidden\n\nfunc Secret() {}\n") },
			ev:     on(textapi.EventTypeCreate, ".hidden/d.go"), wantPending: 1, wantNames: []string{"Run", "Lib"},
		},
		{
			name: "a file edited", change: walk, ev: on(textapi.EventTypeEdit, "a.go"),
			wantNames: []string{"Run", "Lib"},
		},
		{
			name: "a file opened", change: walk, ev: on(textapi.EventTypeOpen, "a.go"),
			wantNames: []string{"Run", "Lib"},
		},
		{
			name: "a file closed", change: walk, ev: on(textapi.EventTypeClose, "a.go"),
			wantNames: []string{"Run", "Lib"},
		},
		{
			name: "a file outside the workspace", change: walk,
			ev: func(*testing.T, *defsEnv) textapi.Event {
				return textapi.Event{Type: textapi.EventTypeFlush, URI: outside}
			},
			wantNames: []string{"Run", "Lib"},
		},
		{
			name: "the workspace itself", change: walk,
			ev: func(_ *testing.T, e *defsEnv) textapi.Event {
				return textapi.Event{Type: textapi.EventTypeChange, URI: e.rootURI}
			},
			wantNames: []string{"Run", "Lib"},
		},
		{
			name: "without an index", noDB: true, change: walk, ev: on(textapi.EventTypeFlush, "a.go"),
			wantNames: []string{"Walk", "Lib"},
		},
		{
			name:   "once removed",
			state:  func(t *testing.T, p *Parser) { require.NoError(t, p.Remove(context.Background())) },
			change: walk, ev: on(textapi.EventTypeFlush, "a.go"),
			wantNames: []string{"Walk", "Lib"},
		},
		{
			name:   "once closed",
			closes: true,
			state:  func(t *testing.T, p *Parser) { require.NoError(t, p.Close()) },
			ev:     on(textapi.EventTypeFlush, "a.go"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, files, "go")
			p := e.open(t, e.config(!tt.noDB))
			if tt.state != nil {
				tt.state(t, p)
			}
			if tt.change != nil {
				tt.change(t, e)
			}

			assert.False(t, p.Handle(context.Background(), tt.ev(t, e)), "other handlers see the event too")
			assert.Equal(t, tt.wantPending, p.Status().Pending)
			if tt.closes {
				return
			}
			wait(t, p)
			assert.Zero(t, p.Status().Pending)
			assert.ElementsMatch(t, tt.wantNames, defNames(definitions(t, p, DefinitionQuery{})))
		})
	}
}

func TestRescan(t *testing.T) {
	prime := func(t *testing.T, e *defsEnv, p *Parser) {
		require.ElementsMatch(t, []string{"Run", "Walk"}, defNames(definitions(t, p, DefinitionQuery{})),
			"the search asked for the grammar of Python, which is not installed")
		e.pkgs.install(t, "python")
	}
	tests := []struct {
		name  string
		langs []string
		// files are added to twoFuncs.
		files  map[string]string
		noDB   bool
		closes bool
		state  func(t *testing.T, p *Parser)
		change func(t *testing.T, e *defsEnv, p *Parser)
		// wantParsed are the files the Rescan parses.
		wantParsed []string
		wantNames  []string
		wantFiles  int
		// wantScans is how many scans Rescan reported to
		// Config.Progress.
		wantScans int
	}{
		{
			name:       "reads the files of a grammar installed since",
			langs:      []string{"go"},
			change:     func(t *testing.T, e *defsEnv, _ *Parser) { e.pkgs.install(t, "python") },
			wantParsed: []string{"app.py"},
			wantNames:  []string{"Run", "Walk", "checkout"},
			wantFiles:  3,
			wantScans:  1,
		},
		{
			name:       "reads the files of a grammar changed since",
			langs:      []string{"go", "python"},
			change:     func(_ *testing.T, e *defsEnv, _ *Parser) { e.parser.setSalt("python", "-2") },
			wantParsed: []string{"app.py"},
			wantNames:  []string{"Run", "Walk", "checkout"},
			wantFiles:  3,
			wantScans:  1,
		},
		{
			name:       "forgets the files of a grammar uninstalled since",
			langs:      []string{"go", "python"},
			change:     func(_ *testing.T, e *defsEnv, _ *Parser) { e.parser.setMissing("python") },
			wantParsed: []string{},
			wantNames:  []string{"Run", "Walk"},
			wantFiles:  2,
			wantScans:  1,
		},
		{
			name:  "reads the files injecting a grammar installed since",
			langs: []string{"go", "svelte"},
			files: map[string]string{
				"Counter.svelte": "<script lang=\"ts\">\n  export function increment() {}\n</script>\n",
			},
			change:     func(t *testing.T, e *defsEnv, _ *Parser) { e.pkgs.install(t, "typescript") },
			wantParsed: []string{"Counter.svelte"},
			wantNames:  []string{"Run", "Walk", "increment"},
			wantFiles:  3,
			wantScans:  1,
		},
		{
			name:  "reads what changed without events",
			langs: []string{"go"},
			change: func(t *testing.T, e *defsEnv, _ *Parser) {
				e.write(t, "a.go", "package pkg\n\nfunc Jump() {}\n")
			},
			wantParsed: []string{"a.go"},
			wantNames:  []string{"Jump", "Walk"},
			wantFiles:  2,
			wantScans:  1,
		},
		{
			name:  "forgets what was removed without events",
			langs: []string{"go"},
			change: func(t *testing.T, e *defsEnv, _ *Parser) {
				require.NoError(t, os.Remove(filepath.Join(e.root, "b.go")))
			},
			wantParsed: []string{},
			wantNames:  []string{"Run"},
			wantFiles:  1,
			wantScans:  1,
		},
		{
			name:       "reads nothing when nothing changed",
			langs:      []string{"go"},
			wantParsed: []string{},
			wantNames:  []string{"Run", "Walk"},
			wantFiles:  2,
			wantScans:  1,
		},
		{
			name:       "has searches use the grammars installed since, without an index",
			langs:      []string{"go"},
			noDB:       true,
			change:     prime,
			wantParsed: []string{},
			wantNames:  []string{"Run", "Walk", "checkout"},
		},
		{
			name:       "has searches use the grammars installed since, once removed",
			langs:      []string{"go"},
			state:      func(t *testing.T, p *Parser) { require.NoError(t, p.Remove(context.Background())) },
			change:     prime,
			wantParsed: []string{},
			wantNames:  []string{"Run", "Walk", "checkout"},
		},
		{
			name:       "does nothing once closed",
			langs:      []string{"go"},
			closes:     true,
			state:      func(t *testing.T, p *Parser) { require.NoError(t, p.Close()) },
			wantParsed: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := maps.Clone(twoFuncs)
			maps.Copy(files, tt.files)
			e := newDefsEnv(t, files, tt.langs...)
			auto := &recordingProgress{}
			cfg := e.config(!tt.noDB)
			cfg.Progress = func() Progress { return auto }
			p := e.open(t, cfg)
			if tt.state != nil {
				tt.state(t, p)
			}
			if tt.change != nil {
				tt.change(t, e, p)
			}
			e.parser.takeParsed()
			scans := auto.scans()

			p.Rescan()
			if tt.closes {
				assert.ErrorIs(t, p.Wait(context.Background()), errClosed)
			} else {
				wait(t, p)
			}
			assert.Equal(t, tt.wantParsed, append([]string{}, e.parser.takeParsed()...))
			assert.Equal(t, tt.wantScans, auto.scans()-scans)
			if tt.closes {
				return
			}
			assert.ElementsMatch(t, tt.wantNames, defNames(definitions(t, p, DefinitionQuery{})))
			assert.Equal(t, tt.wantFiles, p.Status().Files)
		})
	}
}

type recordingProgress struct {
	mu     sync.Mutex
	starts []int
	files  int
	done   []int
	stops  []error
}

func (p *recordingProgress) Start(total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts = append(p.starts, total)
}

func (p *recordingProgress) File(int, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files++
}

func (p *recordingProgress) Done(files int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = append(p.done, files)
}

func (p *recordingProgress) Stop(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops = append(p.stops, err)
}

// scans is how many scans reported they ended.
func (p *recordingProgress) scans() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.done) + len(p.stops)
}

func (p *recordingProgress) snapshot() (starts, done []int, stops int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.starts), slices.Clone(p.done), len(p.stops)
}

// queuedScans is how many scans wait for the one under way.
func (p *Parser) queuedScans() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.scans)
}

// reindexRun is a Reindex held while it parses b.go.
type reindexRun struct {
	e      *defsEnv
	p      *Parser
	gate   *parseGate
	cancel context.CancelFunc
}

// reindexQueued asks for another Reindex once it waits for the one under
// way, and returns what it returns.
func (r *reindexRun) reindexQueued(t *testing.T, prog Progress) <-chan error {
	t.Helper()
	errs := make(chan error, 1)
	go func() { errs <- r.p.Reindex(context.Background(), prog) }()
	require.Eventually(t, func() bool { return r.p.queuedScans() == 1 }, 30*time.Second, time.Millisecond)
	return errs
}

// inBackground runs fn and returns what waits for it.
func inBackground(fn func() error) func() error {
	errs := make(chan error, 1)
	go func() { errs <- fn() }()
	return func() error { return <-errs }
}

// corruptRecord has the index hold Ghost as the definition of rel, which
// it reads as unchanged since.
func corruptRecord(t *testing.T, e *defsEnv, rel string) {
	t.Helper()
	ctx := context.Background()
	files, err := e.db.Partition(filesPartition)
	require.NoError(t, err)
	uri := e.uri(t, rel).String()
	var doc fileDoc
	require.NoError(t, files.Get(ctx, uri, &doc))
	doc.Defs = encodeDefs([]record{{Kind: KindFunction, Line: 3, Col: 6, Name: "Ghost"}})
	require.NoError(t, files.Set(ctx, uri, doc))
}

func TestReindex(t *testing.T) {
	tests := []struct {
		name  string
		langs []string
		noDB  bool
		// state runs on the open index, and returns the one to reindex.
		state    func(t *testing.T, e *defsEnv, p *Parser) *Parser
		canceled bool
		// whileParsing runs while Reindex parses b.go, and returns what
		// to check once Reindex returned, if anything.
		whileParsing func(t *testing.T, r *reindexRun) func()
		wantErr      error
		// wantParsed are the files parsed until the index settles; nil
		// leaves them unchecked.
		wantParsed      []string
		wantNames       []string
		wantFiles       int
		wantRemoved     bool
		wantScannedEver bool
		wantStarts      []int
		wantDone        []int
		wantStops       int
	}{
		{
			name:            "parses every file again",
			wantParsed:      []string{"a.go", "b.go"},
			wantNames:       []string{"Run", "Walk"},
			wantFiles:       2,
			wantScannedEver: true,
			wantStarts:      []int{2},
			wantDone:        []int{2},
		},
		{
			name: "forgets what was removed without events",
			state: func(t *testing.T, e *defsEnv, p *Parser) *Parser {
				require.NoError(t, os.Remove(filepath.Join(e.root, "b.go")))
				return p
			},
			wantParsed:      []string{"a.go"},
			wantNames:       []string{"Run"},
			wantFiles:       1,
			wantScannedEver: true,
			wantStarts:      []int{1},
			wantDone:        []int{1},
		},
		{
			name: "replaces what the index holds of files unchanged since",
			state: func(t *testing.T, e *defsEnv, p *Parser) *Parser {
				require.NoError(t, p.Close())
				corruptRecord(t, e, "a.go")
				p = e.open(t, e.config(true))
				require.Equal(t, []string{"Ghost", "Walk"}, defNames(definitions(t, p, DefinitionQuery{})))
				return p
			},
			wantParsed:      []string{"a.go", "b.go"},
			wantNames:       []string{"Run", "Walk"},
			wantFiles:       2,
			wantScannedEver: true,
			wantStarts:      []int{2},
			wantDone:        []int{2},
		},
		{
			name: "reads the files of the grammars installed since",
			state: func(t *testing.T, e *defsEnv, p *Parser) *Parser {
				e.pkgs.install(t, "python")
				return p
			},
			wantParsed:      []string{"a.go", "app.py", "b.go"},
			wantNames:       []string{"Run", "checkout", "Walk"},
			wantFiles:       3,
			wantScannedEver: true,
			wantStarts:      []int{3},
			wantDone:        []int{3},
		},
		{
			name: "reports no file to parse in a workspace without any",
			state: func(t *testing.T, e *defsEnv, p *Parser) *Parser {
				require.NoError(t, os.Remove(filepath.Join(e.root, "a.go")))
				require.NoError(t, os.Remove(filepath.Join(e.root, "b.go")))
				return p
			},
			wantParsed:      []string{},
			wantNames:       []string{},
			wantScannedEver: true,
			wantDone:        []int{0},
		},
		{
			name:            "builds the index again in the background when canceled before it starts",
			canceled:        true,
			wantErr:         context.Canceled,
			wantParsed:      []string{"a.go", "b.go"},
			wantNames:       []string{"Run", "Walk"},
			wantFiles:       2,
			wantScannedEver: true,
			wantStops:       1,
		},
		{
			name: "builds the index again in the background when canceled while parsing",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				r.cancel()
				return nil
			},
			wantErr:         context.Canceled,
			wantNames:       []string{"Run", "Walk"},
			wantFiles:       2,
			wantScannedEver: true,
			wantStarts:      []int{2},
			wantStops:       1,
		},
		{
			name: "reads what changed while parsing",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				r.e.write(t, "c.go", "package pkg\n\nfunc Late() {}\n")
				r.e.handle(t, r.p, textapi.EventTypeCreate, "c.go")
				r.gate.open()
				return nil
			},
			wantParsed:      []string{"a.go", "b.go", "c.go"},
			wantNames:       []string{"Run", "Walk", "Late"},
			wantFiles:       3,
			wantScannedEver: true,
			wantStarts:      []int{2},
			wantDone:        []int{2},
		},
		{
			name: "builds again when asked while building",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				prog := &recordingProgress{}
				queued := r.reindexQueued(t, prog)
				r.gate.open()
				return func() {
					assert.NoError(t, <-queued)
					starts, done, stops := prog.snapshot()
					assert.Equal(t, []int{2}, starts)
					assert.Equal(t, []int{2}, done)
					assert.Zero(t, stops)
				}
			},
			wantParsed:      []string{"a.go", "a.go", "b.go", "b.go"},
			wantNames:       []string{"Run", "Walk"},
			wantFiles:       2,
			wantScannedEver: true,
			wantStarts:      []int{2},
			wantDone:        []int{2},
		},
		{
			name: "fails when closed while parsing",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				closed := inBackground(r.p.Close)
				return func() { assert.NoError(t, closed()) }
			},
			wantErr:    errClosed,
			wantStarts: []int{2},
			wantStops:  1,
		},
		{
			name: "fails when closed while waiting for another",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				queued := r.reindexQueued(t, &recordingProgress{})
				closed := inBackground(r.p.Close)
				return func() {
					assert.ErrorIs(t, <-queued, errClosed)
					assert.NoError(t, closed())
				}
			},
			wantErr:    errClosed,
			wantStarts: []int{2},
			wantStops:  1,
		},
		{
			name: "fails when removed while parsing",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				removed := inBackground(func() error { return r.p.Remove(context.Background()) })
				return func() { assert.NoError(t, removed()) }
			},
			wantErr:     ErrRemoved,
			wantNames:   []string{"Run", "Walk"},
			wantRemoved: true,
			wantStarts:  []int{2},
			wantStops:   1,
		},
		{
			name: "fails when removed while waiting for another",
			whileParsing: func(t *testing.T, r *reindexRun) func() {
				queued := r.reindexQueued(t, &recordingProgress{})
				removed := inBackground(func() error { return r.p.Remove(context.Background()) })
				return func() {
					assert.ErrorIs(t, <-queued, ErrRemoved)
					assert.NoError(t, removed())
				}
			},
			wantErr:     ErrRemoved,
			wantNames:   []string{"Run", "Walk"},
			wantRemoved: true,
			wantStarts:  []int{2},
			wantStops:   1,
		},
		{
			name: "fails once removed",
			state: func(t *testing.T, _ *defsEnv, p *Parser) *Parser {
				require.NoError(t, p.Remove(context.Background()))
				return p
			},
			wantErr:     ErrRemoved,
			wantParsed:  []string{},
			wantNames:   []string{"Run", "Walk"},
			wantRemoved: true,
		},
		{
			name: "fails once closed",
			state: func(t *testing.T, _ *defsEnv, p *Parser) *Parser {
				require.NoError(t, p.Close())
				return p
			},
			wantErr: errClosed,
		},
		{
			name:       "fails without an index",
			noDB:       true,
			wantErr:    errDisabled,
			wantParsed: []string{},
			wantNames:  []string{"Run", "Walk"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			langs := tt.langs
			if langs == nil {
				langs = []string{"go"}
			}
			e := newDefsEnv(t, twoFuncs, langs...)
			p := e.open(t, e.config(!tt.noDB))
			if tt.state != nil {
				p = tt.state(t, e, p)
			}
			e.parser.takeParsed()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			r := &reindexRun{e: e, p: p, cancel: cancel}
			if tt.whileParsing != nil {
				r.gate = e.parser.hold(t, "b.go")
			}

			prog := &recordingProgress{}
			reindexed := inBackground(func() error { return p.Reindex(ctx, prog) })
			var check func()
			if tt.whileParsing != nil {
				r.gate.waitEntered(t)
				check = tt.whileParsing(t, r)
			}
			assert.ErrorIs(t, reindexed(), tt.wantErr)
			if check != nil {
				check()
			}
			starts, done, stops := prog.snapshot()
			assert.Equal(t, tt.wantStarts, starts, "files to parse reported")
			assert.Equal(t, tt.wantDone, done, "builds reported done")
			assert.Equal(t, tt.wantStops, stops, "builds reported stopped")
			if tt.wantErr == errClosed {
				return
			}

			wait(t, p)
			if tt.wantParsed != nil {
				assert.Equal(t, tt.wantParsed, append([]string{}, e.parser.takeParsed()...))
			}
			assert.Equal(t, tt.wantNames, defNames(definitions(t, p, DefinitionQuery{})))
			st := p.Status()
			assert.True(t, st.Scanned, "searches need not say the index is incomplete")
			assert.Equal(t, tt.wantFiles, st.Files)
			assert.Equal(t, tt.wantRemoved, st.Removed)
			assert.Equal(t, tt.wantScannedEver, p.hasScannedEver(), "lookups are served by the index")
		})
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		noDB bool
		// duringScan removes the index while its first scan parses a.go.
		duringScan bool
		// concurrent is how many Remove calls race, of which one
		// removes the index and the rest fail; 0 is one call.
		concurrent  int
		state       func(t *testing.T, p *Parser)
		wantErr     error
		wantDropped bool
	}{
		{name: "deletes the index", wantDropped: true},
		{name: "stops the scan under way", duringScan: true, wantDropped: true},
		{name: "removes once when asked at once", concurrent: 4, wantDropped: true},
		{
			name:        "fails once removed",
			state:       func(t *testing.T, p *Parser) { require.NoError(t, p.Remove(ctx)) },
			wantErr:     ErrRemoved,
			wantDropped: true,
		},
		{
			name:    "fails once closed",
			state:   func(t *testing.T, p *Parser) { require.NoError(t, p.Close()) },
			wantErr: errClosed,
		},
		{name: "fails without an index", noDB: true, wantErr: errDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ideStorage := storagestub.NewInMemoryService()
			t.Cleanup(func() { _ = ideStorage.Close() })
			e := newDefsEnv(t, twoFuncs, "go")
			e.db = workspaceStorage(t, ideStorage, e.rootURI)
			other, err := workspaceapi.ParseURI(e.rootURI.String() + "-other")
			require.NoError(t, err)
			otherDB := workspaceStorage(t, ideStorage, other)
			require.NoError(t, otherDB.Set(ctx, metaScanID, metaDoc{Complete: true}))

			var p *Parser
			if tt.duringScan {
				g := e.parser.hold(t, "a.go")
				p = e.start(t, e.config(true))
				g.waitEntered(t)
			} else {
				p = e.open(t, e.config(!tt.noDB))
			}
			if tt.state != nil {
				tt.state(t, p)
			}

			if tt.concurrent > 0 {
				errs := make(chan error, tt.concurrent)
				for range tt.concurrent {
					go func() { errs <- p.Remove(ctx) }()
				}
				removed := 0
				for range tt.concurrent {
					if err := <-errs; err == nil {
						removed++
					} else {
						assert.ErrorIs(t, err, ErrRemoved)
					}
				}
				assert.Equal(t, 1, removed)
			} else {
				assert.ErrorIs(t, p.Remove(ctx), tt.wantErr)
			}

			dropped := func() bool {
				var m metaDoc
				if !errors.Is(e.db.Get(ctx, metaScanID, &m), storageapi.ErrNotFound) {
					return false
				}
				for _, name := range []string{filesPartition, symbolsPartition, namesPartition} {
					if countDocs(t, e.db, name) > 0 {
						return false
					}
				}
				return true
			}
			if !tt.noDB {
				assert.Equal(t, tt.wantDropped, dropped(), "the index is deleted")
			}
			var m metaDoc
			assert.NoError(t, otherDB.Get(ctx, metaScanID, &m), "the index of another workspace is kept")
			if tt.wantErr == errClosed {
				return
			}

			st := p.Status()
			assert.Equal(t, !tt.noDB, st.Removed)
			assert.True(t, st.Scanned, "searches parse the files they cover")
			assert.Zero(t, st.Files)
			e.write(t, "a.go", "package pkg\n\nfunc Jump() {}\n")
			assert.False(t, e.handle(t, p, textapi.EventTypeFlush, "a.go"))
			assert.Zero(t, p.Status().Pending, "events are not looked at")
			p.Rescan()
			wantReindex := ErrRemoved
			if tt.noDB {
				wantReindex = errDisabled
			}
			assert.ErrorIs(t, p.Reindex(ctx, &recordingProgress{}), wantReindex)
			wait(t, p)
			e.parser.takeParsed()

			assert.Equal(t, []string{"Jump", "Walk"}, defNames(definitions(t, p, DefinitionQuery{})))
			assert.Equal(t, []string{"a.go", "b.go"}, e.parser.takeParsed(), "the search parsed the files")
			resolves, lists := e.parser.backingCalls()
			assert.Len(t, resolve(t, p, "pkg.Walk"), 1)
			assert.Equal(t, []string{"pkg.Jump", "pkg.Walk"}, listReferenced(t, p))
			afterResolves, afterLists := e.parser.backingCalls()
			assert.Equal(t, resolves+1, afterResolves, "lookups go to the backing parser")
			assert.Equal(t, lists+1, afterLists, "listings go to the backing parser")
			if tt.noDB {
				return
			}
			assert.True(t, dropped(), "nothing is written to the index again")

			require.NoError(t, p.Close())
			reopened := e.open(t, e.config(true))
			assert.Equal(t, []string{"a.go", "b.go"}, e.parser.takeParsed(), "opened again, the index is built anew")
			assert.Equal(t, 2, reopened.Status().Files)
			assert.True(t, reopened.hasScannedEver())
		})
	}
}

// unlistableFiles is storage whose file records cannot be listed.
type unlistableFiles struct{ storageapi.Service }

func (s unlistableFiles) Partition(name string) (storageapi.Service, error) {
	part, err := s.Service.Partition(name)
	if err != nil || name != filesPartition {
		return part, err
	}
	return unlistable{part}, nil
}

type unlistable struct{ storageapi.Service }

func (unlistable) List(context.Context, []storageapi.Filter) (storageapi.Iterator, error) {
	return nil, errors.New("the disk is gone")
}

func TestStatus(t *testing.T) {
	files := map[string]string{"a.go": goSource, "app.py": "def checkout():\n    pass\n"}
	held := func(t *testing.T, e *defsEnv, cfg Config) *Parser {
		g := e.parser.hold(t, "a.go")
		p := e.start(t, cfg)
		g.waitEntered(t)
		return p
	}
	tests := []struct {
		name string
		// open opens the index in the state described.
		open func(t *testing.T, e *defsEnv) *Parser
		// want is the status, given the fingerprint of the Go grammar.
		want    func(fp string) Status
		wantErr bool
	}{
		{
			name: "without an index",
			open: func(t *testing.T, e *defsEnv) *Parser { return e.open(t, e.config(false)) },
			want: func(string) Status {
				return Status{Loaded: true, Scanned: true, Skipped: map[string]int{}, Grammars: map[string]string{}}
			},
		},
		{
			name: "scanning for the first time",
			open: func(t *testing.T, e *defsEnv) *Parser { return held(t, e, e.config(true)) },
			want: func(fp string) Status {
				return Status{
					Loaded: true, Pending: 1, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "scanned",
			open: func(t *testing.T, e *defsEnv) *Parser { return e.open(t, e.config(true)) },
			want: func(fp string) Status {
				return Status{
					Loaded: true, Scanned: true, Files: 1, Records: 5, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "with events to look at",
			open: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				e.handle(t, p, textapi.EventTypeFlush, "a.go")
				return p
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Scanned: true, Files: 1, Records: 5, Pending: 1, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "with a file that failed to parse",
			open: func(t *testing.T, e *defsEnv) *Parser {
				e.write(t, "bad.go", "package pkg\n")
				e.parser.setFailing("bad.go")
				return e.open(t, e.config(true))
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Scanned: true, Files: 2, Records: 5, Skipped: map[string]int{skippedParse: 1},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "reindexing",
			open: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				g := e.parser.hold(t, "a.go")
				go func() { _ = p.Reindex(context.Background(), nopProgress{}) }()
				g.waitEntered(t)
				return p
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Pending: 1, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "unreadable, while scanning",
			open: func(t *testing.T, e *defsEnv) *Parser {
				cfg := e.config(true)
				cfg.DB = unlistableFiles{e.db}
				return held(t, e, cfg)
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Pending: 1, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
			wantErr: true,
		},
		{
			name: "unreadable, once scanned",
			open: func(t *testing.T, e *defsEnv) *Parser {
				cfg := e.config(true)
				cfg.DB = unlistableFiles{e.db}
				return e.open(t, cfg)
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Scanned: true, Files: 1, Records: 5, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
		{
			name: "removed",
			open: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				require.NoError(t, p.Remove(context.Background()))
				return p
			},
			want: func(fp string) Status {
				return Status{
					Loaded: true, Scanned: true, Removed: true, Skipped: map[string]int{},
					Grammars: map[string]string{"go": fp, "python": ""},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, files, "go")
			got := tt.open(t, e).Status()
			if tt.wantErr {
				assert.NotEmpty(t, got.Err)
				got.Err = ""
			}
			assert.Equal(t, tt.want(e.fingerprint(t, "go")), got)
		})
	}
}

func TestLookupsByIndexState(t *testing.T) {
	tests := []struct {
		name string
		noDB bool
		// state brings the index to the state described and returns it.
		state func(t *testing.T, e *defsEnv) *Parser
		// wantBacking is set when lookups go to the backing parser.
		wantBacking bool
	}{
		{
			name: "before the first scan completes",
			state: func(t *testing.T, e *defsEnv) *Parser {
				g := e.parser.hold(t, "b.go")
				p := e.start(t, e.config(true))
				g.waitEntered(t)
				return p
			},
			wantBacking: true,
		},
		{
			name: "once scanned",
		},
		{
			name: "while reindexing",
			state: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				g := e.parser.hold(t, "b.go")
				go func() { _ = p.Reindex(context.Background(), nopProgress{}) }()
				g.waitEntered(t)
				return p
			},
			wantBacking: true,
		},
		{
			name: "once a canceled reindex is made up for",
			state: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.ErrorIs(t, p.Reindex(ctx, nopProgress{}), context.Canceled)
				wait(t, p)
				return p
			},
		},
		{
			name:        "without an index",
			noDB:        true,
			wantBacking: true,
		},
		{
			name: "once removed",
			state: func(t *testing.T, e *defsEnv) *Parser {
				p := e.open(t, e.config(true))
				require.NoError(t, p.Remove(context.Background()))
				return p
			},
			wantBacking: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDefsEnv(t, twoFuncs, "go")
			var p *Parser
			if tt.state != nil {
				p = tt.state(t, e)
			} else {
				p = e.open(t, e.config(!tt.noDB))
			}
			backed := 0
			if tt.wantBacking {
				backed = 1
			}

			resolves, lists := e.parser.backingCalls()
			matches := resolve(t, p, "pkg.Walk")
			require.Len(t, matches, 1)
			assert.Equal(t, e.uri(t, "b.go").String(), matches[0].URI)
			after, _ := e.parser.backingCalls()
			assert.Equal(t, resolves+backed, after, "a lookup goes to the backing parser")

			// The index answers a miss with no match, the backing parser
			// with an error: lspcmd reports both alike.
			it, err := p.ResolveSymbol(context.Background(), "pkg.Missing", nil)
			require.NoError(t, err)
			missed, err := iterator.ToSlice(context.Background(), it)
			if err != nil {
				assert.ErrorContains(t, err, "no symbols found")
			}
			assert.Empty(t, missed)
			after, _ = e.parser.backingCalls()
			assert.Equal(t, resolves+2*backed, after, "a miss goes to the backing parser")

			_, err = p.ResolveSymbol(context.Background(), "Walk", nil)
			assert.ErrorIs(t, err, syntaxapi.ErrNoDot)

			assert.Equal(t, []string{"pkg.Run", "pkg.Walk"}, listReferenced(t, p))
			_, afterLists := e.parser.backingCalls()
			assert.Equal(t, lists+backed, afterLists, "a listing goes to the backing parser")
		})
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		KindFunction: "function", KindMethod: "method", KindType: "type", KindVar: "variable", 0: "unknown",
	} {
		assert.Equal(t, want, k.String())
	}
}
