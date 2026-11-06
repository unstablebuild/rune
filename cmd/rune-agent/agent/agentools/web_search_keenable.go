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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	keenableBaseURL = "https://api.keenable.ai"
	// keenableAppTitle names the calling app. Keenable rejects requests
	// that carry neither an API key nor this header.
	keenableAppTitle = "Rune"
	// keenableMaxResponseBytes bounds the response body read into
	// memory: 20 results of a few kilobytes each fit with room to spare.
	keenableMaxResponseBytes = 1 << 20
)

// KeenableSearch is a WebSearchProvider backed by Keenable's search API
// (https://docs.keenable.ai). It works without an account; an API key
// only raises the rate limits.
type KeenableSearch struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

var _ WebSearchProvider = (*KeenableSearch)(nil)

// NewKeenableSearch returns a Keenable provider. An empty apiKey uses
// the public endpoint, which needs no key.
func NewKeenableSearch(apiKey string) *KeenableSearch {
	return &KeenableSearch{
		apiKey:  apiKey,
		baseURL: keenableBaseURL,
		client:  http.DefaultClient,
	}
}

type keenableSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
	Site       string `json:"site,omitempty"`
}

type keenableSearchResponse struct {
	Results []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		// Snippet holds the page text; Keenable leaves description
		// empty.
		Snippet string `json:"snippet"`
	} `json:"results"`
}

type keenableError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Name satisfies WebSearchProvider.
func (k *KeenableSearch) Name() string { return "Keenable" }

// Search satisfies WebSearchProvider.
func (k *KeenableSearch) Search(ctx context.Context, q WebSearchQuery) ([]WebSearchResult, error) {
	body, err := json.Marshal(keenableSearchRequest(q))
	if err != nil {
		return nil, err
	}
	endpoint := k.baseURL + "/v1/search/public"
	if k.apiKey != "" {
		endpoint = k.baseURL + "/v1/search"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Keenable-Title", keenableAppTitle)
	if k.apiKey != "" {
		req.Header.Set("X-API-Key", k.apiKey)
	}

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, keenableMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, k.statusError(resp.StatusCode, data)
	}

	var parsed keenableSearchResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	results := make([]WebSearchResult, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, WebSearchResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
	}
	return results, nil
}

func (k *KeenableSearch) statusError(status int, body []byte) error {
	var e keenableError
	_ = json.Unmarshal(body, &e)
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = strings.TrimSpace(e.Error)
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if detail != "" {
		msg += ": " + detail
	}
	switch {
	case status == http.StatusTooManyRequests && k.apiKey == "":
		msg += " (set KEENABLE_API_KEY for higher rate limits)"
	case status == http.StatusUnauthorized && k.apiKey != "":
		msg += " (check KEENABLE_API_KEY)"
	}
	return errors.New(msg)
}
