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

package ide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	gmtext "github.com/yuin/goldmark/text"
	"unstable.build/rune/internal/ide/ideauthorizer"
)

func TestWorkspaceTrust(t *testing.T) {
	const (
		original = "editor:\n  tabspaces: 2\n"
		changed  = "editor:\n  tabspaces: 8\n"
	)
	type answer int
	const (
		// answerNone fails the test if the prompt is shown.
		answerNone answer = iota
		answerTrust
		answerOnce
		answerIgnore
		answerDismiss
		// answerCancel closes the pending workspace while the prompt is open.
		answerCancel
		// answerFail is a prompt that could not be shown.
		answerFail
	)
	type step struct {
		// restart starts a new Rune session over the same storage.
		restart  bool
		other    bool
		contents string
		answer   answer
		want     bool
		wantErr  bool
	}
	tests := []struct {
		name    string
		builtin []string
		steps   []step
	}{
		{
			name: "trust persists across sessions",
			steps: []step{
				{answer: answerTrust, want: true},
				{want: true},
				{restart: true, want: true},
			},
		},
		{
			name: "a changed file asks again",
			steps: []step{
				{answer: answerTrust, want: true},
				{contents: changed, answer: answerIgnore},
				{contents: changed},
				{restart: true, contents: changed, answer: answerOnce, want: true},
				{want: true},
			},
		},
		{
			name: "the same file in another workspace asks again",
			steps: []step{
				{answer: answerTrust, want: true},
				{other: true, answer: answerIgnore},
			},
		},
		{
			name: "trust once lasts the session",
			steps: []step{
				{answer: answerOnce, want: true},
				{want: true},
				{restart: true, answer: answerIgnore},
			},
		},
		{
			name: "ignore lasts the session",
			steps: []step{
				{answer: answerIgnore},
				{},
				{restart: true, answer: answerTrust, want: true},
			},
		},
		{
			name: "dismissing ignores",
			steps: []step{
				{answer: answerDismiss},
				{},
			},
		},
		{
			name: "a closed workspace decides nothing",
			steps: []step{
				{answer: answerCancel, wantErr: true},
				{answer: answerTrust, want: true},
			},
		},
		{
			name: "a prompt that cannot be shown decides nothing",
			steps: []step{
				{answer: answerFail, wantErr: true},
				{answer: answerTrust, want: true},
			},
		},
		{
			name:    "built-in configs are trusted by content",
			builtin: []string{original},
			steps: []step{
				{want: true},
				{other: true, want: true},
				{contents: changed, answer: answerIgnore},
			},
		},
	}
	repo, err := workspaceapi.ParseURI("file:///repo")
	require.NoError(t, err)
	other, err := workspaceapi.ParseURI("file:///other")
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := storagestub.NewInMemoryService()
			builtin := make(map[string]struct{})
			for _, b := range tt.builtin {
				builtin[workspaceConfigFingerprint([]byte(b))] = struct{}{}
			}
			var (
				current  step
				cancel   context.CancelFunc
				prompted bool
			)
			prompt := func(ctx context.Context, message string, options []string) (int, error) {
				prompted = true
				require.Len(t, options, 3)
				switch current.answer {
				case answerTrust:
					return workspaceTrustPersist, nil
				case answerOnce:
					return workspaceTrustOnce, nil
				case answerIgnore:
					return workspaceTrustIgnore, nil
				case answerDismiss:
					return -1, context.Canceled
				case answerCancel:
					cancel()
					return -1, ctx.Err()
				case answerFail:
					return -1, errors.New("event loop stopped")
				}
				t.Fatal("the prompt must not be shown")
				return -1, nil
			}
			trust := newWorkspaceTrust(storage, builtin, prompt)
			for i, s := range tt.steps {
				if s.restart {
					trust = newWorkspaceTrust(storage, builtin, prompt)
				}
				current, prompted = s, false
				uri := repo
				if s.other {
					uri = other
				}
				contents := original
				if s.contents != "" {
					contents = s.contents
				}
				var ctx context.Context
				ctx, cancel = context.WithCancel(context.Background())
				got, err := trust.trusted(ctx, uri, ".rune/config.yaml", []byte(contents))
				cancel()
				if s.wantErr {
					require.Error(t, err, "step %d", i)
				} else {
					require.NoError(t, err, "step %d", i)
				}
				assert.Equal(t, s.want, got, "step %d", i)
				assert.Equal(t, s.answer != answerNone, prompted, "step %d prompted", i)
			}
		})
	}
}

// answeringWorkspaceTrustPrompt answers each prompt with the next of
// answers and counts the prompts shown.
type answeringWorkspaceTrustPrompt struct {
	answers []int
	shown   int
}

