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
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

type imageGenFake struct {
	llmapi.Service
	requests []llmapi.ImageRequest
	models   []llmapi.ModelEntry
	image    llmapi.Image
	err      error
}

func (f *imageGenFake) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return true, nil
}
func (f *imageGenFake) GenerateImage(_ context.Context, model llmapi.ModelEntry, request llmapi.ImageRequest) (llmapi.Image, error) {
	f.requests = append(f.requests, request)
	f.models = append(f.models, model)
	return f.image, f.err
}

func TestImageGenValidation(t *testing.T) {
	tests := []struct{ args, want string }{
		{`{}`, "missing field `prompt`"},
		{`{"prompt":"x","referenced_image_paths":["a","b","c","d","e","f"]}`, "`referenced_image_paths` must contain at most 5 paths"},
		{`{"prompt":"x","referenced_image_paths":["/a.png"],"num_last_images_to_include":1}`, "provide only one of `referenced_image_paths` or `num_last_images_to_include`"},
		{`{"prompt":"x","num_last_images_to_include":0}`, "`num_last_images_to_include` must be between 1 and 5"},
		{`{"prompt":"x","num_last_images_to_include":6}`, "`num_last_images_to_include` must be between 1 and 5"},
		{`{"prompt":"x","num_last_images_to_include":1}`, "requested the last 1 conversation images, but only 0 were available"},
		{`{"prompt":"x","referenced_image_paths":["relative.png"]}`, "path is not absolute: relative.png"},
		{`{"prompt":"x","unknown":true}`, `json: unknown field "unknown"`},
		{`{"prompt":"x"} {}`, "invalid trailing arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			fake := &imageGenFake{}
			tool := NewImageGen(fake, llmapi.ModelEntry{}, localFS{}, t.TempDir())
			result := tool.Execute(context.Background(), tt.args)
			assert.True(t, result.IsError)
			assert.Equal(t, "image generation failed: "+tt.want, result.Content)
			assert.Empty(t, fake.requests)
		})
	}
}

func TestImageGen(t *testing.T) {
	ctx := agent.WithParentToolCallID(agent.WithDialogueID(context.Background(), "dialogue"), "call")
	for _, args := range []string{`{"prompt":"a cat","transparent_background":true}`, `{"prompt":"a cat","transparent_background":true,"referenced_image_paths":[]}`} {
		t.Run(args, func(t *testing.T) {
			dir := t.TempDir()
			var buf bytes.Buffer
			require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
			original := buf.Bytes()
			fake := &imageGenFake{image: llmapi.Image{PNG: original}}
			model := llmapi.ModelEntry{Provider: "codex"}
			tool := NewImageGen(fake, model, localFS{}, dir)
			assert.Equal(t, "imagegen", tool.Definition().Function.Name)
			assert.Equal(t, "a cat", tool.Summary(args))
			assert.True(t, tool.NeedsDeterministicOrder())
			result := tool.Execute(ctx, args)
			require.False(t, result.IsError, result.Content)
			name := filepath.Join(dir, "dialogue", "call.png")
			data, err := os.ReadFile(name)
			require.NoError(t, err)
			assert.Equal(t, original, data)
			assert.Equal(t, []llmapi.ImageRequest{{Prompt: "a cat", TransparentBackground: true}}, fake.requests)
			assert.Equal(t, []llmapi.ModelEntry{model}, fake.models)
			require.Len(t, result.MultiContent, 2)
			assert.Equal(t, "Generated image: "+name, result.MultiContent[0].Text,
				"the image label must not repeat the tool result's instructions")
			assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(original), result.MultiContent[1].ImageURL)
			parsed, ok := GeneratedImagePath(result.Content)
			require.True(t, ok)
			assert.Equal(t, name, parsed)
			assert.Contains(t, result.Content, "copy it and leave the original in place")
			assert.Contains(t, result.Content, "already displayed to the user")
			assert.True(t, tool.Execute(ctx, args).IsError)
			assert.Len(t, fake.requests, 1)
			data, err = os.ReadFile(name)
			require.NoError(t, err)
			assert.Equal(t, original, data)
		})
	}
}

