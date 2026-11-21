-- The tolerance a payment match was confirmed within (D39 5a, ADR-0042).
-- When a bank match is confirmed (or its amount corrected while confirmed) and
-- the occurrence's counted evidence is confirmed bank matches alone, short of
-- the occurrence's amount by no more than the commitment's match tolerance,
-- the store records that tolerance here, inside the write transaction. The
-- payment state is derived from this record, never from the commitment's
-- current tolerance, so editing a tolerance later does not change past
-- occurrences. Both columns are NULL when the match was not confirmed within
-- a tolerance (every existing match: none was). The pair is set or cleared
-- together, in the match's own currency; nothing is converted (ADR-0005).
ALTER TABLE payment_matches ADD COLUMN tolerance_amount TEXT CHECK (tolerance_amount IS NULL OR (length(tolerance_amount) BETWEEN 1 AND 62 AND length(replace(replace(tolerance_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND tolerance_amount NOT GLOB '*[^0-9.-]*' AND tolerance_amount NOT GLOB '0[0-9]*' AND tolerance_amount NOT GLOB '-0[0-9]*' AND replace(replace(tolerance_amount, '0', ''), '.', '') <> '-' AND tolerance_amount NOT GLOB '?*-*' AND tolerance_amount NOT GLOB '*.*.*' AND tolerance_amount NOT GLOB '*.' AND tolerance_amount NOT GLOB '.*' AND tolerance_amount NOT GLOB '-.*' AND tolerance_amount GLOB '*[0-9]*' AND tolerance_amount NOT GLOB '-*'));
ALTER TABLE payment_matches ADD COLUMN tolerance_currency TEXT CHECK (tolerance_currency IS NULL OR (length(tolerance_currency) = 3 AND tolerance_currency GLOB '[A-Z][A-Z][A-Z]'));

CREATE TRIGGER payment_matches_tolerance_insert
BEFORE INSERT ON payment_matches
WHEN (NEW.tolerance_amount IS NULL) <> (NEW.tolerance_currency IS NULL) OR NEW.tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a recorded tolerance has an amount and the match''s currency');
END;

CREATE TRIGGER payment_matches_tolerance_update
BEFORE UPDATE OF currency, tolerance_amount, tolerance_currency ON payment_matches
WHEN (NEW.tolerance_amount IS NULL) <> (NEW.tolerance_currency IS NULL) OR NEW.tolerance_currency <> NEW.currency
BEGIN
    SELECT RAISE(ABORT, 'a recorded tolerance has an amount and the match''s currency');
END;
