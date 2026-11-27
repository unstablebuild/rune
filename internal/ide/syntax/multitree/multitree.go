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

// Package multitree composes one syntax tree per language of a file. The
// host tree parses the file with the grammar of its language; every region
// that grammar's injections query marks as another language is parsed by a
// section tree running on a private copy of the region, kept in step with
// the file by replaying its edits. Highlights, folds, indentation, syntactic
// selection and comment coverage are routed to the tree that owns the
// position and reported in the file's coordinates.
package multitree

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

var _ syntax.NewTreeFunc = New

// New installs a composite tree into buf, as treesitter.New installs a single
// tree: the host tree is treesitter.New's, and the composite replaces it as
// buf's view. See syntax.NewTreeFunc.
func New(
	ctx context.Context,
	n browserapi.Notifications, interrupter term.Interrupter,
	pkg syntax.PkgManager, loc syntax.LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer,
	fc workspace.FlusherCloser,
	opener syntax.Opener,
	config syntax.Config,
) syntax.Tree {
	return newTree(ctx, defaultConstructors, n, interrupter, pkg, loc, uri, buf, fc, opener, config)
}

var _ syntax.ParseTreeFunc = Parse

// Parse parses buf with treesitter.Parse into the host tree of a composite
// tree that is not installed yet. Installing it creates the sections of the
// parsed host synchronously; they highlight once their own trees are
// parsed. See syntax.ParseTreeFunc.
func Parse(
	ctx context.Context, pkg syntax.PkgManager, uri workspaceapi.URI, buf *cell.Buffer,
	config syntax.Config,
) syntax.ParsedTree {
	return parse(ctx, defaultConstructors, pkg, uri, buf, config)
}

// constructors are the tree constructors a composite tree builds on. Tests
// substitute fakes for them.
type constructors struct {
	host    syntax.NewTreeFunc
	parse   syntax.ParseTreeFunc
	section syntax.NewTreeFunc
}

var defaultConstructors = constructors{
	host:    treesitter.New,
	parse:   treesitter.Parse,
	section: treesitter.New,
}

// injector is the host tree capability the composite builds its sections
// from. A host without it, or whose grammar ships no injections query, keeps
// the composite to a single tree.
type injector interface {
	Injections() ([]treesitter.Injection, bool)
}

type indentService interface {
	IndentationAt(line int) (int, bool)
}

// indentCapable is the section tree capability that tells a tree unable to
// indent at all, like a comment's or a declined language's, from one that
// leaves a line as is.
type indentCapable interface {
	CanIndent() bool
}

type selectionService interface {
	SelectionExpand(rng term.Range) (term.Range, bool)
	SelectionShrink(rng term.Range, caret term.Coordinates) (term.Range, bool)
}

type commentService interface {
	CommentCoverage(rng term.Range) ([]term.Range, bool)
}

type foldsService interface {
	Folds() (iterator.Iterator[term.Range], bool)
	FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool)
	InitialFolds() (iterator.Iterator[term.Range], bool)
}

type stateService interface {
	State() iterator.Iterator[syntax.State]
}

// Tree is a composite syntax tree. See the package documentation.
type Tree struct {
	ctx         context.Context
	ctors       constructors
	n           browserapi.Notifications
	interrupter term.Interrupter
	pkg         syntax.PkgManager
	loc         syntax.LocationSetter
	uri         workspaceapi.URI
	buf         *cell.Buffer
	opener      syntax.Opener
	config      syntax.Config

	host    syntax.Tree
	hostInj injector
	// view is the view buf had before the composite replaced it: the host
	// tree, which reads buf's cells.
	view cell.View

	// mu guards the fields below. It is never held while calling into the
	// host or a section tree: those publish highlights under their own
	// locks through setters that take mu, so the reverse order would
	// deadlock.
	mu       sync.Mutex
	closed   bool
	sections []*section
	// hostHL is the host's last highlight list, in host coordinates.
	hostHL []textapi.Location
	// merged and mergedSpare alternate as the list handed to loc, which
	// keeps the last one until the next call.
	merged      []textapi.Location
	mergedSpare []textapi.Location
	// pending records that a tree published since loc was last handed the
	// merged list. publishing suppresses the publishes the trees trigger
	// while an edit is replayed and reconciled, so loc sees one merged
	// list per edit. publishScheduled records that the sections'
	// publishes are merged on the next tick, so the sections that publish
	// in one frame cost one merge.
	pending          bool
	publishing       bool
	publishScheduled bool
	// notified holds the languages whose missing grammar has been
	// reported, so every section of that language does not repeat it.
	notified map[string]bool
	// sectionsReady is closed once the sections of the parsed host exist,
	// also when the host failed to parse, so initial folds can be drained.
	sectionsReady chan struct{}
	readyOnce     sync.Once

	onWillEditStart term.Coordinates
	onWillEditEnd   term.Coordinates
	onWillEditStr   string
}