func TestImageGenPNGOutput(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		invalid       bool
	}{
		{"byte limit", 1200, 1200, false},
		{"edge limit", 2100, 700, false},
		{"invalid", 0, 0, true},
		{"truncated", 16, 16, true},
		{"pixel budget", 16, 16, true},
		{"checksum", 16, 16, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("not a PNG")
			if tt.width > 0 {
				src := image.NewNRGBA(image.Rect(0, 0, tt.width, tt.height))
				_, err := rand.New(rand.NewSource(1)).Read(src.Pix)
				require.NoError(t, err)
				var buf bytes.Buffer
				require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, src))
				data = buf.Bytes()
				if tt.name == "pixel budget" {
					binary.BigEndian.PutUint32(data[16:20], uint32(maxDecodePixels))
					binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
				} else if tt.name == "checksum" {
					data[len(data)-1] ^= 1
				} else if tt.invalid {
					data = data[:len(data)-16]
				} else {
					require.Greater(t, len(data), maxImageBytes)
				}
			}
			dir := t.TempDir()
			fake := &imageGenFake{image: llmapi.Image{PNG: data}}
			ctx := agent.WithParentToolCallID(agent.WithDialogueID(context.Background(), "dialogue"), "call")
			result := NewImageGen(fake, llmapi.ModelEntry{}, localFS{}, dir).Execute(ctx, `{"prompt":"x"}`)
			name := filepath.Join(dir, "dialogue", "call.png")
			if tt.invalid {
				require.True(t, result.IsError, result.Content)
				if tt.name == "pixel budget" {
					assert.Contains(t, result.Content, "decode budget")
				}
				assert.Empty(t, result.MultiContent)
				_, err := os.Stat(name)
				assert.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.False(t, result.IsError, result.Content)
			saved, err := os.ReadFile(name)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(data, saved), "original PNG must be saved byte-for-byte")
			require.Len(t, result.MultiContent, 2)
			preview, err := decodeImageGenURI(result.MultiContent[1].ImageURL)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(preview), maxImageBytes)
			decoded, err := png.Decode(bytes.NewReader(preview))
			require.NoError(t, err)
			assert.LessOrEqual(t, decoded.Bounds().Dx(), maxImageEdge)
			assert.LessOrEqual(t, decoded.Bounds().Dy(), maxImageEdge)
			_, _, _, alpha := decoded.At(0, 0).RGBA()
			assert.Less(t, alpha, uint32(65535), "preview must retain transparency")
		})
	}
}

func TestImageGenReferences(t *testing.T) {
	uri := func(s string) llmapi.ContentPart {
		return llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(s))}
	}
	messages := []llmapi.Message{
		{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{uri("old"), uri("attachment")}},
		{Role: llmapi.RoleAssistant, MultiContent: []llmapi.ContentPart{uri("ignored")}},
		{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{uri("read_file"), uri("browser"), uri("generated")}},
	}
	ctx := agent.WithConversation(agent.WithParentToolCallID(agent.WithDialogueID(context.Background(), "dialogue"), "call"), messages)
	for _, tt := range []struct {
		name, args string
		want       [][]byte
	}{
		{"recent", `{"prompt":"edit","num_last_images_to_include":4}`, [][]byte{[]byte("attachment"), []byte("read_file"), []byte("browser"), []byte("generated")}},
		{"latest", `{"prompt":"edit","num_last_images_to_include":1}`, [][]byte{[]byte("generated")}},
		{"file", `{"prompt":"edit","referenced_image_paths":["FILE"]}`, [][]byte{[]byte("file")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "input.png")
			require.NoError(t, os.WriteFile(name, []byte("file"), 0o600))
			var buf bytes.Buffer
			require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
			fake := &imageGenFake{image: llmapi.Image{PNG: buf.Bytes()}}
			tool := NewImageGen(fake, llmapi.ModelEntry{}, localFS{}, dir)
			result := tool.Execute(ctx, strings.ReplaceAll(tt.args, "FILE", name))
			require.False(t, result.IsError, result.Content)
			require.Len(t, fake.requests, 1)
			assert.Equal(t, tt.want, fake.requests[0].Images)
		})
	}
	_, err := recentImageGenImages(messages, 6)
	assert.EqualError(t, err, "requested the last 6 conversation images, but only 5 were available")
}

