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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
)

// maxImageBytes is the maximum raw file size for image reads. The image
// is base64-encoded into the LLM request, which inflates payload size
// by ~33%. The extension host's gRPC server caps inbound messages at 16
// MiB (llmrpc.MaxRecvMsgSize) and capToolResult drops any encoded image
// part above 8 MiB (agent.MaxToolResultBytes). Capping raw bytes at 5
// MiB keeps the ~6.7 MiB encoded payload under the 8 MiB per-result
// ceiling with room for the surrounding message envelope.
const maxImageBytes = 5 * 1024 * 1024

// maxImageEdge is Anthropic's per-image dimension cap for many-image
// requests. An image exceeding it on either axis causes the API to
// reject the entire request with a 400, poisoning the conversation.
const maxImageEdge = 2000

// maxDecodePixels bounds the memory spent decoding an oversized image
// for downscaling (~4 bytes per pixel), so a small file declaring huge
// dimensions cannot exhaust memory.
const maxDecodePixels = 50_000_000

type readFileTool struct {
	fs           workspaceapi.FileSystem
	cwd          workspaceapi.URI
	tracker      *FileTracker
	maxLineBytes int
}

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func newReadFile(fs workspaceapi.FileSystem, cwd workspaceapi.URI, tracker *FileTracker, maxLineBytes int) agent.Tool {
	if maxLineBytes <= 0 {
		maxLineBytes = agent.DefaultMaxLineBytes
	}
	return &readFileTool{fs: fs, cwd: cwd, tracker: tracker, maxLineBytes: maxLineBytes}
}

func (t *readFileTool) NeedsDeterministicOrder() bool { return false }

