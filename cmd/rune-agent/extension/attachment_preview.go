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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// Legacy attachment content parts double as the durable record of what
// was attached to a turn, so the chip grid can be rebuilt on replay.
// These prefixes are the contract for messages persisted before the v1
// canonical format below; replay still falls back to them.
const (
	attachedFilePrefix   = "Attached file "
	attachedImagePrefix  = "Attached image "
	attachedSymbolPrefix = "Attached symbol "
)

// attachmentPartKind identifies what a canonical v1 attachment content
// part carries.
type attachmentPartKind string

const (
	attachmentKindFile   attachmentPartKind = "file"
	attachmentKindSymbol attachmentPartKind = "symbol"
	attachmentKindImage  attachmentPartKind = "image"
)

const (
	attachmentStatusOK    = "ok"
	attachmentStatusError = "error"
)

// attachmentPartV1Prefix marks a content part as the canonical v1
// attachment record: a single header line, `attachmentPartV1Prefix` plus
// JSON metadata, optionally followed by a newline and the attachment's
// body.
const attachmentPartV1Prefix = "Rune attachment v1: "

// attachmentPartMeta is the JSON header of a canonical v1 attachment
// content part. It is the durable record of what was attached to a turn,
// so replay reconstructs the chip grid from it instead of a parallel copy
// that could drift from what the model actually received.
type attachmentPartMeta struct {
	ID     string             `json:"id"`
	Kind   attachmentPartKind `json:"kind"`
	Name   string             `json:"name"`
	Path   string             `json:"path,omitempty"`
	Symbol string             `json:"symbol,omitempty"`
	Status string             `json:"status"`
	Error  string             `json:"error,omitempty"`
}

// encodeAttachmentPart renders meta and body as a canonical v1 content
// part. body is omitted entirely when empty, which is how an image's
// header line stays immediately followed by its image content part.
func encodeAttachmentPart(meta attachmentPartMeta, body string) string {
	header, err := json.Marshal(meta)
	if err != nil {
		// meta is built entirely from fields already known to marshal
		// cleanly; degrade rather than lose the attachment record.
		header = []byte("{}")
	}
	if body == "" {
		return attachmentPartV1Prefix + string(header)
	}
	return attachmentPartV1Prefix + string(header) + "\n" + body
}

// parseAttachmentPartV1 parses a canonical v1 content part. It reports
// false for any text that is not v1, including a v1-prefixed header with
// malformed JSON.
func parseAttachmentPartV1(text string) (attachmentPartMeta, string, bool) {
	rest, ok := strings.CutPrefix(text, attachmentPartV1Prefix)
	if !ok {
		return attachmentPartMeta{}, "", false
	}
	head, body, _ := strings.Cut(rest, "\n")
	var meta attachmentPartMeta
	if err := json.Unmarshal([]byte(head), &meta); err != nil {
		return attachmentPartMeta{}, "", false
	}
	return meta, body, true
}

// attachmentFromV1 rebuilds the chip dialoguetui.Attachment a v1 content
// part was rendered from. Status is not consulted: even a failed
// attachment still gets a chip, matching the legacy prefix behavior.
func attachmentFromV1(meta attachmentPartMeta, body string) dialoguetui.Attachment {
	if meta.Kind == attachmentKindSymbol {
		name := meta.Symbol
		if name == "" {
			name = meta.Name
		}
		return dialoguetui.NewResolvedSymbolAttachment(name, body)
	}
	path := meta.Path
	if path == "" {
		path = meta.Name
	}
	if filepath.IsAbs(path) {
		return dialoguetui.NewAttachment(path)
	}
	return dialoguetui.NewWorkspaceFileAttachment(path)
}

