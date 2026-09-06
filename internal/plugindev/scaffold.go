package plugindev

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var packageNamePattern = regexp.MustCompile(`[^a-zA-Z0-9]+`)
var pluginIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{1,126}[a-z0-9])$`)

type ScaffoldOptions struct {
	Output  string
	Type    string
	ID      string
	Name    string
	Image   string
	SDKPath string
}

type scaffoldData struct {
	Type          string
	ID            string
	Name          string
	Image         string
	Module        string
	ConnectorType string
	RetrieverType string
	Handler       string
	ServerField   string
	Capabilities  string
	DataAccess    string
	SDKPath       string
}

func Scaffold(options ScaffoldOptions) error {
	data, err := normalizeScaffoldOptions(options)
	if err != nil {
		return err
	}
	info, err := os.Stat(options.Output)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("output path %s is not a directory", options.Output)
	case err == nil:
		entries, readErr := os.ReadDir(options.Output)
		if readErr != nil {
			return readErr
		}
		if len(entries) > 0 {
			return fmt.Errorf("output directory %s is not empty", options.Output)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(options.Output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	files := map[string]string{
		"go.mod":                              goModTemplate,
		"main.go":                             mainTemplate,
		"plugin.yaml":                         manifestTemplate,
		"Dockerfile":                          dockerfileTemplate,
		"README.md":                           readmeTemplate,
		".github/workflows/compatibility.yml": compatibilityWorkflowTemplate,
	}
	for name, source := range files {
		content, renderErr := render(name, source, data)
		if renderErr != nil {
			return renderErr
		}
		target := filepath.Join(options.Output, name)
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			return fmt.Errorf("create parent directory for %s: %w", name, mkdirErr)
		}
		if writeErr := os.WriteFile(target, content, 0o644); writeErr != nil {
			return fmt.Errorf("write %s: %w", name, writeErr)
		}
	}
	return nil
}

func normalizeScaffoldOptions(options ScaffoldOptions) (scaffoldData, error) {
	pluginType := strings.ToLower(strings.TrimSpace(options.Type))
	switch pluginType {
	case "data_source", "document_parser", "web_search", "model_provider", "retrieval_engine":
	default:
		return scaffoldData{}, fmt.Errorf("unsupported plugin type %q", options.Type)
	}
	if strings.TrimSpace(options.Output) == "" || strings.TrimSpace(options.ID) == "" || strings.TrimSpace(options.Name) == "" {
		return scaffoldData{}, errors.New("output, id and name are required")
	}
	if !pluginIDPattern.MatchString(strings.TrimSpace(options.ID)) {
		return scaffoldData{}, errors.New("id must use lowercase reverse-DNS characters")
	}
	sdkPath := strings.TrimSpace(options.SDKPath)
	if sdkPath == "" {
		sdkPath = "."
	}
	absSDKPath, err := filepath.Abs(sdkPath)
	if err != nil {
		return scaffoldData{}, fmt.Errorf("resolve SDK path: %w", err)
	}
	absOutput, err := filepath.Abs(options.Output)
	if err != nil {
		return scaffoldData{}, fmt.Errorf("resolve output path: %w", err)
	}
	relativeSDKPath, err := filepath.Rel(absOutput, absSDKPath)
	if err != nil {
		return scaffoldData{}, fmt.Errorf("make SDK path relative: %w", err)
	}
	image := strings.TrimSpace(options.Image)
	if image == "" {
		image = "ghcr.io/your-org/" + strings.ReplaceAll(options.ID, ".", "-") + ":0.1.0"
	}
	connectorType := ""
	retrieverType := ""
	if pluginType == "data_source" {
		connectorType = strings.Trim(packageNamePattern.ReplaceAllString(options.ID, "_"), "_")
	}
	if pluginType == "retrieval_engine" {
		retrieverType = generatedRetrieverType(options.ID)
	}
	handler, field, capabilities, access := capabilityTemplate(pluginType)
	return scaffoldData{
		Type: pluginType, ID: strings.TrimSpace(options.ID), Name: strings.TrimSpace(options.Name),
		Image: image, Module: "example.com/" + strings.ReplaceAll(options.ID, ".", "-"),
		ConnectorType: connectorType, RetrieverType: retrieverType, Handler: handler, ServerField: field,
		Capabilities: capabilities, DataAccess: access, SDKPath: filepath.ToSlash(relativeSDKPath),
	}, nil
}

func generatedRetrieverType(pluginID string) string {
	value := strings.Trim(packageNamePattern.ReplaceAllString(pluginID, "_"), "_")
	if len(value) <= 50 {
		return value
	}
	sum := sha256.Sum256([]byte(pluginID))
	suffix := fmt.Sprintf("_%x", sum[:4])
	return strings.TrimRight(value[:50-len(suffix)], "_") + suffix
}

func render(name, source string, data scaffoldData) ([]byte, error) {
	tmpl, err := template.New(name).Parse(source)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func capabilityTemplate(pluginType string) (handler, serverField, capabilities, dataAccess string) {
	switch pluginType {
	case "data_source":
		return dataSourceHandler, "DataSource", "\n    - incremental\n    - deletion_sync", "\n      - document_content\n      - document_metadata"
	case "document_parser":
		return parserHandler, "DocumentParser", "\n    - file_type:md", "\n      - document_content\n      - document_metadata"
	case "web_search":
		return webSearchHandler, "WebSearch", "\n    - search", "\n      - query_text"
	case "retrieval_engine":
		return retrievalEngineHandler, "RetrievalEngine", "\n    - keywords\n    - vector", "\n      - document_content\n      - document_metadata\n      - query_text\n      - embeddings"
	default:
		return modelProviderHandler, "ModelProvider", "\n    - chat\n    - embedding\n    - rerank", "\n      - conversation\n      - query_text\n      - document_content"
	}
}

const goModTemplate = `module {{.Module}}

