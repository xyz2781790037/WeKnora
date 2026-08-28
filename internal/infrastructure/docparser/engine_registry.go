package docparser

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// EngineRegistration is the interface every locally registered parser engine
// must implement. Remote-only engines (e.g. markitdown) are discovered via
// the docreader ListEngines RPC and do not need a local registration.
type EngineRegistration interface {
	Name() string
	Description() string
	FileTypes(docreaderConnected bool) []string
	CheckAvailable(docreaderConnected bool, overrides map[string]string) (available bool, reason string)
}

// localEngines holds all locally registered parser engines.
var (
	engineMu        sync.RWMutex
	localEngines    []EngineRegistration
	externalReaders = make(map[string]func(map[string]string) interfaces.DocReader)
)

// RegisterEngine adds an engine to the local registry. Called in init().
func RegisterEngine(e EngineRegistration) {
	engineMu.Lock()
	defer engineMu.Unlock()
	localEngines = append(localEngines, e)
}

type externalEngine struct {
	name, description string
	fileTypes         []string
	configSchema      map[string]any
	secretFields      []string
	pluginID          string
}

func (e *externalEngine) Name() string                                        { return e.name }
func (e *externalEngine) Description() string                                 { return e.description }
func (e *externalEngine) FileTypes(bool) []string                             { return append([]string(nil), e.fileTypes...) }
func (*externalEngine) CheckAvailable(bool, map[string]string) (bool, string) { return true, "" }

// RegisterExternalEngine keeps the original registration API for internal
// callers that do not expose manifest-driven configuration.
func RegisterExternalEngine(name, description string, fileTypes []string, factory func(map[string]string) interfaces.DocReader) error {
	return RegisterExternalEngineWithMetadata(name, description, fileTypes, nil, nil, factory)
}

// RegisterExternalEngineWithMetadata registers an external parser together
// with the schema rendered by the shared plugin configuration form.
func RegisterExternalEngineWithMetadata(name, description string, fileTypes []string, configSchema map[string]any, secretFields []string, factory func(map[string]string) interfaces.DocReader) error {
	name = strings.TrimSpace(name)
	if name == "" || factory == nil {
		return fmt.Errorf("external parser engine name and factory are required")
	}
	engineMu.Lock()
	defer engineMu.Unlock()
	for _, engine := range localEngines {
		if engine.Name() == name {
			if _, external := externalReaders[name]; !external {
				return fmt.Errorf("parser engine %s conflicts with a built-in engine", name)
			}
			for index, existing := range localEngines {
				if existing.Name() == name {
					localEngines[index] = &externalEngine{name: name, description: description, fileTypes: append([]string(nil), fileTypes...), configSchema: configSchema, secretFields: append([]string(nil), secretFields...), pluginID: name}
					externalReaders[name] = factory
					return nil
				}
			}
		}
	}
	localEngines = append(localEngines, &externalEngine{name: name, description: description, fileTypes: append([]string(nil), fileTypes...), configSchema: configSchema, secretFields: append([]string(nil), secretFields...), pluginID: name})
	externalReaders[name] = factory
	return nil
}

func UnregisterExternalEngine(name string) {
	engineMu.Lock()
	defer engineMu.Unlock()
	if _, ok := externalReaders[name]; !ok {
		return
	}
	delete(externalReaders, name)
	for index, engine := range localEngines {
		if engine.Name() == name {
			localEngines = append(localEngines[:index], localEngines[index+1:]...)
			return
		}
	}
}

func ExternalReader(name string, overrides map[string]string) (interfaces.DocReader, bool) {
	engineMu.RLock()
	factory, ok := externalReaders[name]
	engineMu.RUnlock()
	if !ok {
		return nil, false
	}
	return factory(overrides), true
}

func init() {
	RegisterEngine(&builtinEngine{})
	RegisterEngine(&simpleEngine{})
	RegisterEngine(&weKnoraCloudEngine{})
	RegisterEngine(&mineruEngine{})
	RegisterEngine(&mineruCloudEngine{})
	RegisterEngine(&paddleOCRVLEngine{})
	RegisterEngine(&paddleOCRVLCloudEngine{})
}

// ---------------------------------------------------------------------------
// builtin — DocReader-backed parser for complex document formats.
// ---------------------------------------------------------------------------

type builtinEngine struct{}

