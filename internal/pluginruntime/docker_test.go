package pluginruntime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureInternalNetworkAcceptsExistingInternalNetwork(t *testing.T) {
	client := &dockerClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/networks/weknora-sandbox", request.URL.Path)
		return dockerTestResponse(http.StatusOK, `{"Internal":true}`), nil
	})}}

	require.NoError(t, client.ensureInternalNetwork(context.Background(), "weknora-sandbox"))
}

func TestEnsureInternalNetworkRejectsExistingNonInternalNetwork(t *testing.T) {
	client := &dockerClient{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return dockerTestResponse(http.StatusOK, `{"Internal":false}`), nil
	})}}

	err := client.ensureInternalNetwork(context.Background(), "weknora-sandbox")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not internal")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func dockerTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
