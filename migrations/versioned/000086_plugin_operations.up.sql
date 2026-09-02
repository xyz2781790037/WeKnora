-- Persist plugin update discovery and automatic health recovery state for
-- installations created before migration 000085 was expanded.

ALTER TABLE plugins ADD COLUMN IF NOT EXISTS consecutive_health_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS recovery_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS last_recovery_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS source_manifest_url TEXT NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS latest_version VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS update_available BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS update_checked_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS update_message TEXT NOT NULL DEFAULT '';
