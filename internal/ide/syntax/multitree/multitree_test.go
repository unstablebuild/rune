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

package multitree

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

const (
	// hostContent has a javascript region at {8,1}-{0,3} and a css
	// region at {7,5}-{0,7}.
	hostContent = "<p>hi</p>\n<script>\nconst a = 1;\n</script>\n<p>mid</p>\n<style>\np { }\n</style>\n<p>bye</p>"
)

func jsRegion() treesitter.Injection {
	return treesitter.Injection{Language: "javascript", Range: rng(8, 1, 0, 3)}
}

func cssRegion() treesitter.Injection {
	return treesitter.Injection{Language: "css", Range: rng(7, 5, 0, 7)}
}

type fakeEdit struct {
	start, end term.Coordinates
	str        string
}

// fakeTree stands in for a treesitter.Tree: it records the buffer edits it
// is subscribed to and answers the editor services from scripted values.
type fakeTree struct {
	f   *fakes
	uri workspaceapi.URI
	buf *cell.Buffer
	loc syntax.LocationSetter
	fc  workspace.FlusherCloser

	mu            sync.Mutex
	closed        bool
	edits         []fakeEdit
	willEdit      fakeEdit
	indents       map[int]int
	cannotIndent  bool
	expand        map[term.Range]term.Range
	shrink        map[term.Range]term.Range
	comments      map[term.Range][]term.Range
	folds         []term.Range
	initialFolds  []term.Range
	publishOnEdit []textapi.Location
}

func (f *fakeTree) Commands(syntax.Handler) ([]textapi.CommandManual, syntax.CommandHandler) {
	return nil, nil
}

func (f *fakeTree) Flush(ctx context.Context) (<-chan error, error) { return f.fc.Flush(ctx) }

func (f *fakeTree) ForceFlush(ctx context.Context) (<-chan error, error) {
	return f.fc.ForceFlush(ctx)
}

func (f *fakeTree) Reload(ctx context.Context) (<-chan error, error) { return f.fc.Reload(ctx) }

func (f *fakeTree) LastFlush() time.Time { return f.fc.LastFlush() }

func (f *fakeTree) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.f.mu.Lock()
	f.f.closeOrder = append(f.f.closeOrder, f)
	f.f.mu.Unlock()
	return f.fc.Close()
}

func (f *fakeTree) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeTree) OnWillEdit(_ context.Context, start, end term.Coordinates, str string) {
	f.willEdit = fakeEdit{start: start, end: end, str: str}
}

func (f *fakeTree) OnDidEdit(context.Context, term.Coordinates, term.Coordinates, string) {
	f.mu.Lock()
	f.edits = append(f.edits, f.willEdit)
	publish := f.publishOnEdit
	f.mu.Unlock()
	if publish != nil {
		f.loc.SetLocationList(textapi.LocationSlice(publish))
	}
}

func (f *fakeTree) recordedEdits() []fakeEdit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeEdit(nil), f.edits...)
}

func (f *fakeTree) Injections() ([]treesitter.Injection, bool) {
	f.f.mu.Lock()
	defer f.f.mu.Unlock()
	return f.f.injections, f.f.injectionsOK
}

func (f *fakeTree) State() iterator.Iterator[syntax.State] {
	return iterator.FromSlice([]syntax.State{{LangID: "fake", Progress: 1}})
}

func (f *fakeTree) IndentationAt(line int) (int, bool) {
	n, ok := f.indents[line]
	return n, ok
}

func (f *fakeTree) CanIndent() bool {
	return !f.cannotIndent
}

func (f *fakeTree) SelectionExpand(r term.Range) (term.Range, bool) {
	next, ok := f.expand[r]
	return next, ok
}

func (f *fakeTree) SelectionShrink(r term.Range, caret term.Coordinates) (term.Range, bool) {
	next, ok := f.shrink[r]
	return next, ok
}

func (f *fakeTree) CommentCoverage(r term.Range) ([]term.Range, bool) {
	ranges, ok := f.comments[r]
	return ranges, ok
}

func (f *fakeTree) Folds() (iterator.Iterator[term.Range], bool) {
	if f.folds == nil {
		return nil, false
	}
	return iterator.FromSlice(f.folds), true
}

func (f *fakeTree) FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool) {
	if f.folds == nil {
		return nil, false
	}
	var ret []term.Range
	for _, r := range f.folds {
		if !coordinatesLess(r.End, pos) {
			ret = append(ret, r)
		}
	}
	return iterator.FromSlice(ret), true
}

