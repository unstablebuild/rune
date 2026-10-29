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

package searchrpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi/searchrpc"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"unstable.build/rune/internal/debug"
)

// fakeSearcher answers both searches with its results, failing the call
// with callErr or the stream with iterErr. With block set, each search
// hands its context over and waits for it to end.
type fakeSearcher struct {
	regexp   []searchapi.RegexpResult
	semantic []searchapi.SemanticResult
	callErr  error
	iterErr  error
	block    chan context.Context

	gotRegexp   searchapi.RegexpRequest
	gotSemantic searchapi.SemanticRequest
}

func (f *fakeSearcher) RegexpSearch(
	ctx context.Context, req searchapi.RegexpRequest,
) (iterator.Iterator[searchapi.RegexpResult], error) {
	f.gotRegexp = req
	return answer(ctx, f, f.regexp)
}

func (f *fakeSearcher) SemanticSearch(
	ctx context.Context, req searchapi.SemanticRequest,
) (iterator.Iterator[searchapi.SemanticResult], error) {
	f.gotSemantic = req
	return answer(ctx, f, f.semantic)
}

func answer[T any](ctx context.Context, f *fakeSearcher, results []T) (iterator.Iterator[T], error) {
	if f.block != nil {
		f.block <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.callErr != nil {
		return nil, f.callErr
	}
	it := iterator.FromSlice(results)
	if f.iterErr == nil {
		return it, nil
	}
	return iterator.Aggregate(it, iterator.Error[T](f.iterErr)), nil
}

func serve(t *testing.T, s searchapi.Searcher) *searchrpc.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterServer(srv, s)
	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return searchrpc.NewClient(cc)
}

func TestRegexpSearch(t *testing.T) {
	many := make([]searchapi.RegexpResult, 300)
	for i := range many {
		many[i] = searchapi.RegexpResult{
			Path: fmt.Sprintf("pkg/f%03d.go", i), Line: i + 1,
			Text: "func retry() {", Spans: [][2]int{{5, 10}},
		}
	}
	req := searchapi.RegexpRequest{
		Pattern: `retry\(`, IgnoreCase: true, Roots: []string{"pkg", "/w/cmd"},
		Include: []string{"*.go", "*.md"}, Limit: 50, MaxPerFile: 1,
	}
	tests := []struct {
		name    string
		s       *fakeSearcher
		want    []searchapi.RegexpResult
		wantErr error
	}{
		{name: "no results", s: &fakeSearcher{}, want: []searchapi.RegexpResult{}},
		{
			name: "lines with and without spans",
			s: &fakeSearcher{regexp: []searchapi.RegexpResult{
				{Path: "a.go", Line: 3, Text: "retry(x) retry(y)", Spans: [][2]int{{0, 6}, {9, 15}}},
				{Path: "dir/b.go", Line: 7, Text: ""},
			}},
			want: []searchapi.RegexpResult{
				{Path: "a.go", Line: 3, Text: "retry(x) retry(y)", Spans: [][2]int{{0, 6}, {9, 15}}},
				{Path: "dir/b.go", Line: 7, Text: ""},
			},
		},
		{name: "more lines than one message holds", s: &fakeSearcher{regexp: many}, want: many},
		{
			name: "lines that are not UTF-8",
			s: &fakeSearcher{regexp: []searchapi.RegexpResult{
				{Path: "latin1/caf\xe9.txt", Line: 1, Text: "caf\xe9 retry(", Spans: [][2]int{{5, 11}}},
			}},
			want: []searchapi.RegexpResult{
				{Path: "latin1/caf\xe9.txt", Line: 1, Text: "caf\xe9 retry(", Spans: [][2]int{{5, 11}}},
			},
		},
		{
			name:    "unsupported when called",
			s:       &fakeSearcher{callErr: fmt.Errorf("no index: %w", searchapi.ErrUnsupported)},
			wantErr: searchapi.ErrUnsupported,
		},
		{
			name:    "unsupported while streaming",
			s:       &fakeSearcher{iterErr: fmt.Errorf("no index: %w", searchapi.ErrUnsupported)},
			wantErr: searchapi.ErrUnsupported,
		},
		{
			name:    "deadline",
			s:       &fakeSearcher{callErr: fmt.Errorf("walk: %w", context.DeadlineExceeded)},
			wantErr: context.DeadlineExceeded,
		},
		{name: "other failure", s: &fakeSearcher{callErr: errors.New("bad pattern")}, wantErr: errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, tc.s)
			got, err := search(t, func() (iterator.Iterator[searchapi.RegexpResult], error) {
				return c.RegexpSearch(t.Context(), req)
			})
			checkErr(t, tc.wantErr, err)
			if tc.wantErr == nil {
				assert.Equal(t, tc.want, got)
			}
			assert.Equal(t, req, tc.s.gotRegexp)
		})
	}
}