func TestImageGenLargeReferences(t *testing.T) {
	encode := func(t *testing.T, width, height int, noisy bool) []byte {
		src := image.NewNRGBA(image.Rect(0, 0, width, height))
		if noisy {
			_, err := rand.New(rand.NewSource(1)).Read(src.Pix)
			require.NoError(t, err)
		}
		var buf bytes.Buffer
		require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, src))
		return buf.Bytes()
	}
	for _, tt := range []struct {
		name      string
		write     func(t *testing.T, name string) int64
		wantErr   string
		wantWidth int
	}{
		{
			name: "large dimensions are downscaled",
			write: func(t *testing.T, name string) int64 {
				data := encode(t, 2100, 1000, false)
				require.Greater(t, len(data), maxImageBytes)
				require.NoError(t, os.WriteFile(name, data, 0o600))
				return int64(len(data))
			},
			wantWidth: maxImageEdge,
		},
		{
			name: "large file within the edge cap",
			write: func(t *testing.T, name string) int64 {
				data := encode(t, 1200, 1200, true)
				require.Greater(t, len(data), maxImageBytes)
				require.NoError(t, os.WriteFile(name, data, 0o600))
				return int64(len(data))
			},
			wantErr: "image file input.png is too large to send to the model (%d bytes, max 5242880 bytes / 5 MiB). Reduce the image size (resize or recompress) and try again",
		},
		{
			name: "file over the read cap",
			write: func(t *testing.T, name string) int64 {
				require.NoError(t, os.WriteFile(name, nil, 0o600))
				require.NoError(t, os.Truncate(name, maxReferenceImageFileBytes+1))
				return maxReferenceImageFileBytes + 1
			},
			wantErr: "referenced image input.png is larger than 64 MiB; resize or recompress it and try again",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "input.png")
			size := tt.write(t, name)
			var buf bytes.Buffer
			require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
			fake := &imageGenFake{image: llmapi.Image{PNG: buf.Bytes()}}
			ctx := agent.WithParentToolCallID(agent.WithDialogueID(context.Background(), "dialogue"), "call")
			result := NewImageGen(fake, llmapi.ModelEntry{}, localFS{}, dir).
				Execute(ctx, `{"prompt":"edit","referenced_image_paths":["`+name+`"]}`)
			if tt.wantErr != "" {
				require.True(t, result.IsError)
				want := tt.wantErr
				if strings.Contains(want, "%d") {
					want = fmt.Sprintf(want, size)
				}
				assert.Equal(t, "image generation failed: "+want, result.Content)
				assert.Empty(t, fake.requests)
				return
			}
			require.False(t, result.IsError, result.Content)
			require.Len(t, fake.requests, 1)
			require.Len(t, fake.requests[0].Images, 1)
			cfg, err := png.DecodeConfig(bytes.NewReader(fake.requests[0].Images[0]))
			require.NoError(t, err)
			assert.Equal(t, tt.wantWidth, cfg.Width)
		})
	}
}

func TestImageGenErrors(t *testing.T) {
	ctx := agent.WithParentToolCallID(agent.WithDialogueID(context.Background(), "dialogue"), "call")
	dir := t.TempDir()
	fake := &imageGenFake{err: errors.New("usage limit reached; resets at 2030-01-02T03:04:05Z")}
	tool := NewImageGen(fake, llmapi.ModelEntry{}, localFS{}, dir)
	result := tool.Execute(ctx, `{"prompt":"x"}`)
	assert.Equal(t, "image generation failed: "+fake.err.Error(), result.Content)
	assert.True(t, result.IsError)
	_, err := os.Stat(filepath.Join(dir, "dialogue", "call.png"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	fake.err = nil
	assert.Contains(t, tool.Execute(ctx, `{"prompt":"x"}`).Content, "returned no image data")
	assert.True(t, NewImageGen(nil, llmapi.ModelEntry{}, localFS{}, dir).Execute(ctx, `{"prompt":"x"}`).IsError)

	large := "data:image/png;base64," + strings.Repeat("A", 8*1024*1024)
	ctx = agent.WithConversation(ctx, []llmapi.Message{{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
		{Type: llmapi.ContentPartTypeImageURL, ImageURL: large},
		{Type: llmapi.ContentPartTypeImageURL, ImageURL: large},
	}}})
	fake.requests = nil
	result = tool.Execute(ctx, `{"prompt":"edit","num_last_images_to_include":2}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "15 MiB encoded size budget")
	assert.Empty(t, fake.requests)
	_, err = decodeImageGenURI("https://example.com/image.png")
	assert.ErrorContains(t, err, "attach the image again")
}

func TestGeneratedImagePath(t *testing.T) {
	for _, name := range []string{"/data/generated_images/dialogue/call.png", "/data as space/dir as x/call.png"} {
		got, ok := GeneratedImagePath(generatedImageHint(name))
		assert.True(t, ok)
		assert.Equal(t, name, got)
	}
	for _, output := range []string{"", "/tmp/image.png", generatedImageHint("relative.png"), generatedImageHint("/tmp/image.png") + "extra", strings.ReplaceAll(generatedImageHint("/tmp/image.png"), "saved to /tmp as", "saved to /other as")} {
		_, ok := GeneratedImagePath(output)
		assert.False(t, ok)
	}
}
