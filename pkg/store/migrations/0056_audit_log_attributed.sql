-- ADR-0108: the operator event list filters by the person an attributed
-- audit row names; only operator actions carry detail.attribution.
CREATE INDEX audit_log_attributed ON audit_log (id) WHERE detail ? 'attribution';
