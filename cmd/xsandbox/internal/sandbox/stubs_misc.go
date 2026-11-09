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

package sandbox

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// stubParser answers every syntax query with an empty result set.
type stubParser struct{}

var _ syntaxapi.Parser = stubParser{}

func (stubParser) Search(string, []string, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (stubParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (stubParser) Query(workspaceapi.URI, string, []string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (stubParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (stubParser) Highlight(workspaceapi.URI, string) (iterator.Iterator[textapi.Location], error) {
	return iterator.Empty[textapi.Location](), nil
}

func (stubParser) ResolveSymbol(context.Context, string, syntaxapi.Progress) (iterator.Iterator[syntaxapi.Match], error) {
	return iterator.Empty[syntaxapi.Match](), nil
}

func (stubParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// stubLLM exposes no models; calls fail with the canonical service
// errors so extensions can exercise their error paths.
type stubLLM struct{}

var _ llmapi.Service = stubLLM{}

func (stubLLM) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (stubLLM) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

func (stubLLM) CreateCompletion(
	context.Context, llmapi.ModelEntry, llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	return iterator.Empty[llmapi.Event](), nil
}

func (stubLLM) CountTokens(llmapi.ModelEntry, []llmapi.Message) (int, error) {
	return 0, nil
}

func (stubLLM) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.Empty[llmapi.ModelEntry]()
}

func (stubLLM) GetModel(context.Context, llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}
