package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
)

func (s *MemoryStore) ListSecrets(_ context.Context, workspaceID string) ([]model.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := append([]string(nil), s.secretsByWorkspace[workspaceID]...)
	items := make([]model.Secret, 0, len(ids))
	for _, id := range ids {
		secret, ok := s.secrets[id]
		if !ok || secret.WorkspaceID != workspaceID {
			continue
		}
		items = append(items, cloneSecret(secret))
	}
	sortSecrets(items)
	return items, nil
}

func (s *MemoryStore) GetSecretByID(_ context.Context, workspaceID, secretID string) (model.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	secret, ok := s.secrets[secretID]
	if !ok || secret.WorkspaceID != workspaceID {
		return model.Secret{}, ErrNotFound
	}
	return cloneSecret(secret), nil
}

func (s *MemoryStore) GetSecretByName(_ context.Context, workspaceID, name string) (model.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if id, ok := s.secretsByWorkspaceName[secretIndexKey(workspaceID, name)]; ok {
		secret, exists := s.secrets[id]
		if exists && secret.WorkspaceID == workspaceID {
			return cloneSecret(secret), nil
		}
	}
	return model.Secret{}, ErrNotFound
}

func (s *MemoryStore) CreateSecret(_ context.Context, input model.CreateSecretInput) (model.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	if _, ok := s.secretsByWorkspaceName[secretIndexKey(input.WorkspaceID, name)]; ok {
		return model.Secret{}, ErrConflict
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
	now := time.Now().UTC()

	secret := model.Secret{
		ID:            uuid.NewString(),
		WorkspaceID:   input.WorkspaceID,
		WorkspaceOrgID: strings.TrimSpace(input.WorkspaceOrgID),
		Name:          name,
		Description:   strings.TrimSpace(input.Description),
		Enabled:       input.Enabled,
		SecretType:    secretType,
		ValueSource:   valueSource,
		ValueRef:      valueRef,
		Metadata:      CloneSecretJSON(metadata),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.putSecretLocked(secret)
	return cloneSecret(secret), nil
}

func (s *MemoryStore) UpdateSecret(_ context.Context, workspaceID, secretID string, input model.UpdateSecretInput) (model.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	secret, ok := s.secrets[secretID]
	if !ok || secret.WorkspaceID != workspaceID {
		return model.Secret{}, ErrNotFound
	}

	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	if existingID, ok := s.secretsByWorkspaceName[secretIndexKey(workspaceID, name)]; ok && existingID != secretID {
		return model.Secret{}, ErrConflict
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
	now := time.Now().UTC()

	delete(s.secretsByWorkspaceName, secretIndexKey(workspaceID, secret.Name))
	secret.Name = name
	secret.Description = strings.TrimSpace(input.Description)
	if input.Enabled != nil {
		secret.Enabled = *input.Enabled
	}
	secret.SecretType = secretType
	secret.ValueSource = valueSource
	secret.ValueRef = valueRef
	secret.Metadata = CloneSecretJSON(metadata)
	secret.UpdatedAt = now
	s.secrets[secret.ID] = secret
	s.secretsByWorkspaceName[secretIndexKey(workspaceID, secret.Name)] = secret.ID
	return cloneSecret(secret), nil
}

func (s *MemoryStore) DeleteSecret(_ context.Context, workspaceID, secretID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	secret, ok := s.secrets[secretID]
	if !ok || secret.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	delete(s.secrets, secretID)
	delete(s.secretsByWorkspaceName, secretIndexKey(workspaceID, secret.Name))
	ids := s.secretsByWorkspace[workspaceID]
	filtered := ids[:0]
	for _, id := range ids {
		if id != secretID {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) == 0 {
		delete(s.secretsByWorkspace, workspaceID)
	} else {
		s.secretsByWorkspace[workspaceID] = append([]string(nil), filtered...)
	}
	return nil
}

func (s *MemoryStore) putSecretLocked(secret model.Secret) {
	s.secrets[secret.ID] = secret
	s.secretsByWorkspace[secret.WorkspaceID] = append(s.secretsByWorkspace[secret.WorkspaceID], secret.ID)
	s.secretsByWorkspaceName[secretIndexKey(secret.WorkspaceID, secret.Name)] = secret.ID
}

func cloneSecret(secret model.Secret) model.Secret {
	secret.Metadata = CloneSecretJSON(secret.Metadata)
	secret.Bindings = CloneSecretBindings(secret.Bindings)
	return secret
}

func sortSecrets(items []model.Secret) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
}

func secretIndexKey(workspaceID, name string) string {
	return workspaceID + "::" + strings.ToLower(strings.TrimSpace(name))
}
