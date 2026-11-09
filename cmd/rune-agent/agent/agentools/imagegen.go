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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/image/draw"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// ImageGen saves generated images on the workspace host and returns visual content
// through the agent's hidden-user-message mechanism.
type ImageGen struct {
	generator llmapi.Service
	model     llmapi.ModelEntry
	fs        workspaceapi.FileSystem
	dir       string
}

// NewImageGen accepts a service; nil reports unavailable on execution.
// dir must be the absolute generated_images directory on the workspace host.
func NewImageGen(generator llmapi.Service, model llmapi.ModelEntry, fs workspaceapi.FileSystem, dir string) *ImageGen {
	return &ImageGen{generator: generator, model: model, fs: fs, dir: dir}
}

var _ agent.Tool = (*ImageGen)(nil)

type imageGenArgs struct {
	Prompt                 *string  `json:"prompt"`
	TransparentBackground  bool     `json:"transparent_background"`
	ReferencedImagePaths   []string `json:"referenced_image_paths"`
	NumLastImagesToInclude *int     `json:"num_last_images_to_include"`
}

// Definition permits either local references or recent conversation images, never both.
func (t *ImageGen) Definition() llmapi.Tool {
	return llmapi.Tool{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
		Name: "imagegen",
		Description: "The `imagegen` tool enables image generation from descriptions and editing of existing images based on specific instructions. Use it when:\n\n" +
			"- The user requests an image based on a scene description, such as a diagram, portrait, comic, meme, or any other visual.\n" +
			"- The user wants to modify an attached or previously generated image with specific changes, including adding or removing elements, altering colors, improving quality/resolution, or transforming the style (e.g., cartoon, oil painting).\n\n" +
			"Guidelines:\n" +
			"- Set `transparent_background` to true when the request calls for a transparent background, including background removal or a cutout; set it to false otherwise. For edits, preserve existing transparency unless the user asks to change it.\n" +
			"- Omit both `referenced_image_paths` and `num_last_images_to_include` when generating a brand new image.\n" +
			"- For edits, use `referenced_image_paths` when every target image has a local file path.\n" +
			"- If you have not seen a local image yet, use `read_file` to inspect it before editing.\n" +
			"- Use `num_last_images_to_include` only when at least one target image has no local file path.\n" +
			"- Set `num_last_images_to_include` to the smallest number of recent conversation images that includes every target image, up to 5.\n" +
			"- Never provide both `referenced_image_paths` and `num_last_images_to_include`.\n" +
			"- If neither mechanism can include every target image, ask the user to attach the missing images again.\n" +
			"- Directly generate the image without reconfirmation or clarification unless required images must be attached again.\n" +
			"- Always use this tool for image editing unless the user explicitly requests otherwise.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"prompt"},
			"properties": map[string]any{
				"prompt":                     map[string]any{"type": "string"},
				"transparent_background":     map[string]any{"type": "boolean", "description": "Whether the output should have a transparent background. Defaults to false."},
				"referenced_image_paths":     map[string]any{"type": "array", "maxItems": 5, "items": map[string]any{"type": "string"}, "description": "Absolute paths to the images to edit."},
				"num_last_images_to_include": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			},
		},
	}}
}

// Summary uses the prompt as the preview caption; malformed arguments have no summary.
func (t *ImageGen) Summary(arguments string) string {
	var args imageGenArgs
	if json.Unmarshal([]byte(arguments), &args) != nil || args.Prompt == nil {
		return ""
	}
	return *args.Prompt
}

// NeedsDeterministicOrder lets edits reference images produced earlier in the batch.
func (t *ImageGen) NeedsDeterministicOrder() bool { return true }

// Execute preserves original PNGs without overwriting and returns a bounded model
// preview. Failures are tool errors prefixed with "image generation failed: ".
func (t *ImageGen) Execute(ctx context.Context, arguments string) agent.ToolResult {
	result, err := t.execute(ctx, arguments)
	if err != nil {
		return agent.ToolResult{Content: "image generation failed: " + err.Error(), IsError: true}
	}
	return result
}

// The SDK caps the encoded request at 15 MiB, below the transport's 16 MiB
// limit. Leave room for the model and JSON/protobuf framing as well.
const imageGenRequestBudget = 15*1024*1024 - 64*1024

// maxReferenceImageFileBytes bounds how much of a referenced image is read.
// Files above maxImageBytes are still accepted when downscaling brings them
// under the model caps, as read_file does; this only keeps a huge file out of memory.
const maxReferenceImageFileBytes = 64 << 20

