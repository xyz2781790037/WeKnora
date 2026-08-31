// Package runtimeauth resolves the shared credential used between WeKnora and
// plugin-runtime. Keeping the precedence here prevents the two processes from
// interpreting secret configuration differently.
package runtimeauth

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	TokenEnv      = "WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN"
	TokenFileEnv  = "WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN_FILE"
	minTokenBytes = 32
	maxTokenBytes = 1024
)

// Resolve returns the token from the environment, or from the configured
// secret file when the environment value is empty. An unset source is not an
// error so callers can keep plugin-runtime optional.
func Resolve() (string, error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		return token, nil
	}

	path := strings.TrimSpace(os.Getenv(TokenFileEnv))
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", TokenFileEnv, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s points to an empty file", TokenFileEnv)
	}
	return token, nil
}

// Validate enforces enough entropy for a shared control-plane credential and
// restricts it to visible ASCII so it is safe to carry in gRPC metadata.
func Validate(token string) error {
	if len(token) < minTokenBytes {
		return fmt.Errorf("plugin runtime auth token must be at least %d bytes", minTokenBytes)
	}
	if len(token) > maxTokenBytes {
		return fmt.Errorf("plugin runtime auth token must not exceed %d bytes", maxTokenBytes)
	}
	for _, value := range []byte(token) {
		if value < 0x21 || value > 0x7e {
			return errors.New("plugin runtime auth token must contain visible ASCII characters only")
		}
	}
	return nil
}