func newComposite(
	ctx context.Context, ctors constructors,
	pkg syntax.PkgManager, uri workspaceapi.URI, buf *cell.Buffer, config syntax.Config,
) *Tree {
	if config.ScheduleNextTick == nil {
		panic("invalid config")
	}
	return &Tree{
		ctx:           ctx,
		ctors:         ctors,
		pkg:           pkg,
		uri:           uri,
		buf:           buf,
		config:        config,
		notified:      make(map[string]bool),
		sectionsReady: make(chan struct{}),
	}
}

func newTree(
	ctx context.Context, ctors constructors,
	n browserapi.Notifications, interrupter term.Interrupter,
	pkg syntax.PkgManager, loc syntax.LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer,
	fc workspace.FlusherCloser,
	opener syntax.Opener,
	config syntax.Config,
) *Tree {
	c := newComposite(ctx, ctors, pkg, uri, buf, config)
	c.n, c.interrupter, c.loc, c.opener = n, interrupter, loc, opener
	host := ctors.host(ctx, n, interrupter, pkg,
		syntax.FuncLocationSetter(c.setHostHighlights), uri, buf, fc, opener, config)
	c.install(host)

	go debug.CapturePanicReport(func() {
		// The host's first state is its readiness, parsed or failed.
		if svc, ok := host.(stateService); ok {
			it := svc.State()
			_, _ = it.Next(ctx)
			_ = it.Close()
		}
		if !config.ScheduleNextTick(c.hostReady) {
			c.markSectionsReady()
		}
	})
	return c
}

func parse(
	ctx context.Context, ctors constructors,
	pkg syntax.PkgManager, uri workspaceapi.URI, buf *cell.Buffer, config syntax.Config,
) *parsedTree {
	c := newComposite(ctx, ctors, pkg, uri, buf, config)
	return &parsedTree{c: c, host: ctors.parse(ctx, pkg, uri, buf, config)}
}

// parsedTree is a composite tree whose host Parse parsed, waiting to be
// installed.
type parsedTree struct {
	c    *Tree
	host syntax.ParsedTree
}

func (p *parsedTree) Install(
	n browserapi.Notifications, interrupter term.Interrupter, loc syntax.LocationSetter,
	fc workspace.FlusherCloser, opener syntax.Opener,
) syntax.Tree {
	c := p.c
	c.n, c.interrupter, c.loc, c.opener = n, interrupter, loc, opener
	host := p.host.Install(n, interrupter,
		syntax.FuncLocationSetter(c.setHostHighlights), fc, opener)
	c.install(host)
	c.hostReady()
	return c
}

func (p *parsedTree) Close() error {
	return p.host.Close()
}

// install makes the composite buf's view and subscribes it to buf's edits
// after host, which subscribed itself, so host has re-parsed by the time the
// composite's OnDidEdit runs.
func (c *Tree) install(host syntax.Tree) {
	c.host = host
	c.hostInj, _ = host.(injector)
	c.view = c.buf.WithView(c)
	c.buf.Subscribe(c)
}

// hostReady runs on the event loop once the host is parsed or has failed.
func (c *Tree) hostReady() {
	c.reconcile()
	c.markSectionsReady()
}

func (c *Tree) markSectionsReady() {
	c.readyOnce.Do(func() { close(c.sectionsReady) })
}

// Section is a region of the file parsed by its own tree.
type Section struct {
	// Language is the Rune language ID of the region's grammar.
	Language string
	// Range is the region in file coordinates, End exclusive.
	Range term.Range
}

