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

package llmconsole

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

// downloadUsage is the markdown help block returned when the user runs
// `models local download` without arguments (or with too many). It is
// also reused by `help models local download` so the normal manual path
// shows the same examples.
const downloadUsage = "## `models local download <reference>`\n\n" +
	"Download a GGUF model from an OCI registry into the local cache.\n\n" +
	"When the reference has no host, `" + llamacpp.DefaultRegistryHost + "` is assumed.\n\n" +
	"### Synopsis\n\n" +
	"`models local download <host/>owner/repo[:tag|@digest]`\n\n" +
	"### Examples\n\n" +
	"- `models local download unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M`  \n" +
	"  Pulls the `Q4_K_M` tag from Hugging Face.\n" +
	"- `models local download hf.co/unsloth/gemma-3n-E2B-it-GGUF`  \n" +
	"  Same thing with an explicit (short) host and the default `latest` tag.\n" +
	"- `models local download huggingface.co/foo/bar@sha256:abc…`  \n" +
	"  Pulls a specific digest instead of a tag.\n" +
	"- `models local download docker.io/library/myrepo:v1`  \n" +
	"  Works against any OCI v2 registry, not just Hugging Face.\n\n" +
	"See `help models local download` for this manual from the REPL."

// handleDownload pulls a model through llamacpp.Registry. Progress is
// streamed to the REPL ProgressWriter; a single markdown summary is
// yielded when the download completes. All OCI / cache details are
// owned by the registry — this handler is a pure UI adapter.
func (h *localHandler) handleDownload(
	ctx context.Context, args []string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) != 1 {
		// downloadUsage is a markdown help block that intentionally
		// ends with punctuation; the REPL renders it as markdown.
		return nil, errors.New(downloadUsage) //nolint:staticcheck // markdown help block, not a sentence-style error
	}

	ref, err := llamacpp.ParseReference(args[0])
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}

	// Show an indeterminate 0% progress bar immediately so the user sees
	// *something* while we wait for the manifest fetch to complete.
	if pw != nil {
		pw.Progress(0, 0, "B")
	}

	type progress struct {
		downloaded, total int64
	}
	progressCh := make(chan progress, 8)
	type result struct {
		res *llamacpp.DownloadResult
		err error
	}
	resultCh := make(chan result, 1)

	go func() {
		pr, err := h.registry.Download(ctx, ref,
			func(downloaded, total int64) {
				select {
				case progressCh <- progress{downloaded, total}:
				default:
					// Channel full — drop this sample; the next one
					// will catch up. Progress is purely informational.
				}
			})
		// Signal done by closing progress first so the iterator's drain
		// loop observes it before the result.
		close(progressCh)
		resultCh <- result{res: pr, err: err}
	}()

	// Throttled progress -> ProgressWriter. We don't yield Responsives for
	// progress updates; the REPL's ProgressWriter renders them directly in
	// the prompt line.
	var lastPwUpdate time.Time
	pump := func(p progress) {
		if pw == nil {
			return
		}
		// Always emit terminal samples (downloaded >= total > 0) so
		// the bar lands at 100% even when the throttle would
		// otherwise drop them.
		terminal := p.total > 0 && p.downloaded >= p.total
		now := time.Now()
		if !terminal && now.Sub(lastPwUpdate) < 200*time.Millisecond {
			return
		}
		lastPwUpdate = now
		scaled, scaledTotal, unit := scaleProgressBytes(p.downloaded, p.total)
		pw.Progress(scaled, scaledTotal, unit)
	}

	done := false
	return iterator.FromFunc(
		func(ctx context.Context) (component.Responsive, bool, error) {
			if done {
				return nil, false, nil
			}
			for {
				select {
				case <-ctx.Done():
					done = true
					return nil, false, ctx.Err()
				case p, ok := <-progressCh:
					if !ok {
						select {
						case <-ctx.Done():
							done = true
							return nil, false, ctx.Err()
						case r := <-resultCh:
							done = true
							if r.err != nil {
								return nil, false, r.err
							}
							return downloadSummary(r.res, ref), true, nil
						}
					}
					pump(p)
				}
			}
		},
		func() error { return nil },
	), nil
}

// downloadSummary renders a short markdown block describing what was
// pulled, including the on-disk path of every layer that ended up in the
// cache.
func downloadSummary(pr *llamacpp.DownloadResult, requested llamacpp.Reference) component.Responsive {
	if pr == nil {
		return component.NewResponsiveString("download: no result", component.StringResponsiveConfig{})
	}
	ref := pr.Reference
	// Pull may canonicalise the reference (e.g. filling in a default
	// tag); fall back to what the user actually typed when the result's
	// copy is empty.
	if ref.Host == "" && ref.Repository == "" {
		ref = requested
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Downloaded `%s`\n\n", ref.String())
	if pr.ModelPath != "" {
		fmt.Fprintf(&b, "- **Model**: `%s`\n", pr.ModelPath)
	}
	for _, layer := range pr.LayerSizes {
		fmt.Fprintf(&b, "- %s: `%s` (%s)\n",
			shortMediaType(layer.MediaType), layer.Path, humanBytes(layer.Size))
	}
	b.WriteByte('\n')
	b.WriteString("Use `models local list` to see it in the list.\n")
	return markdownResponsive(b.String())
}

// shortMediaType returns a user-friendly label for known Ollama layer
// media types, and the media type itself as a fallback.
func shortMediaType(mt string) string {
	switch mt {
	case ociregistry.MediaTypeOllamaModel:
		return "model"
	case ociregistry.MediaTypeOllamaTemplate:
		return "template"
	case ociregistry.MediaTypeOllamaParams:
		return "params"
	case ociregistry.MediaTypeOllamaSystem:
		return "system"
	case ociregistry.MediaTypeOllamaAdapter:
		return "adapter"
	case ociregistry.MediaTypeOllamaLicense:
		return "license"
	}
	return mt
}

// humanBytes formats a byte count as KiB/MiB/GiB.
func humanBytes(n int64) string {
	const (
		k = 1024
		m = 1024 * 1024
		g = 1024 * 1024 * 1024
	)
	switch {
	case n >= g:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(g))
	case n >= m:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(m))
	case n >= k:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// scaleProgressBytes maps a (downloaded, total) byte pair into a shared,
// human-readable unit suitable for a repl.ProgressWriter. The unit is
// chosen from `total` so the denominator stays stable across updates as
// the download grows.
func scaleProgressBytes(downloaded, total int64) (int64, int64, string) {
	const (
		kib = int64(1024)
		mib = 1024 * kib
		gib = 1024 * mib
		tib = 1024 * gib

		minTicks = int64(100)
	)
	if total > 0 && downloaded > total {
		downloaded = total
	}
	units := []struct {
		div  int64
		name string
	}{
		{tib, "TiB"},
		{gib, "GiB"},
		{mib, "MiB"},
		{kib, "KiB"},
	}
	for _, u := range units {
		if total/u.div >= minTicks {
			return downloaded / u.div, total / u.div, u.name
		}
	}
	return downloaded, total, "B"
}
