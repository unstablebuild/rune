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

// Package llmrouter provides a Router that implements llmapi.Service by
// dispatching CreateCompletion / CountTokens calls to one of the
// known provider clients. The router owns every client it dispatches
// to; there is no external Register API. Callers configure the router
// once with an llm.Config and treat it as an opaque llmapi.Service.
package llmrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/bedrock"
	"unstable.build/rune/internal/llm/claude"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmsearch"
	"unstable.build/rune/internal/llm/ollama"
	"unstable.build/rune/internal/llm/openai"
)

// Provider identifiers as they appear on llmapi.ModelEntry.Provider.
// The router uses these to dispatch CreateCompletion / CountTokens.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderCodex     = "codex"
	ProviderClaude    = "claude"
	ProviderGemini    = "gemini"
	ProviderBedrock   = "bedrock"
	ProviderCustom    = "custom"
	ProviderOllama    = "ollama"
	ProviderLocal     = llamacpp.LLMProvider // "llamacpp"
)

// Router is the rune-side LLM dispatcher. It is constructed once from
// an llm.Config and then implements llmapi.Service for the lifetime of
// the IDE. The router owns one llmapi.Service per provider and the
// workspace search service, which answers SemanticSearch.
type Router struct {
	cfg llm.Config

	custom llmapi.Service
	ollama llmapi.Service

	// customCatalog is materialised once from cfg.Custom.AvailableModels
	// so Models()/GetModel() can return a stable slice on each call.
	customCatalog []llmapi.ModelEntry
	// ollamaRegistry is the dynamic Ollama-tag registry; its Models()
	// queries the local Ollama daemon on demand.
	ollamaRegistry *ollama.Registry

	// localRegistry is the llama.cpp on-disk model cache. Exposed via
	// LocalRegistry so the `models local` REPL command can manage it
	// without taking a separate dependency.
	localRegistry *llamacpp.Registry

	// localService is the backend that serves ProviderLocal completions
	// (the managed llama-server pool). It is supplied at construction and
	// never mutated; the backend itself decides whether the server binary
	// is installed and returns the install-instruction error when it is
	// not. Never nil.
	localService llmapi.Service

	// search answers SemanticSearch. Never nil.
	search *llmsearch.Service

	// mu guards closed. The host no longer serialises
	// Router calls on the event-loop locker: llmrpc.Server dispatches each
	// gRPC request on grpc-go's goroutine pool, so the Router must be
	// internally goroutine-safe for the I/O-bound provider paths.
	mu sync.Mutex
	// hostedClients caches the constructed openai/anthropic/gemini
	// clients keyed by the resolved API key. The key is resolved lazily
	// (storage first, then config) so a `providers <p> setup` takes
	// effect without restarting the IDE; a changed key produces a new
	// cache entry. Guarded by mu.
	hostedClients map[hostedCacheKey]llmapi.Service
	// newHostedClient builds a throwaway hosted client for a given
	// provider/key, used by VerifyProviderKey to test a key before
	// storing it. region carries the key's scope for region-bound
	// providers (Bedrock) and is empty otherwise. Tests swap in a fake to
	// avoid real network calls.
	newHostedClient func(provider, apiKey, region string) (llmapi.Service, llmapi.ModelEntry, error)
	// closed flips to true after Close so late dispatches that try to
	// resurrect a torn-down service are rejected instead.
	closed bool

	// storage is the rune-side persistent storage used by providers
	// that own their auth state (today: codex). The router never reads
	// from storage itself; it only forwards it to per-provider
	// credential loaders that resolve the access token lazily, on
	// the request that needs it.
	storage storageapi.Service

	// store persists named hosted-provider API keys (set via
	// `/models providers <p> add`). resolveAPIKey consults the active
	// key here before falling back to the static configured key.
	store *keyStore

	// aliasStore persists named model aliases (e.g. "default", "query")
	// that resolve to a "provider/name" target. resolveAlias consults it
	// when a bare-name model entry is dispatched.
	aliasStore *aliasStore
}

type hostedCacheKey struct {
	provider string
	apiKey   string
	// region distinguishes clients for region-scoped keys (Bedrock), so
	// re-adding the same key under a different region cannot serve stale
	// clients. Empty for region-less providers.
	region string
}

// ErrRouterClosed is returned by dispatches issued after Router.Close.
var ErrRouterClosed = errors.New("llmrouter: router closed")

