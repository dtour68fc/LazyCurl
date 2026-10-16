package nexus

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// MCPConfigEntry is one server entry under the "mcpServers" object of a
// .mcp.json file - the de-facto standard most MCP-aware CLIs and agents
// (Claude Code, Cursor, and others) already read for project-scoped
// server config, so no bespoke format/loader is needed on the agent side.
type MCPConfigEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// WriteMCPConfig merges entry into the "mcpServers" object of the
// .mcp.json file at path under key name, creating the file (and its
// mcpServers object) if it doesn't exist yet. Any other entries or
// top-level keys already in the file are preserved untouched - repeated
// calls for different projects accumulate into the same file instead of
// clobbering each other.
//
// .mcp.json commonly embeds a live API key (see entry.Env's
// NEXUSCURL_API_KEY, set by completeMCPSetup) so it's written 0600, not
// the default-ish 0644, matching the server-side secrets file
// convention (internal/auth.AppendEntry on the NexusCurlServer side).
func WriteMCPConfig(path, name string, entry MCPConfigEntry) error {
	doc := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("parsing existing %s: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	servers := map[string]MCPConfigEntry{}
	if raw, ok := doc["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("parsing mcpServers in %s: %w", path, err)
		}
	}
	servers[name] = entry

	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	doc["mcpServers"] = serversJSON

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o600)
}

// EnsureGitignored appends name to the .gitignore file in dir (creating
// it if it doesn't exist) unless an entry for it is already present.
// Used so a generated .mcp.json - which embeds a live API key - doesn't
// end up committed to the target project's repo just because the user
// never thought to exclude it. A best-effort nicety: failures are
// returned for the caller to decide whether to surface, but are never
// fatal to the .mcp.json write itself.
func EnsureGitignored(dir, name string) error {
	path := dir + "/.gitignore"
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == name {
			return nil // already ignored
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + name + "\n")
	return err
}
