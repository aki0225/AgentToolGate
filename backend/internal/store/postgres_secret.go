package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
)

const secretColumnList = `
	id, workspace_id, workspace_org_id, name, description, enabled, secret_type, value_source,
	value_ref, metadata_json, created_at, updated_at
`

func (s *PostgresStore) ListSecrets(ctx context.Context, workspaceID string) ([]model.Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+secretColumnList+`
		FROM secrets
		WHERE workspace_id = $1
		ORDER BY created_at DESC, name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.Secret, 0)
	for rows.Next() {
		item, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetSecretByID(ctx context.Context, workspaceID, secretID string) (model.Secret, error) {
	return s.getSecret(ctx, `
		SELECT `+secretColumnList+`
		FROM secrets
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, secretID)
}

func (s *PostgresStore) GetSecretByName(ctx context.Context, workspaceID, name string) (model.Secret, error) {
	normalized, err := NormalizeSecretName(name)
	if err != nil {
		return model.Secret{}, ErrNotFound
	}
	return s.getSecret(ctx, `
		SELECT `+secretColumnList+`
		FROM secrets
		WHERE workspace_id = $1 AND name = $2
	`, workspaceID, normalized)
}

func (s *PostgresStore) CreateSecret(ctx context.Context, input model.CreateSecretInput) (model.Secret, error) {
	now := time.Now().UTC()
	secret := model.Secret{}
	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	secretType := NormalizeSecretType(input.SecretType)
	if err := ValidateSecretType(secretType); err != nil {
		return model.Secret{}, err
	}
	valueSource := NormalizeSecretValueSource(input.ValueSource)
	if err := ValidateSecretValueSource(valueSource); err != nil {
		return model.Secret{}, err
	}
	valueRef, err := NormalizeSecretValueRef(input.ValueRef)
	if err != nil {
		return model.Secret{}, err
	}
	metadata, err := NormalizeSecretMetadata(input.Metadata)
	if err != nil {
		return model.Secret{}, err
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO secrets (
			id, workspace_id, workspace_org_id, name, description, enabled, secret_type,
			value_source, value_ref, metadata_json, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)
		RETURNING `+secretColumnList+`
	`, ensureID("", "secret"), input.WorkspaceID, strings.TrimSpace(input.WorkspaceOrgID), name, strings.TrimSpace(input.Description), input.Enabled,
		secretType, valueSource, valueRef, mustJSON(metadata), now, now).Scan(
		&secret.ID, &secret.WorkspaceID, &secret.WorkspaceOrgID, &secret.Name, &secret.Description, &secret.Enabled,
		&secret.SecretType, &secret.ValueSource, &secret.ValueRef, &secret.Metadata, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if err != nil {
		return model.Secret{}, mapPgErr(err)
	}
	return secret, nil
}

func (s *PostgresStore) UpdateSecret(ctx context.Context, workspaceID, secretID string, input model.UpdateSecretInput) (model.Secret, error) {
	now := time.Now().UTC()
	secret := model.Secret{}
	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	secretType := NormalizeSecretType(input.SecretType)
	if err := ValidateSecretType(secretType); err != nil {
		return model.Secret{}, err
	}
	valueSource := NormalizeSecretValueSource(input.ValueSource)
	if err := ValidateSecretValueSource(valueSource); err != nil {
		return model.Secret{}, err
	}
	valueRef, err := NormalizeSecretValueRef(input.ValueRef)
	if err != nil {
		return model.Secret{}, err
	}
	metadata, err := NormalizeSecretMetadata(input.Metadata)
	if err != nil {
		return model.Secret{}, err
	}

	err = s.pool.QueryRow(ctx, `
		UPDATE secrets
		SET name = $3,
		    description = $4,
		    enabled = COALESCE($5::boolean, enabled),
		    secret_type = $6,
		    value_source = $7,
		    value_ref = $8,
		    metadata_json = $9::jsonb,
		    updated_at = $10
		WHERE workspace_id = $1 AND id = $2
		RETURNING `+secretColumnList+`
	`, workspaceID, secretID, name, strings.TrimSpace(input.Description), input.Enabled, secretType, valueSource, valueRef, mustJSON(metadata), now).Scan(
		&secret.ID, &secret.WorkspaceID, &secret.WorkspaceOrgID, &secret.Name, &secret.Description, &secret.Enabled,
		&secret.SecretType, &secret.ValueSource, &secret.ValueRef, &secret.Metadata, &secret.CreatedAt, &secret.UpdatedAt,
	)
	if err != nil {
		return model.Secret{}, mapPgErr(err)
	}
	return secret, nil
}

func (s *PostgresStore) DeleteSecret(ctx context.Context, workspaceID, secretID string) error {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM secrets
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, secretID)
	if err != nil {
		return mapPgErr(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) getSecret(ctx context.Context, query string, args ...any) (model.Secret, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	secret, err := scanSecret(row)
	if err != nil {
		return model.Secret{}, mapPgErr(err)
	}
	return secret, nil
}

func scanSecret(row interface{ Scan(...any) error }) (model.Secret, error) {
	var secret model.Secret
	var metadata []byte
	if err := row.Scan(&secret.ID, &secret.WorkspaceID, &secret.WorkspaceOrgID, &secret.Name, &secret.Description, &secret.Enabled,
		&secret.SecretType, &secret.ValueSource, &secret.ValueRef, &metadata, &secret.CreatedAt, &secret.UpdatedAt); err != nil {
		return model.Secret{}, err
	}
	if len(metadata) == 0 {
		secret.Metadata = json.RawMessage(`{}`)
	} else {
		secret.Metadata = json.RawMessage(metadata)
	}
	return secret, nil
}
