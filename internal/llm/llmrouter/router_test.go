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

package llmrouter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/bedrock"
	"unstable.build/rune/internal/llm/gemini"
)

func TestMain(m *testing.M) {
	absent := filepath.Join(os.TempDir(), "rune-llmrouter-absent-aws-config")
	for k, v := range map[string]string{
		"AWS_REGION":                  "",
		"AWS_DEFAULT_REGION":          "",
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             absent,
		"AWS_SHARED_CREDENTIALS_FILE": absent,
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		if v == "" {
			_ = os.Unsetenv(k)
			continue
		}
		_ = os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

// newTestRouter constructs a router with sensible defaults rooted in
// a fresh temp directory.
func newTestRouter(t *testing.T) *Router {
	t.Helper()
	cfg := llm.DefaultConfig()
	// Pin the Gemini client at a closed loopback endpoint with a dummy key so
	// the live model listing fails instantly without reaching Google, keeping
	// catalog tests hermetic regardless of GOOGLE_API_KEY/GEMINI_API_KEY in
	// the environment.
	cfg.Gemini.APIKey = "test-key"
	cfg.Gemini.BaseURL = "http://127.0.0.1:1"
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)
	return r
}

// newTestRouterWithLocal is newTestRouter but also returns the injected
// local backend so tests can assert delegation and Close behaviour.
func newTestRouterWithLocal(t *testing.T) (*Router, *fakeLocalService) {
	t.Helper()
	cfg := llm.DefaultConfig()
	cfg.Gemini.APIKey = "test-key"
	cfg.Gemini.BaseURL = "http://127.0.0.1:1"
	local := &fakeLocalService{}
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), local)
	require.NoError(t, err)
	return r, local
}

func TestNew_ConstructsLocalRegistry(t *testing.T) {
	r := newTestRouter(t)
	require.NotNil(t, r.LocalRegistry())
}