func TestSemanticSearch(t *testing.T) {
	results := []searchapi.SemanticResult{
		{
			Path: "internal/retry.go", Kind: "code", Score: 3.5, Z: 2.25,
			Cosine: 0.61, KeywordRank: 1, Segment: "lines", Loc: 10,
			LocEnd: 42, Focus: 12, Excerpt: "func «parseRetryBackoff»(s string)",
			Modified: 1700000000, Size: 2048, Text: "\tfunc parseRetryBackoff(s string) {",
		},
		{Path: "README.md", Kind: "doc", Score: 1, Segment: "name"},
	}
	req := searchapi.SemanticRequest{
		Query: "retry backoff", Model: llmapi.ModelEntry{Provider: "llamacpp", Name: "gemma"},
		Roots: []string{"internal"}, Limit: 5, Meaning: true, PrefixLast: true,
	}
	tests := []struct {
		name    string
		s       *fakeSearcher
		want    []searchapi.SemanticResult
		wantErr error
	}{
		{name: "results round-trip", s: &fakeSearcher{semantic: results}, want: results},
		{
			name:    "unsupported",
			s:       &fakeSearcher{callErr: fmt.Errorf("openai: %w", searchapi.ErrUnsupported)},
			wantErr: searchapi.ErrUnsupported,
		},
		{
			name:    "model not found",
			s:       &fakeSearcher{callErr: fmt.Errorf("gemma: %w", llmapi.ErrModelNotFound)},
			wantErr: llmapi.ErrModelNotFound,
		},
		{
			name:    "canceled while streaming",
			s:       &fakeSearcher{semantic: results, iterErr: fmt.Errorf("embed: %w", context.Canceled)},
			wantErr: context.Canceled,
		},
		{name: "other failure", s: &fakeSearcher{iterErr: errors.New("index broke")}, wantErr: errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, tc.s)
			got, err := search(t, func() (iterator.Iterator[searchapi.SemanticResult], error) {
				return c.SemanticSearch(t.Context(), req)
			})
			checkErr(t, tc.wantErr, err)
			if tc.wantErr == nil {
				assert.Equal(t, tc.want, got)
			}
			assert.Equal(t, req, tc.s.gotSemantic)
		})
	}
}

func TestCloseStopsTheSearch(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *searchrpc.Client) (func() error, error)
	}{
		{"regexp", func(ctx context.Context, c *searchrpc.Client) (func() error, error) {
			it, err := c.RegexpSearch(ctx, searchapi.RegexpRequest{Pattern: "x"})
			if err != nil {
				return nil, err
			}
			return it.Close, nil
		}},
		{"semantic", func(ctx context.Context, c *searchrpc.Client) (func() error, error) {
			it, err := c.SemanticSearch(ctx, searchapi.SemanticRequest{Query: "x"})
			if err != nil {
				return nil, err
			}
			return it.Close, nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeSearcher{block: make(chan context.Context, 1)}
			c := serve(t, s)
			closeIt, err := tc.call(t.Context(), c)
			require.NoError(t, err)
			srvCtx := <-s.block
			require.NoError(t, closeIt())
			<-srvCtx.Done()
		})
	}
}

func search[T any](t *testing.T, call func() (iterator.Iterator[T], error)) ([]T, error) {
	t.Helper()
	it, err := call()
	if err != nil {
		return nil, err
	}
	return iterator.ToSlice(t.Context(), it)
}

func checkErr(t *testing.T, want, got error) {
	t.Helper()
	switch want {
	case nil:
		require.NoError(t, got)
	case errAny:
		require.Error(t, got)
		for _, mapped := range []error{
			searchapi.ErrUnsupported, llmapi.ErrModelNotFound,
			context.Canceled, context.DeadlineExceeded,
		} {
			assert.NotErrorIs(t, got, mapped)
		}
	default:
		require.ErrorIs(t, got, want)
	}
}

var errAny = errors.New("any error")