func (f *fakeTree) InitialFolds() (iterator.Iterator[term.Range], bool) {
	if f.initialFolds == nil {
		return nil, false
	}
	return iterator.FromSlice(f.initialFolds), true
}

// fakes builds the fake host and section trees of a composite and records
// what the composite did with them.
type fakes struct {
	mu           sync.Mutex
	host         *fakeTree
	sections     []*fakeTree
	closeOrder   []*fakeTree
	injections   []treesitter.Injection
	injectionsOK bool
	// notifyOnNew makes every section tree report a missing grammar as
	// it is constructed.
	notifyOnNew bool
	// sectionFolds and sectionInitialFolds are what every section tree
	// answers, in its own coordinates.
	sectionFolds        []term.Range
	sectionInitialFolds []term.Range
}

func (f *fakes) setInjections(inj ...treesitter.Injection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injections, f.injectionsOK = inj, true
}

func (f *fakes) setInjectionsUnavailable() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injections, f.injectionsOK = nil, false
}

func (f *fakes) newTree(
	uri workspaceapi.URI, buf *cell.Buffer, loc syntax.LocationSetter, fc workspace.FlusherCloser,
) *fakeTree {
	return &fakeTree{
		f: f, uri: uri, buf: buf, loc: loc, fc: fc,
		indents: map[int]int{}, expand: map[term.Range]term.Range{},
		shrink: map[term.Range]term.Range{}, comments: map[term.Range][]term.Range{},
	}
}

func (f *fakes) newHost(
	_ context.Context, _ browserapi.Notifications, _ term.Interrupter,
	_ syntax.PkgManager, loc syntax.LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer, fc workspace.FlusherCloser,
	_ syntax.Opener, _ syntax.Config,
) syntax.Tree {
	t := f.newTree(uri, buf, loc, fc)
	f.mu.Lock()
	f.host = t
	f.mu.Unlock()
	return t
}

func (f *fakes) newSection(
	_ context.Context, n browserapi.Notifications, _ term.Interrupter,
	_ syntax.PkgManager, loc syntax.LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer, fc workspace.FlusherCloser,
	_ syntax.Opener, _ syntax.Config,
) syntax.Tree {
	t := f.newTree(uri, buf, loc, fc)
	buf.Subscribe(t)
	f.mu.Lock()
	f.sections = append(f.sections, t)
	notify := f.notifyOnNew
	t.folds, t.initialFolds = f.sectionFolds, f.sectionInitialFolds
	f.mu.Unlock()
	if notify {
		_, _ = n.Notify(browserapi.LevelWarn, "grammar for %q is not available", uri.Name())
	}
	return t
}

func (f *fakes) parse(
	_ context.Context, _ syntax.PkgManager, uri workspaceapi.URI, buf *cell.Buffer, _ syntax.Config,
) syntax.ParsedTree {
	return &fakeParsed{f: f, uri: uri, buf: buf}
}

type fakeParsed struct {
	f   *fakes
	uri workspaceapi.URI
	buf *cell.Buffer
}

func (p *fakeParsed) Install(
	_ browserapi.Notifications, _ term.Interrupter, loc syntax.LocationSetter,
	fc workspace.FlusherCloser, _ syntax.Opener,
) syntax.Tree {
	t := p.f.newTree(p.uri, p.buf, loc, fc)
	p.f.mu.Lock()
	p.f.host = t
	p.f.mu.Unlock()
	// A parsed host publishes its highlights as it is installed.
	loc.SetLocationList(textapi.LocationSlice([]textapi.Location{hostLoc}))
	return t
}

func (p *fakeParsed) Close() error { return nil }

func (f *fakes) sectionList() []*fakeTree {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeTree(nil), f.sections...)
}

func (f *fakes) liveSections() []*fakeTree {
	var ret []*fakeTree
	for _, s := range f.sectionList() {
		if !s.isClosed() {
			ret = append(ret, s)
		}
	}
	return ret
}

// scheduler queues the callbacks the composite schedules so the test runs
// them when it chooses, as the event loop would.
type scheduler struct {
	ch chan func()
}

func newScheduler() *scheduler {
	return &scheduler{ch: make(chan func(), 64)}
}

func (s *scheduler) schedule(fn func()) bool {
	s.ch <- fn
	return true
}

// tick runs the next scheduled callback, waiting for one to be scheduled.
func (s *scheduler) tick(t *testing.T) {
	t.Helper()
	select {
	case fn := <-s.ch:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("nothing scheduled")
	}
}

