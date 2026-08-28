package docparser

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAllEnginesBuiltinIncludesHTML(t *testing.T) {
	engines := ListAllEngines(true, nil, nil)
	for _, engine := range engines {
		if engine.Name != "builtin" {
			continue
		}
		if !engine.Available {
			t.Fatalf("builtin engine is unavailable: %s", engine.UnavailableReason)
		}

		fileTypes := make(map[string]bool, len(engine.FileTypes))
		for _, fileType := range engine.FileTypes {
			fileTypes[fileType] = true
		}
		for _, want := range []string{"html", "htm"} {
			if !fileTypes[want] {
				t.Errorf("builtin engine file types do not include %q: %v", want, engine.FileTypes)
			}
		}
		return
	}

	t.Fatal("builtin engine not found")
}

func TestExternalEngineLifecycle(t *testing.T) {
	const engineName = "plugin.io.example.parser"
	t.Cleanup(func() { UnregisterExternalEngine(engineName) })
	factory := func(map[string]string) interfaces.DocReader { return nil }
	require.NoError(t, RegisterExternalEngine(engineName, "Example parser", []string{"abc"}, factory))

	reader, ok := ExternalReader(engineName, map[string]string{"key": "value"})
	assert.True(t, ok)
	assert.Nil(t, reader)
	found := false
	for _, engine := range ListAllEngines(false, nil, nil) {
		if engine.Name == engineName {
			found = true
			assert.Equal(t, []string{"abc"}, engine.FileTypes)
		}
	}
	assert.True(t, found)

	UnregisterExternalEngine(engineName)
	_, ok = ExternalReader(engineName, nil)
	assert.False(t, ok)
}
