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

package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/packages/ssestream"
	"github.com/openai/openai-go/v2/responses"
	"github.com/openai/openai-go/v2/shared"
	"github.com/pkoukk/tiktoken-go"
	tiktokenLoader "github.com/pkoukk/tiktoken-go-loader"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
)

// Config represents the default parameters used for chat completions
// along with the configuration passed to the openai client.
type Config struct {
	// Number between -2.0 and 2.0. Positive values penalize new tokens based
	// on their existing frequency in the text so far, decreasing the model's likelihood
	// to repeat the same line verbatim.
	FrequencyPenalty float64
	// Modify the likelihood of specified tokens appearing in the completion.
	LogitBias map[string]int
	// The maximum number of tokens to generate in the chat completion.
	MaxTokens int
	// Number between -2.0 and 2.0. Positive values penalize new tokens based on whether they
	// appear in the text so far, increasing the model's likelihood to talk about new topics.
	PresencePenalty float64
	// What sampling temperature to use, between 0 and 2.
	Temperature float64
	// An alternative to sampling with temperature, called nucleus sampling.
	TopP float64

	// ReasoningEffort controls reasoning effort for reasoning models.
	ReasoningEffort string
	// ReasoningSummary controls the level of reasoning summary output
	// for models using the Responses API (e.g. "auto", "concise", "detailed").
	ReasoningSummary string
	// MaxCompletionTokens is an upper bound for generated tokens including
	// reasoning tokens. Used instead of MaxTokens for reasoning models.
	MaxCompletionTokens int
	// DisableMaxOutputTokens prevents Responses requests from sending an
	// explicit output limit to providers that manage it server-side.
	DisableMaxOutputTokens bool

	// A list of tools the model may call.
	Tools []llmapi.Tool
	// BaseURL for of the http service.
	BaseURL string
	// Headers contains optional provider-specific headers to send with every request.
	Headers map[string]string

	// ResponseFormat ensures responses always follow a specific format.
	ResponseFormat *llmapi.ResponseFormat

	// ForceResponsesAPI forces the client to use the /v1/responses
	// endpoint even for models that support /v1/chat/completions.
	ForceResponsesAPI bool
	// Store, when non-nil, sets the Responses API `store` parameter.
	// Pass false to opt into stateless operation (e.g. ChatGPT Codex
	// backend, ZDR organizations). When stateless, the client also
	// includes `reasoning.encrypted_content` so reasoning items can be
	// threaded back across turns.
	Store *bool
	// DisableParallelToolCalls, when true, sets parallel_tool_calls=false
	// on the Responses API. The ChatGPT Codex backend requires this.
	DisableParallelToolCalls bool
	// ClientMetadata is forwarded as the Responses API `client_metadata`
	// field. Used by the Codex backend to carry e.g. the installation ID
	// (`x-codex-installation-id`).
	ClientMetadata map[string]string
	// DefaultPromptCacheKey is used as request.PromptCacheKey when the
	// caller does not supply one. The Responses API path turns
	// PromptCacheKey into the `prompt_cache_key` body field plus the
	// session_id / x-client-request-id headers used by the ChatGPT
	// Codex backend for routing and tracing. Codex requests without
	// these correlation headers are rejected with 400 Bad Request, so
	// the codex provider seeds this with a per-service UUID. Non-codex
	// providers should leave this empty.
	DefaultPromptCacheKey string

	// DebugHTTP enables debug logging of HTTP request and response
	// byte lengths and selected headers.
	DebugHTTP bool
}

// NewClient returns an llmapi.Service backed by OpenAI's chat completion API.
// The model used for each request is supplied per call via
// CreateCompletion(ctx, model, request).
func NewClient(token string, config Config) llmapi.Service {
	return NewClientWithHTTP(token, config, nil)
}

// NewClientWithHTTP returns an llmapi.Service backed by OpenAI's chat completion
// API using the supplied HTTP client. Pass nil to use the default transport.
// Intended for tests and benchmarks that need an in-process transport without
// starting a real server.
func NewClientWithHTTP(token string, config Config, httpClient *http.Client) llmapi.Service {
	tools := openAIToolsFromModel(config.Tools)

	opts := []option.RequestOption{
		option.WithAPIKey(token),
		option.WithMiddleware(sseDataOnlyMiddleware),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}
	for key, value := range config.Headers {
		if value != "" {
			opts = append(opts, option.WithHeader(key, value))
		}
	}
	if config.DebugHTTP {
		opts = append(opts, option.WithMiddleware(
			newDebugHTTPMiddleware(isAnthropicURL(config.BaseURL)),
		))
	}
	c := openai.NewClient(opts...)

	return &client{
		tools:          tools,
		responseFormat: openAIResponseFormatFromModel(config.ResponseFormat),
		config:         config,
		client:         c,
	}
}

func init() {
	tiktoken.SetBpeLoader(tiktokenLoader.NewOfflineLoader())
}

// Headers logged regardless of provider.
var commonHeaders = []string{
	"Retry-After",
	"Cf-Ray",
}

