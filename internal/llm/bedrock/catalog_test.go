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

package bedrock

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

type fakeControlPlane struct {
	profiles    []bedrocktypes.InferenceProfileSummary
	profilesErr error
	models      []bedrocktypes.FoundationModelSummary
	modelsErr   error
}

func (f *fakeControlPlane) ListInferenceProfiles(
	context.Context, *bedrock.ListInferenceProfilesInput, ...func(*bedrock.Options),
) (*bedrock.ListInferenceProfilesOutput, error) {
	if f.profilesErr != nil {
		return nil, f.profilesErr
	}
	return &bedrock.ListInferenceProfilesOutput{InferenceProfileSummaries: f.profiles}, nil
}

func (f *fakeControlPlane) ListFoundationModels(
	context.Context, *bedrock.ListFoundationModelsInput, ...func(*bedrock.Options),
) (*bedrock.ListFoundationModelsOutput, error) {
	if f.modelsErr != nil {
		return nil, f.modelsErr
	}
	return &bedrock.ListFoundationModelsOutput{ModelSummaries: f.models}, nil
}

func profile(id, status string, modelARNs ...string) bedrocktypes.InferenceProfileSummary {
	models := make([]bedrocktypes.InferenceProfileModel, 0, len(modelARNs))
	for _, arn := range modelARNs {
		models = append(models, bedrocktypes.InferenceProfileModel{ModelArn: aws.String(arn)})
	}
	return bedrocktypes.InferenceProfileSummary{
		InferenceProfileId: aws.String(id),
		Status:             bedrocktypes.InferenceProfileStatus(status),
		Models:             models,
	}
}

func foundationModel(id string, streaming bool) bedrocktypes.FoundationModelSummary {
	return bedrocktypes.FoundationModelSummary{
		ModelId:                    aws.String(id),
		ResponseStreamingSupported: aws.Bool(streaming),
	}
}

func drainCatalog(t *testing.T, c *catalogIterator) []llmapi.ModelEntry {
	t.Helper()
	var out []llmapi.ModelEntry
	for {
		entry, ok := c.Next(t.Context())
		if !ok {
			return out
		}
		out = append(out, entry)
	}
}

func newCatalog(api controlPlaneAPI) *catalogIterator {
	return &catalogIterator{
		list: func(ctx context.Context) ([]llmapi.ModelEntry, error) {
			return listCatalog(ctx, api)
		},
		fallback: ModelEntries(),
	}
}

func TestListCatalog_PrefersInferenceProfiles(t *testing.T) {
	api := &fakeControlPlane{
		profiles: []bedrocktypes.InferenceProfileSummary{
			profile(ClaudeOpus45, string(bedrocktypes.InferenceProfileStatusActive),
				"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus-4-5-20251101-v1:0"),
			profile("us.legacy-v1:0", "INACTIVE"),
		},
		models: []bedrocktypes.FoundationModelSummary{
			// Already routed by the profile above, so it must not appear twice.
			foundationModel("anthropic.claude-opus-4-5-20251101-v1:0", true),
			foundationModel("mistral.mistral-large-2407-v1:0", true),
			foundationModel("amazon.titan-embed-text-v2:0", false),
		},
	}

	entries := drainCatalog(t, newCatalog(api))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
		assert.Equal(t, LLMProvider, e.Provider)
	}
	assert.Equal(t, []string{MistralLarge, ClaudeOpus45}, names)
}

func TestListCatalog_ResolvesContextWindowFromStaticCatalog(t *testing.T) {
	api := &fakeControlPlane{
		profiles: []bedrocktypes.InferenceProfileSummary{
			// Geo and global profiles of catalog models.
			profile("au.anthropic.claude-opus-5-5", string(bedrocktypes.InferenceProfileStatusActive)),
			profile("global.openai.gpt-6.2-astra", string(bedrocktypes.InferenceProfileStatusActive)),
		},
		models: []bedrocktypes.FoundationModelSummary{
			// Bare foundation-model ID of a catalog inference profile.
			foundationModel("amazon.nova-pro-v1:0", true),
			foundationModel("some.unknown-model-v1:0", true),
		},
	}

	entries := drainCatalog(t, newCatalog(api))
	require.Len(t, entries, 4)
	byName := map[string]int{}
	for _, e := range entries {
		byName[e.Name] = e.ContextWindow
	}
	assert.Equal(t, AvailableModels()[NovaPro], byName["amazon.nova-pro-v1:0"])
	assert.Equal(t, 1_000_000, byName["au.anthropic.claude-opus-5-5"])
	assert.Equal(t, 1_050_000, byName["global.openai.gpt-6.2-astra"])
	assert.Zero(t, byName["some.unknown-model-v1:0"])
}

func TestCatalog_FallsBackWhenControlPlaneDenied(t *testing.T) {
	denied := errors.New("AccessDeniedException")
	c := newCatalog(&fakeControlPlane{profilesErr: denied})

	entries := drainCatalog(t, c)
	assert.Len(t, entries, len(AvailableModels()))
	// The static catalog is a complete answer, so the failure must not
	// propagate as an iterator error and truncate an aggregate.
	assert.NoError(t, c.Err())
}

func TestCatalog_KeepsProfilesWhenFoundationModelsDenied(t *testing.T) {
	c := newCatalog(&fakeControlPlane{
		profiles: []bedrocktypes.InferenceProfileSummary{
			profile(ClaudeOpus45, string(bedrocktypes.InferenceProfileStatusActive)),
		},
		modelsErr: errors.New("AccessDeniedException"),
	})

	entries := drainCatalog(t, c)
	require.Len(t, entries, 1)
	assert.Equal(t, ClaudeOpus45, entries[0].Name)
	assert.NoError(t, c.Err())
}

func TestCatalog_FallsBackOnEmptyLiveCatalog(t *testing.T) {
	c := newCatalog(&fakeControlPlane{})

	entries := drainCatalog(t, c)
	assert.Len(t, entries, len(AvailableModels()))
	assert.NoError(t, c.Err())
}