// Sections returns the regions currently parsed by their own trees, in
// document order. It is empty until the host is parsed, and stays empty for
// a host whose grammar marks no injections.
func (c *Tree) Sections() []Section {
	c.mu.Lock()
	defer c.mu.Unlock()
	ret := make([]Section, len(c.sections))
	for i, s := range c.sections {
		ret[i] = Section{Language: s.lang, Range: s.rng}
	}
	return ret
}

// Commands returns the host's commands.
func (c *Tree) Commands(handler syntax.Handler) ([]textapi.CommandManual, syntax.CommandHandler) {
	return c.host.Commands(handler)
}

// State streams the state of the host tree, whose language is the file's.
func (c *Tree) State() iterator.Iterator[syntax.State] {
	if svc, ok := c.host.(stateService); ok {
		return svc.State()
	}
	return iterator.Empty[syntax.State]()
}

// Close closes the sections, then the host, which closes the wrapped
// FlusherCloser.
func (c *Tree) Close() (ret error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sections := c.sections
	c.sections = nil
	c.mu.Unlock()
	c.markSectionsReady()

	for _, s := range sections {
		if err := s.tree.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	c.buf.Unsubscribe(c)
	if err := c.host.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	return ret
}

// Flush flushes through the host; the result is sent once the sections are
// in step with the host's re-parse.
func (c *Tree) Flush(ctx context.Context) (<-chan error, error) {
	return c.wrapReconcile(c.host.Flush(ctx))
}

// Reload reloads through the host; the result is sent once the sections are
// in step with the host's re-parse.
func (c *Tree) Reload(ctx context.Context) (<-chan error, error) {
	return c.wrapReconcile(c.host.Reload(ctx))
}

// ForceFlush flushes through the host; the result is sent once the sections
// are in step with the host's re-parse.
func (c *Tree) ForceFlush(ctx context.Context) (<-chan error, error) {
	return c.wrapReconcile(c.host.ForceFlush(ctx))
}

// LastFlush is the host's.
func (c *Tree) LastFlush() time.Time {
	return c.host.LastFlush()
}

// wrapReconcile forwards the host's result once the sections have been
// reconciled with the host's re-parse, keeping the host's promise that a
// send means the tree is in step with the file.
func (c *Tree) wrapReconcile(inner <-chan error, err error) (<-chan error, error) {
	if err != nil {
		return nil, err
	}
	out := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		res := <-inner
		forward := func() {
			out <- res
			close(out)
		}
		if !c.config.ScheduleNextTick(func() {
			c.reconcile()
			forward()
		}) {
			forward()
		}
	})
	return out, nil
}

// Rows implements cell.View over the file's cells.
func (c *Tree) Rows() int {
	return c.view.Rows()
}

// Columns implements cell.View over the file's cells.
func (c *Tree) Columns(row int) int {
	return c.view.Columns(row)
}

// Cell implements cell.View over the file's cells.
func (c *Tree) Cell(at term.Coordinates) (term.Cell, bool) {
	return c.view.Cell(at)
}

// RawCells implements cell.View over the file's cells.
func (c *Tree) RawCells() [][]term.Cell {
	return c.view.RawCells()
}

// String implements cell.View over the file's cells.
func (c *Tree) String() string {
	return c.view.String()
}

// OnWillEdit records the edit OnDidEdit replays into the sections.
func (c *Tree) OnWillEdit(ctx context.Context, start, end term.Coordinates, str string) {
	c.onWillEditStart = start
	c.onWillEditEnd = end
	c.onWillEditStr = str
}