// Headers logged for OpenAI (and OpenAI-compatible) endpoints.
var openaiHeaders = []string{
	// Request tracking.
	"X-Request-Id",
	"Openai-Organization",
	"Openai-Processing-Ms",
	"Openai-Model",
	"Openai-Version",
	// Rate limits.
	"X-Ratelimit-Limit-Requests",
	"X-Ratelimit-Limit-Tokens",
	"X-Ratelimit-Remaining-Requests",
	"X-Ratelimit-Remaining-Tokens",
	"X-Ratelimit-Reset-Requests",
	"X-Ratelimit-Reset-Tokens",
}

// Headers logged for Anthropic endpoints.
var anthropicHeaders = []string{
	// Request tracking.
	"Request-Id",
	"Anthropic-Organization-Id",
	// Rate limits — requests.
	"Anthropic-Ratelimit-Requests-Limit",
	"Anthropic-Ratelimit-Requests-Remaining",
	"Anthropic-Ratelimit-Requests-Reset",
	// Rate limits — input tokens.
	"Anthropic-Ratelimit-Input-Tokens-Limit",
	"Anthropic-Ratelimit-Input-Tokens-Remaining",
	"Anthropic-Ratelimit-Input-Tokens-Reset",
	// Rate limits — output tokens.
	"Anthropic-Ratelimit-Output-Tokens-Limit",
	"Anthropic-Ratelimit-Output-Tokens-Remaining",
	"Anthropic-Ratelimit-Output-Tokens-Reset",
	// Rate limits — combined tokens.
	"Anthropic-Ratelimit-Tokens-Limit",
	"Anthropic-Ratelimit-Tokens-Remaining",
	"Anthropic-Ratelimit-Tokens-Reset",
}

// newDebugHTTPMiddleware returns a middleware that logs request and response
// details at Debug level, selecting provider-specific headers based on
// whether the endpoint is Anthropic.
func newDebugHTTPMiddleware(isAnthropic bool) func(
	*http.Request, option.MiddlewareNext,
) (*http.Response, error) {
	providerHeaders := openaiHeaders
	if isAnthropic {
		providerHeaders = anthropicHeaders
	}

	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		reqLen := int64(-1)
		if req.ContentLength > 0 {
			reqLen = req.ContentLength
		}
		slog.Debug("HTTP request",
			"method", req.Method,
			"url", req.URL.String(),
			"content_length", reqLen,
		)

		resp, err := next(req)
		if resp != nil {
			attrs := []any{
				"status", resp.StatusCode,
				"content_length", resp.ContentLength,
				"content_type", resp.Header.Get("Content-Type"),
			}
			for _, h := range commonHeaders {
				if v := resp.Header.Get(h); v != "" {
					attrs = append(attrs, h, v)
				}
			}
			for _, h := range providerHeaders {
				if v := resp.Header.Get(h); v != "" {
					attrs = append(attrs, h, v)
				}
			}
			slog.Debug("HTTP response", attrs...)
		}
		if err != nil {
			slog.Debug("HTTP error", "error", err)
		}
		return resp, err
	}
}

type client struct {
	tools          []openai.ChatCompletionToolUnionParam
	responseFormat *openai.ChatCompletionNewParamsResponseFormatUnion
	config         Config
	client         openai.Client
	encoders       sync.Map // string → *modelEncoder
}

type modelEncoder struct {
	tk               *tiktoken.Tiktoken
	tokensPerMessage int
	tokensPerName    int
}

// encoderFor returns a per-model tiktoken encoder, cached across requests.
// Falls back to the GPT4 encoder if the model is not recognised by
// tiktoken-go.
func (a *client) encoderFor(modelName string) *modelEncoder {
	if v, ok := a.encoders.Load(modelName); ok {
		return v.(*modelEncoder)
	}
	tkm, err := tiktoken.EncodingForModel(modelName)
	if err != nil {
		slog.Warn("token counting might be off: encoding error",
			"model", modelName, "error", err)
		tkm, err = tiktoken.EncodingForModel(GPT4)
		if err != nil {
			panic(fmt.Errorf("fallback encoding for model "+
				"from %s to %s failed: %v", modelName, GPT4, err))
		}
	}
	enc := &modelEncoder{tk: tkm, tokensPerMessage: 3, tokensPerName: 1}
	if modelName == "gpt-3.5-turbo-0301" {
		enc.tokensPerMessage = 4
		enc.tokensPerName = -1
	}
	actual, _ := a.encoders.LoadOrStore(modelName, enc)
	return actual.(*modelEncoder)
}

