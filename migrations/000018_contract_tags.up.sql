-- Operator-assigned and auto-detected category tags for browsing by purpose.
ALTER TABLE contracts ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_contracts_tags ON contracts USING GIN (tags);