var _ llmapi.Service = (*Router)(nil)

// New constructs a Router from cfg. registry is the llama.cpp model
// cache that lists the local models. storage is forwarded to providers
// that own their own auth state (currently codex); pass an in-memory
// stub in tests that don't exercise the codex path. local is the backend
// that serves ProviderLocal completions (the managed llama-server pool);
// the "server not installed" state is represented inside the backend,
// never by a nil dependency. search answers SemanticSearch, and is closed
// by Close. Every argument is required.
func New(
	cfg llm.Config, registry *llamacpp.Registry, storage storageapi.Service,
	local llmapi.Service, search *llmsearch.Service,
) *Router {
	if registry == nil {
		panic("llmrouter: New: registry must not be nil")
	}
	if storage == nil {
		panic("llmrouter: New: storage must not be nil")
	}
	if local == nil {
		panic("llmrouter: New: local backend must not be nil")
	}
	if search == nil {
		panic("llmrouter: New: search must not be nil")
	}
	r := &Router{
		cfg:           cfg,
		storage:       storage,
		localRegistry: registry,
		localService:  local,
		search:        search,
		hostedClients: make(map[hostedCacheKey]llmapi.Service),
	}
	r.store = newKeyStore(storage)
	r.aliasStore = newAliasStore(storage)
	r.newHostedClient = r.buildHostedClient

	// openai, anthropic, and gemini clients are constructed lazily by
	// resolveOpenAI / resolveAnthropic / resolveGemini so the API key
	// can be sourced from storage (set via `providers <p> setup`) and
	// take effect without restarting the IDE. Codex follows the same
	// per-request pattern (see resolveCodex). Custom uses the
	// OpenAI-compatible API with a static configured key.
	if cfg.Custom.URL != "" {
		r.custom = openai.NewClient(cfg.Custom.APIKey, cfg.CustomClientConfig())
		r.customCatalog = customModelEntries(cfg.Custom)
	}

	r.ollamaRegistry = ollama.NewRegistry("")
	r.ollama = openai.NewClient("ollama", cfg.OllamaClientConfig(""))

	return r
}

// LocalRegistry returns the llama.cpp on-disk model cache. Used by the
// `models local` REPL command and by callers that need to download or
// inspect cached models.
func (r *Router) LocalRegistry() *llamacpp.Registry { return r.localRegistry }

// SearchService returns the workspace search service, which opens the
// index of each workspace.
func (r *Router) SearchService() *llmsearch.Service { return r.search }

// Close releases what owns off-process resources: the search service
// (its indexes and embedding servers) and the local backend (the managed
// llama-server pool). The other clients hold only Go-side HTTP state.
// After Close the router rejects further dispatches with ErrRouterClosed.
// Close is idempotent.
func (r *Router) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	local := r.localService
	r.mu.Unlock()
	errs := []error{r.search.Close()}
	if c, ok := local.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

func (r *Router) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// CanGenerateImages reports the resolved provider's image capability.
func (r *Router) CanGenerateImages(ctx context.Context, model llmapi.ModelEntry) (bool, error) {
	svc, model, err := r.serviceForModel(ctx, model)
	if err != nil {
		return false, err
	}
	return svc.CanGenerateImages(ctx, model)
}

// GenerateImage uses the same model and alias routing as completions.
func (r *Router) GenerateImage(ctx context.Context, model llmapi.ModelEntry, request llmapi.ImageRequest) (llmapi.Image, error) {
	svc, model, err := r.serviceForModel(ctx, model)
	if err != nil {
		return llmapi.Image{}, err
	}
	return svc.GenerateImage(ctx, model, request)
}

// CreateCompletion implements llmapi.Service.
func (r *Router) CreateCompletion(
	ctx context.Context,
	model llmapi.ModelEntry,
	req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	svc, model, err := r.serviceForModel(ctx, model)
	if err != nil {
		return nil, err
	}
	return svc.CreateCompletion(ctx, model, req)
}

// CountTokens implements llmapi.Service.
func (r *Router) CountTokens(model llmapi.ModelEntry, messages []llmapi.Message) (int, error) {
	svc, model, err := r.serviceForModel(context.Background(), model)
	if err != nil {
		return 0, err
	}
	return svc.CountTokens(model, messages)
}

