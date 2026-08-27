package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type serverManifest struct {
	Type              string            `json:"type"`
	Command           string            `json:"command"`
	Args              []string          `json:"args"`
	URL               string            `json:"url"`
	Headers           map[string]string `json:"headers"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var"`
}

func TestPluginManifestsUseLocalStdio(t *testing.T) {
	t.Parallel()

	paths := []string{
		filepath.Join("..", "..", "plugins", "dataporch", ".mcp.json"),
		filepath.Join("..", "..", "plugins", "dataporch", ".claude-plugin", "plugin.json"),
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", path, err)
			}
			var document struct {
				MCPServers map[string]serverManifest `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatalf("Unmarshal(%q) error = %v", path, err)
			}
			server, ok := document.MCPServers["dataporch"]
			if !ok {
				t.Fatalf("%q has no dataporch MCP server", path)
			}
			if server.Type != "stdio" || server.Command != "dataporch" || !reflect.DeepEqual(server.Args, []string{"mcp"}) {
				t.Fatalf("server = %#v, want dataporch mcp over stdio", server)
			}
			if server.URL != "" || len(server.Headers) != 0 || server.BearerTokenEnvVar != "" {
				t.Fatalf("server contains direct HTTP authentication: %#v", server)
			}
		})
	}
}