func (t *ImageGen) execute(ctx context.Context, arguments string) (agent.ToolResult, error) {
	var args imageGenArgs
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return agent.ToolResult{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return agent.ToolResult{}, fmt.Errorf("invalid trailing arguments")
	}
	if args.Prompt == nil {
		return agent.ToolResult{}, fmt.Errorf("missing field `prompt`")
	}
	if len(args.ReferencedImagePaths) > 5 {
		return agent.ToolResult{}, fmt.Errorf("`referenced_image_paths` must contain at most 5 paths")
	}
	if len(args.ReferencedImagePaths) > 0 && args.NumLastImagesToInclude != nil {
		return agent.ToolResult{}, fmt.Errorf("provide only one of `referenced_image_paths` or `num_last_images_to_include`")
	}
	if args.NumLastImagesToInclude != nil && (*args.NumLastImagesToInclude < 1 || *args.NumLastImagesToInclude > 5) {
		return agent.ToolResult{}, fmt.Errorf("`num_last_images_to_include` must be between 1 and 5")
	}
	request := llmapi.ImageRequest{Prompt: *args.Prompt, TransparentBackground: args.TransparentBackground}
	budget := len(arguments) + len(t.model.Name) + len(t.model.Provider) + len(t.model.BaseURL) + len(t.model.ProjectorPath)
	addImage := func(uri string) error {
		budget += len(uri)
		if budget > imageGenRequestBudget {
			return fmt.Errorf("image request exceeds the 15 MiB encoded size budget (16 MiB RPC limit); resize images or include fewer images")
		}
		data, err := decodeImageGenURI(uri)
		if err != nil {
			return err
		}
		request.Images = append(request.Images, data)
		return nil
	}
	if budget > imageGenRequestBudget {
		return agent.ToolResult{}, fmt.Errorf("image request exceeds the 15 MiB encoded size budget (16 MiB RPC limit)")
	}
	for _, name := range args.ReferencedImagePaths {
		if !path.IsAbs(name) {
			return agent.ToolResult{}, fmt.Errorf("path is not absolute: %s", name)
		}
		if t.fs == nil {
			return agent.ToolResult{}, fmt.Errorf("referenced image paths are unavailable in this session")
		}
		mime, ok := ImageMediaType(name)
		if !ok {
			return agent.ToolResult{}, fmt.Errorf("unsupported image format: %s", name)
		}
		f, err := t.fs.OpenFile(name, os.O_RDONLY, 0)
		if err != nil {
			return agent.ToolResult{}, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxReferenceImageFileBytes+1))
		closeErr := f.Close()
		if err != nil {
			return agent.ToolResult{}, err
		}
		if closeErr != nil {
			return agent.ToolResult{}, closeErr
		}
		if len(data) > maxReferenceImageFileBytes {
			return agent.ToolResult{}, fmt.Errorf("referenced image %s is larger than %d MiB; resize or recompress it and try again",
				path.Base(name), maxReferenceImageFileBytes>>20)
		}
		uri, err := EncodeImageDataURI(name, data, mime)
		if err != nil {
			return agent.ToolResult{}, err
		}
		if err := addImage(uri); err != nil {
			return agent.ToolResult{}, err
		}
	}
	if args.NumLastImagesToInclude != nil {
		uris, err := recentImageGenImages(agent.Conversation(ctx), *args.NumLastImagesToInclude)
		if err != nil {
			return agent.ToolResult{}, err
		}
		for _, uri := range uris {
			if err := addImage(uri); err != nil {
				return agent.ToolResult{}, err
			}
		}
	}
	if t.generator == nil {
		return agent.ToolResult{}, fmt.Errorf("image generation is unavailable in this session")
	}
	if t.fs == nil || !path.IsAbs(t.dir) {
		return agent.ToolResult{}, fmt.Errorf("generated image directory is unavailable in this session")
	}
	dialogue, call := agent.DialogueIDFromContext(ctx), agent.ParentToolCallID(ctx)
	if dialogue == "" || call == "" {
		return agent.ToolResult{}, fmt.Errorf("image generation requires a dialogue and tool call ID")
	}
	name := path.Join(t.dir, imageGenPathComponent(dialogue), imageGenPathComponent(call)+".png")
	if err := t.fs.MkdirAll(path.Dir(name), 0o700); err != nil {
		return agent.ToolResult{}, err
	}
	// Reserve the name before spending an upstream generation on a duplicate call.
	f, err := t.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return agent.ToolResult{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = f.Close()
			_ = t.fs.Remove(name)
		}
	}()
	generated, err := t.generator.GenerateImage(ctx, t.model, request)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if len(generated.PNG) == 0 {
		return agent.ToolResult{}, fmt.Errorf("image generation returned no image data")
	}
	src, err := decodeGeneratedPNG(generated.PNG)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if n, err := f.Write(generated.PNG); err != nil {
		return agent.ToolResult{}, err
	} else if n != len(generated.PNG) {
		return agent.ToolResult{}, io.ErrShortWrite
	}
	if err := f.Close(); err != nil {
		return agent.ToolResult{}, err
	}
	complete = true
	// A preview failure must not discard a valid, paid-for original.
	uri, err := generatedPNGPreview(name, generated.PNG, src)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("original image saved to %s, but model preview failed: %w", name, err)
	}
	// The image travels in a separate user message; label it with its path
	// rather than repeating the instructions the tool result already carries.
	return agent.ToolResult{Content: generatedImageHint(name), MultiContent: []llmapi.ContentPart{
		{Type: llmapi.ContentPartTypeText, Text: "Generated image: " + name},
		{Type: llmapi.ContentPartTypeImageURL, ImageURL: uri},
	}}, nil
}

