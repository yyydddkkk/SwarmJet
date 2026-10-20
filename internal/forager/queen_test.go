package forager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	queenprompt "github.com/yyydddkkk/SwarmJet/internal/queen"
)

const testQueenID = "queen-1"

func (s *fakeStore) Queen(_ context.Context, hiveID string) (domain.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agent := range s.agents {
		if agent.HiveID == hiveID && agent.Role == domain.RoleQueen {
			return agent, nil
		}
	}
	return domain.Agent{}, domain.ErrNotFound
}

func newQueenFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := newFixture(t, mutate)
	f.store.mu.Lock()
	delete(f.store.agents, testAgentID)
	h := f.store.hives[testHiveID]
	h.QueenEnabled = true
	f.store.hives[testHiveID] = h
	f.store.mu.Unlock()
	f.store.addAgent(domain.Agent{ID: testQueenID, HiveID: testHiveID, Role: domain.RoleQueen, State: domain.AgentDisabled})
	ctx, cancel := context.WithCancel(context.Background())
	f.sup.runCtx = ctx
	t.Cleanup(cancel)
	return f
}

func queenMessage(f *fixture, id string) domain.Message {
	return f.store.addMessage(domain.Message{
		ID: id, HiveID: testHiveID, From: domain.LocalAddress("user"), To: domain.AddressQueen,
		Kind: domain.MessageTask, Trust: domain.TrustUser, Body: "Manage this Hive",
	})
}

func scanQueen(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

func waitQueenTurn(t *testing.T, f *fixture, id string) {
	t.Helper()
	eventually(t, 5*time.Second, func() bool {
		a := f.sup.actor(testQueenID, testHiveID, true)
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		return f.store.isHandled(id) && f.store.state(testQueenID) == domain.AgentParked && !running
	}, "Queen did not finish and park")
}

func TestQueenScanEnablesAndWakes(t *testing.T) {
	f := newQueenFixture(t, nil)
	f.store.mu.Lock()
	h := f.store.hives[testHiveID]
	h.QueenEnabled = false
	f.store.hives[testHiveID] = h
	f.store.mu.Unlock()
	msg := queenMessage(f, "queen-task")
	scanQueen(t, f)
	if f.store.state(testQueenID) != domain.AgentDisabled || len(f.runtime.startsFor(testQueenID)) != 0 || f.store.isHandled(msg.ID) {
		t.Fatal("disabled Queen started or consumed its inbox")
	}
	f.store.mu.Lock()
	h.QueenEnabled = true
	f.store.hives[testHiveID] = h
	f.store.mu.Unlock()
	scanQueen(t, f)
	waitQueenTurn(t, f, msg.ID)
	if len(f.runtime.startsFor(testQueenID)) != 1 {
		t.Fatal("enabled Queen did not start exactly once")
	}
}

func TestQueenDisableInterruptsPromptAndRetainsCell(t *testing.T) {
	f := newQueenFixture(t, nil)
	script := f.runtime.script(testQueenID)
	script.blockPrompts = 1
	msg := queenMessage(f, "blocking-queen-task")
	f.notify(msg)
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "Queen prompt did not start")
	f.store.mu.Lock()
	h := f.store.hives[testHiveID]
	h.QueenEnabled = false
	f.store.hives[testHiveID] = h
	f.store.mu.Unlock()
	scanQueen(t, f)
	if f.store.state(testQueenID) != domain.AgentDisabled || f.runtime.stopCount(testQueenID) == 0 {
		t.Fatal("disabling Queen did not stop its Cell and set disabled")
	}
	if f.runtime.destroyCount(testQueenID) != 0 || f.runtime.removeCount(testQueenID) != 0 || f.store.tokenCount() != 0 {
		t.Fatal("disable destroyed the Cell or left its token active")
	}
	if f.store.isHandled(msg.ID) {
		t.Fatal("interrupted Queen input was marked handled")
	}
}

