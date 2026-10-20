package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestHiveAndAgentReadsNormalizeDatabaseTimesToUTC(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/utc-contract")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	h, err = s.Hive(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.Agent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := s.Pool.Config()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.FixedZone("non-UTC", -7*60*60)},
		})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reader := &Store{Pool: pool}
	gotHive, err := reader.Hive(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotAgent, err := reader.Agent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []time.Time{gotHive.CreatedAt, gotHive.UpdatedAt, gotAgent.CreatedAt, gotAgent.UpdatedAt} {
		if value.Location() != time.UTC {
			t.Fatalf("non-UTC database time escaped into API objects: %s", value)
		}
	}
	if !gotHive.CreatedAt.Equal(h.CreatedAt) || !gotAgent.CreatedAt.Equal(a.CreatedAt) {
		t.Fatal("normalization changed an instant")
	}
}