// OnDidEdit replays the edit into the sections it falls in, moves the ones
// after it and reconciles the sections with the host's re-parse, then
// publishes the merged highlights once.
func (c *Tree) OnDidEdit(ctx context.Context, from, to term.Coordinates, old string) {
	start, end, str := c.onWillEditStart, c.onWillEditEnd, c.onWillEditStr

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.publishing = true
	type replay struct {
		s          *section
		start, end term.Coordinates
	}
	var replays []replay
	for _, s := range c.sections {
		switch {
		case coordinatesLessOrEqual(s.rng.Start, start) && coordinatesLessOrEqual(end, s.rng.End):
			replays = append(replays, replay{s: s, start: s.toLocal(start), end: s.toLocal(end)})
			s.rng.End = shift(s.rng.End, end, to)
		case coordinatesLessOrEqual(end, s.rng.Start):
			s.rng.Start = shift(s.rng.Start, end, to)
			s.rng.End = shift(s.rng.End, end, to)
		case coordinatesLessOrEqual(s.rng.End, start):
		default:
			s.dirty = true
		}
	}
	c.mu.Unlock()

	for _, r := range replays {
		r.s.buf.Edit(ctx, r.start, r.end, str)
	}
	c.reconcile()

	c.mu.Lock()
	c.publishing = false
	// Sections after the edit moved with it, so their highlights must
	// be re-translated even when no tree published.
	if c.pending || len(c.sections) > 0 {
		c.publishLocked()
	}
	c.mu.Unlock()
}

// reconcile brings the sections in line with the regions the host reports
// now: it keeps the sections whose region is unchanged, reloads the ones of
// the same language whose region moved, creates the new ones and closes
// the rest. Regions overlapping an earlier one are ignored.
func (c *Tree) reconcile() {
	var detected []treesitter.Injection
	var ok bool
	if c.hostInj != nil {
		detected, ok = c.hostInj.Injections()
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	prev := c.sections
	used := make([]bool, len(prev))
	next := make([]*section, 0, len(detected))
	// creates are the indices of next holding a placeholder for a
	// section to create once mu is released: constructing a tree may
	// call back into the composite.
	var creates []int
	type reload struct {
		s    *section
		text string
	}
	var reloads []reload
	if ok {
		var lastEnd term.Coordinates
		for i, d := range detected {
			if i > 0 && coordinatesLess(d.Range.Start, lastEnd) {
				c.log(log.DebugLevel, "ignoring %s injection at %v overlapping the previous one",
					d.Language, d.Range)
				continue
			}
			lastEnd = d.Range.End
			if idx := matchSection(prev, used, d); idx >= 0 {
				used[idx] = true
				s := prev[idx]
				if s.dirty || s.rng != d.Range {
					reloads = append(reloads, reload{s: s, text: c.text(d.Range)})
				}
				s.rng, s.dirty = d.Range, false
				next = append(next, s)
				continue
			}
			creates = append(creates, len(next))
			next = append(next, &section{lang: d.Language, rng: d.Range})
		}
	}
	var closes []*section
	for i, s := range prev {
		if !used[i] {
			closes = append(closes, s)
		}
	}
	c.mu.Unlock()

	created := make([]*section, 0, len(creates))
	for _, i := range creates {
		next[i] = c.newSection(next[i].lang, next[i].rng)
		created = append(created, next[i])
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		for _, s := range created {
			_ = s.tree.Close()
		}
		return
	}
	c.sections = next
	// A closed section's highlights leave the merged list.
	if len(created) > 0 || len(closes) > 0 {
		c.publishLocked()
	}
	c.mu.Unlock()

	for _, r := range reloads {
		r.s.buf.ReloadContents(c.ctx, r.text)
	}
	for _, s := range closes {
		if err := s.tree.Close(); err != nil {
			c.log(log.WarnLevel, "close %s section at %v: %v", s.lang, s.rng, err)
		}
	}
}

// matchSection returns the index of the unused section of prev that d
// continues: the one covering exactly d's region, else one of d's language
// overlapping it. It returns -1 when there is none.
func matchSection(prev []*section, used []bool, d treesitter.Injection) int {
	for j, s := range prev {
		if !used[j] && s.lang == d.Language && !s.dirty && s.rng == d.Range {
			return j
		}
	}
	for j, s := range prev {
		if !used[j] && s.lang == d.Language && rangesOverlap(s.rng, d.Range) {
			return j
		}
	}
	return -1
}

// newSection creates the section of rng, with its tree parsing a copy of
// rng's text as a file of lang next to the host's. mu must not be held.
func (c *Tree) newSection(lang string, rng term.Range) *section {
	s := &section{lang: lang, rng: rng, buf: cell.NewBuffer()}
	if _, err := s.buf.ReadFrom(strings.NewReader(c.text(rng))); err != nil {
		c.log(log.WarnLevel, "fill %s section at %v: %v", lang, rng, err)
	}
	uri, err := workspaceapi.ParseURI(
		c.uri.String() + languages.ExtensionForLanguageWithFallback(lang))
	if err != nil {
		c.log(log.WarnLevel, "section uri for %s: %v", lang, err)
		uri = c.uri
	}
	loc := syntax.FuncLocationSetter(func(ll textapi.LocationList) {
		c.setSectionHighlights(s, ll)
	})
	s.tree = c.ctors.section(c.ctx, notifier{Notifications: c.n, c: c, lang: lang},
		c.interrupter, c.pkg, loc, uri, s.buf, nopFlusherCloser{}, c.opener, c.config)
	c.log(log.DebugLevel, "created %s section at %v", lang, rng)
	return s
}

// text returns the host's text in rng.
func (c *Tree) text(rng term.Range) string {
	cells, _, ok := c.buf.Select(rng.Start, rng.End)
	if !ok {
		return ""
	}
	return term.CellsToString(cells)
}

func (c *Tree) setHostHighlights(ll textapi.LocationList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hostHL = appendLocations(c.hostHL, ll)
	c.publishLocked()
}

func (c *Tree) setSectionHighlights(s *section, ll textapi.LocationList) {
	c.mu.Lock()
	s.hl = appendLocations(s.hl, ll)
	schedule := false
	if !c.closed && c.loc != nil {
		c.pending = true
		schedule = !c.publishing && !c.publishScheduled
		c.publishScheduled = c.publishScheduled || schedule
	}
	c.mu.Unlock()
	if schedule && !c.config.ScheduleNextTick(c.publishPending) {
		c.publishPending()
	}
}

// publishPending hands loc the merged list if a tree published since it
// last did.
func (c *Tree) publishPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publishScheduled = false
	if c.pending {
		c.publishLocked()
	}
}