func TestQueenRetriesStartAndRestartsAfterPromptCrash(t *testing.T) {
	for _, failure := range []string{"start", "prompt"} {
		t.Run(failure, func(t *testing.T) {
			f := newQueenFixture(t, nil)
			script := f.runtime.script(testQueenID)
			if failure == "start" {
				script.startErrs = []error{errors.New("temporary start failure")}
			} else {
				script.promptErrs = []error{errors.New("CLI crashed")}
			}
			msg := queenMessage(f, "retry-task")
			scanQueen(t, f)
			waitQueenTurn(t, f, msg.ID)
			if got := len(f.runtime.startsFor(testQueenID)); got != 2 {
				t.Fatalf("Runtime.Start calls = %d, want 2", got)
			}
			if failure == "prompt" && (script.promptTotal() != 2 || f.runtime.stopCount(testQueenID) == 0) {
				t.Fatal("crashed prompt was not rerun after stopping and restarting Cell")
			}
			if f.runtime.destroyCount(testQueenID) != 0 {
				t.Fatal("retry destroyed persistent Cell volumes")
			}
		})
	}
}

func TestQueenExhaustionReportsAndNextScanRetriesUnhandled(t *testing.T) {
	for _, failure := range []string{"start", "prompt"} {
		t.Run(failure, func(t *testing.T) {
			f := newQueenFixture(t, func(o *Options) { o.MaxFailures = 2 })
			script := f.runtime.script(testQueenID)
			failures := []error{errors.New("first failure"), errors.New("second failure")}
			if failure == "start" {
				script.startErrs = failures
			} else {
				script.promptErrs = failures
			}
			msg := queenMessage(f, "recover-task")
			scanQueen(t, f)
			eventually(t, 5*time.Second, func() bool {
				a := f.sup.actor(testQueenID, testHiveID, true)
				a.mu.Lock()
				running := a.running
				a.mu.Unlock()
				return f.store.state(testQueenID) == domain.AgentFailed && !running
			}, "Queen did not exhaust retry budget")
			if f.store.isHandled(msg.ID) || f.runtime.destroyCount(testQueenID) != 0 {
				t.Fatal("failed Queen consumed input or destroyed volumes")
			}
			f.store.mu.Lock()
			reports := append([]domain.Message(nil), f.store.sent...)
			f.store.mu.Unlock()
			if len(reports) != 1 || reports[0].From != domain.AddressSystem || reports[0].To != domain.AddressHive || reports[0].Trust != domain.TrustSystem || reports[0].Kind != domain.MessageReport {
				t.Fatalf("failure reports = %+v, want one system report to Hive", reports)
			}
			scanQueen(t, f)
			waitQueenTurn(t, f, msg.ID)
			if len(f.runtime.startsFor(testQueenID)) != 3 {
				t.Fatal("next scan did not restart failed Queen")
			}
		})
	}
}

func TestQueenLifetimeModelSpecAndNoPublish(t *testing.T) {
	for _, model := range []string{"", "custom/queen-model"} {
		t.Run("model="+model, func(t *testing.T) {
			f := newQueenFixture(t, nil)
			f.store.setHiveConfig(testHiveID, func(c *domain.HiveConfig) { c.QueenModel = model; c.ForagerMaxLifetimeHours = 1 })
			a := f.store.agentCopy(testQueenID)
			a.CreatedAt = f.clock.Now().Add(-365 * 24 * time.Hour)
			f.store.setAgent(a)
			msg := queenMessage(f, "old-queen-task")
			scanQueen(t, f)
			waitQueenTurn(t, f, msg.ID)
			starts := f.runtime.startsFor(testQueenID)
			if len(starts) != 1 {
				t.Fatalf("starts = %d, want one despite expired Forager lifetime", len(starts))
			}
			spec := starts[0]
			wantModel := model
			if wantModel == "" {
				wantModel = f.sup.opts.Model
			}
			if spec.Model != wantModel || spec.Branch != "queen/"+testHiveID || !spec.ReadOnly || spec.BaseBranch != "" || spec.Repository != "acme/widgets" {
				t.Fatalf("unexpected Queen spec: %+v", spec)
			}
			if spec.MCPToken == "" || spec.LLMToken != spec.MCPToken || f.store.tokenCount() != 1 {
				t.Fatal("Queen did not receive its own shared MCP/model-proxy token")
			}
			if len(f.publisher.callsCopy()) != 0 {
				t.Fatal("Queen published repository changes")
			}
		})
	}
}

