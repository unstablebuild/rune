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

package ide

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	yaml "gopkg.in/yaml.v3"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/component/shader/shaderloop"
	tconfig "unstable.build/rune/internal/config"
	"unstable.build/rune/internal/extension/extutil"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/search"
	"unstable.build/rune/internal/handler/searchbox"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/starlarkconfig"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace"
)

const (
	inputEsc               = "esc"
	inputAlt               = "alt"
	inputMouse             = "mouse"
	inputCurrent           = "current"
	editorModeVim          = "vim"
	editorModeStandard     = "standard"
	editorModeEmacs        = "emacs"
	editorModeHelix        = "helix"
	editorModeModal        = "modal"    // deprecated alias for editorModeVim
	editorModeModeless     = "modeless" // deprecated alias for editorModeStandard
	editorModeExo          = "exo"
	editorFallbackVim      = "vim"
	editorFallbackStandard = "standard"
	editorFallbackEmacs    = "emacs"
	editorFallbackHelix    = "helix"
	editorFallbackModal    = "modal"    // deprecated alias for editorFallbackVim
	editorFallbackModeless = "modeless" // deprecated alias for editorFallbackStandard
	editorSectionVim       = "vim"
	editorSectionModal     = "modal" // deprecated alias for editorSectionVim
	keyCommandAliases      = "aliases"
	keyCommandKey          = "key"
	keyCommandHistoryKey   = "history_key"

	keyCommandKeyBindingHintColor = "key_binding_hint_color"

	keyCommandKeyBindingHintFocusColor = "key_binding_hint_focus_color"

	keyGUI       = "gui"
	keyQuickMenu = "quick_menu"

	// maxQuickMenuButtons caps the reserved column so a runaway config
	// cannot produce more buttons than the window can display.
	maxQuickMenuButtons = 24
)

// quickMenuSymbolRe is the SF Symbol name grammar. It also excludes the
// control bytes that would truncate the name at the native bridge's
// C string boundary.
var quickMenuSymbolRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// QuickMenuButton is one button of the native quick menu and the Rune
// command it runs. Symbol is an SF Symbol name.
type QuickMenuButton struct {
	Symbol  string
	Title   string
	Command []string
}

// ID identifies the button to the native bar and keys the key-binding
// lookup that renders its accelerator.
func (b QuickMenuButton) ID() string { return strings.Join(b.Command, " ") }

// QuickMenuButtons decodes cfg's `gui.quick_menu` list into the buttons
// of the native quick menu, top to bottom. Malformed entries are
// dropped; validateQuickMenu reports them at load time.
func QuickMenuButtons(cfg config.Config) []QuickMenuButton {
	guiCfg, err := cfg.GetConfig(keyGUI)
	if err != nil {
		return nil
	}
	return parseQuickMenuButtons(guiCfg, map[string]error{})
}

func parseQuickMenuButtons(
	cfg config.Config, errs map[string]error,
) []QuickMenuButton {
	entries, err := cfg.GetSlice(keyQuickMenu)
	if err != nil {
		if err != config.ErrNotFound {
			errs[keyGUI+"."+keyQuickMenu] = err
		}
		return nil
	}
	if len(entries) > maxQuickMenuButtons {
		errs[keyGUI+"."+keyQuickMenu] = fmt.Errorf(
			"at most %d buttons are supported but found %d",
			maxQuickMenuButtons, len(entries))
		entries = entries[:maxQuickMenuButtons]
	}
	ret := make([]QuickMenuButton, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		path := fmt.Sprintf("%s.%s[%d]", keyGUI, keyQuickMenu, i)
		button, err := parseQuickMenuButton(entry, path, errs)
		if err != nil {
			errs[path] = err
			continue
		}
		if button == nil {
			continue
		}
		if seen[button.ID()] {
			errs[path] = fmt.Errorf("duplicate command %q", button.ID())
			continue
		}
		seen[button.ID()] = true
		ret = append(ret, *button)
	}
	return ret
}

// parseQuickMenuButton returns a nil button when a field-level error was
// already recorded under path, so the caller drops the entry without
// masking the more specific message.
func parseQuickMenuButton(
	entry any, path string, errs map[string]error,
) (*QuickMenuButton, error) {
	m, ok := entry.(map[string]any)
	if !ok {
		return nil, errors.New("expected a dict with 'symbol', " +
			"'title' and 'command' keys")
	}
	for key := range m {
		switch key {
		case "symbol", "title", "command":
		default:
			return nil, fmt.Errorf("unknown key %q", key)
		}
	}

	symbol, ok := m["symbol"].(string)
	if !ok {
		errs[path+".symbol"] = errors.New("required, must be an SF Symbol name")
		return nil, nil
	}
	if !quickMenuSymbolRe.MatchString(symbol) {
		errs[path+".symbol"] = fmt.Errorf(
			"%q is not a valid SF Symbol name", symbol)
		return nil, nil
	}

	cmdAndArgs, err := parseQuickMenuCommand(m["command"])
	if err != nil {
		errs[path+".command"] = err
		return nil, nil
	}

	title := strings.Join(cmdAndArgs, " ")
	if raw, ok := m["title"]; ok {
		title, ok = raw.(string)
		if !ok {
			errs[path+".title"] = errors.New("must be a string")
			return nil, nil
		}
	}

	return &QuickMenuButton{Symbol: symbol, Title: title, Command: cmdAndArgs}, nil
}

func parseQuickMenuCommand(raw any) ([]string, error) {
	var parts []string
	switch v := raw.(type) {
	case string:
		parts = strings.Fields(v)
	case []any:
		for _, ifc := range v {
			word, ok := ifc.(string)
			if !ok {
				return nil, errors.New("expected space-separated multi-word " +
					"string or []string but found unknown type")
			}
			parts = append(parts, strings.Fields(word)...)
		}
	default:
		return nil, errors.New("required, must be a space-separated " +
			"multi-word string or []string")
	}
	if len(parts) == 0 {
		return nil, errors.New("must name a command")
	}
	for _, part := range parts {
		if strings.ContainsFunc(part, unicode.IsControl) {
			return nil, errors.New("must not contain control characters")
		}
	}
	return parts, nil
}

var (
	defaultWindowManagerConfig = handler.DefaultWindowManagerConfig()
)

type extensionConfig struct {
	id     string
	parent *ideConfig
	cfg    config.Config
}

type ideConfig struct {
	defaultWallpaper browser.Wallpaper

	configPath       string
	cfg              map[string]any
	errors           map[string]error
	ringBell         func()
	scheduleNextTick func(func()) bool
	cellPixelSize    func() (int, int)
	zdotDir          string
	// storage is the IDE-wide storage service. It's owned by the IDE
	// and shared across workspaces; commandAliases consults it to
	// resolve `{history}` placeholders in alias completer chains by
	// constructing a search.History against the prompt's history doc.
	// May be nil during validation paths or in tests that don't need
	// to actually run completer chains.
	storage storageapi.Service
}

func overrideConfig(ideConfig, cfg map[string]any) {
	for key, new := range cfg {
		prev, ok := ideConfig[key]
		if !ok {
			ideConfig[key] = new
			continue
		}
		prevMap, ok := prev.(map[string]any)
		if !ok {
			ideConfig[key] = new
			continue
		}
		newMap, ok := new.(map[string]any)
		if !ok {
			ideConfig[key] = new
			continue
		}
		overrideConfig(prevMap, newMap)
	}
}

func initConfig(
	c *ideConfig, cfg map[string]any, defaultWallpaper browser.Wallpaper,
	ringBell func(), scheduleNextTick func(func()) bool,
	zdotDir string, configPath string,
) {
	c.cfg = cfg
	c.configPath = configPath
	c.ringBell = ringBell
	c.scheduleNextTick = scheduleNextTick
	c.zdotDir = zdotDir
	c.defaultWallpaper = defaultWallpaper
	c.errors = make(map[string]error)
}

func initDefaultConfig(
	c *ideConfig, defaultWallpaper browser.Wallpaper,
	ringBell func(), scheduleNextTick func(func()) bool,
	zdotDir, configPath string,
) {
	cfg := make(map[string]any)
	initConfig(c, cfg, defaultWallpaper, ringBell,
		scheduleNextTick, zdotDir, configPath)
}

func (c ideConfig) command() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "command")
}

// debugger returns the `debugger` configuration block, if any.
func (c ideConfig) debugger() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	cfg, ok := c.getConfig(config.MapConfig(c.cfg), "debugger")
	return cfg, ok
}

// llmConfig returns the typed llm.Config the rune-side LLM router
// consumes. It starts from llm.DefaultConfig() and overlays any values
// found under the `models.*` block. Missing keys keep their default
// value, matching every other helper in this file. Before returning,
// it validates the result so the router never sees an invalid config
// at runtime.
func (c ideConfig) llmConfig() llm.Config {
	out := llm.DefaultConfig()
	if c.cfg == nil {
		c.recordLLMValidationError(llm.ValidateConfig(out))
		return out
	}
	models, ok := c.getConfig(config.MapConfig(c.cfg), "models")
	if !ok {
		c.recordLLMValidationError(llm.ValidateConfig(out))
		return out
	}
	overrideString(models, "reasoning_summary", &out.ReasoningSummary)
	overrideBool(models, "debug_http", &out.DebugHTTP)

	if openai, ok := c.getConfig(models, "openai"); ok {
		overrideString(openai, "api_key", &out.OpenAI.APIKey)
		overrideString(openai, "base_url", &out.OpenAI.BaseURL)
		overrideString(openai, "reasoning_effort", &out.OpenAI.ReasoningEffort)
	}
	if anthropic, ok := c.getConfig(models, "anthropic"); ok {
		overrideString(anthropic, "api_key", &out.Anthropic.APIKey)
		overrideString(anthropic, "base_url", &out.Anthropic.BaseURL)
		overrideString(anthropic, "reasoning_effort", &out.Anthropic.ReasoningEffort)
		overrideString(anthropic, "cache_control", &out.Anthropic.CacheControl)
	}
	if gemini, ok := c.getConfig(models, "gemini"); ok {
		overrideString(gemini, "api_key", &out.Gemini.APIKey)
		overrideString(gemini, "base_url", &out.Gemini.BaseURL)
		overrideString(gemini, "reasoning_effort", &out.Gemini.ReasoningEffort)
	}
	if bedrock, ok := c.getConfig(models, "bedrock"); ok {
		overrideString(bedrock, "profile", &out.Bedrock.Profile)
		overrideString(bedrock, "base_url", &out.Bedrock.BaseURL)
		overrideString(bedrock, "reasoning_effort", &out.Bedrock.ReasoningEffort)
		overrideString(bedrock, "cache_control", &out.Bedrock.CacheControl)
	}
	if codex, ok := c.getConfig(models, "codex"); ok {
		overrideString(codex, "base_url", &out.Codex.BaseURL)
	}
	if claude, ok := c.getConfig(models, "claude"); ok {
		overrideString(claude, "base_url", &out.Claude.BaseURL)
	}
	if custom, ok := c.getConfig(models, "custom"); ok {
		overrideString(custom, "url", &out.Custom.URL)
		overrideString(custom, "api_key", &out.Custom.APIKey)
		if m, err := custom.GetMap("available_models"); err == nil && len(m) > 0 {
			out.Custom.AvailableModels = make(map[string]int, len(m))
			for name, v := range m {
				switch n := v.(type) {
				case int:
					out.Custom.AvailableModels[name] = n
				case int64:
					out.Custom.AvailableModels[name] = int(n)
				case float64:
					out.Custom.AvailableModels[name] = int(n)
				}
			}
		}
	}
	if local, ok := c.getConfig(models, "local"); ok {
		overrideString(local, "models_cache_dir", &out.Local.ModelsCacheDir)
		overrideUint32Into(local, "batch_size", &out.Local.Service.BatchSize)
		overrideIntInto(local, "n_gpu_layers", &out.Local.Service.NGPULayers)
		overrideIntInto(local, "threads", &out.Local.Service.Threads)
		overrideBool(local, "flash_attention", &out.Local.Service.FlashAttention)
		overrideIntInto(local, "max_output_tokens", &out.Local.Service.MaxOutputTokens)
		overrideString(local, "chat_template", &out.Local.Service.ChatTemplate)
		overrideIntInto(local, "n_cache_reuse", &out.Local.Service.NCacheReuse)
		overrideString(local, "host", &out.Local.Service.Host)
		overrideString(local, "server_bin_path", &out.Local.Service.ServerBinPath)
		overrideIntInto(local, "max_servers", &out.Local.Service.MaxServers)
		if d, err := config.GetDuration(local, "idle_timeout",
			out.Local.Service.IdleTimeout); err == nil {
			out.Local.Service.IdleTimeout = d
		}
		if d, err := config.GetDuration(local, "startup_timeout",
			out.Local.Service.StartupTimeout); err == nil {
			out.Local.Service.StartupTimeout = d
		}
		if args, err := getStringSlice(local, "extra_args"); err == nil {
			out.Local.Service.ExtraArgs = args
		}

		if sampling, ok := c.getConfig(local, "sampling"); ok {
			s := &out.Local.Service.Sampling
			overrideUint32Into(sampling, "seed", &s.Seed)
			overrideFloat32Into(sampling, "temperature", &s.Temperature)
			overrideIntInto(sampling, "top_k", &s.TopK)
			overrideFloat32Into(sampling, "top_p", &s.TopP)
			overrideFloat32Into(sampling, "min_p", &s.MinP)
			overrideFloat32Into(sampling, "repeat_penalty", &s.RepeatPenalty)
			overrideIntInto(sampling, "repeat_last_n", &s.RepeatLastN)
			overrideFloat32Into(sampling, "freq_penalty", &s.FreqPenalty)
			overrideFloat32Into(sampling, "presence_penalty", &s.PresencePenalty)
			if setFloat32WithFlag(sampling, "typical_p", &s.TypicalP) {
				s.HasTypicalP = true
			}
			if setFloat32WithFlag(sampling, "top_n_sigma", &s.TopNSigma) {
				s.HasTopNSigma = true
			}
			overrideInt32Into(sampling, "mirostat", &s.Mirostat)
			if setFloat32WithFlag(sampling, "mirostat_tau", &s.MirostatTau) {
				s.HasMirostatTau = true
			}
			if setFloat32WithFlag(sampling, "mirostat_eta", &s.MirostatEta) {
				s.HasMirostatEta = true
			}
			overrideFloat32Into(sampling, "dynatemp_range", &s.DynaTempRange)
			overrideFloat32Into(sampling, "dynatemp_exponent", &s.DynaTempExponent)
			overrideFloat32Into(sampling, "xtc_probability", &s.XtcProbability)
			overrideFloat32Into(sampling, "xtc_threshold", &s.XtcThreshold)
			overrideFloat32Into(sampling, "dry_multiplier", &s.DryMultiplier)
			if setFloat32WithFlag(sampling, "dry_base", &s.DryBase) {
				s.HasDryBase = true
			}
			if setInt32WithFlag(sampling, "dry_allowed_length", &s.DryAllowedLength) {
				s.HasDryAllowedLength = true
			}
			if setInt32WithFlag(sampling, "dry_penalty_last_n", &s.DryPenaltyLastN) {
				s.HasDryPenaltyLastN = true
			}
		}
	}
	c.recordLLMValidationError(llm.ValidateConfig(out))
	return out
}

// recordLLMValidationError stores err under the `models` key in c.errors
// so the standard config-error reporter surfaces it like every other
// typed validation failure.
func (c ideConfig) recordLLMValidationError(err error) {
	if err == nil {
		return
	}
	c.errors["models"] = err
}

// overrideString sets *dst to the string value at key in cfg when the
// key resolves successfully. Missing/typed-mismatched keys leave dst
// unchanged, preserving any default the caller seeded.
func overrideString(cfg config.Config, key string, dst *string) {
	if v, err := cfg.GetString(key); err == nil {
		*dst = v
	}
}

func overrideBool(cfg config.Config, key string, dst *bool) {
	if v, err := cfg.GetBool(key); err == nil {
		*dst = v
	}
}

func overrideIntInto(cfg config.Config, key string, dst *int) {
	if v, err := cfg.GetInt(key); err == nil {
		*dst = v
	}
}

