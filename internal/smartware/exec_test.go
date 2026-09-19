package smartware

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverlayRunDrafts(t *testing.T) {
	core := CoreDir()
	cli := filepath.Join(core, "src", "cli.ts")
	if _, err := os.Stat(cli); err != nil {
		t.Skip("smartware-core cli missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := OverlayRun(ctx, "jacket.fixture.inspect", "summarize readiness")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		JacketID string `json:"jacketId"`
		Accepted bool   `json:"accepted"`
		Executed bool   `json:"executed"`
	}
	if err := json.Unmarshal(out, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.JacketID != "jacket.fixture.inspect" {
		t.Fatalf("jacket %s", rec.JacketID)
	}
	if rec.Accepted || rec.Executed {
		t.Fatalf("draft must not accept or execute: %+v", rec)
	}
}
