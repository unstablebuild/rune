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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
	"github.com/aws/smithy-go/logging"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/ratelimit"
)

// DefaultRegion is used when neither the configuration nor the AWS
// environment names a region. Bedrock has no global endpoint, so a request
// without a region cannot be signed at all.
const DefaultRegion = "us-east-1"

// bearerAuthScheme is the auth-scheme preference that makes the client
// authenticate with a Bedrock API key instead of SigV4.
const bearerAuthScheme = "httpBearerAuth"

// credentialProbeTimeout bounds CredentialChainStatus, which may reach out
// to the instance metadata service.
const credentialProbeTimeout = 3 * time.Second

// Config holds the configuration for the Bedrock client.
type Config struct {
	// Region is the AWS region to invoke. When empty the AWS environment
	// (AWS_REGION, profile) decides, falling back to DefaultRegion.
	Region string
	// Profile selects a named profile from the shared AWS config files.
	Profile string
	// BaseURL overrides the Bedrock runtime endpoint, e.g. a VPC endpoint
	// or an inference gateway. It applies to model invocation only: such
	// endpoints do not serve the control plane, so the live model catalog
	// is disabled and the static catalog answers instead.
	BaseURL string

	MaxTokens   int
	Temperature float64
	TopP        float64

	// ReasoningEffort maps onto the Claude extended thinking budget.
	ReasoningEffort string
	// ResponseFormat, when set, requests structured JSON output. A
	// request-level format takes precedence.
	ResponseFormat *llmapi.ResponseFormat
	// CacheControl enables prompt caching when set to any value other than
	// "" or "none".
	CacheControl string

	DebugHTTP bool
}

// converseStreamAPI is the subset of the Bedrock runtime client the
// provider uses. Tests substitute a stub that returns an event stream built
// with bedrockruntime.NewConverseStreamEventStream.
type converseStreamAPI interface {
	ConverseStream(
		ctx context.Context, in *bedrockruntime.ConverseStreamInput,
	) (*bedrockruntime.ConverseStreamEventStream, error)
	CountTokens(
		ctx context.Context, in *bedrockruntime.CountTokensInput,
	) (*bedrockruntime.CountTokensOutput, error)
}

type client struct {
	config Config
	api    converseStreamAPI
	// control is the Bedrock control plane used to list the models this
	// account can actually invoke. Nil when Bedrock is not configured, in
	// which case Models() answers from the static catalog.
	control controlPlaneAPI
	// catalogOnce guards the one live catalog query per client. The router
	// shares a client across goroutines, so the result is memoised.
	catalogOnce    sync.Once
	catalogEntries []llmapi.ModelEntry
	catalogErr     error
	// initErr is surfaced lazily from CreateCompletion/CountTokens so the
	// NewClient(...) llmapi.Service seam matches the other providers, which
	// never fail at construction time.
	initErr error
}

func (c *client) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (c *client) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

// runtimeAPI adapts the generated Bedrock runtime client to
// converseStreamAPI, unwrapping the operation output down to the event
// stream the iterator drains.
type runtimeAPI struct{ c *bedrockruntime.Client }

func (a runtimeAPI) ConverseStream(
	ctx context.Context, in *bedrockruntime.ConverseStreamInput,
) (*bedrockruntime.ConverseStreamEventStream, error) {
	out, err := a.c.ConverseStream(ctx, in)
	if err != nil {
		return nil, err
	}
	return out.GetStream(), nil
}

func (a runtimeAPI) CountTokens(
	ctx context.Context, in *bedrockruntime.CountTokensInput,
) (*bedrockruntime.CountTokensOutput, error) {
	return a.c.CountTokens(ctx, in)
}

