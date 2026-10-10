// Throwaway seeder: create live + 2 archived dialogues in a fresh datadir.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/localstorage"
)

func main() {
	ctx := context.Background()
	dir := os.Args[1]
	mode := os.Args[2]
	svc := localstorage.New(ctx, dir, docbson.Marshaler())
	store := dialoguemanager.NewStore(svc, dir+"/sessions")
	msgs := []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}}
	switch mode {
	case "seed":
		id := os.Args[3]
		if err := store.Create(ctx, dialoguemanager.Dialogue{
			ID: "seed", Messages: msgs,
		}); err != nil {
			panic(err)
		}
		_ = id
	}
