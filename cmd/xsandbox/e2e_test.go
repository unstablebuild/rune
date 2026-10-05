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

//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/xsandbox/internal/record"
)

var fixture struct {
	once sync.Once
	path string
	err  error
}

// buildFixture compiles the fixture extension once per test binary.
func buildFixture(t *testing.T) string {
	t.Helper()
	fixture.once.Do(func() {
		dir, err := os.MkdirTemp("", "xsandbox-fixture-*")
		if err != nil {
			fixture.err = err
			return
		}
		bin := filepath.Join(dir, "fixtureext")
		cmd := exec.Command("go", "build", "-o", bin, "./internal/fixtureext")
		if out, err := cmd.CombinedOutput(); err != nil {
			fixture.err = err
			t.Logf("go build fixture: %s", out)
			return
		}
		fixture.path = bin
	})
	require.NoError(t, fixture.err)
	return fixture.path
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixture.path != "" {
		_ = os.RemoveAll(filepath.Dir(fixture.path))
	}
	os.Exit(code)
}

// buildSampleExtension compiles testdata/sampleext, which is a
// self-contained Go module depending on rune-go-sdk, using the local
// Go toolchain. Its go.sum is committed, so on a machine that has
// built it before the build resolves entirely from the module cache;
// a fresh CI runner only has the main module's dependencies cached
// and must be allowed to fetch sampleext's own pins.
func buildSampleExtension(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sampleext")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "testdata/sampleext"
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build sampleext: %v\n%s", err, out)
	}
	return bin
}

func runXSandbox(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	t.Logf("stdout:\n%s", stdout.String())
	t.Logf("stderr:\n%s", stderr.String())
	return code, stdout.String(), stderr.String()
}

// buildColorPalette compiles the color palette extension, which lives
// in the same module. Its spec exercises render and send_key against a
// live window-manager handler.
func buildColorPalette(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "extension_color_palette")
	cmd := exec.Command("go", "build", "-o", bin, "../extension_color_palette")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build color palette: %v\n%s", err, out)
	}
	return bin
}

func TestE2EPass(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildFixture(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/pass.star",
		"--json", jsonPath,
		"--bench",
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")
	assert.Contains(t, stdout, "[ok] handshake")
	assert.Contains(t, stdout, "[ok] expect_command hello")
	assert.Contains(t, stdout, "[ok] invoke_command hello [world]")
	assert.Contains(t, stdout, "[ok] expect_rpc browser.Notifications/Notify")
	assert.Contains(t, stdout, "[ok] assert_no_unexpected_rpcs")
	// --bench prints the per-method latency table.
	assert.Contains(t, stdout, "workspace.Files/Read")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	assert.True(t, report.Pass)
	assert.Greater(t, report.HandshakeMS, 0.0)
	assert.Greater(t, report.WallTimeMS, report.HandshakeMS)
	assert.Equal(t, "exit status 0", report.ExtensionExit)
	assert.Equal(t, "xsandbox_fixture", report.Metadata["id"])
	assert.NotEmpty(t, report.Expectations)
	for _, exp := range report.Expectations {
		assert.True(t, exp.Pass, "expectation %q failed: %s", exp.Name, exp.Detail)
	}
	assert.Contains(t, report.Methods, "browser.Notifications/Notify")
	assert.Contains(t, report.Methods, "text.Editor/SubscribeCommand")
	notify := report.Methods["browser.Notifications/Notify"]
	assert.GreaterOrEqual(t, notify.Count, 2)
	assert.GreaterOrEqual(t, notify.MaxMS, notify.MinMS)
}

func TestE2EPassInsecure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildFixture(t)
	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/pass.star",
		"--insecure",
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")
}

func TestE2EExpectationFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildFixture(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, stderr := runXSandbox(t,
		"--spec", "testdata/fail.star",
		"--json", jsonPath,
		"--", bin)
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "FAIL")
	assert.Contains(t, stdout, "[FAIL] expect_rpc browser.Notifications/Notify")
	assert.Contains(t, stdout, "was not observed")
	// The spec error carries the source position of the failed line.
	assert.Contains(t, stderr, "fail.star:5")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	assert.False(t, report.Pass)
}

