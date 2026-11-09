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

package anthropic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	ant "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"unstable.build/rune/internal/llm/ratelimit"
)

// Config holds the configuration for the Anthropic client.
type Config struct {
	MaxTokens   int
	Temperature float64
	TopP        float64
	DebugHTTP   bool
	// BaseURL overrides the default Anthropic API endpoint.
	BaseURL string

	// ReasoningEffort maps to Anthropic's OutputConfig.Effort.
	// Valid values: "none", "minimal", "low", "medium", "high", "xhigh" (OpenAI),
	// "max" (Anthropic Opus 4.6 only).
	ReasoningEffort string
	// EnableThinking enables adaptive extended thinking. Claude decides
	// dynamically when and how much to think. Recommended for 4.6 models.
	EnableThinking bool
	// ResponseFormat, when set, maps to Anthropic's OutputConfig.Format
	// for structured JSON output.
	ResponseFormat *llmapi.ResponseFormat
	// CacheControl configures prompt caching. Valid values:
	// "ephemeral" (5m default TTL), "5m", "1h", or "" (disabled).
	CacheControl string

	// OAuthToken, when set, authenticates requests with an OAuth bearer
	// token (Authorization: Bearer ...) instead of an x-api-key. It is
	// used by the `claude` provider, which onboards a Claude Code
	// subscription via OAuth2. When OAuthToken is set the SDK's x-api-key
	// header is suppressed.
	OAuthToken string
	// Headers are extra per-request headers merged into every request,
	// used by the `claude` provider to send the Agent-SDK identifying
	// headers (notably anthropic-beta). Values here override headers the
	// SDK would otherwise set.
	Headers map[string]string

	// ClaudeCodeSpoof shapes requests like the official Claude Code CLI.
	// When set, the system prompt array is prefixed with the Claude Code
	// billing header, agent identifier, and static system prompt, and the
	// serialized body's billing-header cch field is signed. Required by the
	// `claude` provider so the subscription backend does not throttle the
	// request as third-party traffic.
	ClaudeCodeSpoof bool
}

type client struct {
	config    Config
	anthropic ant.Client
}

func (c *client) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (c *client) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

// NewClient returns an llmapi.Service backed by the native Anthropic Messages API
// with prompt caching support.
func NewClient(token string, config Config) llmapi.Service {
	return NewClientWithHTTP(token, config, nil)
}

// CreateCompletion starts a streaming completion using the native Anthropic
// Messages API. Prompt caching is automatically applied to the system prompt
// and tool definitions.
func (c *client) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	// Check token count against context window with 5% safety margin.
	// Use the caller-supplied token count when available (the agent loop
	// pre-computes this from provider-reported usage), avoiding a redundant
	// HTTP round-trip to the CountTokens API on every turn.
	if model.ContextWindow > 0 {
		count := request.TokenCount
		if count == 0 {
			var err error
			count, err = c.CountTokens(model, request.Messages)
			if err != nil {
				count = 0
			}
		}
		if count > 0 {
			maxAllowed := int(float64(model.ContextWindow) * 0.95)
			if count > maxAllowed {
				return nil, &llmapi.ErrContextWindowExceeded{Count: count, Max: model.ContextWindow}
			}
		}
	}

	// Resolve effective effort: request-level takes precedence over config-level.
	// Only an explicit per-request effort warrants a warning when the model
	// cannot honor it; the config value is a standing cross-model preference
	// and is dropped silently.
	effort := string(request.ReasoningEffort)
	explicitEffort := effort != ""
	if !explicitEffort {
		effort = c.config.ReasoningEffort
	}

	// Normalize effort against model capabilities. Unsupported levels are
	// dropped (empty string) so the provider default applies.
	var effortWarnings []llmapi.Event
	normalized, effortWarn := NormalizeEffort(model.Name, effort)
	request.ReasoningEffort = llmapi.ReasoningEffort(normalized)
	if explicitEffort && effortWarn != "" {
		effortWarnings = append(effortWarnings, llmapi.Event{
			Type: llmapi.EventRateLimitWarning,
			RateLimit: &llmapi.RateLimitInfo{
				Message: effortWarn,
			},
		})
	}

	// Build request parameters.
	params := anthropicParamsFromRequest(model.Name, request, c.config)

	// Apply prompt caching breakpoints on system + tools.
	applyCacheBreakpoints(&params, c.config.CacheControl)

	// Capture response headers via middleware for rate limit inspection.
	var capturedHeaders http.Header
	headerMiddleware := option.WithMiddleware(
		func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			resp, err := next(req)
			if resp != nil {
				capturedHeaders = resp.Header.Clone()
			}
			return resp, err
		},
	)

	// Retry on transient errors.
	var warnings []llmapi.Event
	var stream *ssestream.Stream[ant.MessageStreamEventUnion]

	retryStrategy := retry.CombinedStrategy(
		retry.LimitStrategy(ratelimit.MaxStreamRetries+1),
		ratelimit.RetryAfterOrBackoffStrategy(&capturedHeaders, &warnings),
	)

	retryErr := retry.Retry(ctx, retryStrategy, func(_ context.Context) (bool, error) {
		capturedHeaders = nil
		stream = c.anthropic.Messages.NewStreaming(ctx, params,
			headerMiddleware, option.WithMaxRetries(0))

		err := stream.Err()
		if err == nil {
			return false, nil
		}
		if !isRetryableError(err) {
			return false, err
		}
		_ = stream.Close()
		return true, err
	})
	if retryErr != nil {
		if len(warnings) == 0 {
			if stream != nil {
				_ = stream.Close()
			}
			return nil, retryErr
		}
	}

	newStream := func() *ssestream.Stream[ant.MessageStreamEventUnion] {
		capturedHeaders = nil
		return c.anthropic.Messages.NewStreaming(ctx, params,
			headerMiddleware, option.WithMaxRetries(0))
	}

	// Prepend effort normalization warnings before any retry warnings.
	if len(effortWarnings) > 0 {
		warnings = append(effortWarnings, warnings...)
	}

	return &streamIterator{
		stream:           stream,
		pendingWarnings:  warnings,
		capturedHeaders:  capturedHeaders,
		newStream:        newStream,
		midStreamRetries: ratelimit.MaxMidStreamRetries,
	}, nil
}