func (e *builtinEngine) Name() string { return "builtin" }
func (e *builtinEngine) Description() string {
	return "DocReader built-in parser engine"
}
func (e *builtinEngine) FileTypes(_ bool) []string {
	return []string{"docx", "doc", "pdf", "md", "markdown", "xlsx", "xls", "epub", "html", "htm", "mhtml", "jpg", "jpeg", "png", "gif", "bmp", "tiff", "webp", "mp3", "wav", "m4a", "flac", "ogg"}
}
func (e *builtinEngine) CheckAvailable(docreaderConnected bool, _ map[string]string) (bool, string) {
	if docreaderConnected {
		return true, ""
	}
	return false, "DocReader service not connected"
}

// SimpleEngineName is the engine name for Go-native simple format handling.
const SimpleEngineName = "simple"

// WeKnoraCloudEngineName is the engine name for WeKnoraCloud-backed document parsing.
const WeKnoraCloudEngineName = "weknoracloud"

// ---------------------------------------------------------------------------
// simple — Go handles md/txt/csv natively, no external service needed.
// Distinct from docreader's "builtin" which uses Python libraries for
// complex formats (docx, pdf, etc.).
// ---------------------------------------------------------------------------

type simpleEngine struct{}

func (e *simpleEngine) Name() string { return SimpleEngineName }
func (e *simpleEngine) Description() string {
	return "Simple format & image parsing (no external service required)"
}
func (e *simpleEngine) FileTypes(_ bool) []string {
	return []string{"md", "markdown", "txt", "csv", "json", "jpg", "jpeg", "png", "gif", "bmp", "tiff", "webp", "mp3", "wav", "m4a", "flac", "ogg"}
}
func (e *simpleEngine) CheckAvailable(_ bool, _ map[string]string) (bool, string) {
	return true, ""
}

// ---------------------------------------------------------------------------
// weknoracloud — Tenant-scoped WeKnoraCloud docreader with signed requests.
// ---------------------------------------------------------------------------

type weKnoraCloudEngine struct{}

func (e *weKnoraCloudEngine) Name() string        { return WeKnoraCloudEngineName }
func (e *weKnoraCloudEngine) Description() string { return "WeKnoraCloud document reader" }
func (e *weKnoraCloudEngine) FileTypes(_ bool) []string {
	return []string{"docx", "doc", "pdf", "md", "markdown", "xlsx", "xls", "pptx", "ppt"}
}
func (e *weKnoraCloudEngine) CheckAvailable(docreaderConnected bool, overrides map[string]string) (bool, string) {
	if overrides["weknoracloud_app_id"] != "" {
		return true, ""
	}
	return false, "WeKnora Cloud credentials not configured. Go to Settings → WeKnora Cloud to set up."
}

// ---------------------------------------------------------------------------
// mineru — Go-native, calls self-hosted MinerU API directly
// ---------------------------------------------------------------------------

type mineruEngine struct{}

func (e *mineruEngine) Name() string        { return "mineru" }
func (e *mineruEngine) Description() string { return "MinerU self-hosted service" }
func (e *mineruEngine) FileTypes(_ bool) []string {
	return []string{"pdf", "jpg", "jpeg", "png", "bmp", "tiff", "doc", "docx", "ppt", "pptx"}
}
func (e *mineruEngine) CheckAvailable(_ bool, overrides map[string]string) (bool, string) {
	endpoint := strings.TrimSpace(overrides["mineru_endpoint"])
	if endpoint == "" {
		return false, "MinerU service not configured"
	}
	return PingMinerU(endpoint)
}

// ---------------------------------------------------------------------------
// mineru_cloud — Go-native, calls MinerU Cloud API directly
// ---------------------------------------------------------------------------

type mineruCloudEngine struct{}

func (e *mineruCloudEngine) Name() string        { return "mineru_cloud" }
func (e *mineruCloudEngine) Description() string { return "MinerU Cloud API" }
func (e *mineruCloudEngine) FileTypes(_ bool) []string {
	return []string{"pdf", "jpg", "jpeg", "png", "bmp", "tiff", "doc", "docx", "ppt", "pptx"}
}
func (e *mineruCloudEngine) CheckAvailable(_ bool, overrides map[string]string) (bool, string) {
	apiKey := strings.TrimSpace(overrides["mineru_api_key"])
	if apiKey == "" {
		return false, "MinerU API Key not configured"
	}
	return PingMinerUCloud(apiKey)
}

