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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reports(s *Store, bodies ...string) {
	for _, body := range bodies {
		r, err := Parse([]byte(body))
		if err != nil {
			panic(fmt.Sprintf("bad test report %q: %v", body, err))
		}
		s.Apply(r)
	}
}

func ids(sum Summary) []string {
	out := make([]string, 0, len(sum.Records))
	for _, r := range sum.Records {
		out = append(out, r.ID)
	}
	return out
}

func TestStoreSummaryEmpty(t *testing.T) {
	t.Parallel()
	s := New()
	sum := s.Summary()
	assert.Equal(t, Summary{State: Idle, Progress: -1}, sum)
	assert.True(t, sum.Equal(Summary{Progress: -1}))
}

func TestStoreReplacesWholesale(t *testing.T) {
	t.Parallel()
	s := New()
	reports(s, "state=working:app=brew:title="+b64("Brew")+":msg="+b64("Installing"))
	reports(s, "state=done:msg="+b64("Upgraded"))
	sum := s.Summary()
	require.Len(t, sum.Records, 1)
	assert.Equal(t, Report{State: Done, Progress: -1, Msg: "Upgraded"}, sum.Records[0].Report)
	assert.Equal(t, Done, sum.State)
	assert.Equal(t, "", sum.App)
	assert.Equal(t, "", sum.Title)
}

func TestStoreSpecDeployWalkthrough(t *testing.T) {
	t.Parallel()
	s := New()
	reports(s,
		"state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
		"state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==",
	)
	sum := s.Summary()
	assert.Equal(t, Working, sum.State)
	assert.Equal(t, []string{"", "us-east"}, ids(sum))
	// most recent working record drives the roll-up
	assert.Equal(t, 40, sum.Progress)
	assert.Equal(t, "deploy", sum.App, "app inherited from root")
	assert.Equal(t, "US East", sum.Title)
	assert.Equal(t, "deploy", sum.Records[1].App)

	reports(s, "state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/")
	sum = s.Summary()
	assert.Equal(t, Blocked, sum.State, "blocked outranks working")
	assert.Equal(t, KindPermission, sum.Kind)
	assert.Equal(t, "deploy", sum.App)
	assert.Equal(t, "EU West", sum.Title)
	assert.Equal(t, "Approve deploy to eu-west (production)?", sum.Msg)
	assert.Equal(t, -1, sum.Progress)

	reports(s, "state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==")
	assert.Equal(t, Blocked, s.Summary().State, "blocked still outranks done")

	reports(s, "state=clear:id=us-east", "state=clear:id=eu-west")
	sum = s.Summary()
	assert.Equal(t, []string{""}, ids(sum))
	assert.Equal(t, Working, sum.State)

	reports(s, "state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z")
	sum = s.Summary()
	assert.Equal(t, Done, sum.State)
	assert.Equal(t, "Deployed to 3 regions", sum.Msg)
}

func TestStorePrecedence(t *testing.T) {
	t.Parallel()
	order := []State{Idle, Working, Done, Error, Blocked}
	for i, lower := range order {
		for _, higher := range order[i+1:] {
			t.Run(lower.String()+"<"+higher.String(), func(t *testing.T) {
				t.Parallel()
				s := New()
				s.Apply(Report{ID: "a", State: higher, Progress: -1, Msg: "hi"})
				s.Apply(Report{ID: "b", State: lower, Progress: -1, Msg: "lo"})
				sum := s.Summary()
				assert.Equal(t, higher, sum.State)
				assert.Equal(t, "hi", sum.Msg)
			})
		}
	}
}

func TestStoreTiesPreferMostRecent(t *testing.T) {
	t.Parallel()
	s := New()
	s.Apply(Report{ID: "b", State: Working, Progress: 10})
	s.Apply(Report{ID: "a", State: Working, Progress: 20})
	assert.Equal(t, 20, s.Summary().Progress)
	s.Apply(Report{ID: "b", State: Working, Progress: 30})
	assert.Equal(t, 30, s.Summary().Progress)
}

func TestStoreClearSubtree(t *testing.T) {
	t.Parallel()
	s := New()
	for _, id := range []string{"build", "build/test", "build/test/unit", "buildx", "other"} {
		s.Apply(Report{ID: id, State: Working, Progress: -1})
	}
	s.Apply(Report{State: Working, Progress: -1})
	s.Apply(Report{ID: "build", State: Clear})
	assert.Equal(t, []string{"", "buildx", "other"}, ids(s.Summary()))
	s.Apply(Report{ID: "missing", State: Clear})
	assert.Equal(t, []string{"", "buildx", "other"}, ids(s.Summary()))
	s.Apply(Report{State: Clear})
	assert.Empty(t, s.Summary().Records)
	assert.Equal(t, Idle, s.Summary().State)
}