type recordingLoc struct {
	mu    sync.Mutex
	calls int
	last  []textapi.Location
}

func (l *recordingLoc) SetLocationList(ll textapi.LocationList) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.last = appendLocations(nil, ll)
}

func (l *recordingLoc) snapshot() (int, []textapi.Location) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, append([]textapi.Location(nil), l.last...)
}

type recordingNotifications struct {
	mu       sync.Mutex
	messages []string
}

func (n *recordingNotifications) Notify(
	_ browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, fmt.Sprintf(msg, args...))
	return "", nil
}

func (n *recordingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

type closeRecorder struct {
	nopFlusherCloser
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

var (
	hostLoc    = textapi.Location{From: xy(1, 0), To: xy(2, 0), Attr: term.Attributes{Fg: term.ColorRed}}
	sectionLoc = textapi.Location{From: xy(0, 1), To: xy(5, 1), Attr: term.Attributes{Fg: term.ColorYellow}}
)

type harness struct {
	t     *testing.T
	f     *fakes
	sched *scheduler
	buf   *cell.Buffer
	loc   *recordingLoc
	n     *recordingNotifications
	fc    *closeRecorder
	tree  *Tree
}

func newBuffer(t *testing.T, content string) *cell.Buffer {
	t.Helper()
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	return buf
}

// newHarness installs a composite over content whose host reports
// injections, and runs the tick that creates their sections.
func newHarness(t *testing.T, content string, injections ...treesitter.Injection) *harness {
	t.Helper()
	h := newHarnessPending(t, content, injections...)
	h.sched.tick(t)
	return h
}

// newHarnessPending is newHarness before the host is ready.
func newHarnessPending(t *testing.T, content string, injections ...treesitter.Injection) *harness {
	t.Helper()
	h := &harness{
		t: t, f: &fakes{}, sched: newScheduler(), buf: newBuffer(t, content),
		loc: &recordingLoc{}, n: &recordingNotifications{}, fc: &closeRecorder{},
	}
	h.f.setInjections(injections...)
	uri, err := workspaceapi.ParseURI("memory:///dir/file.svelte")
	require.NoError(t, err)
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = h.sched.schedule
	ctors := constructors{host: h.f.newHost, parse: h.f.parse, section: h.f.newSection}
	h.tree = newTree(context.Background(), ctors, h.n, term.NopInterrupter(), nil, h.loc,
		uri, h.buf, h.fc, nil, cfg)
	t.Cleanup(func() { _ = h.tree.Close() })
	return h
}

func (h *harness) edit(start, end term.Coordinates, str string) {
	h.buf.Edit(context.Background(), start, end, str)
}

func (h *harness) sectionRanges() []term.Range {
	h.tree.mu.Lock()
	defer h.tree.mu.Unlock()
	ret := make([]term.Range, len(h.tree.sections))
	for i, s := range h.tree.sections {
		ret[i] = s.rng
	}
	return ret
}

func TestNewCreatesSectionsOnceHostIsReady(t *testing.T) {
	h := newHarnessPending(t, hostContent, jsRegion(), cssRegion())
	assert.Empty(t, h.f.sectionList(), "sections must wait for the host to be parsed")
	assert.Same(t, h.tree, h.buf.View(), "the composite is the buffer's view")

	h.sched.tick(t)

	sections := h.f.sectionList()
	require.Len(t, sections, 2)
	assert.Equal(t, "memory:///dir/file.svelte.js", sections[0].uri.String())
	assert.Equal(t, "\nconst a = 1;\n", sections[0].buf.String())
	assert.Equal(t, "memory:///dir/file.svelte.css", sections[1].uri.String())
	assert.Equal(t, "\np { }\n", sections[1].buf.String())
	assert.Equal(t, []term.Range{rng(8, 1, 0, 3), rng(7, 5, 0, 7)}, h.sectionRanges())

	select {
	case <-h.tree.sectionsReady:
	default:
		t.Fatal("sectionsReady must be closed once the first reconcile ran")
	}
}

func TestNewWithoutInjectionsKeepsSingleTree(t *testing.T) {
	h := newHarnessPending(t, hostContent)
	h.f.setInjectionsUnavailable()
	h.sched.tick(t)
	assert.Empty(t, h.f.sectionList())

	h.edit(xy(0, 0), xy(0, 0), "x")
	assert.Empty(t, h.f.sectionList())
	calls, _ := h.loc.snapshot()
	assert.Equal(t, 0, calls, "nothing to publish without trees publishing")
}

func TestOnDidEditReplaysIntoSections(t *testing.T) {
	cases := []struct {
		name       string
		start, end term.Coordinates
		str        string
		// after is what the host reports once re-parsed.
		after    term.Range
		wantText string
		wantEdit []fakeEdit
	}{
		{
			name: "insert on a row above", start: xy(0, 0), end: xy(0, 0), str: "x",
			after: rng(8, 1, 0, 3), wantText: "\nconst a = 1;\n",
		},
		{
			name: "insert a line above", start: xy(0, 0), end: xy(0, 0), str: "\n",
			after: rng(8, 2, 0, 4), wantText: "\nconst a = 1;\n",
		},
		{
			name: "insert on the start row before the section", start: xy(1, 1), end: xy(1, 1), str: "xx",
			after: rng(10, 1, 0, 3), wantText: "\nconst a = 1;\n",
		},
		{
			name: "delete a line above", start: xy(0, 0), end: xy(0, 1), str: "",
			after: rng(8, 0, 0, 2), wantText: "\nconst a = 1;\n",
		},
		{
			name: "insert inside", start: xy(12, 2), end: xy(12, 2), str: "x",
			after: rng(8, 1, 0, 3), wantText: "\nconst a = 1;x\n",
			wantEdit: []fakeEdit{{start: xy(12, 1), end: xy(12, 1), str: "x"}},
		},
		{
			name: "insert a line inside", start: xy(12, 2), end: xy(12, 2), str: "\nlet b;",
			after: rng(8, 1, 0, 4), wantText: "\nconst a = 1;\nlet b;\n",
			wantEdit: []fakeEdit{{start: xy(12, 1), end: xy(12, 1), str: "\nlet b;"}},
		},
		{
			name: "delete inside", start: xy(6, 2), end: xy(8, 2), str: "",
			after: rng(8, 1, 0, 3), wantText: "\nconst = 1;\n",
			wantEdit: []fakeEdit{{start: xy(6, 1), end: xy(8, 1), str: ""}},
		},
		{
			name: "insert at the section start", start: xy(8, 1), end: xy(8, 1), str: "x",
			after: rng(8, 1, 0, 3), wantText: "x\nconst a = 1;\n",
			wantEdit: []fakeEdit{{start: xy(0, 0), end: xy(0, 0), str: "x"}},
		},
		{
			name: "insert at the section end", start: xy(0, 3), end: xy(0, 3), str: "x",
			after: rng(8, 1, 1, 3), wantText: "\nconst a = 1;\nx",
			wantEdit: []fakeEdit{{start: xy(0, 2), end: xy(0, 2), str: "x"}},
		},
		{
			name: "insert after", start: xy(0, 4), end: xy(0, 4), str: "\n\n",
			after: rng(8, 1, 0, 3), wantText: "\nconst a = 1;\n",
		},
		{
			name: "delete after", start: xy(0, 3), end: xy(0, 4), str: "",
			after: rng(8, 1, 0, 3), wantText: "\nconst a = 1;\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, hostContent, jsRegion())
			sec := h.f.sectionList()[0]

			h.f.setInjections(treesitter.Injection{Language: "javascript", Range: c.after})
			h.edit(c.start, c.end, c.str)

			assert.Equal(t, []term.Range{c.after}, h.sectionRanges())
			assert.Equal(t, c.wantText, sec.buf.String())
			assert.Equal(t, c.wantEdit, nilIfEmpty(sec.recordedEdits()))
			assert.Len(t, h.f.liveSections(), 1, "the section is kept, not recreated")
			assert.Equal(t, h.tree.text(c.after), sec.buf.String(),
				"the section buffer mirrors the host region")
		})
	}
}

func nilIfEmpty(edits []fakeEdit) []fakeEdit {
	if len(edits) == 0 {
		return nil
	}
	return edits
}

func TestOnDidEditStraddlingABoundaryReloadsTheSection(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion())
	sec := h.f.sectionList()[0]

	// Delete from inside the script through its end tag.
	after := rng(8, 1, 10, 2)
	h.f.setInjections(treesitter.Injection{Language: "javascript", Range: after})
	h.edit(xy(10, 2), xy(2, 3), "")

	assert.Equal(t, []term.Range{after}, h.sectionRanges())
	assert.Equal(t, "\nconst a = ", h.tree.text(after))
	assert.Equal(t, h.tree.text(after), sec.buf.String())
	edits := sec.recordedEdits()
	require.Len(t, edits, 1, "one whole-buffer reload")
	assert.Equal(t, xy(0, 0), edits[0].start)
	assert.Len(t, h.f.liveSections(), 1)
}