func overrideUint32Into(cfg config.Config, key string, dst *uint32) {
	if v, err := cfg.GetInt(key); err == nil && v >= 0 {
		*dst = uint32(v) // #nosec G115 -- guarded above
	}
}

func overrideInt32Into(cfg config.Config, key string, dst *int32) {
	if v, err := cfg.GetInt(key); err == nil {
		*dst = int32(v) // #nosec G115 -- starlark ints fit; out-of-range is config error
	}
}

func overrideFloat32Into(cfg config.Config, key string, dst *float32) {
	if v, err := cfg.GetFloat(key); err == nil {
		*dst = float32(v)
		return
	}
	// Starlark integers come through as int when the literal has no
	// decimal point; accept those for numeric knobs.
	if v, err := cfg.GetInt(key); err == nil {
		*dst = float32(v)
	}
}

// setFloat32WithFlag is overrideFloat32Into with an "explicitly set" flag.
// Returns true when the key was found and the destination was updated.
func setFloat32WithFlag(cfg config.Config, key string, dst *float32) bool {
	if v, err := cfg.GetFloat(key); err == nil {
		*dst = float32(v)
		return true
	}
	if v, err := cfg.GetInt(key); err == nil {
		*dst = float32(v)
		return true
	}
	return false
}

// setInt32WithFlag is overrideInt32Into with an "explicitly set" flag.
func setInt32WithFlag(cfg config.Config, key string, dst *int32) bool {
	if v, err := cfg.GetInt(key); err == nil {
		*dst = int32(v) // #nosec G115 -- range-checked in validation
		return true
	}
	return false
}

// debuggerConfigs extracts the `debugger.<langID>` adapter
// registry from the merged rune.star configuration. Each entry
// must provide a string `command` (argv template with an
// optional {addr} placeholder) and may provide a string
// `adapter_id` (defaults to the language ID).
//
// Invalid entries are recorded in c.errors and skipped.
func (c ideConfig) debuggerConfigs() map[string]idedebug.AdapterConfig {
	dbg, ok := c.debugger()
	if !ok {
		return nil
	}
	ret := make(map[string]idedebug.AdapterConfig)
	dbg.Iterate(func(langID string, _ any) {
		entry, err := dbg.GetConfig(langID)
		if err != nil {
			c.errors["debugger."+langID] = err
			return
		}
		cmdStr, err := entry.GetString("command")
		if err != nil {
			c.errors["debugger."+langID+".command"] = err
			return
		}
		argv := strings.Fields(cmdStr)
		if len(argv) == 0 {
			c.errors["debugger."+langID+".command"] = errors.New(
				"command is empty")
			return
		}
		if err := validateConnectCommand(argv); err != nil {
			c.errors["debugger."+langID+".command"] = err
			return
		}
		adapterID := langID
		if s, err := entry.GetString("adapter_id"); err == nil && s != "" {
			adapterID = s
		} else if err != nil && err != config.ErrNotFound {
			c.errors["debugger."+langID+".adapter_id"] = err
			return
		}
		launchArgs := c.debuggerArgsTemplate(entry, langID, "launch")
		attachArgs := c.debuggerArgsTemplate(entry, langID, "attach")
		ret[langID] = idedebug.AdapterConfig{
			Command:    argv,
			AdapterID:  adapterID,
			LaunchArgs: launchArgs,
			AttachArgs: attachArgs,
		}
	})
	return ret
}

// validateConnectCommand checks a `connect://host:port` adapter
// command so a malformed endpoint is reported at config load rather
// than as a dial failure when a debug session is created. Commands
// that spawn an adapter process are left alone.
func validateConnectCommand(argv []string) error {
	addr, ok := strings.CutPrefix(argv[0], "connect://")
	if !ok {
		return nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("adapter connect address %q: %w", addr, err)
	}
	if len(argv) > 1 {
		return errors.New("connect:// command takes no arguments")
	}
	return nil
}

// debuggerArgsTemplate reads an optional string->string argument
// template under debugger.<langID>.<key> (e.g. "launch" or
// "attach"). A missing key yields nil so the adapter falls back to
// built-in defaults; malformed entries are recorded in c.errors.
func (c ideConfig) debuggerArgsTemplate(
	entry config.Config, langID, key string,
) map[string]string {
	m, err := entry.GetMap(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["debugger."+langID+"."+key] = err
		}
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	ret := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			c.errors["debugger."+langID+"."+key+"."+k] = errors.New(
				"value must be a string")
			continue
		}
		ret[k] = s
	}
	return ret
}

func (c ideConfig) commandKeyMappings() map[handler.Sequence][][]string {
	cfg, ok := c.command()
	if !ok {
		return make(map[handler.Sequence][][]string)
	}
	return parseCommandKeyMappings(cfg, c.errors)
}

func parseCommandKeyMappings(
	cfg config.Config, errs map[string]error,
) map[handler.Sequence][][]string {
	ret := make(map[handler.Sequence][][]string)
	m, err := cfg.GetMap("key_bindings")
	if err != nil {
		if err != config.ErrNotFound {
			errs["key_bindings"] = err
		}
		return ret
	}

	for k, v := range m {
		seq, err := handler.ParseSequence(k)
		if err != nil {
			seq.First, err = term.ParseKey(k)
			if err != nil {
				errs["key_bindings."+k] = err
				continue
			}
		}
		cmdsAndArgsSliceIfc, ok := v.([]any)
		if ok {
			var cmdsAndArgs [][]string
			for _, ifc := range cmdsAndArgsSliceIfc {
				cmdAndArgs, ok := ifc.(string)
				if !ok {
					errs["key_bindings."+k] = errors.New(
						"expected space-separated multi-word " +
							"string or []string but found unknown type")
					cmdsAndArgs = nil
					break
				}
				cmdsAndArgs = append(cmdsAndArgs,
					strings.Split(strings.Trim(cmdAndArgs, " "), " "))
			}
			if len(cmdsAndArgs) != 0 {
				ret[seq] = cmdsAndArgs
			}
		} else {
			cmd, ok := v.(string)
			if !ok {
				errs["key_bindings."+k] = errors.New(
					"expected space-separated multi-word string or " +
						"[]string but found unknown type")
				continue
			}
			parts := strings.Split(strings.Trim(cmd, " "), " ")
			ret[seq] = [][]string{parts}
		}
	}

	return ret
}

// CommandKeyBindings inverts cfg's `command.key_bindings` into a map
// from full command line ("quit", "echo hello") to the single chord
// bound to it. Two-key sequences are omitted: a native menu accelerator
// can only be a single chord. Printable chords win over named-key
// aliases, then lexical order makes the choice stable.
func CommandKeyBindings(cfg config.Config) map[string]term.KeyComb {
	cmdCfg, err := cfg.GetConfig("command")
	if err != nil {
		return nil
	}
	mappings := parseCommandKeyMappings(cmdCfg, map[string]error{})

	lookup := make(map[string]term.KeyComb)
	for _, seq := range sortedCommandKeySequences(mappings) {
		if seq.Last != (term.KeyComb{}) {
			continue
		}
		for _, cmdAndArgs := range mappings[seq] {
			line := strings.Join(cmdAndArgs, " ")
			if _, ok := lookup[line]; !ok {
				lookup[line] = seq.First
			}
		}
	}
	return lookup
}

func sortedCommandKeySequences(mappings map[handler.Sequence][][]string) []handler.Sequence {
	seqs := make([]handler.Sequence, 0, len(mappings))
	for seq := range mappings {
		seqs = append(seqs, seq)
	}
	slices.SortFunc(seqs, func(a, b handler.Sequence) int {
		// A focused terminal consumes prefix chords such as C-x as PTY
		// input, so a single chord is always the more useful thing to
		// advertise when a command carries both spellings.
		aSeq, bSeq := a.Last != (term.KeyComb{}), b.Last != (term.KeyComb{})
		if aSeq != bSeq {
			if bSeq {
				return -1
			}
			return 1
		}
		if a.First.Ch != 0 && b.First.Ch == 0 {
			return -1
		}
		if a.First.Ch == 0 && b.First.Ch != 0 {
			return 1
		}
		return strings.Compare(a.String(), b.String())
	})
	return seqs
}

// commandKeyBindingLookup returns a closure that resolves a command
// name (and optional args) to the user's configured key spec, or ""
// when no binding matches. It inverts commandKeyMappings once: each
// command line in a binding maps to that binding's key, keyed by the
// joined command+args. A multi-command sequence binding maps every
// command line to the same key. Printable chords are preferred over
// named-key aliases, then lexical order makes the choice stable.
func (c ideConfig) commandKeyBindingLookup() func(string, []string) string {
	lookup := make(map[string]string)
	mappings := c.commandKeyMappings()
	for _, seq := range sortedCommandKeySequences(mappings) {
		cmds := mappings[seq]
		key := seq.First.String()
		if seq.Last != (term.KeyComb{}) {
			key += seq.Last.String()
		}
		for _, cmdAndArgs := range cmds {
			line := strings.Join(cmdAndArgs, " ")
			if _, ok := lookup[line]; !ok {
				lookup[line] = key
			}
		}
	}
	return func(cmd string, args []string) string {
		line := strings.Join(append([]string{cmd}, args...), " ")
		if key, ok := lookup[line]; ok {
			return key
		}
		return ""
	}
}

func (c ideConfig) commandKey() (ret term.KeyComb) {
	switch c.editorMode() {
	case editorModeStandard, editorModeEmacs:
		ret = defaultModelessCommandKey
	default:
		ret = defaultModalCommandKey
	}
	cfg, ok := c.command()
	if !ok {
		return
	}
	cfgKey, err := cfg.GetString(keyCommandKey)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", keyCommandKey)] = err
		}
		return
	}
	key, err := term.ParseKey(cfgKey)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", keyCommandKey)] = err
		}
		return
	}
	ret = key
	return
}

func (c ideConfig) commandMaxHistory() (ret int) {
	ret = text.DefaultConfig().CommandMaxHistory
	b, ok := c.command()
	if !ok {
		return
	}
	height, err := b.GetInt("max_history")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["command.max_history"] = err
		}
		return
	}
	ret = height
	return
}

func (c ideConfig) console() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "console")
}

func (c ideConfig) consoleMaxHistory() (ret int) {
	ret = text.DefaultConfig().ShellMaxHistory
	b, ok := c.console()
	if !ok {
		return
	}
	max, err := b.GetInt("max_history")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["console.max_history"] = err
		}
		return
	}
	ret = max
	return
}

func (c ideConfig) consoleModalStartInsert() bool {
	cfg, ok := c.console()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("modal_start_insert")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["console.modal_start_insert"] = err
		}
		return true
	}
	return enabled
}

// consoleConfig bundles the resolved companion-console settings that the
// ex editor needs at console-construction time. It is passed explicitly
// to newEx rather than threaded through the SDK text.Config, which models
// the editor component, not the console prompt.
type consoleConfig struct {
	// modal reports whether the resolved editor mode is modal (vi).
	modal bool
	// modalStartInsert opens the console input in insert mode when modal.
	modalStartInsert bool
	// prompt is the input-line prefix shown in the companion console.
	// Empty falls back to the console's built-in default.
	prompt string
}

func (c ideConfig) consoleCfg() consoleConfig {
	return consoleConfig{
		modal:            c.consoleEditorModal(),
		modalStartInsert: c.consoleModalStartInsert(),
		prompt:           c.consolePrompt(),
	}
}

func (c ideConfig) consolePrompt() (ret string) {
	cfg, ok := c.console()
	if !ok {
		return
	}
	prompt, err := cfg.GetString("prompt")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["console.prompt"] = err
		}
		return
	}
	ret = prompt
	return
}

// consoleEditorModal reports whether the companion console's input line is
// backed by a modal (vi) editor. It mirrors newPromptEditor's mode
// switch: exo cannot host the in-memory prompt surface, so it follows
// its configured fallback, leaving only resolved-modal as modal here.
func (c ideConfig) consoleEditorModal() bool {
	return modalEditorMode(c.pkgEditorMode())
}

func (c ideConfig) commandHistoryKey() (ret term.KeyComb) {
	ret = text.DefaultConfig().CommandHistoryKey
	cfg, ok := c.command()
	if !ok {
		return
	}
	cfgKey, err := cfg.GetString(keyCommandHistoryKey)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", keyCommandHistoryKey)] = err
		}
		return
	}
	key, err := term.ParseKey(cfgKey)
	if err != nil {
		c.errors[fmt.Sprintf("command.%s", keyCommandHistoryKey)] = err
		return
	}
	ret = key
	return
}

func (c ideConfig) animationsLoadingWorkspace() bool {
	return c.animationsBool("loading_workspace")
}

func (c ideConfig) animationsOpenWorkspace() bool {
	return c.animationsBool("open_workspace")
}

func (c ideConfig) animationsCommandPrompt() bool {
	return c.animationsBool("command_prompt")
}

func (c ideConfig) animationsCommandPromptColor() (term.Color, bool) {
	d, ok := c.animationsDict("command_prompt")
	if !ok {
		return 0, false
	}
	if _, ok := d["color"]; !ok {
		return 0, false
	}
	col, err := config.MapConfig(d).GetColor("color")
	if err != nil {
		c.errors["animations.command_prompt.color"] = err
		return 0, false
	}
	return col, true
}

func (c ideConfig) animationsCommandPromptAngularWidth() (float64, bool) {
	d, ok := c.animationsDict("command_prompt")
	if !ok {
		return 0, false
	}
	v, ok := d["angular_width"]
	if !ok {
		return 0, false
	}
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case int:
		f = float64(t)
	case int64:
		f = float64(t)
	default:
		c.errors["animations.command_prompt.angular_width"] = fmt.Errorf(
			"expected number, got %T", v)
		return 0, false
	}
	if f <= 0 || f > 1 {
		c.errors["animations.command_prompt.angular_width"] = fmt.Errorf(
			"expected value in (0, 1], got %v", f)
		return 0, false
	}
	return f, true
}

func (c ideConfig) animationsCommandPromptCycles() (int, bool) {
	d, ok := c.animationsDict("command_prompt")
	if !ok {
		return 0, false
	}
	v, ok := d["cycles"]
	if !ok {
		return 0, false
	}
	var n int
	switch t := v.(type) {
	case int:
		n = t
	case int64:
		n = int(t)
	default:
		c.errors["animations.command_prompt.cycles"] = fmt.Errorf(
			"expected int, got %T", v)
		return 0, false
	}
	if n < 1 {
		c.errors["animations.command_prompt.cycles"] = fmt.Errorf(
			"expected value >= 1, got %d", n)
		return 0, false
	}
	return n, true
}

func (c ideConfig) commandPromptShaderCfg() commandPromptShaderConfig {
	out := commandPromptShaderConfig{enabled: c.animationsCommandPrompt()}
	if col, ok := c.animationsCommandPromptColor(); ok {
		out.color = col
		out.colorSet = true
	}
	if w, ok := c.animationsCommandPromptAngularWidth(); ok {
		out.angularWidth = w
	}
	if n, ok := c.animationsCommandPromptCycles(); ok {
		out.cycles = n
	}
	return out
}

func (c ideConfig) commandPromptSeparatorCharset() commandPromptSeparatorCharset {
	out := defaultCommandPromptSeparatorCharset()
	cmd, ok := c.command()
	if !ok {
		return out
	}
	cfg, err := cmd.GetConfig("separator_charset")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["command.separator_charset"] = err
		}
		return out
	}
	type field struct {
		key  string
		dst  *rune
		path string
	}
	fields := []field{
		{"left", &out.Left, "command.separator_charset.left"},
		{"horizontal_left", &out.HorizontalLeft, "command.separator_charset.horizontal_left"},
		{"horizontal_right", &out.HorizontalRight, "command.separator_charset.horizontal_right"},
		{"right", &out.Right, "command.separator_charset.right"},
	}
	for _, f := range fields {
		r, err := cfg.GetRune(f.key)
		if err != nil {
			if err != config.ErrNotFound {
				c.errors[f.path] = err
			}
			continue
		}
		*f.dst = r
	}
	return out
}

