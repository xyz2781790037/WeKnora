package pluginruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
)

func TestAuthorizedRequiresBearerScheme(t *testing.T) {
	const token = "runtime-secret"

	valid := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+token,
	))
	assert.True(t, authorized(valid, token))

	missingScheme := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", token,
	))
	assert.False(t, authorized(missingScheme, token))

	wrong := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer wrong-secret",
	))
	assert.False(t, authorized(wrong, token))
}
