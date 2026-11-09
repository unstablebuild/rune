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

package llmsearch

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/llamaserver"
)

// Embedder turns texts into L2-normalized vectors of dims values. A nil
// vector stands for a text the model could not take; an error means the
// embedder cannot run at all. Info identifies the model so that vectors
// from different models never share an index.
type Embedder interface {
	Info() EmbedderInfo
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbedderInfo describes what makes an embedder's vectors: the same info
// means the same vector for the same text.
type EmbedderInfo struct {
	Model, Revision, Runtime, Dtype string
	Dim                             int
}

// VectorSource is "<Model>@<Revision[:7]> <Runtime> <Dtype>".
func (i EmbedderInfo) VectorSource() string {
	rev := i.Revision
	if len(rev) > 7 {
		rev = rev[:7]
	}
	return fmt.Sprintf("%s@%s %s %s", i.Model, rev, i.Runtime, i.Dtype)
}

// EmbedderFactory builds the embedder for a downloaded local model.
type EmbedderFactory func(entry llmapi.ModelEntry) Embedder

// llamaRuntime names the vectors llama-server makes. A llama.cpp update
// that changes them must bump it, so that indexes are rebuilt.
const llamaRuntime = "llama-server-vectors1"

// NewLlamaEmbedderFactory returns embedders that run the model on svc's
// embedding servers.
func NewLlamaEmbedderFactory(svc *llamaserver.Service) EmbedderFactory {
	return func(entry llmapi.ModelEntry) Embedder {
		return &llamaEmbedder{svc: svc, entry: entry, info: llamaInfo(entry)}
	}
}

// llamaInfo identifies a registry entry: its blob's digest is the
// revision, its tag's quantization the dtype.
func llamaInfo(entry llmapi.ModelEntry) EmbedderInfo {
	dtype := ""
	if i := strings.LastIndex(entry.Name, ":"); i >= 0 {
		dtype = entry.Name[i+1:]
	}
	return EmbedderInfo{
		Model:    entry.Name,
		Revision: strings.TrimPrefix(filepath.Base(entry.BaseURL), "sha256-"),
		Runtime:  llamaRuntime,
		Dtype:    dtype,
		Dim:      dims,
	}
}

type llamaEmbedder struct {
	svc   *llamaserver.Service
	entry llmapi.ModelEntry
	info  EmbedderInfo
}

func (e *llamaEmbedder) Info() EmbedderInfo { return e.info }

func (e *llamaEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := e.svc.EmbedTexts(ctx, e.entry, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("llmsearch: %s returned %d vectors for %d texts", e.entry.Name, len(vecs), len(texts))
	}
	for _, v := range vecs {
		if v != nil && len(v) != dims {
			return nil, fmt.Errorf("llmsearch: %s returned a vector of %d values, want %d", e.entry.Name, len(v), dims)
		}
	}
	return vecs, nil
}

// NewFakeEmbedderFactory returns an embedder for tests that runs no model:
// each of a text's Terms adds to one of dims buckets, so the cosine of two
// vectors follows the words their texts share.
func NewFakeEmbedderFactory() EmbedderFactory {
	return func(llmapi.ModelEntry) Embedder { return fakeEmbedder{} }
}

type fakeEmbedder struct{}

func (fakeEmbedder) Info() EmbedderInfo {
	return EmbedderInfo{Model: "fake", Revision: "0000000", Runtime: "fake", Dtype: "fp32", Dim: dims}
}

func (fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, dims)
		for _, term := range Terms(text) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(term))
			v[h.Sum32()%dims]++
		}
		var sum float64
		for _, x := range v {
			sum += float64(x * x)
		}
		if sum > 0 {
			norm := float32(math.Sqrt(sum))
			for j := range v {
				v[j] /= norm
			}
		}
		out[i] = v
	}
	return out, nil
}
