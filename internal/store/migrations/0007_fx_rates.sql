-- Currency-rate cache (FX1, ADR-0022). One row per rate a provider returned:
-- the provider (adapter name), the source key the provider reports, the
-- pair, the exact rate as published, the date it applies to, and when Ledger
-- first and last retrieved it. Rates are provider facts: never computed,
-- inverted or edited, so the table has no version (store/versioned_test.go).
-- Rates of different sources (for Frankfurter, its blended rates and a
-- single provider's such as ECB) differ for the same pair and date, so the
-- source is part of the key. A re-retrieval of the same (provider, source,
-- pair, date) moves retrieved_at forward and keeps first_retrieved_at while
-- the value is numerically the same. confirmed_through is the latest date a
-- retrieval confirmed this rate applies to (the date asked for, or the
-- retrieval's UTC date for a latest rate): a rate is final for a date only
-- up to it, because retrieved_at alone does not say which dates a retrieval
-- covered. When the provider revises the value, the old one is kept in
-- previous_rate with revised_at, and first_retrieved_at restarts at the
-- revision, so provenance never claims an earlier receipt. The rate is TEXT
-- (ADR-0005), never REAL, and strictly positive.
CREATE TABLE fx_rates (
    provider           TEXT NOT NULL CHECK (provider <> '' AND provider NOT GLOB '*[^a-z0-9_]*'),
    source             TEXT NOT NULL DEFAULT '',
    base_currency      TEXT NOT NULL CHECK (length(base_currency) = 3 AND base_currency GLOB '[A-Z][A-Z][A-Z]'),
    quote_currency     TEXT NOT NULL CHECK (length(quote_currency) = 3 AND quote_currency GLOB '[A-Z][A-Z][A-Z]'),
    rate               TEXT NOT NULL CHECK (length(rate) BETWEEN 1 AND 62 AND length(replace(replace(rate, '-', ''), '.', '')) BETWEEN 1 AND 60 AND rate NOT GLOB '*[^0-9.-]*' AND rate NOT GLOB '0[0-9]*' AND rate NOT GLOB '-0[0-9]*' AND replace(replace(rate, '0', ''), '.', '') <> '-' AND rate NOT GLOB '?*-*' AND rate NOT GLOB '*.*.*' AND rate NOT GLOB '*.' AND rate NOT GLOB '.*' AND rate NOT GLOB '-.*' AND rate GLOB '*[0-9]*' AND rate NOT GLOB '-*' AND replace(replace(rate, '0', ''), '.', '') <> ''),
    rate_date          TEXT NOT NULL CHECK (length(rate_date) = 10 AND rate_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(rate_date) = rate_date),
    first_retrieved_at TEXT NOT NULL CHECK (length(first_retrieved_at) = 30 AND first_retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    retrieved_at       TEXT NOT NULL CHECK (length(retrieved_at) = 30 AND retrieved_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    confirmed_through  TEXT NOT NULL CHECK (length(confirmed_through) = 10 AND confirmed_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(confirmed_through) = confirmed_through),
    previous_rate      TEXT CHECK (previous_rate IS NULL OR (length(previous_rate) BETWEEN 1 AND 62 AND length(replace(replace(previous_rate, '-', ''), '.', '')) BETWEEN 1 AND 60 AND previous_rate NOT GLOB '*[^0-9.-]*' AND previous_rate NOT GLOB '0[0-9]*' AND previous_rate NOT GLOB '-0[0-9]*' AND replace(replace(previous_rate, '0', ''), '.', '') <> '-' AND previous_rate NOT GLOB '?*-*' AND previous_rate NOT GLOB '*.*.*' AND previous_rate NOT GLOB '*.' AND previous_rate NOT GLOB '.*' AND previous_rate NOT GLOB '-.*' AND previous_rate GLOB '*[0-9]*' AND previous_rate NOT GLOB '-*' AND replace(replace(previous_rate, '0', ''), '.', '') <> '')),
    revised_at         TEXT CHECK (revised_at IS NULL OR (length(revised_at) = 30 AND revised_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')),
    CHECK (base_currency <> quote_currency),
    CHECK (confirmed_through >= rate_date),
    CHECK ((previous_rate IS NULL) = (revised_at IS NULL)),
    CHECK (first_retrieved_at <= retrieved_at),
    PRIMARY KEY (provider, source, base_currency, quote_currency, rate_date)
) STRICT, WITHOUT ROWID;