// SemanticSearch answers req from the index of the workspace at root. A
// req.Model without a Provider is an alias the router resolves, and the
// zero model is SearchAlias. Until the model behind an unset SearchAlias
// is downloaded, the index answers by words alone and tells the user how
// to download it, and the status says so. It returns
// searchapi.ErrUnsupported for a model that is not a local embedding
// model, and llmsearch.ErrNoIndex when root has no open index.
func (r *Router) SemanticSearch(
	ctx context.Context, root workspaceapi.URI, req searchapi.SemanticRequest,
) (iterator.Iterator[searchapi.SemanticResult], searchapi.SemanticStatus, error) {
	if r.isClosed() {
		return nil, searchapi.SemanticStatus{}, ErrRouterClosed
	}
	if req.Model == (llmapi.ModelEntry{}) {
		req.Model.Name = SearchAlias
	}
	resolved, ok, err := r.resolveAlias(ctx, req.Model)
	switch {
	case errors.Is(err, llmapi.ErrModelNotFound) && req.Model.Name == SearchAlias:
		req.Model = r.search.ConfiguredModel()
	case err != nil:
		return nil, searchapi.SemanticStatus{}, err
	case ok:
		req.Model = resolved
	}
	return r.search.Search(ctx, root, req)
}

// Models implements llmapi.Service by concatenating every known
// provider's static catalog plus the dynamic gemini / bedrock / ollama /
// llamacpp registries. The order is deterministic: openai, anthropic,
// codex, custom, ollama, local, bedrock, gemini. Gemini is queried live
// and surfaces list errors through its iterator; it is placed last so a
// failed Gemini query (e.g. missing key) cannot truncate the preceding
// providers when iterator.Aggregate halts on error. Bedrock is also
// queried live but degrades to its static catalog without reporting an
// error, so it is safe to place ahead of Gemini.
func (r *Router) Models() iterator.Iterator[llmapi.ModelEntry] {
	its := []iterator.Iterator[llmapi.ModelEntry]{
		iterator.FromSlice(openai.ModelEntries()),
		iterator.FromSlice(anthropic.ModelEntries()),
		iterator.FromSlice(codex.ModelEntries()),
		iterator.FromSlice(claude.ModelEntries()),
	}
	if len(r.customCatalog) > 0 {
		its = append(its, iterator.FromSlice(r.customCatalog))
	}
	if r.ollamaRegistry != nil {
		its = append(its, r.ollamaRegistry.Models())
	}
	its = append(its, r.localRegistry.Models())
	its = append(its, r.bedrockCatalog(context.Background()))
	its = append(its, r.geminiCatalog(context.Background()))
	return iterator.Aggregate(its...)
}

// GetModel implements llmapi.Service. Bare-name lookups (empty
// Provider) resolve through the alias store when the name is a known
// alias; otherwise they return ErrModelNotFound so name collisions
// across providers cannot silently dispatch to the wrong backend.
func (r *Router) GetModel(ctx context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	if model.Provider == "" {
		if resolved, ok, err := r.resolveAlias(ctx, model); err != nil {
			return llmapi.ModelEntry{}, err
		} else if ok {
			return resolved, nil
		}
		return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
	}
	it := r.Models()
	defer func() { _ = it.Close() }()
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
		}
		if entry.Name != model.Name {
			continue
		}
		if entry.Provider != model.Provider {
			continue
		}
		return entry, nil
	}
}

// serviceForModel rejects dispatch after Close and returns the canonical model
// that must accompany calls to the resolved service, rather than its alias.
func (r *Router) serviceForModel(ctx context.Context, model llmapi.ModelEntry) (llmapi.Service, llmapi.ModelEntry, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, llmapi.ModelEntry{}, ErrRouterClosed
	}
	if model.Provider == "" {
		if resolved, ok, err := r.resolveAlias(ctx, model); err != nil {
			return nil, llmapi.ModelEntry{}, err
		} else if ok {
			model = resolved
		}
	}
	svc, err := r.resolve(ctx, model)
	return svc, model, err
}

