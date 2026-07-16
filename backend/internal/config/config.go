package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port                       string
	Host                       string
	StoreDriver                string
	DatabaseURL                string
	AGTDataDir                 string
	SQLitePath                 string
	DatabaseQueryURL           string
	DatabaseQueryDatasource    string
	DatabaseQueryTimeoutMs     int
	DatabaseQueryMaxRows       int
	DatabaseQueryAllowedTables []string
	GitHubToken                string
	GitHubAPIBaseURL           string
	GitHubAllowedRepos         []string
	GitHubTimeoutMs            int
	HTTPAllowedHosts           []string
	HTTPAllowedMethods         []string
	HTTPTimeoutMs              int
	HTTPMaxResponseBytes       int
	RateLimitPerMinute         int
	RateLimitEvictIntervalSec  int
	RateLimitIdleTimeoutSec    int
	PolicyConfigPath           string
	PolicyReloadIntervalMs     int
	OTelExporterOTLPEndpoint   string
	AuthMode                   string
	OIDCIssuerURL              string
	OIDCClientID               string
	OIDCWorkspaceClaim         string
	OIDCSubjectClaim           string
	OIDCEmailClaim             string
	OIDCNameClaim              string
	OIDCRoleClaim              string
	DefaultWorkspaceName       string
	DefaultWorkspaceSlug       string
	DefaultWorkspaceOrgID      string
	LocalSubject               string
	LocalEmail                 string
	LocalName                  string
	LocalRole                  string
	CORSAllowedOrigins         []string
	DevMode                    bool
}

