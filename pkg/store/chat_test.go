package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func chatInput(chatID string) AdmitChat {
	return AdmitChat{ChatID: chatID, Team: "silver", MessageSHA256: askQuestion("What is waiting for me?"),
		BudgetUSD: 0.05, LimitUSD: 2, TTL: 5 * time.Minute}
}

func TestAdmitChatCreatesARunWithNoWorkItemShiftOrLease(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	chat, created, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("chat-1"))
	if err != nil || !created {
		t.Fatalf("admit: %v created=%t", err, created)
	}
	if chat.State != "open" || chat.BudgetUSD != 0.05 || chat.Team != "silver" || chat.Spend.KeyState != "none" || chat.RunToken == "" ||
		chat.ScopeKind != AllowanceScopeTeam || chat.ScopeID != "silver" {
		t.Fatalf("admitted chat: %+v", chat)
	}
	var role string
	var writes, noWorkItem, noShift bool
	if err := testStore.pool.QueryRow(ctx, `SELECT role, writes, work_item_id IS NULL, shift_id IS NULL FROM agent_runs WHERE run_token = $1`, chat.RunToken).
		Scan(&role, &writes, &noWorkItem, &noShift); err != nil {
		t.Fatal(err)
	}
	if role != work.ChatRole || writes || !noWorkItem || !noShift {
		t.Fatalf("chat run: role=%s writes=%t noWorkItem=%t noShift=%t", role, writes, noWorkItem, noShift)
	}
	var workItems, shifts, leases int
	if err := testStore.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM work_items), (SELECT count(*) FROM shifts), (SELECT count(*) FROM leases)`).
		Scan(&workItems, &shifts, &leases); err != nil {
		t.Fatal(err)
	}
	if workItems != 0 || shifts != 0 || leases != 0 {
		t.Fatalf("a chat message opened %d Work Items, %d Shifts and %d Leases", workItems, shifts, leases)
	}

	again, created, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("chat-1"))
	if err != nil || created || again.RunID != chat.RunID {
		t.Fatalf("replay: %v created=%t %+v", err, created, again)
	}
	changed := chatInput("chat-1")
	changed.MessageSHA256 = askQuestion("Something else")
	if _, _, err := testStore.AdmitChat(ctx, "workbench", "alice", changed); !errors.Is(err, ErrChatConflict) {
		t.Fatalf("reused chatId for another message: %v", err)
	}
	otherTeam := chatInput("chat-1")
	otherTeam.Team = "gold"
	if _, _, err := testStore.AdmitChat(ctx, "workbench", "alice", otherTeam); !errors.Is(err, ErrChatConflict) {
		t.Fatalf("reused chatId for another team: %v", err)
	}
	if _, _, err := testStore.AdmitChat(ctx, "workbench", "bob", chatInput("chat-1")); !errors.Is(err, ErrChatConflict) {
		t.Fatalf("reused chatId by another actor: %v", err)
	}
	if _, _, err := testStore.AdmitChat(ctx, "other", "alice", chatInput("chat-1")); err != nil {
		t.Fatalf("chatId is per consumer: %v", err)
	}
	if _, err := testStore.Chat(ctx, "workbench", "bob", "chat-1"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("cross-actor read: %v", err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO agent_runs (work_item_id, team, run_token, role) VALUES (NULL, 'silver', 'no-item', 'ask')`); err == nil {
		t.Fatal("a Run other than a chat message was stored without a Work Item")
	}
}

