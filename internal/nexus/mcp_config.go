package nexus

import (
	"encoding/json"
	"fmt"
	"os"
)

// MCPConfigEntry is one server entry under the "mcpServers" object of a
// .mcp.json file - the de-facto standard most MCP-aware CLIs and agents
// (Claude Code, Cursor, and others) already read for project-scoped
// server config, so no bespoke format/loader is needed on the agent side.
type MCPConfigEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// WriteMCPConfig merges entry into the "mcpServers" object of the
// .mcp.json file at path under key name, creating the file (and its
// mcpServers object) if it doesn't exist yet. Any other entries or
// top-level keys already in the file are preserved untouched - repeated
// calls for different projects accumulate into the same file instead of
// clobbering each other.
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
	return os.WriteFile(path, out, 0644)
}
