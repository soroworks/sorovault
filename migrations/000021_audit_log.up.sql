-- Audit trail of registry write actions (registrations, refreshes, source edits).
CREATE TABLE audit_log (
    id          BIGSERIAL   PRIMARY KEY,
    action      TEXT        NOT NULL,
    network     TEXT        NOT NULL,
    contract_id TEXT        NOT NULL,
    client_ip   TEXT        NOT NULL DEFAULT '',
    token_name  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_created_at ON audit_log (created_at DESC, id DESC);
CREATE INDEX idx_audit_log_contract ON audit_log (network, contract_id, created_at DESC);
