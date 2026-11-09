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

package gemini

import (
	"context"
	"iter"
	"log/slog"
	"net/http"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"google.golang.org/genai"
)

// Config holds the configuration for the native Gemini client.
type Config struct {
	Temperature     float64
	TopP            float64
	MaxOutputTokens int
	// ReasoningEffort maps to Gemini's ThinkingConfig thinking level.
	ReasoningEffort string
	// ResponseFormat, when set, requests structured JSON output.
	ResponseFormat *llmapi.ResponseFormat
	// BaseURL overrides the Gemini API endpoint. Intended for tests that
	// point the SDK at an in-process server.
	BaseURL string
	// DebugHTTP enables HTTP debug logging.
	DebugHTTP bool
}

type client struct {
	config Config
	genai  *genai.Client
	// initErr is surfaced lazily from CreateCompletion/CountTokens so the
	// NewClient(...) llmapi.Service seam matches the openai/anthropic
	// providers (which never fail at construction time).
	initErr error
}

func (c *client) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (c *client) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

// NewClient returns an llmapi.Service backed by the native Gemini API via
// Google's google.golang.org/genai SDK.
func NewClient(token string, config Config) llmapi.Service {
	return NewClientWithHTTP(token, config, nil)
}

// NewClientWithHTTP creates an llmapi.Service backed by the Gemini API using
// the supplied HTTP client. Pass nil to use the SDK default transport.
// Intended for tests that need an in-process transport.
func NewClientWithHTTP(token string, config Config, httpClient *http.Client) llmapi.Service {
	if config.DebugHTTP {
		base := http.DefaultTransport
		if httpClient != nil && httpClient.Transport != nil {
			base = httpClient.Transport
		}
		if httpClient == nil {
			httpClient = &http.Client{}
		}
		httpClient.Transport = &debugTransport{base: base}
	}
	cc := &genai.ClientConfig{
		APIKey:     token,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	}
	if config.BaseURL != "" {
		cc.HTTPOptions.BaseURL = config.BaseURL
	}
	gc, err := genai.NewClient(context.Background(), cc)
	return &client{config: config, genai: gc, initErr: err}
}

// CreateCompletion starts a streaming completion using the native Gemini API.
func (c *client) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if c.initErr != nil {
		return nil, c.initErr
	}

	// Context-window guard with a 5% safety margin. Prefer the caller-supplied
	// token count to avoid a redundant CountTokens round-trip.
	if model.ContextWindow > 0 {
		count := request.TokenCount
		if count == 0 {
			if n, err := c.CountTokens(model, request.Messages); err == nil {
				count = n
			}
		}
		if count > 0 {
			maxAllowed := int(float64(model.ContextWindow) * 0.95)
			if count > maxAllowed {
				return nil, &llmapi.ErrContextWindowExceeded{Count: count, Max: model.ContextWindow}
			}
		}
	}

	// Resolve effective effort: request-level takes precedence over config.
	// Only an explicit per-request effort warrants a warning when the model
	// cannot honor it; the config value is a standing cross-model preference
	// and is dropped silently.
	effort := string(request.ReasoningEffort)
	explicitEffort := effort != ""
	if !explicitEffort {
		effort = c.config.ReasoningEffort
	}
	normalized, effortWarn := NormalizeEffort(model.Name, effort)

	var warnings []llmapi.Event
	if explicitEffort && effortWarn != "" {
		warnings = append(warnings, llmapi.Event{
			Type:      llmapi.EventRateLimitWarning,
			RateLimit: &llmapi.RateLimitInfo{Message: effortWarn},
		})
	}

	system, contents := contentsFromMessages(request.Messages)
	cfg := &genai.GenerateContentConfig{
		Tools:      toolsFromModel(request.Tools),
		ToolConfig: toolConfig(request.ToolChoice),
	}
	if system != "" {
		cfg.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	if c.config.Temperature != 0 {
		cfg.Temperature = new(float32(c.config.Temperature))
	}
	if c.config.TopP != 0 {
		cfg.TopP = new(float32(c.config.TopP))
	}
	if maxOutputTokens := effectiveMaxOutputTokens(
		model.Name, request.MaxOutputTokens, c.config.MaxOutputTokens,
	); maxOutputTokens > 0 {
		cfg.MaxOutputTokens = int32(maxOutputTokens)
	}
	if tc := thinkingConfig(normalized); tc != nil {
		cfg.ThinkingConfig = tc
	}
	applyResponseFormat(cfg, request.ResponseFormat, c.config.ResponseFormat)

	seq := c.genai.Models.GenerateContentStream(ctx, model.Name, contents, cfg)
	return newStreamIterator(seq, warnings), nil
}