func TestOnDidEditBetweenTwoSections(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion(), cssRegion())
	js, css := h.f.sectionList()[0], h.f.sectionList()[1]

	h.f.setInjections(jsRegion(), treesitter.Injection{Language: "css", Range: rng(7, 6, 0, 8)})
	h.edit(xy(0, 4), xy(0, 4), "\n")

	assert.Equal(t, []term.Range{rng(8, 1, 0, 3), rng(7, 6, 0, 8)}, h.sectionRanges())
	assert.Empty(t, js.recordedEdits())
	assert.Empty(t, css.recordedEdits())
	assert.Equal(t, "\np { }\n", css.buf.String())
	assert.Len(t, h.f.liveSections(), 2)
}

func TestReconcile(t *testing.T) {
	t.Run("unchanged region keeps the section without a reload", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion())
		sec := h.f.sectionList()[0]
		h.tree.reconcile()
		assert.Empty(t, sec.recordedEdits())
		assert.Equal(t, []*fakeTree{sec}, h.f.liveSections())
	})

	t.Run("drifted region reloads the section once", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion())
		sec := h.f.sectionList()[0]
		drifted := rng(8, 1, 5, 2)
		h.f.setInjections(treesitter.Injection{Language: "javascript", Range: drifted})
		h.tree.reconcile()
		assert.Equal(t, []term.Range{drifted}, h.sectionRanges())
		assert.Equal(t, "\nconst", sec.buf.String())
		assert.Len(t, sec.recordedEdits(), 1)
		assert.Equal(t, []*fakeTree{sec}, h.f.liveSections())
	})

	t.Run("language change recreates the section", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion())
		old := h.f.sectionList()[0]
		h.f.setInjections(treesitter.Injection{Language: "typescript", Range: jsRegion().Range})
		h.tree.reconcile()
		assert.True(t, old.isClosed())
		live := h.f.liveSections()
		require.Len(t, live, 1)
		assert.NotSame(t, old, live[0])
		assert.Equal(t, "memory:///dir/file.svelte.ts", live[0].uri.String())
	})

	t.Run("vanished region closes the section", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion(), cssRegion())
		js, css := h.f.sectionList()[0], h.f.sectionList()[1]
		h.f.setInjections(cssRegion())
		h.tree.reconcile()
		assert.True(t, js.isClosed())
		assert.False(t, css.isClosed())
		assert.Equal(t, []term.Range{cssRegion().Range}, h.sectionRanges())
	})

	t.Run("host without injections closes every section", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion(), cssRegion())
		h.f.setInjectionsUnavailable()
		h.tree.reconcile()
		assert.Empty(t, h.f.liveSections())
		assert.Empty(t, h.sectionRanges())
	})

	t.Run("region overlapping the previous one is ignored", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion(),
			treesitter.Injection{Language: "css", Range: rng(0, 2, 0, 5)})
		assert.Equal(t, []term.Range{jsRegion().Range}, h.sectionRanges())
		assert.Len(t, h.f.liveSections(), 1)
	})

	t.Run("flush reconciles before reporting", func(t *testing.T) {
		h := newHarness(t, hostContent, jsRegion())
		h.f.setInjections(jsRegion(), cssRegion())
		ch, err := h.tree.Flush(context.Background())
		require.NoError(t, err)
		h.sched.tick(t)
		require.NoError(t, <-ch)
		assert.Equal(t, []term.Range{jsRegion().Range, cssRegion().Range}, h.sectionRanges())
	})
}

