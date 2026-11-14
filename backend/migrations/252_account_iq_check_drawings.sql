-- Latest IQ check drawing per account (an animated pelican-on-a-bicycle SVG),
-- kept for visual review only. Rows are overwritten each round.
CREATE TABLE IF NOT EXISTS account_iq_check_drawings (
  account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  svg        TEXT NOT NULL DEFAULT '',
  error      TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  latency_ms BIGINT NOT NULL DEFAULT 0,
  drawn_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