func (c ideConfig) commandPromptCfg() commandPromptConfig {
	hintLookup := c.commandKeyBindingHintLookup()
	return commandPromptConfig{
		shader:    c.commandPromptShaderCfg(),
		separator: c.commandPromptSeparatorCharset(),
		keyBindingHint: func(line string) string {
			return hintLookup[line]
		},
		keyBindingHintAttr: term.Attributes{Fg: c.commandKeyBindingHintColor()},
		keyBindingHintFocusAttr: term.Attributes{
			Fg: c.commandKeyBindingHintFocusColor(),
		},
	}
}

// commandKeyBindingHintColor returns the color of the right-aligned
// key hint shown in command-prompt results. It defaults to gray and
// accepts named or hex colors under command.key_binding_hint_color.
func (c ideConfig) commandKeyBindingHintColor() term.Color {
	cfg, ok := c.command()
	if !ok {
		return term.ColorGray
	}
	col, err := cfg.GetColor(keyCommandKeyBindingHintColor)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["command."+keyCommandKeyBindingHintColor] = err
		}
		return term.ColorGray
	}
	return col
}

// commandKeyBindingHintFocusColor returns the color of the key hint on
// the focused command-prompt row. It defaults to silver and accepts
// named or hex colors under command.key_binding_hint_focus_color.
func (c ideConfig) commandKeyBindingHintFocusColor() term.Color {
	cfg, ok := c.command()
	if !ok {
		return term.ColorSilver
	}
	col, err := cfg.GetColor(keyCommandKeyBindingHintFocusColor)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["command."+keyCommandKeyBindingHintFocusColor] = err
		}
		return term.ColorSilver
	}
	return col
}

// commandKeyBindingHintLookup inverts the resolved key bindings into a
// full-command-line to long-form-key map used to annotate prompt rows.
// Direct single-command bindings are seeded first; echo-prefill
// bindings of the exact shape `echo {prompt}<cmd><space>` fill only the
// top-level command entries a direct binding did not already claim, so
// a direct binding always wins over an echo-prefill for the same line.
func (c ideConfig) commandKeyBindingHintLookup() map[string]string {
	lookup := make(map[string]string)
	mappings := c.commandKeyMappings()

	seqKey := func(seq handler.Sequence) string {
		key := seq.First.String()
		if seq.Last != (term.KeyComb{}) {
			key += seq.Last.String()
		}
		return key
	}

	for _, seq := range sortedCommandKeySequences(mappings) {
		cmds := mappings[seq]
		if len(cmds) != 1 {
			continue
		}
		line := strings.Join(cmds[0], " ")
		if _, ok := lookup[line]; !ok {
			lookup[line] = seqKey(seq)
		}
	}

	for _, seq := range sortedCommandKeySequences(mappings) {
		cmds := mappings[seq]
		if len(cmds) != 1 || len(cmds[0]) != 2 || cmds[0][0] != "echo" {
			continue
		}
		cmd, ok := echoPromptSingleCommand(cmds[0][1])
		if !ok {
			continue
		}
		if _, exists := lookup[cmd]; !exists {
			lookup[cmd] = seqKey(seq)
		}
	}

	return lookup
}

// echoPromptSingleCommand reports whether body is exactly
// `{prompt}<cmd><space>` — the prompt instruction followed by a single
// literal command word and one trailing space — and returns that word.
// Bodies that type more than one word (e.g. `{prompt}lsp<space>hover<space>`)
// or carry additional instructions are rejected so only top-level
// command prefills produce a hint.
func echoPromptSingleCommand(body string) (string, bool) {
	keys, err := parseEchoKeys(body)
	if err != nil || len(keys) < 3 {
		return "", false
	}
	if !keys[0].instructPrompt {
		return "", false
	}
	var word []rune
	for i := 1; i < len(keys); i++ {
		k := keys[i]
		if k.instructPrompt || k.instructWait || k.instructReg != "" {
			return "", false
		}
		if k.Key == term.KeySpace {
			// the trailing space must terminate a single non-empty word
			if len(word) == 0 || i != len(keys)-1 {
				return "", false
			}
			return string(word), true
		}
		if k.Key != 0 || k.Mod != 0 || k.Ch == 0 {
			return "", false
		}
		word = append(word, k.Ch)
	}
	return "", false
}

func (c ideConfig) animationsBool(key string) bool {
	if c.cfg == nil {
		return true
	}
	anims, ok := c.cfg["animations"]
	if !ok {
		return true
	}
	m, ok := anims.(map[string]any)
	if !ok {
		c.errors["animations"] = fmt.Errorf("expected dict, got %T", anims)
		return true
	}
	v, ok := m[key]
	if !ok {
		return true
	}
	switch tp := v.(type) {
	case bool:
		return tp
	case map[string]any:
		enabled, ok := tp["enabled"]
		if !ok {
			return true
		}
		b, ok := enabled.(bool)
		if !ok {
			c.errors["animations."+key+".enabled"] = fmt.Errorf(
				"expected bool, got %T", enabled)
			return true
		}
		return b
	default:
		c.errors["animations."+key] = fmt.Errorf(
			"expected bool or dict, got %T", v)
		return true
	}
}

// animationsDict returns the dict associated with
// `animations.<key>` when the key is configured as a dict. Returns
// nil and false for missing keys or bool-form values. Type errors are
// already surfaced by animationsBool/the section guard.
func (c ideConfig) animationsDict(key string) (map[string]any, bool) {
	if c.cfg == nil {
		return nil, false
	}
	anims, ok := c.cfg["animations"]
	if !ok {
		return nil, false
	}
	m, ok := anims.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	d, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	return d, true
}

// animationsOpenWorkspaceShader returns the configured shader name
// override for the open-workspace animation, if any. Records a soft
// error under "animations.open_workspace.shader" when the value is
// the wrong type or not a known shader name.
func (c ideConfig) animationsOpenWorkspaceShader() (string, bool) {
	d, ok := c.animationsDict("open_workspace")
	if !ok {
		return "", false
	}
	v, ok := d["shader"]
	if !ok {
		return "", false
	}
	name, ok := v.(string)
	if !ok {
		c.errors["animations.open_workspace.shader"] = fmt.Errorf(
			"expected string, got %T", v)
		return "", false
	}
	if name == "" {
		return "", false
	}
	if !slices.Contains(namedShaderNames(), name) {
		c.errors["animations.open_workspace.shader"] = fmt.Errorf(
			"unknown shader %q", name)
		return "", false
	}
	return name, true
}

// animationsOpenWorkspaceDuration returns the configured duration
// override for the open-workspace animation, if any. Accepts any Go
// duration string. Records a soft error on type/parse failure.
func (c ideConfig) animationsOpenWorkspaceDuration() (time.Duration, bool) {
	d, ok := c.animationsDict("open_workspace")
	if !ok {
		return 0, false
	}
	v, ok := d["duration"]
	if !ok {
		return 0, false
	}
	s, ok := v.(string)
	if !ok {
		c.errors["animations.open_workspace.duration"] = fmt.Errorf(
			"expected duration string, got %T", v)
		return 0, false
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		c.errors["animations.open_workspace.duration"] = err
		return 0, false
	}
	return dur, true
}

const (
	// animActiveContentTab configures the effect run over the content
	// tabs an extension marks as having work in progress.
	animActiveContentTab = "active_content_tab"
	// animActiveWorkspaceTab configures the effect run over the
	// workspace tabs that own such content tabs.
	animActiveWorkspaceTab = "active_workspace_tab"
	// defaultActiveTabShader is the effect run over active tabs when
	// animations.<key>.shader is absent.
	defaultActiveTabShader = "pulse"
)

// animationsActiveTabShader returns the continuous effect configured
// under animations.<key>, one of animActiveContentTab or
// animActiveWorkspaceTab. It returns "" when the animation is disabled.
// An unknown name is recorded as a soft error and falls back to the
// default.
func (c ideConfig) animationsActiveTabShader(key string) string {
	if !c.animationsBool(key) {
		return ""
	}
	d, ok := c.animationsDict(key)
	if !ok {
		return defaultActiveTabShader
	}
	name, err := config.MapConfig(d).GetString("shader")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["animations."+key+".shader"] = err
		}
		return defaultActiveTabShader
	}
	if name == "" {
		return defaultActiveTabShader
	}
	if !slices.Contains(shaderloop.Names(), name) {
		c.errors["animations."+key+".shader"] = fmt.Errorf(
			"unknown shader %q, expected one of %s",
			name, strings.Join(shaderloop.Names(), ", "))
		return defaultActiveTabShader
	}
	return name
}

// animationsActiveTabFPS returns the cadence of the effect configured
// under animations.<key>.
func (c ideConfig) animationsActiveTabFPS(key string) int {
	d, ok := c.animationsDict(key)
	if !ok {
		return shaderloop.DefaultFPS
	}
	fps, err := config.MapConfig(d).GetInt("fps")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["animations."+key+".fps"] = err
		}
		return shaderloop.DefaultFPS
	}
	if fps <= 0 {
		c.errors["animations."+key+".fps"] = fmt.Errorf(
			"expected a positive integer, got %d", fps)
		return shaderloop.DefaultFPS
	}
	return fps
}

// animationsActiveTabLoop returns how long one visual loop of the
// effect configured under animations.<key> lasts.
func (c ideConfig) animationsActiveTabLoop(key string) time.Duration {
	d, ok := c.animationsDict(key)
	if !ok {
		return shaderloop.DefaultLoop
	}
	s, err := config.MapConfig(d).GetString("loop")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["animations."+key+".loop"] = err
		}
		return shaderloop.DefaultLoop
	}
	loop, err := time.ParseDuration(s)
	if err != nil {
		c.errors["animations."+key+".loop"] = err
		return shaderloop.DefaultLoop
	}
	if loop <= 0 {
		c.errors["animations."+key+".loop"] = fmt.Errorf(
			"expected a positive duration, got %s", s)
		return shaderloop.DefaultLoop
	}
	return loop
}

func (c ideConfig) prompt() (config.Config, bool) {
	b, ok := c.browser()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, "prompt")
}

func (c ideConfig) getCfgAttr(
	key string, def term.Attributes,
	cfgKey string,
	cfgFn func() (config.Config, bool),
) (attr term.Attributes) {
	attr = def
	cfg, ok := cfgFn()
	if !ok {
		return
	}
	cfgAttr, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[cfgKey+"."+key] = err
		}
		return
	}
	attr = cfgAttr
	return
}

func (c ideConfig) getCommandAttr(
	key string, def term.Attributes) term.Attributes {
	return c.getCfgAttr(key, def, "command", c.command)
}

func (c ideConfig) promptTextAttr() term.Attributes {
	return c.getCfgAttr(
		"text_attr", browser.DefaultConfig().TextAttr,
		"prompt", c.prompt,
	)
}

func (c ideConfig) promptHighlightAttr() term.Attributes {
	return c.getCfgAttr(
		"highlight_attr", browser.DefaultConfig().HighlightAttr,
		"prompt", c.prompt,
	)
}

func (c ideConfig) promptBackgroundAttr() term.Attributes {
	return c.getCfgAttr(
		"background_attr", browser.DefaultConfig().BackgroundAttr,
		"prompt", c.prompt,
	)
}

func (c ideConfig) commandOverlayMatchedTextAttr() (ret term.Attributes) {
	def := text.DefaultCommandOverlayConfig().MatchedTextAttr
	return c.getCommandAttr("matched_text_attr", def)
}

func (c ideConfig) commandOverlayFocusElementAttr() (ret term.Attributes) {
	def := text.DefaultCommandOverlayConfig().FocusElementAttr
	return c.getCommandAttr("focus_element_attr", def)
}

func (c ideConfig) commandOverlayElementAttr() (ret term.Attributes) {
	def := text.DefaultCommandOverlayConfig().ElementAttr
	return c.getCommandAttr("element_attr", def)
}

func (c ideConfig) commandOverlayManualAttr() (ret term.Attributes) {
	def := text.DefaultCommandOverlayConfig().ManualAttr
	return c.getCommandAttr("manual_attr", def)
}

func (c ideConfig) commandOverlayShowManual() (ret bool) {
	ret = text.DefaultCommandOverlayConfig().ShowManual
	cfg, ok := c.command()
	if !ok {
		return
	}
	key := "show_manual"
	v, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", key)] = err
		}
		return
	}
	ret = v
	return
}

func (c ideConfig) commandOverlayShowProgressHint() (ret bool) {
	ret = text.DefaultCommandOverlayConfig().ShowProgressHint
	cfg, ok := c.command()
	if !ok {
		return
	}
	key := "show_progress_hint"
	v, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", key)] = err
		}
		return
	}
	ret = v
	return
}

// commandAliases returns the parsed command aliases together with their
// completer chains. It panics when c.storage is nil — building an alias
// chain requires a storage-backed history accessor to resolve `{history}`
// placeholders, and that storage is an IDE-wide invariant set by ide.New.
// Validation paths that only need cycle detection over alias names should
// use parseAliasCommands directly.
func (c ideConfig) commandAliases() map[string]text.CommandAlias {
	if c.storage == nil {
		panic("ideConfig.commandAliases: storage is nil; must be set " +
			"before constructing alias completer chains")
	}
	history := search.NewHistory(
		c.storage, commandHistoryDocumentID, c.commandMaxHistory())
	ret, _ := c.parseAliasCommands()

	cfg, ok := c.command()
	if !ok {
		return ret
	}
	cfgsAliases, err := cfg.GetMap(keyCommandAliases)
	if err != nil {
		// already recorded by parseAliasCommands.
		return ret
	}
	var perr error
	for k, v := range cfgsAliases {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := m["completer"]
		if !ok {
			continue
		}
		alias, ok := ret[k]
		if !ok {
			continue
		}
		completers, parseErr := parseAliasCompleter(k, raw, history)
		if parseErr != nil {
			perr = multierr.Append(perr, parseErr)
		}
		alias.Completers = completers
		ret[k] = alias
	}
	if perr != nil {
		c.errors[fmt.Sprintf("command.%s", keyCommandAliases)] = perr
	}
	return ret
}

// parseAliasCommands returns a map of alias name to CommandAlias with only
// the Commands field populated, plus the parsing error. Used by both
// commandAliases (which then layers completer chains on top) and the
// validator (which only needs to detect alias cycles based on Commands).
// Records any parse errors under "command.aliases" in c.errors.
func (c ideConfig) parseAliasCommands() (map[string]text.CommandAlias, error) {
	ret := make(map[string]text.CommandAlias)
	cfg, ok := c.command()
	if !ok {
		return ret, nil
	}
	cfgsAliases, err := cfg.GetMap(keyCommandAliases)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("command.%s", keyCommandAliases)] = err
		}
		return ret, err
	}
	for k, v := range cfgsAliases {
		alias := text.CommandAlias{Name: k}
		switch tp := v.(type) {
		case map[string]any:
			for kk, vv := range tp {
				if kk != "commands" && kk != "command" {
					continue
				}
				switch ttp := vv.(type) {
				case string:
					alias.Commands = []string{ttp}
				case []any:
					for _, v := range ttp {
						s, isStr := v.(string)
						if !isStr {
							err = multierr.Append(err, fmt.Errorf(
								"invalid value type for command.%s.%s",
								keyCommandAliases, k))
							continue
						}
						alias.Commands = append(alias.Commands, s)
					}
				}
			}
		case string:
			alias.Commands = []string{tp}
		case []any:
			for _, v := range tp {
				s, isStr := v.(string)
				if !isStr {
					err = multierr.Append(err, fmt.Errorf(
						"invalid value type for command.%s.%s",
						keyCommandAliases, k))
					continue
				}
				alias.Commands = append(alias.Commands, s)
			}
		}
		if len(alias.Commands) != 0 {
			ret[k] = alias
		}
	}
	if err != nil {
		c.errors[fmt.Sprintf("command.%s", keyCommandAliases)] = err
	}
	return ret, err
}

func (c ideConfig) commandOverlayConfig() text.CommandOverlayConfig {
	cfg := text.CommandOverlayConfig{
		MatchedTextAttr:  c.commandOverlayMatchedTextAttr(),
		FocusElementAttr: c.commandOverlayFocusElementAttr(),
		ElementAttr:      c.commandOverlayElementAttr(),
		ManualAttr:       c.commandOverlayManualAttr(),
		ShowManual:       c.commandOverlayShowManual(),
		ShowProgressHint: c.commandOverlayShowProgressHint(),
	}
	return cfg
}

