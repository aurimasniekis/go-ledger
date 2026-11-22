-- Commitment leeway (task DF2, ADR-0052, decision D14): what the user knows
-- about the grace a creditor allows after a due date. Leeway is user-entered
-- only: nothing writes it from payment history, bank data or model output, and
-- there is no default.
--
-- leeway_kind is NULL when the user recorded nothing (existing commitments
-- stay that way). 'unknown' is the user's explicit statement that they do not
-- know and 'none' that no grace is allowed; neither carries days or a date, and
-- neither is read as zero. 'period' needs leeway_days (1 to 365, counted from
-- each occurrence's due date) and 'deadline' leeway_until (a fixed last day of
-- grace). The optional text members hold the conditions the grace depends on,
-- the fees or other consequences the user knows of, and where the information
-- comes from; any are NULL when not given. Changing leeway never touches a
-- commitment's occurrences, their due dates or payment evidence: a grace only
-- annotates an overdue occurrence (decision D7, ADR-0031).
ALTER TABLE commitments ADD COLUMN leeway_kind TEXT CHECK (leeway_kind IS NULL OR leeway_kind IN ('unknown', 'none', 'period', 'deadline'));
ALTER TABLE commitments ADD COLUMN leeway_days INTEGER CHECK (leeway_days IS NULL OR leeway_days BETWEEN 1 AND 365);
ALTER TABLE commitments ADD COLUMN leeway_until TEXT CHECK (leeway_until IS NULL OR (length(leeway_until) = 10 AND leeway_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(leeway_until) = leeway_until));
ALTER TABLE commitments ADD COLUMN leeway_conditions TEXT CHECK (leeway_conditions IS NULL OR length(leeway_conditions) BETWEEN 1 AND 500);
ALTER TABLE commitments ADD COLUMN leeway_consequences TEXT CHECK (leeway_consequences IS NULL OR length(leeway_consequences) BETWEEN 1 AND 500);
ALTER TABLE commitments ADD COLUMN leeway_source TEXT CHECK (leeway_source IS NULL OR length(leeway_source) BETWEEN 1 AND 200);

-- The members fit the kind; nothing may be recorded without a kind.
CREATE TRIGGER commitments_leeway_insert
BEFORE INSERT ON commitments
WHEN (NEW.leeway_kind IS NULL AND (NEW.leeway_days IS NOT NULL OR NEW.leeway_until IS NOT NULL OR NEW.leeway_conditions IS NOT NULL OR NEW.leeway_consequences IS NOT NULL OR NEW.leeway_source IS NOT NULL))
  OR (NEW.leeway_kind = 'period') <> (NEW.leeway_days IS NOT NULL)
  OR (NEW.leeway_kind = 'deadline') <> (NEW.leeway_until IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'leeway members must match the leeway kind');
END;

CREATE TRIGGER commitments_leeway_update
BEFORE UPDATE OF leeway_kind, leeway_days, leeway_until, leeway_conditions, leeway_consequences, leeway_source ON commitments
WHEN (NEW.leeway_kind IS NULL AND (NEW.leeway_days IS NOT NULL OR NEW.leeway_until IS NOT NULL OR NEW.leeway_conditions IS NOT NULL OR NEW.leeway_consequences IS NOT NULL OR NEW.leeway_source IS NOT NULL))
  OR (NEW.leeway_kind = 'period') <> (NEW.leeway_days IS NOT NULL)
  OR (NEW.leeway_kind = 'deadline') <> (NEW.leeway_until IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'leeway members must match the leeway kind');
END;