// NewClient returns an llmapi.Service backed by the Bedrock ConverseStream
// API. When apiKey is set the client authenticates with it as a bearer
// token; otherwise it falls back to the standard AWS credential chain
// (environment, shared profile, SSO, IMDS) and signs requests with SigV4.
func NewClient(apiKey string, config Config) llmapi.Service {
	awsCfg, err := loadAWSConfig(config)
	if err != nil {
		return &client{config: config, initErr: err}
	}
	// Listing models is a network round-trip, and Models() is called on
	// interactive paths. Only reach for the control plane when Bedrock is
	// actually configured; otherwise the static catalog answers instantly.
	// A custom runtime endpoint also disables it: gateways and VPC runtime
	// endpoints do not serve the control-plane API.
	liveCatalog := config.BaseURL == "" &&
		(apiKey != "" || config.Profile != "" || awsCfg.Region != "")
	awsCfg = withRegion(awsCfg, DefaultRegion)

	c := &client{
		config: config,
		api: runtimeAPI{c: bedrockruntime.NewFromConfig(
			awsCfg, bearerAuth(apiKey), runtimeEndpoint(config.BaseURL))},
	}
	if liveCatalog {
		c.control = bedrock.NewFromConfig(awsCfg, controlPlaneBearerAuth(apiKey))
	}
	return c
}

// runtimeEndpoint overrides the Bedrock runtime endpoint when configured.
func runtimeEndpoint(baseURL string) func(*bedrockruntime.Options) {
	return func(o *bedrockruntime.Options) {
		if baseURL != "" {
			o.BaseEndpoint = aws.String(baseURL)
		}
	}
}

func loadAWSConfig(config Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if config.Region != "" {
		opts = append(opts, awsconfig.WithRegion(config.Region))
	}
	if config.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(config.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("bedrock: load aws config: %w", err)
	}
	if config.DebugHTTP {
		awsCfg.ClientLogMode = aws.LogRequest | aws.LogResponse | aws.LogRetries
		awsCfg.Logger = logging.LoggerFunc(
			func(_ logging.Classification, format string, v ...any) {
				slog.Debug("bedrock HTTP", "detail", fmt.Sprintf(format, v...))
			})
	}
	return awsCfg, nil
}

// withRegion fills in a fallback region. Bedrock has no global endpoint, so
// a request without a region cannot be signed at all.
func withRegion(cfg aws.Config, fallback string) aws.Config {
	if cfg.Region == "" {
		cfg.Region = fallback
	}
	return cfg
}

// bearerAuth installs a Bedrock API key as a static bearer token. This is
// the programmatic equivalent of the AWS_BEARER_TOKEN_BEDROCK environment
// variable the SDK already understands.
func bearerAuth(apiKey string) func(*bedrockruntime.Options) {
	return func(o *bedrockruntime.Options) {
		if apiKey == "" {
			return
		}
		o.BearerAuthTokenProvider = bearer.TokenProviderFunc(
			func(context.Context) (bearer.Token, error) {
				return bearer.Token{Value: apiKey}, nil
			})
		o.AuthSchemePreference = []string{bearerAuthScheme}
	}
}

// controlPlaneBearerAuth mirrors bearerAuth for the control-plane client.
func controlPlaneBearerAuth(apiKey string) func(*bedrock.Options) {
	return func(o *bedrock.Options) {
		if apiKey == "" {
			return
		}
		o.BearerAuthTokenProvider = bearer.TokenProviderFunc(
			func(context.Context) (bearer.Token, error) {
				return bearer.Token{Value: apiKey}, nil
			})
		o.AuthSchemePreference = []string{bearerAuthScheme}
	}
}

// CredentialChainStatus reports whether the standard AWS credential chain
// can sign Bedrock requests, and names the source that supplied them. It is
// used to tell the user whether Bedrock works without a stored API key.
func CredentialChainStatus(ctx context.Context, config Config) (source string, ok bool) {
	awsCfg, err := loadAWSConfig(config)
	if err != nil || awsCfg.Credentials == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, credentialProbeTimeout)
	defer cancel()
	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil || !creds.HasKeys() {
		return "", false
	}
	return creds.Source, true
}