func (t *readFileTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "read_file",
			Description: `Read the contents of a file. Returns each line prefixed with its 1-based
line number (e.g. "L1: hello world").

Supports image files (.png, .jpg, .jpeg, .gif, .webp) — the image is
returned as visual content that you can see directly. Offset and limit
are ignored for images.

Use offset (1-based) and limit to read a slice of a large text file —
for example, offset=10 limit=20 returns lines 10 through 29. Omit both
to read the entire file.

When you only need to know what symbols a file contains, prefer the
outline_file tool instead — it returns the structure without the full
source. When you need a symbol's type or documentation, prefer the
describe_symbol tool.

The path can be relative to the workspace root or absolute. Always read
a file before modifying it so you understand its current contents.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The file path to read (relative to workspace root or absolute).",
					},
					"offset": map[string]any{
						"type":        []string{"integer", "null"},
						"description": "Optional 1-based line number to start reading from.",
					},
					"limit": map[string]any{
						"type":        []string{"integer", "null"},
						"description": "Optional maximum number of lines to read.",
					},
				},
				"required":             []string{"path", "offset", "limit"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *readFileTool) Summary(arguments string) string {
	var args readFileArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	s := summaryPath(t.cwd, args.Path)
	if args.Offset > 0 {
		end := args.Offset + args.Limit - 1
		if args.Limit > 0 {
			s += fmt.Sprintf(":%d-%d", args.Offset, end)
		} else {
			s += fmt.Sprintf(":%d-", args.Offset)
		}
	}
	return s
}

func (t *readFileTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args readFileArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	path := resolvePath(t.cwd, args.Path)

	data, err := readFile(t.fs, path)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}
	}

	if mime, ok := imageMediaType(path); ok {
		return t.executeImage(ctx, path, data, mime)
	}

	t.tracker.Record(path, data)

	variant := readFileVariant(args.Offset, args.Limit)

	// Binary files: skip in-band content and return a metadata stub
	// so the model doesn't try to reason about raw bytes (and so the
	// proto-go marshaller doesn't reject the request). Still track
	// the read so re-reads detect mutation as usual.
	if utf8validate.IsBinary(data) {
		staleIDs := t.tracker.TrackRead(ctx, "read_file", path, variant)
		staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(path)...)
		return agent.ToolResult{
			Content:           utf8validate.BinaryStub(filepath.Base(path), len(data), sha256.Sum256(data)),
			DropToolResultIDs: staleIDs,
		}
	}

	lines := strings.Split(string(data), "\n")

	// Apply offset (1-based)
	start := 0
	if args.Offset > 0 {
		start = args.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}

	end := len(lines)
	if args.Limit > 0 && start+args.Limit < end {
		end = start + args.Limit
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "L%d: %s\n", i+1, agent.TruncateLine(lines[i], t.maxLineBytes))
	}

	content := sb.String()
	if n := utf8validate.CountInvalidBytes(content); n > 0 {
		content = utf8validate.Sanitize(content) + "\n" + utf8validate.InvalidBytesMarker(n) + "\n"
	}

	staleIDs := t.tracker.TrackRead(ctx, "read_file", path, variant)
	staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(path)...)
	return agent.ToolResult{
		Content:           content,
		DropToolResultIDs: staleIDs,
	}
}

// readFileVariant returns a string that distinguishes ranged reads
// from full-file reads so the FileTracker can track them independently.
func readFileVariant(offset, limit int) string {
	if offset <= 0 && limit <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", offset, limit)
}

func resolvePath(cwd workspaceapi.URI, path string) string {
	expanded, err := workspaceapi.ExpandPathWithURI(path, cwd)
	if err != nil {
		return path
	}
	return expanded
}

// imageMediaType returns the MIME type for recognised image extensions.
// SVG is excluded because it's text XML and should be read normally.
func imageMediaType(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
}

// ImageMediaType returns the MIME type for the image extensions that can
// be sent to the model as visual content, and whether path is one of them.
func ImageMediaType(path string) (string, bool) {
	return imageMediaType(path)
}

// EncodeImageDataURI enforces the model image caps on data and returns a
// base64 data URI suitable for an llmapi.ContentPartTypeImageURL part.
// Images exceeding the per-dimension cap are downscaled to fit,
// preserving aspect ratio; JPEGs are re-encoded as JPEG and every other
// format as PNG, so the URI's media type may differ from mime. The
// returned error is user-facing: it explains which cap was exceeded and
// how to get the image under it.
func EncodeImageDataURI(path string, data []byte, mime string) (string, error) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil &&
		(cfg.Width > maxImageEdge || cfg.Height > maxImageEdge) {
		data, mime, err = downscaleImage(path, data, cfg)
		if err != nil {
			return "", err
		}
	}
	if len(data) > maxImageBytes {
		return "", fmt.Errorf(
			"image file %s is too large to send to the model "+
				"(%d bytes, max %d bytes / %d MiB). Reduce the image "+
				"size (resize or recompress) and try again",
			filepath.Base(path), len(data), maxImageBytes,
			maxImageBytes/(1024*1024))
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// downscaleImage resizes data so its longest edge is maxImageEdge and
// returns the re-encoded bytes with their media type.
func downscaleImage(path string, data []byte, cfg image.Config) ([]byte, string, error) {
	name := filepath.Base(path)
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, "", fmt.Errorf(
			"image file %s dimensions are too large to downscale "+
				"(%dx%d px, max %d pixels). Resize or crop the image so "+
				"neither dimension exceeds %dpx and try again",
			name, cfg.Width, cfg.Height, maxDecodePixels, maxImageEdge)
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image file %s to downscale it: %w", name, err)
	}

	w, h := cfg.Width, cfg.Height
	if w >= h {
		w, h = maxImageEdge, max(1, (h*maxImageEdge+w/2)/w)
	} else {
		w, h = max(1, (w*maxImageEdge+h/2)/h), maxImageEdge
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom keeps screenshot text legible where bilinear blurs it.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&buf, dst)
	}
	if err != nil {
		return nil, "", fmt.Errorf("encode downscaled image file %s: %w", name, err)
	}
	return buf.Bytes(), mime, nil
}

func (t *readFileTool) executeImage(ctx context.Context, path string, data []byte, mime string) agent.ToolResult {
	dataURI, err := EncodeImageDataURI(path, data, mime)
	if err != nil {
		return agent.ToolResult{
			Content: "error: " + err.Error(),
			IsError: true,
		}
	}

	t.tracker.Record(path, data)

	summary := fmt.Sprintf("Read image file: %s (%d bytes, %s)", filepath.Base(path), len(data), mime)

	staleIDs := t.tracker.TrackRead(ctx, "read_file", path, "")
	staleIDs = append(staleIDs, t.tracker.ConsumeDiscoveries(path)...)

	return agent.ToolResult{
		Content: summary,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: summary},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: dataURI},
		},
		DropToolResultIDs: staleIDs,
	}
}

// summaryPath returns a workspace-relative form of the given path for display.
// Paths outside the workspace collapse "../" chains into ".../".
func summaryPath(cwd workspaceapi.URI, path string) string {
	if path == "" {
		return ""
	}
	abs := resolvePath(cwd, path)
	rel, err := filepath.Rel(cwd.Path(), abs)
	if err != nil {
		return path
	}
	if strings.HasPrefix(rel, "..") {
		for strings.HasPrefix(rel, "../") {
			rel = rel[len("../"):]
		}
		if rel == ".." {
			return "..."
		}
		return ".../" + rel
	}
	return rel
}