// CountTokens returns the token count for the given messages using the
// Anthropic CountTokens API, which provides exact counts for the model's
// tokenizer. Falls back to a character-based estimate on API errors.
func (c *client) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	system, messages := convertMessages(msgs)

	params := ant.MessageCountTokensParams{
		Model:    ant.Model(model.Name),
		Messages: messages,
	}
	if len(system) > 0 {
		params.System = ant.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: system,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := c.anthropic.Messages.CountTokens(ctx, params)
	if err != nil {
		slog.Warn("anthropic CountTokens API failed, falling back to estimate",
			"error", err)
		return estimateTokens(msgs), nil
	}
	return int(result.InputTokens), nil
}

// estimateTokens provides a rough character-based token estimate as a fallback
// when the CountTokens API is unavailable.
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
		total += 4 // per-message overhead
	}
	return total
}

// Models advertises the static Anthropic model catalog.
func (c *client) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice(ModelEntries())
}

// GetModel scans the static model catalog for the given model name.
func (c *client) GetModel(_ context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	for _, e := range ModelEntries() {
		if e.Name == model.Name {
			return e, nil
		}
	}
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// isRetryableError reports whether err represents a transient Anthropic API error.
func isRetryableError(err error) bool {
	// Check x-should-retry header from Anthropic response.
	var apiErr *ant.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		if v := apiErr.Response.Header.Get("x-should-retry"); v != "" {
			return v == "true"
		}
	}
	if ratelimit.IsTransientNetworkError(err) {
		return true
	}
	status := httpStatusFromError(err)
	switch {
	case status == http.StatusRequestTimeout,
		status == http.StatusConflict,
		status == http.StatusTooManyRequests:
		return true
	case status >= http.StatusInternalServerError:
		return true
	}
	return false
}

// httpStatusFromError extracts the HTTP status code from an Anthropic SDK error.
func httpStatusFromError(err error) int {
	var apiErr *ant.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// debugMiddleware returns an option.Middleware that logs HTTP request/response details.
func debugMiddleware() option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		slog.Debug("anthropic HTTP request",
			"method", req.Method,
			"url", req.URL.String(),
			"content_length", req.ContentLength,
		)
		resp, err := next(req)
		if resp != nil {
			slog.Debug("anthropic HTTP response",
				"status", resp.StatusCode,
				"content_type", resp.Header.Get("Content-Type"),
				"request_id", resp.Header.Get("Request-Id"),
			)
		}
		if err != nil {
			slog.Debug("anthropic HTTP error", "error", err)
		}
		return resp, err
	}
}

// oauthHeaderMiddleware suppresses the SDK-attached x-api-key header (the
// OAuth bearer path authenticates via Authorization) and sets the
// Agent-SDK identifying headers from the credential.
func oauthHeaderMiddleware(headers map[string]string) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		req.Header.Del("x-api-key")
		req.Header.Del("X-Api-Key")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return next(req)
	}
}

// NewClientWithHTTP creates an llmapi.Service backed by the Anthropic Messages API
// using the supplied HTTP client. Pass nil to use the default transport.
// Intended for tests and benchmarks that need an in-process transport without
// starting a real server.
func NewClientWithHTTP(token string, config Config, httpClient *http.Client) llmapi.Service {
	opts := []option.RequestOption{
		option.WithMaxRetries(0), // We handle retries ourselves.
	}
	if config.OAuthToken != "" {
		opts = append(opts,
			option.WithAuthToken(config.OAuthToken),
			option.WithMiddleware(oauthHeaderMiddleware(config.Headers)),
		)
	} else {
		opts = append(opts, option.WithAPIKey(token))
	}
	if config.ClaudeCodeSpoof {
		opts = append(opts, option.WithMiddleware(claudeCodeMiddleware()))
	}
	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	if config.DebugHTTP {
		opts = append(opts, option.WithMiddleware(debugMiddleware()))
	}
	return &client{
		config:    config,
		anthropic: ant.NewClient(opts...),
	}
}

// Verify at compile time that client implements llmapi.Service.
var _ llmapi.Service = (*client)(nil)

// ClientConstructor is the function signature for creating an Anthropic
// llmapi.Service. Tests may substitute a stub.
type ClientConstructor func(token string, cfg Config) llmapi.Service

// Verify NewClient matches ClientConstructor signature.
var _ ClientConstructor = NewClient

// FormatUsageLog returns a formatted string with token usage including
// cache metrics for structured logging.
func FormatUsageLog(usage llmapi.Usage) string {
	return fmt.Sprintf("sent=%d received=%d cached=%d",
		usage.TokensSent, usage.TokensReceived, usage.TokensCached)
}
