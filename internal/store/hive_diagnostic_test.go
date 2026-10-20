package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestHiveDiagnosticBypassesQueenRoutingOnlyExplicitly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Repository: "acme/diagnostic", QueenEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	const body = "Queen failed: model unavailable"
	diagnostic, err := s.SendHiveDiagnostic(ctx, h.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.From != domain.AddressSystem || diagnostic.Trust != domain.TrustSystem || diagnostic.To != domain.AddressHive || diagnostic.Kind != domain.MessageReport {
		t.Fatalf("diagnostic lost platform identity or Hive destination: %+v", diagnostic)
	}
	// The identical message sent through normal routing must still reach Queen.
	normal, err := s.SendMessage(ctx, domain.Message{
		HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive,
		Kind: domain.MessageReport, Trust: domain.TrustSystem, Body: body,
	})
	if err != nil || normal.To != domain.AddressQueen {
		t.Fatalf("ordinary system report did not route to Queen: %+v, %v", normal, err)
	}
	inbox, err := s.Unhandled(ctx, h.ID, domain.AddressHive, 100)
	if err != nil || len(inbox) != 1 || inbox[0].ID != diagnostic.ID || inbox[0].From != domain.AddressSystem || inbox[0].Trust != domain.TrustSystem {
		t.Fatalf("stored Hive diagnostic = %+v, %v", inbox, err)
	}
	if err = s.DeliverMessage(ctx, diagnostic.ID); err != nil {
		t.Fatal(err)
	}
	inbox, err = s.Unhandled(ctx, h.ID, domain.AddressQueen, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range inbox {
		if message.ID == diagnostic.ID {
			t.Fatal("diagnostic was redelivered to Queen")
		}
	}
	jobs := s.Jobs.(*memoryJobs).delivered()
	if len(jobs) != 3 || jobs[1] != diagnostic.ID {
		t.Fatalf("diagnostic delivery job missing: %v", jobs)
	}
	if _, err = s.SendHiveDiagnostic(ctx, h.ID, strings.Repeat("x", domain.MaxMessageBody+1)); !errors.Is(err, domain.ErrMessageTooLarge) {
		t.Fatalf("diagnostic bypassed message validation: %v", err)
	}
}
