-- Instance settings that must survive restarts and config changes. The
-- reporting time zone is captured here on first start (decision D5).
CREATE TABLE settings (
    key        TEXT PRIMARY KEY CHECK (key <> ''),
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;