func TestRouter_UnknownProvider(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.CreateCompletion(context.Background(),
		llmapi.ModelEntry{Name: "foo", Provider: "unknown"}, llmapi.Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no provider registered for "unknown"`)

	_, err = r.CountTokens(llmapi.ModelEntry{Provider: "unknown"}, nil)
	require.Error(t, err)
}

func TestRouter_CustomDisabledWithoutURL(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.CreateCompletion(context.Background(),
		llmapi.ModelEntry{Name: "x", Provider: ProviderCustom}, llmapi.Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "custom provider not configured")
}

func TestRouter_CustomCatalogSurfacesEntries(t *testing.T) {
	cfg := llm.DefaultConfig()
	cfg.Custom = llm.CustomConfig{
		URL:    "https://example.test/v1",
		APIKey: "k",
		AvailableModels: map[string]int{
			"my-model": 8192,
		},
	}
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)

	got, err := r.GetModel(context.Background(),
		llmapi.ModelEntry{Provider: ProviderCustom, Name: "my-model"})
	require.NoError(t, err)
	assert.Equal(t, 8192, got.ContextWindow)
	assert.Equal(t, ProviderCustom, got.Provider)
}

func TestRouter_StaticCatalog(t *testing.T) {
	r := newTestRouter(t)
	seen := map[string]bool{}
	ctx := context.Background()
	it := r.Models()
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			break
		}
		seen[entry.Provider] = true
	}
	// At least one entry per static provider should be present. Tracking
	// a provider set (not a name->provider map) keeps the assertion valid
	// even though claude and anthropic share identical model names.
	hasProvider := func(p string) bool { return seen[p] }
	assert.True(t, hasProvider(ProviderOpenAI), "no openai models")
	assert.True(t, hasProvider(ProviderAnthropic), "no anthropic models")
	assert.True(t, hasProvider(ProviderCodex), "no codex models")
	assert.True(t, hasProvider(ProviderClaude), "no claude models")
	assert.True(t, hasProvider(ProviderGemini), "no gemini models")
}

func TestRouter_Models_IncludesBedrock(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	it := r.Models()
	defer func() { _ = it.Close() }()

	var hasBedrock bool
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			break
		}
		if entry.Provider == ProviderBedrock {
			hasBedrock = true
		}
	}
	assert.True(t, hasBedrock, "bedrock static catalog should be present without a key")
}

func TestRouter_ResolveBedrock_EmptyKeyUsesCredentialChain(t *testing.T) {
	ctx := context.Background()
	cfg := llm.DefaultConfig()
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)

	svc, err := r.resolveBedrock(ctx)
	require.NoError(t, err)
	require.NotNil(t, svc)

	r.mu.Lock()
	_, cached := r.hostedClients[hostedCacheKey{provider: ProviderBedrock, apiKey: ""}]
	r.mu.Unlock()
	assert.True(t, cached, "the chain-authenticated client is cached under the empty key")
}

func TestRouter_ResolveBedrock_StoredKeyGetsOwnClient(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)

	chainSvc, err := r.resolveBedrock(ctx)
	require.NoError(t, err)

	require.NoError(t, r.AddProviderKey(ctx, ProviderBedrock, "work", "bedrock-key", "us-east-1"))
	keySvc, err := r.resolveBedrock(ctx)
	require.NoError(t, err)
	assert.NotSame(t, chainSvc, keySvc)

	again, err := r.resolveBedrock(ctx)
	require.NoError(t, err)
	assert.Same(t, keySvc, again, "repeated resolves must return the cached client")
}

func TestRouter_Resolve_Bedrock(t *testing.T) {
	r := newTestRouter(t)
	svc, err := r.resolve(context.Background(),
		llmapi.ModelEntry{Provider: ProviderBedrock, Name: bedrock.FlagshipModel()})
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestRouter_BuildHostedClient_BedrockVerifiesRegionalFlagship(t *testing.T) {
	r := newTestRouter(t)

	svc, model, err := r.buildHostedClient(ProviderBedrock, "bedrock-key", "us-east-1")
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Equal(t, ProviderBedrock, model.Provider)
	assert.Equal(t, bedrock.FlagshipModel(), model.Name)

	_, model, err = r.buildHostedClient(ProviderBedrock, "bedrock-key", "eu-west-1")
	require.NoError(t, err)
	assert.Equal(t, bedrock.VerificationModelForRegion("eu-west-1"), model.Name)
}

func TestRouter_ResolveBedrock_RegionChangeGetsOwnClient(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)

	require.NoError(t, r.AddProviderKey(ctx, ProviderBedrock, "work", "bedrock-key", "us-east-1"))
	usSvc, err := r.resolveBedrock(ctx)
	require.NoError(t, err)

	require.NoError(t, r.AddProviderKey(ctx, ProviderBedrock, "work", "bedrock-key", "eu-west-1"))
	euSvc, err := r.resolveBedrock(ctx)
	require.NoError(t, err)
	assert.NotSame(t, usSvc, euSvc)
}

func TestRouter_Models_GeminiErrorDoesNotTruncate(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	it := r.Models()
	defer func() { _ = it.Close() }()

	providers := map[string]bool{}
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			break
		}
		providers[entry.Provider] = true
	}
	assert.True(t, providers[ProviderOpenAI], "openai dropped")
	assert.True(t, providers[ProviderAnthropic], "anthropic dropped")
	assert.True(t, providers[ProviderCodex], "codex dropped")
}

func TestRouter_Models_GeminiStaticWithoutKey(t *testing.T) {
	cfg := llm.DefaultConfig()
	cfg.Gemini.APIKey = ""
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)

	ctx := context.Background()
	it := r.Models()
	defer func() { _ = it.Close() }()

	var hasGemini bool
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			break
		}
		if entry.Provider == ProviderGemini {
			hasGemini = true
		}
	}
	assert.True(t, hasGemini, "gemini static catalog should be present without a key")
}

func TestRouter_GetModel_RequiresProvider(t *testing.T) {
	r := newTestRouter(t)
	// gpt-5.5 collides across openai and codex catalogs.
	_, err := r.GetModel(context.Background(), llmapi.ModelEntry{Name: "gpt-5.5"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound, "GetModel must reject empty Provider")
}

func TestRouter_GetModel_QualifiedDisambiguates(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()

	got, err := r.GetModel(ctx, llmapi.ModelEntry{Provider: ProviderCodex, Name: "gpt-5.5"})
	require.NoError(t, err)
	assert.Equal(t, ProviderCodex, got.Provider)

	got, err = r.GetModel(ctx, llmapi.ModelEntry{Provider: ProviderOpenAI, Name: "gpt-5.5"})
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAI, got.Provider)
}

func TestRouter_GetModel_ClaudeDisambiguates(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()

	// claude-opus-4-8 collides across the anthropic and claude catalogs;
	// a bare-name lookup must fail.
	_, err := r.GetModel(ctx, llmapi.ModelEntry{Name: "claude-opus-4-8"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound, "bare-name claude-opus-4-8 must require a provider")

	got, err := r.GetModel(ctx, llmapi.ModelEntry{Provider: ProviderClaude, Name: "claude-opus-4-8"})
	require.NoError(t, err)
	assert.Equal(t, ProviderClaude, got.Provider)

	got, err = r.GetModel(ctx, llmapi.ModelEntry{Provider: ProviderAnthropic, Name: "claude-opus-4-8"})
	require.NoError(t, err)
	assert.Equal(t, ProviderAnthropic, got.Provider)
}

func TestRouter_ResolveClaude_NotLoggedIn(t *testing.T) {
	r, err := New(llm.DefaultConfig(), t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)
	_, err = r.resolveClaude(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no claude credential found")
	assert.Contains(t, err.Error(), "models providers claude login")
}

func TestRouter_Resolve_RejectsEmptyProvider(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.resolve(context.Background(), llmapi.ModelEntry{Name: "gpt-5.5"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ModelEntry.Provider must be set")
}

func TestRouter_ResolveHosted_StorageWinsOverConfig(t *testing.T) {
	ctx := context.Background()
	cfg := llm.DefaultConfig()
	cfg.OpenAI.APIKey = "config-key"
	storage := storagestub.NewInMemoryService()
	r, err := New(cfg, t.TempDir(), storage, &fakeLocalService{})
	require.NoError(t, err)
	require.NoError(t, r.AddProviderKey(ctx, ProviderOpenAI, "work", "storage-key", ""))

	svc, err := r.resolveOpenAI(ctx)
	require.NoError(t, err)
	require.NotNil(t, svc)

	r.mu.Lock()
	_, fromStorage := r.hostedClients[hostedCacheKey{provider: ProviderOpenAI, apiKey: "storage-key"}]
	_, fromConfig := r.hostedClients[hostedCacheKey{provider: ProviderOpenAI, apiKey: "config-key"}]
	r.mu.Unlock()
	assert.True(t, fromStorage, "client should be keyed by the storage key")
	assert.False(t, fromConfig, "config key must not be used when storage has a key")
}

func TestRouter_ResolveHosted_FallsBackToConfig(t *testing.T) {
	ctx := context.Background()
	cfg := llm.DefaultConfig()
	cfg.Anthropic.APIKey = "config-key"
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)

	svc, err := r.resolveAnthropic(ctx)
	require.NoError(t, err)
	require.NotNil(t, svc)

	svc2, err := r.resolveAnthropic(ctx)
	require.NoError(t, err)
	assert.Same(t, svc, svc2, "repeated resolves must return the cached client")
}

func TestRouter_ResolveHosted_ErrorWhenUnset(t *testing.T) {
	ctx := context.Background()
	cfg := llm.DefaultConfig()
	cfg.OpenAI.APIKey = ""
	cfg.Anthropic.APIKey = ""
	cfg.Gemini.APIKey = ""
	r, err := New(cfg, t.TempDir(), storagestub.NewInMemoryService(), &fakeLocalService{})
	require.NoError(t, err)

	_, err = r.resolveOpenAI(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openai api key not configured")

	_, err = r.resolveAnthropic(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic api key not configured")

	_, err = r.resolveGemini(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gemini api key not configured")
}

type verifyService struct {
	events []llmapi.Event
}

func (s *verifyService) CreateCompletion(
	_ context.Context, _ llmapi.ModelEntry, _ llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	return iterator.FromSlice(s.events), nil
}
func (s *verifyService) CountTokens(_ llmapi.ModelEntry, _ []llmapi.Message) (int, error) {
	return 0, nil
}
func (s *verifyService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice[llmapi.ModelEntry](nil)
}
func (s *verifyService) GetModel(_ context.Context, _ llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

func TestRouter_VerifyProviderKey_Success(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	r.newHostedClient = func(_, _, _ string) (llmapi.Service, llmapi.ModelEntry, error) {
		return &verifyService{events: []llmapi.Event{{Type: llmapi.EventTextDelta, Text: "ok"}}},
			llmapi.ModelEntry{Name: "m", Provider: ProviderOpenAI}, nil
	}
	require.NoError(t, r.VerifyProviderKey(ctx, ProviderOpenAI, "good-key", ""))
}

func TestRouter_VerifyProviderKey_AuthError(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	authErr := errors.New("invalid api key")
	r.newHostedClient = func(_, _, _ string) (llmapi.Service, llmapi.ModelEntry, error) {
		return &verifyService{events: []llmapi.Event{{Type: llmapi.EventStreamError, Error: authErr}}},
			llmapi.ModelEntry{Name: "m", Provider: ProviderOpenAI}, nil
	}
	err := r.VerifyProviderKey(ctx, ProviderOpenAI, "bad-key", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, authErr)
}

func TestRouter_Resolve_Gemini(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	svc, err := r.resolve(ctx,
		llmapi.ModelEntry{Provider: ProviderGemini, Name: gemini.Gemini_2_5_Pro})
	require.NoError(t, err)
	require.NotNil(t, svc)

	svc2, err := r.resolve(ctx,
		llmapi.ModelEntry{Provider: ProviderGemini, Name: gemini.Gemini_2_5_Pro})
	require.NoError(t, err)
	assert.Same(t, svc, svc2, "repeated resolves must return the cached client")
}

func TestRouter_CreateCompletion_RejectsEmptyProvider(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.CreateCompletion(context.Background(),
		llmapi.ModelEntry{Name: "gpt-5.5"}, llmapi.Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ModelEntry.Provider must be set")

	_, err = r.CountTokens(llmapi.ModelEntry{Name: "gpt-5.5"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ModelEntry.Provider must be set")
}

// fakeLocalService satisfies localService with no off-heap resources. It
// counts CreateCompletion dispatches and Close calls so tests can assert
// the router delegates ProviderLocal to the injected backend and closes it
// exactly once on teardown.
type fakeLocalService struct {
	dispatches atomic.Int32
	closes     atomic.Int32
}

func (f *fakeLocalService) CreateCompletion(
	_ context.Context, _ llmapi.ModelEntry, _ llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	f.dispatches.Add(1)
	return iterator.FromSlice[llmapi.Event](nil), nil
}
func (f *fakeLocalService) CountTokens(_ llmapi.ModelEntry, _ []llmapi.Message) (int, error) {
	return 0, nil
}
func (f *fakeLocalService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice[llmapi.ModelEntry](nil)
}
func (f *fakeLocalService) GetModel(_ context.Context, _ llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}
func (f *fakeLocalService) Close() error { f.closes.Add(1); return nil }

func localModel(name string, ctxWindow int) llmapi.ModelEntry {
	return llmapi.ModelEntry{
		Name:          name,
		Provider:      ProviderLocal,
		BaseURL:       "/models/" + name + ".gguf",
		ContextWindow: ctxWindow,
	}
}

func TestRouterResolveLocal_DelegatesToBackend(t *testing.T) {
	r, local := newTestRouterWithLocal(t)

	for range 10 {
		_, err := r.CreateCompletion(context.Background(), localModel("m1", 4096), llmapi.Request{})
		require.NoError(t, err)
	}
	_, err := r.CreateCompletion(context.Background(), localModel("m2", 8192), llmapi.Request{})
	require.NoError(t, err)
	assert.Equal(t, int32(11), local.dispatches.Load())
}

func TestRouterClose_ClosesLocalBackend(t *testing.T) {
	r, local := newTestRouterWithLocal(t)

	_, err := r.CreateCompletion(context.Background(), localModel("a", 4096), llmapi.Request{})
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, int32(1), local.closes.Load())
}

func TestRouterClose_Idempotent(t *testing.T) {
	r, local := newTestRouterWithLocal(t)

	require.NoError(t, r.Close())
	require.NoError(t, r.Close())
	assert.Equal(t, int32(1), local.closes.Load())
}

func TestRouterResolveLocal_RejectsAfterClose(t *testing.T) {
	r, _ := newTestRouterWithLocal(t)
	require.NoError(t, r.Close())

	_, err := r.CreateCompletion(context.Background(), localModel("m", 4096), llmapi.Request{})
	require.ErrorIs(t, err, ErrRouterClosed)

	_, err = r.CountTokens(localModel("m", 4096), nil)
	require.ErrorIs(t, err, ErrRouterClosed)
}

func TestRouterConcurrentDispatch(t *testing.T) {
	r, _ := newTestRouterWithLocal(t)

	models := []llmapi.ModelEntry{
		localModel("m1", 4096),
		localModel("m2", 8192),
		localModel("m3", 4096),
	}

	var wg sync.WaitGroup
	for i := range 32 {
		model := models[i%len(models)]
		wg.Go(func() {
			if it, err := r.CreateCompletion(context.Background(), model, llmapi.Request{}); err == nil {
				_ = it.Close()
			}
			_, _ = r.CountTokens(model, nil)
		})
	}
	wg.Go(func() {
		_ = r.Close()
	})
	wg.Wait()
}
