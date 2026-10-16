package nexus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMCPConfig_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	if err := WriteMCPConfig(path, "nexuscurl-PMC", MCPConfigEntry{
		Command: "lazycurl",
		Args:    []string{"mcp", "--server", "10.10.1.107:29500", "--project", "PMC", "--api-key", "ncmcp_abc"},
	}); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}

	var doc struct {
		MCPServers map[string]MCPConfigEntry `json:"mcpServers"`
	}
	data := readFile(t, path)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entry, ok := doc.MCPServers["nexuscurl-PMC"]
	if !ok {
		t.Fatalf("missing nexuscurl-PMC entry, got %+v", doc.MCPServers)
	}
	if entry.Command != "lazycurl" || len(entry.Args) != 7 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestWriteMCPConfig_MergesWithoutClobbering(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	if err := WriteMCPConfig(path, "nexuscurl-PMC", MCPConfigEntry{Command: "lazycurl", Args: []string{"mcp", "--project", "PMC"}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteMCPConfig(path, "nexuscurl-PMO", MCPConfigEntry{Command: "lazycurl", Args: []string{"mcp", "--project", "PMO"}}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	var doc struct {
		MCPServers map[string]MCPConfigEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(readFile(t, path), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.MCPServers) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(doc.MCPServers), doc.MCPServers)
	}
	if _, ok := doc.MCPServers["nexuscurl-PMC"]; !ok {
		t.Fatalf("first entry was clobbered")
	}
	if _, ok := doc.MCPServers["nexuscurl-PMO"]; !ok {
		t.Fatalf("second entry missing")
	}
}

func TestWriteMCPConfig_OverwritesSameEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	if err := WriteMCPConfig(path, "nexuscurl-PMC", MCPConfigEntry{Command: "lazycurl", Args: []string{"mcp", "--project", "PMC"}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteMCPConfig(path, "nexuscurl-PMC", MCPConfigEntry{Command: "lazycurl", Args: []string{"mcp", "--project", "PMC", "--api-key", "ncmcp_new"}}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	var doc struct {
		MCPServers map[string]MCPConfigEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(readFile(t, path), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.MCPServers) != 1 {
		t.Fatalf("expected 1 entry (overwritten), got %d: %+v", len(doc.MCPServers), doc.MCPServers)
	}
	entry := doc.MCPServers["nexuscurl-PMC"]
	if len(entry.Args) != 5 {
		t.Fatalf("expected updated args, got %+v", entry.Args)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}