// resolve picks the llmapi.Service that serves model.Provider. The
// codex provider builds a fresh service per request because the access
// token rotates. Local llama.cpp services are cached per
// (name, path, projector, context window) tuple so the multi-GiB model
// load + KV cache happen once per configuration.
func (r *Router) resolve(ctx context.Context, model llmapi.ModelEntry) (llmapi.Service, error) {
	switch model.Provider {
	case "":
		return nil, errors.New("llmrouter: ModelEntry.Provider must be set")
	case ProviderOpenAI:
		return r.resolveOpenAI(ctx)
	case ProviderAnthropic:
		return r.resolveAnthropic(ctx)
	case ProviderCodex:
		return r.resolveCodex(ctx)
	case ProviderClaude:
		return r.resolveClaude(ctx)
	case ProviderGemini:
		return r.resolveGemini(ctx)
	case ProviderBedrock:
		return r.resolveBedrock(ctx)
	case ProviderCustom:
		if r.custom == nil {
			return nil, fmt.Errorf("llmrouter: custom provider not configured")
		}
		return r.custom, nil
	case ProviderOllama:
		// Ollama models carry their per-model base URL in
		// ModelEntry.BaseURL; rebuild a client tuned to that URL.
		return openai.NewClient("ollama", r.cfg.OllamaClientConfig(model.BaseURL)), nil
	case ProviderLocal:
		return r.resolveLocal()
	default:
		return nil, fmt.Errorf("llmrouter: no provider registered for %q", model.Provider)
	}
}

// resolveLocal returns the local backend. The backend owns the managed
// llama-server pool; it decides whether the server binary is installed and,
// if not, returns the install-instruction error when a completion is
// dispatched.
func (r *Router) resolveLocal() (llmapi.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRouterClosed
	}
	return r.localService, nil
}

// customModelEntries materialises cfg.Custom.AvailableModels as a
// stable slice of ModelEntry suitable for Models() to expose.
func customModelEntries(cfg llm.CustomConfig) []llmapi.ModelEntry {
	out := make([]llmapi.ModelEntry, 0, len(cfg.AvailableModels))
	for name, ctxWindow := range cfg.AvailableModels {
		out = append(out, llmapi.ModelEntry{
			Name:          name,
			Provider:      ProviderCustom,
			ContextWindow: ctxWindow,
			BaseURL:       cfg.URL,
		})
	}
	return out
}

// resolveCodex loads the stored Codex credential and constructs a
// fresh OpenAI-compatible client that targets the ChatGPT Codex
// backend. The credential's access token is the bearer; the
// installation ID and per-request headers come from the same
// credential. Surfacing a clear "no codex credential" error here
// avoids the openai SDK fallback to api.openai.com that masks the
// real failure as an HTTP 401.
func (r *Router) resolveCodex(ctx context.Context) (llmapi.Service, error) {
	cred, err := codex.CredentialForClient(ctx, r.storage)
	if err != nil {
		if errors.Is(err, codex.ErrCredentialNotFound) {
			return nil, fmt.Errorf(
				"llmrouter: no codex credential found; run " +
					"`models providers codex login` from the rune shell")
		}
		return nil, fmt.Errorf("llmrouter: load codex credential: %w", err)
	}
	cfg := r.cfg.CodexClientConfig()
	cfg.ClientMetadata = map[string]string{
		"x-codex-installation-id": cred.InstallationID,
	}
	cfg.Headers = cred.ClientHeaders()
	// Seed a default PromptCacheKey so the openai client emits the
	// session_id / x-client-request-id headers (and the
	// prompt_cache_key body field) on every request. The ChatGPT
	// Codex backend rejects requests without these correlation
	// headers with 400 Bad Request. Callers that thread their own
	// conversation correlation key via request.PromptCacheKey still
	// take precedence; this fallback only fires when the caller does
	// not supply one (e.g. ad-hoc `runectl llm message ...`).
	if sid, idErr := codex.NewSessionID(); idErr == nil {
		cfg.DefaultPromptCacheKey = sid
	}
	return openai.NewClient(cred.AccessToken, cfg), nil
}

// resolveClaude loads the stored Claude Code subscription credential and
// constructs a fresh Anthropic client authenticated with the OAuth bearer
// token plus the Agent-SDK identifying headers. The credential's access
// token rotates, so the client is rebuilt per request (like codex).
func (r *Router) resolveClaude(ctx context.Context) (llmapi.Service, error) {
	cred, err := claude.CredentialForClient(ctx, r.storage)
	if err != nil {
		if errors.Is(err, claude.ErrCredentialNotFound) {
			return nil, fmt.Errorf(
				"llmrouter: no claude credential found; run " +
					"`models providers claude login` from the rune shell")
		}
		return nil, fmt.Errorf("llmrouter: load claude credential: %w", err)
	}
	cfg := r.cfg.ClaudeClientConfig()
	cfg.OAuthToken = cred.AccessToken
	cfg.Headers = cred.ClientHeaders()
	return anthropic.NewClient(cred.AccessToken, cfg), nil
}