func TestHighlightsAreMergedInHostCoordinates(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion())
	host, sec := h.f.host, h.f.sectionList()[0]

	host.loc.SetLocationList(textapi.LocationSlice([]textapi.Location{hostLoc}))
	_, got := h.loc.snapshot()
	assert.Equal(t, []textapi.Location{hostLoc}, got)

	sec.loc.SetLocationList(textapi.LocationSlice([]textapi.Location{sectionLoc}))
	_, got = h.loc.snapshot()
	want := sectionLoc
	want.From, want.To = xy(0, 2), xy(5, 2)
	assert.Equal(t, []textapi.Location{hostLoc, want}, got, "host first, section translated")

	t.Run("an edit above shifts the section highlights without a re-highlight", func(t *testing.T) {
		h.f.setInjections(treesitter.Injection{Language: "javascript", Range: rng(8, 2, 0, 4)})
		calls, _ := h.loc.snapshot()
		h.edit(xy(0, 0), xy(0, 0), "\n")
		after, got := h.loc.snapshot()
		assert.Equal(t, calls+1, after, "one merged publish per edit")
		shifted := sectionLoc
		shifted.From, shifted.To = xy(0, 3), xy(5, 3)
		assert.Equal(t, []textapi.Location{hostLoc, shifted}, got)
		assert.Empty(t, sec.recordedEdits())
	})

	t.Run("publishes during an edit are batched into one", func(t *testing.T) {
		sec.mu.Lock()
		sec.publishOnEdit = []textapi.Location{sectionLoc, sectionLoc}
		sec.mu.Unlock()
		host.loc.SetLocationList(textapi.LocationSlice([]textapi.Location{hostLoc, hostLoc}))
		calls, _ := h.loc.snapshot()
		h.edit(xy(12, 3), xy(12, 3), "x")
		after, got := h.loc.snapshot()
		assert.Equal(t, calls+1, after)
		shifted := sectionLoc
		shifted.From, shifted.To = xy(0, 3), xy(5, 3)
		assert.Equal(t, []textapi.Location{hostLoc, hostLoc, shifted, shifted}, got)
	})

	t.Run("a closed section's highlights leave the merged list", func(t *testing.T) {
		h.f.setInjectionsUnavailable()
		h.tree.reconcile()
		_, got := h.loc.snapshot()
		assert.Equal(t, []textapi.Location{hostLoc, hostLoc}, got)
	})
}