func (c ideConfig) promptConfig() browser.PromptConfig {
	ret := browser.DefaultConfig().PromptConfig
	ret.TextAttr = c.promptTextAttr()
	ret.HighlightAttr = c.promptHighlightAttr()
	ret.BackgroundAttr = c.promptBackgroundAttr()
	return ret
}

func (c ideConfig) getConfig(cfg config.Config, key string) (config.Config, bool) {
	cfg, err := cfg.GetConfig(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[key] = err
		}
		return nil, false
	}
	return cfg, true
}

func (c ideConfig) windowManager() (config.Config, bool) {
	b, ok := c.browser()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, "window_manager")
}

func (c ideConfig) windowFrameAttr() (attr term.Attributes) {
	return c.windowAttr("frame_attr", defaultWindowManagerConfig.FrameAttr)
}

type workspaceBarKind uint8

const (
	workspaceBarKindDisabled workspaceBarKind = iota
	workspaceBarKindPaths
	workspaceBarKindNumbers
)

func (c ideConfig) workspaceBarKind() (ret workspaceBarKind) {
	ret = workspaceBarKindNumbers
	if c.cfg == nil {
		return
	}
	cfg, ok := c.browser()
	if !ok {
		return
	}

	if barKindBool, err := cfg.GetBool("workspace_bar"); err == nil {
		if barKindBool {
			return workspaceBarKindPaths
		} else {
			return workspaceBarKindDisabled
		}
	}

	barKindStr, err := cfg.GetString("workspace_bar")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.workspace_bar"] = err
		}
		return
	}
	switch barKindStr {
	case "false":
		return workspaceBarKindDisabled
	case "number", "numbers":
		return workspaceBarKindNumbers
	case "path", "paths":
		return workspaceBarKindPaths
	default:
		c.errors["browser.workspace_bar"] = errors.New("expected either " +
			"false, 'number' or 'path'")
		return
	}
}

func (c ideConfig) windowFocusFrameAttr() (attr term.Attributes) {
	return c.windowAttr("focus_frame_attr",
		defaultWindowManagerConfig.FrameAttr)
}

func (c ideConfig) windowAttr(key string, def term.Attributes) (
	attr term.Attributes,
) {
	attr = def
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	cfgAttr, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("window_manager.%s", key)] = err
		}
		return
	}
	attr = cfgAttr
	return
}

func (c ideConfig) getConfigAttr(
	cfgKey, key string, def term.Attributes,
) (attr term.Attributes) {
	attr = def
	if c.cfg == nil {
		return
	}
	cfg, ok := c.getConfig(config.MapConfig(c.cfg), cfgKey)
	if !ok {
		return
	}
	cfgAttr, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("%s.%s", cfgKey, key)] = err
		}
		return
	}
	attr = cfgAttr
	return
}

func (c ideConfig) defaultAttr() (attr term.Attributes) {
	const key = "default_attr"
	cfgAttr, err := config.GetAttributes(config.MapConfig(c.cfg), key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[key] = err
		}
		return
	}
	attr = cfgAttr
	return
}

func (c ideConfig) getBrowserAttr(
	key string, def term.Attributes,
) (attr term.Attributes) {
	return c.getConfigAttr("browser", key, def)
}

func (c ideConfig) lsp() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "lsp")
}

func (c ideConfig) lspIcons() idelsp.IconSet {
	ret := idelsp.DefaultIconSet()
	lsp, ok := c.lsp()
	if !ok {
		return ret
	}
	icons, err := lsp.GetMap("icons")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["lsp.icons"] = err
		}
		return ret
	}

	iconKeys := []idelsp.IconKey{
		idelsp.IconDiagnosticError,
		idelsp.IconDiagnosticWarning,
		idelsp.IconDiagnosticInformation,
		idelsp.IconDiagnosticHint,
		idelsp.IconCompilerInline,
		idelsp.IconCompilerEscape,
		idelsp.IconCompilerBounds,
		idelsp.IconCompilerNilcheck,
		idelsp.IconCompilerDefault,
	}
	for _, key := range iconKeys {
		name := string(key)
		icon, ok := icons[name]
		if !ok {
			continue
		}
		iconStr, ok := icon.(string)
		if !ok {
			c.errors[fmt.Sprintf("lsp.icons.%s", name)] = errors.New("expected a string")
			continue
		}
		ret[key] = iconStr
	}
	if icon, ok := icons["info"]; ok {
		iconStr, ok := icon.(string)
		if !ok {
			c.errors["lsp.icons.info"] = errors.New("expected a string")
		} else {
			ret[idelsp.IconDiagnosticInformation] = iconStr
		}
	}
	return ret
}

func (c ideConfig) notifications() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "notifications")
}

func (c ideConfig) notificationsCharset(def component.FrameCharSet) (
	cs component.FrameCharSet,
) {
	cs = def
	cfg, ok := c.notifications()
	if !ok {
		return
	}
	cfgCs, err := tconfig.GetFrameCharset(cfg, "frame_charset", cs)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("notifications.%s", "frame_charset")] = err
		}
		return
	}
	cs = cfgCs
	return
}

func (c ideConfig) notificationsPadding(def int) (ret int) {
	ret = def
	cfg, ok := c.notifications()
	if !ok {
		return
	}
	i, err := cfg.GetInt("padding")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("notifications.%s", "padding")] = err
		}
		return
	}
	ret = i
	return
}

func (c ideConfig) notificationsProgressRunes(def notifications.ProgressRunes) (
	cs notifications.ProgressRunes,
) {
	cs = def
	cfg, ok := c.notifications()
	if !ok {
		return
	}
	cfgCs, err := getProgressRunes(cfg, cs)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("notifications.%s", "progress_format")] = err
		}
		return
	}
	cs = cfgCs

	return
}

func getProgressRunes(c config.Config, def notifications.ProgressRunes) (
	notifications.ProgressRunes, error,
) {
	cfg, err := c.GetConfig("progress_format")
	if err != nil {
		return def, err
	}

	ret := def
	r, err := cfg.GetRune("current")
	if err == nil {
		ret.Current = r
	}
	r, err = cfg.GetRune("remain")
	if err == nil {
		ret.Remain = r
	}
	r, err = cfg.GetRune("current_tip")
	if err == nil {
		ret.CurrentTip = r
	}
	r, err = cfg.GetRune("end")
	if err == nil {
		ret.End = r
	}
	r, err = cfg.GetRune("start")
	if err == nil {
		ret.Start = r
	}
	return ret, nil
}

func (c ideConfig) notificationsBool(key string, def bool) (ret bool) {
	ret = def
	cfg, ok := c.notifications()
	if !ok {
		return
	}
	cfgFrame, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("notifications.%s", key)] = err
		}
		return
	}
	ret = cfgFrame
	return
}

func (c ideConfig) notificationsDuration(key string, def time.Duration) (ret time.Duration) {
	ret = def
	cfg, ok := c.notifications()
	if !ok {
		return
	}
	cfgDur, err := config.GetDuration(cfg, key, def)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("notifications.%s", key)] = err
		}
		return
	}
	ret = cfgDur
	return
}

func (c ideConfig) notificationsConfig() notifications.Config {
	ret := defaultNotificationsConfig()
	ret.Attributes = c.getConfigAttr("notifications", "attr", ret.Attributes)
	ret.BackgroundAttributes = c.getConfigAttr("notifications", "background_attr", ret.BackgroundAttributes)
	ret.FrameCharSet = c.notificationsCharset(ret.FrameCharSet)
	ret.ProgressBar = c.notificationsBool("progress_bar", ret.ProgressBar)
	ret.AutoClose = c.notificationsDuration("auto_close", ret.AutoClose)
	ret.ProgressRunes = c.notificationsProgressRunes(ret.ProgressRunes)
	ret.Padding = c.notificationsPadding(ret.Padding)
	setNotificationsColor(&ret)
	return ret
}

func setNotificationsColor(ret *notifications.Config) {
	ret.ColorInfo.Bg = ret.BackgroundAttributes.Bg
	ret.ColorSuccess.Bg = ret.BackgroundAttributes.Bg
	ret.ColorError.Bg = ret.BackgroundAttributes.Bg
	ret.ColorWarning.Bg = ret.BackgroundAttributes.Bg
	ret.ColorInfo.Fg = term.ColorSilver
	ret.ColorSuccess.Fg = term.ColorGreen
	ret.ColorError.Fg = term.ColorRed
	ret.ColorWarning.Fg = term.ColorYellow
	ret.ColorInfo.Attrs = ret.BackgroundAttributes.Attrs | term.AttrBold
	ret.ColorSuccess.Attrs = ret.BackgroundAttributes.Attrs | term.AttrBold
	ret.ColorError.Attrs = ret.BackgroundAttributes.Attrs | term.AttrBold
	ret.ColorWarning.Attrs = ret.BackgroundAttributes.Attrs | term.AttrBold
}

func (c ideConfig) focusTabAttr() term.Attributes {
	return c.getBrowserAttr("focus_tab_attr",
		browser.DefaultConfig().FocusTabAttr)
}

func (c ideConfig) nonFocusTabAttr() term.Attributes {
	return c.getBrowserAttr("non_focus_tab_attr",
		browser.DefaultConfig().NonFocusTabAttr)
}

func (c ideConfig) focusTabIconAttr() term.Attributes {
	return c.getBrowserAttr("focus_tab_icon_attr",
		browser.DefaultConfig().FocusTabIconAttr)
}

func (c ideConfig) nonFocusTabIconAttr() term.Attributes {
	return c.getBrowserAttr("non_focus_tab_icon_attr",
		browser.DefaultConfig().NonFocusTabIconAttr)
}

func (c ideConfig) highlightTabAttr() term.Attributes {
	return c.getBrowserAttr("focus_tab_highlight_attr",
		browser.DefaultConfig().FocusTabHighlightAttr)
}

func (c ideConfig) highlightTabChar() rune {
	def := browser.DefaultConfig().FocusTabHighlightChar
	cfg, ok := c.browser()
	if !ok {
		return def
	}
	s, err := cfg.GetString("focus_tab_highlight_char")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.focus_tab_highlight_char"] = err
		}
		return def
	}
	// An explicitly empty string means: disable the highlight (no
	// rune is painted). Use the configured default only when the key
	// is absent — handled by ErrNotFound above.
	for _, r := range s {
		return r
	}
	return 0
}

func (c ideConfig) dirtyTabAttr() term.Attributes {
	return c.getBrowserAttr("dirty_tab_attr",
		text.DefaultConfig().DirtyTabAttr)
}

func (c ideConfig) icons() (ret text.IconSet) {
	ret = text.DefaultConfig().Icons
	b, ok := c.browser()
	if !ok {
		return
	}

	m, err := b.GetMap("icons")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.icons"] = err
		}
		return
	}

	if _, ok := m["default"]; ok {
		ret.Default = c.getSpecialIcon(m, "default")
	}
	if _, ok := m["directory"]; ok {
		ret.Directory = c.getSpecialIcon(m, "directory")
	}
	if _, ok := m["open_directory"]; ok {
		ret.OpenDirectory = c.getSpecialIcon(m, "open_directory")
	}
	if _, ok := m["terminal"]; ok {
		ret.Terminal = c.getSpecialIcon(m, "terminal")
	}
	if _, ok := m["console"]; ok {
		ret.Shell = c.getSpecialIcon(m, "console")
	}

	for k, v := range m {
		vstr, ok := v.(string)
		if !ok {
			c.errors[fmt.Sprintf("browser.icons.%s", k)] =
				errors.New("expected a map of strings")
		} else if vstr != "" {
			ret.Extensions[k] = []rune(vstr)[0]
		}
	}
	return ret
}

// tabOverrideIcon reads browser.tab_override_icon and returns the
// first rune of the configured string. A zero rune means "no override":
// tabs keep the icons supplied by their callers.
func (c ideConfig) tabOverrideIcon() rune {
	cfg, ok := c.browser()
	if !ok {
		return 0
	}
	s, err := cfg.GetString("tab_override_icon")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.tab_override_icon"] = err
		}
		return 0
	}
	for _, r := range s {
		return r
	}
	return 0
}

func (c ideConfig) getSpecialIcon(m map[string]any, key string) (ret rune) {
	iconIfc, ok := m[key]
	if !ok {
		return
	}
	iconStr, ok := iconIfc.(string)
	if !ok {
		c.errors[fmt.Sprintf("browser.icons.%s", key)] =
			errors.New("expected a string")
	} else if iconStr != "" {
		ret = []rune(iconStr)[0]
	}
	delete(m, key)
	return
}

func (c ideConfig) windowFrameCharset() (cs component.FrameCharSet) {
	return c.windowCharset("frame_charset", defaultWindowManagerConfig.FrameCharSet)
}

func (c ideConfig) windowScrollBarAttr() (attr term.Attributes) {
	return c.windowAttr("scroll_bar_attr", defaultWindowManagerConfig.ScrollBarAttr)
}

func (c ideConfig) windowScrollBarChar() (ch rune) {
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	r, err := cfg.GetRune("scroll_bar_char")
	if err == nil {
		ch = r
	}
	return
}

func (c ideConfig) windowScrollBarHoverChar() (ch rune) {
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	r, err := cfg.GetRune("scroll_bar_hover_char")
	if err == nil {
		ch = r
	}
	return
}

func (c ideConfig) windowNoMaxSize() (ret bool) {
	ret = true
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	val, err := cfg.GetBool("no_max_size")
	if err == nil {
		ret = val
	}
	return
}

func (c ideConfig) windowBar() bool {
	return c.windowManagerBool("window_bar", defaultWindowManagerConfig.WindowBar)
}

func (c ideConfig) windowBarCharset() (cs tcomponent.WindowBarCharSet) {
	cs = defaultWindowManagerConfig.WindowBarCharSet
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	sub, err := cfg.GetConfig("window_bar_charset")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["window_manager.window_bar_charset"] = err
		}
		return
	}
	fields := []struct {
		key string
		dst *rune
	}{
		{"left", &cs.Left},
		{"horizontal", &cs.Horizontal},
		{"right", &cs.Right},
	}
	for _, f := range fields {
		r, err := sub.GetRune(f.key)
		if err != nil {
			if err != config.ErrNotFound {
				c.errors[fmt.Sprintf("window_manager.window_bar_charset.%s", f.key)] = err
			}
			continue
		}
		*f.dst = r
	}
	return
}

// windowCloseIcon reads window_manager.close_icon and returns the
// first rune of the configured string. An empty string disables the
// close icon.
func (c ideConfig) windowCloseIcon() rune {
	cfg, ok := c.windowManager()
	if !ok {
		return defaultWindowManagerConfig.CloseIcon
	}
	s, err := cfg.GetString("close_icon")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["window_manager.close_icon"] = err
		}
		return defaultWindowManagerConfig.CloseIcon
	}
	for _, r := range s {
		return r
	}
	return 0
}

func (c ideConfig) windowCloseIconAttr() term.Attributes {
	return c.windowAttr("close_icon_attr", defaultWindowManagerConfig.CloseIconAttr)
}

func (c ideConfig) windowFocusFrameCharset() (cs component.FrameCharSet) {
	return c.windowCharset("focus_frame_charset",
		defaultWindowManagerConfig.FocusFrameCharSet)
}

func (c ideConfig) windowCharset(key string, def component.FrameCharSet) (
	cs component.FrameCharSet,
) {
	cs = def
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	cfgCs, err := tconfig.GetFrameCharset(cfg, key, cs)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("window_manager.%s", key)] = err
		}
		return
	}
	cs = cfgCs
	return
}

func (c ideConfig) windowManagerBool(key string, def bool) (frame bool) {
	frame = def
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	cfgFrame, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("window_manager.%s", key)] = err
		}
		return
	}
	frame = cfgFrame
	return
}

func (c ideConfig) frame() (frame bool) {
	return c.windowManagerBool("frame", defaultWindowManagerConfig.Frame)
}

