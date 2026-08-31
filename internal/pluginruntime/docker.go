package pluginruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxDockerErrorBody = 64 * 1024

type dockerClient struct {
	httpClient *http.Client
}

func newDockerClient(socketPath string) (*dockerClient, error) {
	if _, err := os.Stat(socketPath); err != nil {
		return nil, fmt.Errorf("access Docker socket: %w", err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
	return &dockerClient{httpClient: &http.Client{Transport: transport}}, nil
}

func (c *dockerClient) ping(ctx context.Context) error {
	response, err := c.request(ctx, http.MethodGet, "/_ping", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) ensureInternalNetwork(ctx context.Context, name string) error {
	response, err := c.request(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusOK {
		defer response.Body.Close()
		var inspected struct {
			Internal bool `json:"Internal"`
		}
		if err := json.NewDecoder(response.Body).Decode(&inspected); err != nil {
			return fmt.Errorf("inspect plugin sandbox network: %w", err)
		}
		if !inspected.Internal {
			return fmt.Errorf("plugin sandbox network %q exists but is not internal", name)
		}
		return nil
	}
	if response.StatusCode != http.StatusNotFound {
		defer response.Body.Close()
		return dockerResponseError(response)
	}
	_ = response.Body.Close()

	body := map[string]any{
		"Name":           name,
		"CheckDuplicate": true,
		"Driver":         "bridge",
		"Internal":       true,
		"Attachable":     true,
	}
	response, err = c.request(ctx, http.MethodPost, "/networks/create", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) connectContainerToNetwork(
	ctx context.Context,
	networkName, containerName string,
	aliases []string,
) error {
	connected, err := c.networkContainsContainer(ctx, networkName, containerName)
	if err != nil || connected {
		return err
	}
	response, err := c.request(ctx, http.MethodPost, "/networks/"+url.PathEscape(networkName)+"/connect", map[string]any{
		"Container": containerName,
		"EndpointConfig": map[string]any{
			"Aliases": aliases,
		},
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) disconnectContainerFromNetwork(
	ctx context.Context,
	networkName, containerName string,
) error {
	connected, err := c.networkContainsContainer(ctx, networkName, containerName)
	if err != nil || !connected {
		return err
	}
	response, err := c.request(ctx, http.MethodPost, "/networks/"+url.PathEscape(networkName)+"/disconnect", map[string]any{
		"Container": containerName,
		"Force":     true,
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) removeNetwork(ctx context.Context, name string) error {
	response, err := c.request(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) networkContainsContainer(ctx context.Context, networkName, containerName string) (bool, error) {
	response, err := c.request(ctx, http.MethodGet, "/networks/"+url.PathEscape(networkName), nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, dockerResponseError(response)
	}
	var inspected struct {
		Containers map[string]struct {
			Name string `json:"Name"`
		} `json:"Containers"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inspected); err != nil {
		return false, err
	}
	for id, item := range inspected.Containers {
		if item.Name == containerName || strings.HasPrefix(id, containerName) || strings.HasPrefix(containerName, id) {
			return true, nil
		}
	}
	return false, nil
}

func (c *dockerClient) pullImage(ctx context.Context, image string) error {
	path := "/images/create?fromImage=" + url.QueryEscape(image)
	response, err := c.request(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerResponseError(response)
	}
	// Docker streams one JSON progress object per line. Consuming the stream is
	// required for the pull to finish; progress text is intentionally discarded
	// because registry responses can contain sensitive repository details.
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func (c *dockerClient) imageDigest(ctx context.Context, image string) (string, error) {
	response, err := c.request(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", dockerResponseError(response)
	}
	var inspected struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inspected); err != nil {
		return "", err
	}
	repository := imageRepository(image)
	for _, digest := range inspected.RepoDigests {
		name, _, ok := strings.Cut(digest, "@")
		if ok && strings.EqualFold(name, repository) {
			return digest, nil
		}
	}
	if len(inspected.RepoDigests) > 0 {
		return inspected.RepoDigests[0], nil
	}
	return inspected.ID, nil
}

func imageRepository(image string) string {
	image = strings.TrimSpace(image)
	if repository, _, ok := strings.Cut(image, "@"); ok {
		return repository
	}
	lastSlash := strings.LastIndexByte(image, '/')
	if tag := strings.LastIndexByte(image, ':'); tag > lastSlash {
		return image[:tag]
	}
	return image
}

type containerSpec struct {
	PluginID      string
	PluginVersion string
	Image         string
	Name          string
	Network       string
	ProxyURL      string
	ProxyToken    string
	PluginPort    int
	MemoryBytes   int64
	NanoCPUs      int64
	PIDsLimit     int64
}

func (c *dockerClient) createContainer(ctx context.Context, spec containerSpec) error {
	proxyURL, err := url.Parse(spec.ProxyURL)
	if err != nil {
		return fmt.Errorf("parse plugin proxy URL: %w", err)
	}
	proxyURL.User = url.UserPassword(spec.PluginID, spec.ProxyToken)
	port := strconv.Itoa(spec.PluginPort) + "/tcp"
	body := map[string]any{
		"Image": spec.Image,
		"User":  "65532:65532",
		"Env": []string{
			"WEKNORA_PLUGIN_LISTEN=:" + strconv.Itoa(spec.PluginPort),
			"HTTP_PROXY=" + proxyURL.String(),
			"HTTPS_PROXY=" + proxyURL.String(),
			"http_proxy=" + proxyURL.String(),
			"https_proxy=" + proxyURL.String(),
			"NO_PROXY=localhost,127.0.0.1",
			"no_proxy=localhost,127.0.0.1",
		},
		"ExposedPorts": map[string]any{port: map[string]any{}},
		"Labels": map[string]string{
			"io.weknora.plugin.managed": "true",
			"io.weknora.plugin.id":      spec.PluginID,
			"io.weknora.plugin.version": spec.PluginVersion,
		},
		"HostConfig": map[string]any{
			"ReadonlyRootfs": true,
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges"},
			"NetworkMode":    spec.Network,
			"Memory":         spec.MemoryBytes,
			"NanoCpus":       spec.NanoCPUs,
			"PidsLimit":      spec.PIDsLimit,
			"Tmpfs": map[string]string{
				"/tmp": "rw,noexec,nosuid,size=67108864",
			},
			"LogConfig": map[string]any{
				"Type": "local",
				"Config": map[string]string{
					"max-size": "10m",
					"max-file": "3",
				},
			},
			"RestartPolicy": map[string]any{"Name": "no"},
		},
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": map[string]any{
				spec.Network: map[string]any{},
			},
		},
	}
	response, err := c.request(
		ctx,
		http.MethodPost,
		"/containers/create?name="+url.QueryEscape(spec.Name),
		body,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) startContainer(ctx context.Context, name string) error {
	response, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotModified {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) stopContainer(ctx context.Context, name string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	response, err := c.request(
		ctx,
		http.MethodPost,
		"/containers/"+url.PathEscape(name)+"/stop?t="+strconv.Itoa(seconds),
		nil,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent &&
		response.StatusCode != http.StatusNotModified &&
		response.StatusCode != http.StatusNotFound {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) removeContainer(ctx context.Context, name string) error {
	response, err := c.request(
		ctx,
		http.MethodDelete,
		"/containers/"+url.PathEscape(name)+"?force=true&v=true",
		nil,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		return dockerResponseError(response)
	}
	return nil
}

func (c *dockerClient) containerState(ctx context.Context, name string) (bool, bool, error) {
	response, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return false, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, false, dockerResponseError(response)
	}
	var inspected struct {
		State struct {
			Running bool   `json:"Running"`
			Error   string `json:"Error"`
		} `json:"State"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inspected); err != nil {
		return false, false, err
	}
	if inspected.State.Error != "" {
		return true, false, errors.New(inspected.State.Error)
	}
	return true, inspected.State.Running, nil
}

func (c *dockerClient) containerNetworkMode(ctx context.Context, name string) (string, error) {
	response, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", dockerResponseError(response)
	}
	var inspected struct {
		HostConfig struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inspected); err != nil {
		return "", err
	}
	return inspected.HostConfig.NetworkMode, nil
}

func (c *dockerClient) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.httpClient.Do(request)
}

func dockerResponseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxDockerErrorBody))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("Docker API %s: %s", response.Status, message)
}