func (a *client) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	// Use the caller-supplied token count when available (the agent loop
	// pre-computes this from provider-reported usage). This avoids a
	// redundant tiktoken encode of the entire conversation on every turn.
	count := request.TokenCount
	if count == 0 {
		count, _ = a.CountTokens(model, request.Messages)
	}
	max := model.ContextWindow
	// Apply a 5% safety margin to account for token count estimation drift.
	safeMax := max * 95 / 100
	if count > safeMax {
		return nil, &llmapi.ErrContextWindowExceeded{Count: count, Max: max}
	}

	// Resolve effective effort: request-level takes precedence over config-level.
	// Only an explicit per-request effort warrants a warning when the model
	// cannot honor it; the config value is a standing cross-model preference
	// and is dropped silently.
	effort := string(request.ReasoningEffort)
	explicitEffort := effort != ""
	if !explicitEffort {
		effort = a.config.ReasoningEffort
	}

	// Fall back to a provider-level default prompt cache key when the
	// caller does not supply one. The codex provider seeds this with a
	// per-service UUID so the ChatGPT Codex backend always sees the
	// session_id / x-client-request-id correlation headers it requires.
	if request.PromptCacheKey == "" && a.config.DefaultPromptCacheKey != "" {
		request.PromptCacheKey = a.config.DefaultPromptCacheKey
	}

	// Normalize effort against model capabilities. Unsupported levels are
	// dropped (empty string) so the provider default applies.
	var effortWarning *llmapi.Event
	normalized, warning := NormalizeEffort(model.Name, effort)
	request.ReasoningEffort = llmapi.ReasoningEffort(normalized)
	if explicitEffort && warning != "" {
		effortWarning = &llmapi.Event{
			Type: llmapi.EventRateLimitWarning,
			RateLimit: &llmapi.RateLimitInfo{
				Message: warning,
			},
		}
	}

	var it iterator.Iterator[llmapi.Event]
	var err error
	if a.config.ForceResponsesAPI {
		it, err = a.createResponsesCompletion(ctx, model.Name, request)
	} else {
		it, err = a.createChatCompletion(ctx, model.Name, request)
	}
	if err != nil {
		return nil, err
	}
	if effortWarning != nil {
		it = &prefixWarningIterator{warning: effortWarning, inner: it}
	}
	return it, nil
}

// prefixWarningIterator emits a single warning event before delegating
// to the wrapped iterator.
type prefixWarningIterator struct {
	warning *llmapi.Event
	inner   iterator.Iterator[llmapi.Event]
}

func (p *prefixWarningIterator) Next(ctx context.Context) (llmapi.Event, bool) {
	if p.warning != nil {
		ev := *p.warning
		p.warning = nil
		return ev, true
	}
	return p.inner.Next(ctx)
}

func (p *prefixWarningIterator) Err() error   { return p.inner.Err() }
func (p *prefixWarningIterator) Close() error { return p.inner.Close() }

// createResponsesCompletion uses the /v1/responses endpoint for models
// that don't support /v1/chat/completions.
func (a *client) createResponsesCompletion(
	ctx context.Context, modelName string, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	input, instructions := responsesInputFromMessages(request.Messages)

	// Use per-request tools if provided, otherwise fall back to client-level tools.
	var tools []responses.ToolUnionParam
	if len(request.Tools) > 0 {
		tools = responsesToolsFromModel(request.Tools)
	} else if len(a.config.Tools) > 0 {
		tools = responsesToolsFromModel(a.config.Tools)
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(modelName),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Tools: tools,
	}
	var requestOptions []option.RequestOption

	// Set prompt_cache_key so the provider can cache the tokenized
	// prefix across requests sharing the same conversation. This
	// gives a ~90% discount on repeated input tokens without
	// requiring server-side response storage (ZDR-compatible).
	if request.PromptCacheKey != "" {
		params.PromptCacheKey = param.NewOpt(request.PromptCacheKey)
	}

	if instructions != "" {
		params.Instructions = param.NewOpt(instructions)
	}

	// Reasoning parameters — effort is already normalized by CreateCompletion.
	if SupportsReasoning(modelName) {
		effort := string(request.ReasoningEffort)
		if effort != "" {
			params.Reasoning.Effort = shared.ReasoningEffort(effort)
		}
		summary := string(request.ReasoningSummary)
		if summary == "" {
			summary = a.config.ReasoningSummary
		}
		if summary == "" {
			summary = string(llmapi.ReasoningSummaryAuto)
		}
		if summary != "" {
			params.Reasoning.Summary = shared.ReasoningSummary(summary)
		}
	}

	if !a.config.DisableMaxOutputTokens {
		if request.MaxOutputTokens > 0 {
			params.MaxOutputTokens = param.NewOpt(int64(request.MaxOutputTokens))
		} else if a.config.MaxCompletionTokens > 0 {
			params.MaxOutputTokens = param.NewOpt(int64(a.config.MaxCompletionTokens))
		}
	}

	if a.config.Temperature != 0 {
		params.Temperature = param.NewOpt(a.config.Temperature)
	}
	if a.config.TopP != 0 {
		params.TopP = param.NewOpt(a.config.TopP)
	}
	if a.config.Store != nil {
		params.Store = param.NewOpt(*a.config.Store)
	}
	// Parallel tool calls: per-request override beats provider config.
	if request.ParallelToolCalls != nil {
		params.ParallelToolCalls = param.NewOpt(*request.ParallelToolCalls)
	} else if a.config.DisableParallelToolCalls {
		params.ParallelToolCalls = param.NewOpt(false)
	}
	// Tool choice. The Responses API accepts the "auto"/"none"/"required"
	// literals via OfToolChoiceMode.
	if tc := responsesToolChoice(request.ToolChoice); tc != nil {
		params.ToolChoice = *tc
	}
	// Always include encrypted_content for reasoning models so that
	// reasoning items returned by the provider can be threaded back as
	// input on subsequent turns. This is required when store=false (or
	// for ZDR organizations) and harmless otherwise. See:
	// https://platform.openai.com/docs/guides/reasoning ("Encrypted
	// reasoning items").
	if SupportsReasoning(modelName) {
		params.Include = []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		}
	}
	// Provider-specific session correlation headers. The Codex backend
	// uses these for routing and tracing; OpenAI hosted ignores them.
	if request.PromptCacheKey != "" {
		requestOptions = append(requestOptions,
			option.WithHeader("session_id", request.PromptCacheKey),
			option.WithHeader("x-client-request-id", request.PromptCacheKey),
		)
	}
	if len(a.config.ClientMetadata) > 0 {
		requestOptions = append(requestOptions,
			option.WithJSONSet("client_metadata", a.config.ClientMetadata))
	}

	stream := a.client.Responses.NewStreaming(ctx, params, requestOptions...)

	newStream := func() *ssestream.Stream[responses.ResponseStreamEventUnion] {
		return a.client.Responses.NewStreaming(ctx, params, requestOptions...)
	}

	return &responsesStreamIterator{
		stream:           stream,
		newStream:        newStream,
		midStreamRetries: maxMidStreamRetries,
	}, nil
}