go 1.26.0

require github.com/Tencent/WeKnora v0.0.0

replace github.com/Tencent/WeKnora => {{.SDKPath}}
`

const mainTemplate = `package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	plugin "github.com/Tencent/WeKnora/sdk/plugin/go"
)

//go:embed plugin.yaml
var manifestYAML []byte

func main() {
	manifest, err := plugin.ParseManifest(manifestYAML)
	if err != nil { log.Fatalf("invalid manifest: %v", err) }
	handler := &handler{}
	server, err := plugin.NewServer(manifest, plugin.ServerOptions{Lifecycle: handler, {{.ServerField}}: handler})
	if err != nil { log.Fatalf("create server: %v", err) }
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	address := os.Getenv("WEKNORA_PLUGIN_LISTEN")
	if address == "" { address = ":9000" }
	if err := server.Serve(ctx, address); err != nil { log.Fatal(err) }
}

type handler struct{}

func (*handler) ValidateConfig(_ context.Context, config json.RawMessage) []plugin.FieldViolation {
	if !json.Valid(config) {
		violation := plugin.FieldViolation{Field: "$", Description: "配置必须是合法 JSON"}
		return []plugin.FieldViolation{violation}
	}
	return nil
}

func (*handler) HealthCheck(context.Context) plugin.Health {
	return plugin.Health{Status: pluginv1.HealthCheckResponse_STATUS_SERVING, Message: "插件运行正常"}
}

{{.Handler}}

var _ = io.EOF
`

const dataSourceHandler = `func (*handler) ListResources(context.Context, plugin.Invocation, json.RawMessage, string) ([]plugin.Resource, error) {
	return []plugin.Resource{{ExternalID: "root", Name: "示例资源", Type: "collection"}}, nil
}
func (*handler) ResolveResourceAncestors(context.Context, plugin.Invocation, json.RawMessage, []string) ([]string, error) { return nil, nil }
func (*handler) Sync(_ context.Context, _ plugin.DataSourceSyncInput, emitter plugin.DataSourceEmitter) error {
	return emitter.Checkpoint(json.RawMessage("{\"version\":1}"))
}`

const parserHandler = `func (*handler) Parse(_ context.Context, _ plugin.ParseInput, stream plugin.ParseStream) error {
	for {
		content, err := stream.RecvContent()
		if err == io.EOF { return nil }
		if err != nil { return err }
		if err := stream.EmitMarkdown(string(content)); err != nil { return err }
	}
}`

const webSearchHandler = `func (*handler) Search(_ context.Context, input plugin.WebSearchInput) (plugin.WebSearchOutput, error) {
	return plugin.WebSearchOutput{Results: []plugin.WebSearchResult{{Title: input.Query, URL: "https://example.com", Snippet: "替换为真实搜索结果", Source: "example"}}}, nil
}`

const modelProviderHandler = `func (*handler) ListModels(context.Context, plugin.Invocation, json.RawMessage) ([]plugin.Model, error) {
	return []plugin.Model{{ID: "example", Name: "Example", Capabilities: []pluginv1.ModelCapability{pluginv1.ModelCapability_MODEL_CAPABILITY_CHAT}}}, nil
}
func (*handler) Chat(_ context.Context, _ plugin.ChatInput, emitter plugin.ChatEmitter) error { return emitter.Emit("example", true, plugin.ModelUsage{}) }
func (*handler) Embed(context.Context, plugin.EmbedInput) (plugin.EmbedOutput, error) { return plugin.EmbedOutput{}, nil }
func (*handler) Rerank(context.Context, plugin.RerankInput) (plugin.RerankOutput, error) { return plugin.RerankOutput{}, nil }`

const retrievalEngineHandler = `func (*handler) Upsert(context.Context, plugin.RetrievalUpsertInput) error { return nil }
func (*handler) Retrieve(_ context.Context, input plugin.RetrievalQueryInput) ([]plugin.RetrievalResultSet, error) {
	return []plugin.RetrievalResultSet{{RetrieverType: input.RetrieverType}}, nil
}
func (*handler) EstimateStorage(context.Context, plugin.RetrievalEstimateInput) (int64, error) { return 0, nil }
func (*handler) Delete(context.Context, plugin.RetrievalDeleteInput) error { return nil }
func (*handler) Copy(context.Context, plugin.RetrievalCopyInput) error { return nil }
func (*handler) UpdateChunks(context.Context, plugin.RetrievalUpdateChunksInput) error { return nil }`

const manifestTemplate = `apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: {{.ID}}
  name: {{.Name}}
  description: 使用 pluginctl 生成的 {{.Type}} 插件
  version: 0.1.0