func Load() Config {
	cfg := Config{
		Port:                       getEnv("PORT", "8080"),
		Host:                       getEnv("HOST", "127.0.0.1"),
		StoreDriver:                strings.ToLower(getEnv("STORE_DRIVER", "")),
		DatabaseURL:                getEnv("DATABASE_URL", ""),
		AGTDataDir:                 getEnv("AGT_DATA_DIR", ""),
		SQLitePath:                 firstNonEmpty(getEnv("AGT_SQLITE_PATH", ""), getEnv("SQLITE_PATH", "")),
		DatabaseQueryURL:           getEnv("DATABASE_QUERY_URL", ""),
		DatabaseQueryDatasource:    getEnv("DATABASE_QUERY_DATASOURCE", "local_postgres"),
		DatabaseQueryTimeoutMs:     parsePositiveInt(getEnv("DATABASE_QUERY_TIMEOUT_MS", "3000"), 3000),
		DatabaseQueryMaxRows:       parsePositiveInt(getEnv("DATABASE_QUERY_MAX_ROWS", "100"), 100),
		DatabaseQueryAllowedTables: splitAndTrim(getEnv("DATABASE_QUERY_ALLOWED_TABLES", "")),
		GitHubToken:                getEnv("GITHUB_TOKEN", ""),
		GitHubAPIBaseURL:           getEnv("GITHUB_API_BASE_URL", "https://api.github.com"),
		GitHubAllowedRepos:         splitAndTrim(getEnv("GITHUB_ALLOWED_REPOS", "")),
		GitHubTimeoutMs:            parsePositiveInt(getEnv("GITHUB_TIMEOUT_MS", "3000"), 3000),
		HTTPAllowedHosts:           splitAndTrim(getEnv("HTTP_ALLOWED_HOSTS", "")),
		HTTPAllowedMethods:         splitAndTrim(getEnv("HTTP_ALLOWED_METHODS", "GET,HEAD,OPTIONS,POST,PUT,PATCH,DELETE")),
		HTTPTimeoutMs:              parsePositiveInt(getEnv("HTTP_TIMEOUT_MS", "3000"), 3000),
		HTTPMaxResponseBytes:       parsePositiveInt(getEnv("HTTP_MAX_RESPONSE_BYTES", "65536"), 65536),
		RateLimitPerMinute:         parsePositiveInt(getEnv("RATE_LIMIT_PER_MINUTE", "60"), 60),
		RateLimitEvictIntervalSec:  parsePositiveInt(getEnv("RATE_LIMIT_EVICT_INTERVAL_SEC", "300"), 300),
		RateLimitIdleTimeoutSec:    parsePositiveInt(getEnv("RATE_LIMIT_IDLE_TIMEOUT_SEC", "600"), 600),
		PolicyConfigPath:           getEnv("POLICY_CONFIG_PATH", "configs/policies.yaml"),
		PolicyReloadIntervalMs:     parsePositiveInt(getEnv("POLICY_RELOAD_INTERVAL_MS", "5000"), 5000),
		OTelExporterOTLPEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		AuthMode:                   strings.ToLower(getEnv("AUTH_MODE", "")),
		OIDCIssuerURL:              getEnv("OIDC_ISSUER_URL", ""),
		OIDCClientID:               getEnv("OIDC_CLIENT_ID", ""),
		OIDCWorkspaceClaim:         getEnv("OIDC_WORKSPACE_CLAIM", "urn:zitadel:iam:user:resourceowner:id"),
		OIDCSubjectClaim:           getEnv("OIDC_SUBJECT_CLAIM", "sub"),
		OIDCEmailClaim:             getEnv("OIDC_EMAIL_CLAIM", "email"),
		OIDCNameClaim:              getEnv("OIDC_NAME_CLAIM", "name"),
		OIDCRoleClaim:              getEnv("OIDC_ROLE_CLAIM", "role"),
		DefaultWorkspaceName:       getEnv("DEFAULT_WORKSPACE_NAME", "Default Workspace"),
		DefaultWorkspaceSlug:       getEnv("DEFAULT_WORKSPACE_SLUG", "default"),
		DefaultWorkspaceOrgID:      getEnv("DEFAULT_WORKSPACE_ORG_ID", "local-org"),
		LocalSubject:               getEnv("LOCAL_SUBJECT", "local-dev"),
		LocalEmail:                 getEnv("LOCAL_EMAIL", "dev@agenttoolgate.local"),
		LocalName:                  getEnv("LOCAL_NAME", "Local Developer"),
		LocalRole:                  getEnv("LOCAL_ROLE", "owner"),
		CORSAllowedOrigins:         splitAndTrim(getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")),
		DevMode:                    parseBool(getEnv("DEV_MODE", "true")),
	}

	if cfg.StoreDriver == "" {
		if cfg.DatabaseURL != "" {
			cfg.StoreDriver = "postgres"
		} else {
			cfg.StoreDriver = "sqlite"
		}
	}
	if cfg.SQLitePath == "" {
		cfg.SQLitePath = defaultSQLitePath(cfg.AGTDataDir)
	}

	if cfg.AuthMode == "" {
		if cfg.OIDCIssuerURL != "" && cfg.OIDCClientID != "" {
			cfg.AuthMode = "oidc"
		} else {
			cfg.AuthMode = "local"
		}
	}
	if cfg.DatabaseQueryURL == "" {
		cfg.DatabaseQueryURL = cfg.DatabaseURL
	}
	if cfg.GitHubTimeoutMs > 30000 {
		cfg.GitHubTimeoutMs = 30000
	}
	if cfg.HTTPTimeoutMs > 30000 {
		cfg.HTTPTimeoutMs = 30000
	}
	if cfg.HTTPMaxResponseBytes > 1048576 {
		cfg.HTTPMaxResponseBytes = 1048576
	}

	return cfg
}

func defaultSQLitePath(dataDir string) string {
	base := strings.TrimSpace(dataDir)
	if base == "" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			base = filepath.Join(appData, "AgentToolGate")
		}
	}
	if base == "" {
		if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
			base = filepath.Join(configDir, "AgentToolGate")
		}
	}
	if base == "" {
		if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
			base = filepath.Join(homeDir, ".agenttoolgate")
		}
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "agenttoolgate.db")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

func parseBool(raw string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return value
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (c Config) UsesOIDC() bool {
	return c.AuthMode == "oidc"
}