// createChatCompletion uses the /v1/chat/completions endpoint.
func (a *client) createChatCompletion(
	ctx context.Context, modelName string, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, len(request.Messages))
	for i, msg := range request.Messages {
		var err error
		messages[i], err = openAIMessageFromModel(msg)
		if err != nil {
			return nil, err
		}
	}

	// Use per-request tools if provided, otherwise fall back to client-level tools.
	tools := a.tools
	if len(request.Tools) > 0 {
		tools = openAIToolsFromModel(request.Tools)
	}

	// Use per-request response format if provided, otherwise fall back to client-level.
	responseFormat := a.responseFormat
	if request.ResponseFormat != nil {
		responseFormat = openAIResponseFormatFromModel(request.ResponseFormat)
	}

	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    shared.ChatModel(modelName),
		Tools:    tools,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}

	if request.PromptCacheKey != "" {
		params.PromptCacheKey = param.NewOpt(request.PromptCacheKey)
	}

	if responseFormat != nil {
		params.ResponseFormat = *responseFormat
	}

	// Tool routing: per-request ParallelToolCalls/ToolChoice override
	// provider config. ChatCompletion accepts the literal modes via
	// OfAuto on the union; named-tool choices are not exposed.
	if request.ParallelToolCalls != nil {
		params.ParallelToolCalls = param.NewOpt(*request.ParallelToolCalls)
	} else if a.config.DisableParallelToolCalls {
		params.ParallelToolCalls = param.NewOpt(false)
	}
	if tc := chatToolChoice(request.ToolChoice); tc != nil {
		params.ToolChoice = *tc
	}

	if SupportsReasoning(modelName) {
		if request.MaxOutputTokens > 0 {
			params.MaxCompletionTokens = param.NewOpt(int64(request.MaxOutputTokens))
		} else if a.config.MaxCompletionTokens > 0 {
			params.MaxCompletionTokens = param.NewOpt(int64(a.config.MaxCompletionTokens))
		}
		// Effort is already normalized by CreateCompletion.
		effort := string(request.ReasoningEffort)
		if effort != "" {
			params.ReasoningEffort = shared.ReasoningEffort(effort)
		}
	} else {
		if a.config.MaxTokens > 0 {
			params.MaxTokens = param.NewOpt(int64(a.config.MaxTokens))
		}
	}

	// O-series reasoning models don't support temperature or penalty parameters.
	if !IsReasoningModel(modelName) {
		if a.config.FrequencyPenalty != 0 {
			params.FrequencyPenalty = param.NewOpt(a.config.FrequencyPenalty)
		}
		if a.config.PresencePenalty != 0 {
			params.PresencePenalty = param.NewOpt(a.config.PresencePenalty)
		}
		if a.config.Temperature != 0 {
			params.Temperature = param.NewOpt(a.config.Temperature)
		}
		if a.config.TopP != 0 {
			params.TopP = param.NewOpt(a.config.TopP)
		}
	}

	// Capture response headers via middleware for rate limit inspection.
	var capturedHeaders http.Header
	headerMiddleware := option.WithMiddleware(
		func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			resp, err := next(req)
			if resp != nil {
				capturedHeaders = resp.Header.Clone()
				slog.Debug("completion HTTP response",
					"status", resp.StatusCode,
					"content_type", resp.Header.Get("Content-Type"),
				)
			}
			return resp, err
		},
	)

	isAnthropic := isAnthropicURL(a.config.BaseURL)

	// Retry on transient errors (408, 409, 429, 5xx) using the SDK retry
	// package. Warnings are buffered and emitted through the iterator so
	// the UI can display them while the client waits.
	var warnings []llmapi.Event
	var stream *ssestream.Stream[openai.ChatCompletionChunk]

	retryStrategy := retry.CombinedStrategy(
		// LimitStrategy must be first so CombinedStrategy short-circuits
		// before the backoff strategy appends a warning on the final attempt.
		// +1 because LimitStrategy counts total fn calls, not retries.
		retry.LimitStrategy(maxStreamRetries+1),
		retryAfterOrBackoffStrategy(&capturedHeaders, &warnings),
	)

	retryErr := retry.Retry(ctx, retryStrategy, func(_ context.Context) (bool, error) {
		capturedHeaders = nil
		// Disable the SDK's built-in retry so our strategy controls
		// backoff timing and can emit warning events between attempts.
		stream = a.client.Chat.Completions.NewStreaming(ctx, params,
			headerMiddleware, option.WithMaxRetries(0))

		// The SDK stores initial HTTP errors (e.g. 429) in the stream
		// immediately — Err() is available without calling Next().
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
			// Non-retryable error — no warnings to emit, return directly.
			if stream != nil {
				_ = stream.Close()
			}
			if isToolCallParseError(retryErr) {
				return nil, &ToolCallParseError{Cause: retryErr}
			}
			return nil, retryErr
		}
		// Retries exhausted — return the broken stream so buffered retry
		// warnings are emitted to the caller before the error surfaces.
	}

	// Build stream factory for mid-stream retries.
	newStream := func() *ssestream.Stream[openai.ChatCompletionChunk] {
		capturedHeaders = nil
		return a.client.Chat.Completions.NewStreaming(ctx, params,
			headerMiddleware, option.WithMaxRetries(0))
	}

	return &completionStreamIterator{
		stream:           stream,
		pendingWarnings:  warnings,
		capturedHeaders:  capturedHeaders,
		isAnthropic:      isAnthropic,
		newStream:        newStream,
		midStreamRetries: maxMidStreamRetries,
	}, nil
}

