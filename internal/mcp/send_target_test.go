package mcp

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func newForagerWithToken(t *testing.T, env *testEnv, hiveID string) (domain.Agent, string) {
	t.Helper()
	forager, err := env.backend.CreateForager(context.Background(), domain.Agent{
		HiveID: hiveID,
		Task:   "fix the bug",
	})
	if err != nil {
		t.Fatalf("CreateForager: %v", err)
	}
	token, _, err := env.backend.CreateAgentToken(context.Background(), forager.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}
	return forager, token
}

func deliverTask(t *testing.T, env *testEnv, hiveID string, from, to domain.Address, body string) {
	t.Helper()
	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: hiveID,
		From:   from,
		To:     to,
		Kind:   domain.MessageTask,
		Body:   body,
	}); err != nil {
		t.Fatalf("SendMessage(task): %v", err)
	}
}

func TestSendForagerDefaultsToDispatcherWhenQueenDisabled(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive := env.seedHive("alpha", "acme/alpha")
	forager, token := newForagerWithToken(t, env, hive.ID)
	deliverTask(t, env, hive.ID, domain.LocalAddress(env.user.ID), domain.ForagerAddress(forager.ID), "please fix it")

	session := env.connect(token)
	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"body":   "done, PR is up",
	}))
	if out.Message.To != string(domain.LocalAddress(env.user.ID)) {
		t.Fatalf("to = %q, want the dispatcher local:%s", out.Message.To, env.user.ID)
	}
	if out.Message.From != string(domain.ForagerAddress(forager.ID)) {
		t.Fatalf("from = %q, want the Forager address", out.Message.From)
	}
}

func TestSendForagerDefaultsToLatestTaskSender(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive := env.seedHive("alpha", "acme/alpha")
	forager, token := newForagerWithToken(t, env, hive.ID)
	deliverTask(t, env, hive.ID, domain.LocalAddress("first"), domain.ForagerAddress(forager.ID), "first task")
	deliverTask(t, env, hive.ID, domain.LocalAddress("second"), domain.ForagerAddress(forager.ID), "second task")
	// A newer non-task message must not change the dispatcher.
	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: hive.ID, From: domain.AddressSystem, To: domain.ForagerAddress(forager.ID),
		Kind: domain.MessageEvent, Body: "CI passed",
	}); err != nil {
		t.Fatal(err)
	}

	session := env.connect(token)
	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"body":   "report",
	}))
	if out.Message.To != string(domain.LocalAddress("second")) {
		t.Fatalf("to = %q, want local:second", out.Message.To)
	}
}

func TestSendForagerDefaultsToHiveWithoutDispatcher(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive := env.seedHive("alpha", "acme/alpha")
	_, token := newForagerWithToken(t, env, hive.ID)

	session := env.connect(token)
	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"body":   "no task message exists",
	}))
	if out.Message.To != string(domain.AddressHive) {
		t.Fatalf("to = %q, want hive", out.Message.To)
	}
}

func TestSendForagerDefaultsToHiveForSystemDispatcher(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive := env.seedHive("alpha", "acme/alpha")
	forager, token := newForagerWithToken(t, env, hive.ID)
	deliverTask(t, env, hive.ID, domain.AddressSystem, domain.ForagerAddress(forager.ID), "router task")

	session := env.connect(token)
	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"body":   "report",
	}))
	if out.Message.To != string(domain.AddressHive) {
		t.Fatalf("to = %q, want hive because system has no readable Inbox", out.Message.To)
	}
}

func TestSendForagerDefaultsToQueenWhenEnabled(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive, err := env.backend.CreateHive(context.Background(), domain.Hive{
		Name: "queen-hive", Repository: "acme/queen", QueenEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	forager, token := newForagerWithToken(t, env, hive.ID)
	deliverTask(t, env, hive.ID, domain.LocalAddress(env.user.ID), domain.ForagerAddress(forager.ID), "please fix it")

	session := env.connect(token)
	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"body":   "queen should get this",
	}))
	if out.Message.To != string(domain.AddressQueen) {
		t.Fatalf("to = %q, want queen while the Queen is enabled", out.Message.To)
	}
}

func TestSendQuestionWithoutTargetFollowsQueenState(t *testing.T) {
	for _, tc := range []struct {
		name         string
		queenEnabled bool
		want         domain.Address
	}{
		{name: "queen enabled", queenEnabled: true, want: domain.AddressQueen},
		{name: "queen disabled", want: domain.LocalAddress("dispatcher")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, Options{})
			hive, err := env.backend.CreateHive(context.Background(), domain.Hive{
				Name: "question-hive", Repository: "acme/question", QueenEnabled: tc.queenEnabled,
			})
			if err != nil {
				t.Fatal(err)
			}
			forager, token := newForagerWithToken(t, env, hive.ID)
			deliverTask(t, env, hive.ID, domain.LocalAddress("dispatcher"), domain.ForagerAddress(forager.ID), "investigate")
			out := decode[sendOutput](t, callTool(t, env.connect(token), "send", map[string]any{
				"hiveId": hive.ID, "kind": "question", "body": "Which approach should I take?",
			}))
			if out.Message.Kind != string(domain.MessageQuestion) || out.Message.To != string(tc.want) {
				t.Fatalf("question = %+v, want kind=question to=%s", out.Message, tc.want)
			}
		})
	}
}

func TestSendExplicitHiveTargetRedirectsToEnabledQueen(t *testing.T) {
	env := newTestEnv(t, Options{})
	hive, err := env.backend.CreateHive(context.Background(), domain.Hive{
		Name: "queen-hive", Repository: "acme/queen", QueenEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, token := newForagerWithToken(t, env, hive.ID)
	session := env.connect(token)

	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": hive.ID,
		"to":     "hive",
		"body":   "explicit inbox",
	}))
	if out.Message.To != string(domain.AddressQueen) {
		t.Fatalf("to = %q, want queen", out.Message.To)
	}
}
