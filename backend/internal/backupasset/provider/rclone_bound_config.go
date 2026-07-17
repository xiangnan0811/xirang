package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const rcloneBackendClassificationRevision = 1

type RcloneBackendClass string

const (
	RcloneBackendLiteralSelfContained RcloneBackendClass = "literal_self_contained"
	RcloneBackendClosureWrapper       RcloneBackendClass = "closure_wrapper"
	RcloneBackendUnsupported          RcloneBackendClass = "unsupported"
)

type RcloneBoundConfigReasonCode string

const (
	RcloneBoundConfigInvalidDocument          RcloneBoundConfigReasonCode = "invalid_document"
	RcloneBoundConfigDuplicateSection         RcloneBoundConfigReasonCode = "duplicate_section"
	RcloneBoundConfigDuplicateKey             RcloneBoundConfigReasonCode = "duplicate_key"
	RcloneBoundConfigUnusedSection            RcloneBoundConfigReasonCode = "unused_section"
	RcloneBoundConfigDependencyCycle          RcloneBoundConfigReasonCode = "dependency_cycle"
	RcloneBoundConfigDependencyMissing        RcloneBoundConfigReasonCode = "dependency_missing"
	RcloneBoundConfigDependencyAmbiguous      RcloneBoundConfigReasonCode = "dependency_ambiguous"
	RcloneBoundConfigDynamicCredentialSource  RcloneBoundConfigReasonCode = "dynamic_credential_source"
	RcloneBoundConfigUnknownOption            RcloneBoundConfigReasonCode = "unknown_option"
	RcloneBoundConfigUnknownBackend           RcloneBoundConfigReasonCode = "unknown_backend"
	RcloneBoundConfigUncertifiedWrapper       RcloneBoundConfigReasonCode = "uncertified_wrapper"
	RcloneBoundConfigIdentityRefreshUnbounded RcloneBoundConfigReasonCode = "identity_refresh_unbounded"
	RcloneBoundConfigNonRemoteBackend         RcloneBoundConfigReasonCode = "non_remote_backend"
	RcloneBoundConfigBackendNotCertified      RcloneBoundConfigReasonCode = "backend_not_certified"
	RcloneBoundConfigCredentialIncomplete     RcloneBoundConfigReasonCode = "credential_incomplete"
)

type RcloneBoundConfigError struct {
	Reason RcloneBoundConfigReasonCode
}

func (err *RcloneBoundConfigError) Error() string {
	if err == nil {
		return "invalid Rclone bound config"
	}
	return "invalid Rclone bound config: " + string(err.Reason)
}

type RcloneBackendClassification struct {
	Name              string
	Class             RcloneBackendClass
	UnsupportedReason RcloneBoundConfigReasonCode
}

func literalRcloneBackend(name string) RcloneBackendClassification {
	return RcloneBackendClassification{Name: name, Class: RcloneBackendLiteralSelfContained}
}

func closureRcloneBackend(name string) RcloneBackendClassification {
	return RcloneBackendClassification{Name: name, Class: RcloneBackendClosureWrapper}
}

func unsupportedRcloneBackend(name string, reason RcloneBoundConfigReasonCode) RcloneBackendClassification {
	return RcloneBackendClassification{Name: name, Class: RcloneBackendUnsupported, UnsupportedReason: reason}
}