func (a *client) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	enc := a.encoderFor(model.Name)
	var ret int
	for _, message := range msgs {
		ret += enc.tokensPerMessage
		ret += len(enc.tk.Encode(message.Content, nil, nil))
		ret += len(enc.tk.Encode(message.ReasoningContent, nil, nil))
		ret += len(enc.tk.Encode(string(message.Role), nil, nil))
		ret += len(enc.tk.Encode(message.Name, nil, nil))
		if message.Name != "" {
			ret += enc.tokensPerName
		}
		// Count tool calls in assistant messages (function name, arguments, ID).
		for _, tc := range message.ToolCalls {
			ret += len(enc.tk.Encode(tc.Function.Name, nil, nil))
			ret += len(enc.tk.Encode(tc.Function.Arguments, nil, nil))
			ret += len(enc.tk.Encode(tc.ID, nil, nil))
			ret += 3 // per-tool-call structural overhead
		}
		// Count tool call ID in tool result messages.
		if message.ToolCallID != "" {
			ret += len(enc.tk.Encode(message.ToolCallID, nil, nil))
		}
	}
	ret += 3
	return ret, nil
}

// Models advertises the static OpenAI model catalog. The router typically
// supplies this via llmrouter.WithModels; this implementation lets the
// service double as a direct Service when no router is involved.
func (a *client) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice(ModelEntries())
}