spec:
  protocolVersion: 1.1.0
  weknoraVersion: ">=0.2.0"
  image: {{.Image}}
  types:
    - {{.Type}}
  capabilities:{{.Capabilities}}
{{if .ConnectorType}}  connectorType: {{.ConnectorType}}
{{end}}{{if .RetrieverType}}  retrieverEngineType: {{.RetrieverType}}
{{end}}  icon: extension
  defaultTimeout: 2m
  config:
    schema:
      $schema: https://json-schema.org/draft/2020-12/schema
      type: object
      additionalProperties: false
      properties: {}
    secretFields: []
  permissions:
    network: false
    allowedHosts: []
    dataAccess:{{.DataAccess}}
  resources:
    memoryBytes: 268435456
    nanoCPUs: 500000000
    pidsLimit: 64
    maxConcurrency: 4
    callsPerMinute: 120
`

const dockerfileTemplate = `FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY --from=weknora . /weknora
COPY . .
RUN go mod edit -replace github.com/Tencent/WeKnora=/weknora && \
    go mod tidy && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plugin .
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/plugin /plugin
EXPOSE 9000
USER 65532:65532
ENTRYPOINT ["/plugin"]
`

const readmeTemplate = `# {{.Name}}

这是由 WeKnora pluginctl 生成的 {{.Type}} 插件骨架。

1. 修改 plugin.yaml 中的配置 Schema、权限和镜像地址。
2. 实现 main.go 中的业务方法。
3. 首次执行：go mod tidy
4. 本地运行：WEKNORA_PLUGIN_LISTEN=:9000 go run .
5. 在 WeKnora 主仓运行：go run ./cmd/pluginctl doctor --address 127.0.0.1:9000 --manifest /path/to/plugin.yaml
6. 提交前检查兼容性：go run ./cmd/pluginctl compat --manifest /path/to/plugin.yaml --weknora-version 0.2.0

生成的 go.mod 通过 replace 使用本地主仓 SDK。开发期构建镜像时，把 WeKnora 主仓作为命名构建上下文传入：

    docker buildx build --build-context weknora={{.SDKPath}} -t {{.Image}} .

发布镜像前请把 WeKnora 依赖固定为可拉取的 commit 伪版本，并移除 replace；此时也可删除 Dockerfile 的 weknora 命名上下文。

插件不得直接连接 WeKnora 的 PostgreSQL 或 Redis；所有数据都通过公开 gRPC 协议交换。
`

const compatibilityWorkflowTemplate = `name: Plugin compatibility

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

jobs:
  compatibility:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout plugin
        uses: actions/checkout@v4
        with:
          path: plugin
      - name: Checkout WeKnora
        uses: actions/checkout@v4
        with:
          repository: Tencent/WeKnora
          path: WeKnora
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: 1.26.x
          cache: false
      - name: Check plugin compatibility
        working-directory: WeKnora
        run: >-
          go run ./cmd/pluginctl compat
          --manifest ../plugin/plugin.yaml
          --weknora-version 0.2.0
`
