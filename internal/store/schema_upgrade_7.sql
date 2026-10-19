-- Round 3: a GitHub App created from the onboarding page. The private key and
-- webhook secret are stored only as secretbox ciphertext; the plaintext never
-- reaches the database. The singleton row is replaced when the user creates a
-- new App, so reads do not need to guess which App is current.

CREATE TABLE IF NOT EXISTS github_apps(
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  app_id bigint NOT NULL CHECK (app_id > 0),
  slug text NOT NULL DEFAULT '',
  html_url text NOT NULL DEFAULT '',
  private_key_cipher text NOT NULL,
  webhook_secret_cipher text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- One-time manifest state. Only SHA-256 hashes are stored. callback_hash binds
-- the state to the browser that started the flow, because the console session
-- cookie is SameSite=Strict and is not sent on GitHub's cross-site redirect.
CREATE TABLE IF NOT EXISTS github_app_manifest_states(
  state_hash text PRIMARY KEY,
  callback_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS github_app_manifest_states_expiry ON github_app_manifest_states(expires_at);
