-- Deferral plans (DF1, ADR-0050). A deferral plan records that the user means
-- to pay an outstanding occurrence later than its due date: a personal plan, an
-- extension the creditor agreed to, or a documented grace period (decision D14:
-- a creditor-approved change is a distinct plan type). A plan is separate from
-- the occurrence: it never changes the occurrence's due date, amount or
-- payment state, so the obligation, its overdue history (decision D7: overdue
-- counts from the original due date) and its payments stay as they are. A plan
-- is never payment evidence: nothing here makes an occurrence paid.
--
-- original_due_date and original_amount are the occurrence's due date and
-- amount when the plan was made, and outstanding_amount what was still owed
-- then; they are history and never change (the occurrence's live state is
-- derived when read). Amounts are exact decimal text (ADR-0005), never summed in
-- SQL. A plan refers to its occurrence with ON DELETE RESTRICT: an occurrence
-- with a plan (active or undone) is cancelled instead of deleted when its
-- commitment's terms no longer schedule it (D31), so the plan's history stays.
--
-- An occurrence has at most one active plan. Revising changes plan_type,
-- planned_date and reason and increases version; undoing sets undone_at, after
-- which the row never changes again. Every change is one row of
-- deferral_plan_events.
CREATE TABLE deferral_plans (
    id                 TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    occurrence_id      TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE RESTRICT CHECK (length(occurrence_id) = 26 AND occurrence_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_type          TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date       TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason             TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    original_due_date  TEXT NOT NULL CHECK (length(original_due_date) = 10 AND original_due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(original_due_date) = original_due_date),
    original_amount    TEXT NOT NULL CHECK (length(original_amount) BETWEEN 1 AND 62 AND length(replace(replace(original_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND original_amount NOT GLOB '*[^0-9.-]*' AND original_amount NOT GLOB '0[0-9]*' AND original_amount NOT GLOB '-0[0-9]*' AND replace(replace(original_amount, '0', ''), '.', '') <> '-' AND original_amount NOT GLOB '?*-*' AND original_amount NOT GLOB '*.*.*' AND original_amount NOT GLOB '*.' AND original_amount NOT GLOB '.*' AND original_amount NOT GLOB '-.*' AND original_amount GLOB '*[0-9]*' AND original_amount NOT GLOB '-*' AND replace(replace(original_amount, '0', ''), '.', '') <> ''),
    original_currency  TEXT NOT NULL CHECK (length(original_currency) = 3 AND original_currency GLOB '[A-Z][A-Z][A-Z]'),
    outstanding_amount   TEXT NOT NULL CHECK (length(outstanding_amount) BETWEEN 1 AND 62 AND length(replace(replace(outstanding_amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND outstanding_amount NOT GLOB '*[^0-9.-]*' AND outstanding_amount NOT GLOB '0[0-9]*' AND outstanding_amount NOT GLOB '-0[0-9]*' AND replace(replace(outstanding_amount, '0', ''), '.', '') <> '-' AND outstanding_amount NOT GLOB '?*-*' AND outstanding_amount NOT GLOB '*.*.*' AND outstanding_amount NOT GLOB '*.' AND outstanding_amount NOT GLOB '.*' AND outstanding_amount NOT GLOB '-.*' AND outstanding_amount GLOB '*[0-9]*' AND outstanding_amount NOT GLOB '-*' AND replace(replace(outstanding_amount, '0', ''), '.', '') <> ''),
    outstanding_currency TEXT NOT NULL CHECK (length(outstanding_currency) = 3 AND outstanding_currency GLOB '[A-Z][A-Z][A-Z]'),
    created_at         TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    updated_at         TEXT NOT NULL CHECK (length(updated_at) = 30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    undone_at          TEXT CHECK (undone_at IS NULL OR (length(undone_at) = 30 AND undone_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (outstanding_currency = original_currency)
) STRICT;

CREATE INDEX deferral_plans_occurrence ON deferral_plans (occurrence_id, id);
CREATE INDEX deferral_plans_planned ON deferral_plans (planned_date, id) WHERE undone_at IS NULL;

-- At most one active plan per occurrence: a deferred occurrence is planned
-- once, so its amount is never counted twice.
CREATE UNIQUE INDEX deferral_plans_one_active ON deferral_plans (occurrence_id) WHERE undone_at IS NULL;

-- What a plan is about and what was owed when it was made never change.
CREATE TRIGGER deferral_plans_identity_immutable
BEFORE UPDATE OF id, occurrence_id, original_due_date, original_amount, original_currency, outstanding_amount, outstanding_currency, created_at ON deferral_plans
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan cannot change its occurrence or what was owed when it was made');
END;

-- An undone plan is history: it never changes again.
CREATE TRIGGER deferral_plans_undone_final
BEFORE UPDATE ON deferral_plans
WHEN OLD.undone_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'an undone deferral plan cannot change');
END;

-- A plan is in its occurrence's currency.
CREATE TRIGGER deferral_plans_currency
BEFORE INSERT ON deferral_plans
WHEN NEW.original_currency IS NOT (SELECT currency FROM commitment_occurrences WHERE id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan must be in the currency of its occurrence');
END;

-- Plans are planning history: never deleted.
CREATE TRIGGER deferral_plans_kept
BEFORE DELETE ON deferral_plans
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan is never deleted; undo it instead');
END;

-- deferral_plan_adjustments are the fees, interest and other charges the user
-- records explicitly on a plan (decision D14): a plan never adds a fee by
-- itself. Each is a positive amount in the plan's currency. Removing one sets
-- removed_at once; rows are never deleted.
CREATE TABLE deferral_plan_adjustments (
    id          TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id     TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    kind        TEXT NOT NULL CHECK (kind IN ('fee', 'interest', 'other')),
    amount      TEXT NOT NULL CHECK (length(amount) BETWEEN 1 AND 62 AND length(replace(replace(amount, '-', ''), '.', '')) BETWEEN 1 AND 60 AND amount NOT GLOB '*[^0-9.-]*' AND amount NOT GLOB '0[0-9]*' AND amount NOT GLOB '-0[0-9]*' AND replace(replace(amount, '0', ''), '.', '') <> '-' AND amount NOT GLOB '?*-*' AND amount NOT GLOB '*.*.*' AND amount NOT GLOB '*.' AND amount NOT GLOB '.*' AND amount NOT GLOB '-.*' AND amount GLOB '*[0-9]*' AND amount NOT GLOB '-*' AND replace(replace(amount, '0', ''), '.', '') <> ''),
    currency    TEXT NOT NULL CHECK (length(currency) = 3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
    note        TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
    created_at  TEXT NOT NULL CHECK (length(created_at) = 30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    removed_at  TEXT CHECK (removed_at IS NULL OR (length(removed_at) = 30 AND removed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'))
) STRICT;

CREATE INDEX deferral_plan_adjustments_plan ON deferral_plan_adjustments (plan_id, created_at, id);

CREATE TRIGGER deferral_plan_adjustments_currency
BEFORE INSERT ON deferral_plan_adjustments
WHEN NEW.currency IS NOT (SELECT original_currency FROM deferral_plans WHERE id = NEW.plan_id)
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment must be in the currency of its plan');
END;

CREATE TRIGGER deferral_plan_adjustments_only_removed
BEFORE UPDATE ON deferral_plan_adjustments
WHEN OLD.removed_at IS NOT NULL OR NEW.removed_at IS NULL
  OR NEW.id IS NOT OLD.id OR NEW.plan_id IS NOT OLD.plan_id OR NEW.kind IS NOT OLD.kind OR NEW.amount IS NOT OLD.amount
  OR NEW.currency IS NOT OLD.currency OR NEW.note IS NOT OLD.note OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment can only be removed, once');
END;

CREATE TRIGGER deferral_plan_adjustments_kept
BEFORE DELETE ON deferral_plan_adjustments
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment is never deleted');
END;

-- deferral_plan_events is the append-only history of each plan: every change
-- with the plan's type and planned date after it, who made it, how (D42 3a)
-- and the reason recorded with it. plan_version is the plan's version after
-- the change, so the history reads in order. The audit log records the same
-- changes without reasons or amounts (ADR-0025).
CREATE TABLE deferral_plan_events (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id       TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_version  INTEGER NOT NULL CHECK (plan_version >= 1),
    action        TEXT NOT NULL CHECK (action IN ('created', 'revised', 'undone', 'adjustment_added', 'adjustment_removed')),
    plan_type     TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date  TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason        TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    adjustment_id TEXT REFERENCES deferral_plan_adjustments (id) ON DELETE RESTRICT CHECK (adjustment_id IS NULL OR (length(adjustment_id) = 26 AND adjustment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_via   TEXT CHECK (actor_via IS NULL OR actor_via IN ('session', 'token', 'mcp', 'system')),
    at            TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((adjustment_id IS NOT NULL) = (action IN ('adjustment_added', 'adjustment_removed'))),
    CHECK (actor_via IS NULL OR (actor_via = 'session' AND actor_kind = 'user') OR (actor_via IN ('token', 'mcp') AND actor_kind = 'token') OR (actor_via = 'system' AND actor_kind = 'system'))
) STRICT;

CREATE UNIQUE INDEX deferral_plan_events_order ON deferral_plan_events (plan_id, plan_version);

CREATE TRIGGER deferral_plan_events_append_only
BEFORE UPDATE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is append-only');
END;

CREATE TRIGGER deferral_plan_events_kept
BEFORE DELETE ON deferral_plan_events
BEGIN
    SELECT RAISE(ABORT, 'deferral plan history is never deleted');
END;