// publishLocked hands loc the host's highlights followed by every section's
// in host coordinates, so a section's attributes are layered over the
// host's. During an edit it waits for the edit to end. mu must be held.
func (c *Tree) publishLocked() {
	if c.closed || c.loc == nil {
		return
	}
	if c.publishing {
		c.pending = true
		return
	}
	c.pending = false
	clear(c.merged)
	merged := append(c.merged[:0], c.hostHL...)
	for _, s := range c.sections {
		for _, l := range s.hl {
			l.From, l.To = s.toHost(l.From), s.toHost(l.To)
			merged = append(merged, l)
		}
	}
	c.loc.SetLocationList(textapi.LocationSlice(merged))
	c.merged, c.mergedSpare = c.mergedSpare, merged
}

// appendLocations replaces dst's contents with ll's.
func appendLocations(dst []textapi.Location, ll textapi.LocationList) []textapi.Location {
	clear(dst)
	dst = dst[:0]
	if ll == nil {
		return dst
	}
	for ok := true; ok; _, ok = ll.Prev() {
	}
	for l, ok := ll.Current(); ok; l, ok = ll.Next() {
		dst = append(dst, l)
	}
	return dst
}

// IndentationAt returns the indentation of line as nvim-treesitter computes
// it. A line from a section's first token on, up to the section's end, gets
// the section's own indentation on top of the indentation of the line of
// that first token, without consulting the host. Lines before the first
// token, like the one opened right after <script>, blank sections and
// sections whose tree cannot indent, such as a comment's, one whose grammar
// ships no indents query or one whose package was declined, are the host's,
// as are lines outside any section.
func (c *Tree) IndentationAt(line int) (int, bool) {
	c.mu.Lock()
	var inside *section
	var base, startY int
	for _, s := range c.sections {
		if line <= s.rng.Start.Y || line >= s.rng.End.Y {
			continue
		}
		cells := c.view.RawCells()
		first, ok := firstNonBlankRow(cells, s.rng)
		if !ok || line < first {
			continue
		}
		inside = s
		startY = s.rng.Start.Y
		base = leadingTabs(cells[first])
		break
	}
	c.mu.Unlock()

	if inside != nil {
		if capable, ok := inside.tree.(indentCapable); !ok || !capable.CanIndent() {
			inside = nil
		}
	}
	if inside == nil {
		if svc, ok := c.host.(indentService); ok {
			return svc.IndentationAt(line)
		}
		return 0, false
	}
	svc, ok := inside.tree.(indentService)
	if !ok {
		return 0, false
	}
	local, ok := svc.IndentationAt(line - startY)
	if !ok {
		return 0, false
	}
	return base + local, true
}