func TestStoreAppInheritance(t *testing.T) {
	t.Parallel()
	s := New()
	s.Apply(Report{State: Idle, Progress: -1, App: "root"})
	s.Apply(Report{ID: "a", State: Working, Progress: -1})
	s.Apply(Report{ID: "a/b", State: Working, Progress: -1, App: "mid"})
	s.Apply(Report{ID: "a/b/c", State: Working, Progress: -1})
	s.Apply(Report{ID: "x/y/z", State: Working, Progress: -1})
	apps := map[string]string{}
	for _, r := range s.Summary().Records {
		apps[r.ID] = r.App
	}
	assert.Equal(t, map[string]string{
		"":      "root",
		"a":     "root",
		"a/b":   "mid",
		"a/b/c": "mid",
		"x/y/z": "root",
	}, apps)

	s.Apply(Report{State: Clear})
	s.Apply(Report{ID: "orphan/child", State: Working, Progress: -1})
	assert.Equal(t, "", s.Summary().App)
}

func TestStoreLifetimes(t *testing.T) {
	t.Parallel()
	seed := func() *Store {
		s := New()
		for st, id := range map[State]string{
			Idle: "idle", Working: "working", Blocked: "blocked", Done: "done", Error: "error",
		} {
			s.Apply(Report{ID: id, State: st, Progress: -1})
		}
		return s
	}
	t.Run("DropTransient", func(t *testing.T) {
		t.Parallel()
		s := seed()
		s.DropTransient()
		assert.Equal(t, []string{"done", "error"}, ids(s.Summary()))
		assert.Equal(t, Error, s.Summary().State)
	})
	t.Run("Ack", func(t *testing.T) {
		t.Parallel()
		s := seed()
		s.Ack()
		assert.Equal(t, []string{"blocked", "working"}, ids(s.Summary()))
		assert.Equal(t, Blocked, s.Summary().State)
	})
	t.Run("DropTransient then Ack empties", func(t *testing.T) {
		t.Parallel()
		s := seed()
		s.DropTransient()
		s.Ack()
		assert.Equal(t, Summary{Progress: -1}, s.Summary())
	})
}

func TestStoreLRUEviction(t *testing.T) {
	t.Parallel()
	s := New()
	for i := range MaxRecords {
		s.Apply(Report{ID: fmt.Sprintf("r%03d", i), State: Working, Progress: -1})
	}
	require.Len(t, s.Summary().Records, MaxRecords)

	// touching the oldest makes it the newest
	s.Apply(Report{ID: "r000", State: Done, Progress: -1})
	require.Len(t, s.Summary().Records, MaxRecords)

	s.Apply(Report{ID: "new1", State: Working, Progress: -1})
	sum := s.Summary()
	require.Len(t, sum.Records, MaxRecords)
	assert.NotContains(t, ids(sum), "r001", "least recently updated evicted")
	assert.Contains(t, ids(sum), "r000")
	assert.Contains(t, ids(sum), "new1")

	s.Apply(Report{ID: "new2", State: Working, Progress: -1})
	assert.NotContains(t, ids(s.Summary()), "r002")
	assert.Len(t, s.Summary().Records, MaxRecords)
}

func TestSummaryEqual(t *testing.T) {
	t.Parallel()
	a, b := New(), New()
	reports(a, "state=working:app=brew:msg="+b64("x"))
	reports(b, "state=working:app=brew:msg="+b64("x"))
	assert.True(t, a.Summary().Equal(b.Summary()))
	assert.True(t, b.Summary().Equal(a.Summary()))

	reports(b, "state=working:app=brew:msg="+b64("y"))
	assert.False(t, a.Summary().Equal(b.Summary()))

	reports(b, "state=working:app=brew:msg="+b64("x"), "state=idle:id=child")
	assert.False(t, a.Summary().Equal(b.Summary()), "record count differs")

	reports(a, "state=idle:id=child")
	assert.True(t, a.Summary().Equal(b.Summary()))
}

func TestSummaryOutranks(t *testing.T) {
	t.Parallel()
	summary := func(bodies ...string) Summary {
		s := New()
		reports(s, bodies...)
		return s.Summary()
	}
	silent := summary()
	idle := summary("state=idle")
	working := summary("state=working")
	blocked := summary("state=blocked")
	for _, tc := range []struct {
		desc string
		s, o Summary
		want bool
	}{
		{"higher precedence", blocked, working, true},
		{"lower precedence", working, blocked, false},
		{"same state", working, working, false},
		{"working over a reported idle", working, idle, true},
		{"a reported idle over silence", idle, silent, true},
		{"silence under a reported idle", silent, idle, false},
		{"a reported idle over another", idle, idle, false},
		{"silence over silence", silent, silent, false},
	} {
		assert.Equal(t, tc.want, tc.s.Outranks(tc.o), tc.desc)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	t.Parallel()
	s := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			s.Apply(Report{ID: fmt.Sprintf("r%d", i%10), State: State(i % 5), Progress: -1})
		}
	}()
	for range 200 {
		s.Summary()
		s.Ack()
		s.DropTransient()
	}
	<-done
}
