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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// parseProviderModel splits the "provider/model" argument used by every
// llm sub-command. The argument must contain exactly one '/' separator;
// both halves must be non-empty.
func parseProviderModel(s string) (provider, model string, err error) {
	idx := strings.IndexByte(s, '/')
	if idx <= 0 || idx == len(s)-1 {
		return "", "", fmt.Errorf(
			"expected provider/model, got %q", s,
		)
	}
	return s[:idx], s[idx+1:], nil
}

// resolveModel looks up the canonical ModelEntry for the given
// provider/model string. If the host doesn't know the model (e.g.
// arbitrary ollama tags), it falls back to a synthetic entry so
// downstream calls still reach the provider.
func resolveModel(
	ctx context.Context, svc llmapi.Service, arg string,
) (llmapi.ModelEntry, error) {
	entry, err := lookupModel(ctx, svc, arg)
	if errors.Is(err, llmapi.ErrModelNotFound) {
		// lookupModel returns a synthetic entry for unknown models
		// (e.g. arbitrary ollama tags); use it as-is.
		return entry, nil
	}
	return entry, err
}

// lookupModel returns the canonical entry for arg. When the model is
// not known to the service it returns a synthetic entry built from arg
// together with llmapi.ErrModelNotFound, so callers can either fall
// back to the synthetic entry or surface the error.
func lookupModel(
	ctx context.Context, svc llmapi.Service, arg string,
) (llmapi.ModelEntry, error) {
	provider, name, err := parseProviderModel(arg)
	if err != nil {
		return llmapi.ModelEntry{}, err
	}
	query := llmapi.ModelEntry{Name: name, Provider: provider}
	entry, err := svc.GetModel(ctx, query)
	if err != nil {
		if errors.Is(err, llmapi.ErrModelNotFound) {
			return query, err
		}
		return llmapi.ModelEntry{}, err
	}
	return entry, nil
}

// loadMessageBody returns the literal message when arg does not
// reference an existing file on disk; otherwise it reads the file.
func loadMessageBody(arg string) (string, error) {
	st, err := os.Stat(arg)
	if err != nil || st.IsDir() {
		// Not a regular file: treat as literal text.
		return arg, nil
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return "", fmt.Errorf("read message: %w", err)
	}
	return string(data), nil
}

func newLLMCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "Talk to the workspace's LLM router",
	}
	cmd.AddCommand(
		newLLMListCmd(a),
		newLLMInfoCmd(a),
		newLLMCountCmd(a),
		newLLMMessageCmd(a),
	)
	return cmd
}

type flatModelEntry struct {
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window"`
	BaseURL       string `json:"base_url,omitempty"`
	ProjectorPath string `json:"projector_path,omitempty"`
}

func flattenModelEntry(m llmapi.ModelEntry) flatModelEntry {
	return flatModelEntry{
		Provider:      m.Provider,
		Name:          m.Name,
		ContextWindow: m.ContextWindow,
		BaseURL:       m.BaseURL,
		ProjectorPath: m.ProjectorPath,
	}
}

func modelIterToGeneric(
	mit iterator.Iterator[llmapi.ModelEntry],
) iterator.Iterator[flatModelEntry] {
	return iterator.FromFunc(
		func(ctx context.Context) (flatModelEntry, bool, error) {
			m, ok := mit.Next(ctx)
			if !ok {
				return flatModelEntry{}, false, mit.Err()
			}
			return flattenModelEntry(m), true, nil
		},
		mit.Close,
	)
}

func newLLMListCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all known model entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			svc := w.LLM(cmd.Context())
			mit := svc.Models()
			defer func() { _ = mit.Close() }()
			it := modelIterToGeneric(mit)
			defer func() { _ = it.Close() }()
			if format == "" {
				ctx := cmd.Context()
				for {
					m, ok := it.Next(ctx)
					if !ok {
						return it.Err()
					}
					fmt.Printf("%s/%s\t%d\n",
						m.Provider, m.Name, m.ContextWindow)
				}
			}
			return printIterator(cmd.Context(), format, it, []string{
				"Provider", "Name", "ContextWindow", "BaseURL", "ProjectorPath",
			})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	return cmd
}

func newLLMInfoCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "info <provider>/<model>",
		Short: "Show the canonical entry for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			svc := w.LLM(cmd.Context())
			entry, err := lookupModel(cmd.Context(), svc, args[0])
			if errors.Is(err, llmapi.ErrModelNotFound) {
				return fmt.Errorf("model %q not found", args[0])
			}
			if err != nil {
				return err
			}
			flat := flattenModelEntry(entry)
			return printResult(cmd.Context(), format, flat,
				func(v flatModelEntry) {
					fmt.Printf("provider:       %s\n", v.Provider)
					fmt.Printf("name:           %s\n", v.Name)
					fmt.Printf("context_window: %d\n", v.ContextWindow)
					if v.BaseURL != "" {
						fmt.Printf("base_url:       %s\n", v.BaseURL)
					}
					if v.ProjectorPath != "" {
						fmt.Printf("projector_path: %s\n", v.ProjectorPath)
					}
				},
				[]string{
					"Provider", "Name", "ContextWindow",
					"BaseURL", "ProjectorPath",
				})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	return cmd
}

func newLLMCountCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "count <provider>/<model> <message-or-file>",
		Short: "Count tokens for a single user message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			svc := w.LLM(cmd.Context())
			entry, err := resolveModel(cmd.Context(), svc, args[0])
			if err != nil {
				return err
			}
			body, err := loadMessageBody(args[1])
			if err != nil {
				return err
			}
			msgs := []llmapi.Message{{
				Role:    llmapi.RoleUser,
				Content: body,
			}}
			count, err := svc.CountTokens(entry, msgs)
			if err != nil {
				return err
			}
			type result struct {
				Count int `json:"count"`
			}
			return printResult(cmd.Context(), format, result{Count: count},
				func(v result) { fmt.Println(v.Count) },
				[]string{"Count"})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	return cmd
}

// parseResponseFormat turns a --response-format value into an
// llmapi.ResponseFormat. Accepted shapes:
//   - "" → nil (provider default)
//   - "text" → ResponseFormatTypeText
//   - "json" → ResponseFormatTypeJSONObject
//   - valid JSON object → ResponseFormatTypeJSONSchema with the JSON
//     object parsed as a JSON schema. If parsing fails, returns an error.
func parseResponseFormat(s string) (*llmapi.ResponseFormat, error) {
	if s == "" {
		return nil, nil
	}
	switch s {
	case "text":
		return &llmapi.ResponseFormat{Type: llmapi.ResponseFormatTypeText}, nil
	case "json":
		return &llmapi.ResponseFormat{Type: llmapi.ResponseFormatTypeJSONObject}, nil
	}
	// Anything else must parse as a JSON object and is used as the
	// JSON schema.
	var probe map[string]any
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		return nil, fmt.Errorf("--response-format: %w", err)
	}
	name, _ := probe["name"].(string)
	if name == "" {
		name = "response"
	}
	description, _ := probe["description"].(string)
	strict, _ := probe["strict"].(bool)
	var schemaBytes []byte
	if v, ok := probe["schema"]; ok {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("--response-format schema: %w", err)
		}
		schemaBytes = b
	} else {
		// The entire object is the schema.
		schemaBytes = []byte(s)
	}
	return &llmapi.ResponseFormat{
		Type: llmapi.ResponseFormatTypeJSONSchema,
		JSONSchema: &llmapi.ResponseFormatJSONSchema{
			Name:        name,
			Description: description,
			Strict:      strict,
			Schema:      rawSchema(schemaBytes),
		},
	}, nil
}

// rawSchema wraps the raw JSON bytes so they round-trip through any
// json.Marshaler-aware consumer (proto conversion, native clients).
type rawSchema []byte

func (r rawSchema) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return []byte(r), nil
}