func (p *answeringWorkspaceTrustPrompt) prompt(context.Context, string, []string) (int, error) {
	p.shown++
	if len(p.answers) == 0 {
		return -1, errors.New("unexpected workspace trust prompt")
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer, nil
}

type failingSetStorage struct {
	storageapi.Service
}

func (failingSetStorage) Set(context.Context, string, any) error {
	return errors.New("disk full")
}

func TestWorkspaceTrustDecisions(t *testing.T) {
	ctx := context.Background()
	repo, err := workspaceapi.ParseURI("file:///repo")
	require.NoError(t, err)
	other, err := workspaceapi.ParseURI("file:///other")
	require.NoError(t, err)
	contentsA, contentsB := []byte("a: 1\n"), []byte("b: 2\n")
	contents := map[string][]byte{
		workspaceConfigFingerprint(contentsA): contentsA,
		workspaceConfigFingerprint(contentsB): contentsB,
	}
	trustedA := ideauthorizer.WorkspaceConfigDecision{
		Workspace: repo, Fingerprint: workspaceConfigFingerprint(contentsA),
		Trusted: true, Persisted: true,
	}
	ignoredA := ideauthorizer.WorkspaceConfigDecision{
		Workspace: other, Fingerprint: workspaceConfigFingerprint(contentsA),
	}
	onceB := ideauthorizer.WorkspaceConfigDecision{
		Workspace: repo, Fingerprint: workspaceConfigFingerprint(contentsB),
		Trusted: true,
	}
	// decide answers the prompts that make trustedA, ignoredA and onceB.
	decide := func(t *testing.T, trust *workspaceTrust, p *answeringWorkspaceTrustPrompt) {
		t.Helper()
		p.answers = []int{workspaceTrustPersist, workspaceTrustIgnore, workspaceTrustOnce}
		for _, d := range []ideauthorizer.WorkspaceConfigDecision{trustedA, ignoredA, onceB} {
			got, err := trust.trusted(ctx, d.Workspace, ".rune/config.yaml", contents[d.Fingerprint])
			require.NoError(t, err)
			require.Equal(t, d.Trusted, got)
		}
		require.Equal(t, 3, p.shown)
	}

	t.Run("lists every decision", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		p := new(answeringWorkspaceTrustPrompt)
		trust := newWorkspaceTrust(storage, nil, p.prompt)
		decide(t, trust, p)

		decisions, err := trust.WorkspaceConfigDecisions(ctx)
		require.NoError(t, err)
		assert.ElementsMatch(t, []ideauthorizer.WorkspaceConfigDecision{
			trustedA, ignoredA, onceB,
		}, decisions)

		decisions, err = newWorkspaceTrust(storage, nil, p.prompt).WorkspaceConfigDecisions(ctx)
		require.NoError(t, err)
		assert.Equal(t, []ideauthorizer.WorkspaceConfigDecision{trustedA}, decisions,
			"only persisted trust outlives the session")
	})

	revokes := []struct {
		name     string
		revoke   ideauthorizer.WorkspaceConfigDecision
		wantLeft []ideauthorizer.WorkspaceConfigDecision
	}{
		{name: "persisted trust", revoke: trustedA, wantLeft: []ideauthorizer.WorkspaceConfigDecision{ignoredA, onceB}},
		{name: "transient ignore", revoke: ignoredA, wantLeft: []ideauthorizer.WorkspaceConfigDecision{trustedA, onceB}},
		{name: "transient trust", revoke: onceB, wantLeft: []ideauthorizer.WorkspaceConfigDecision{trustedA, ignoredA}},
	}
	for _, tt := range revokes {
		t.Run("revoking "+tt.name+" asks again", func(t *testing.T) {
			p := new(answeringWorkspaceTrustPrompt)
			trust := newWorkspaceTrust(storagestub.NewInMemoryService(), nil, p.prompt)
			decide(t, trust, p)

			require.NoError(t, trust.RevokeWorkspaceConfigDecision(ctx, tt.revoke))
			decisions, err := trust.WorkspaceConfigDecisions(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantLeft, decisions)

			for _, d := range tt.wantLeft {
				got, err := trust.trusted(ctx, d.Workspace, ".rune/config.yaml", contents[d.Fingerprint])
				require.NoError(t, err)
				assert.Equal(t, d.Trusted, got)
			}
			require.Equal(t, 3, p.shown, "the decisions left must still hold")
			p.answers = []int{workspaceTrustIgnore}
			_, err = trust.trusted(ctx, tt.revoke.Workspace, ".rune/config.yaml",
				contents[tt.revoke.Fingerprint])
			require.NoError(t, err)
			assert.Equal(t, 4, p.shown, "the revoked decision must ask again")
		})
	}

	t.Run("revoking a replaced trust keeps the newer one", func(t *testing.T) {
		p := &answeringWorkspaceTrustPrompt{
			answers: []int{workspaceTrustPersist, workspaceTrustPersist},
		}
		trust := newWorkspaceTrust(storagestub.NewInMemoryService(), nil, p.prompt)
		_, err := trust.trusted(ctx, repo, ".rune/config.yaml", contentsA)
		require.NoError(t, err)
		stale, err := trust.WorkspaceConfigDecisions(ctx)
		require.NoError(t, err)
		require.Equal(t, []ideauthorizer.WorkspaceConfigDecision{trustedA}, stale)
		_, err = trust.trusted(ctx, repo, ".rune/config.yaml", contentsB)
		require.NoError(t, err)

		require.NoError(t, trust.RevokeWorkspaceConfigDecision(ctx, stale[0]))
		decisions, err := trust.WorkspaceConfigDecisions(ctx)
		require.NoError(t, err)
		assert.Equal(t, []ideauthorizer.WorkspaceConfigDecision{{
			Workspace: repo, Fingerprint: workspaceConfigFingerprint(contentsB),
			Trusted: true, Persisted: true,
		}}, decisions)
	})

	t.Run("trust that can't be saved lasts the session", func(t *testing.T) {
		p := &answeringWorkspaceTrustPrompt{answers: []int{workspaceTrustPersist}}
		trust := newWorkspaceTrust(
			failingSetStorage{Service: storagestub.NewInMemoryService()}, nil, p.prompt)
		for range 2 {
			got, err := trust.trusted(ctx, repo, ".rune/config.yaml", contentsA)
			require.NoError(t, err)
			assert.True(t, got)
		}
		assert.Equal(t, 1, p.shown)
		decisions, err := trust.WorkspaceConfigDecisions(ctx)
		require.NoError(t, err)
		assert.Equal(t, []ideauthorizer.WorkspaceConfigDecision{{
			Workspace: repo, Fingerprint: workspaceConfigFingerprint(contentsA), Trusted: true,
		}}, decisions)
	})
}

