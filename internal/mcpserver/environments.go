package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dtour68fc/LazyCurl/internal/api"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func (s *Server) registerEnvironmentTools(m *mcpsrv.MCPServer) {
	m.AddTool(mcp.NewTool("list_environments",
		mcp.WithDescription("List every Environment in the pinned project, with just its variable keys (not values) - use get_environment for full values."),
	), s.handleListEnvironments)

	m.AddTool(mcp.NewTool("get_environment",
		mcp.WithDescription("Fetch the full structure (including variable values) of one Environment as JSON."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Exact, case-sensitive Environment name.")),
	), s.handleGetEnvironment)

	m.AddTool(mcp.NewTool("create_environment",
		mcp.WithDescription("Create a new Environment in the pinned project. Fails if an Environment with this name already exists."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("description"),
		mcp.WithObject("variables", mcp.Description("Optional map of variable name -> {value, secret?, active?} (active defaults to true if omitted).")),
	), s.handleCreateEnvironment)

	m.AddTool(mcp.NewTool("set_environment_variables",
		mcp.WithDescription("Bulk-set (upsert/merge) one or more variables on an Environment in a single call - creates the Environment first if it doesn't exist yet. Existing variables not mentioned are left untouched (this merges, it does not replace the whole variable map)."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithObject("variables", mcp.Required(), mcp.Description("Map of variable name -> {value, secret?, active?} (active defaults to true if omitted).")),
	), s.handleSetEnvironmentVariables)

	m.AddTool(mcp.NewTool("delete_environment_variable",
		mcp.WithDescription("Delete a single variable from an Environment."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("key", mcp.Required()),
	), s.handleDeleteEnvironmentVariable)
}

type environmentSummary struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	VariableKeys []string `json:"variable_keys"`
}

func (s *Server) handleListEnvironments(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resources, err := s.listEnvironmentResources(ctx)
	if err != nil {
		return errResult(err), nil
	}

	summaries := make([]environmentSummary, 0, len(resources))
	for _, r := range resources {
		var env api.EnvironmentFile
		if err := json.Unmarshal(r.GetContent(), &env); err != nil {
			return errResult(fmt.Errorf("parsing environment %q: %w", r.GetName(), err)), nil
		}
		keys := make([]string, 0, len(env.Variables))
		for k := range env.Variables {
			keys = append(keys, k)
		}
		summaries = append(summaries, environmentSummary{
			Name:         env.Name,
			Description:  env.Description,
			VariableKeys: keys,
		})
	}
	return jsonResult(summaries)
}

func (s *Server) handleGetEnvironment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	env, err := s.pullEnvironment(ctx, name)
	if err != nil {
		return errResult(err), nil
	}
	return jsonResult(env)
}

func (s *Server) handleCreateEnvironment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	description := req.GetString("description", "")

	if _, err := s.pullEnvironment(ctx, name); err == nil {
		return errResult(fmt.Errorf("environment %q already exists", name)), nil
	}

	variables, err := parseVariablesArg(req.GetArguments()["variables"])
	if err != nil {
		return errResult(err), nil
	}
	if variables == nil {
		variables = make(map[string]*api.EnvironmentVariable)
	}

	env := &api.EnvironmentFile{Name: name, Description: description, Project: s.project, Variables: variables}
	if err := s.pushEnvironment(ctx, env); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "name": name})
}

func (s *Server) handleSetEnvironmentVariables(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	rawVariables, ok := req.GetArguments()["variables"]
	if !ok {
		return errResult(fmt.Errorf("variables is required")), nil
	}
	variables, err := parseVariablesArg(rawVariables)
	if err != nil {
		return errResult(err), nil
	}
	if len(variables) == 0 {
		return errResult(fmt.Errorf("variables must contain at least one entry")), nil
	}

	env, err := s.pullEnvironment(ctx, name)
	if err != nil {
		// Doesn't exist yet - create it fresh with just these variables.
		env = &api.EnvironmentFile{Name: name, Project: s.project, Variables: make(map[string]*api.EnvironmentVariable)}
	}
	if env.Variables == nil {
		env.Variables = make(map[string]*api.EnvironmentVariable)
	}

	for k, v := range variables {
		env.Variables[k] = v
	}

	if err := s.pushEnvironment(ctx, env); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "name": name, "updated_keys": variableKeys(variables)})
}

func (s *Server) handleDeleteEnvironmentVariable(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	key, err := req.RequireString("key")
	if err != nil {
		return errResult(err), nil
	}

	env, err := s.pullEnvironment(ctx, name)
	if err != nil {
		return errResult(err), nil
	}
	if _, ok := env.Variables[key]; !ok {
		return errResult(fmt.Errorf("variable %q not found in environment %q", key, name)), nil
	}
	delete(env.Variables, key)

	if err := s.pushEnvironment(ctx, env); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true})
}

// variableInput mirrors api.EnvironmentVariable's JSON shape except
// Active is a pointer, so "omitted" (nil) can be distinguished from
// "explicitly false" - omitted defaults to true per the tool contract.
type variableInput struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	Active *bool  `json:"active"`
}

// parseVariablesArg decodes a tool argument shaped like
// map[string]{value, secret?, active?} into api.EnvironmentVariable
// values, defaulting Active to true when omitted. A nil/absent argument
// returns a nil map, not an error.
func parseVariablesArg(raw any) (map[string]*api.EnvironmentVariable, error) {
	if raw == nil {
		return nil, nil
	}

	var inputs map[string]variableInput
	if err := decodeInto(raw, &inputs); err != nil {
		return nil, fmt.Errorf("parsing variables: %w", err)
	}

	out := make(map[string]*api.EnvironmentVariable, len(inputs))
	for k, v := range inputs {
		active := true
		if v.Active != nil {
			active = *v.Active
		}
		out[k] = &api.EnvironmentVariable{Value: v.Value, Secret: v.Secret, Active: active}
	}
	return out, nil
}

func variableKeys(vars map[string]*api.EnvironmentVariable) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	return keys
}