func (c ideConfig) dim() (dim, bw bool) {
	dim = defaultWindowManagerConfig.Dim
	cfg, ok := c.windowManager()
	if !ok {
		return
	}
	if b, err := cfg.GetBool("dim"); err == nil {
		return b, false
	}
	s, err := cfg.GetString("dim")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["window_manager.dim"] = err
		}
		return
	}
	switch s {
	case "b&w", "bw":
		return true, true
	default:
		c.errors["window_manager.dim"] = errors.New(
			"expected true, false, or 'b&w'")
		return
	}
}

func (c ideConfig) frameUnion() (ret bool) {
	ret = c.frame()
	cfg, ok := c.browser()
	if !ok {
		return
	}

	unionFrames, err := cfg.GetBool("union_frames")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.union_frames"] = err
		}
	} else {
		ret = unionFrames
	}
	return
}

func (c ideConfig) frameUnionCharset() (cs component.FrameUnionCharSet) {
	cs = component.DefaultFrameUnionCharSet()
	cs.FrameCharSet = c.windowFrameCharset()
	cfg, ok := c.browser()
	if !ok {
		return
	}

	cfg, err := cfg.GetConfig("frameunion_charset")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.frameunion_charset"] = err
		}
		return
	}

	left, err := cfg.GetRune("left")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.frameunion_charset.left"] = err
		}
	} else {
		cs.Left = left
	}

	right, err := cfg.GetRune("right")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.frameunion_charset.right"] = err
		}
	} else {
		cs.Right = right
	}

	top, err := cfg.GetRune("top")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.frameunion_charset.top"] = err
		}
	} else {
		cs.Top = top
	}

	bottom, err := cfg.GetRune("bottom")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.frameunion_charset.bottom"] = err
		}
	} else {
		cs.Bottom = bottom
	}

	return
}

func (c ideConfig) windowManagerConfig() handler.WindowManagerConfig {
	dim, bw := c.dim()
	return handler.WindowManagerConfig{
		Dim:                dim,
		BW:                 bw,
		FocusFrameAttr:     c.windowFocusFrameAttr(),
		FocusFrameCharSet:  c.windowFocusFrameCharset(),
		ScrollBarHoverChar: c.windowScrollBarHoverChar(),
		WindowManagerConfig: tcomponent.WindowManagerConfig{
			NoMaxSize:        c.windowNoMaxSize(),
			Frame:            c.frame(),
			FrameAttr:        c.windowFrameAttr(),
			FrameCharSet:     c.windowFrameCharset(),
			ScrollBarAttr:    c.windowScrollBarAttr(),
			ScrollBarChar:    c.windowScrollBarChar(),
			WindowBar:        c.windowBar(),
			WindowBarCharSet: c.windowBarCharset(),
			CloseIcon:        c.windowCloseIcon(),
			CloseIconAttr:    c.windowCloseIconAttr(),
		},
	}
}

func (c ideConfig) browser() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "browser")
}

func (c ideConfig) editor() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "editor")
}

func (c ideConfig) updates() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "updates")
}

func (c ideConfig) updatesAutoInstall() bool {
	cfg, ok := c.updates()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("auto_install")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["updates.auto_install"] = err
		}
		return false
	}
	return enabled
}

func (c ideConfig) authorizer() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "authorizer")
}

func (c ideConfig) authorizerAutoAuthorizeExtensions() bool {
	return c.authorizerAutoAuthorize("auto_authorize_extensions", true)
}

func (c ideConfig) authorizerAutoAuthorizeCommands() bool {
	return c.authorizerAutoAuthorize("auto_authorize_commands", false)
}

func (c ideConfig) authorizerAutoAuthorizeVerified() bool {
	return c.authorizerAutoAuthorize("auto_authorize_verified", true)
}

func (c ideConfig) authorizerAutoAuthorize(key string, defaultValue bool) bool {
	cfg, ok := c.authorizer()
	if !ok {
		return defaultValue
	}
	enabled, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["authorizer."+key] = err
			return false
		}
		return defaultValue
	}
	return enabled
}

func (c ideConfig) telemetry() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	return c.getConfig(config.MapConfig(c.cfg), "telemetry")
}

// telemetryEnabled reports whether usage telemetry may be sent. A malformed
// value disables telemetry: the user's intent is unreadable, so the quiet
// option is the safe one.
func (c ideConfig) telemetryEnabled() bool {
	cfg, ok := c.telemetry()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("enabled")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["telemetry.enabled"] = err
			return false
		}
		return true
	}
	return enabled
}

// vim returns the vim editor's settings. Loading folds the section's older
// editor.modal spelling into it; see foldVimSection.
func (c ideConfig) vim() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	b, ok := c.editor()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, editorSectionVim)
}

func (c ideConfig) standard() (config.Config, string, bool) {
	if c.cfg == nil {
		return nil, "", false
	}
	b, ok := c.editor()
	if !ok {
		return nil, "", false
	}
	cfg, err := b.GetConfig("standard")
	if err == nil {
		return cfg, "editor.standard", true
	}
	if err != config.ErrNotFound {
		c.errors["editor.standard"] = err
		return nil, "editor.standard", false
	}
	return nil, "", false
}

func (c ideConfig) emacs() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	b, ok := c.editor()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, "emacs")
}

// helix returns the `editor.helix` configuration block, if any.
func (c ideConfig) helix() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	b, ok := c.editor()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, "helix")
}

// exo returns the `editor.exo` configuration block, if any.
func (c ideConfig) exo() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	b, ok := c.editor()
	if !ok {
		return nil, false
	}
	return c.getConfig(b, "exo")
}

// exoCommand returns the argv template for the external editor.
func (c ideConfig) exoCommand() string {
	cfg, ok := c.exo()
	if !ok {
		return ""
	}
	s, err := cfg.GetString("command")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.exo.command"] = err
		}
		return ""
	}
	return s
}

// exoGoto returns the Rune key sequence template used to position the
// external editor's cursor. Empty when unset or invalid.
func (c ideConfig) exoGoto() string {
	cfg, ok := c.exo()
	if !ok {
		return ""
	}
	s, err := cfg.GetString("goto")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.exo.goto"] = err
		}
		return ""
	}
	return s
}

// exoQuit returns the Rune key sequence sent to the external editor
// before the PTY is torn down. Empty when unset or invalid.
func (c ideConfig) exoQuit() string {
	cfg, ok := c.exo()
	if !ok {
		return ""
	}
	s, err := cfg.GetString("quit")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.exo.quit"] = err
		}
		return ""
	}
	return s
}

// exoFallback returns the Rune-native fallback editor used by the
// exofallback router for URIs that exo cannot serve (e.g.
// memory://). Valid values are "vim", "helix", "standard", or "emacs"
// ("modal" and "modeless" are accepted as deprecated aliases for "vim"
// and "standard"); defaults to "standard".
func (c ideConfig) exoFallback() string {
	cfg, ok := c.exo()
	if !ok {
		return editorFallbackStandard
	}
	s, err := cfg.GetString("fallback")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.exo.fallback"] = err
		}
		return editorFallbackStandard
	}
	if canonical, ok := normalizeEditorFallback(s); ok {
		return canonical
	}
	return editorFallbackStandard
}

// normalizeEditorFallback resolves a raw editor.exo.fallback value to a
// canonical fallback ("vim", "helix", "standard", or "emacs"), mapping
// the deprecated "modal" and "modeless" aliases to "vim" and "standard".
// ok is false for an unrecognised value. This is the single place the
// deprecated aliases are understood so no other file needs to know about
// them.
func normalizeEditorFallback(raw string) (canonical string, ok bool) {
	switch raw {
	case editorFallbackModal, editorFallbackVim:
		return editorFallbackVim, true
	case editorFallbackHelix:
		return editorFallbackHelix, true
	case editorFallbackModeless, editorFallbackStandard:
		return editorFallbackStandard, true
	case editorFallbackEmacs:
		return editorFallbackEmacs, true
	}
	return "", false
}

// modalEditorMode reports whether a resolved editor mode drives a modal
// grammar. The console input line, the terminal keymap and the ex
// command layer all key off this rather than off "vim" alone, so
// helix gets the same treatment as vim.
func modalEditorMode(mode string) bool {
	return mode == editorModeVim || mode == editorModeHelix
}

// exoExperimentalHighlights reports whether Rune should overlay its
// location-list attributes (syntax, diagnostics, debugger variables)
// on top of the external editor's rendered output. Defaults to true
// so users opt out rather than in; an unparseable value also yields
// the default while recording the error.
func (c ideConfig) exoExperimentalHighlights() bool {
	cfg, ok := c.exo()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("experimental_highlights")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.exo.experimental_highlights"] = err
		}
		return true
	}
	return enabled
}

func (c ideConfig) editorMode() (ret string) {
	ret = editorModeVim
	if c.cfg == nil {
		return
	}
	e, ok := c.editor()
	if !ok {
		return
	}
	mode, err := e.GetString("mode")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.mode"] = err
		}
		return
	}
	switch mode {
	case editorModeModal, editorModeVim:
		ret = editorModeVim
	case editorModeModeless, editorModeStandard:
		ret = editorModeStandard
	case editorModeHelix, editorModeEmacs, editorModeExo:
		ret = mode
	}
	return
}

// EditorMode resolves the canonical editor mode from cfg. The deprecated
// "modal" and "modeless" modes map to "vim" and "standard", and a missing,
// unreadable or unknown editor.mode defaults to "vim".
func EditorMode(cfg config.Config) string {
	var raw map[string]any
	if editor, err := cfg.GetMap("editor"); err == nil {
		raw = map[string]any{"editor": editor}
	}
	c := ideConfig{cfg: raw, errors: map[string]error{}}
	return c.editorMode()
}

// TelemetryEnabled resolves telemetry.enabled from cfg. A missing section or
// key enables telemetry; a malformed value disables it.
func TelemetryEnabled(cfg config.Config) bool {
	var raw map[string]any
	if telemetry, err := cfg.GetMap("telemetry"); err == nil {
		raw = map[string]any{"telemetry": telemetry}
	}
	c := ideConfig{cfg: raw, errors: map[string]error{}}
	return c.telemetryEnabled()
}

// pkgEditorMode returns the editor mode exposed to package config.star
// scripts. exo substitutes the configured exo.fallback so packages get a
// concrete built-in editor mode instead of the meta value "exo".
func (c ideConfig) pkgEditorMode() string {
	mode := c.editorMode()
	if mode == editorModeExo {
		return c.exoFallback()
	}
	return mode
}

func (c ideConfig) editorRuler() (ruler int) {
	ruler = 90
	cfg, ok := c.editor()
	if !ok {
		return
	}
	value, err := cfg.GetInt("ruler")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.ruler"] = err
		}
		return
	}
	ruler = value
	return
}

func (c ideConfig) editorAutoPair() bool {
	cfg, ok := c.editor()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("auto_pair")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.auto_pair"] = err
		}
		return false
	}
	return enabled
}

func (c ideConfig) editorAutoSave() bool {
	cfg, ok := c.editor()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("auto_save")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.auto_save"] = err
		}
		return false
	}
	return enabled
}

func (c ideConfig) vimResultAttr() (attr term.Attributes) {
	attr = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	cfg, ok := c.vim()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "search_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.vim.search_attr"] = err
		}
	}
	return attr
}

func (c ideConfig) vimAttr() (attr term.Attributes) {
	cfg, ok := c.vim()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.vim.attr"] = err
		}
	}
	return attr
}

func (c ideConfig) vimMessageBarLayout() handler.LessMessageLayout {
	cfg, ok := c.vim()
	if !ok {
		return handler.DefaultLessMessageLayout()
	}
	return c.messageBarLayout(cfg, "editor.vim")
}

func (c ideConfig) vimMessageBarAttr() term.Attributes {
	cfg, ok := c.vim()
	if !ok {
		return term.Attributes{}
	}
	return c.messageBarAttr(cfg, "editor.vim")
}

func (c ideConfig) helixResultAttr() (attr term.Attributes) {
	attr = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	cfg, ok := c.helix()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "search_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.helix.search_attr"] = err
		}
	}
	return attr
}

func (c ideConfig) helixAttr() (attr term.Attributes) {
	cfg, ok := c.helix()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.helix.attr"] = err
		}
	}
	return attr
}

func (c ideConfig) helixMessageBarLayout() handler.LessMessageLayout {
	cfg, ok := c.helix()
	if !ok {
		return handler.DefaultLessMessageLayout()
	}
	return c.messageBarLayout(cfg, "editor.helix")
}

func (c ideConfig) helixMessageBarAttr() term.Attributes {
	cfg, ok := c.helix()
	if !ok {
		return term.Attributes{}
	}
	return c.messageBarAttr(cfg, "editor.helix")
}

func (c ideConfig) initialFolds() bool {
	cfg, ok := c.editor()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("initial_folds")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.initial_folds"] = err
		}
	}
	return enabled
}

func (c ideConfig) auxiliaryBar() (config.Config, bool) {
	cfg, ok := c.editor()
	if !ok {
		return nil, false
	}
	cfg, err := cfg.GetConfig("aux_bar")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar"] = err
		}
		return nil, false
	}
	return cfg, true
}

func (c ideConfig) statusBar() (config.Config, bool) {
	cfg, ok := c.editor()
	if !ok {
		return nil, false
	}
	cfg, err := cfg.GetConfig("status_bar")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.status_bar"] = err
		}
		return nil, false
	}
	return cfg, true
}

// fileExplorer returns the editor.file_explorer config block, if any.
// Settings nested here tune the :fexplorer UI (indent guide color,
// future knobs) without affecting the code editor itself.
func (c ideConfig) fileExplorer() (config.Config, bool) {
	cfg, ok := c.editor()
	if !ok {
		return nil, false
	}
	cfg, err := cfg.GetConfig("file_explorer")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer"] = err
		}
		return nil, false
	}
	return cfg, true
}

// fileExplorerIndentAttr returns the attributes used to render the
// indent guide runes drawn at the start of every depth level in the
// file explorer. Defaults to a gray foreground so the guides recede
// visually behind file names.
func (c ideConfig) fileExplorerIndentAttr() term.Attributes {
	def := term.Attributes{Fg: term.ColorGray}
	cfg, ok := c.fileExplorer()
	if !ok {
		return def
	}
	attrs, err := config.GetAttributes(cfg, "indent_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.indent_attr"] = err
		}
		return def
	}
	return attrs
}

// fileExplorerIconAttr returns the attributes used to render the
// per-row icon glyph (directory, default file, per-extension
// override) in the file explorer. Defaults to a gray foreground so
// the icons recede visually behind file names.
func (c ideConfig) fileExplorerIconAttr() term.Attributes {
	def := term.Attributes{Fg: term.ColorGray}
	cfg, ok := c.fileExplorer()
	if !ok {
		return def
	}
	attrs, err := config.GetAttributes(cfg, "icon_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.icon_attr"] = err
		}
		return def
	}
	return attrs
}

// fileExplorerReadOnly reports whether the :fexplorer split refuses
// buffer edits and writes. Defaults to false so the oil.nvim-style
// editing flow stays on unless the user opts out.
func (c ideConfig) fileExplorerReadOnly() bool {
	cfg, ok := c.fileExplorer()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("read_only")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.read_only"] = err
		}
		return false
	}
	return enabled
}

// fileExplorerEditKey returns the key that leaves the explorer's
// read-only mode. Defaults to <shift-esc>: a bare <esc> is
// load-bearing in every editor mode, so the shifted variant carries
// this instead.
func (c ideConfig) fileExplorerEditKey() term.KeyComb {
	def := term.KeyComb{Key: term.KeyEsc, Mod: term.ModShift}
	cfg, ok := c.fileExplorer()
	if !ok {
		return def
	}
	spec, err := cfg.GetString("edit_key")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.edit_key"] = err
		}
		return def
	}
	keys, err := term.ParseKeys(spec)
	if err != nil {
		c.errors["editor.file_explorer.edit_key"] = err
		return def
	}
	if len(keys) != 1 {
		c.errors["editor.file_explorer.edit_key"] = fmt.Errorf(
			"expected a single key combination, got %d", len(keys))
		return def
	}
	return keys[0]
}

// fileExplorerMinWidth returns the width the explorer reports when
// the tree renders nothing. Dimensions are derived from the rendered
// buffer, so an empty workspace (or one whose entries are all
// ignored) would otherwise collapse the split to a two-cell sliver:
// invisible, and impossible to type the first entry into.
func (c ideConfig) fileExplorerMinWidth() int {
	const def = 24
	cfg, ok := c.fileExplorer()
	if !ok {
		return def
	}
	width, err := cfg.GetInt("min_width")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.min_width"] = err
		}
		return def
	}
	return width
}