func TestIndentationAt(t *testing.T) {
	// Rows: 0 <div>, 1 \t<script>, 2 (blank), 3 \t\tconst a = 1;,
	// 4 \t\tfunction f() {, 5 (blank), 6 \t</script>, 7 \t<!-- c -->,
	// 8 \t<style>, 9 (blank), 10 \t</style>, 11 </div>
	content := "<div>\n\t<script>\n\n\t\tconst a = 1;\n\t\tfunction f() {\n\n\t</script>\n" +
		"\t<!-- c -->\n\t<style>\n\n\t</style>\n</div>"
	script := treesitter.Injection{Language: "javascript", Range: rng(9, 1, 1, 6)}
	comment := treesitter.Injection{Language: "comment", Range: rng(1, 7, 11, 7)}
	style := treesitter.Injection{Language: "css", Range: rng(8, 8, 1, 10)}
	h := newHarness(t, content, script, comment, style)
	host, sec := h.f.host, h.f.sectionList()[0]
	for line := range 12 {
		host.indents[line] = 100 + line
	}
	sec.indents[3] = 0 // row 4, the function line
	sec.indents[4] = 1 // row 5, inside the function body

	cases := []struct {
		name string
		line int
		want int
		ok   bool
	}{
		{name: "script start row goes to the host", line: 1, want: 101, ok: true},
		{name: "blank row before the first token goes to the host", line: 2, want: 102, ok: true},
		{name: "section without an answer", line: 3},
		{name: "base alone when the section adds nothing", line: 4, want: 2, ok: true},
		{name: "trailing blank row adds the section indent to the base", line: 5, want: 3, ok: true},
		{name: "script end row goes to the host", line: 6, want: 106, ok: true},
		{name: "comment sections are skipped", line: 7, want: 107, ok: true},
		{name: "blank section goes to the host", line: 9, want: 109, ok: true},
		{name: "outside goes to the host", line: 11, want: 111, ok: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := h.tree.IndentationAt(c.line)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
	t.Run("a section whose tree cannot indent goes to the host", func(t *testing.T) {
		sec.cannotIndent = true
		got, ok := h.tree.IndentationAt(4)
		assert.True(t, ok)
		assert.Equal(t, 104, got)
	})
}

func TestFirstNonBlankRow(t *testing.T) {
	cells := func(content string) [][]term.Cell {
		return newBuffer(t, content).RawCells()
	}
	cases := []struct {
		name    string
		content string
		rng     term.Range
		want    int
		ok      bool
		tabs    int
	}{
		{
			name:    "first non-blank row",
			content: "<script>\n\n\t\tconst a;\n</script>",
			rng:     rng(8, 0, 0, 3),
			want:    2, ok: true, tabs: 2,
		},
		{
			name:    "code on the start row uses that row",
			content: "\t<script>const a;\n\t\t\tlet b;\n\t</script>",
			rng:     rng(9, 0, 1, 2),
			want:    0, ok: true, tabs: 1,
		},
		{
			name:    "blank section",
			content: "\t<script>\n\n\t</script>",
			rng:     rng(9, 0, 1, 2),
		},
		{
			name:    "text before the start column is not the section's",
			content: "\t\t<script>\n\n\t</script>",
			rng:     rng(10, 0, 1, 2),
		},
		{
			name:    "end row counts only up to the end column",
			content: "<script>\n\n</script>",
			rng:     rng(8, 0, 0, 2),
		},
		{
			name:    "spaces are not tabs",
			content: "<script>\n    const a;\n</script>",
			rng:     rng(8, 0, 0, 2),
			want:    1, ok: true, tabs: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cells := cells(c.content)
			got, ok := firstNonBlankRow(cells, c.rng)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
			if ok {
				assert.Equal(t, c.tabs, leadingTabs(cells[got]))
			}
		})
	}
}

func TestSelectionRouting(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion())
	host, sec := h.f.host, h.f.sectionList()[0]
	ident := rng(6, 2, 7, 2)
	statement := rng(0, 2, 12, 2)
	sec.expand[rng(6, 1, 7, 1)] = rng(0, 1, 12, 1)
	host.expand[statement] = rng(0, 1, 9, 3)
	host.expand[rng(0, 0, 3, 0)] = rng(0, 0, 9, 0)
	sec.shrink[rng(0, 1, 12, 1)] = rng(6, 1, 7, 1)
	host.shrink[rng(0, 1, 9, 3)] = rng(8, 1, 0, 3)
	sec.comments[rng(0, 1, 12, 1)] = []term.Range{rng(0, 1, 12, 1)}
	host.comments[rng(0, 4, 3, 4)] = []term.Range{rng(0, 4, 10, 4)}

	t.Run("expand inside the section", func(t *testing.T) {
		got, ok := h.tree.SelectionExpand(ident)
		require.True(t, ok)
		assert.Equal(t, statement, got)
	})
	t.Run("expand past the section root falls through to the host", func(t *testing.T) {
		got, ok := h.tree.SelectionExpand(statement)
		require.True(t, ok)
		assert.Equal(t, rng(0, 1, 9, 3), got)
	})
	t.Run("expand outside goes to the host", func(t *testing.T) {
		got, ok := h.tree.SelectionExpand(rng(0, 0, 3, 0))
		require.True(t, ok)
		assert.Equal(t, rng(0, 0, 9, 0), got)
	})
	t.Run("shrink inside the section", func(t *testing.T) {
		got, ok := h.tree.SelectionShrink(statement, xy(6, 2))
		require.True(t, ok)
		assert.Equal(t, ident, got)
	})
	t.Run("shrink of a range crossing the section goes to the host", func(t *testing.T) {
		got, ok := h.tree.SelectionShrink(rng(0, 1, 9, 3), xy(6, 2))
		require.True(t, ok)
		assert.Equal(t, rng(8, 1, 0, 3), got)
	})
	t.Run("comment coverage inside the section", func(t *testing.T) {
		got, ok := h.tree.CommentCoverage(statement)
		require.True(t, ok)
		assert.Equal(t, []term.Range{statement}, got)
	})
	t.Run("comment coverage outside goes to the host", func(t *testing.T) {
		got, ok := h.tree.CommentCoverage(rng(0, 4, 3, 4))
		require.True(t, ok)
		assert.Equal(t, []term.Range{rng(0, 4, 10, 4)}, got)
	})
}