func TestWorkspaceTrustMessage(t *testing.T) {
	tests := []struct {
		name      string
		contents  string
		wantCode  string
		wantFence string
		wantLines string
	}{
		{
			name:      "plain",
			contents:  "a: 1\nb: 2\n",
			wantCode:  "a: 1\nb: 2\n",
			wantFence: "```",
			wantLines: "2 lines",
		},
		{
			name:      "no trailing newline",
			contents:  "a: 1",
			wantCode:  "a: 1\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "backtick runs lengthen the fence",
			contents:  "a: 1\n````\n# injected\n````\n",
			wantCode:  "a: 1\n````\n# injected\n````\n",
			wantFence: "`````",
			wantLines: "4 lines",
		},
		{
			name:      "CRLF is normalized",
			contents:  "a: 1\r\nb: 2\r\n",
			wantCode:  "a: 1\nb: 2\n",
			wantFence: "```",
			wantLines: "2 lines",
		},
		{
			name:      "a lone CR is visible",
			contents:  "a: 1\rb: 2\n",
			wantCode:  `a: 1\u{000D}b: 2` + "\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "control characters are visible",
			contents:  "a: \x1b[31mred\x00\n",
			wantCode:  `a: \u{001B}[31mred\u{0000}` + "\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "bidi and zero-width characters are visible",
			contents:  "a: \u202eevil\u200b\ufeff\n",
			wantCode:  `a: \u{202E}evil\u{200B}\u{FEFF}` + "\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "line and paragraph separators are visible",
			contents:  "a: 1\u2028b: 2\u2029\n",
			wantCode:  `a: 1\u{2028}b: 2\u{2029}` + "\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "invalid UTF-8 is visible",
			contents:  "a: \xff\n",
			wantCode:  `a: \x{FF}` + "\n",
			wantFence: "```",
			wantLines: "1 line",
		},
		{
			name:      "tabs are kept",
			contents:  "a:\t1\n",
			wantCode:  "a:\t1\n",
			wantFence: "```",
			wantLines: "1 line",
		},
	}
	const location = "file:///re`po/.rune/config.yaml"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := workspaceTrustMessage(location, []byte(tt.contents))
			src := []byte(msg)
			doc := goldmark.New().Parser().Parse(gmtext.NewReader(src))

			var codeBlocks, codeSpans []string
			headings := 0
			require.NoError(t, ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				switch n := n.(type) {
				case *ast.FencedCodeBlock:
					assert.Equal(t, "yaml", string(n.Language(src)))
					var b strings.Builder
					for i := 0; i < n.Lines().Len(); i++ {
						seg := n.Lines().At(i)
						b.Write(seg.Value(src))
					}
					codeBlocks = append(codeBlocks, b.String())
				case *ast.CodeSpan:
					var b strings.Builder
					for c := n.FirstChild(); c != nil; c = c.NextSibling() {
						if text, ok := c.(*ast.Text); ok {
							b.Write(text.Segment.Value(src))
						}
					}
					codeSpans = append(codeSpans, b.String())
				case *ast.Heading:
					headings++
				}
				return ast.WalkContinue, nil
			}))

			assert.Equal(t, []string{tt.wantCode}, codeBlocks,
				"the contents must render as exactly one code block")
			assert.Equal(t, 1, headings, "the contents must not inject markdown")
			assert.Contains(t, codeSpans, location)
			assert.Contains(t, msg, "\n"+tt.wantFence+"yaml\n")
			assert.Contains(t, msg, "("+tt.wantLines+")")
		})
	}
}
