package profile

import (
	"encoding/json"
	"errors"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

const (
	appProfileAccessKind   = "app_profile_access"
	appProfileAccessSource = "profile_settings"
)

var ErrInvalidAppProfileAccess = errors.New("invalid app profile access settings")

type AppProfileAccess struct {
	config   map[string]interface{}
	Provider string
	Kind     string
	Source   string
}

func ResolveAppProfileAccess(db *gorm.DB, appCredentialID uint) (AppProfileAccess, error) {
	var credential model.AppCredential
	if err := db.First(&credential, appCredentialID).Error; err != nil {
		return AppProfileAccess{}, err
	}
	return ResolveAppProfileAccessFromRaw(credential.Config)
}

func ResolveAppProfileAccessFromRaw(raw string) (AppProfileAccess, error) {
	cfg, err := parseAppProfileConfig(raw)
	if err != nil {
		return AppProfileAccess{}, ErrInvalidAppProfileAccess
	}
	return NewAppProfileAccess(cfg), nil
}

func ResolveAppProfileAccessFromRawOrEmpty(raw string) AppProfileAccess {
	access, err := ResolveAppProfileAccessFromRaw(raw)
	if err != nil {
		return NewAppProfileAccess(map[string]interface{}{})
	}
	return access
}

func NewAppProfileAccess(config map[string]interface{}) AppProfileAccess {
	if config == nil {
		config = map[string]interface{}{}
	}
	return AppProfileAccess{
		config:   copyAppProfileConfig(config),
		Provider: sshutil.CredentialProviderLocal,
		Kind:     appProfileAccessKind,
		Source:   appProfileAccessSource,
	}
}

func (access AppProfileAccess) Config() map[string]interface{} {
	return copyAppProfileConfig(access.config)
}

func (access AppProfileAccess) Value(key string) (interface{}, bool) {
	value, ok := access.config[key]
	return value, ok
}

func (access AppProfileAccess) HasPassword() bool {
	value, ok := access.config["password"]
	if !ok || value == nil {
		return false
	}
	if text, ok := value.(string); ok {
		return text != ""
	}
	return true
}

func (access AppProfileAccess) SafeMetadata() map[string]string {
	return map[string]string{
		"provider": access.Provider,
		"kind":     access.Kind,
		"source":   access.Source,
	}
}

func parseAppProfileConfig(raw string) (map[string]interface{}, error) {
	if raw == "" {
		return map[string]interface{}{}, nil
	}
	cfg := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func copyAppProfileConfig(config map[string]interface{}) map[string]interface{} {
	copied := make(map[string]interface{}, len(config))
	for key, value := range config {
		copied[key] = value
	}
	return copied
}