// ---------------------------------------------------------------------------
// paddleocr_vl — Go-native, calls a self-hosted PaddleOCR-VL pipeline service
// ---------------------------------------------------------------------------

type paddleOCRVLEngine struct{}

func (e *paddleOCRVLEngine) Name() string        { return "paddleocr_vl" }
func (e *paddleOCRVLEngine) Description() string { return "PaddleOCR-VL self-hosted service" }
func (e *paddleOCRVLEngine) FileTypes(_ bool) []string {
	return []string{"pdf", "jpg", "jpeg", "png", "bmp", "tiff"}
}
func (e *paddleOCRVLEngine) CheckAvailable(_ bool, overrides map[string]string) (bool, string) {
	endpoint := strings.TrimSpace(overrides["paddleocr_vl_endpoint"])
	if endpoint == "" {
		return false, "PaddleOCR-VL service not configured"
	}
	return PingPaddleOCRVL(endpoint)
}

// ---------------------------------------------------------------------------
// paddleocr_vl_cloud — Go-native, calls the PaddleOCR-VL AI Studio cloud API
// ---------------------------------------------------------------------------

type paddleOCRVLCloudEngine struct{}

func (e *paddleOCRVLCloudEngine) Name() string        { return "paddleocr_vl_cloud" }
func (e *paddleOCRVLCloudEngine) Description() string { return "PaddleOCR-VL Cloud API" }
func (e *paddleOCRVLCloudEngine) FileTypes(_ bool) []string {
	return []string{"pdf", "jpg", "jpeg", "png", "bmp", "tiff"}
}
func (e *paddleOCRVLCloudEngine) CheckAvailable(_ bool, overrides map[string]string) (bool, string) {
	token := strings.TrimSpace(overrides["paddleocr_vl_cloud_token"])
	if token == "" {
		return false, "PaddleOCR-VL Cloud Token not configured"
	}
	return PingPaddleOCRVLCloud(token)
}

// ---------------------------------------------------------------------------
// ListAllEngines — merge local + remote
// ---------------------------------------------------------------------------

// ListAllEngines returns the merged engine list: locally registered engines
// plus engines discovered from the remote docreader via ListEngines RPC.
//
// Merge rules:
//   - Local engines are always included, with Go-side availability checks.
//   - For a remote engine whose name matches a local one, the remote's
//     file_types and description take precedence (the remote service is
//     authoritative for its own capabilities).
//   - Remote engines not present locally are appended as-is, enabling
//     auto-discovery of newly added docreader engines without Go changes.
func ListAllEngines(docreaderConnected bool, overrides map[string]string, remoteEngines []types.ParserEngineInfo) []types.ParserEngineInfo {
	engineMu.RLock()
	engines := append([]EngineRegistration(nil), localEngines...)
	engineMu.RUnlock()
	remoteMap := make(map[string]types.ParserEngineInfo, len(remoteEngines))
	for _, re := range remoteEngines {
		remoteMap[re.Name] = re
	}

	seen := make(map[string]bool, len(localEngines))
	result := make([]types.ParserEngineInfo, 0, len(engines)+len(remoteEngines))

	for _, e := range engines {
		name := e.Name()
		seen[name] = true

		fileTypes := e.FileTypes(docreaderConnected)
		description := e.Description()

		if re, ok := remoteMap[name]; ok {
			if len(re.FileTypes) > 0 {
				fileTypes = re.FileTypes
			}
			if re.Description != "" {
				description = re.Description
			}
		}

		available, reason := e.CheckAvailable(docreaderConnected, overrides)
		info := types.ParserEngineInfo{
			Name:              name,
			Description:       description,
			FileTypes:         fileTypes,
			Available:         available,
			UnavailableReason: reason,
		}
		if external, ok := e.(*externalEngine); ok {
			info.Origin = types.PluginOriginExternal
			info.PluginID = external.pluginID
			info.ConfigSchema = external.configSchema
			info.SecretFields = append([]string(nil), external.secretFields...)
		}
		result = append(result, info)
	}

	for _, re := range remoteEngines {
		if seen[re.Name] {
			continue
		}
		result = append(result, re)
	}

	return result
}