func newLLMMessageCmd(a *app) *cobra.Command {
	var (
		format            string
		reasoningEffort   string
		reasoningSummary  string
		maxOutputTokens   int
		promptCacheKey    string
		responseFormat    string
		parallelToolCalls string
	)

	cmd := &cobra.Command{
		Use:   "message <provider>/<model> <message-or-file>",
		Short: "Send a single user message and stream the response",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			svc := w.LLM(cmd.Context())
			entry, err := resolveModel(cmd.Context(), svc, args[0])
			if err != nil {
				return err
			}
			body, err := loadMessageBody(args[1])
			if err != nil {
				return err
			}
			rf, err := parseResponseFormat(responseFormat)
			if err != nil {
				return err
			}
			req := llmapi.Request{
				Messages: []llmapi.Message{{
					Role:    llmapi.RoleUser,
					Content: body,
				}},
				ReasoningEffort:  llmapi.ReasoningEffort(reasoningEffort),
				ReasoningSummary: llmapi.ReasoningSummary(reasoningSummary),
				MaxOutputTokens:  maxOutputTokens,
				PromptCacheKey:   promptCacheKey,
				ResponseFormat:   rf,
			}
			switch parallelToolCalls {
			case "":
				// leave unset
			case "true":
				v := true
				req.ParallelToolCalls = &v
			case "false":
				v := false
				req.ParallelToolCalls = &v
			default:
				return fmt.Errorf(
					"--parallel-tool-calls: must be true or false, got %q",
					parallelToolCalls)
			}
			it, err := svc.CreateCompletion(cmd.Context(), entry, req)
			if err != nil {
				return err
			}
			defer func() { _ = it.Close() }()
			return printMessageStream(cmd.Context(), format, it)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringVar(
		&reasoningEffort, "reasoning-effort", "",
		"Reasoning effort: none|minimal|low|medium|high|xhigh|max",
	)
	cmd.Flags().StringVar(
		&reasoningSummary, "reasoning-summary", "",
		"Reasoning summary: auto|concise|detailed|disabled",
	)
	cmd.Flags().IntVar(
		&maxOutputTokens, "max-output-tokens", 0,
		"Cap on tokens generated per completion",
	)
	cmd.Flags().StringVar(
		&promptCacheKey, "prompt-cache-key", "",
		"Stable conversation ID for server-side prompt caching. "+
			"Pass the same value across related requests to share "+
			"cache across turns. Honored by openai, codex, gemini, "+
			"ollama, custom; ignored by anthropic and llamacpp.",
	)
	cmd.Flags().StringVar(
		&responseFormat, "response-format", "",
		`Response format: "text", "json", or a JSON schema object`,
	)
	cmd.Flags().StringVar(
		&parallelToolCalls, "parallel-tool-calls", "",
		"Override parallel_tool_calls: true|false",
	)
	return cmd
}

// printMessageStream forwards textual deltas to stdout and surfaces
// the final DoneData (or stream error) on the structured JSON output.
// When format is empty, this is a plain-text streaming experience.
func printMessageStream(
	ctx context.Context, format string,
	it iterator.Iterator[llmapi.Event],
) error {
	type streamResult struct {
		Text         string `json:"text"`
		Reasoning    string `json:"reasoning,omitempty"`
		FinishReason string `json:"finish_reason,omitempty"`
	}
	var result streamResult
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		switch ev.Type {
		case llmapi.EventTextDelta:
			result.Text += ev.Text
			if format == "" {
				if _, err := io.WriteString(os.Stdout, ev.Text); err != nil {
					return err
				}
			}
		case llmapi.EventReasoningDelta:
			result.Reasoning += ev.Reasoning
		case llmapi.EventStreamDone:
			if ev.DoneData != nil {
				result.FinishReason = string(ev.DoneData.FinishReason)
			}
		case llmapi.EventStreamError:
			if ev.Error != nil {
				return ev.Error
			}
			return errors.New("llm: stream error")
		}
	}
	if err := it.Err(); err != nil {
		return err
	}
	if format == "" {
		// Make sure the output ends in a newline so prompts don't
		// glue onto the response.
		if !strings.HasSuffix(result.Text, "\n") {
			fmt.Println()
		}
		return nil
	}
	return printResult(ctx, format, result,
		func(v streamResult) {
			fmt.Println(v.Text)
		},
		[]string{"Text", "Reasoning", "FinishReason"})
}
