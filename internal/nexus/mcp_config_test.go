package nexus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteMCPConfig_WritesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	if err := WriteMCPConfig(path, "nexuscurl-PMC", MCPConfigEntry{
		Command: "lazycurl",
		Args:    []string{"mcp", "--project", "PMC"},
		Env:     map[string]string{"NEXUSCURL_API_KEY": "ncmcp_abc"},
	}); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 permissions (file holds a live api key), got %o", perm)
	}
}

func TestEnsureGitignored_CreatesFileWithEntry(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureGitignored(dir, ".mcp.json"); err != nil {
		t.Fatalf("EnsureGitignored: %v", err)
	}

	data := readFile(t, filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), ".mcp.json") {
		t.Fatalf(".gitignore missing .mcp.json entry, got %q", data)
	}
}

func TestEnsureGitignored_AppendsToExistingFileWithoutDuplicating(t *testing.T) {
	dir := t.TempDir()
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}

	if err := EnsureGitignored(dir, ".mcp.json"); err != nil {
		t.Fatalf("first EnsureGitignored: %v", err)
	}
	if err := EnsureGitignored(dir, ".mcp.json"); err != nil {
		t.Fatalf("second EnsureGitignored: %v", err)
	}

	data := string(readFile(t, gitignorePath))
	if !strings.Contains(data, "node_modules") {
		t.Fatalf("existing entry was clobbered, got %q", data)
	}
	if n := strings.Count(data, ".mcp.json"); n != 1 {
		t.Fatalf("expected exactly 1 .mcp.json entry, got %d in %q", n, data)
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