// CreateCompletion starts a streaming completion through ConverseStream.
func (c *client) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, request llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if c.initErr != nil {
		return nil, c.initErr
	}

	// Context-window guard with a 5% safety margin. Prefer the
	// caller-supplied token count to avoid a redundant CountTokens
	// round-trip on every turn.
	if model.ContextWindow > 0 {
		count := request.TokenCount
		if count == 0 {
			if n, err := c.CountTokens(model, request.Messages); err == nil {
				count = n
			}
		}
		if count > 0 && count > int(float64(model.ContextWindow)*0.95) {
			return nil, &llmapi.ErrContextWindowExceeded{Count: count, Max: model.ContextWindow}
		}
	}

	effort := string(request.ReasoningEffort)
	// Only an explicit per-request effort warrants a warning when the model
	// cannot honor it. The config value is a standing cross-model preference,
	// so it is dropped silently.
	explicit := effort != ""
	if !explicit {
		effort = c.config.ReasoningEffort
	}
	normalized, effortWarn := NormalizeEffort(model.Name, effort)
	request.ReasoningEffort = llmapi.ReasoningEffort(normalized)

	var warnings []llmapi.Event
	if explicit && effortWarn != "" {
		warnings = append(warnings, llmapi.Event{
			Type:      llmapi.EventRateLimitWarning,
			RateLimit: &llmapi.RateLimitInfo{Message: effortWarn},
		})
	}

	in := converseInput(model.Name, request, c.config)
	// The stream is dialled lazily on the first Next call; see
	// streamIterator.connect for the initial-connect retry policy.
	return &streamIterator{
		newStream: func(ctx context.Context) (*bedrockruntime.ConverseStreamEventStream, error) {
			return c.api.ConverseStream(ctx, in)
		},
		pendingWarnings:  warnings,
		midStreamRetries: ratelimit.MaxMidStreamRetries,
	}, nil
}

// CountTokens returns the token count for the given messages using the
// Bedrock CountTokens API. Not every model or region supports it, so a
// character-based estimate is used when the call fails.
func (c *client) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	if c.initErr != nil {
		return 0, c.initErr
	}
	system, messages := convertMessages(msgs)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := c.api.CountTokens(ctx, &bedrockruntime.CountTokensInput{
		ModelId: aws.String(model.Name),
		Input: &types.CountTokensInputMemberConverse{
			Value: types.ConverseTokensRequest{
				Messages: messages,
				System:   system,
			},
		},
	})
	if err != nil {
		slog.Debug("bedrock CountTokens unavailable, falling back to estimate", "error", err)
		return estimateTokens(msgs), nil
	}
	return int(deref(out.InputTokens)), nil
}

// estimateTokens provides a rough character-based token estimate used when
// the CountTokens API is unavailable.
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

// Models lists the models this account can invoke, falling back to the
// static catalog when the control plane cannot be queried.
func (c *client) Models() iterator.Iterator[llmapi.ModelEntry] {
	if c.control == nil {
		return iterator.FromSlice(ModelEntries())
	}
	return &catalogIterator{list: c.cachedCatalog, fallback: ModelEntries()}
}

// cachedCatalog queries the control plane once per client. Callers list
// models on interactive paths (bootstrap, model pickers), so the round-trip
// is capped and its result reused rather than repeated per listing.
func (c *client) cachedCatalog(ctx context.Context) ([]llmapi.ModelEntry, error) {
	c.catalogOnce.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
		defer cancel()
		c.catalogEntries, c.catalogErr = listCatalog(ctx, c.control)
	})
	return c.catalogEntries, c.catalogErr
}

// GetModel scans the model catalog for the given model name.
func (c *client) GetModel(ctx context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	it := c.Models()
	defer func() { _ = it.Close() }()
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
		}
		if entry.Name == model.Name {
			return entry, nil
		}
	}
}

// isRetryableError reports whether err represents a transient Bedrock
// failure that is worth retrying.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var throttling *types.ThrottlingException
	var unavailable *types.ServiceUnavailableException
	var internal *types.InternalServerException
	var timeout *types.ModelTimeoutException
	var notReady *types.ModelNotReadyException
	var streamErr *types.ModelStreamErrorException
	switch {
	case errors.As(err, &throttling),
		errors.As(err, &unavailable),
		errors.As(err, &internal),
		errors.As(err, &timeout),
		errors.As(err, &notReady),
		errors.As(err, &streamErr):
		return true
	}
	return ratelimit.IsTransientNetworkError(err)
}

// Verify at compile time that client implements llmapi.Service.
var _ llmapi.Service = (*client)(nil)

// ClientConstructor is the function signature for creating a Bedrock
// llmapi.Service. Tests may substitute a stub.
type ClientConstructor func(apiKey string, cfg Config) llmapi.Service

// Verify NewClient matches ClientConstructor signature.
var _ ClientConstructor = NewClient
