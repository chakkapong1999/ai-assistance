DROP TRIGGER IF EXISTS audit_log_no_change ON audit_log;
DROP FUNCTION IF EXISTS audit_log_append_only();
DROP TABLE IF EXISTS audit_log;