// resolveAPIKey returns the API key for a hosted provider, preferring the
// active key stored in rune-side local storage (set via
// `/models providers <p> add`) and falling back to the static configured
// key. It returns an empty string when neither source supplies one.
func (r *Router) resolveAPIKey(ctx context.Context, provider, configKey string) string {
	if key, err := r.store.active(ctx, provider); err == nil && key != "" {
		return key
	}
	return configKey
}

func (r *Router) hostedClient(
	provider, apiKey string, build func() llmapi.Service,
) (llmapi.Service, error) {
	return r.hostedClientInRegion(provider, apiKey, "", build)
}

func (r *Router) hostedClientInRegion(
	provider, apiKey, region string, build func() llmapi.Service,
) (llmapi.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRouterClosed
	}
	key := hostedCacheKey{provider: provider, apiKey: apiKey, region: region}
	if svc, ok := r.hostedClients[key]; ok {
		return svc, nil
	}
	svc := build()
	r.hostedClients[key] = svc
	return svc, nil
}

func (r *Router) resolveOpenAI(ctx context.Context) (llmapi.Service, error) {
	key := r.resolveAPIKey(ctx, ProviderOpenAI, r.cfg.OpenAI.APIKey)
	if key == "" {
		return nil, errors.New(
			"llmrouter: openai api key not configured; run " +
				"`/models providers openai add`")
	}
	return r.hostedClient(ProviderOpenAI, key, func() llmapi.Service {
		return openai.NewClient(key, r.cfg.OpenAIClientConfig())
	})
}

func (r *Router) resolveAnthropic(ctx context.Context) (llmapi.Service, error) {
	key := r.resolveAPIKey(ctx, ProviderAnthropic, r.cfg.Anthropic.APIKey)
	if key == "" {
		return nil, errors.New(
			"llmrouter: anthropic api key not configured; run " +
				"`/models providers anthropic add`")
	}
	return r.hostedClient(ProviderAnthropic, key, func() llmapi.Service {
		return anthropic.NewClient(key, r.cfg.AnthropicClientConfig())
	})
}

func (r *Router) resolveGemini(ctx context.Context) (llmapi.Service, error) {
	key := r.resolveAPIKey(ctx, ProviderGemini, r.cfg.Gemini.APIKey)
	if key == "" {
		return nil, errors.New(
			"llmrouter: gemini api key not configured; run " +
				"`/models providers gemini add`")
	}
	return r.hostedClient(ProviderGemini, key, func() llmapi.Service {
		return gemini.NewClient(key, r.cfg.GeminiClientConfig())
	})
}

func (r *Router) geminiCatalog(ctx context.Context) iterator.Iterator[llmapi.ModelEntry] {
	svc, err := r.resolveGemini(ctx)
	if err != nil {
		return iterator.FromSlice(gemini.ModelEntries())
	}
	return svc.Models()
}

// resolveBedrock returns the Bedrock client. Unlike the other hosted
// providers an unset key is not an error: the client then authenticates
// through the standard AWS credential chain (environment, shared profile,
// SSO, IMDS). The empty key is still a valid cache key, so switching
// between a stored key and the chain produces distinct clients. Keys come
// exclusively from the keystore, which also records the region each key
// is scoped to; there is no config-file key or region.
func (r *Router) resolveBedrock(ctx context.Context) (llmapi.Service, error) {
	key, region, err := r.store.activeWithRegion(ctx, ProviderBedrock)
	if err != nil {
		key, region = "", ""
	}
	return r.hostedClientInRegion(ProviderBedrock, key, region, func() llmapi.Service {
		cfg := r.cfg.BedrockClientConfig()
		cfg.Region = region
		return bedrock.NewClient(key, cfg)
	})
}

func (r *Router) bedrockCatalog(ctx context.Context) iterator.Iterator[llmapi.ModelEntry] {
	svc, err := r.resolveBedrock(ctx)
	if err != nil {
		return iterator.FromSlice(bedrock.ModelEntries())
	}
	return svc.Models()
}

// BedrockCredentialChain reports whether ambient AWS credentials can sign
// Bedrock requests, and names the source that supplied them. Callers use it
// to explain that Bedrock works even with no stored API key.
func (r *Router) BedrockCredentialChain(ctx context.Context) (string, bool) {
	return bedrock.CredentialChainStatus(ctx, r.cfg.BedrockClientConfig())
}

