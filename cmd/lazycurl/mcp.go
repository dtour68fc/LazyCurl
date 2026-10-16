package main

import (
	"fmt"
	"log"

	"github.com/dtour68fc/LazyCurl/internal/mcpserver"
	"github.com/dtour68fc/LazyCurl/internal/nexus"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// MCPCommand holds parsed arguments for the "mcp" subcommand, which
// starts a stdio MCP server exposing Collection/Environment CRUD and
// bulk tools against one NexusCurlServer project.
type MCPCommand struct {
	Server  string // NexusCurlServer address, host:port
	Project string // Project this MCP server is pinned to
	APIKey  string // Optional API key for the project (opt-in gating)
}

// ParseMCPArgs parses `lazycurl mcp --server HOST:PORT --project NAME [--api-key KEY]`.
func ParseMCPArgs(args []string) (*MCPCommand, error) {
	cmd := &MCPCommand{}

	usage := "usage: lazycurl mcp --server HOST:PORT --project NAME [--api-key KEY]\n\n" +
		"Starts an MCP (Model Context Protocol) server over stdio, exposing\n" +
		"Collection/Environment CRUD and bulk tools against one NexusCurlServer\n" +
		"project, for use by AI coding agents.\n\n" +
		"Options:\n" +
		"  --server HOST:PORT  NexusCurlServer address (required)\n" +
		"  --project NAME      Project to operate on (required)\n" +
		"  --api-key KEY       API key for the project (optional - if omitted,\n" +
		"                      requests are unauthenticated, matching the\n" +
		"                      server's opt-in gating)"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--server requires a value\n\n%s", usage)
			}
			i++
			cmd.Server = args[i]
		case "--project":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--project requires a value\n\n%s", usage)
			}
			i++
			cmd.Project = args[i]
		case "--api-key":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--api-key requires a value\n\n%s", usage)
			}
			i++
			cmd.APIKey = args[i]
		default:
			return nil, fmt.Errorf("unknown option: %s\n\n%s", args[i], usage)
		}
	}

	if cmd.Server == "" || cmd.Project == "" {
		return nil, fmt.Errorf("--server and --project are required\n\n%s", usage)
	}

	return cmd, nil
}

// RunMCPCommand dials the configured NexusCurlServer and serves MCP tool
// calls over stdio until stdin closes.
func RunMCPCommand(cmd *MCPCommand) error {
	if cmd.APIKey == "" {
		log.Printf("warning: no --api-key given; requests to %s will be unauthenticated", cmd.Server)
	}

	client, err := nexus.Dial(cmd.Server)
	if err != nil {
		return fmt.Errorf("dialing %q: %w", cmd.Server, err)
	}
	defer client.Close()

	if cmd.APIKey != "" {
		client.SetAPIKey(cmd.APIKey)
	}

	mcpServer := mcpsrv.NewMCPServer("lazycurl-nexuscurl", version)
	mcpserver.New(client, cmd.Project).Register(mcpServer)

	return mcpsrv.ServeStdio(mcpServer)
}