// GetModel scans the static model catalog for the given model name.
func (a *client) GetModel(_ context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	for _, e := range ModelEntries() {
		if e.Name == model.Name {
			return e, nil
		}
	}
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// streamState tracks the streaming state machine.
type streamState int

const (
	streamStateStreaming streamState = iota
	streamStateEmitDone
	streamStateCheckHeaders
	streamStateDone
)

// maxMidStreamRetries is the maximum number of retry attempts when a
// transient network error occurs after the stream has already started
// delivering events.
const maxMidStreamRetries = 3

// completionStreamIterator wraps the official SDK's SSE stream and emits
// typed llmapi.Event values. It uses ChatCompletionAccumulator to track state.
type completionStreamIterator struct {
	stream *ssestream.Stream[openai.ChatCompletionChunk]
	acc    openai.ChatCompletionAccumulator
	state  streamState

	// Track reasoning content ourselves since the SDK doesn't accumulate it.
	reasoningContent strings.Builder
	err              error

	// Rate limit support.
	pendingWarnings []llmapi.Event // warnings buffered during retries
	warningIdx      int            // index into pendingWarnings
	capturedHeaders http.Header    // response headers from the last HTTP response
	isAnthropic     bool           // true when base URL contains "anthropic.com"

	// Mid-stream retry support.
	newStream        func() *ssestream.Stream[openai.ChatCompletionChunk] // factory to recreate stream
	midStreamRetries int                                                  // remaining mid-stream retry attempts
	retryEvents      []llmapi.Event                                       // buffered events emitted during a mid-stream retry
	retryEventIdx    int                                                  // index into retryEvents
}

func (s *completionStreamIterator) Next(ctx context.Context) (llmapi.Event, bool) {
	// Emit buffered warnings from retries before yielding stream events.
	if s.warningIdx < len(s.pendingWarnings) {
		ev := s.pendingWarnings[s.warningIdx]
		s.warningIdx++
		return ev, true
	}

	// Emit buffered mid-stream retry events.
	if s.retryEventIdx < len(s.retryEvents) {
		ev := s.retryEvents[s.retryEventIdx]
		s.retryEventIdx++
		return ev, true
	}

	for {
		switch s.state {
		case streamStateDone:
			return llmapi.Event{}, false

		case streamStateCheckHeaders:
			s.state = streamStateDone
			if s.capturedHeaders != nil {
				if s.isAnthropic {
					if warning, ok := checkAnthropicRateLimitHeaders(s.capturedHeaders); ok {
						return warning, true
					}
				} else {
					if warning, ok := checkStandardRateLimitHeaders(s.capturedHeaders); ok {
						return warning, true
					}
				}
			}
			return llmapi.Event{}, false

		case streamStateEmitDone:
			s.state = streamStateCheckHeaders
			return s.buildDoneEvent(), true

		case streamStateStreaming:
			if !s.stream.Next() {
				if err := s.stream.Err(); err != nil {
					// Attempt mid-stream retry on transient network or stream errors.
					retryable := isTransientNetworkError(err) || isRetryableStreamError(err)
					if s.newStream != nil && s.midStreamRetries > 0 && retryable {
						s.midStreamRetries--
						_ = s.stream.Close()

						attempt := maxMidStreamRetries - s.midStreamRetries
						wait := retryWait(nil, attempt-1)
						slog.Warn("mid-stream retryable error, retrying",
							"error", err, "attempt", attempt, "wait", wait)

						time.Sleep(wait)
						s.stream = s.newStream()

						// Reset accumulator state.
						s.acc = openai.ChatCompletionAccumulator{}
						s.reasoningContent.Reset()

						// Buffer reset + warning events.
						var msg string
						if isTransientNetworkError(err) {
							msg = retryNetworkMessage(err, wait, attempt, maxMidStreamRetries)
						} else {
							msg = retryStreamMessage(err, wait, attempt, maxMidStreamRetries)
						}
						s.retryEvents = []llmapi.Event{
							{Type: llmapi.EventStreamReset},
							{Type: llmapi.EventRateLimitWarning, RateLimit: &llmapi.RateLimitInfo{
								WaitDuration: wait,
								Message:      msg,
							}},
						}
						s.retryEventIdx = 1
						return s.retryEvents[0], true
					}

					s.state = streamStateDone
					if isToolCallParseError(err) {
						err = &ToolCallParseError{Cause: err}
					}
					s.err = err
					var textLen, toolCalls int
					if len(s.acc.Choices) > 0 {
						textLen = len(s.acc.Choices[0].Message.Content)
						toolCalls = len(s.acc.Choices[0].Message.ToolCalls)
					}
					slog.Warn("completion stream error",
						"error", err,
						"accumulated_text_len", textLen,
						"accumulated_reasoning_len", s.reasoningContent.Len(),
						"accumulated_tool_calls", toolCalls,
						"prompt_tokens", s.acc.Usage.PromptTokens,
						"completion_tokens", s.acc.Usage.CompletionTokens,
					)
					return llmapi.Event{Type: llmapi.EventStreamError, Error: err}, true
				}
				// Stream exhausted normally — emit done, then check headers.
				s.state = streamStateCheckHeaders
				return s.buildDoneEvent(), true
			}

			chunk := s.stream.Current()
			s.acc.AddChunk(chunk)

			// Check for reasoning content in ExtraFields.
			if len(chunk.Choices) > 0 {
				if rc, ok := chunk.Choices[0].Delta.JSON.ExtraFields["reasoning_content"]; ok && rc.Valid() {
					raw := rc.Raw()
					// Strip JSON string quotes.
					var text string
					if err := json.Unmarshal([]byte(raw), &text); err == nil && text != "" {
						s.reasoningContent.WriteString(text)
						return llmapi.Event{Type: llmapi.EventReasoningDelta, Reasoning: text}, true
					}
				}
			}

			// Check for text content delta.
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				return llmapi.Event{
					Type: llmapi.EventTextDelta,
					Text: chunk.Choices[0].Delta.Content,
				}, true
			}

			// Check for just-finished tool call.
			if tc, ok := s.acc.JustFinishedToolCall(); ok {
				return llmapi.Event{
					Type: llmapi.EventToolCallDone,
					ToolCall: &llmapi.ToolCall{
						ID:   tc.ID,
						Type: llmapi.ToolTypeFunction,
						Function: llmapi.FunctionCall{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					},
				}, true
			}

			// Chunk had no actionable content — continue reading.
			continue
		}
	}
}

func (s *completionStreamIterator) buildDoneEvent() llmapi.Event {
	msg := llmapi.Message{
		Role:             llmapi.RoleAssistant,
		ReasoningContent: s.reasoningContent.String(),
	}

	if len(s.acc.Choices) > 0 {
		msg.Content = s.acc.Choices[0].Message.Content

		// Extract accumulated reasoning from the final message's ExtraFields.
		if msg.ReasoningContent == "" {
			if rc, ok := s.acc.Choices[0].Message.JSON.ExtraFields["reasoning_content"]; ok && rc.Valid() {
				var text string
				if json.Unmarshal([]byte(rc.Raw()), &text) == nil {
					msg.ReasoningContent = text
				}
			}
		}

		// Build tool calls from the accumulated message (source of truth).
		for _, tc := range s.acc.Choices[0].Message.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			msg.ToolCalls = append(msg.ToolCalls, llmapi.ToolCall{
				ID:   tc.ID,
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
	}

	var finishReason llmapi.FinishReason
	if len(s.acc.Choices) > 0 {
		finishReason = llmapi.FinishReason(s.acc.Choices[0].FinishReason)
	}

	usage := llmapi.Usage{
		TokensSent:     int(s.acc.Usage.PromptTokens),
		TokensReceived: int(s.acc.Usage.CompletionTokens),
	}
	// Extract detailed token info from ExtraFields if available.
	if ctd, ok := s.acc.Usage.JSON.ExtraFields["completion_tokens_details"]; ok && ctd.Valid() {
		var details struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}
		if json.Unmarshal([]byte(ctd.Raw()), &details) == nil {
			usage.TokensReasoned = details.ReasoningTokens
		}
	}
	if ptd, ok := s.acc.Usage.JSON.ExtraFields["prompt_tokens_details"]; ok && ptd.Valid() {
		var details struct {
			CachedTokens int `json:"cached_tokens"`
		}
		if json.Unmarshal([]byte(ptd.Raw()), &details) == nil {
			usage.TokensCached = details.CachedTokens
		}
	}

	return llmapi.Event{
		Type: llmapi.EventStreamDone,
		DoneData: &llmapi.DoneData{
			Message:      msg,
			FinishReason: finishReason,
			Usage:        usage,
		},
	}
}

func (s *completionStreamIterator) Err() error {
	return s.err
}

func (s *completionStreamIterator) Close() error {
	return s.stream.Close()
}

// responsesStreamIterator wraps the Responses API SSE stream and emits
// typed llmapi.Event values.
type responsesStreamIterator struct {
	stream *ssestream.Stream[responses.ResponseStreamEventUnion]
	state  streamState

	// Accumulate content and tool calls across streaming events.
	textContent      strings.Builder
	reasoningContent strings.Builder
	toolCalls        []llmapi.ToolCall
	// Track in-flight function call arguments by output_index.
	pendingCalls map[int64]*llmapi.ToolCall
	// providerItems carries opaque provider-specific output items (e.g.
	// reasoning items with encrypted_content) captured in stream order.
	// These must be threaded back into the next request to maintain
	// stateful continuity for stateless backends like the ChatGPT Codex
	// store=false flow.
	providerItems []json.RawMessage
	// Final response (set by response.completed event).
	finalResponse *responses.Response
	err           error

	// Mid-stream retry support.
	newStream        func() *ssestream.Stream[responses.ResponseStreamEventUnion]
	midStreamRetries int
	retryEvents      []llmapi.Event
	retryEventIdx    int
}

func (s *responsesStreamIterator) Next(ctx context.Context) (llmapi.Event, bool) {
	// Emit buffered mid-stream retry events.
	if s.retryEventIdx < len(s.retryEvents) {
		ev := s.retryEvents[s.retryEventIdx]
		s.retryEventIdx++
		return ev, true
	}

	for {
		switch s.state {
		case streamStateDone:
			return llmapi.Event{}, false

		case streamStateEmitDone:
			s.state = streamStateDone
			return s.buildDoneEvent(), true

		case streamStateStreaming:
			if !s.stream.Next() {
				if err := s.stream.Err(); err != nil {
					// Attempt mid-stream retry on transient network errors.
					if s.newStream != nil && s.midStreamRetries > 0 && isTransientNetworkError(err) {
						s.midStreamRetries--
						_ = s.stream.Close()

						attempt := maxMidStreamRetries - s.midStreamRetries
						wait := retryWait(nil, attempt-1)
						slog.Warn("mid-stream transient error (responses), retrying",
							"error", err, "attempt", attempt, "wait", wait)

						time.Sleep(wait)
						s.stream = s.newStream()

						// Reset accumulator state.
						s.textContent.Reset()
						s.reasoningContent.Reset()
						s.toolCalls = nil
						s.pendingCalls = nil
						s.providerItems = nil
						s.finalResponse = nil

						// Buffer reset + warning events.
						msg := retryNetworkMessage(err, wait, attempt, maxMidStreamRetries)
						s.retryEvents = []llmapi.Event{
							{Type: llmapi.EventStreamReset},
							{Type: llmapi.EventRateLimitWarning, RateLimit: &llmapi.RateLimitInfo{
								WaitDuration: wait,
								Message:      msg,
							}},
						}
						s.retryEventIdx = 1
						return s.retryEvents[0], true
					}

					s.state = streamStateDone
					s.err = err
					slog.Warn("responses stream error",
						"error", err,
						"accumulated_text_len", s.textContent.Len(),
						"accumulated_reasoning_len", s.reasoningContent.Len(),
						"accumulated_tool_calls", len(s.toolCalls),
					)
					return llmapi.Event{Type: llmapi.EventStreamError, Error: err}, true
				}
				// Stream ended without a completed event — emit done with
				// whatever we've accumulated.
				s.state = streamStateDone
				return s.buildDoneEvent(), true
			}

			event := s.stream.Current()

			switch event.Type {
			case "response.output_text.delta":
				s.textContent.WriteString(event.Delta)
				return llmapi.Event{Type: llmapi.EventTextDelta, Text: event.Delta}, true

			case "response.reasoning_text.delta",
				"response.reasoning_summary_text.delta":
				s.reasoningContent.WriteString(event.Delta)
				return llmapi.Event{Type: llmapi.EventReasoningDelta, Reasoning: event.Delta}, true

			case "response.function_call_arguments.delta":
				// Accumulate arguments for an in-flight function call.
				if s.pendingCalls == nil {
					s.pendingCalls = make(map[int64]*llmapi.ToolCall)
				}
				tc, ok := s.pendingCalls[event.OutputIndex]
				if !ok {
					tc = &llmapi.ToolCall{
						ID:   event.ItemID,
						Type: llmapi.ToolTypeFunction,
					}
					s.pendingCalls[event.OutputIndex] = tc
				}
				tc.Function.Arguments += event.Delta

			case "response.function_call_arguments.done":
				// Function call is complete.
				if s.pendingCalls != nil {
					if tc, ok := s.pendingCalls[event.OutputIndex]; ok {
						tc.Function.Arguments = event.Arguments
						delete(s.pendingCalls, event.OutputIndex)
					}
				}

			case "response.output_item.done":
				// An output item is complete. Check if it's a function call.
				item := event.Item
				// Preserve the raw JSON of items the provider may need to
				// see threaded back as input on subsequent turns. We keep
				// reasoning, message, and function_call items so the full
				// assistant turn can be faithfully replayed for stateless
				// backends (e.g. ChatGPT Codex with store=false).
				switch item.Type {
				case "reasoning", "message", "function_call":
					if raw := item.RawJSON(); raw != "" {
						s.providerItems = append(s.providerItems, json.RawMessage(raw))
					}
				}
				if item.Type == "function_call" {
					tc := llmapi.ToolCall{
						ID:   item.CallID,
						Type: llmapi.ToolTypeFunction,
						Function: llmapi.FunctionCall{
							Name:      item.Name,
							Arguments: item.Arguments,
						},
					}
					s.toolCalls = append(s.toolCalls, tc)
					return llmapi.Event{Type: llmapi.EventToolCallDone, ToolCall: &tc}, true
				}

			case "response.completed":
				s.finalResponse = &event.Response
				s.state = streamStateDone
				return s.buildDoneEvent(), true

			case "error":
				s.state = streamStateDone
				s.err = fmt.Errorf("openai responses API error: %s", event.Message)
				return llmapi.Event{
					Type:  llmapi.EventStreamError,
					Error: s.err,
				}, true

			case "response.failed":
				s.state = streamStateDone
				errMsg := "response failed"
				if event.Response.Error.Message != "" {
					errMsg = event.Response.Error.Message
				}
				s.err = fmt.Errorf("openai responses API: %s", errMsg)
				return llmapi.Event{
					Type:  llmapi.EventStreamError,
					Error: s.err,
				}, true
			}
			continue
		}
	}
}

func (s *responsesStreamIterator) buildDoneEvent() llmapi.Event {
	msg := llmapi.Message{
		Role:             llmapi.RoleAssistant,
		Content:          s.textContent.String(),
		ReasoningContent: s.reasoningContent.String(),
		ToolCalls:        s.toolCalls,
		ProviderItems:    s.providerItems,
	}

	var finishReason llmapi.FinishReason
	if len(s.toolCalls) > 0 {
		finishReason = llmapi.FinishReasonToolCall
	} else {
		finishReason = llmapi.FinishReasonStop
	}

	var usage llmapi.Usage
	if s.finalResponse != nil {
		// Check status for incomplete finish.
		switch s.finalResponse.Status {
		case "incomplete":
			finishReason = llmapi.FinishReasonLength
		case "failed":
			finishReason = llmapi.FinishReasonContentFilter
		}

		usage = llmapi.Usage{
			TokensSent:     int(s.finalResponse.Usage.InputTokens),
			TokensReceived: int(s.finalResponse.Usage.OutputTokens),
			TokensReasoned: int(s.finalResponse.Usage.OutputTokensDetails.ReasoningTokens),
			TokensCached:   int(s.finalResponse.Usage.InputTokensDetails.CachedTokens),
		}

		// If we didn't accumulate text from deltas, extract from the final response.
		if msg.Content == "" {
			for _, item := range s.finalResponse.Output {
				if item.Type == "message" {
					for _, content := range item.Content {
						if content.Type == "output_text" {
							msg.Content += content.Text
						}
					}
				}
			}
		}
	}

	return llmapi.Event{
		Type: llmapi.EventStreamDone,
		DoneData: &llmapi.DoneData{
			Message:      msg,
			FinishReason: finishReason,
			Usage:        usage,
		},
	}
}

func (s *responsesStreamIterator) Err() error {
	return s.err
}

func (s *responsesStreamIterator) Close() error {
	return s.stream.Close()
}
