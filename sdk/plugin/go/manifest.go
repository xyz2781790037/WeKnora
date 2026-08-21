// Package plugin provides the public Go SDK for WeKnora out-of-process plugins.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/blang/semver/v4"
	"google.golang.org/protobuf/types/known/durationpb"
	"gopkg.in/yaml.v3"
)

const (
	// ManifestAPIVersion is the only manifest schema supported by this SDK.
	ManifestAPIVersion = "weknora.io/v1"
	// ProtocolVersion is the protocol implemented by this SDK.
	ProtocolVersion = "1.0.0"
	// ManifestKind distinguishes plugin manifests from other YAML documents.
	ManifestKind = "Plugin"
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{1,126}[a-z0-9])$`)

// Manifest is the portable plugin.yaml representation. It intentionally keeps
// runtime concerns out of the type so the same file can be validated by the
// plugin process, WeKnora and plugin-runtime.
type Manifest struct {
	APIVersion string           `yaml:"apiVersion" json:"api_version"`
	Kind       string           `yaml:"kind" json:"kind"`
	Metadata   ManifestMetadata `yaml:"metadata" json:"metadata"`
	Spec       ManifestSpec     `yaml:"spec" json:"spec"`
}

// ManifestMetadata contains stable plugin identity and display fields.
type ManifestMetadata struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Version     string `yaml:"version" json:"version"`
}

// ManifestSpec declares compatibility, capabilities, configuration and
// permissions. Types may contain more than one capability family.
type ManifestSpec struct {
	ProtocolVersion          string              `yaml:"protocolVersion" json:"protocol_version"`
	WeKnoraVersionConstraint string              `yaml:"weknoraVersion" json:"weknora_version"`
	Image                    string              `yaml:"image" json:"image"`
	Types                    []string            `yaml:"types" json:"types"`
	Capabilities             []string            `yaml:"capabilities" json:"capabilities"`
	ConnectorType            string              `yaml:"connectorType" json:"connector_type"`
	Icon                     string              `yaml:"icon" json:"icon"`
	Config                   ManifestConfig      `yaml:"config" json:"config"`
	Permissions              ManifestPermissions `yaml:"permissions" json:"permissions"`
	DefaultTimeout           time.Duration       `yaml:"-" json:"-"`
	DefaultTimeoutText       string              `yaml:"defaultTimeout" json:"default_timeout"`
}

// ManifestConfig embeds JSON Schema as a YAML object and separately identifies
// secret paths so hosts can redact and encrypt them.
type ManifestConfig struct {
	Schema       map[string]any `yaml:"schema" json:"schema"`
	SecretFields []string       `yaml:"secretFields" json:"secret_fields"`
}

// ManifestPermissions are declarative. plugin-runtime is responsible for
// enforcing them; the SDK only validates their shape.
type ManifestPermissions struct {
	Network      bool     `yaml:"network" json:"network"`
	AllowedHosts []string `yaml:"allowedHosts" json:"allowed_hosts"`
	DataAccess   []string `yaml:"dataAccess" json:"data_access"`
}

// ParseManifest decodes and validates one plugin.yaml document.
func ParseManifest(data []byte) (*Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode plugin manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("plugin manifest must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing plugin manifest document: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks invariants required before a plugin image can be started.
func (m *Manifest) Validate() error {
	if m == nil {
		return errors.New("plugin manifest is nil")
	}
	if m.APIVersion != ManifestAPIVersion {
		return fmt.Errorf("unsupported manifest apiVersion %q", m.APIVersion)
	}
	if m.Kind != ManifestKind {
		return fmt.Errorf("unsupported manifest kind %q", m.Kind)
	}
	if !pluginIDPattern.MatchString(m.Metadata.ID) {
		return fmt.Errorf("invalid plugin id %q: use lowercase reverse-DNS characters", m.Metadata.ID)
	}
	if strings.TrimSpace(m.Metadata.Name) == "" {
		return errors.New("plugin name is required")
	}
	if _, err := semver.Parse(strings.TrimPrefix(m.Metadata.Version, "v")); err != nil {
		return fmt.Errorf("invalid plugin version %q: %w", m.Metadata.Version, err)
	}
	if m.Spec.ProtocolVersion == "" {
		return errors.New("protocolVersion is required")
	}
	if _, err := semver.Parse(strings.TrimPrefix(m.Spec.ProtocolVersion, "v")); err != nil {
		return fmt.Errorf("invalid protocolVersion %q: %w", m.Spec.ProtocolVersion, err)
	}
	if strings.TrimSpace(m.Spec.Image) == "" {
		return errors.New("plugin image is required")
	}
	if len(m.Spec.Types) == 0 {
		return errors.New("at least one plugin type is required")
	}

	typeSet := make(map[string]struct{}, len(m.Spec.Types))
	for _, rawType := range m.Spec.Types {
		pluginType := normalizePluginType(rawType)
		if _, ok := pluginTypeToProto[pluginType]; !ok {
			return fmt.Errorf("unsupported plugin type %q", rawType)
		}
		typeSet[pluginType] = struct{}{}
	}
	if _, ok := typeSet["data_source"]; ok && strings.TrimSpace(m.Spec.ConnectorType) == "" {
		return errors.New("connectorType is required for a data_source plugin")
	}

	if len(m.Spec.Config.Schema) == 0 {
		return errors.New("config.schema is required")
	}
	if _, err := json.Marshal(m.Spec.Config.Schema); err != nil {
		return fmt.Errorf("config.schema is not valid JSON data: %w", err)
	}
	properties, _ := m.Spec.Config.Schema["properties"].(map[string]any)
	secretFields := make(map[string]struct{}, len(m.Spec.Config.SecretFields))
	for _, rawField := range m.Spec.Config.SecretFields {
		field := strings.TrimSpace(rawField)
		if field == "" {
			return errors.New("config.secretFields cannot contain an empty field")
		}
		if _, duplicate := secretFields[field]; duplicate {
			return fmt.Errorf("config.secretFields contains duplicate field %q", field)
		}
		if _, exists := properties[field]; !exists {
			return fmt.Errorf("config.secretFields field %q is not defined in config.schema.properties", field)
		}
		secretFields[field] = struct{}{}
	}

	if !m.Spec.Permissions.Network && len(m.Spec.Permissions.AllowedHosts) > 0 {
		return errors.New("allowedHosts must be empty when network permission is false")
	}
	for _, host := range m.Spec.Permissions.AllowedHosts {
		if err := validateAllowedHost(host); err != nil {
			return err
		}
	}
	for _, access := range m.Spec.Permissions.DataAccess {
		if _, ok := dataAccessToProto[normalizeDataAccess(access)]; !ok {
			return fmt.Errorf("unsupported dataAccess value %q", access)
		}
	}

	if m.Spec.DefaultTimeoutText == "" {
		m.Spec.DefaultTimeout = 2 * time.Minute
	} else {
		d, err := time.ParseDuration(m.Spec.DefaultTimeoutText)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid defaultTimeout %q", m.Spec.DefaultTimeoutText)
		}
		m.Spec.DefaultTimeout = d
	}
	return nil
}

// ToProto converts the YAML representation to its transport form.
func (m *Manifest) ToProto() (*pluginv1.PluginManifest, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	schema, err := json.Marshal(m.Spec.Config.Schema)
	if err != nil {
		return nil, fmt.Errorf("encode config schema: %w", err)
	}

	types := make([]pluginv1.PluginType, 0, len(m.Spec.Types))
	for _, value := range m.Spec.Types {
		types = append(types, pluginTypeToProto[normalizePluginType(value)])
	}
	access := make([]pluginv1.DataAccess, 0, len(m.Spec.Permissions.DataAccess))
	for _, value := range m.Spec.Permissions.DataAccess {
		access = append(access, dataAccessToProto[normalizeDataAccess(value)])
	}

	return &pluginv1.PluginManifest{
		Id:                       m.Metadata.ID,
		Name:                     m.Metadata.Name,
		Description:              m.Metadata.Description,
		Version:                  strings.TrimPrefix(m.Metadata.Version, "v"),
		ProtocolVersion:          strings.TrimPrefix(m.Spec.ProtocolVersion, "v"),
		WeknoraVersionConstraint: m.Spec.WeKnoraVersionConstraint,
		Image:                    m.Spec.Image,
		Types:                    types,
		Capabilities:             append([]string(nil), m.Spec.Capabilities...),
		Config: &pluginv1.ConfigSchema{
			JsonSchema:   string(schema),
			SecretFields: append([]string(nil), m.Spec.Config.SecretFields...),
		},
		Permissions: &pluginv1.PluginPermissions{
			Network:      m.Spec.Permissions.Network,
			AllowedHosts: append([]string(nil), m.Spec.Permissions.AllowedHosts...),
			DataAccess:   access,
		},
		DefaultTimeout: durationpb.New(m.Spec.DefaultTimeout),
		ConnectorType:  m.Spec.ConnectorType,
		Icon:           m.Spec.Icon,
	}, nil
}

// NegotiateProtocol returns the highest mutually supported protocol version.
// Minor and patch releases are backward compatible within one major version.
func NegotiateProtocol(hostVersion, pluginVersion string) (string, error) {
	host, err := semver.Parse(strings.TrimPrefix(hostVersion, "v"))
	if err != nil {
		return "", fmt.Errorf("invalid host protocol version %q: %w", hostVersion, err)
	}
	remote, err := semver.Parse(strings.TrimPrefix(pluginVersion, "v"))
	if err != nil {
		return "", fmt.Errorf("invalid plugin protocol version %q: %w", pluginVersion, err)
	}
	if host.Major != remote.Major {
		return "", fmt.Errorf("incompatible protocol major versions: host=%s plugin=%s", host, remote)
	}
	if remote.LT(host) {
		return remote.String(), nil
	}
	return host.String(), nil
}

// NormalizedTypes returns stable, deduplicated plugin type names.
func (m *Manifest) NormalizedTypes() []string {
	seen := make(map[string]struct{}, len(m.Spec.Types))
	for _, value := range m.Spec.Types {
		seen[normalizePluginType(value)] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

var pluginTypeToProto = map[string]pluginv1.PluginType{
	"data_source":     pluginv1.PluginType_PLUGIN_TYPE_DATA_SOURCE,
	"document_parser": pluginv1.PluginType_PLUGIN_TYPE_DOCUMENT_PARSER,
	"web_search":      pluginv1.PluginType_PLUGIN_TYPE_WEB_SEARCH,
	"model_provider":  pluginv1.PluginType_PLUGIN_TYPE_MODEL_PROVIDER,
}

var dataAccessToProto = map[string]pluginv1.DataAccess{
	"document_content":  pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
	"document_metadata": pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_METADATA,
	"query_text":        pluginv1.DataAccess_DATA_ACCESS_QUERY_TEXT,
	"conversation":      pluginv1.DataAccess_DATA_ACCESS_CONVERSATION,
}

func normalizePluginType(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func normalizeDataAccess(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func validateAllowedHost(value string) error {
	host := strings.TrimSpace(strings.ToLower(value))
	if host == "" || strings.ContainsAny(host, "/?#@") {
		return fmt.Errorf("invalid allowed host %q", value)
	}
	if strings.HasPrefix(host, "*.") {
		host = strings.TrimPrefix(host, "*.")
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return nil
	}
	if strings.Contains(host, ":") {
		var err error
		host, _, err = net.SplitHostPort(host)
		if err != nil {
			return fmt.Errorf("invalid allowed host %q", value)
		}
	}
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") {
		return fmt.Errorf("invalid allowed host %q", value)
	}
	return nil
}
