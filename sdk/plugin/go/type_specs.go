package plugin

import (
	"errors"
	"fmt"
	"strings"
)

// TypeSpecification documents where each plugin family's configuration is
// stored and which manifest conventions its adapter understands.
type TypeSpecification struct {
	Type                   string   `json:"type"`
	ConfigScope            string   `json:"config_scope"`
	RequiredManifestFields []string `json:"required_manifest_fields"`
	SupportedSecretFields  []string `json:"supported_secret_fields"`
	CapabilityExamples     []string `json:"capability_examples"`
}

func TypeSpecifications() []TypeSpecification {
	return []TypeSpecification{
		{Type: "data_source", ConfigScope: "data_source_instance", RequiredManifestFields: []string{"spec.connectorType", "spec.config.schema"}, SupportedSecretFields: []string{"*"}, CapabilityExamples: []string{"incremental", "deletion_sync"}},
		{Type: "document_parser", ConfigScope: "tenant_parser_engine", RequiredManifestFields: []string{"spec.config.schema", "spec.capabilities[file_type:*]", "spec.permissions.dataAccess[document_content,document_metadata]"}, SupportedSecretFields: []string{}, CapabilityExamples: []string{"file_type:pdf", "file_type:docx"}},
		{Type: "web_search", ConfigScope: "web_search_provider", RequiredManifestFields: []string{"spec.config.schema", "spec.permissions.dataAccess[query_text]"}, SupportedSecretFields: []string{"api_key"}, CapabilityExamples: []string{"search"}},
		{Type: "model_provider", ConfigScope: "model_instance", RequiredManifestFields: []string{"spec.config.schema", "spec.capabilities", "spec.permissions.dataAccess[按能力]"}, SupportedSecretFields: []string{"api_key", "app_secret"}, CapabilityExamples: []string{"chat", "embedding", "rerank"}},
	}
}

func validateTypeConfigContract(types map[string]struct{}, secretFields, capabilities, dataAccess []string) error {
	access := make(map[string]struct{}, len(dataAccess))
	for _, value := range dataAccess {
		access[normalizeDataAccess(value)] = struct{}{}
	}
	requireAccess := func(pluginType string, values ...string) error {
		for _, value := range values {
			if _, ok := access[value]; !ok {
				return fmt.Errorf("%s requires permissions.dataAccess %q", pluginType, value)
			}
		}
		return nil
	}
	if _, parser := types["document_parser"]; parser {
		if len(secretFields) > 0 {
			return errors.New("document_parser config.secretFields are not supported")
		}
		hasFileType := false
		for _, capability := range capabilities {
			if value, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(capability)), "file_type:"); ok && strings.TrimSpace(value) != "" {
				hasFileType = true
				break
			}
		}
		if !hasFileType {
			return errors.New("document_parser requires at least one file_type:* capability")
		}
		if err := requireAccess("document_parser", "document_content", "document_metadata"); err != nil {
			return err
		}
	}
	if _, search := types["web_search"]; search {
		for _, field := range secretFields {
			if field != "api_key" {
				return fmt.Errorf("web_search secret field %q is unsupported; use api_key", field)
			}
		}
		if err := requireAccess("web_search", "query_text"); err != nil {
			return err
		}
	}
	if _, model := types["model_provider"]; model {
		hasModelCapability := false
		for _, capability := range capabilities {
			switch strings.ToLower(strings.TrimSpace(capability)) {
			case "chat":
				hasModelCapability = true
				if err := requireAccess("model_provider chat", "conversation"); err != nil {
					return err
				}
			case "embed", "embedding", "rerank":
				hasModelCapability = true
				if err := requireAccess("model_provider "+capability, "query_text", "document_content"); err != nil {
					return err
				}
			}
		}
		if !hasModelCapability {
			return errors.New("model_provider requires chat, embedding or rerank capability")
		}
		for _, field := range secretFields {
			if field != "api_key" && field != "app_secret" {
				return fmt.Errorf("model_provider secret field %q is unsupported", field)
			}
		}
	}
	return nil
}