func TestFoldsAggregateInHostCoordinates(t *testing.T) {
	h := newHarnessPending(t, hostContent, jsRegion(), cssRegion())
	host := h.f.host
	host.folds = []term.Range{rng(0, 0, 9, 0), rng(0, 5, 8, 7)}
	host.initialFolds = []term.Range{rng(0, 5, 8, 7)}
	h.f.sectionFolds = []term.Range{rng(0, 1, 1, 1)}
	h.f.sectionInitialFolds = []term.Range{rng(0, 1, 1, 1)}

	initial, ok := h.tree.InitialFolds()
	require.True(t, ok)
	initialDone := make(chan []term.Range, 1)
	go func() {
		got, err := iterator.ToSlice(context.Background(), initial)
		assert.NoError(t, err)
		initialDone <- got
	}()

	h.sched.tick(t)

	t.Run("folds", func(t *testing.T) {
		it, ok := h.tree.Folds()
		require.True(t, ok)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []term.Range{
			rng(0, 0, 9, 0), rng(0, 5, 8, 7), rng(0, 2, 1, 2), rng(0, 6, 1, 6),
		}, got)
	})
	t.Run("folds from skips sections ending before the position", func(t *testing.T) {
		it, ok := h.tree.FoldsFrom(xy(0, 4))
		require.True(t, ok)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []term.Range{rng(0, 5, 8, 7), rng(0, 6, 1, 6)}, got)
	})
	t.Run("initial folds wait for the sections", func(t *testing.T) {
		select {
		case got := <-initialDone:
			assert.Equal(t, []term.Range{rng(0, 5, 8, 7), rng(0, 2, 1, 2), rng(0, 6, 1, 6)}, got)
		case <-time.After(5 * time.Second):
			t.Fatal("initial folds did not complete")
		}
	})
}

