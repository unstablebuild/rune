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

package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

func TestEncodeParseAttachmentPartV1Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		meta attachmentPartMeta
		body string
	}{
		{
			name: "file with body",
			meta: attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			},
			body: "package main\n",
		},
		{
			name: "symbol with body",
			meta: attachmentPartMeta{
				ID: "attachment-2", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			},
			body: "## Definition\na.go:1",
		},
		{
			name: "image without body",
			meta: attachmentPartMeta{
				ID: "attachment-3", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			},
		},
		{
			name: "file failure keeps id and status but no image",
			meta: attachmentPartMeta{
				ID: "attachment-4", Kind: attachmentKindFile,
				Name: "gone.go", Path: "gone.go",
				Status: attachmentStatusError, Error: "no such file",
			},
			body: "could not be read: no such file",
		},
		{
			name: "unicode name and multiline body",
			meta: attachmentPartMeta{
				ID: "attachment-5", Kind: attachmentKindFile,
				Name: "résumé.pdf", Path: "docs/résumé.pdf", Status: attachmentStatusOK,
			},
			body: "line one\nline two\n日本語",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeAttachmentPart(tc.meta, tc.body)
			gotMeta, gotBody, ok := parseAttachmentPartV1(encoded)
			require.True(t, ok)
			assert.Equal(t, tc.meta, gotMeta)
			assert.Equal(t, tc.body, gotBody)
		})
	}
}

func TestParseAttachmentPartV1RejectsNonV1Text(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"plain text", "please look at the parser"},
		{"legacy file prefix", "Attached file pkg/main.go:\npackage main"},
		{"malformed json header", "Rune attachment v1: {not json"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := parseAttachmentPartV1(tc.text)
			assert.False(t, ok)
		})
	}
}

func TestParseAttachmentPartV1(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{
		{
			name: "workspace file",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			}, "package main"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
			wantOK: true,
		},
		{
			name: "absolute path keeps the dragged-file icon",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "notes.txt", Path: "/tmp/notes.txt", Status: attachmentStatusOK,
			}, "hi"),
			want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
			wantOK: true,
		},
		{
			name: "symbol",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			}, "## Definition\na.go:1"),
			want: dialoguetui.NewResolvedSymbolAttachment(
				"pkg.Symbol", "## Definition\na.go:1"),
			wantOK: true,
		},
		{
			name: "image",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			}, ""),
			want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
			wantOK: true,
		},
		{
			name: "unreadable file still yields a chip",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "gone.go", Path: "pkg/gone.go",
				Status: attachmentStatusError, Error: "no such file",
			}, "could not be read: no such file"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAttachmentContentPartsV1Roundtrip(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "main.go"), []byte("package main"), 0o600))

	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	sent := []dialoguetui.Attachment{
		dialoguetui.NewWorkspaceFileAttachment("main.go"),
		dialoguetui.NewResolvedSymbolAttachment(
			"value", "## Definition\n- `scope.go:3`"),
	}
	finalized := make([]finalizedAttachment, len(sent))
	for i, a := range sent {
		finalized[i] = finalizedAttachment{id: fmt.Sprintf("attachment-%d", i+1), Attachment: a}
	}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 2)
	for _, p := range parts {
		assert.Contains(t, p.Text, attachmentPartV1Prefix)
	}

	msg := llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: "look",
		MultiContent: append([]llmapi.ContentPart{{
			Type: llmapi.ContentPartTypeText, Text: "look",
		}}, parts...),
	}

	assert.Equal(t, sent, replayedAttachments(msg))
}

func TestAttachmentContentPartsV1Failures(t *testing.T) {
	root := t.TempDir()
	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	missing := dialoguetui.NewWorkspaceFileAttachment("gone.go")
	finalized := []finalizedAttachment{{id: "attachment-1", Attachment: missing}}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 1)
	require.Equal(t, llmapi.ContentPartTypeText, parts[0].Type)

	meta, _, ok := parseAttachmentPartV1(parts[0].Text)
	require.True(t, ok)
	assert.Equal(t, "attachment-1", meta.ID)
	assert.Equal(t, attachmentKindFile, meta.Kind)
	assert.Equal(t, attachmentStatusError, meta.Status)
	assert.NotEmpty(t, meta.Error)
}

func TestParseAttachmentPart(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{{
		name:   "workspace file",
		text:   "Attached file pkg/main.go:\npackage main",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
		wantOK: true,
	}, {
		name:   "absolute path keeps the dragged-file icon",
		text:   "Attached file /tmp/notes.txt:\nhi",
		want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
		wantOK: true,
	}, {
		name: "symbol",
		text: "Attached symbol pkg.Symbol:\n## Definition\na.go:1",
		want: dialoguetui.NewResolvedSymbolAttachment(
			"pkg.Symbol", "## Definition\na.go:1"),
		wantOK: true,
	}, {
		name:   "image",
		text:   "Attached image shot.png:",
		want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
		wantOK: true,
	}, {
		name:   "unreadable file still yields a chip",
		text:   "Attached file pkg/gone.go could not be read: no such file",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
		wantOK: true,
	}, {
		name: "ordinary text",
		text: "please look at the parser",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReplayedAttachmentsIgnoresPlainMessages(t *testing.T) {
	assert.Nil(t, replayedAttachments(llmapi.Message{
		Role: llmapi.RoleUser, Content: "Attached file foo.go: is what I meant",
	}))
}