// fileExplorerHint reports whether the explorer draws the bottom row
// naming the next action available in the current mode.
func (c ideConfig) fileExplorerHint() bool {
	cfg, ok := c.fileExplorer()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("hint")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.hint"] = err
		}
		return true
	}
	return enabled
}

// fileExplorerHintAttr returns the attributes used to render the
// explorer's hint row. Defaults to a gray foreground only, so the row
// recedes instead of reading as another status bar.
func (c ideConfig) fileExplorerHintAttr() term.Attributes {
	def := term.Attributes{Fg: term.ColorGray}
	cfg, ok := c.fileExplorer()
	if !ok {
		return def
	}
	attrs, err := config.GetAttributes(cfg, "hint_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.file_explorer.hint_attr"] = err
		}
		return def
	}
	return attrs
}

func (c ideConfig) auxiliaryBarEnabled() bool {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("enabled")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.enabled"] = err
		}
	}
	return enabled
}

func (c ideConfig) statusBarEnabled() bool {
	cfg, ok := c.statusBar()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("enabled")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.status_bar.enabled"] = err
		}
		return true
	}
	return enabled
}

func (c ideConfig) auxiliaryBarConfig(
	pub text.EventPublisher, svc vctrl.Service,
) text.AuxBarConfig {
	auxBarLinesEnabled, auxBarLinesAbsolute := c.auxiliaryBarLines()
	return text.AuxBarConfig{
		GitEnabled:          c.auxiliaryBarGit(),
		LinesEnabled:        auxBarLinesEnabled,
		FoldsEnabled:        c.auxiliaryBarFolds(),
		AbsoluteLines:       auxBarLinesAbsolute,
		HighlightCursor:     c.auxiliaryBarHighlightCursor(),
		Service:             svc,
		Publisher:           pub,
		ScheduleNextTick:    c.scheduleNextTick,
		DelAttr:             c.auxiliaryBarAttr("git_del_inline_attr"),
		AddAttr:             c.auxiliaryBarAttr("git_add_inline_attr"),
		DelOverlayAttr:      c.auxiliaryBarAttr("git_del_locations_attr"),
		AddOverlayAttr:      c.auxiliaryBarAttr("git_add_locations_attr"),
		HighlightCursorAttr: c.auxiliaryBarAttr("highlight_cursor_attr"),
		LineNumberAttr:      c.auxiliaryBarAttr("line_number_attr"),
	}
}

func (c ideConfig) iconsBarConfig(pub text.EventPublisher) text.IconsBarConfig {
	return text.IconsBarConfig{
		ScheduleNextTick: c.scheduleNextTick,
		Publisher:        pub,
		DelAttr:          c.auxiliaryBarAttr("git_del_inline_attr"),
		AddAttr:          c.auxiliaryBarAttr("git_add_inline_attr"),
		DelOverlayAttr:   c.auxiliaryBarAttr("git_del_locations_attr"),
		AddOverlayAttr:   c.auxiliaryBarAttr("git_add_locations_attr"),
	}
}

func (c ideConfig) statusBarConfig(
	cwd workspaceapi.URI, pub text.EventPublisher, svc vctrl.Service,
) text.StatusBarConfig {
	return text.StatusBarConfig{
		Workspace:        cwd,
		ScheduleNextTick: c.scheduleNextTick,
		Publisher:        pub,
		BackgroundColor:  c.statusBarAttr("background_attr", term.Attributes{}).Bg,
		ErrorColor:       c.statusBarAttr("foreground_error_attr", term.Attributes{}).Fg,
		GitService:       svc,
		Layout:           c.statusBarLayout(),
	}
}

func (c ideConfig) auxiliaryBarFolds() bool {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("folds")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.folds"] = err
		}
	}
	return enabled
}

func (c ideConfig) auxiliaryBarAttr(key string) term.Attributes {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return term.Attributes{}
	}
	attrs, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("editor.aux_bar.%s", key)] = err
		}
	}
	return attrs
}

func (c ideConfig) statusBarAttr(key string, def term.Attributes) (ret term.Attributes) {
	ret = def
	cfg, ok := c.statusBar()
	if !ok {
		return
	}
	attrs, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("editor.status_bar.%s", key)] = err
		}
	} else {
		ret = attrs
	}
	return
}

func (c ideConfig) statusBarLayout() (ret []text.StatusBarComponent) {
	ret = nil // nil delegates default config to StatusBar
	cfg, ok := c.statusBar()
	if !ok {
		return
	}
	val, err := cfg.GetString("layout")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.status_bar.layout"] = err
		}
		return
	}
	ret, err = text.ParseStatusBarLayout(val)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.status_bar.layout"] = err
		}
		return
	}
	return
}

func (c ideConfig) auxiliaryBarGit() bool {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return false
	}
	enabled, err := cfg.GetString("git")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.git"] = err
		}
	}
	return enabled == "all" || enabled == "inline"
}

func (c ideConfig) gitIconsEnabled() bool {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return false
	}
	enabled, err := cfg.GetString("git")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.git"] = err
		}
	}
	return enabled == "all" || enabled == "bar"
}

func (c ideConfig) iconsBarEnabled() bool {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return false
	}
	enabled, err := cfg.GetBool("icons")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.icons"] = err
		}
	}
	return enabled
}

func (c ideConfig) auxiliaryBarHighlightCursor() (ret bool) {
	ret = true
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return
	}
	enabled, err := cfg.GetBool("highlight_cursor")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.highlight_cursor"] = err
		}
		return
	}
	return enabled
}

func (c ideConfig) auxiliaryBarLines() (bool, bool) {
	cfg, ok := c.auxiliaryBar()
	if !ok {
		return true, true
	}
	enabled, err := cfg.GetBool("lines")
	if err == nil {
		// default is absolute
		return enabled, true
	}
	lines, err := cfg.GetString("lines")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.aux_bar.lines"] = err
		}
		// defaults is absolute and enabled
		return true, true
	}
	switch lines {
	case "disabled":
		return false, false
	case "absolute":
		return true, true
	case "relative":
		return true, false
	default:
		return true, true
	}
}

func (c ideConfig) clipboard() clipboard.Register {
	cfg := config.MapConfig(c.cfg)
	ret, err := extutil.Clipboard(cfg)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["clipboard"] = err
		}
		ret = clipboard.NewInMemory()
	}
	return registerhistory.NewClipboard(registerset.New(ret))
}

func (c ideConfig) standardResultAttr() (attr term.Attributes) {
	attr = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	cfg, path, ok := c.standard()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "search_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".search_attr"] = err
		}
	}
	return attr
}

func (c ideConfig) standardAttr() (attr term.Attributes) {
	cfg, path, ok := c.standard()
	if !ok {
		return
	}
	attr, err := config.GetAttributes(cfg, "attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".attr"] = err
		}
	}
	return attr
}

func (c ideConfig) standardSearchConfig(wm standard.SearchWindowManager) standard.SearchConfig {
	standardAttr := c.standardAttr()
	ret := standard.SearchConfig{
		WindowManager:    wm,
		FindKey:          term.KeyComb{Mod: term.ModMeta, Ch: 'f'},
		ReplaceKey:       term.KeyComb{Mod: term.ModMeta, Ch: 'r'},
		Attr:             standardAttr,
		InputAttr:        standardAttr,
		PlaceholderAttr:  standardAttr,
		FrameAttr:        standardAttr,
		FocusFrameAttr:   standardAttr,
		ButtonAttr:       standardAttr,
		ButtonHoverAttr:  standardAttr,
		MatchAttr:        c.standardResultAttr(),
		CurrentMatchAttr: standardAttr,
	}
	ret.PlaceholderAttr.Fg = term.ColorGray
	ret.FrameAttr.Fg = term.ColorGray
	ret.FocusFrameAttr.Fg = term.ColorSilver
	ret.ButtonAttr.Bg = term.ColorGray
	ret.ButtonHoverAttr.Bg = term.ColorBlue
	standardCfg, path, ok := c.standard()
	if !ok {
		return ret
	}
	searchCfg, err := standardCfg.GetConfig("search")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".search"] = err
		}
		return ret
	}
	for key, dst := range map[string]*term.KeyComb{
		"find_key": &ret.FindKey, "replace_key": &ret.ReplaceKey,
	} {
		configured, err := searchCfg.GetString(key)
		if err != nil {
			if err != config.ErrNotFound {
				c.errors[path+".search."+key] = err
			}
			continue
		}
		parsed, err := term.ParseKey(configured)
		if err != nil {
			c.errors[path+".search."+key] = err
			continue
		}
		*dst = parsed
	}
	for key, dst := range map[string]*term.Attributes{
		"attr":               &ret.Attr,
		"input_attr":         &ret.InputAttr,
		"placeholder_attr":   &ret.PlaceholderAttr,
		"frame_attr":         &ret.FrameAttr,
		"focus_frame_attr":   &ret.FocusFrameAttr,
		"button_attr":        &ret.ButtonAttr,
		"button_hover_attr":  &ret.ButtonHoverAttr,
		"match_attr":         &ret.MatchAttr,
		"current_match_attr": &ret.CurrentMatchAttr,
		"status_attr":        &ret.StatusAttr,
	} {
		configured, err := config.GetAttributes(searchCfg, key)
		if err != nil {
			if err != config.ErrNotFound {
				c.errors[path+".search."+key] = err
			}
			continue
		}
		*dst = configured
	}
	return ret
}

func (c ideConfig) emacsResultAttr() term.Attributes {
	attr := c.standardResultAttr()
	cfg, ok := c.emacs()
	if !ok {
		return attr
	}
	configured, err := config.GetAttributes(cfg, "search_attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.emacs.search_attr"] = err
		}
		return attr
	}
	return configured
}

func (c ideConfig) emacsMessageBarLayout() (ret handler.LessMessageLayout) {
	cfg, ok := c.emacs()
	if !ok {
		return handler.DefaultLessMessageLayout()
	}
	return c.messageBarLayout(cfg, "editor.emacs")
}

func (c ideConfig) emacsMessageBarAttr() term.Attributes {
	cfg, ok := c.emacs()
	if !ok {
		return term.Attributes{}
	}
	return c.messageBarAttr(cfg, "editor.emacs")
}

// messageBarAttr reads the base `message_bar.attr` attributes of an editor
// section, which the layout template styling composes over.
func (c ideConfig) messageBarAttr(
	editor config.Config, path string,
) (attr term.Attributes) {
	cfg, err := editor.GetConfig("message_bar")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".message_bar"] = err
		}
		return
	}
	attr, err = config.GetAttributes(cfg, "attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".message_bar.attr"] = err
		}
		return term.Attributes{}
	}
	return
}

// messageBarLayout reads the `message_bar.layout` template of an editor
// section, falling back to the plain layout when unset or invalid.
func (c ideConfig) messageBarLayout(
	editor config.Config, path string,
) handler.LessMessageLayout {
	cfg, err := editor.GetConfig("message_bar")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".message_bar"] = err
		}
		return handler.DefaultLessMessageLayout()
	}
	layout, err := cfg.GetString("layout")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[path+".message_bar.layout"] = err
		}
		return handler.DefaultLessMessageLayout()
	}
	parsed, err := handler.ParseLessMessageLayout(layout)
	if err != nil {
		c.errors[path+".message_bar.layout"] = err
		return handler.DefaultLessMessageLayout()
	}
	return parsed
}

func (c ideConfig) emacsAttr() term.Attributes {
	attr := c.standardAttr()
	cfg, ok := c.emacs()
	if !ok {
		return attr
	}
	configured, err := config.GetAttributes(cfg, "attr")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.emacs.attr"] = err
		}
		return attr
	}
	return configured
}

func (c ideConfig) syntaxConfig() (ret syntax.Config) {
	ret = syntax.DefaultConfig()
	ret.ScheduleNextTick = c.scheduleNextTick
	cfg, ok := c.editor()
	if !ok {
		return
	}
	reparse, err := cfg.GetBool("reparse_syntax_errors")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.reparse_syntax_errors"] = err
		}
	} else {
		ret.ReparseOnErrors = reparse
	}
	strictErrors, err := cfg.GetBool("strict_errors")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.reparse_strict_errors"] = err
		}
	} else {
		ret.StrictErrors = strictErrors
	}
	autoindent, err := cfg.GetBool("autoindent")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.autoindent"] = err
		}
	} else {
		ret.Autoindent = autoindent
	}
	cfgHighlights, err := cfg.GetMap("highlights")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.highlights"] = err
		}
		return
	}
	cfg = config.MapConfig(cfgHighlights)
	for key := range cfgHighlights {
		cfgAttr, err := config.GetAttributes(cfg, key)
		if err != nil {
			c.errors["editor.highlights."+key] = err
		} else {
			ret.CaptureNamesAttributes[key] = cfgAttr
		}
	}
	return
}

func (c ideConfig) editorComments() (ret text.CommentConfig) {
	ret = text.CommentConfig{}
	cfg, ok := c.editor()
	if !ok {
		return
	}
	comments, err := cfg.GetMap("comments")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.comments"] = err
		}
		return
	}
	for langID := range comments {
		langCfg, err := config.MapConfig(comments).GetConfig(langID)
		if err != nil {
			c.errors["editor.comments."+langID] = err
			continue
		}
		spec := text.CommentSpec{}
		if line, lerr := getStringSlice(langCfg, "line"); lerr != nil {
			if lerr != config.ErrNotFound {
				c.errors["editor.comments."+langID+".line"] = lerr
			}
		} else {
			spec.Line = line
		}
		if block, berr := getStringSlice(langCfg, "block"); berr != nil {
			if berr != config.ErrNotFound {
				c.errors["editor.comments."+langID+".block"] = berr
			}
		} else {
			for i := 0; i+1 < len(block); i += 2 {
				spec.Block = append(spec.Block, text.CommentBlock{Start: block[i], End: block[i+1]})
			}
		}
		ret[langID] = spec
	}
	return
}

func (c ideConfig) editorIndents() text.IndentConfig {
	ret := text.IndentConfig{}
	cfg, ok := c.editor()
	if !ok {
		return ret
	}
	entries, err := cfg.GetMap("indents")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.indents"] = err
		}
		return ret
	}
	for langID, raw := range entries {
		val, ok := raw.(string)
		if !ok {
			c.errors["editor.indents."+langID] = errors.New("expected string value")
			continue
		}
		switch val {
		case "tab", "tabs":
			ret[langID] = text.IndentRuneTab
		case "space", "spaces":
			ret[langID] = text.IndentRuneSpace
		default:
			c.errors["editor.indents."+langID] = errors.New("expected 'tab' or 'spaces'")
		}
	}
	return ret
}

func getStringSlice(cfg config.Config, key string) ([]string, error) {
	vals, err := cfg.GetSlice(key)
	if err != nil {
		return nil, err
	}
	ret := make([]string, 0, len(vals))
	var retErr error
	for _, val := range vals {
		s, ok := val.(string)
		if !ok {
			retErr = multierr.Append(retErr,
				fmt.Errorf("slice of strings expected for %q", key))
			continue
		}
		ret = append(ret, s)
	}
	if retErr != nil {
		return nil, retErr
	}
	return ret, nil
}

func (c ideConfig) editorTabspaces() (tabs int) {
	tabs = text.DefaultConfig().Tabspaces
	cfg, ok := c.editor()
	if !ok {
		return
	}
	cfgTabs, err := cfg.GetInt("tabspaces")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.tabspaces"] = err
		}
		return
	}
	tabs = cfgTabs
	return
}

func (c ideConfig) editorMaxSizeForSyntax() (size int) {
	size = text.DefaultConfig().MaxSyntaxParseSize
	cfg, ok := c.editor()
	if !ok {
		return
	}
	cfgSize, err := cfg.GetInt("max_size_for_syntax")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.max_size_for_syntax"] = err
		}
		return
	}
	size = cfgSize
	return
}

