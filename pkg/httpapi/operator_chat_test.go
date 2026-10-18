package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type chatReply struct {
	Created    bool           `json:"created"`
	Chat       store.Chat     `json:"chat"`
	Credential *askCredential `json:"credential"`
	Allowance  struct {
		store.Allowance
		ChatCount     int64   `json:"chatCount"`
		ChatBudgetUSD float64 `json:"chatBudgetUsd"`
		ChatsEnabled  bool    `json:"chatsEnabled"`
	} `json:"allowance"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

const chatMessage = "What is waiting for me today?"

func chatCall(t *testing.T, s *Server, method, path, token, actor string, body any) (*httptest.ResponseRecorder, chatReply) {
	t.Helper()
	w := operatorExecutionRequest(s, method, path, token, actor, body)
	schemaPath, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s %s violates the published schema: %v\n%s", method, path, err, w.Body)
	}
	if strings.Contains(w.Body.String(), "waiting for me") {
		t.Fatalf("%s %s echoed the message: %s", method, path, w.Body)
	}
	var reply chatReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return w, reply
}

func TestOperatorChatAdmitsMintsOnceUnderTheAskPolicyAndBlocksOnFinish(t *testing.T) {
	s, g, token, id, _ := askFixture(t, askPolicies, 0)
	path := "/api/v1/operator/teams/silver/chats"
	message := map[string]string{"chatId": "chat-1", "message": chatMessage}

	w, admitted := chatCall(t, s, "POST", path, token, "alice", message)
	if w.Code != 201 || !admitted.Created || admitted.Credential == nil || admitted.Credential.Key == "" {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	c := admitted.Credential
	if c.BudgetUSD != DefaultChatBudgetUSD || len(c.Models) != 1 || c.Models[0] != "small-model" || !c.ExpiresAt.Equal(admitted.Chat.ExpiresAt) {
		t.Fatalf("credential: %+v", c)
	}
	if len(g.mints) != 1 || g.mints[0].MaxBudget != DefaultChatBudgetUSD || g.mints[0].Duration != "120s" {
		t.Fatalf("gateway mint: %+v", g.mints)
	}
	if ch := admitted.Chat; ch.State != "open" || ch.Spend.KeyState != "issued" || ch.Team != "silver" || ch.Actor != "alice" || ch.BudgetUSD != DefaultChatBudgetUSD {
		t.Fatalf("chat: %+v", ch)
	}
	if al := admitted.Allowance; al.Purpose != "chat" || al.LimitUSD != DefaultChatAllowanceUSD || al.HeldUSD != DefaultChatBudgetUSD ||
		al.ChatBudgetUSD != DefaultChatBudgetUSD || !al.ChatsEnabled || al.ChatCount != 1 {
		t.Fatalf("allowance: %+v", al)
	}

	w, replay := chatCall(t, s, "POST", path, token, "alice", message)
	if w.Code != 200 || replay.Created || replay.Credential != nil || replay.Chat.RunID != admitted.Chat.RunID || len(g.mints) != 1 {
		t.Fatalf("replay minted again or changed the chat: %d %s mints=%d", w.Code, w.Body, len(g.mints))
	}
	if w, reply := chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "chat-1", "message": "Another message"}); w.Code != 409 || reply.Error.Code != "chat_conflict" {
		t.Fatalf("reused chatId for another message: %d %s", w.Code, w.Body)
	}
	if w, reply := chatCall(t, s, "POST", path, token, "bob", message); w.Code != 409 || reply.Error.Code != "chat_conflict" {
		t.Fatalf("reused chatId by another actor: %d %s", w.Code, w.Body)
	}

	chatPath := path + "/chat-1"
	if w, _ := chatCall(t, s, "GET", chatPath, token, "bob", nil); w.Code != 404 {
		t.Fatalf("another actor read the chat: %d", w.Code)
	}
	if w, _ := chatCall(t, s, "GET", "/api/v1/operator/teams/gold/chats/chat-1", token, "alice", nil); w.Code != 404 {
		t.Fatalf("the chat under a team outside the scope: %d", w.Code)
	}
	if w, read := chatCall(t, s, "GET", chatPath, token, "alice", nil); w.Code != 200 || read.Chat.State != "open" || read.Chat.Spend.CostStatus != "observed" {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}

	w, finished := chatCall(t, s, "POST", chatPath+"/finish", token, "alice", nil)
	if w.Code != 200 || finished.Chat.State != "finished" || finished.Chat.Spend.KeyState != "blocked" || g.blockedKeys() != 1 {
		t.Fatalf("finish: %d %s blocked=%d", w.Code, w.Body, g.blockedKeys())
	}
	if w, again := chatCall(t, s, "POST", chatPath+"/finish", token, "alice", nil); w.Code != 200 || !again.Chat.FinishedAt.Equal(*finished.Chat.FinishedAt) || g.blockedKeys() != 1 {
		t.Fatalf("second finish: %d %s", w.Code, w.Body)
	}
	if w, _ := chatCall(t, s, "POST", path, token, "alice", message); w.Code != 200 || len(g.mints) != 1 {
		t.Fatalf("replay after finish: %d %s", w.Code, w.Body)
	}

	for _, endpoint := range []string{"work-items/" + itoa(id), "work-items/" + itoa(id) + "/facts", "runs?team=silver"} {
		if body := operatorSchemaGET(t, s, token, endpoint); strings.Contains(string(body), `"role":"chat"`) || strings.Contains(string(body), `"runId":"`+admitted.Chat.RunID+`"`) {
			t.Fatalf("%s shows the chat Run: %s", endpoint, body)
		}
	}
	summary := string(operatorSchemaGET(t, s, token, "summary"))
	if !strings.Contains(summary, `"heldUsd":0.05`) {
		t.Fatalf("the team summary does not hold the chat message's budget: %s", summary)
	}
}

func TestOperatorChatScopeIdentityAndInput(t *testing.T) {
	s, _, token, _, _ := askFixture(t, askPolicies, 0)
	readConsumers, readToken := operatorTestConsumers(t, []string{"silver"}, false)
	readConsumers[0].Principal.Name = "reader"
	s.OperatorConfig.Consumers = append(s.OperatorConfig.Consumers, readConsumers...)
	path := "/api/v1/operator/teams/silver/chats"
	message := map[string]string{"chatId": "scope", "message": chatMessage}
	for name, tc := range map[string]struct {
		token, actor, path string
		body               any
		status             int
		code               string
	}{
		"no credential":      {"", "alice", path, message, 401, "unauthorized"},
		"read-only consumer": {readToken, "alice", path, message, 403, ""},
		"no actor":           {token, "", path, message, 400, ""},
		"outside the scope":  {token, "alice", "/api/v1/operator/teams/gold/chats", message, 404, "not_found"},
		"bad team":           {token, "alice", "/api/v1/operator/teams/..%2Fx/chats", message, 404, ""},
		"empty message":      {token, "alice", path, map[string]string{"chatId": "scope", "message": "  "}, 400, "invalid_chat"},
		"long message":       {token, "alice", path, map[string]string{"chatId": "scope", "message": strings.Repeat("x", MaxChatMessageRunes+1)}, 400, "invalid_chat"},
		"bad chatId":         {token, "alice", path, map[string]string{"chatId": "../x", "message": "Why?"}, 400, "invalid_chat"},
		"no chatId":          {token, "alice", path, map[string]string{"message": "Why?"}, 400, "invalid_chat"},
		"caller budget":      {token, "alice", path, map[string]any{"chatId": "scope", "message": "Why?", "budgetUsd": 5}, 400, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := operatorExecutionRequest(s, "POST", tc.path, tc.token, tc.actor, tc.body)
			if w.Code != tc.status || (tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`)) {
				t.Fatalf("%d want %d %s: %s", w.Code, tc.status, tc.code, w.Body)
			}
		})
	}
	var chats int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chats`).Scan(&chats); err != nil || chats != 0 {
		t.Fatalf("refused requests recorded %d chats: %v", chats, err)
	}
}

func TestOperatorChatRefusesAnExhaustedAllowanceWithItsResetTime(t *testing.T) {
	s, g, token, _, _ := askFixture(t, askPolicies, 0)
	s.ChatAllowanceUSD = 0.08
	path := "/api/v1/operator/teams/silver/chats"
	if w, _ := chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "first", "message": chatMessage}); w.Code != 201 {
		t.Fatalf("first chat: %d %s", w.Code, w.Body)
	}
	w, refused := chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "second", "message": chatMessage})
	if w.Code != 402 || refused.Error.Code != "allowance_exhausted" || refused.Allowance.Purpose != "chat" || refused.Allowance.ResetAt.IsZero() ||
		refused.Allowance.RemainingUSD != 0.03 || refused.Allowance.ChatBudgetUSD != DefaultChatBudgetUSD {
		t.Fatalf("exhausted allowance: %d %s", w.Code, w.Body)
	}
	if len(g.mints) != 1 {
		t.Fatalf("a refused chat minted: %d", len(g.mints))
	}
	ask := map[string]string{"askId": "still", "question": "How far is it?"}
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/work-items/"+itoa(mustAskItem(t))+"/asks", token, "alice", ask); w.Code != 201 {
		t.Fatalf("a spent Chat Allowance refused an Ask: %d %s", w.Code, w.Body)
	}
}

func mustAskItem(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM work_items WHERE team = 'silver'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOperatorChatNeedsTheTeamsAskPolicy(t *testing.T) {
	s, g, token, _, _ := askFixture(t, `[{"team":"silver","role":"builder","budgetUsd":1,"models":["big-model"],"ttl":"1h"},
		{"team":"silver","role":"chat","budgetUsd":1,"models":["big-model"],"ttl":"1h"}]`, 0)
	path := "/api/v1/operator/teams/silver/chats"
	w, reply := chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "nopolicy", "message": chatMessage})
	if w.Code != 409 || reply.Error.Code != "inference_policy" || len(g.mints) != 0 {
		t.Fatalf("no ask policy: %d %s", w.Code, w.Body)
	}
	s.LLMControl = nil
	w, reply = chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "nopolicy", "message": chatMessage})
	if w.Code != 503 || reply.Error.Code != "inference_unavailable" {
		t.Fatalf("no managed inference: %d %s", w.Code, w.Body)
	}
	var chats int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chats`).Scan(&chats); err != nil || chats != 0 {
		t.Fatalf("a refused chat was recorded: %d %v", chats, err)
	}
}