func decodeGeneratedPNG(data []byte) (image.Image, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid generated PNG: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, fmt.Errorf("generated PNG dimensions exceed the decode budget (%dx%d px, max %d pixels)", cfg.Width, cfg.Height, maxDecodePixels)
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid generated PNG: %w", err)
	}
	return src, nil
}

func generatedPNGPreview(name string, data []byte, src image.Image) (string, error) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= maxImageEdge && h <= maxImageEdge && len(data) <= maxImageBytes {
		return EncodeImageDataURI(name, data, "image/png")
	}
	if w > maxImageEdge || h > maxImageEdge {
		if w >= h {
			w, h = maxImageEdge, max(1, (h*maxImageEdge+w/2)/w)
		} else {
			w, h = max(1, (w*maxImageEdge+h/2)/h), maxImageEdge
		}
	}
	for {
		// The edge cap bounds both pixel storage and PNG encoding buffers.
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return "", err
		}
		if buf.Len() <= maxImageBytes {
			return EncodeImageDataURI(name, buf.Bytes(), "image/png")
		}
		w, h = max(1, w*3/4), max(1, h*3/4)
	}
}

func decodeImageGenURI(uri string) ([]byte, error) {
	header, data, ok := strings.Cut(uri, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return nil, fmt.Errorf("conversation image is not a base64 image data URI; attach the image again")
	}
	return base64.StdEncoding.DecodeString(data)
}

func recentImageGenImages(messages []llmapi.Message, count int) ([]string, error) {
	images := make([]string, 0, count)
	for i := len(messages) - 1; i >= 0 && len(images) < count; i-- {
		if messages[i].Role != llmapi.RoleUser {
			continue
		}
		parts := messages[i].MultiContent
		for j := len(parts) - 1; j >= 0 && len(images) < count; j-- {
			if parts[j].Type == llmapi.ContentPartTypeImageURL {
				images = append(images, parts[j].ImageURL)
			}
		}
	}
	if len(images) != count {
		return nil, fmt.Errorf("requested the last %d conversation images, but only %d were available", count, len(images))
	}
	slices.Reverse(images)
	return images, nil
}

func imageGenPathComponent(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

const generatedImageHintSuffix = " by default.\nIf you need to use a generated image at another path, copy it and leave the original in place unless the user explicitly asks you to delete it.\nThe generated image is already displayed to the user. There is no need to render it in the final response as a Markdown image or file link."

func generatedImageHint(name string) string {
	return "Generated images are saved to " + path.Dir(name) + " as " + name + generatedImageHintSuffix
}

// GeneratedImagePath recognizes only a complete generated-image hint, not arbitrary
// paths in tool output. It returns the absolute workspace-host path for replay.
func GeneratedImagePath(output string) (string, bool) {
	body, ok := strings.CutSuffix(output, generatedImageHintSuffix)
	if !ok {
		return "", false
	}
	for i := strings.Index(body, " as "); i >= 0; {
		name := body[i+4:]
		if path.IsAbs(name) && generatedImageHint(name) == output {
			return name, true
		}
		next := strings.Index(body[i+4:], " as ")
		if next < 0 {
			break
		}
		i += 4 + next
	}
	return "", false
}