// editorSwapDir reports whether swap files are kept in the data
// directory rather than next to the file being edited.
func (c ideConfig) editorSwapDir() bool {
	cfg, ok := c.editor()
	if !ok {
		return true
	}
	enabled, err := cfg.GetBool("swap_dir")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["editor.swap_dir"] = err
		}
		return true
	}
	return enabled
}

func (c ideConfig) wallpaper() (ret browser.Wallpaper) {
	backgroundAttr := c.workspaceWallpaperBackgroundAttr()

	ret = c.defaultWallpaper
	// allow user to override the default wallpaper's background
	ret.BackgroundAttr = backgroundAttr
	if c.cfg == nil {
		return
	}

	cfg := c.workspace()
	cfgImage, err := cfg.GetString("wallpaper_image")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.wallpaper_image"] = err
		}
		return c.wallpaperASCII(cfg, backgroundAttr)
	}
	cfgImage = os.ExpandEnv(cfgImage)

	densityChars, err := cfg.GetString("wallpaper_density_characters")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.wallpaper_density_characters"] = err
		}
		densityChars = " ▓▓▓▓"
	}

	img, err := openImage(cfgImage)
	if err != nil {
		c.errors["workspace.wallpaper_image"] = err
		return c.wallpaperASCII(cfg, backgroundAttr)
	}

	ret = makeWallpaper(img, densityChars)
	ret.BackgroundAttr = backgroundAttr
	return ret
}

func openImage(imagePath string) (image.Image, error) {
	// resolve image path
	imageURI, err := workspaceapi.CurrentUserHostURI(imagePath)
	if err != nil {
		return nil, fmt.Errorf("expand image %q: %w", imagePath, err)
	}
	f, err := os.Open(imageURI.Path())
	if err != nil {
		return nil, fmt.Errorf("open image %q: %w", imageURI.Path(), err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("png decode: %v", err)
	}
	return img, nil
}

func makeWallpaper(img image.Image, densityCharacters string) browser.Wallpaper {
	return browser.Wallpaper{
		NewComponent: func() tui.Component {
			cfg := asciiart.DefaultConfig()
			cfg.Color = true
			cfg.MaintainAspectRatio = true
			cfg.DensityCharacters = densityCharacters
			comp := asciiart.NewComponent(img, cfg)
			return component.NewSpan(comp, component.SpanConfig{
				PadHorizontalPerc: 0.4,
				PadVerticalPerc:   0.2,
				ContentAlignment:  component.AlignmentCentered,
			})
		},
	}
}

func (c ideConfig) wallpaperASCII(
	cfg config.Config, backgroundAttr term.Attributes,
) (ret browser.Wallpaper) {
	ret = c.defaultWallpaper
	ret.BackgroundAttr = backgroundAttr
	if c.cfg == nil {
		return
	}
	cfgText, err := cfg.GetString("wallpaper")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.wallpaper"] = err
		}
		return
	}
	strcfg := component.StringConfig{
		Attributes:           c.workspaceWallpaperAttr(),
		BackgroundAttributes: c.workspaceWallpaperBackgroundAttr(),
		Alignment:            component.AlignmentCentered,
	}
	ret.NewComponent = func() tui.Component {
		return component.NewStringWithConfig(cfgText, strcfg)
	}
	return
}

func (c ideConfig) autoRestore() (ret bool) {
	ret = true
	if c.cfg == nil {
		return
	}
	cfg := c.workspace()
	cfgBool, err := cfg.GetBool("auto_restore")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.auto_restore"] = err
		}
		return
	}
	ret = cfgBool
	return
}

func (c ideConfig) logOutputPath() string {
	if c.cfg == nil {
		return ""

	}
	path, err := config.MapConfig(c.cfg).GetString("log_path")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["log_path"] = err
		}
		return ""
	}
	return os.ExpandEnv(path)
}

func (c ideConfig) logLevel() (log.Level, slog.Level) {
	if c.cfg == nil {
		return log.ErrorLevel, slog.LevelError

	}
	levelStr, err := config.MapConfig(c.cfg).GetString("log_level")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["log_level"] = err
		}
		return log.ErrorLevel, slog.LevelError
	}

	level, err := log.ParseLevel(levelStr)
	if err != nil {
		c.errors["log_level"] = err
		return log.ErrorLevel, slog.LevelError
	}
	switch level {
	case log.InfoLevel:
		return level, slog.LevelInfo
	case log.DebugLevel:
		return level, slog.LevelDebug
	case log.TraceLevel:
		return level, slog.LevelDebug
	case log.WarnLevel:
		return level, slog.LevelWarn
	default:
		// log.ErrorLevel
		// log.PanicLevel
		// log.FatalLevel
		return level, slog.LevelError
	}
}

func (c ideConfig) inputMode() term.InputMode {
	inputModeIfc, ok := c.cfg["input_mode"]
	if !ok {
		return term.InputCurrent
	}

	inputModeSlice, ok := inputModeIfc.([]any)
	if !ok {
		inputModeStr, ok := inputModeIfc.(string)
		if !ok {
			c.errors["input_mode"] = errors.New("invalid type")
			return term.InputCurrent
		}

		inputModeSlice = []any{inputModeStr}
	}

	var ret term.InputMode
	for _, inputMode := range inputModeSlice {
		switch inputMode {
		case inputEsc:
			ret |= term.InputEsc
		case inputAlt:
			ret |= term.InputAlt
		case inputMouse:
			ret |= term.InputMouse
		case inputCurrent:
			return term.InputCurrent
		default:
			c.errors["input_mode"] = fmt.Errorf("unknown input mode: %s", inputMode)
		}
	}

	return ret
}

func (c ideConfig) extensions() map[string]extensionConfig {
	pConfigIfc, ok := c.cfg["extensions"]
	if !ok {
		return nil
	}
	pConfigMap, ok := pConfigIfc.(map[string]any)
	if !ok {
		c.errors["extensions"] = errors.New("invalid type")
		return nil
	}

	ret := make(map[string]extensionConfig)
	for id, pConfig := range pConfigMap {
		pcfg, ok := pConfig.(map[string]any)
		if !ok {
			c.errors["extensions."+id] = errors.New("invalid type")
			continue
		}
		ret[id] = extensionConfig{
			id:     id,
			parent: &c,
			cfg:    config.MapConfig(pcfg),
		}
	}

	return ret
}

func (c ideConfig) tutorialFiles() map[string]string {
	raw, ok := c.cfg["tutorials"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		c.errors["tutorials"] = errors.New("invalid type")
		return nil
	}
	ret := make(map[string]string, len(m))
	for name, v := range m {
		p, ok := v.(string)
		if !ok {
			c.errors["tutorials."+name] = errors.New(
				"expected string path")
			continue
		}
		ret[name] = p
	}
	return ret
}

func (c extensionConfig) path() (string, bool) {
	path, err := c.cfg.GetString("path")
	if err != nil {
		return "", false
	}
	return path, true
}

func (c extensionConfig) config() (config.Config, bool) {
	cfg, err := c.cfg.GetConfig("config")
	if err != nil {
		if err != config.ErrNotFound {
			errorID := fmt.Sprintf("extension.%s.config", c.id)
			c.parent.errors[errorID] = err
		}
		return nil, false
	}
	return cfg, true
}

func (c ideConfig) workspace() config.Config {
	if c.cfg == nil {
		return config.NopConfig()
	}
	cfg, ok := c.getConfig(config.MapConfig(c.cfg), "workspace")
	if ok {
		return cfg
	}
	return config.NopConfig()
}

func (c ideConfig) workspaceWallpaperAttr() term.Attributes {
	return c.getConfigAttr("workspace", "wallpaper_attr",
		term.Attributes{})
}

// workspaceHome returns the configured home workspace path with
// environment variables expanded. It defaults to "~" when unset;
// callers are responsible for expanding the "~" shortcut against the
// user's home directory.
func (c ideConfig) workspaceHome() string {
	ws := c.workspace()
	home, err := ws.GetString("home")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.home"] = err
		}
		return "~"
	}
	home = os.ExpandEnv(home)
	if home == "" {
		return "~"
	}
	return home
}

// workspaceSymbolDB reports whether the persistent per-workspace
// symbol database is enabled. Defaults to false when unset.
func (c ideConfig) workspaceSymbolDB() bool {
	ws := c.workspace()
	enabled, err := ws.GetBool("symboldb")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.symboldb"] = err
		}
		return false
	}
	return enabled
}

func (c ideConfig) workspaceWallpaperBackgroundAttr() term.Attributes {
	return c.getConfigAttr("workspace", "wallpaper_background_attr",
		term.Attributes{})
}

func (c ideConfig) workspaceNotice() (path, literal, show string) {
	ws := c.workspace()
	notice, err := ws.GetConfig("notice")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.notice"] = err
		}
		return "", "", ""
	}
	path = workspaceNoticeString(c, notice, "path")
	literal = workspaceNoticeString(c, notice, "literal")
	show = workspaceNoticeString(c, notice, "show")
	return
}

func workspaceNoticeString(c ideConfig, cfg config.Config, key string) string {
	v, err := cfg.GetString(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.notice."+key] = err
		}
		return ""
	}
	return v
}

func (c ideConfig) workspaceHighlightTabChar() rune {
	def := browser.DefaultConfig().FocusTabHighlightChar
	cfg, ok := c.getConfig(config.MapConfig(c.cfg), "workspace")
	if !ok {
		return def
	}
	s, err := cfg.GetString("focus_tab_highlight_char")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["workspace.focus_tab_highlight_char"] = err
		}
		return def
	}
	// An explicitly empty string means: disable the highlight (no
	// rune is painted). Use the configured default only when the key
	// is absent — handled by ErrNotFound above.
	for _, r := range s {
		return r
	}
	return 0
}

func (c ideConfig) terminalDefaultAttr() term.Attributes {
	return c.getConfigAttr("terminal", "attr", term.Attributes{})
}

func (c ideConfig) terminalSelectionAttr() term.Attributes {
	return c.getConfigAttr("terminal", "selection_attr",
		term.Attributes{Attrs: term.AttrReverse})
}

func (c ideConfig) terminalNeedsAttentionAttr() term.Attributes {
	return c.getConfigAttr("terminal", "needs_attention_attr",
		term.Attributes{Attrs: term.AttrBlink})
}

func (c ideConfig) terminalDynamicTabName() bool {
	return c.terminalBool("dynamic_tab_name")
}

func (c ideConfig) terminal() (config.Config, bool) {
	if c.cfg == nil {
		return nil, false
	}
	cfg, ok := c.getConfig(config.MapConfig(c.cfg), "terminal")
	return cfg, ok
}

func (c ideConfig) terminalShell() (ret []string) {
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	str, err := cfg.GetString("shell")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.shell"] = err
		}
	}
	return strings.Split(str, " ")
}

func (c ideConfig) terminalMaxLines() (ret int) {
	ret = vte.DefaultConfig().MaxLines
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	ret, err := cfg.GetInt("max_lines")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.max_lines"] = err
		}
	}
	return ret
}

func (c ideConfig) terminalBellTrigger() (ret []byte) {
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	retStr, err := cfg.GetString("bell_trigger")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.bell_trigger"] = err
		}
	} else {
		ret = []byte(retStr)
	}
	return
}

func (c ideConfig) initialTerminalCapacity() (ret int) {
	ret = 1
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	res, err := cfg.GetInt("initial_reservoir")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.initial_reservoir"] = err
		}
		return
	}
	return res
}
func (c ideConfig) terminalBool(key string) (ret bool) {
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	ret, err := cfg.GetBool(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("terminal.%s", key)] = err
		}
	}
	return ret
}

func (c ideConfig) terminalModal() (ret bool) {
	cfg, ok := c.terminal()
	if !ok {
		return c.terminalModalDefault()
	}
	val, err := cfg.GetBool("modal")
	if err != nil {
		if err == config.ErrNotFound {
			return c.terminalModalDefault()
		}
		c.errors["terminal.modal"] = err
		return false
	}
	return val
}

// terminalModalDefault resolves terminal.modal when it is not set: modal
// editors default to modal terminals, modeless editors to modeless. exo
// follows its configured fallback.
func (c ideConfig) terminalModalDefault() bool {
	return modalEditorMode(c.pkgEditorMode())
}

func (c ideConfig) terminalDebug() (ret bool) {
	return c.terminalBool("debug")
}

func (c ideConfig) tabNameSeparator() (ret string) {
	ret = "  "
	cfg, ok := c.browser()
	if !ok {
		return
	}
	sep, err := cfg.GetString("tab_name_separator")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["browser.tab_name_separator"] = err
		}
		return
	}
	ret = sep
	return
}

// this is an internal optimization, no need to expose it
const defaultMinWidth = 30

func (c ideConfig) terminalConfig() vte.Config {
	ret := vte.DefaultConfig()
	ret.Attributes = c.terminalDefaultAttr()
	ret.SelectionAttributes = c.terminalSelectionAttr()
	ret.NeedsAttentionAttributes = c.terminalNeedsAttentionAttr()
	ret.DynamicTabName = c.terminalDynamicTabName()
	ret.MaxLines = c.terminalMaxLines()
	ret.Bell = c.terminalBellTrigger()
	ret.Modal = c.terminalModal()
	ret.Debug = c.terminalDebug()
	ret.CommandAndArgs = c.terminalShell()
	ret.Clipboard = c.clipboard()
	ret.ScheduleNextTick = c.scheduleNextTick
	ret.RingBell = c.ringBell
	ret.CellPixelSize = c.cellPixelSize
	ret.MinWidth = defaultMinWidth
	ret.Search = c.terminalSearchConfig()
	return ret
}

// terminalSearchConfig derives the terminal's scrollback search from
// the standard editor's search theme so both prompts look alike.
// terminal.search.find_key overrides the key that opens it, defaulting
// to the standard editor's find_key when not set.
func (c ideConfig) terminalSearchConfig() vte.SearchConfig {
	// the terminal draws the box as an overlay of its own, so it has no
	// window manager to hand over: only the theme is reused
	std := c.standardSearchConfig(nil)
	ret := vte.SearchConfig{
		Config: searchbox.Config{
			Editor:  standard.Editor(),
			FindKey: std.FindKey,
			// flush against the top edge, since the terminal draws the box
			// itself rather than centering it in a floating window
			PaddingTop:      0,
			Attr:            std.Attr,
			InputAttr:       std.InputAttr,
			PlaceholderAttr: std.PlaceholderAttr,
			FrameAttr:       std.FrameAttr,
			FocusFrameAttr:  std.FocusFrameAttr,
			ButtonAttr:      std.ButtonAttr,
			ButtonHoverAttr: std.ButtonHoverAttr,
		},
		MatchAttr:        std.MatchAttr,
		CurrentMatchAttr: c.terminalSelectionAttr(),
	}
	cfg, ok := c.terminal()
	if !ok {
		return ret
	}
	searchCfg, err := cfg.GetConfig("search")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.search"] = err
		}
		return ret
	}
	configured, err := searchCfg.GetString("find_key")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.search.find_key"] = err
		}
		return ret
	}
	parsed, err := term.ParseKey(configured)
	if err != nil {
		c.errors["terminal.search.find_key"] = err
		return ret
	}
	ret.FindKey = parsed
	return ret
}

func (c ideConfig) pluginBarBackgroundColor(def term.Color) (bg term.Color) {
	return c.pluginAttr(term.Attributes{Bg: def}, "bar_background_attr").Bg
}

func (c ideConfig) pluginStatusSuccessColor(def term.Color) (fg term.Color) {
	return c.pluginAttr(term.Attributes{Fg: def}, "status_success_attr").Fg
}

func (c ideConfig) pluginStatusErrorColor(def term.Color) (fg term.Color) {
	return c.pluginAttr(term.Attributes{Fg: def}, "status_error_attr").Fg
}

func (c ideConfig) pluginAttr(def term.Attributes, key string) (bg term.Attributes) {
	bg = def
	if c.cfg == nil {
		return
	}
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	cfg, ok = c.getConfig(cfg, "plugin")
	if !ok {
		return
	}
	cfgAttr, err := config.GetAttributes(cfg, key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("terminal.plugin.%s", key)] = err
		}
		return
	}
	bg = cfgAttr
	return
}

