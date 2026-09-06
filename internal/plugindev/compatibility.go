package plugindev

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/blang/semver/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	reflectionv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
)

// CompatibilityOptions defines one deterministic plugin compatibility check.
// Address is optional: without it, only the portable manifest contract runs.
type CompatibilityOptions struct {
	ManifestPath   string
	WeKnoraVersion string
	Address        string
	ConfigPath     string
}

type CompatibilityResult struct {
	PluginID        string
	PluginVersion   string
	WeKnoraVersion  string
	ProtocolVersion string
	Types           []string
	LiveChecked     bool
	HealthStatus    string
	Services        []string
}

// CheckCompatibility verifies the static manifest contract and, when Address
// is provided, the capabilities exposed by the running plugin process.
func CheckCompatibility(ctx context.Context, options CompatibilityOptions) (*CompatibilityResult, error) {
	manifest, err := ValidateManifest(options.ManifestPath)
	if err != nil {
		return nil, err
	}
	if err := checkWeKnoraVersion(manifest.Spec.WeKnoraVersionConstraint, options.WeKnoraVersion); err != nil {
		return nil, err
	}
	protocol, err := pluginsdk.NegotiateProtocol(pluginsdk.ProtocolVersion, manifest.Spec.ProtocolVersion)
	if err != nil {
		return nil, fmt.Errorf("protocol compatibility: %w", err)
	}

	result := &CompatibilityResult{
		PluginID:        manifest.Metadata.ID,
		PluginVersion:   manifest.Metadata.Version,
		WeKnoraVersion:  strings.TrimPrefix(strings.TrimSpace(options.WeKnoraVersion), "v"),
		ProtocolVersion: protocol,
		Types:           manifest.NormalizedTypes(),
	}
	if strings.TrimSpace(options.Address) == "" {
		return result, nil
	}

	doctor, err := Doctor(ctx, options.Address, options.ManifestPath, options.ConfigPath)
	if err != nil {
		return nil, err
	}
	if doctor.ConfigValidation != "valid" {
		return nil, errors.New("plugin rejected the compatibility-test configuration")
	}
	if doctor.HealthStatus != "STATUS_SERVING" {
		return nil, fmt.Errorf("plugin is not serving: %s", doctor.HealthStatus)
	}
	services, err := reflectedServices(ctx, options.Address)
	if err != nil {
		return nil, err
	}
	if err := requireDeclaredServices(manifest, services); err != nil {
		return nil, err
	}
	result.LiveChecked = true
	result.HealthStatus = doctor.HealthStatus
	result.Services = services
	return result, nil
}

func checkWeKnoraVersion(constraint, current string) error {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	if current == "" {
		return errors.New("current WeKnora version is required")
	}
	version, err := semver.Parse(current)
	if err != nil {
		return fmt.Errorf("invalid current WeKnora version %q: %w", current, err)
	}
	rangeCheck, err := semver.ParseRange(strings.TrimSpace(constraint))
	if err != nil {
		return fmt.Errorf("invalid plugin WeKnora version constraint %q: %w", constraint, err)
	}
	if !rangeCheck(version) {
		return fmt.Errorf("WeKnora %s does not satisfy plugin constraint %s", version, constraint)
	}
	return nil
}

func reflectedServices(ctx context.Context, address string) ([]string, error) {
	if err := validateLocalAddress(address); err != nil {
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := grpc.DialContext(checkCtx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("connect to local plugin reflection service: %w", err)
	}
	defer connection.Close()
	stream, err := reflectionv1.NewServerReflectionClient(connection).ServerReflectionInfo(checkCtx)
	if err != nil {
		return nil, fmt.Errorf("open plugin reflection stream: %w", err)
	}
	if err := stream.Send(&reflectionv1.ServerReflectionRequest{
		MessageRequest: &reflectionv1.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		return nil, fmt.Errorf("request plugin service list: %w", err)
	}
	response, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("read plugin service list; rebuild the plugin with the current Go SDK: %w", err)
	}
	if response.GetErrorResponse() != nil {
		return nil, fmt.Errorf("plugin reflection failed: %s", response.GetErrorResponse().GetErrorMessage())
	}
	services := make([]string, 0, len(response.GetListServicesResponse().GetService()))
	for _, service := range response.GetListServicesResponse().GetService() {
		services = append(services, service.GetName())
	}
	sort.Strings(services)
	return services, nil
}

func requireDeclaredServices(manifest *pluginsdk.Manifest, services []string) error {
	available := make(map[string]struct{}, len(services))
	for _, service := range services {
		available[service] = struct{}{}
	}
	required := []string{"weknora.plugin.v1.PluginLifecycle"}
	serviceByType := map[string]string{
		"data_source":      "weknora.plugin.v1.DataSourcePlugin",
		"document_parser":  "weknora.plugin.v1.DocumentParserPlugin",
		"web_search":       "weknora.plugin.v1.WebSearchPlugin",
		"model_provider":   "weknora.plugin.v1.ModelProviderPlugin",
		"retrieval_engine": "weknora.plugin.v1.RetrievalEnginePlugin",
	}
	for _, pluginType := range manifest.NormalizedTypes() {
		required = append(required, serviceByType[pluginType])
	}
	for _, service := range required {
		if _, ok := available[service]; !ok {
			return fmt.Errorf("declared plugin capability is not registered: %s", service)
		}
	}
	return nil
}

func validateLocalAddress(address string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("address must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("compatibility checks only connect to localhost or a loopback IP")
	}
	return nil
}
