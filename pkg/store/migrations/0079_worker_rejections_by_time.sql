CREATE INDEX audit_log_worker_rejected ON audit_log (at) WHERE action = 'worker.request_rejected';
