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

package dialoguetui

import (
	"context"
	"log/slog"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/search"
)

// ContextCompleter supplies the candidates offered when the user types
// '#' in the compose box, and turns an accepted candidate back into an
// Attachment.
type ContextCompleter interface {
	// Candidates streams icon-prefixed "<icon> <value>" entries.
	Candidates(ctx context.Context, query string) (iterator.Iterator[string], error)
	// Resolve strips the icon off a candidate produced by Candidates and
	// builds the attachment it stands for.
	Resolve(candidate string) (Attachment, bool)
}

// WithContextCompleter enables '#' attachment completion in the compose
// box.
func WithContextCompleter(c ContextCompleter) HandlerOption {
	return func(dh *dialogueHandler) { dh.completer = c }
}

// openCompletion opens the '#' completion band and streams the
// completer's candidates into it off the event loop.
func (s *dialogueHandler) openCompletion() {
	s.compQuery = s.compQuery[:0]
	s.refreshCompletion()
}

func (s *dialogueHandler) newCompletionList() *search.List {
	cfg := s.comp.cfg
	matched := cfg.CompletionMatchedTextAttr
	focus := cfg.CompletionFocusElementAttr
	element := cfg.CompletionElementAttr
	return search.NewList(search.ListConfig{
		Algo:             search.FuzzyMatch,
		Interrupter:      s.interrupter,
		BottomSearchBar:  true,
		SyncSearch:       s.compSyncSearch,
		MatchedTextAttr:  &matched,
		FocusElementAttr: &focus,
		ElementAttr:      &element,
	})
}

func (s *dialogueHandler) refreshCompletion() {
	generation := s.compLoadingGeneration.Add(1)
	s.retireCompletionStream()
	list := s.newCompletionList()
	s.compList = list
	s.comp.SetCompletion(list)
	query := string(s.compQuery)
	list.Buffer().Replace(query)
	iter, err := s.completer.Candidates(s.ctx, query)
	if err != nil {
		slog.Error("dialoguetui: context completion candidates",
			"struct", "dialogue.handler", "error", err)
		s.compList = list
		s.comp.setCompletionLoading(false)
		s.closeCompletion()
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.compCancel = cancel
	s.compDone = done
	ch := list.Push(ctx)
	s.comp.setCompletionLoading(true)
	go debug.CapturePanicReport(func() {
		defer close(done)
		defer func() {
			if s.compLoadingGeneration.Load() == generation {
				s.comp.setCompletionLoading(false)
			}
		}()
		streamCompletionCandidates(ctx, iter, ch)
		list.Wait()
	})
}

func (s *dialogueHandler) retireCompletionStream() {
	if s.compCancel == nil {
		return
	}
	list := s.compList
	cancel, done := s.compCancel, s.compDone
	s.compCancel = nil
	s.compDone = nil
	cancel()
	list.Cancel()
	go debug.CapturePanicReport(func() {
		<-done
		list.Wait()
		_ = list.Close()
	})
}

// streamCompletionCandidates pumps iter into the list's async push
// channel until the iterator is exhausted or ctx is cancelled. It owns
// closing both the channel and the iterator. It must not force a
// re-sort (search.List.Pause): the list already re-ranks periodically
// until the user moves the focus, and a forced sort would snap the
// user's selection back to the best match.
func streamCompletionCandidates(
	ctx context.Context, iter iterator.Iterator[string], ch chan<- []byte,
) {
	defer close(ch)
	defer func() { _ = iter.Close() }()
	for {
		v, ok := iter.Next(ctx)
		if !ok {
			return
		}
		select {
		case ch <- []byte(v):
		case <-ctx.Done():
			return
		}
	}
}

// stopCompletionStream cancels the feeder goroutine and waits for it to
// exit so it cannot push into a torn-down list or leak.
func (s *dialogueHandler) stopCompletionStream() {
	if s.compCancel == nil {
		return
	}
	s.compCancel()
	<-s.compDone
	s.compList.Wait()
	s.compCancel = nil
	s.compDone = nil
}

// acceptCompletion attaches the focused candidate and replaces the
// "#query" token that opened the band with the attachment's full label,
// linked to the chip that keeps holding its content.
func (s *dialogueHandler) acceptCompletion() {
	list := s.compList
	typed := len(s.compQuery)
	s.stopCompletionStream()
	list.Cancel()
	list.Wait()
	match, ok := list.Focus()
	s.closeCompletion()
	if !ok {
		return
	}
	a, ok := s.completer.Resolve(string(match.Data()))
	if !ok {
		return
	}
	a = s.comp.AddAttachment(a)
	s.comp.Input().ReplaceBeforeCursor(typed+1, a.Label, a.Key)
}

func (s *dialogueHandler) closeCompletion() {
	if s.compList == nil {
		return
	}
	s.stopCompletionStream()
	list := s.compList
	s.compList = nil
	s.compQuery = s.compQuery[:0]
	_ = list.Close()
	s.comp.ClearCompletion()
}

// atWordBoundary reports whether a '#' typed now starts a new token
// rather than continuing one, so "foo#bar" and markdown "a # b" do not
// open the band.
func (s *dialogueHandler) atWordBoundary() bool {
	return s.comp.Input().AtWordBoundary()
}

// handleCompletionKey routes a key event while the completion band is
// open. It reports whether the event was consumed.
func (s *dialogueHandler) handleCompletionKey(ev term.Event) bool {
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter, term.KeyTab:
			s.acceptCompletion()
			return true
		case term.KeyEsc:
			s.closeCompletion()
			return true
		case term.KeyArrowUp:
			s.compList.FocusUp()
			return true
		case term.KeyArrowDown:
			s.compList.FocusDown()
			return true
		case term.KeyBackspace:
			s.comp.Input().Handle(ev)
			if len(s.compQuery) == 0 {
				s.closeCompletion()
				return true
			}
			s.compQuery = s.compQuery[:len(s.compQuery)-1]
			s.refreshCompletion()
			return true
		case term.KeySpace:
			s.comp.Input().Handle(ev)
			s.closeCompletion()
			return true
		}
		if ev.Ch == 0 {
			return false
		}
		if ev.Ch == ' ' {
			s.comp.Input().Handle(ev)
			s.closeCompletion()
			return true
		}
		s.comp.Input().Handle(ev)
		s.compQuery = append(s.compQuery, ev.Ch)
		s.refreshCompletion()
		return true
	case term.ModCtrl:
		switch ev.Ch {
		case 'k', 'p':
			s.compList.FocusUp()
			return true
		case 'j', 'n':
			s.compList.FocusDown()
			return true
		case 'c', 'g':
			s.closeCompletion()
			return true
		}
	}
	return false
}
