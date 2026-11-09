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

package programstatus

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// Record is what the terminal stores for one id: exactly the keys of
// the last report that used it.
type Record struct {
	Report
	Updated time.Time
	seq     uint64
}

// Summary is the roll-up of every record in a terminal. State is the
// highest-precedence state among the records (blocked > error > done >
// working > idle), and the other fields come from the most recently
// updated record in that state. Records lists every record sorted by
// id, with App inherited from the nearest ancestor that has one.
type Summary struct {
	State    State
	Kind     Kind
	Progress int
	App      string
	Title    string
	Msg      string
	Records  []Record
}

// Equal reports whether both summaries would render identically. Update
// times are not compared.
func (s Summary) Equal(o Summary) bool {
	if s.State != o.State || s.Kind != o.Kind || s.Progress != o.Progress ||
		s.App != o.App || s.Title != o.Title || s.Msg != o.Msg ||
		len(s.Records) != len(o.Records) {
		return false
	}
	for i := range s.Records {
		if s.Records[i].Report != o.Records[i].Report {
			return false
		}
	}
	return true
}

// Outranks reports whether s is more pressing than o in a roll-up: its
// state has a higher Precedence, or both are idle and only s was
// reported, since a program saying it is idle is waiting on the user
// while silence says nothing.
func (s Summary) Outranks(o Summary) bool {
	if p, q := Precedence(s.State), Precedence(o.State); p != q {
		return p > q
	}
	return s.State == Idle && len(s.Records) > 0 && len(o.Records) == 0
}

// Store keeps the status records of one terminal, keyed by id. Methods
// may be called concurrently. Use New to create one; the zero value is
// not usable.
type Store struct {
	mu      sync.Mutex
	records map[string]*Record
	seq     uint64
}

// New returns an empty Store.
func New() *Store {
	return &Store{records: make(map[string]*Record, 8)}
}

// Apply replaces the record addressed by r wholesale, or removes records
// when r.State is Clear: the addressed record and every record beneath
// it, or every record when r addresses the root. Creating a record past
// MaxRecords evicts the least recently updated one.
func (s *Store) Apply(r Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.State == Clear {
		if r.ID == "" {
			clear(s.records)
			return
		}
		s.dropIf(func(rec *Record) bool {
			return rec.ID == r.ID || strings.HasPrefix(rec.ID, r.ID+"/")
		})
		return
	}
	if _, exists := s.records[r.ID]; !exists && len(s.records) >= MaxRecords {
		s.evictOldest()
	}
	s.seq++
	s.records[r.ID] = &Record{Report: r, Updated: time.Now(), seq: s.seq}
}

// DropTransient removes the records whose lifetime ends with the
// process or the shell prompt: working, blocked, and idle. It reports
// whether any record was removed.
func (s *Store) DropTransient() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropIf(func(rec *Record) bool {
		return rec.State == Working || rec.State == Blocked || rec.State == Idle
	})
}

// Ack removes the records the user is done with once they have seen
// them: done, error, and idle. It reports whether any record was
// removed.
func (s *Store) Ack() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropIf(func(rec *Record) bool {
		return rec.State == Done || rec.State == Error || rec.State == Idle
	})
}

// Summary rolls the records up. An empty store summarizes as Idle with
// no records and Progress -1.
func (s *Store) Summary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := Summary{Progress: -1}
	if len(s.records) == 0 {
		return sum
	}
	sum.Records = make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		r := *rec
		if r.App == "" {
			r.App = s.inheritedApp(r.ID)
		}
		sum.Records = append(sum.Records, r)
	}
	slices.SortFunc(sum.Records, func(a, b Record) int {
		return strings.Compare(a.ID, b.ID)
	})
	var top *Record
	for i := range sum.Records {
		rec := &sum.Records[i]
		if top == nil || Precedence(rec.State) > Precedence(top.State) ||
			(Precedence(rec.State) == Precedence(top.State) && rec.seq > top.seq) {
			top = rec
		}
	}
	sum.State = top.State
	sum.Kind = top.Kind
	sum.Progress = top.Progress
	sum.App = top.App
	sum.Title = top.Title
	sum.Msg = top.Msg
	return sum
}

func (s *Store) inheritedApp(id string) string {
	for id != "" {
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			id = id[:i]
		} else {
			id = ""
		}
		if rec, ok := s.records[id]; ok && rec.App != "" {
			return rec.App
		}
	}
	return ""
}

func (s *Store) dropIf(match func(*Record) bool) bool {
	dropped := false
	for id, rec := range s.records {
		if match(rec) {
			delete(s.records, id)
			dropped = true
		}
	}
	return dropped
}

func (s *Store) evictOldest() {
	var oldest *Record
	for _, rec := range s.records {
		if oldest == nil || rec.seq < oldest.seq {
			oldest = rec
		}
	}
	delete(s.records, oldest.ID)
}

// Precedence ranks states for roll-ups: blocked > error > done >
// working > idle, with Clear ranking as idle.
func Precedence(st State) int {
	switch st {
	case Blocked:
		return 4
	case Error:
		return 3
	case Done:
		return 2
	case Working:
		return 1
	}
	return 0
}
