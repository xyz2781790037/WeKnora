package plugindev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type DoctorResult struct {
	PluginID         string
	ProtocolVersion  string
	HealthStatus     string
	HealthMessage    string
	ConfigValidation string
}

func ValidateManifest(path string) (*pluginsdk.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return pluginsdk.ParseManifest(data)
}

func Doctor(ctx context.Context, address, manifestPath, configPath string) (*DoctorResult, error) {
	manifest, err := ValidateManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return nil, fmt.Errorf("address must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("doctor only connects to localhost or a loopback IP")
	}
	configJSON := json.RawMessage(`{}`)
	if strings.TrimSpace(configPath) != "" {
		configJSON, err = os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if !json.Valid(configJSON) {
			return nil, errors.New("config file must contain valid JSON")
		}
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := grpc.DialContext(dialCtx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("connect to local plugin: %w", err)
	}
	defer connection.Close()
	checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
	defer checkCancel()
	client := pluginv1.NewPluginLifecycleClient(connection)
	invocation := &pluginv1.InvocationContext{PluginId: manifest.Metadata.ID}
	handshake, err := client.Handshake(checkCtx, &pluginv1.HandshakeRequest{
		Context: invocation, HostProtocolVersion: pluginsdk.ProtocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	expectedManifest, err := manifest.ToProto()
	if err != nil {
		return nil, fmt.Errorf("encode local manifest: %w", err)
	}
	if !proto.Equal(expectedManifest, handshake.GetManifest()) {
		return nil, errors.New("running plugin manifest does not match local plugin.yaml")
	}
	expectedProtocol, err := pluginsdk.NegotiateProtocol(pluginsdk.ProtocolVersion, manifest.Spec.ProtocolVersion)
	if err != nil {
		return nil, err
	}
	if handshake.GetNegotiatedProtocolVersion() != expectedProtocol {
		return nil, fmt.Errorf(
			"unexpected negotiated protocol: got %s, want %s",
			handshake.GetNegotiatedProtocolVersion(),
			expectedProtocol,
		)
	}
	health, err := client.HealthCheck(checkCtx, &pluginv1.HealthCheckRequest{Context: invocation})
	if err != nil {
		return nil, fmt.Errorf("health check: %w", err)
	}
	validation, err := client.ValidateConfig(checkCtx, &pluginv1.ValidateConfigRequest{Context: invocation, ConfigJson: configJSON})
	if err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	validationText := "valid"
	if !validation.GetValid() {
		validationText = "invalid"
	}
	return &DoctorResult{
		PluginID: manifest.Metadata.ID, ProtocolVersion: handshake.GetNegotiatedProtocolVersion(),
		HealthStatus: health.GetStatus().String(), HealthMessage: health.GetMessage(), ConfigValidation: validationText,
	}, nil
}