// firstNonBlankRow returns the first row of rng whose part inside rng is
// not blank, which holds the first token of the region's tree. ok is false
// when rng is blank.
func firstNonBlankRow(cells [][]term.Cell, rng term.Range) (int, bool) {
	for y := rng.Start.Y; y <= rng.End.Y && y < len(cells); y++ {
		row := cells[y]
		from, to := 0, len(row)
		if y == rng.Start.Y {
			from = min(rng.Start.X, to)
		}
		if y == rng.End.Y {
			to = max(from, min(rng.End.X, to))
		}
		if !isBlank(row[from:to]) {
			return y, true
		}
	}
	return 0, false
}

func leadingTabs(row []term.Cell) int {
	indent := 0
	for _, cl := range row {
		if cl.Ch != '\t' {
			break
		}
		indent++
	}
	return indent
}

func isBlank(cells []term.Cell) bool {
	for _, cl := range cells {
		if cl.Ch != ' ' && cl.Ch != '\t' {
			return false
		}
	}
	return true
}

// SelectionExpand grows rng within the section holding it, and through the
// host once the section's root is reached.
func (c *Tree) SelectionExpand(rng term.Range) (term.Range, bool) {
	rng = sortRange(rng)
	if s, ok := c.sectionContaining(rng); ok {
		if svc, ok := s.tree.(selectionService); ok {
			if next, ok := svc.SelectionExpand(s.rangeToLocal(rng)); ok {
				return s.rangeToHost(next), true
			}
		}
	}
	if svc, ok := c.host.(selectionService); ok {
		return svc.SelectionExpand(rng)
	}
	return term.Range{}, false
}

// SelectionShrink shrinks rng within the section holding it and caret, else
// through the host.
func (c *Tree) SelectionShrink(rng term.Range, caret term.Coordinates) (term.Range, bool) {
	rng = sortRange(rng)
	if s, ok := c.sectionContaining(rng); ok && s.contains(term.Range{Start: caret, End: caret}) {
		if svc, ok := s.tree.(selectionService); ok {
			if next, ok := svc.SelectionShrink(s.rangeToLocal(rng), s.toLocal(caret)); ok {
				return s.rangeToHost(next), true
			}
			return term.Range{}, false
		}
	}
	if svc, ok := c.host.(selectionService); ok {
		return svc.SelectionShrink(rng, caret)
	}
	return term.Range{}, false
}

// CommentCoverage reports the comments covering rng in the section holding
// it, else in the host.
func (c *Tree) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	rng = sortRange(rng)
	if s, ok := c.sectionContaining(rng); ok {
		if svc, ok := s.tree.(commentService); ok {
			if ranges, ok := svc.CommentCoverage(s.rangeToLocal(rng)); ok {
				for i := range ranges {
					ranges[i] = s.rangeToHost(ranges[i])
				}
				return ranges, true
			}
		}
	}
	if svc, ok := c.host.(commentService); ok {
		return svc.CommentCoverage(rng)
	}
	return nil, false
}

func (c *Tree) sectionContaining(rng term.Range) (sectionSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sections {
		if s.contains(rng) {
			return s.snapshot(), true
		}
	}
	return sectionSnapshot{}, false
}

// Folds returns the folds of the host and of every section, in host
// coordinates.
func (c *Tree) Folds() (iterator.Iterator[term.Range], bool) {
	host, sections := c.snapshot()
	return aggregateFolds(host, sections, func(svc foldsService, s *sectionSnapshot) (
		iterator.Iterator[term.Range], bool,
	) {
		return svc.Folds()
	})
}