var rcloneBackendClassificationsV1744 = []RcloneBackendClassification{
	unsupportedRcloneBackend("alias", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("hdfs", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("local", RcloneBoundConfigNonRemoteBackend),
	unsupportedRcloneBackend("storj", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("tardigrade", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("cloudinary", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("doi", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("fichier", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("filelu", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("filescom", RcloneBoundConfigBackendNotCertified),
	literalRcloneBackend("ftp"),
	unsupportedRcloneBackend("http", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("imagekit", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("internetarchive", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("koofr", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("linkbox", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("mega", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("opendrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("pixeldrain", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("protondrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("seafile", RcloneBoundConfigBackendNotCertified),
	literalRcloneBackend("sftp"),
	unsupportedRcloneBackend("sia", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("smb", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("ulozto", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("azurefiles", RcloneBoundConfigBackendNotCertified),
	closureRcloneBackend("crypt"),
	unsupportedRcloneBackend("filen", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("gofile", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("iclouddrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("memory", RcloneBoundConfigNonRemoteBackend),
	unsupportedRcloneBackend("netstorage", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("qingstor", RcloneBoundConfigBackendNotCertified),
	literalRcloneBackend("webdav"),
	unsupportedRcloneBackend("filefabric", RcloneBoundConfigIdentityRefreshUnbounded),
	literalRcloneBackend("azureblob"),
	unsupportedRcloneBackend("drime", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("quatrix", RcloneBoundConfigBackendNotCertified),
	unsupportedRcloneBackend("shade", RcloneBoundConfigBackendNotCertified),
	literalRcloneBackend("b2"),
	unsupportedRcloneBackend("cache", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("chunker", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("combine", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("hasher", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("oracleobjectstorage", RcloneBoundConfigBackendNotCertified),
	literalRcloneBackend("s3"),
	unsupportedRcloneBackend("sugarsync", RcloneBoundConfigIdentityRefreshUnbounded),
	literalRcloneBackend("swift"),
	unsupportedRcloneBackend("union", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("compress", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("dropbox", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("google photos", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("hidrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("huaweidrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("internxt", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("jottacloud", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("mailru", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("onedrive", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("pcloud", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("pikpak", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("premiumizeme", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("putio", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("sharefile", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("yandex", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("zoho", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("box", RcloneBoundConfigIdentityRefreshUnbounded),
	unsupportedRcloneBackend("archive", RcloneBoundConfigUncertifiedWrapper),
	unsupportedRcloneBackend("drive", RcloneBoundConfigIdentityRefreshUnbounded),
	literalRcloneBackend("google cloud storage"),
}

func RcloneBackendClassificationsV1744() []RcloneBackendClassification {
	return append([]RcloneBackendClassification(nil), rcloneBackendClassificationsV1744...)
}

type RcloneBoundConfig struct {
	exactBytes             []byte
	targetRemote           string
	backend                string
	dependencyRemotes      []string
	keyedDigest            string
	classificationRevision int
}

func (value RcloneBoundConfig) ExactBytes() []byte          { return append([]byte(nil), value.exactBytes...) }
func (value RcloneBoundConfig) TargetRemote() string        { return value.targetRemote }
func (value RcloneBoundConfig) Backend() string             { return value.backend }
func (value RcloneBoundConfig) KeyedDigest() string         { return value.keyedDigest }
func (value RcloneBoundConfig) ClassificationRevision() int { return value.classificationRevision }
func (value RcloneBoundConfig) DependencyRemotes() []string {
	return append([]string(nil), value.dependencyRemotes...)
}

type rcloneConfigSection struct {
	name    string
	options map[string]string
}

func ValidateRcloneBoundConfigV1744(payload []byte, targetRemote string, identityKey []byte, maxBytes int64) (RcloneBoundConfig, error) {
	if maxBytes <= 0 || int64(len(payload)) > maxBytes || len(payload) == 0 || len(identityKey) < 32 || !validRcloneRemoteName(targetRemote) {
		return RcloneBoundConfig{}, boundConfigError(RcloneBoundConfigInvalidDocument)
	}
	sections, err := parseStrictRcloneConfig(payload)
	if err != nil {
		return RcloneBoundConfig{}, err
	}
	classifications := make(map[string]RcloneBackendClassification, len(rcloneBackendClassificationsV1744))
	for _, classification := range rcloneBackendClassificationsV1744 {
		classifications[classification.Name] = classification
	}
	visited := make(map[string]bool, len(sections))
	visiting := make(map[string]bool, len(sections))
	dependencies := make([]string, 0, len(sections))
	var targetBackend string
	var visit func(string) error
	visit = func(remote string) error {
		if visiting[remote] {
			return boundConfigError(RcloneBoundConfigDependencyCycle)
		}
		if visited[remote] {
			return nil
		}
		section, ok := sections[remote]
		if !ok {
			return boundConfigError(RcloneBoundConfigDependencyMissing)
		}
		backend := section.options["type"]
		if backend == "" {
			return boundConfigError(RcloneBoundConfigInvalidDocument)
		}
		classification, ok := classifications[backend]
		if !ok {
			return boundConfigError(RcloneBoundConfigUnknownBackend)
		}
		if classification.Class == RcloneBackendUnsupported {
			return boundConfigError(classification.UnsupportedReason)
		}
		if err := validateRcloneSectionOptions(backend, section.options); err != nil {
			return err
		}
		if remote == targetRemote {
			targetBackend = backend
		}
		visiting[remote] = true
		dependencies = append(dependencies, remote)
		if classification.Class == RcloneBackendClosureWrapper {
			dependency, err := parseRcloneDependencyRemote(section.options["remote"])
			if err != nil {
				return err
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, remote)
		visited[remote] = true
		return nil
	}
	if err := visit(targetRemote); err != nil {
		return RcloneBoundConfig{}, err
	}
	if len(visited) != len(sections) {
		return RcloneBoundConfig{}, boundConfigError(RcloneBoundConfigUnusedSection)
	}

	mac := hmac.New(sha256.New, identityKey)
	_, _ = io.WriteString(mac, "xirang-rclone-bound-config-v1\n")
	_, _ = io.WriteString(mac, fmt.Sprintf("classification:%d\n", rcloneBackendClassificationRevision))
	_, _ = io.WriteString(mac, "target:"+targetRemote+"\n")
	_, _ = mac.Write(payload)
	return RcloneBoundConfig{
		exactBytes: append([]byte(nil), payload...), targetRemote: targetRemote, backend: targetBackend,
		dependencyRemotes: dependencies, keyedDigest: hex.EncodeToString(mac.Sum(nil)), classificationRevision: rcloneBackendClassificationRevision,
	}, nil
}

func parseStrictRcloneConfig(payload []byte) (map[string]rcloneConfigSection, error) {
	if !utf8.Valid(payload) || strings.ContainsRune(string(payload), '\x00') || strings.ContainsRune(string(payload), '\r') {
		return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
	}
	sections := make(map[string]rcloneConfigSection)
	var current *rcloneConfigSection
	lines := strings.Split(string(payload), "\n")
	for index, line := range lines {
		if len(line) > 32<<10 || strings.TrimRight(line, " \t") != line {
			return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if !validRcloneRemoteName(name) || line != "["+name+"]" {
				return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
			}
			if _, exists := sections[name]; exists {
				return nil, boundConfigError(RcloneBoundConfigDuplicateSection)
			}
			section := rcloneConfigSection{name: name, options: make(map[string]string)}
			sections[name] = section
			current = &section
			continue
		}
		if current == nil || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
		}
		separator := strings.Index(line, " = ")
		if separator <= 0 || strings.Contains(line[separator+3:], " = ") {
			return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
		}
		key := line[:separator]
		value := line[separator+3:]
		if !validRcloneConfigKey(key) || value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "${") || strings.Contains(value, "$(") {
			return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
		}
		section := sections[current.name]
		if _, exists := section.options[key]; exists {
			return nil, boundConfigError(RcloneBoundConfigDuplicateKey)
		}
		section.options[key] = value
		sections[current.name] = section
		current = &section
		_ = index
	}
	if len(sections) == 0 {
		return nil, boundConfigError(RcloneBoundConfigInvalidDocument)
	}
	return sections, nil
}

func validateRcloneSectionOptions(backend string, options map[string]string) error {
	allowed := rcloneCertifiedOptions[backend]
	if allowed == nil {
		return boundConfigError(RcloneBoundConfigBackendNotCertified)
	}
	for key := range options {
		if key == "type" {
			continue
		}
		if dynamicRcloneCredentialOption(key) {
			return boundConfigError(RcloneBoundConfigDynamicCredentialSource)
		}
		if !allowed[key] {
			return boundConfigError(RcloneBoundConfigUnknownOption)
		}
	}
	if !rcloneCredentialsComplete(backend, options) {
		return boundConfigError(RcloneBoundConfigCredentialIncomplete)
	}
	return nil
}

var rcloneCertifiedOptions = map[string]map[string]bool{
	"s3":                   optionSet("provider", "access_key_id", "secret_access_key", "session_token", "region", "endpoint", "location_constraint", "force_path_style", "server_side_encryption", "sse_kms_key_id", "storage_class", "encoding", "directory_markers"),
	"azureblob":            optionSet("account", "key", "sas_url", "connection_string", "tenant", "client_id", "client_secret", "endpoint", "access_tier", "encoding", "directory_markers"),
	"google cloud storage": optionSet("project_number", "user_project", "service_account_credentials", "access_token", "anonymous", "endpoint", "location", "storage_class", "encoding", "directory_markers"),
	"b2":                   optionSet("account", "key", "endpoint", "download_url", "encoding"),
	"sftp":                 optionSet("host", "user", "port", "pass", "key_pem", "key_file_pass", "pubkey", "use_insecure_cipher", "disable_hashcheck", "path_override", "set_modtime", "shell_type", "hashes", "skip_links", "subsystem", "server_command", "use_fstat", "disable_concurrent_reads", "disable_concurrent_writes", "idle_timeout", "chunk_size", "concurrency", "connections", "ciphers", "key_exchange", "macs", "host_key_algorithms", "copy_is_hardlink"),
	"webdav":               optionSet("url", "vendor", "user", "pass", "bearer_token", "encoding", "headers", "pacer_min_sleep", "nextcloud_chunk_size", "owncloud_exclude_shares", "owncloud_exclude_mounts", "auth_redirect"),
	"swift":                optionSet("user", "key", "auth", "user_id", "domain", "tenant", "tenant_id", "tenant_domain", "region", "storage_url", "auth_token", "application_credential_id", "application_credential_name", "application_credential_secret", "auth_version", "endpoint_type", "storage_policy", "encoding"),
	"ftp":                  optionSet("host", "user", "port", "pass", "tls", "explicit_tls", "concurrency", "no_check_certificate", "disable_epsv", "disable_mlsd", "disable_utf8", "writing_mdtm", "force_list_hidden", "idle_timeout", "close_timeout", "tls_cache_size", "disable_tls13", "allow_insecure_tls_ciphers", "shut_timeout", "no_check_upload", "encoding"),
	"crypt":                optionSet("remote", "filename_encryption", "directory_name_encryption", "password", "password2", "server_side_across_configs", "show_mapping", "no_data_encryption", "pass_bad_blocks", "strict_names", "filename_encoding", "suffix"),
}

func optionSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func dynamicRcloneCredentialOption(key string) bool {
	switch key {
	case "env_auth", "shared_credentials_file", "profile", "credential_process", "bearer_token_command",
		"key_file", "pubkey_file", "known_hosts_file", "key_use_agent", "ask_password", "service_account_file",
		"client_certificate_path", "service_principal_file", "use_msi", "use_az", "token_command", "config_refresh_token":
		return true
	default:
		return strings.HasSuffix(key, "_command") || strings.HasSuffix(key, "_file") || strings.HasSuffix(key, "_keyring")
	}
}

func rcloneCredentialsComplete(backend string, options map[string]string) bool {
	has := func(keys ...string) bool {
		for _, key := range keys {
			if options[key] == "" {
				return false
			}
		}
		return true
	}
	switch backend {
	case "s3":
		return has("access_key_id", "secret_access_key")
	case "azureblob":
		return has("account", "key") || has("sas_url") || has("connection_string") || has("tenant", "client_id", "client_secret")
	case "google cloud storage":
		return has("service_account_credentials") || has("access_token")
	case "b2":
		return has("account", "key")
	case "sftp":
		return has("host", "user") && (has("pass") || has("key_pem"))
	case "webdav":
		return has("url") && (has("user", "pass") || has("bearer_token"))
	case "swift":
		return has("user", "key", "auth") || has("storage_url", "auth_token") || has("application_credential_id", "application_credential_secret", "auth")
	case "ftp":
		return has("host", "user", "pass")
	case "crypt":
		return has("remote", "password")
	default:
		return false
	}
}

func parseRcloneDependencyRemote(value string) (string, error) {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 {
		return "", boundConfigError(RcloneBoundConfigDependencyAmbiguous)
	}
	remote := value[:separator]
	if !validRcloneRemoteName(remote) {
		return "", boundConfigError(RcloneBoundConfigDependencyAmbiguous)
	}
	return remote, nil
}

func validRcloneRemoteName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validRcloneConfigKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func boundConfigError(reason RcloneBoundConfigReasonCode) error {
	return &RcloneBoundConfigError{Reason: reason}
}
