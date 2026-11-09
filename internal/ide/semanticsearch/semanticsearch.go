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

// Package semanticsearch implements the searchsemantic command: a search
// of the workspace by what code does, picked from a floating list whose
// preview shows why each result matched. Its first use offers to set up
// what meaning search runs on, so that Rune ships neither.
package semanticsearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmsearch"
)

// Name is the name of the command.
const Name = "searchsemantic"

const synopsis = "[-r <dir>]... [-n <limit>] [--meaning=false] [--prefix-last] [--] <query...>"

// Manual returns the manual of the command.
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name: Name,
		Summary: "Search the workspace by what code does, not only by its words, and pick a " +
			"result from a floating list. The preview emphasizes the matched words, and the " +
			"status bar at the bottom says how the result ranked. When meaning search fails, " +
			"it searches by words instead and says so in red. " +
			"`-r` limits the search to a folder (repeatable), `-n` sets how many results to " +
			"list (20 by default), `--meaning=false` searches by words alone and " +
			"`--prefix-last` also matches the last word as a prefix. The first use offers to " +
			"install llama-server and download the embedding model meaning search runs on.",
		Synopsis: synopsis,
	}
}

var (
	errUsage   = errors.New("usage: " + Name + " " + synopsis)
	errNoIndex = errors.New(
		"this workspace has no search index: search.semantic_enabled is off, or the index failed to open")
)

// Server is the llama-server that meaning search runs the embedding
// model on.
type Server interface {
	// Installed reports whether it is installed, and whether the version
	// installed is too old to run the embedding model, without offering
	// to install it.
	Installed(ctx context.Context) (installed, outdated bool, err error)
	// Install installs its latest version, showing the user its progress.
	Install(ctx context.Context) error
}

// Downloader downloads local models; *llamacpp.Registry is one.
type Downloader interface {
	Download(ctx context.Context, ref llamacpp.Reference, progress llamacpp.ProgressFunc) (
		*llamacpp.DownloadResult, error)
}

// PromptOpener asks the user to pick an option; text.Component is one.
type PromptOpener interface {
	Prompt(message string, options []string, bindings []term.KeyComb,
		h handler.PromptHandler) browser.Window
}

// Config is what the command works with. Every field is required.
type Config struct {
	// Root is the workspace searched.
	Root     workspaceapi.URI
	Searcher searchapi.Searcher
	// Search holds the index of Root and knows the embedding model.
	Search *llmsearch.Service
	Models Downloader
	Server Server
	// Clients are the picker's. Its notifications report the setup, too.
	Clients          finder.Clients
	Parser           syntaxapi.Parser
	Prompt           PromptOpener
	ScheduleNextTick func(func()) bool
}

// Handler implements the command as a text.CommandHandler.
type Handler struct {
	cfg Config
	// settingUp is set from when the user agrees to the setup until it
	// ends.
	settingUp atomic.Bool
}

// New returns the command's handler.
func New(cfg Config) *Handler {
	return &Handler{cfg: cfg}
}

// HandleCommand runs a search. It returns right away: the setup it may
// offer, and the search itself, finish in the background.
func (h *Handler) HandleCommand(_ context.Context, cmd textapi.Command) error {
	req, err := parseArgs(cmd.Args, h.cfg.Root)
	if err != nil {
		return err
	}
	if _, ok := h.cfg.Search.Index(h.cfg.Root); !ok {
		return errNoIndex
	}
	if !req.Meaning {
		return h.open(req)
	}
	if h.settingUp.Load() {
		return errors.New(Name + " is still being set up")
	}
	go debug.CapturePanicReport(func() {
		need, err := h.needs(context.Background())
		h.cfg.ScheduleNextTick(func() {
			switch {
			case err == nil && need.any():
				h.offerSetup(need)
				return
			case err == nil:
				err = h.open(req)
			}
			if err != nil {
				h.notifyError(err)
			}
		})
	})
	return nil
}

// flagNames are the flags Complete offers.
var flagNames = []string{"--root", "--limit", "--meaning=false", "--prefix-last"}

// Complete offers the flags.
func (h *Handler) Complete(_ context.Context, cmd textapi.Command) (
	blueiterator.Iterator[string], string, error,
) {
	n := len(cmd.Args)
	if n == 0 || !strings.HasPrefix(cmd.Args[n-1], "-") {
		return blueiterator.FromSlice[string](nil), "", nil
	}
	var names []string
	for _, f := range flagNames {
		if strings.HasPrefix(f, cmd.Args[n-1]) {
			names = append(names, f)
		}
	}
	return blueiterator.FromSlice(names), "", nil
}