func TestQueenCompleteControlIsInputAndPromptHasCoreStyleProvenance(t *testing.T) {
	f := newQueenFixture(t, nil)
	f.store.methodology = &domain.Methodology{Manifest: domain.MethodologyManifest{Queen: domain.QueenStyle{Prompt: "queen-style.md"}}}
	f.store.methodologyFiles = []domain.MethodologyFile{{Path: "queen-style.md", Content: "Explain choices concisely."}}
	m := f.store.addMessage(domain.Message{
		ID: "complete-queen", HiveID: testHiveID, From: domain.AddressSystem, To: domain.AddressQueen,
		Kind: domain.MessageControl, Trust: domain.TrustSystem, Body: "complete\nThe Queen remains available.",
	})
	f.store.addMessage(domain.Message{
		ID: "external-queen", HiveID: testHiveID, From: domain.Address("github:issue"), To: domain.AddressQueen,
		Kind: domain.MessageEvent, Trust: domain.TrustExternal, Body: "Untrusted issue text",
	})
	f.notify(m)
	waitQueenTurn(t, f, m.ID)
	prompts := f.runtime.script(testQueenID).prompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want one", len(prompts))
	}
	header := queenprompt.Header(context.Background(), f.store, testHiveID)
	if !strings.HasPrefix(prompts[0], header) || !strings.Contains(header, "Explain choices concisely.") {
		t.Fatal("Queen turn did not start with embedded core plus active style")
	}
	for _, text := range []string{"kind=control from=system trust=system", "kind=event from=github:issue trust=external", "Untrusted issue text", "complete\nThe Queen remains available."} {
		if !strings.Contains(prompts[0], text) {
			t.Errorf("Queen prompt missing %q", text)
		}
	}
	if f.store.state(testQueenID) == domain.AgentCompleted || f.runtime.destroyCount(testQueenID) != 0 {
		t.Fatal("complete control terminated the Queen")
	}
}

func TestQueenMalformedForagerAddressDoesNotPoisonActor(t *testing.T) {
	f := newQueenFixture(t, nil)
	malformed := f.store.addMessage(domain.Message{
		ID: "wrong-address", HiveID: testHiveID, From: domain.LocalAddress("user"),
		To: domain.ForagerAddress(testQueenID), Kind: domain.MessageControl, Body: "complete",
	})
	f.notify(malformed)
	msg := queenMessage(f, "right-address")
	f.notify(msg)
	waitQueenTurn(t, f, msg.ID)
	if f.store.isHandled(malformed.ID) || f.runtime.destroyCount(testQueenID) != 0 {
		t.Fatal("malformed Forager address was processed as a Queen control")
	}
}

func TestQueenRecoversAfterFailureStatePersistenceError(t *testing.T) {
	f := newQueenFixture(t, func(o *Options) { o.MaxFailures = 1 })
	script := f.runtime.script(testQueenID)
	script.blockPrompts = 1
	msg := queenMessage(f, "persistence-recovery")
	f.notify(msg)
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "Queen prompt did not start")
	// Timeout the turn while persisting its failed state is unavailable.
	f.store.mu.Lock()
	f.store.transitionErr = errors.New("temporary state write failure")
	f.store.mu.Unlock()
	eventually(t, 5*time.Second, func() bool { return f.clock.Pending() > 0 }, "Queen timeout was not scheduled")
	f.clock.Advance(121 * time.Minute)
	eventually(t, 5*time.Second, func() bool {
		a := f.sup.actor(testQueenID, testHiveID, true)
		a.mu.Lock()
		failed := a.terminal && !a.running
		a.mu.Unlock()
		return failed
	}, "Queen did not finish failed-state cleanup")
	if f.store.state(testQueenID) != domain.AgentActive || f.store.isHandled(msg.ID) {
		t.Fatal("state persistence fault did not leave active Queen with unhandled input")
	}
	f.store.mu.Lock()
	f.store.transitionErr = nil
	f.store.mu.Unlock()
	scanQueen(t, f)
	waitQueenTurn(t, f, msg.ID)
}