func TestStateAndCommandsComeFromTheHost(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion())
	it := h.tree.State()
	state, ok := it.Next(context.Background())
	require.True(t, ok)
	assert.Equal(t, "fake", state.LangID)
	require.NoError(t, it.Close())
	cmds, handler := h.tree.Commands(nil)
	assert.Nil(t, cmds)
	assert.Nil(t, handler)
}

func TestCloseClosesSectionsThenHostOnce(t *testing.T) {
	h := newHarness(t, hostContent, jsRegion(), cssRegion())
	host := h.f.host
	js, css := h.f.sectionList()[0], h.f.sectionList()[1]

	require.NoError(t, h.tree.Close())
	assert.Equal(t, []*fakeTree{js, css, host}, h.f.closeOrder)
	assert.True(t, h.fc.closed, "the host closes the wrapped FlusherCloser")
	require.NoError(t, h.tree.Close())
	assert.Len(t, h.f.closeOrder, 3)

	h.edit(xy(0, 0), xy(0, 0), "x")
	assert.Empty(t, js.recordedEdits(), "a closed composite ignores edits")
	_, ok := h.tree.Folds()
	assert.False(t, ok)
	h.tree.reconcile()
	assert.Empty(t, h.f.liveSections())
}

func TestParseInstallCreatesSectionsSynchronously(t *testing.T) {
	f := &fakes{}
	f.setInjections(jsRegion())
	buf := newBuffer(t, hostContent)
	uri, err := workspaceapi.ParseURI("memory:///dir/file.svelte")
	require.NoError(t, err)
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = func(func()) bool {
		t.Fatal("Install must not schedule")
		return false
	}
	ctors := constructors{host: f.newHost, parse: f.parse, section: f.newSection}

	parsed := parse(context.Background(), ctors, nil, uri, buf, cfg)
	assert.Empty(t, f.sectionList())

	loc := &recordingLoc{}
	tree := parsed.Install(&recordingNotifications{}, term.NopInterrupter(), loc,
		&closeRecorder{}, nil)
	t.Cleanup(func() { _ = tree.Close() })

	assert.Len(t, f.sectionList(), 1)
	assert.Same(t, tree, buf.View())
	_, got := loc.snapshot()
	assert.Equal(t, []textapi.Location{hostLoc}, got, "host highlights published on install")
	select {
	case <-tree.(*Tree).sectionsReady:
	default:
		t.Fatal("sections must be ready after Install")
	}
}

func TestParsedTreeCloseWithoutInstall(t *testing.T) {
	f := &fakes{}
	parsed := parse(context.Background(),
		constructors{host: f.newHost, parse: f.parse, section: f.newSection},
		nil, workspaceapi.URI{}, newBuffer(t, hostContent), syntax.DefaultConfig())
	require.NoError(t, parsed.Close())
	assert.Empty(t, f.sectionList())
}

func TestMissingGrammarIsReportedOncePerLanguage(t *testing.T) {
	h := newHarnessPending(t, hostContent, jsRegion(), cssRegion(),
		treesitter.Injection{Language: "css", Range: rng(0, 8, 3, 8)})
	h.f.notifyOnNew = true
	h.sched.tick(t)
	require.Len(t, h.f.sectionList(), 3)
	assert.Equal(t, []string{
		`grammar for "file.svelte.js" is not available`,
		`grammar for "file.svelte.css" is not available`,
	}, h.n.messages)
}