// FoldsFrom returns the folds of the host and of the sections ending after
// pos, in host coordinates.
func (c *Tree) FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool) {
	host, sections := c.snapshot()
	return aggregateFolds(host, sections, func(svc foldsService, s *sectionSnapshot) (
		iterator.Iterator[term.Range], bool,
	) {
		if s == nil {
			return svc.FoldsFrom(pos)
		}
		if !coordinatesLess(pos, s.rng.End) {
			return nil, false
		}
		return svc.FoldsFrom(s.toLocal(coordinatesMax(pos, s.rng.Start)))
	})
}

// InitialFolds returns the folds to hide when the file opens, from the host
// and from the sections of the host's first parse. Its iterator waits for
// those sections to exist, so it is drained off the event loop.
func (c *Tree) InitialFolds() (iterator.Iterator[term.Range], bool) {
	var inner iterator.Iterator[term.Range]
	var err error
	return iterator.FromFunc(func(ctx context.Context) (term.Range, bool, error) {
		if inner == nil {
			select {
			case <-c.sectionsReady:
			case <-ctx.Done():
				return term.Range{}, false, ctx.Err()
			}
			host, sections := c.snapshot()
			var ok bool
			inner, ok = aggregateFolds(host, sections, func(svc foldsService, s *sectionSnapshot) (
				iterator.Iterator[term.Range], bool,
			) {
				return svc.InitialFolds()
			})
			if !ok {
				inner = iterator.Empty[term.Range]()
			}
		}
		next, ok := inner.Next(ctx)
		if !ok {
			err = inner.Err()
		}
		return next, ok, err
	}, func() error {
		if inner != nil {
			return inner.Close()
		}
		return nil
	}), true
}

// snapshot returns the host and a copy of the sections taken under mu, for
// callers that go on to call into the trees, which must not hold mu.
func (c *Tree) snapshot() (syntax.Tree, []sectionSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil
	}
	sections := make([]sectionSnapshot, len(c.sections))
	for i, s := range c.sections {
		sections[i] = s.snapshot()
	}
	return c.host, sections
}

// aggregateFolds combines the fold iterators get returns for the host (s
// nil) and for each section, the latter translated to host coordinates. ok
// is false when no tree had folds to offer.
func aggregateFolds(
	host syntax.Tree, sections []sectionSnapshot,
	get func(svc foldsService, s *sectionSnapshot) (iterator.Iterator[term.Range], bool),
) (iterator.Iterator[term.Range], bool) {
	if host == nil {
		return nil, false
	}
	var its []iterator.Iterator[term.Range]
	if svc, ok := host.(foldsService); ok {
		if it, ok := get(svc, nil); ok {
			its = append(its, it)
		}
	}
	for i := range sections {
		s := &sections[i]
		svc, ok := s.tree.(foldsService)
		if !ok {
			continue
		}
		it, ok := get(svc, s)
		if !ok {
			continue
		}
		its = append(its, iterator.Map(it, s.rangeToHost))
	}
	if len(its) == 0 {
		return nil, false
	}
	return iterator.Aggregate(its...), true
}

func (c *Tree) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "syntax.multitree",
		"uri":            c.uri,
	}).Logf(level, msg, args...)
}

// notifier reports a language's missing grammar once per composite, since
// every section of that language would otherwise repeat it.
type notifier struct {
	browserapi.Notifications
	c    *Tree
	lang string
}

func (n notifier) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.c.mu.Lock()
	seen := n.c.notified[n.lang]
	n.c.notified[n.lang] = true
	n.c.mu.Unlock()
	if seen {
		return "", nil
	}
	return n.Notifications.Notify(level, msg, args...)
}

// nopFlusherCloser is the FlusherCloser of a section tree: its buffer is a
// copy, with no file of its own to flush or reload.
type nopFlusherCloser struct{}

func (nopFlusherCloser) Flush(context.Context) (<-chan error, error) {
	return doneErrChan(), nil
}

func (nopFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	return doneErrChan(), nil
}

func (nopFlusherCloser) Reload(context.Context) (<-chan error, error) {
	return doneErrChan(), nil
}

func (nopFlusherCloser) LastFlush() time.Time { return time.Time{} }

func (nopFlusherCloser) Close() error { return nil }

func doneErrChan() <-chan error {
	ch := make(chan error, 1)
	ch <- nil
	close(ch)
	return ch
}