func TestQueenReenableRecoversFromIncompleteDisable(t *testing.T) {
	f := newQueenFixture(t, nil)
	script := f.runtime.script(testQueenID)
	script.blockPrompts = 1
	msg := queenMessage(f, "disable-recovery")
	f.notify(msg)
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "Queen prompt did not start")
	f.store.mu.Lock()
	h := f.store.hives[testHiveID]
	h.QueenEnabled = false
	f.store.hives[testHiveID] = h
	f.store.revokeErr = errors.New("temporary token revoke failure")
	f.store.mu.Unlock()
	if err := f.sup.Scan(context.Background()); err == nil {
		t.Fatal("scan did not report incomplete disable")
	}
	f.store.mu.Lock()
	h.QueenEnabled = true
	f.store.hives[testHiveID] = h
	f.store.revokeErr = nil
	f.store.mu.Unlock()
	scanQueen(t, f)
	waitQueenTurn(t, f, msg.ID)
}

func TestQueenLargeStyleLeavesRoomForInbox(t *testing.T) {
	core := queenprompt.Header(context.Background(), nil, testHiveID)
	header := core + "\n## Methodology Queen style\n\n" + strings.Repeat("style", MaxPromptChars)
	prompt := buildQueenPrompt(header, []domain.Message{{From: domain.AddressSystem, Trust: domain.TrustSystem, Kind: domain.MessageTask, Body: "Read this real task"}})
	if !strings.HasPrefix(prompt, core) || !strings.Contains(prompt, "Read this real task") {
		t.Fatal("large Queen style displaced core instructions or Inbox input")
	}
}

func TestQueenDisableStateWriteFailureDoesNotReuseRevokedToken(t *testing.T) {
	f := newQueenFixture(t, nil)
	msg := queenMessage(f, "before-disable")
	f.notify(msg)
	waitQueenTurn(t, f, msg.ID)
	first := f.runtime.startsFor(testQueenID)[0].MCPToken
	f.store.mu.Lock()
	h := f.store.hives[testHiveID]
	h.QueenEnabled = false
	f.store.hives[testHiveID] = h
	f.store.transitionErr = errors.New("temporary state write failure")
	f.store.mu.Unlock()
	if err := f.sup.Scan(context.Background()); err == nil {
		t.Fatal("expected failed disabled state write")
	}
	f.store.mu.Lock()
	h.QueenEnabled = true
	f.store.hives[testHiveID] = h
	f.store.transitionErr = nil
	f.store.mu.Unlock()
	msg = queenMessage(f, "after-disable-failure")
	scanQueen(t, f)
	waitQueenTurn(t, f, msg.ID)
	starts := f.runtime.startsFor(testQueenID)
	if len(starts) != 2 || starts[1].MCPToken == first || f.store.tokenCount() != 1 {
		t.Fatal("Queen reused a revoked token after an interrupted disable")
	}
}

func TestQueenRecoversIdleActiveRecordAndHibernates(t *testing.T) {
	f := newQueenFixture(t, nil)
	a := f.store.agentCopy(testQueenID)
	a.State = domain.AgentActive
	a.UpdatedAt = f.clock.Now().Add(-48 * time.Hour)
	f.store.setAgent(a)
	scanQueen(t, f)
	if f.store.state(testQueenID) != domain.AgentHibernated || f.runtime.stopCount(testQueenID) != 1 || f.runtime.removeCount(testQueenID) != 1 || f.runtime.destroyCount(testQueenID) != 0 {
		t.Fatal("idle recovered Queen did not hibernate while retaining its volumes")
	}
	msg := queenMessage(f, "wake-hibernated")
	f.notify(msg)
	waitQueenTurn(t, f, msg.ID)
}
