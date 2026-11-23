-- Trigram indexes for the substring search behind ?q=. The search is
-- ILIKE '%q%' over contracts.contract_id, contracts.name and specs.symbols,
-- and a leading wildcard cannot use a B-tree, so without these every search
-- reads both tables in full. pg_trgm's GIN operator class can answer
-- ILIKE '%q%' from the index for any q of three or more characters.
--
-- The indexes are optional. CREATE EXTENSION needs a privilege some managed
-- PostgreSQL services do not grant to the application role, or the server may
-- not ship the contrib extensions at all. Failing here would leave the
-- database dirty and the registry down over what is only a speed-up, so this
-- migration logs a NOTICE and skips the indexes instead. Search returns the
-- same results either way, only slower. docs/operations.md has the statements
-- to run once the extension is available.
--
-- Since PostgreSQL 13 pg_trgm is a trusted extension: a role with CREATE on
-- the database can install it without being a superuser.
DO $$
DECLARE
    trgm_schema text;
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS pg_trgm;
    EXCEPTION
        WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
            RAISE NOTICE 'sorovault: pg_trgm is not available (%), so search will not use trigram indexes. See docs/operations.md.', SQLERRM;
    END;

    -- The extension may live outside search_path (some providers put it in an
    -- "extensions" schema), so name its operator class by schema.
    SELECT n.nspname INTO trgm_schema
    FROM pg_extension e
    JOIN pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname = 'pg_trgm';

    IF trgm_schema IS NULL THEN
        RETURN;
    END IF;

    EXECUTE format('CREATE INDEX IF NOT EXISTS contracts_contract_id_trgm ON contracts USING gin (contract_id %I.gin_trgm_ops)', trgm_schema);
    EXECUTE format('CREATE INDEX IF NOT EXISTS contracts_name_trgm ON contracts USING gin (name %I.gin_trgm_ops)', trgm_schema);
    EXECUTE format('CREATE INDEX IF NOT EXISTS specs_symbols_trgm ON specs USING gin (symbols %I.gin_trgm_ops)', trgm_schema);
END
$$;
