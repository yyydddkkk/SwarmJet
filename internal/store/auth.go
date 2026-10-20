package store

import "context"

// Principal is the browser session identity. The console has one administrator
// account, stored as a breakglass session with no users row.
type Principal struct {
	UserID        string `json:"userId,omitempty"`
	Email         string `json:"email,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	PlatformAdmin bool   `json:"platformAdmin"`
	Breakglass    bool   `json:"breakglass"`
}

// AddBreakglassSession stores the administrator session created by the single
// ADMIN_USERNAME / ADMIN_PASSWORD login.
func (s *Store) AddBreakglassSession(ctx context.Context, hash string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,expires_at,principal_type) VALUES($1,now()+interval '12 hours','breakglass')`, hash)
	return err
}

// SessionPrincipal resolves an unexpired administrator session. Only the
// breakglass session created by the single administrator login is accepted;
// legacy multi-user sessions are ignored even if their rows still exist.
func (s *Store) SessionPrincipal(ctx context.Context, hash string) (Principal, error) {
	var typ string
	err := s.Pool.QueryRow(ctx, `SELECT principal_type FROM sessions WHERE token_hash=$1 AND expires_at>now() AND principal_type='breakglass'`, hash).Scan(&typ)
	if err != nil {
		return Principal{}, mapErr(err)
	}
	return Principal{PlatformAdmin: true, Breakglass: true}, nil
}
