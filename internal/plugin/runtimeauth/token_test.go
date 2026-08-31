package runtimeauth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateTokenStrengthAndTransportSafety(t *testing.T) {
	require.ErrorContains(t, Validate("short"), "at least")
	require.ErrorContains(t, Validate(strings.Repeat("a", 31)+"\n"), "visible ASCII")
	require.NoError(t, Validate(strings.Repeat("a", 32)))
}