func (c ideConfig) pluginString(def, key string) (ret string) {
	ret = def
	if c.cfg == nil {
		return
	}
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	cfg, ok = c.getConfig(cfg, "plugin")
	if !ok {
		return
	}
	str, err := cfg.GetString(key)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors[fmt.Sprintf("terminal.plugin.%s", key)] = err
		}
		return
	}
	ret = str
	return
}

func (c ideConfig) pluginAnimationFrames(def []string) (ret []string) {
	ret = def
	str := c.pluginString("", "animation")
	if str == "" {
		return
	}
	ret = strings.Split(str, "")
	return
}

func (c ideConfig) pluginStatusErrorIcon(def string) (ret string) {
	ret = def
	str := c.pluginString(def, "status_error_icon")
	if str == "" {
		return
	}
	ret = str
	return
}

func (c ideConfig) pluginStatusSuccessIcon(def string) (ret string) {
	ret = def
	str := c.pluginString(def, "status_success_icon")
	if str == "" {
		return
	}
	ret = str
	return
}

func (c ideConfig) pluginBarLayout(def []plugin.BarComponent) (ret []plugin.BarComponent) {
	ret = def
	layoutStr := c.pluginString("", "bar_layout")
	if layoutStr == "" {
		return
	}
	layout, err := plugin.ParseBarLayout(layoutStr)
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.plugin.bar_layout"] = err
		}
		return
	}
	ret = layout
	return
}

func (c ideConfig) pluginAlignBottom(def bool) (ret bool) {
	ret = def
	if c.cfg == nil {
		return
	}
	cfg, ok := c.terminal()
	if !ok {
		return
	}
	cfg, ok = c.getConfig(cfg, "plugin")
	if !ok {
		return
	}
	do, err := cfg.GetBool("bar_align_bottom")
	if err != nil {
		if err != config.ErrNotFound {
			c.errors["terminal.plugin.bar_align_bottom"] = err
		}
		return
	}
	ret = do
	return
}

func (c ideConfig) pluginBarConfig() plugin.BarConfig {
	ret := plugin.DefaultBarConfig()
	ret.BackgroundColor = c.pluginBarBackgroundColor(ret.BackgroundColor)
	ret.StatusAnimationFrames = c.pluginAnimationFrames(ret.StatusAnimationFrames)
	ret.StatusErrorIcon = c.pluginStatusErrorIcon(ret.StatusErrorIcon)
	ret.StatusErrorColor = c.pluginStatusErrorColor(ret.StatusErrorColor)
	ret.StatusSuccessIcon = c.pluginStatusSuccessIcon(ret.StatusSuccessIcon)
	ret.StatusSuccessColor = c.pluginStatusSuccessColor(ret.StatusSuccessColor)
	ret.AlignBottom = c.pluginAlignBottom(ret.AlignBottom)
	ret.Layout = c.pluginBarLayout(ret.Layout)
	return ret
}

// decodeConfig decodes a rune configuration document from r as YAML.
// See decodeConfigFile for the filename-aware variant used by the runtime.
func decodeConfig(r io.Reader) (cfg map[string]any, err error) {
	return decodeConfigFile(r, "")
}

func isStarlarkConfigFilename(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".star")
}

// decodeConfigFile decodes a rune configuration document from r using the
// filename extension to choose the decoder: `.star` uses Starlark, everything
// else uses YAML.
func decodeConfigFile(r io.Reader, filename string) (cfg map[string]any, err error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if isStarlarkConfigFilename(filename) {
		cfg, err = decodeStarlarkConfig(starlarkConfigSource{
			src:      src,
			filename: filename,
		})
		if err != nil {
			return nil, err
		}
		foldVimSection(cfg, nil)
		return cfg, nil
	}
	cfg = make(map[string]any)
	if err := yaml.NewDecoder(bytes.NewReader(src)).Decode(&cfg); err != nil {
		return nil, err
	}
	foldVimSection(cfg, nil)
	return cfg, nil
}

// foldVimSection resolves the vim editor settings of a decoded config file,
// which may spell the section editor.vim, editor.modal (its name before
// editor.mode "modal" became "vim"), or both, into identical sections under
// both names. That way the file overrides earlier files whichever spelling
// each used, and later config.star scripts may mutate either. editor.vim
// wins wherever both spellings set a key, and wholesale when it is not a
// map, so the accessors report it.
//
// base is the tree a Starlark overlay mutated in place, or nil. The overlay
// starts with base's section under both names, so only the keys it changed
// under editor.vim count; otherwise the untouched copy would revert edits
// made through editor.modal.
func foldVimSection(cfg, base map[string]any) {
	editor, ok := cfg["editor"].(map[string]any)
	if !ok {
		return
	}
	modalVal, hasModal := editor[editorSectionModal]
	vimVal, hasVim := editor[editorSectionVim]
	if !hasModal && !hasVim {
		return
	}
	modal, modalIsMap := modalVal.(map[string]any)
	vim, vimIsMap := vimVal.(map[string]any)
	var folded any
	switch {
	case hasVim && !vimIsMap:
		folded = vimVal
	case !hasVim && !modalIsMap:
		folded = modalVal
	default:
		var baseVim map[string]any
		if baseEditor, ok := base["editor"].(map[string]any); ok {
			baseVim, _ = baseEditor[editorSectionVim].(map[string]any)
		}
		merged := map[string]any{}
		overlayChangedKeys(merged, modal, nil)
		overlayChangedKeys(merged, vim, baseVim)
		folded = merged
	}
	editor[editorSectionVim] = folded
	editor[editorSectionModal] = copyConfigValue(folded)
}

// overlayChangedKeys deep-merges into dst every key of src whose value
// differs from the same key in base.
func overlayChangedKeys(dst, src, base map[string]any) {
	for key, val := range src {
		baseVal, inBase := base[key]
		if inBase && reflect.DeepEqual(val, baseVal) {
			continue
		}
		srcMap, srcOK := val.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			baseMap, _ := baseVal.(map[string]any)
			overlayChangedKeys(dstMap, srcMap, baseMap)
			continue
		}
		dst[key] = copyConfigValue(val)
	}
}

func copyConfigValue(val any) any {
	switch t := val.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = copyConfigValue(v)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = copyConfigValue(v)
		}
		return out
	default:
		return val
	}
}

// decodeOverlayConfigFile decodes a user override document and
// deep-merges it onto base, identically for both Starlark and YAML.
// A top-level rebind in Starlark (config = {...}) is treated as a
// fresh overlay, not a replacement, mirroring YAML semantics.
func decodeOverlayConfigFile(
	r io.Reader, filename string, base map[string]any,
) (map[string]any, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if isStarlarkConfigFilename(filename) {
		overrides, err := decodeStarlarkConfig(starlarkConfigSource{
			src:      src,
			filename: filename,
			base:     base,
		})
		if err != nil {
			if errors.Is(err, starlarkconfig.ErrMissingConfig) {
				return base, nil
			}
			return nil, err
		}
		foldVimSection(overrides, base)
		overrideConfig(base, overrides)
		return base, nil
	}
	overrides := make(map[string]any)
	if err := yaml.NewDecoder(bytes.NewReader(src)).Decode(&overrides); err != nil {
		return nil, err
	}
	foldVimSection(overrides, nil)
	overrideConfig(base, overrides)
	return base, nil
}

func reloadConfig(
	configFilePath string, defaultWallpaper browser.Wallpaper,
	defaultConfig DefaultConfig,
	ringBell func(), scheduleNextTick func(func()) bool,
	zdotDir string,
) (ret ideConfig, err error) {
	err = loadConfig(&ret, configFilePath,
		defaultWallpaper, defaultConfig, ringBell,
		scheduleNextTick, zdotDir)
	return
}

func loadWorkspaceConfig(
	filename string, cwd workspace.Workspace, _ workspaceapi.URI, c *ideConfig,
) (isConfigErr bool, err error) {
	f, err := cwd.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if errors.Is(err, os.ErrPermission) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open local '%s': %v", filename, err)
	}
	defer f.Close()

	cfg, err := decodeOverlayConfigFile(f, filename, c.cfg)
	if err != nil {
		return true, err
	}

	err = validateConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("validate config: %w", err)
	}

	c.cfg = cfg
	return false, nil
}

// NOTE: it's imperative that this function populates c with sane defaults even in the event
// of an error.
func loadConfig(
	c *ideConfig, configPath string,
	defaultWallpaper browser.Wallpaper,
	defaultConfig DefaultConfig,
	ringBell func(), scheduleNextTick func(func()) bool,
	zdotDir string,
) (err error) {
	initDefaultConfig(c, defaultWallpaper, ringBell, scheduleNextTick,
		zdotDir, configPath)

	cfg, err := decodeDefaultConfig(defaultConfig)
	if err != nil {
		panic(err)
	}

	initConfig(c, cfg, defaultWallpaper, ringBell,
		scheduleNextTick, zdotDir, configPath)

	if err := loadFileConfig(c, configPath); err != nil {
		return err
	}

	err = validateConfig(c.cfg)
	if err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	return nil
}

func decodeDefaultConfig(d DefaultConfig) (map[string]any, error) {
	src := []byte(d.src)
	cfg, err := decodeStarlarkConfig(starlarkConfigSource{
		src:      src,
		filename: "rune.star",
		params: map[string]any{
			"mode": map[bool]string{true: editorModeVim, false: editorModeStandard}[d.modal],
			"tui":  d.tui,
		},
	})
	if err != nil {
		return nil, err
	}
	foldVimSection(cfg, nil)
	return cfg, nil
}

func loadFileConfig(c *ideConfig, configPath string) (err error) {
	f, err := workspace.OpenFile(configPath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	cfg, err := decodeOverlayConfigFile(f, configPath, c.cfg)
	if err != nil {
		return err
	}
	c.cfg = cfg
	return nil
}

func filepathCompleter(c *text.Component) command.Completer {
	return command.FilePathCompleter(c.Workspace())
}

func dirCompleter(c *text.Component) command.Completer {
	return command.DirsCompleter(c.Workspace())
}

// historyCompleter returns a factory that exposes the given accessor as
// a Completer. The accessor must be non-nil — see commandAliases for the
// invariant.
func historyCompleter(acc command.HistoryAccessor) func(*text.Component) command.Completer {
	if acc == nil {
		panic("historyCompleter: nil HistoryAccessor")
	}
	c := command.HistoryCompleter(acc)
	return func(*text.Component) command.Completer { return c }
}

func staticOptionsCompleter(options []string) func(*text.Component) command.Completer {
	return func(c *text.Component) command.Completer {
		return command.FuncCompleter(func(context.Context, []string) (
			iterator.Iterator[string], string, error,
		) {
			return iterator.FromSlice(options), "", nil
		})
	}
}

// completerPlaceholders maps the recognized {name} placeholders in an
// alias completer entry to their factory. The "history" placeholder is
// resolved against the supplied HistoryAccessor.
func completerPlaceholders(
	history command.HistoryAccessor,
) map[string]func(*text.Component) command.Completer {
	return map[string]func(*text.Component) command.Completer{
		"file":        filepathCompleter,
		"files":       filepathCompleter,
		"dir":         dirCompleter,
		"dirs":        dirCompleter,
		"directory":   dirCompleter,
		"directories": dirCompleter,
		"history":     historyCompleter(history),
	}
}

// parsePlaceholder returns the factory for `{name}` and true when v is a
// recognized placeholder, the literal placeholder name (or empty) and a
// non-nil error when v is a `{...}` value with an unknown name, or nil and
// false when v is not a placeholder.
func parsePlaceholder(
	v string,
	placeholders map[string]func(*text.Component) command.Completer,
) (func(*text.Component) command.Completer, string, bool) {
	t := strings.TrimSpace(v)
	if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
		return nil, "", false
	}
	name := strings.TrimSpace(t[1 : len(t)-1])
	factory, ok := placeholders[strings.ToLower(name)]
	return factory, name, ok
}

// parseAliasCompleter parses an alias completer YAML value and returns the
// ordered list of factories. Single-string values continue to support the
// existing keywords ("files", "dirs", "history") as well as the new
// {name} placeholder syntax. List values may mix placeholders, external
// commands ("! cmd"), and bare strings (treated as static options). Lists
// of bare strings only continue to behave as a single static-options
// completer for backwards compatibility.
func parseAliasCompleter(
	name string, v any, history command.HistoryAccessor,
) ([]func(*text.Component) command.Completer, error) {
	placeholders := completerPlaceholders(history)
	switch ttp := v.(type) {
	case string:
		t := strings.TrimSpace(ttp)
		switch t {
		case "files":
			return []func(*text.Component) command.Completer{filepathCompleter}, nil
		case "dirs":
			return []func(*text.Component) command.Completer{dirCompleter}, nil
		case "history":
			return []func(*text.Component) command.Completer{historyCompleter(history)}, nil
		}
		if factory, ph, ok := parsePlaceholder(t, placeholders); ok {
			return []func(*text.Component) command.Completer{factory}, nil
		} else if ph != "" {
			return nil, fmt.Errorf(
				"invalid value for command.%s.%s.completer: "+
					"unknown placeholder {%s}",
				keyCommandAliases, name, ph)
		}
		if !strings.HasPrefix(t, "!") {
			return nil, fmt.Errorf(
				"invalid value for command.%s.%s.completer: "+
					"expected 'history', 'files', 'dirs', a {placeholder}, "+
					"a command starting with '!', or a list of completion options",
				keyCommandAliases, name)
		}
		return []func(*text.Component) command.Completer{
			commandCompleter(strings.TrimPrefix(t, "!")),
		}, nil
	case []any:
		strs := make([]string, 0, len(ttp))
		var typeErr error
		hasPlaceholderOrCmd := false
		for _, item := range ttp {
			s, isStr := item.(string)
			if !isStr {
				typeErr = multierr.Append(typeErr, fmt.Errorf(
					"invalid value type for option in command.%s.%s.completer: "+
						"expected string or list of strings",
					keyCommandAliases, name))
				continue
			}
			strs = append(strs, s)
			t := strings.TrimSpace(s)
			if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "!") {
				hasPlaceholderOrCmd = true
			}
		}
		if typeErr != nil {
			return nil, typeErr
		}
		if !hasPlaceholderOrCmd {
			// preserve current behavior: list of bare strings is a single
			// static-options completer.
			return []func(*text.Component) command.Completer{
				staticOptionsCompleter(strs),
			}, nil
		}
		var factories []func(*text.Component) command.Completer
		var staticBuf []string
		flushStatic := func() {
			if len(staticBuf) == 0 {
				return
			}
			opts := make([]string, len(staticBuf))
			copy(opts, staticBuf)
			factories = append(factories, staticOptionsCompleter(opts))
			staticBuf = staticBuf[:0]
		}
		var parseErr error
		for _, s := range strs {
			t := strings.TrimSpace(s)
			if factory, ph, ok := parsePlaceholder(t, placeholders); ok {
				flushStatic()
				factories = append(factories, factory)
				continue
			} else if ph != "" {
				parseErr = multierr.Append(parseErr, fmt.Errorf(
					"invalid value for command.%s.%s.completer: "+
						"unknown placeholder {%s}",
					keyCommandAliases, name, ph))
				continue
			}
			if strings.HasPrefix(t, "!") {
				flushStatic()
				factories = append(factories,
					commandCompleter(strings.TrimPrefix(t, "!")))
				continue
			}
			staticBuf = append(staticBuf, s)
		}
		flushStatic()
		if parseErr != nil {
			return nil, parseErr
		}
		return factories, nil
	default:
		return nil, fmt.Errorf(
			"invalid value type for command.%s.%s.completer",
			keyCommandAliases, name)
	}
}

func commandCompleter(cmdstr string) func(c *text.Component) command.Completer {
	return func(c *text.Component) command.Completer {
		cmdAndArgs := strings.Split(cmdstr, " ")
		return command.OutputLinesCompleter(
			c.Workspace(), cmdAndArgs, cmdenv.Lookup(c.EnvSource()))
	}
}

func defaultNotificationsConfig() notifications.Config {
	return notifications.Config{
		AutoClose:            5 * time.Second,
		ProgressBar:          true,
		Width:                50,
		Attributes:           term.Attributes{},
		BackgroundAttributes: term.Attributes{},
		FrameCharSet:         component.FrameCharSetDefault(),
		Interrupter:          term.NopInterrupter(),
	}
}
