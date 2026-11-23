DROP INDEX IF EXISTS specs_symbols_trgm;
DROP INDEX IF EXISTS contracts_name_trgm;
DROP INDEX IF EXISTS contracts_contract_id_trgm;

-- pg_trgm itself stays installed. It may have been there before the up
-- migration ran, other objects in the database may use it, and dropping it
-- needs the same privilege that creating it did, which a role allowed to
-- skip the up migration's CREATE EXTENSION may not have.
