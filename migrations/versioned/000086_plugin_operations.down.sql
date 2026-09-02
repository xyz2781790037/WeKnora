ALTER TABLE plugins DROP COLUMN IF EXISTS update_message;
ALTER TABLE plugins DROP COLUMN IF EXISTS update_checked_at;
ALTER TABLE plugins DROP COLUMN IF EXISTS update_available;
ALTER TABLE plugins DROP COLUMN IF EXISTS latest_version;
ALTER TABLE plugins DROP COLUMN IF EXISTS source_manifest_url;
ALTER TABLE plugins DROP COLUMN IF EXISTS last_recovery_at;
ALTER TABLE plugins DROP COLUMN IF EXISTS recovery_attempts;
ALTER TABLE plugins DROP COLUMN IF EXISTS consecutive_health_failures;
