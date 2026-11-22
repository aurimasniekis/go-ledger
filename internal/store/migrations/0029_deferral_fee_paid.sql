-- Deferral plan fees with their own paid marker; a plan the person did not keep
-- is visible (DF1c, ADR-0050, operator decision D-DF 1 and 2).
--
-- A recorded fee or interest adjustment stays planned until it is itself marked
-- paid or removed: paying the principal never settles it, and it is never
-- payment evidence for the occurrence. paid_at is when the person marked it
-- paid, paid_actor_kind, paid_actor_user and paid_via who did and how (D42 3a).
-- Marking paid is reversible: clearing the marker (all four columns NULL
-- again) restores the fee as planned, and every change is one row of
-- deferral_plan_events and one audit event. An adjustment already removed
-- cannot change. Existing adjustments are unpaid.
--
-- A missed plan (planned date passed while the occurrence still owes something)
-- is derived when read and never stored: nothing here records it.
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_at TEXT CHECK (paid_at IS NULL OR (length(paid_at) = 30 AND paid_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_actor_kind TEXT CHECK (paid_actor_kind IS NULL OR paid_actor_kind IN ('user', 'token', 'system'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_actor_user TEXT CHECK (paid_actor_user IS NULL OR (length(paid_actor_user) = 26 AND paid_actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'));
ALTER TABLE deferral_plan_adjustments ADD COLUMN paid_via TEXT CHECK (paid_via IS NULL OR paid_via IN ('session', 'token', 'mcp', 'system'));

-- The marker is whole or absent, and its actor fits the channel as in the history.
CREATE TRIGGER deferral_plan_adjustments_paid_insert
BEFORE INSERT ON deferral_plan_adjustments
WHEN NEW.paid_at IS NOT NULL OR NEW.paid_actor_kind IS NOT NULL OR NEW.paid_actor_user IS NOT NULL OR NEW.paid_via IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment starts unpaid');
END;

DROP TRIGGER deferral_plan_adjustments_only_removed;

-- Only removal (once) and the paid marker (set or cleared, never on a removed
-- adjustment) can change an adjustment.
CREATE TRIGGER deferral_plan_adjustments_only_removed_or_paid
BEFORE UPDATE ON deferral_plan_adjustments
WHEN OLD.removed_at IS NOT NULL
  OR NEW.id IS NOT OLD.id OR NEW.plan_id IS NOT OLD.plan_id OR NEW.kind IS NOT OLD.kind OR NEW.amount IS NOT OLD.amount
  OR NEW.currency IS NOT OLD.currency OR NEW.note IS NOT OLD.note OR NEW.created_at IS NOT OLD.created_at
  OR (NEW.removed_at IS NOT OLD.removed_at AND (NEW.removed_at IS NULL OR NEW.paid_at IS NOT OLD.paid_at OR NEW.paid_actor_kind IS NOT OLD.paid_actor_kind
      OR NEW.paid_actor_user IS NOT OLD.paid_actor_user OR NEW.paid_via IS NOT OLD.paid_via))
  OR (NEW.paid_at IS NULL) IS NOT (NEW.paid_actor_kind IS NULL)
  OR (NEW.paid_at IS NULL AND (NEW.paid_actor_user IS NOT NULL OR NEW.paid_via IS NOT NULL))
  OR (NEW.paid_via IS NOT NULL AND NOT ((NEW.paid_via = 'session' AND NEW.paid_actor_kind = 'user') OR (NEW.paid_via IN ('token', 'mcp') AND NEW.paid_actor_kind = 'token') OR (NEW.paid_via = 'system' AND NEW.paid_actor_kind = 'system')))
BEGIN
    SELECT RAISE(ABORT, 'a deferral plan adjustment can only be removed once or marked paid or unpaid while it is not removed');
END;

-- The history gains adjustment_paid and adjustment_unpaid. The table is
-- rebuilt because SQLite cannot change a CHECK; it is a leaf (nothing refers to
-- it) and its rows are copied unchanged.
CREATE TABLE deferral_plan_events_new (
    id            TEXT PRIMARY KEY CHECK (length(id) = 26 AND id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_id       TEXT NOT NULL REFERENCES deferral_plans (id) ON DELETE RESTRICT CHECK (length(plan_id) = 26 AND plan_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]'),
    plan_version  INTEGER NOT NULL CHECK (plan_version >= 1),
    action        TEXT NOT NULL CHECK (action IN ('created', 'revised', 'undone', 'adjustment_added', 'adjustment_removed', 'adjustment_paid', 'adjustment_unpaid')),
    plan_type     TEXT NOT NULL CHECK (plan_type IN ('personal', 'creditor_agreed', 'documented_grace')),
    planned_date  TEXT NOT NULL CHECK (length(planned_date) = 10 AND planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(planned_date) = planned_date),
    reason        TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    adjustment_id TEXT REFERENCES deferral_plan_adjustments (id) ON DELETE RESTRICT CHECK (adjustment_id IS NULL OR (length(adjustment_id) = 26 AND adjustment_id GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    actor_user  TEXT CHECK (actor_user IS NULL OR (length(actor_user) = 26 AND actor_user GLOB '[0-7][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z][0-9A-HJKMNP-TV-Z]')),
    actor_via   TEXT CHECK (actor_via IS NULL OR actor_via IN ('session', 'token', 'mcp', 'system')),
    at            TEXT NOT NULL CHECK (length(at) = 30 AND at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((adjustment_id IS NOT NULL) = (action IN ('adjustment_added', 'adjustment_removed', 'adjustment_paid', 'adjustment_unpaid'))),
    CHECK (actor_via IS NULL OR (actor_via = 'session' AND actor_kind = 'user') OR (actor_via IN ('token', 'mcp') AND actor_kind = 'token') OR (actor_via = 'system' AND actor_kind = 'system'))
) STRICT;

INSERT INTO deferral_plan_events_new SELECT id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at FROM deferral_plan_events;

DROP TABLE deferral_plan_events;
ALTER TABLE deferral_plan_events_new RENAME TO deferral_plan_events;

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