// parseArgs reads the command's flags; the rest of its args are the
// query. Roots may be absolute paths in root.
func parseArgs(args []string, root workspaceapi.URI) (searchapi.SemanticRequest, error) {
	fs := pflag.NewFlagSet(Name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	roots := fs.StringArrayP("root", "r", nil, "search only in this folder")
	limit := fs.IntP("limit", "n", 0, "list at most this many results")
	meaning := fs.Bool("meaning", true, "search by meaning, not only by words")
	prefixLast := fs.Bool("prefix-last", false, "match the last word as a prefix too")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return searchapi.SemanticRequest{}, errUsage
		}
		return searchapi.SemanticRequest{}, fmt.Errorf("%s: %w", Name, err)
	}
	if *limit < 0 {
		return searchapi.SemanticRequest{}, fmt.Errorf("%s: --limit must not be negative", Name)
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return searchapi.SemanticRequest{}, errUsage
	}
	for i, r := range *roots {
		if rel, err := filepath.Rel(root.Path(), r); filepath.IsAbs(r) && err == nil {
			(*roots)[i] = rel
		}
	}
	return searchapi.SemanticRequest{
		Query: query, Roots: *roots, Limit: *limit, Meaning: *meaning, PrefixLast: *prefixLast,
	}, nil
}

// needs is what meaning search is missing.
type needs struct {
	server, upgrade, model bool
}

func (n needs) any() bool { return n.server || n.upgrade || n.model }

func (h *Handler) needs(ctx context.Context) (needs, error) {
	installed, outdated, err := h.cfg.Server.Installed(ctx)
	if err != nil {
		return needs{}, fmt.Errorf("llama-server: %w", err)
	}
	_, err = h.cfg.Search.EmbeddingModel(ctx)
	if err != nil && !errors.Is(err, llmapi.ErrModelNotFound) {
		return needs{}, err
	}
	return needs{server: !installed, upgrade: outdated, model: err != nil}, nil
}

const (
	yesOpt = "    Yes    "
	noOpt  = "    No    "
)

// offerSetup asks the user whether to set up what meaning search needs,
// and does if they agree. It runs on the event loop.
func (h *Handler) offerSetup(need needs) {
	model := h.cfg.Search.ConfiguredModel().Name
	h.cfg.Prompt.Prompt(setupMessage(need, model), []string{yesOpt, noOpt},
		[]term.KeyComb{{Ch: 'y'}, {Ch: 'n'}},
		handler.FuncPromptHandler(func(_ int, option string) {
			if option != yesOpt || !h.settingUp.CompareAndSwap(false, true) {
				return
			}
			go debug.CapturePanicReport(func() {
				defer h.settingUp.Store(false)
				h.setUp(context.Background(), need, model)
			})
		}, func() error { return nil }))
}

func setupMessage(need needs, model string) string {
	var steps []string
	switch {
	case need.server:
		steps = append(steps, "installing the **llama-server** package")
	case need.upgrade:
		steps = append(steps, "upgrading the **llama-server** package")
	}
	if need.model {
		name, from := modelSource(model)
		steps = append(steps, fmt.Sprintf("downloading the **%s** model from %s", name, from))
	}
	return fmt.Sprintf("This command requires %s. Do you want to do that?", strings.Join(steps, " and "))
}

// modelSource splits a model reference into what the user knows it by
// and where it comes from.
func modelSource(model string) (name, from string) {
	ref, err := llamacpp.ParseReference(model)
	if err != nil {
		return model, "its registry"
	}
	from = ref.Host
	if from == llamacpp.DefaultRegistryHost {
		from = "Hugging Face"
	}
	ref.Host = ""
	return ref.String(), from
}

// setUp installs what need says is missing, then has the index adopt
// the model and tells the user the command is ready.
func (h *Handler) setUp(ctx context.Context, need needs, model string) {
	if need.server || need.upgrade {
		if err := h.cfg.Server.Install(ctx); err != nil {
			h.cfg.ScheduleNextTick(func() { h.notifyError(fmt.Errorf("install llama-server: %w", err)) })
			return
		}
		if _, outdated, err := h.cfg.Server.Installed(ctx); err == nil && outdated {
			h.cfg.ScheduleNextTick(func() {
				h.notifyError(errors.New("the latest llama-server package is too old to run the embedding model"))
			})
			return
		}
	}
	if need.model {
		if err := h.download(ctx, model); err != nil {
			h.cfg.ScheduleNextTick(func() { h.notifyError(err) })
			return
		}
	}
	if idx, ok := h.cfg.Search.Index(h.cfg.Root); ok {
		idx.Rescan()
	}
	h.cfg.ScheduleNextTick(func() {
		_, _ = h.cfg.Clients.Notifications.Notify(browserapi.LevelInfo,
			"You can now use the command `%s`", Name)
	})
}

