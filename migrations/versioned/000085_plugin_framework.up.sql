-- Migration 000085: system-wide external plugin installations.
-- Tenant configuration remains in the owning feature tables; this table only
-- stores executable identity, manifest, lifecycle and health state.

CREATE TABLE IF NOT EXISTS plugins (
    id VARCHAR(191) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version VARCHAR(64) NOT NULL,
    protocol_version VARCHAR(64) NOT NULL,
    we_knora_version_constraint VARCHAR(128) NOT NULL DEFAULT '',
    image TEXT NOT NULL,
    image_digest VARCHAR(255) NOT NULL DEFAULT '',
    origin VARCHAR(16) NOT NULL DEFAULT 'external',
    types JSONB NOT NULL DEFAULT '[]',
    capabilities JSONB NOT NULL DEFAULT '[]',
    connector_type VARCHAR(64) NOT NULL DEFAULT '',
    manifest JSONB NOT NULL DEFAULT '{}',
    status VARCHAR(16) NOT NULL DEFAULT 'disabled',
    runtime_state VARCHAR(16) NOT NULL DEFAULT 'unknown',
    health_message TEXT NOT NULL DEFAULT '',
    last_health_at TIMESTAMP WITH TIME ZONE,
    call_timeout_seconds INTEGER NOT NULL DEFAULT 120,
    installed_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_plugins_origin CHECK (origin IN ('builtin', 'external')),
    CONSTRAINT chk_plugins_status CHECK (status IN ('disabled', 'enabled', 'error')),
    CONSTRAINT chk_plugins_timeout CHECK (call_timeout_seconds > 0)
);

CREATE INDEX IF NOT EXISTS idx_plugins_origin ON plugins (origin);
CREATE INDEX IF NOT EXISTS idx_plugins_status ON plugins (status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plugins_connector_type
    ON plugins (connector_type) WHERE connector_type <> '';
