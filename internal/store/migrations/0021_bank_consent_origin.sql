-- When a consent started and who started it (task B4b, ADR-0040). A consent
-- state is single use: it is claimed (cleared) before the provider is called,
-- it expires after a time to live, and only the person who started it may
-- complete it. consent_initiator is the starting user's id (a token acts as
-- its owner); it holds no secret. Rows that already wait for a redirect take
-- their last update as the start, so an abandoned one expires like a new one.
ALTER TABLE bank_connections ADD COLUMN consent_started_at TEXT CHECK (consent_started_at IS NULL OR (length(consent_started_at) = 30 AND consent_started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE bank_connections ADD COLUMN consent_initiator TEXT NOT NULL DEFAULT '';
UPDATE bank_connections SET consent_started_at = updated_at WHERE consent_state IS NOT NULL;
