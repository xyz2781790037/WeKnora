package main

import (
	"context"
	_ "embed"
	"encoding/json"
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
	if err != nil {
		log.Fatalf("invalid plugin manifest: %v", err)
	}

	handler := &exampleDataSource{}
	server, err := plugin.NewServer(manifest, plugin.ServerOptions{
		Lifecycle:  handler,
		DataSource: handler,
	})
	if err != nil {
		log.Fatalf("create plugin server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	address := os.Getenv("WEKNORA_PLUGIN_LISTEN")
	if address == "" {
		address = ":9000"
	}
	if err := server.Serve(ctx, address); err != nil {
		log.Fatalf("serve plugin: %v", err)
	}
}

// exampleDataSource is intentionally small. Copy the template, replace these
// methods with source-specific API calls, and keep WeKnora parsing out of the
// plugin: Sync should emit raw files through emitter.EmitDocument.
type exampleDataSource struct{}

func (s *exampleDataSource) ValidateConfig(_ context.Context, config json.RawMessage) []plugin.FieldViolation {
	if !json.Valid(config) {
		return []plugin.FieldViolation{{Field: "$", Description: "配置必须是合法 JSON"}}
	}
	return nil
}

func (s *exampleDataSource) HealthCheck(_ context.Context) plugin.Health {
	return plugin.Health{
		Status:  pluginv1.HealthCheckResponse_STATUS_SERVING,
		Message: "示例插件运行正常",
	}
}

func (s *exampleDataSource) ListResources(
	_ context.Context,
	_ plugin.Invocation,
	_ json.RawMessage,
	_ string,
) ([]plugin.Resource, error) {
	return []plugin.Resource{{ExternalID: "root", Name: "示例资源", Type: "collection"}}, nil
}

func (s *exampleDataSource) ResolveResourceAncestors(
	_ context.Context,
	_ plugin.Invocation,
	_ json.RawMessage,
	_ []string,
) ([]string, error) {
	return nil, nil
}

func (s *exampleDataSource) Sync(
	_ context.Context,
	_ plugin.DataSourceSyncInput,
	emitter plugin.DataSourceEmitter,
) error {
	// Replace this with source traversal and EmitDocument / EmitDelete calls.
	return emitter.Checkpoint(json.RawMessage(`{"version":1}`))
}