func TestE2EMetadataMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildFixture(t)
	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/metadata_mismatch.star",
		"--", bin)
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "[FAIL] handshake")
	assert.Contains(t, stdout, "permissions mismatch")
	assert.Contains(t, stdout, "extraneous=[permcfg permed permfs permnoti]")
}

func TestE2EExtensionCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/crash.star",
		"--", "/usr/bin/false")
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "[FAIL] handshake")
}

func TestE2EUsageErrors(t *testing.T) {
	t.Parallel()

	code, _, stderr := runXSandbox(t)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "usage:")

	code, _, stderr = runXSandbox(t, "--spec", "does-not-exist.star", "--", "/bin/true")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "read spec")
}

func TestE2ESampleExtensionMatchingSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/sample_pass.star",
		"--json", jsonPath,
		"--bench",
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Pass)
	assert.Equal(t, "xsandbox_sample", report.Metadata["id"])
	for _, exp := range report.Expectations {
		assert.True(t, exp.Pass, "expectation %q failed: %s", exp.Name, exp.Detail)
	}

	// Every RPC the program actually makes is captured and counted.
	assert.Equal(t, 4, report.Methods["browser.Notifications/Notify"].Count)
	assert.Contains(t, report.Methods, "config.Config/Get")
	assert.Contains(t, report.Methods, "workspace.Files/Read")
	assert.Contains(t, report.Methods, "text.Editor/SubscribeCommand")
	assert.Contains(t, report.Methods, "text.Editor/SubscribeEvent")
	assert.Contains(t, report.Methods, "text.Editor/SubscribeREPLCommand")
}

func TestE2ESampleExtensionSpecDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)

	code, stdout, stderr := runXSandbox(t,
		"--spec", "testdata/sample_diff.star",
		"--", bin)
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "FAIL")
	// The command was invoked and registered fine; only the expected
	// notification text diverges from what the program emitted.
	assert.Contains(t, stdout, "[ok] invoke_command greet [ada]")
	assert.Contains(t, stdout, "[FAIL] expect_rpc browser.Notifications/Notify")
	assert.Contains(t, stdout, `expected "demo: hi bob", got "demo: hi ada"`)
	assert.Contains(t, stderr, "sample_diff.star")
}

func TestE2ESampleExtensionFullAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/sample_api.star",
		"--json", jsonPath,
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Pass)
	for _, exp := range report.Expectations {
		assert.True(t, exp.Pass, "expectation %q failed: %s", exp.Name, exp.Detail)
	}
	// Every spec expectation passed above, which already asserts each
	// method by name; this floor guards against the spec silently
	// shrinking (the sample drives 140+ distinct RPC methods).
	assert.GreaterOrEqual(t, len(report.Methods), 140,
		"expected the sample to record the whole per-method API surface")
}

func TestE2ESampleExtensionWindowHandlers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/sample_wm.star",
		"--json", jsonPath,
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Pass)
	for _, method := range []string{
		"browser.WindowManager/Split",
		"browser.WindowManager/Tab",
		"browser.WindowManager/Bar",
	} {
		assert.Contains(t, report.Methods, method,
			"sandbox did not record handler-install stream %s", method)
	}
}

func TestE2ESampleExtensionRender(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "testdata/sample_render.star",
		"--json", jsonPath,
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Pass)
	for _, exp := range report.Expectations {
		assert.True(t, exp.Pass, "expectation %q failed: %s", exp.Name, exp.Detail)
	}
}

func TestE2EColorPaletteRender(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildColorPalette(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := runXSandbox(t,
		"--spec", "../extension_color_palette/spec.star",
		"--json", jsonPath,
		"--", bin)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "PASS")

	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var report record.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Pass)
	for _, exp := range report.Expectations {
		assert.True(t, exp.Pass, "expectation %q failed: %s", exp.Name, exp.Detail)
	}
}

func TestE2ESampleExtensionFullAPIDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	bin := buildSampleExtension(t)

	code, stdout, stderr := runXSandbox(t,
		"--spec", "testdata/sample_api_diff.star",
		"--", bin)
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "FAIL")
	assert.Contains(t, stdout, "[ok] invoke_command api")
	assert.Contains(t, stdout, "[FAIL] expect_rpc browser.WindowManager/Floating")
	assert.Contains(t, stdout, "was not observed")
	assert.Contains(t, stderr, "sample_api_diff.star")
}