// attachmentContentParts reads the files the user attached to the chat
// and renders them as canonical v1 content parts for the next user
// message. A file that cannot be read, or that exceeds the model's image
// caps, becomes a status:error part so the rest of the message still
// goes through; the failure is also surfaced as a notification.
func (h *aiEditorHandler) attachmentContentParts(
	ctx context.Context,
	attachments []finalizedAttachment,
) []llmapi.ContentPart {
	if len(attachments) == 0 {
		return nil
	}
	maxOutput := h.maxToolOutputBytes
	if maxOutput <= 0 {
		maxOutput = agent.DefaultMaxToolOutputBytes
	}
	textPart := func(s string) llmapi.ContentPart {
		return llmapi.ContentPart{Type: llmapi.ContentPartTypeText, Text: s}
	}
	parts := make([]llmapi.ContentPart, 0, len(attachments)+1)
	for _, a := range attachments {
		meta := attachmentPartMeta{ID: a.id, Name: a.Label}
		if a.ID != "" {
			// Virtual attachments (e.g. the /chatreviewchanges comments)
			// carry inline content directly and are never replayed as a
			// chip from persisted content, so they skip the v1 envelope.
			parts = append(parts, textPart(fmt.Sprintf("%s\n%s",
				reviewAttachmentHeading(a.ID), a.Content)))
			continue
		}
		if a.Kind == dialoguetui.AttachmentSymbol {
			meta.Kind = attachmentKindSymbol
			meta.Symbol = a.Symbol
			meta.Status = attachmentStatusOK
			content := a.Content
			if content == "" {
				content = h.symbolContext(ctx, a.Symbol)
			}
			parts = append(parts, textPart(encodeAttachmentPart(meta, content)))
			continue
		}
		meta.Path = a.Path
		data, err := readWorkspaceFile(h.fs, a.Path)
		if err != nil {
			_, _ = h.n.Notify(browserapi.LevelError,
				"attachment %s: %v", a.Name, err)
			meta.Kind = attachmentKindFile
			meta.Status = attachmentStatusError
			meta.Error = err.Error()
			parts = append(parts, textPart(encodeAttachmentPart(meta,
				fmt.Sprintf("could not be read: %v", err))))
			continue
		}
		if mime, isImage := agentools.ImageMediaType(a.Path); isImage {
			meta.Kind = attachmentKindImage
			uri, encErr := agentools.EncodeImageDataURI(a.Path, data, mime)
			if encErr != nil {
				_, _ = h.n.Notify(browserapi.LevelError,
					"attachment %s: %v", a.Name, encErr)
				meta.Status = attachmentStatusError
				meta.Error = encErr.Error()
				parts = append(parts, textPart(encodeAttachmentPart(meta,
					fmt.Sprintf("could not be sent: %v", encErr))))
				continue
			}
			meta.Status = attachmentStatusOK
			parts = append(parts,
				textPart(encodeAttachmentPart(meta, "")),
				llmapi.ContentPart{
					Type:     llmapi.ContentPartTypeImageURL,
					ImageURL: uri,
				})
			continue
		}
		meta.Kind = attachmentKindFile
		meta.Status = attachmentStatusOK
		if utf8validate.IsBinary(data) {
			parts = append(parts, textPart(encodeAttachmentPart(meta,
				utf8validate.BinaryStub(a.Name, len(data), sha256.Sum256(data)))))
			continue
		}
		parts = append(parts, textPart(encodeAttachmentPart(meta,
			agent.TruncateMiddle(utf8validate.Sanitize(string(data)), maxOutput))))
	}
	return parts
}

// attachmentPreviewer opens attachment previews for one chat.
type attachmentPreviewer struct {
	h *aiEditorHandler
}

func (p attachmentPreviewer) OpenAttachment(a dialoguetui.Attachment) {
	// Resolving a symbol runs three LSP tools, so keep it off the event
	// loop that delivered the click.
	go debug.CapturePanicReport(func() { p.h.openAttachmentPreview(a) })
}

func (h *aiEditorHandler) openAttachmentPreview(a dialoguetui.Attachment) {
	var err error
	if a.Kind == dialoguetui.AttachmentSymbol {
		content := a.Content
		if content == "" {
			content = h.symbolContext(h.ctx, a.Symbol)
		}
		_, err = h.openMarkdownPreview(content, nil)
	} else {
		err = h.openFileAttachment(a.Path)
	}
	if err != nil {
		_, _ = h.n.Notify(browserapi.LevelError,
			"preview %s: %v", a.Name, err)
	}
}

// openFileAttachment opens the attached file as a normal editor tab and
// focuses it, so the user lands on the real buffer rather than a
// throwaway copy.
func (h *aiEditorHandler) openFileAttachment(path string) error {
	uri, err := h.fs.URI(path)
	if err != nil {
		return err
	}
	bh, err := h.o.Open(uri)
	if err != nil {
		return err
	}
	win, err := h.wm.Focus()
	if err != nil {
		return err
	}
	if err := h.wm.SetWindowContent(win, bh); err != nil &&
		!errors.Is(err, browserapi.ErrTabNotFree) {
		return err
	}
	return nil
}

