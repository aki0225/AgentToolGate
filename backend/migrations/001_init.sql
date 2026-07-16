CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	zitadel_organization_id TEXT NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	zitadel_user_id TEXT NOT NULL,
	email TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT 'owner',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	UNIQUE (workspace_id, zitadel_user_id)
);

CREATE TABLE IF NOT EXISTS policy_rules (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	priority INTEGER NOT NULL DEFAULT 100,
	effect TEXT NOT NULL,
	connector_type TEXT NOT NULL DEFAULT '*',
	tool_name_pattern TEXT NOT NULL DEFAULT '*',
	operation_type TEXT NOT NULL DEFAULT '*',
	risk_level TEXT NOT NULL DEFAULT '*',
	resource_pattern TEXT NOT NULL DEFAULT '*',
	reason TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS policy_rules_workspace_order_idx
	ON policy_rules (workspace_id, priority, created_at, id);

CREATE TABLE IF NOT EXISTS approval_requests (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	tool_key TEXT NOT NULL,
	tool_display_name TEXT NOT NULL,
	status TEXT NOT NULL,
	requested_by TEXT NOT NULL DEFAULT '',
	reviewed_by TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	fingerprint TEXT NOT NULL DEFAULT '',
	adapter TEXT NOT NULL DEFAULT '',
	action_type TEXT NOT NULL DEFAULT '',
	target TEXT NOT NULL DEFAULT '',
	canonical_target TEXT NOT NULL DEFAULT '',
	content_encoding TEXT NOT NULL DEFAULT '',
	content_hash TEXT NOT NULL DEFAULT '',
	script_hash TEXT NOT NULL DEFAULT '',
	resolved_file_identity TEXT NOT NULL DEFAULT '',
	parent_identity TEXT NOT NULL DEFAULT '',
	decision_payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);

DROP INDEX IF EXISTS approval_requests_workspace_fingerprint_idx;

CREATE UNIQUE INDEX IF NOT EXISTS approval_requests_workspace_active_fingerprint_idx
	ON approval_requests (workspace_id, fingerprint)
	WHERE fingerprint <> '' AND status IN ('pending', 'approved');

CREATE TABLE IF NOT EXISTS connectors (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	type TEXT NOT NULL,
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	UNIQUE (workspace_id, type, name)
);

CREATE TABLE IF NOT EXISTS secrets (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	workspace_org_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	secret_type TEXT NOT NULL,
	value_source TEXT NOT NULL DEFAULT 'env',
	value_ref TEXT NOT NULL DEFAULT '',
	metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS tools (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	namespace TEXT NOT NULL,
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	operation_type TEXT NOT NULL,
	risk_level TEXT NOT NULL,
	requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
	input_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	output_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	UNIQUE (workspace_id, namespace, name)
);

CREATE TABLE IF NOT EXISTS tool_calls (
	id TEXT PRIMARY KEY,
	request_id TEXT NOT NULL UNIQUE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	actor_id TEXT NOT NULL DEFAULT '',
	actor_subject TEXT NOT NULL DEFAULT '',
	actor_email TEXT NOT NULL DEFAULT '',
	actor_name TEXT NOT NULL DEFAULT '',
	tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE RESTRICT,
	tool_key TEXT NOT NULL,
	status TEXT NOT NULL,
	risk_level TEXT NOT NULL,
	policy_decision TEXT NOT NULL DEFAULT 'allow',
	approval_id TEXT NOT NULL DEFAULT '',
	duration_ms BIGINT NOT NULL DEFAULT 0,
	input_redacted_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	input_execution_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	output_redacted_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	explanation_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	error_message TEXT NOT NULL DEFAULT '',
	trace_id TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
