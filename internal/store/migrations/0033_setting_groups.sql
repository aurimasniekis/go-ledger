-- Runtime settings (ADR-0066, task S1a): the stored settings layer. One row
-- per singleton settings group ("reporting", "mcp", ...) or keyed object
-- ("decision_tasks.categorisation", ...), created by Ledger on the group's
-- first write through the Management API; until then the API synthesises the
-- group with version 1 from the configuration. stored holds only the members
-- set through the API, as one JSON object keyed by member name; defaults and
-- configured values are never copied into it. Secrets are never kept here.
-- The captured reporting zone stays in the settings table (point 6a).
CREATE TABLE setting_groups (
    id         TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 127 AND name GLOB '[a-z]*' AND name NOT GLOB '*[^a-z0-9_.]*'
        AND name NOT GLOB '*.*.*' AND name NOT GLOB '*.' AND name NOT GLOB '*.[^a-z]*'),
    stored     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(stored) AND json_type(stored) = 'object'),
    version    INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;