// AddProviderKey stores a named API key for a hosted provider. The first
// key added for a provider becomes the active key. region records the
// key's scope for region-bound providers (Bedrock); pass the empty string
// for providers whose keys are global.
func (r *Router) AddProviderKey(ctx context.Context, provider, name, key, region string) error {
	return r.store.add(ctx, provider, name, key, region)
}

// RemoveProviderKey deletes a named API key. When the removed key is the
// active one, another remaining key is promoted (or the active key is
// cleared when none remain).
func (r *Router) RemoveProviderKey(ctx context.Context, provider, name string) error {
	return r.store.remove(ctx, provider, name)
}

// UseProviderKey makes a stored named key the active key for a provider.
func (r *Router) UseProviderKey(ctx context.Context, provider, name string) error {
	return r.store.use(ctx, provider, name)
}

// ProviderKeyNames returns the stored key names for a provider, sorted.
func (r *Router) ProviderKeyNames(ctx context.Context, provider string) ([]string, error) {
	return r.store.names(ctx, provider)
}

// ProviderKeyActiveName returns the name of the active key for a provider,
// or the empty string when none is set.
func (r *Router) ProviderKeyActiveName(ctx context.Context, provider string) (string, error) {
	return r.store.activeName(ctx, provider)
}

// ProviderKeyActive returns the value of the active key for a provider, or
// ErrAPIKeyNotSet when none is set.
func (r *Router) ProviderKeyActive(ctx context.Context, provider string) (string, error) {
	return r.store.active(ctx, provider)
}

// ProviderKeyRegions returns the stored key-name -> region mapping for a
// provider. Keys stored without a region are absent from the map.
func (r *Router) ProviderKeyRegions(ctx context.Context, provider string) (map[string]string, error) {
	return r.store.regions(ctx, provider)
}

// buildHostedClient constructs a throwaway client for the given provider
// and key plus the model entry to probe during verification. The client
// is not cached. region carries the key's scope for region-bound
// providers (Bedrock) and is ignored by the rest.
func (r *Router) buildHostedClient(
	provider, apiKey, region string,
) (llmapi.Service, llmapi.ModelEntry, error) {
	switch provider {
	case ProviderOpenAI:
		entries := openai.ModelEntries()
		if len(entries) == 0 {
			return nil, llmapi.ModelEntry{}, errors.New("llmrouter: openai has no models to verify against")
		}
		return openai.NewClient(apiKey, r.cfg.OpenAIClientConfig()), entries[0], nil
	case ProviderAnthropic:
		entries := anthropic.ModelEntries()
		if len(entries) == 0 {
			return nil, llmapi.ModelEntry{}, errors.New("llmrouter: anthropic has no models to verify against")
		}
		return anthropic.NewClient(apiKey, r.cfg.AnthropicClientConfig()), entries[0], nil
	case ProviderGemini:
		client := gemini.NewClient(apiKey, r.cfg.GeminiClientConfig())
		model := llmapi.ModelEntry{Name: gemini.VerificationModel, Provider: ProviderGemini}
		return client, model, nil
	case ProviderBedrock:
		cfg := r.cfg.BedrockClientConfig()
		cfg.Region = region
		client := bedrock.NewClient(apiKey, cfg)
		model := llmapi.ModelEntry{
			Name:     bedrock.VerificationModelForRegion(region),
			Provider: ProviderBedrock,
		}
		return client, model, nil
	default:
		return nil, llmapi.ModelEntry{}, fmt.Errorf("llmrouter: provider %q does not support key verification", provider)
	}
}

// VerifyProviderKey tests an API key by issuing a minimal completion
// against the provider's first model and consuming the first event. It
// returns the provider error verbatim, or nil when the key works. region
// scopes the probe for region-bound providers (Bedrock) and is empty
// otherwise.
func (r *Router) VerifyProviderKey(ctx context.Context, provider, key, region string) error {
	client, model, err := r.newHostedClient(provider, key, region)
	if err != nil {
		return err
	}
	it, err := client.CreateCompletion(ctx, model, llmapi.Request{
		MaxOutputTokens: 1,
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "ping"},
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	ev, ok := it.Next(ctx)
	if !ok {
		return nil
	}
	if ev.Type == llmapi.EventStreamError {
		return ev.Error
	}
	return nil
}