func effectiveMaxOutputTokens(model string, requestLimit, configLimit int) int {
	limit := requestLimit
	if limit <= 0 {
		limit = configLimit
	}
	if ceiling := MaxOutputTokens(model); ceiling > 0 && limit > ceiling {
		return ceiling
	}
	return limit
}

// CountTokens returns the token count for the given messages using the Gemini
// CountTokens API. Falls back to a character-based estimate on API errors.
func (c *client) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	if c.initErr != nil {
		return estimateTokens(msgs), nil
	}
	_, contents := contentsFromMessages(msgs)
	if len(contents) == 0 {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.genai.Models.CountTokens(ctx, model.Name, contents, nil)
	if err != nil {
		slog.Warn("gemini CountTokens API failed, falling back to estimate", "error", err)
		return estimateTokens(msgs), nil
	}
	return int(res.TotalTokens), nil
}

// estimateTokens provides a rough character-based token estimate as a fallback.
func estimateTokens(msgs []llmapi.Message) int {
	var total int
	for _, msg := range msgs {
		total += len(msg.Content) / 4
		total += len(msg.ReasoningContent) / 4
		for _, p := range msg.MultiContent {
			total += len(p.Text) / 4
		}
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Arguments) / 4
			total += len(tc.Function.Name) / 4
		}
		total += 4
	}
	return total
}

// Models queries the Gemini API for the live model catalog, filtered to
// generateContent-capable models. The genai listing is paginated and
// lazy. Listing requires a working API key, so when the live query fails
// (or the client could not be constructed) the static ModelEntries
// catalog is served as a fallback while the underlying error is still
// exposed through the iterator's Err.
func (c *client) Models() iterator.Iterator[llmapi.ModelEntry] {
	return withStaticFallback(c.liveModels(), ModelEntries())
}

// liveModels returns an iterator over the live Gemini catalog, surfacing
// any construction or listing error through its Err.
func (c *client) liveModels() iterator.Iterator[llmapi.ModelEntry] {
	if c.initErr != nil {
		return iterator.Error[llmapi.ModelEntry](c.initErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	next, stop := iter.Pull2(c.genai.Models.All(ctx))

	return iterator.FromFunc(
		func(context.Context) (llmapi.ModelEntry, bool, error) {
			for {
				m, err, ok := next()
				if err != nil {
					return llmapi.ModelEntry{}, false, mapError(err)
				}
				if !ok {
					return llmapi.ModelEntry{}, false, nil
				}
				entry, ok := modelEntryFromGenAI(m)
				if !ok {
					continue
				}
				return entry, true, nil
			}
		},
		func() error {
			stop()
			cancel()
			return nil
		},
	)
}

// GetModel looks up the canonical entry for the given model from the live
// Gemini catalog, falling back to the static catalog when the live query
// is unavailable (e.g. no working key during bootstrap).
func (c *client) GetModel(ctx context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	if c.initErr == nil {
		for m, err := range c.genai.Models.All(ctx) {
			if err != nil {
				break
			}
			entry, ok := modelEntryFromGenAI(m)
			if ok && entry.Name == model.Name {
				return entry, nil
			}
		}
	}
	for _, e := range ModelEntries() {
		if e.Name == model.Name {
			return e, nil
		}
	}
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// Verify at compile time that client implements llmapi.Service.
var _ llmapi.Service = (*client)(nil)

// ClientConstructor is the function signature for creating a Gemini
// llmapi.Service. Tests may substitute a stub.
type ClientConstructor func(token string, cfg Config) llmapi.Service

// Verify NewClient matches ClientConstructor signature.
var _ ClientConstructor = NewClient
