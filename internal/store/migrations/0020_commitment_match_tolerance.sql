-- Per-commitment amount tolerance for automatic payment matching (task X2,
-- decision D9: auto-confirm needs the exact amount "or within the user's
-- tolerance"; ADR-0038). Both columns are NULL when the user set none: then
-- only an exact amount can confirm automatically. When set, the tolerance is a
-- non-negative exact decimal in the commitment's own currency (amounts are
-- never converted, ADR-0005); the pair is set or cleared together, and a
-- commitment's currency cannot change away from its tolerance's. Existing
-- commitments get no tolerance. It is never summed or compared in SQL.
ALTER TABLE commitments ADD COLUMN match_tolerance_amount TEXT CHECK (match_tolerance_amount IS NULL OR (length(match_tolerance_amount) BETWEEN 1 AND 62 AND length(replace(replace(match_tolerance_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND match_tolerance_amount NOT GLOB '*[^0-9.-]*' AND match_tolerance_amount NOT GLOB '0[0-9]*' AND match_tolerance_amount NOT GLOB '-0[0-9]*' AND replace(replace(match_tolerance_amount, '0', ''), '.', '') <> '-' AND match_tolerance_amount NOT GLOB '?*-*' AND match_tolerance_amount NOT GLOB '*.*.*' AND match_tolerance_amount NOT GLOB '*.' AND match_tolerance_amount NOT GLOB '.*' AND match_tolerance_amount NOT GLOB '-.*' AND match_tolerance_amount GLOB '*[0-9]*' AND match_tolerance_amount NOT GLOB '-*'));
ALTER TABLE commitments ADD COLUMN match_tolerance_currency TEXT CHECK (match_tolerance_currency IS NULL OR (length(match_tolerance_currency) = 3 AND match_tolerance_currency GLOB '[A-Z][A-Z][A-Z]'));

CREATE TRIGGER commitments_match_tolerance_insert
BEFORE INSERT ON commitments
WHEN (NEW.match_tolerance_amount IS NULL) <> (NEW.match_tolerance_currency IS NULL) OR NEW.match_tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a match tolerance has an amount and the commitment''s currency');
END;

CREATE TRIGGER commitments_match_tolerance_update
BEFORE UPDATE OF currency, match_tolerance_amount, match_tolerance_currency ON commitments
WHEN (NEW.match_tolerance_amount IS NULL) <> (NEW.match_tolerance_currency IS NULL) OR NEW.match_tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a match tolerance has an amount and the commitment''s currency');
END;
