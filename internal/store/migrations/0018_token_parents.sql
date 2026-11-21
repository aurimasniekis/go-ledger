-- Token lineage (A4e, ADR-0029): the root token that created a token.
--
-- A token created through the API by a root token of the root account
-- (decision D25-1) names its creator here. Revoking a token revokes every
-- token below it in the same transaction, and a child's expiry never
-- outlasts its parent's (both are enforced by the tokens store and service).
-- Tokens created by a signed-in user or by the host have no parent, and so
-- do all tokens that existed before this migration: their creator was only
-- recorded in the audit log, so no link can be reconstructed.
ALTER TABLE api_tokens ADD COLUMN parent_token_id TEXT REFERENCES api_tokens (id);

CREATE INDEX api_tokens_parent ON api_tokens (parent_token_id) WHERE parent_token_id IS NOT NULL;