// openMarkdownPreview floats markdown content, such as a symbol's
// definition, references and documentation, rendered by the markdown
// handler to keep links, folds and code highlighting. onClose, if not nil,
// is called with the returned window once the preview is released, which
// does not close the window: it may already show other content.
func (h *aiEditorHandler) openMarkdownPreview(
	content string, onClose func(browserapi.Window),
) (browserapi.Window, error) {
	cfg := defaultMarkdownConfig()
	cfg.Parser = h.parser
	md, err := markdown.NewWithConfig(content, *cfg)
	if err != nil {
		return nil, err
	}
	mdh := mdhandler.New(md)
	// Keep a margin around rendered content without breaking the markdown
	// handler's mouse coordinates, selection, or scrolling.
	padded := newPreviewSpan(mdh)
	var win browserapi.Window
	bhandler := browserapi.FuncHandler(padded, func() error {
		if onClose != nil {
			onClose(win)
		}
		return mdh.Close()
	})
	floating := browserapi.FuncFloating(bhandler, func() (int, int) {
		return floatingWidth, min(padded.Height(floatingWidth), floatingHeight)
	})
	win, err = h.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	return win, err
}

func newPreviewSpan(mdh *mdhandler.Handler) *handler.Span {
	return handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentHorizontallyCentered,
	})
}

// toolCallPreviewer opens a read-only view of a tool's original arguments
// and full output, independent of transcript truncation. A tool call has
// at most one preview: opening it again replaces the open one, which both
// refocuses it and refreshes a response that was still running.
type toolCallPreviewer struct {
	h *aiEditorHandler

	mu   sync.Mutex
	open map[dialoguetui.ToolCallKey]browserapi.Window
}

func newToolCallPreviewer(h *aiEditorHandler) *toolCallPreviewer {
	return &toolCallPreviewer{
		h:    h,
		open: make(map[dialoguetui.ToolCallKey]browserapi.Window),
	}
}

func (p *toolCallPreviewer) OpenToolCall(details dialoguetui.ToolCallDetails) {
	p.mu.Lock()
	prev, ok := p.open[details.Key]
	p.mu.Unlock()
	if ok {
		if err := p.h.wm.CloseWindow(prev); err != nil {
			_, _ = p.h.n.Notify(browserapi.LevelError,
				"preview %s: %v", details.Name, err)
			return
		}
	}
	win, err := p.h.openMarkdownPreview(toolCallPreviewMarkdown(details),
		func(closed browserapi.Window) {
			p.mu.Lock()
			defer p.mu.Unlock()
			// A replaced window reports its close asynchronously, possibly
			// after its replacement is already tracked.
			if p.open[details.Key] == closed {
				delete(p.open, details.Key)
			}
		})
	if err != nil {
		_, _ = p.h.n.Notify(browserapi.LevelError,
			"preview %s: %v", details.Name, err)
		return
	}
	p.mu.Lock()
	p.open[details.Key] = win
	p.mu.Unlock()
}

// toolPreviewField names a string field of a JSON object that a preview
// lifts out of the generic JSON block into a fenced block of its own,
// highlighted as language. A zero field leaves the JSON block as is.
type toolPreviewField struct {
	name, language string
}

// toolPreview specializes the preview of a tool whose arguments or
// response carry code, terminal output or multi-line prose, which reads
// poorly as an escaped JSON string.
type toolPreview struct {
	args, output toolPreviewField
}

// toolPreviews is keyed by tool name. A call that does not have the
// expected shape, such as arguments that are still streaming in or an
// error message in place of a JSON response, gets the generic preview.
var toolPreviews = map[string]toolPreview{
	"agent":          {args: toolPreviewField{"prompt", "markdown"}},
	"bash":           {args: toolPreviewField{"command", "bash"}},
	"run_command":    {args: toolPreviewField{"CommandLine", "bash"}},
	"exec_command":   {args: toolPreviewField{"cmd", "bash"}, output: toolPreviewField{"output", "text"}},
	"write_stdin":    {output: toolPreviewField{"output", "text"}},
	"apply_patch":    {args: toolPreviewField{"patch", "diff"}},
	"query_ast":      {args: toolPreviewField{"query", "query"}},
	"query_file_ast": {args: toolPreviewField{"query", "query"}},
}