func TestAdmitChatPaysFromTheChatAllowanceApartFromTheAskAllowance(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask := askInput(id, "ask")
	ask.LimitUSD = 0.02
	if _, _, err := testStore.AdmitAsk(ctx, "workbench", "alice", ask); err != nil {
		t.Fatal(err)
	}
	limited := chatInput("first")
	limited.LimitUSD = 0.08
	if _, _, err := testStore.AdmitChat(ctx, "workbench", "alice", limited); err != nil {
		t.Fatalf("a spent Ask Allowance refused a chat message: %v", err)
	}
	second := chatInput("second")
	second.LimitUSD = 0.08
	_, _, err := testStore.AdmitChat(ctx, "workbench", "alice", second)
	var exhausted *AllowanceExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("second chat: %v", err)
	}
	a := exhausted.Allowance
	if a.Purpose != AllowancePurposeChat || a.LimitUSD != 0.08 || a.HeldUSD != 0.05 || a.RemainingUSD != 0.03 || a.Count != 1 {
		t.Fatalf("refusing allowance: %+v", a)
	}
	asks, err := testStore.Allowance(ctx, AllowancePurposeAsk, AllowanceScopeTeam, "silver", time.Now(), 2)
	if err != nil || asks.HeldUSD != 0.02 || asks.Count != 1 || asks.LimitUSD != 0.02 {
		t.Fatalf("the Ask Allowance counted a chat message: %+v %v", asks, err)
	}
	var chats int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM chats`).Scan(&chats); err != nil || chats != 1 {
		t.Fatalf("a refused chat message was recorded: %d %v", chats, err)
	}
}

func TestExpireRunsReclaimsAChatPastItsDeadline(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	chat, _, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("late"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`, chat.RunToken); err != nil {
		t.Fatal(err)
	}
	expired, err := testStore.ExpireRuns(ctx)
	if err != nil || len(expired) != 1 || expired[0].RunToken != chat.RunToken || expired[0].Role != work.ChatRole || expired[0].WorkItemID != 0 {
		t.Fatalf("expire: %v %+v", err, expired)
	}
	read, err := testStore.Chat(ctx, "workbench", "alice", "late")
	if err != nil || read.State != "expired" {
		t.Fatalf("expired chat: %+v %v", read, err)
	}
	var audits int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'run.expired' AND work_item_id IS NULL`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("run.expired audits: %d %v", audits, err)
	}
	finished, err := testStore.FinishChat(ctx, "workbench", "alice", "late")
	if err != nil || finished.State != "finished" {
		t.Fatalf("finish after expiry: %+v %v", finished, err)
	}
}

func TestFinishChatSettlesIntoTheTeamSummaryAndNeverIntoAWorkItem(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	before, err := testStore.WorkItemFacts(ctx, id, nil, FactsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chat, _, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("settle"))
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: chat.RunToken, Alias: "alias-chat", Authorized: 0.05, Models: []string{"m"}, TTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	if n, err := testStore.RunningRuns(ctx, "silver"); err != nil || n != 0 {
		t.Fatalf("team capacity counted a chat message: %d %v", n, err)
	}
	summary, err := testStore.OperatorSummary(ctx, []string{"silver"}, nil, time.Now().Add(-time.Hour))
	if err != nil || len(summary) != 1 || summary[0].Spend.HeldUSD != 0.05 || summary[0].Runs.Running != 1 {
		t.Fatalf("summary while open: %+v %v", summary, err)
	}
	finished, err := testStore.FinishChat(ctx, "workbench", "alice", "settle")
	if err != nil || finished.State != "finished" || finished.FinishedAt == nil {
		t.Fatalf("finish: %v %+v", err, finished)
	}
	again, err := testStore.FinishChat(ctx, "workbench", "alice", "settle")
	if err != nil || !again.FinishedAt.Equal(*finished.FinishedAt) {
		t.Fatalf("second finish: %v %+v", err, again)
	}
	var finishedAudits int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'chat.finished'`).Scan(&finishedAudits); err != nil || finishedAudits != 1 {
		t.Fatalf("chat.finished audits: %d %v", finishedAudits, err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state = 'blocked' WHERE run_token = $1`, chat.RunToken); err != nil {
		t.Fatal(err)
	}
	unsettled, err := testStore.UnsettledLLMAccounts(ctx, 0, 0, 100)
	if err != nil || len(unsettled) != 1 || unsettled[0].RunToken != chat.RunToken {
		t.Fatalf("settlement sweep page: %+v %v", unsettled, err)
	}
	if _, err := testStore.SettleLLMAccount(ctx, chat.RunToken, LLMSettlement{Spend: 0.0321, Evidence: "test", CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	settled, err := testStore.Allowance(ctx, AllowancePurposeChat, AllowanceScopeTeam, "silver", time.Now(), 2)
	if err != nil || settled.HeldUSD != 0 || settled.SettledUSD != 0.0321 || settled.RemainingUSD != 1.9679 || settled.Count != 1 {
		t.Fatalf("settled allowance: %+v %v", settled, err)
	}
	read, err := testStore.Chat(ctx, "workbench", "alice", "settle")
	if err != nil || read.Spend.CostStatus != work.CostSettled || read.Spend.USD == nil || *read.Spend.USD != 0.0321 {
		t.Fatalf("settled chat: %+v %v", read, err)
	}
	summary, err = testStore.OperatorSummary(ctx, []string{"silver"}, nil, time.Now().Add(-time.Hour))
	if err != nil || len(summary) != 1 || summary[0].Spend.SettledUSD != 0.0321 || summary[0].Spend.HeldUSD != 0 || summary[0].Runs.Finished != 1 {
		t.Fatalf("summary after settlement: %+v %v", summary, err)
	}
	if gold, err := testStore.OperatorSummary(ctx, []string{"gold"}, nil, time.Now().Add(-time.Hour)); err != nil || len(gold) != 0 {
		t.Fatalf("another team's summary saw the chat message: %+v %v", gold, err)
	}
	f, err := testStore.WorkItemFacts(ctx, id, nil, FactsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Runs) != 0 || len(f.RunBudgetHolds) != 0 || len(f.Asks) != 0 || !f.ActivityAt.Equal(before.ActivityAt) {
		t.Fatalf("a chat message reached a Work Item's facts: runs=%d holds=%d asks=%d", len(f.Runs), len(f.RunBudgetHolds), len(f.Asks))
	}
}

func TestUnsettledFinishedAccountsListsAChatWithoutAWorkItem(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	chat, _, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("stuck"))
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: chat.RunToken, Alias: "alias-stuck", Authorized: 0.05, Models: []string{"m"}, TTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE run_llm_accounts SET state = 'unknown' WHERE run_token = $1`, chat.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.FinishChat(ctx, "workbench", "alice", "stuck"); err != nil {
		t.Fatal(err)
	}
	page, err := testStore.UnsettledFinishedAccounts(ctx, []string{"silver"}, 200)
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].WorkItemID != nil || page.Accounts[0].Team != "silver" || page.HeldUSD != 0.05 {
		t.Fatalf("unsettled chat: %+v %v", page, err)
	}
}
