package plugindev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScaffoldGeneratesValidManifestForEveryType(t *testing.T) {
	for _, pluginType := range []string{"data_source", "document_parser", "web_search", "model_provider", "retrieval_engine"} {
		t.Run(pluginType, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "plugin")
			id := "io.example." + strings.ReplaceAll(pluginType, "_", "-")
			require.NoError(t, Scaffold(ScaffoldOptions{Output: output, Type: pluginType, ID: id, Name: "Example", SDKPath: filepath.Join("..", "..")}))
			manifest, err := ValidateManifest(filepath.Join(output, "plugin.yaml"))
			require.NoError(t, err)
			require.Equal(t, []string{pluginType}, manifest.NormalizedTypes())
			for _, name := range []string{"go.mod", "main.go", "plugin.yaml", "Dockerfile", "README.md", ".github/workflows/compatibility.yml"} {
				_, err := os.Stat(filepath.Join(output, name))
				require.NoError(t, err)
			}
			workflow, err := os.ReadFile(filepath.Join(output, ".github/workflows/compatibility.yml"))
			require.NoError(t, err)
			require.Contains(t, string(workflow), "pluginctl compat")
			dockerfile, err := os.ReadFile(filepath.Join(output, "Dockerfile"))
			require.NoError(t, err)
			require.Contains(t, string(dockerfile), "COPY --from=weknora . /weknora")
			readme, err := os.ReadFile(filepath.Join(output, "README.md"))
			require.NoError(t, err)
			require.Contains(t, string(readme), "--build-context weknora=")
		})
	}
}

func TestScaffoldRefusesNonEmptyOutput(t *testing.T) {
	output := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(output, "keep"), []byte("user data"), 0o644))
	err := Scaffold(ScaffoldOptions{Output: output, Type: "web_search", ID: "io.example.search", Name: "Search"})
	require.ErrorContains(t, err, "not empty")
}

func TestScaffoldLongRetrievalIDProducesValidEngineType(t *testing.T) {
	output := filepath.Join(t.TempDir(), "plugin")
	id := "io.example." + strings.Repeat("long-segment-", 8) + "plugin"
	require.NoError(t, Scaffold(ScaffoldOptions{
		Output: output, Type: "retrieval_engine", ID: id, Name: "Long ID", SDKPath: filepath.Join("..", ".."),
	}))
	manifest, err := ValidateManifest(filepath.Join(output, "plugin.yaml"))
	require.NoError(t, err)
	require.LessOrEqual(t, len(manifest.Spec.RetrieverEngineType), 50)
	require.Equal(t, generatedRetrieverType(id), manifest.Spec.RetrieverEngineType)
}