// split returns the value of the field in the JSON object raw and the
// object's remaining fields as indented JSON sorted by key, or "" when
// none remain. ok is false when f is zero, raw is not an object, or the
// field is not a non-empty string.
func (f toolPreviewField) split(raw string) (value, rest string, ok bool) {
	if f.name == "" {
		return "", "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return "", "", false
	}
	if json.Unmarshal(fields[f.name], &value) != nil || value == "" {
		return "", "", false
	}
	delete(fields, f.name)
	if len(fields) == 0 {
		return value, "", true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Escaping would show <, > and & as \u003c, \u003e and \u0026.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(fields) != nil {
		return "", "", false
	}
	return value, strings.TrimSuffix(buf.String(), "\n"), true
}

func toolCallPreviewMarkdown(details dialoguetui.ToolCallDetails) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", details.Name)
	spec := toolPreviews[details.Name]
	if code, rest, ok := spec.args.split(details.Args); ok {
		// The summary is omitted because every whitelisted tool derives
		// it from the arguments shown here.
		writePreviewCode(&b, spec.args.language, code)
		if rest != "" {
			b.WriteString("## Arguments\n\n")
			writePreviewCode(&b, "json", rest)
		}
	} else {
		writeGenericArgs(&b, details)
	}
	if details.Output != "" {
		if details.IsError {
			b.WriteString("## Error\n\n")
		} else {
			b.WriteString("## Result\n\n")
		}
		if details.MarkdownOutput && !details.IsError {
			b.WriteString(details.Output)
			b.WriteByte('\n')
		} else if text, rest, ok := spec.output.split(details.Output); ok {
			writePreviewCode(&b, spec.output.language, text)
			if rest != "" {
				writePreviewCode(&b, "json", rest)
			}
		} else {
			language := "text"
			if json.Valid([]byte(details.Output)) {
				language = "json"
			}
			writePreviewCode(&b, language, details.Output)
		}
	} else if !details.Done {
		b.WriteString("_Waiting for result…_\n")
	} else if details.IsError {
		b.WriteString("## Error\n\n_(no output)_\n")
	} else {
		b.WriteString("## Result\n\n_(no output)_\n")
	}
	return b.String()
}

func writeGenericArgs(b *strings.Builder, details dialoguetui.ToolCallDetails) {
	if details.Summary != "" {
		writePreviewCode(b, "text", details.Summary)
	}
	if details.Args != "" {
		args := details.Args
		if json.Valid([]byte(args)) {
			var formatted bytes.Buffer
			if json.Indent(&formatted, []byte(args), "", "  ") == nil {
				args = formatted.String()
			}
		}
		b.WriteString("## Arguments\n\n")
		writePreviewCode(b, "json", args)
	}
}

func writePreviewCode(b *strings.Builder, language, text string) {
	// A longer fence keeps arbitrary tool output (including markdown fences)
	// literal, rather than turning part of it into markdown prose.
	fence := "````"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	fmt.Fprintf(b, "%s%s\n%s\n%s\n\n", fence, language, text, fence)
}

// replayedAttachments rebuilds the chip grid for a stored user message.
// The attachment content parts are the durable record, so replay reads
// those instead of a parallel copy that could drift from what the model
// actually received. The first part is the message text itself.
func replayedAttachments(msg llmapi.Message) []dialoguetui.Attachment {
	if len(msg.MultiContent) < 2 {
		return nil
	}
	var out []dialoguetui.Attachment
	for _, p := range msg.MultiContent[1:] {
		if p.Type != llmapi.ContentPartTypeText {
			continue
		}
		if a, ok := parseAttachmentPart(p.Text); ok {
			out = append(out, a)
		}
	}
	return out
}

func parseAttachmentPart(text string) (dialoguetui.Attachment, bool) {
	if meta, body, ok := parseAttachmentPartV1(text); ok {
		return attachmentFromV1(meta, body), true
	}
	return parseLegacyAttachmentPart(text)
}

// parseLegacyAttachmentPart parses the "Attached file/image/symbol "
// prefixed content parts persisted before the v1 canonical format.
func parseLegacyAttachmentPart(text string) (dialoguetui.Attachment, bool) {
	for _, c := range []struct {
		prefix string
		symbol bool
	}{
		{attachedSymbolPrefix, true},
		{attachedFilePrefix, false},
		{attachedImagePrefix, false},
	} {
		rest, ok := strings.CutPrefix(text, c.prefix)
		if !ok {
			continue
		}
		head, _, _ := strings.Cut(rest, "\n")
		name := strings.TrimSuffix(head, ":")
		if i := strings.Index(name, " could not "); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			return dialoguetui.Attachment{}, false
		}
		switch {
		case c.symbol:
			_, body, _ := strings.Cut(text, "\n")
			return dialoguetui.NewResolvedSymbolAttachment(name, body), true
		case filepath.IsAbs(name):
			// Dragged and pasted files were resolved to absolute paths
			// and keep their own icon.
			return dialoguetui.NewAttachment(name), true
		default:
			return dialoguetui.NewWorkspaceFileAttachment(name), true
		}
	}
	return dialoguetui.Attachment{}, false
}
