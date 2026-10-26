package acp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

func TestPermissionLogLinesCarryNoToolTitle(t *testing.T) {
	const title = "Run `cat .env` SEED-TOOL-TITLE"
	request := func() sdk.RequestPermissionRequest {
		kind := sdk.ToolKind("edit")
		text := title
		return sdk.RequestPermissionRequest{
			ToolCall: sdk.ToolCallUpdate{Kind: &kind, Title: &text},
			Options:  []sdk.PermissionOption{{OptionId: "a1", Kind: "allow_once"}},
		}
	}
	for name, policy := range map[string]*PermissionPolicy{
		"no acceptable option": NewPermissionPolicy(PermissionDenyAll, 0, 0),
		"permission storm":     NewPermissionPolicy(PermissionAllowAll, 1, 1000),
	} {
		var logs bytes.Buffer
		c := newClient(&sessionState{}, policy, slog.New(slog.NewTextHandler(&logs, nil)))
		for range 2 {
			if _, err := c.RequestPermission(context.Background(), request()); err != nil {
				t.Fatal(err)
			}
		}
		if logs.Len() == 0 {
			t.Fatalf("%s: nothing logged", name)
		}
		if strings.Contains(logs.String(), "SEED-TOOL-TITLE") {
			t.Errorf("%s: the log carries the agent's tool title:\n%s", name, logs.String())
		}
	}
}
