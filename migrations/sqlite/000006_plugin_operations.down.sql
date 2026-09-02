ALTER TABLE plugins DROP COLUMN update_message;
ALTER TABLE plugins DROP COLUMN update_checked_at;
ALTER TABLE plugins DROP COLUMN update_available;
ALTER TABLE plugins DROP COLUMN latest_version;
ALTER TABLE plugins DROP COLUMN source_manifest_url;
ALTER TABLE plugins DROP COLUMN last_recovery_at;
ALTER TABLE plugins DROP COLUMN recovery_attempts;
ALTER TABLE plugins DROP COLUMN consecutive_health_failures;