func (h *Handler) download(ctx context.Context, model string) error {
	name, _ := modelSource(model)
	ref, err := llamacpp.ParseReference(model)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	p := &progress{
		notify:   h.cfg.Clients.Notifications,
		schedule: h.cfg.ScheduleNextTick,
		title:    "Downloading " + name,
	}
	p.start()
	_, err = h.cfg.Models.Download(ctx, ref, p.update)
	if err != nil {
		p.finish("Could not download " + name)
		return fmt.Errorf("download %s: %w", name, err)
	}
	p.finish("Downloaded " + name)
	return nil
}

func (h *Handler) notifyError(err error) {
	_, _ = h.cfg.Clients.Notifications.Notify(browserapi.LevelError, "%s: %v", Name, err)
}

// open floats the picker of the results of req, which it searches in the
// background. It runs on the event loop.
func (h *Handler) open(req searchapi.SemanticRequest) error {
	win, err := h.cfg.Clients.WindowManager.Focus()
	if err != nil {
		return err
	}
	r := newResults(req.Query)
	cfg := locationsearch.DefaultConfig()
	cfg.Source = h.source(r, req)
	cfg.Annotate = r.annotate
	cfg.Label = "SEMANTIC"
	picker, err := locationsearch.New(context.Background(), h.cfg.Clients, win, cfg,
		h.cfg.Parser, h.cfg.ScheduleNextTick, "", nil)
	if err != nil {
		return err
	}
	if _, err := h.cfg.Clients.WindowManager.Floating(picker, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}); err != nil {
		_ = picker.Close()
		return err
	}
	return nil
}

// source runs req and records what it found in r, searching by words
// through the regular expression index instead when meaning search
// fails. Closing the picker cancels ctx.
func (h *Handler) source(r *results, req searchapi.SemanticRequest) func(context.Context) (iterator.Iterator[string], error) {
	return func(ctx context.Context) (iterator.Iterator[string], error) {
		// The picker draws the status of the search, which an empty
		// result would otherwise not redraw.
		defer func() { _ = h.cfg.Clients.Interrupter.Interrupt(context.Background()) }()
		var hits []searchapi.SemanticResult
		it, st, err := h.cfg.Searcher.SemanticSearch(ctx, req)
		if err == nil {
			hits, err = iterator.ToSlice(ctx, it)
		}
		if ctx.Err() != nil {
			return iterator.FromSlice[string](nil), nil
		}
		if why := h.meaningFailed(req, hits, err); why != "" {
			log.Warnf("%s: meaning search failed, searching by words instead: %s", Name, why)
			hits, err = wordSearch(ctx, h.cfg.Searcher, req)
			if err == nil {
				return iterator.FromSlice(r.found(hits, "", true)), nil
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				r.fail(err)
			}
			return iterator.FromSlice[string](nil), nil
		}
		return iterator.FromSlice(r.found(hits, st.Message, false)), nil
	}
}

// meaningFailed says why the search of req did not answer by meaning, if
// it was asked to and failed: with err, or with hits matched by words
// alone because the model could not run, which also leaves the words of
// the files the indexer did not reach unindexed.
func (h *Handler) meaningFailed(req searchapi.SemanticRequest, hits []searchapi.SemanticResult, err error) string {
	switch {
	case !req.Meaning:
		return ""
	case err != nil:
		return err.Error()
	case slices.ContainsFunc(hits, func(h searchapi.SemanticResult) bool { return h.Cosine != 0 }):
		return ""
	}
	if idx, ok := h.cfg.Search.Index(h.cfg.Root); ok {
		return idx.Status().Unavailable()
	}
	return ""
}

// progressInterval spaces the updates of a download's notification.
const progressInterval = 200 * time.Millisecond

// progress is the notification of a download. It is updated as the
// download goes, and finished however it ends: a progress notification
// otherwise stays open.
type progress struct {
	notify   browserapi.Notifications
	schedule func(func()) bool
	title    string
	// id is only used on the event loop.
	id string

	mu   sync.Mutex
	last time.Time
}

func (p *progress) start() {
	p.schedule(func() {
		if id, err := p.notify.Notify(browserapi.LevelInfo, "%s", p.title); err == nil {
			p.id = id
		}
	})
}

// update implements llamacpp.ProgressFunc.
func (p *progress) update(done, total int64) {
	if total <= 0 || done >= total {
		return
	}
	p.mu.Lock()
	now := time.Now()
	if now.Sub(p.last) < progressInterval {
		p.mu.Unlock()
		return
	}
	p.last = now
	p.mu.Unlock()
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				fmt.Sprintf("%s: %d%%", p.title, done*100/total), done, total)
		}
	})
}

func (p *progress) finish(msg string) {
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id, msg, 1, 1)
		}
	})
}
