package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
)

func TestHiveQueenStatus(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Repository: "acme/queen", QueenEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/hives/" + h.ID + "/queen"
	if w := hiveDo(t, handler, nil, http.MethodGet, path, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/missing/queen", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, path, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d", w.Code)
	}
	status := hiveDecode[queenStatus](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), http.StatusOK)
	queen, err := s.Queen(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.State != domain.AgentDisabled || status.AgentID != queen.ID || status.Model != llmproxy.DefaultModel || status.LastTurnAt != nil {
		t.Fatalf("initial status = %+v", status)
	}
	// Only Queen reports count as a completed turn; a newer reply is ignored.
	finished := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for _, m := range []domain.Message{
		{From: domain.AddressQueen, Kind: domain.MessageReport, CreatedAt: finished},
		{From: domain.AddressQueen, Kind: domain.MessageReply, CreatedAt: finished.Add(time.Second)},
		{From: domain.ForagerAddress("other"), Kind: domain.MessageReport, CreatedAt: finished.Add(time.Second)},
	} {
		m.HiveID, m.To = h.ID, domain.AddressHive
		if _, err := s.SendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	h.Config.QueenModel = "provider/chosen-model"
	h.QueenEnabled = false
	if _, err := s.UpdateHive(ctx, h); err != nil {
		t.Fatal(err)
	}
	status = hiveDecode[queenStatus](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), http.StatusOK)
	if status.Enabled || status.Model != "provider/chosen-model" || status.LastTurnAt == nil || !status.LastTurnAt.Equal(finished) {
		t.Fatalf("updated status = %+v", status)
	}
	// The configured deployment default is used when the Hive has no override.
	h.Config.QueenModel = ""
	h.Version++
	if _, err := s.UpdateHive(ctx, h); err != nil {
		t.Fatal(err)
	}
	handler = New(s, Config{DefaultModel: "provider/deployment-model"})
	status = hiveDecode[queenStatus](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), http.StatusOK)
	if status.Model != "provider/deployment-model" {
		t.Fatalf("deployment default = %q", status.Model)
	}
}