func TestOperatorChatAskPolicyBudgetLowersThePerChatBudget(t *testing.T) {
	s, g, token, _, _ := askFixture(t, `[{"team":"silver","role":"ask","budgetUsd":0.03,"models":["small-model"],"ttl":"1m"}]`, 0)
	w, admitted := chatCall(t, s, "POST", "/api/v1/operator/teams/silver/chats", token, "alice", map[string]string{"chatId": "cheap", "message": chatMessage})
	if w.Code != 201 || admitted.Credential.BudgetUSD != 0.03 || admitted.Chat.BudgetUSD != 0.03 || g.mints[0].MaxBudget != 0.03 || g.mints[0].Duration != "60s" {
		t.Fatalf("policy budget: %d %s", w.Code, w.Body)
	}
}

func TestOperatorChatExpiresAtItsDeadline(t *testing.T) {
	s, _, token, _, _ := askFixture(t, askPolicies, 0)
	path := "/api/v1/operator/teams/silver/chats"
	if w, _ := chatCall(t, s, "POST", path, token, "alice", map[string]string{"chatId": "late", "message": chatMessage}); w.Code != 201 {
		t.Fatalf("admission: %d %s", w.Code, w.Body)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE role = 'chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ExpireRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w, read := chatCall(t, s, "GET", path+"/late", token, "alice", nil); w.Code != 200 || read.Chat.State != "expired" {
		t.Fatalf("expired chat: %d %s", w.Code, w.Body)
	}
}
