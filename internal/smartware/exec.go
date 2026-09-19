package smartware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CoreDir is the on-disk smartware-core checkout. Override with SMARTWARE_CORE.
func CoreDir() string {
	if v := strings.TrimSpace(os.Getenv("SMARTWARE_CORE")); v != "" {
		return v
	}
	return `C:\src\smartware-core`
}

// OverlayRun drafts a job against an installed jacket. Does not execute the jacket.
func OverlayRun(ctx context.Context, jacketID, job string) (json.RawMessage, error) {
	return runCLI(ctx, "overlay", "run", "--jacket", jacketID, "--job", job)
}

// OverlayAccept seals a draft run. Does not execute the jacket.
func OverlayAccept(ctx context.Context, runID string) (json.RawMessage, error) {
	return runCLI(ctx, "overlay", "accept", runID)
}

func runCLI(ctx context.Context, args ...string) (json.RawMessage, error) {
	core := CoreDir()
	cli := filepath.Join(core, "src", "cli.ts")
	cmdArgs := append([]string{"--import", "tsx", cli}, args...)
	cmd := exec.CommandContext(ctx, "node", cmdArgs...)
	cmd.Dir = core
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("smartware cli: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if !json.Valid(out) {
		return nil, fmt.Errorf("smartware cli: output is not json")
	}
	return json.RawMessage(out), nil
}
