-- Searchable symbol names for each stored interface: every function,
-- user-defined type and event name, newline-separated and sorted. Search
-- matches against this column rather than walking the jsonb on every query.
ALTER TABLE specs ADD COLUMN symbols TEXT NOT NULL DEFAULT '';

-- Backfill rows written before this column existed. Names are de-duplicated
-- and byte-ordered to match what the Go code computes on Save.
UPDATE specs SET symbols = COALESCE((
    SELECT string_agg(name, E'\n' ORDER BY name COLLATE "C")
    FROM (
        SELECT DISTINCT elem->>'name' AS name
        FROM (
            SELECT jsonb_array_elements(COALESCE(spec->'functions', '[]'::jsonb))              AS elem
            UNION ALL SELECT jsonb_array_elements(COALESCE(spec->'types'->'structs', '[]'::jsonb))
            UNION ALL SELECT jsonb_array_elements(COALESCE(spec->'types'->'unions', '[]'::jsonb))
            UNION ALL SELECT jsonb_array_elements(COALESCE(spec->'types'->'enums', '[]'::jsonb))
            UNION ALL SELECT jsonb_array_elements(COALESCE(spec->'types'->'error_enums', '[]'::jsonb))
            UNION ALL SELECT jsonb_array_elements(COALESCE(spec->'events', '[]'::jsonb))
        ) AS elems
        WHERE COALESCE(elem->>'name', '') <> ''
    ) AS names
), '');
