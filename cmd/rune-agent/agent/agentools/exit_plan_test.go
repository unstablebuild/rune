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

package agentools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

type mockOpener struct {
	opened []workspaceapi.URI
	err    error
}

func (m *mockOpener) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	m.opened = append(m.opened, uri)
	return nil, m.err
}

type sequencePrompter struct {
	responses []agent.PromptResponse
	errs      []error
	calls     []agent.PromptRequest
	callIndex int
	onPrompt  func(idx int, req agent.PromptRequest)
}

func (s *sequencePrompter) Prompt(_ context.Context, req agent.PromptRequest) (agent.PromptResponse, error) {
	idx := s.callIndex
	s.calls = append(s.calls, req)
	if s.onPrompt != nil {
		s.onPrompt(idx, req)
	}
	if idx < len(s.errs) && s.errs[idx] != nil {
		s.callIndex++
		return agent.PromptResponse{}, s.errs[idx]
	}
	if s.callIndex >= len(s.responses) {
		return agent.PromptResponse{}, errors.New("no more responses")
	}
	resp := s.responses[s.callIndex]
	s.callIndex++
	return resp, nil
}

func TestExitPlanTool(t *testing.T) {
	t.Run("approved plan writes file and returns success", func(t *testing.T) {
		dir := t.TempDir()
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{approveValue}},
		}}
		tool := NewExitPlan(dir, mp, localFS{root: dir}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "Add auth middleware",
			"plan": "## Plan\n\n1. Create middleware\n2. Add tests"
		}`)

		assert.False(t, result.IsError)
		assert.True(t, result.ClearContext, "approved plan must clear context")
		assert.Contains(t, result.Content, "Plan approved")
		assert.Contains(t, result.Content, "add-auth-middleware.md")
		assert.Contains(t, result.Content, "## Plan", "plan content must be in result for context carry-over")

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1)

		data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		require.NoError(t, err)
		assert.Equal(t, "## Plan\n\n1. Create middleware\n2. Add tests", string(data))
	})

	t.Run("feedback returns user feedback", func(t *testing.T) {
		dir := t.TempDir()
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{feedbackValue}},
		}}
		tool := NewExitPlan(dir, mp, localFS{root: dir}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "Fix bug",
			"plan": "the plan"
		}`)

		assert.False(t, result.IsError)
		assert.False(t, result.ClearContext, "feedback must not clear context")
		assert.Contains(t, result.Content, "feedback")

		// Plan file is still written even when feedback is given.
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 1)
	})

	t.Run("custom feedback value is returned", func(t *testing.T) {
		dir := t.TempDir()
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{feedbackValue}, TextInput: "Add error handling to step 3"},
		}}
		tool := NewExitPlan(dir, mp, localFS{root: dir}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "Fix bug",
			"plan": "the plan"
		}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "Add error handling to step 3")
	})

	t.Run("creates plans directory if missing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "plans")
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{approveValue}},
		}}
		tool := NewExitPlan(dir, mp, localFS{root: dir}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "Fix bug",
			"plan": "the plan"
		}`)

		assert.False(t, result.IsError)
		_, err := os.Stat(dir)
		assert.NoError(t, err)
	})

	t.Run("prompter error returns retry instruction", func(t *testing.T) {
		mp := &mockPrompter{err: errors.New("dismissed")}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "Fix bug",
			"plan": "the plan"
		}`)

		assert.False(t, result.IsError, "prompt dismiss should not be an error")
		assert.Contains(t, result.Content, "dismissed the prompt")
		assert.Contains(t, result.Content, "call exit_plan_mode again")
	})

	t.Run("prompt shows correct options", func(t *testing.T) {
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{approveValue}},
		}}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		tool.Execute(context.Background(), `{
			"title": "Test",
			"plan": "content"
		}`)

		require.Len(t, mp.calls, 1)
		assert.Equal(t, "Plan", mp.calls[0].Header)
		assert.Len(t, mp.calls[0].Options, 3)
		assert.Equal(t, "Approve", mp.calls[0].Options[0].Label)
		assert.Equal(t, "Give feedback", mp.calls[0].Options[1].Label)
		assert.Equal(t, "Edit plan", mp.calls[0].Options[2].Label)
	})

	t.Run("missing title returns error", func(t *testing.T) {
		mp := &mockPrompter{}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "something",
			"plan": ""
		}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "title and plan are required")
	})

	t.Run("missing plan returns error", func(t *testing.T) {
		mp := &mockPrompter{}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		result := tool.Execute(context.Background(), `{
			"title": "",
			"plan": "content"
		}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "title and plan are required")
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		mp := &mockPrompter{}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		result := tool.Execute(context.Background(), `bad json`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "invalid arguments")
	})

	t.Run("definition has correct name and schema", func(t *testing.T) {
		mp := &mockPrompter{}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		def := tool.Definition()

		assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
		assert.Equal(t, "exit_plan_mode", def.Function.Name)
		assert.NotEmpty(t, def.Function.Description)
	})

	t.Run("summary returns truncated title", func(t *testing.T) {
		mp := &mockPrompter{}
		tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
		assert.Equal(t, "Fix the bug", tool.Summary(`{"title":"Fix the bug","plan":"x"}`))
		assert.Equal(t, "", tool.Summary(`bad json`))
	})

	t.Run("edit plan then talk about changes with diff", func(t *testing.T) {
		dir := t.TempDir()
		tempPath := filepath.Join(dir, "temp-plan.md")
		originalPlan := "## Plan\n\n1. Initial step\n2. Next step\n"
		editedPlan := "## Plan\n\n1. Initial step\n2. Modified step\n3. Extra step\n"

		opener := &mockOpener{}
		sp := &sequencePrompter{
			responses: []agent.PromptResponse{
				{Values: []string{editValue}},
				{Values: []string{talkChangesValue}},
			},
			onPrompt: func(idx int, req agent.PromptRequest) {
				if idx == 1 {
					assert.Equal(t, "Plan opened in editor", req.Title)
					assert.Equal(t, "Edit plan", req.Header)
					require.Len(t, req.Options, 2)
					assert.Equal(t, "Talk about changes", req.Options[0].Label)
					assert.Equal(t, "Approve plan", req.Options[1].Label)
					require.NoError(t, os.WriteFile(tempPath, []byte(editedPlan), 0o644))
				}
			},
		}

		tool := NewExitPlan(dir, sp, localFS{root: dir}, opener)
		tool.GenerateTempPath = func(title string) string { return tempPath }

		input, err := json.Marshal(map[string]string{
			"title": "Add auth",
			"plan":  originalPlan,
		})
		require.NoError(t, err)
		result := tool.Execute(context.Background(), string(input))

		assert.False(t, result.IsError)
		assert.False(t, result.ClearContext)
		assert.Nil(t, result.ApprovedPlan)
		assert.Contains(t, result.Content, "User edited the plan:")
		assert.Contains(t, result.Content, "```diff")
		assert.Contains(t, result.Content, "-2. Next step")
		assert.Contains(t, result.Content, "+2. Modified step")
		assert.Contains(t, result.Content, "+3. Extra step")
		require.Len(t, opener.opened, 1)
		expectedURI, err := (localFS{root: dir}).URI(tempPath)
		require.NoError(t, err)
		assert.Equal(t, expectedURI, opener.opened[0])
	})

	t.Run("edit plan then talk about changes with no changes", func(t *testing.T) {
		dir := t.TempDir()
		tempPath := filepath.Join(dir, "temp-plan.md")
		plan := "## Plan\n\n1. Single step\n"

		opener := &mockOpener{}
		sp := &sequencePrompter{
			responses: []agent.PromptResponse{
				{Values: []string{editValue}},
				{Values: []string{talkChangesValue}},
			},
		}

		tool := NewExitPlan(dir, sp, localFS{root: dir}, opener)
		tool.GenerateTempPath = func(title string) string { return tempPath }

		input, err := json.Marshal(map[string]string{
			"title": "Add auth",
			"plan":  plan,
		})
		require.NoError(t, err)
		result := tool.Execute(context.Background(), string(input))

		assert.False(t, result.IsError)
		assert.False(t, result.ClearContext)
		assert.Nil(t, result.ApprovedPlan)
		assert.Equal(t, "The user reviewed the plan in the editor without changes.", result.Content)
	})

	t.Run("edit plan then approve plan with changes", func(t *testing.T) {
		dir := t.TempDir()
		tempPath := filepath.Join(dir, "temp-plan.md")
		originalPlan := "## Plan\n\n1. Step A\n2. Step B\n"
		editedPlan := "## Plan\n\n1. Step A\n2. Step B modified\n"

		opener := &mockOpener{}
		sp := &sequencePrompter{
			responses: []agent.PromptResponse{
				{Values: []string{editValue}},
				{Values: []string{approvePlanValue}},
			},
			onPrompt: func(idx int, req agent.PromptRequest) {
				if idx == 1 {
					require.NoError(t, os.WriteFile(tempPath, []byte(editedPlan), 0o644))
				}
			},
		}

		tool := NewExitPlan(dir, sp, localFS{root: dir}, opener)
		tool.GenerateTempPath = func(title string) string { return tempPath }

		input, err := json.Marshal(map[string]string{
			"title": "Add auth",
			"plan":  originalPlan,
		})
		require.NoError(t, err)
		result := tool.Execute(context.Background(), string(input))

		assert.False(t, result.IsError)
		assert.True(t, result.ClearContext)
		require.NotNil(t, result.ApprovedPlan)
		assert.Equal(t, editedPlan, result.ApprovedPlan.Body)
		assert.Contains(t, result.Content, "Plan approved with changes")
		assert.Contains(t, result.Content, result.ApprovedPlan.Path)
		assert.Contains(t, result.Content, editedPlan)

		data, err := os.ReadFile(result.ApprovedPlan.Path)
		require.NoError(t, err)
		assert.Equal(t, editedPlan, string(data))
	})

	t.Run("edit plan then approve plan with no changes", func(t *testing.T) {
		dir := t.TempDir()
		tempPath := filepath.Join(dir, "temp-plan.md")
		plan := "## Plan\n\n1. Step A\n"

		opener := &mockOpener{}
		sp := &sequencePrompter{
			responses: []agent.PromptResponse{
				{Values: []string{editValue}},
				{Values: []string{approvePlanValue}},
			},
		}

		tool := NewExitPlan(dir, sp, localFS{root: dir}, opener)
		tool.GenerateTempPath = func(title string) string { return tempPath }

		input, err := json.Marshal(map[string]string{
			"title": "Add auth",
			"plan":  plan,
		})
		require.NoError(t, err)
		result := tool.Execute(context.Background(), string(input))

		assert.False(t, result.IsError)
		assert.True(t, result.ClearContext)
		require.NotNil(t, result.ApprovedPlan)
		assert.Equal(t, plan, result.ApprovedPlan.Body)
		assert.Contains(t, result.Content, "Plan approved. Saved to")

		data, err := os.ReadFile(result.ApprovedPlan.Path)
		require.NoError(t, err)
		assert.Equal(t, plan, string(data))
	})

	t.Run("edit plan then dismiss initial or second prompt", func(t *testing.T) {
		t.Run("dismiss initial prompt", func(t *testing.T) {
			mp := &mockPrompter{err: errors.New("dismissed")}
			tool := NewExitPlan(t.TempDir(), mp, localFS{}, &mockOpener{})
			result := tool.Execute(context.Background(), `{
				"title": "Fix bug",
				"plan": "the plan"
			}`)

			assert.False(t, result.IsError)
			assert.False(t, result.ClearContext)
			assert.Contains(t, result.Content, "dismissed the prompt")
			assert.Contains(t, result.Content, "call exit_plan_mode again")
		})

		t.Run("dismiss second prompt", func(t *testing.T) {
			dir := t.TempDir()
			tempPath := filepath.Join(dir, "temp-plan.md")
			opener := &mockOpener{}
			sp := &sequencePrompter{
				responses: []agent.PromptResponse{
					{Values: []string{editValue}},
				},
				errs: []error{nil, errors.New("dismissed")},
			}
			tool := NewExitPlan(dir, sp, localFS{root: dir}, opener)
			tool.GenerateTempPath = func(title string) string { return tempPath }

			result := tool.Execute(context.Background(), `{
				"title": "Fix bug",
				"plan": "the plan"
			}`)

			assert.False(t, result.IsError)
			assert.False(t, result.ClearContext)
			assert.Contains(t, result.Content, "dismissed the prompt")
			assert.Contains(t, result.Content, "call exit_plan_mode again")
		})
	})

	t.Run("edit plan with nil fs or opener", func(t *testing.T) {
		mp := &mockPrompter{responses: []agent.PromptResponse{
			{Values: []string{editValue}},
		}}
		tool := NewExitPlan(t.TempDir(), mp, nil, nil)
		result := tool.Execute(context.Background(), `{
			"title": "Fix bug",
			"plan": "the plan"
		}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "editing plan is not supported: filesystem or opener not available")
	})
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Add auth middleware", "add-auth-middleware"},
		{"Fix Bug #123", "fix-bug-123"},
		{"  spaces  and--dashes  ", "spaces-and-dashes"},
		{"UPPER_CASE", "upper-case"},
		{"", "plan"},
		{"a" + string(make([]byte, 100)), "a"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
