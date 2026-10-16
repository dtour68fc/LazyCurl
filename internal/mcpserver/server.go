// Package mcpserver exposes an MCP (Model Context Protocol) tool server,
// over stdio, that lets AI coding agents create/list/update a single
// NexusCurlServer project's Collections (folders + requests) and
// Environments (variables), including bulk/batch operations meant for
// migrating large Postman exports or mass-creating gRPC requests (which
// Postman can't natively export).
//
// Every tool here operates implicitly on the single project this server
// was started pinned to (see New) - there is no "project" argument on
// any tool, matching the one-API-key-per-project-per-person model used
// by NexusCurlServer's auth.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dtour68fc/LazyCurl/internal/api"
	"github.com/dtour68fc/LazyCurl/internal/nexus"
	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// Server implements the MCP tool handlers against one NexusCurlServer
// project.
type Server struct {
	client  *nexus.Client
	project string
}

// New returns a Server pinned to one project on the given (already
// dialed) NexusCurlServer client.
func New(client *nexus.Client, project string) *Server {
	return &Server{client: client, project: project}
}

// Register adds every tool this package implements onto m.
func (s *Server) Register(m *mcpsrv.MCPServer) {
	s.registerCollectionTools(m)
	s.registerEnvironmentTools(m)
}

// pullCollection fetches the project's current resources and returns the
// parsed Collection named name, plus the raw wire Resource it came from.
func (s *Server) pullCollection(ctx context.Context, name string) (*api.CollectionFile, *nexuscurlv1.Resource, error) {
	resources, err := s.client.Pull(ctx, s.project)
	if err != nil {
		return nil, nil, fmt.Errorf("pulling project %q: %w", s.project, err)
	}
	for _, r := range resources {
		if r.GetType() != nexus.ResourceTypeCollection || r.GetName() != name {
			continue
		}
		var col api.CollectionFile
		if err := json.Unmarshal(r.GetContent(), &col); err != nil {
			return nil, nil, fmt.Errorf("parsing collection %q: %w", name, err)
		}
		return &col, r, nil
	}
	return nil, nil, fmt.Errorf("collection %q not found", name)
}

// pushCollection serializes col to the wire Resource shape and pushes it.
func (s *Server) pushCollection(ctx context.Context, col *api.CollectionFile) error {
	res, err := nexus.ResourceFromCollection(col)
	if err != nil {
		return err
	}
	if _, err := s.client.Push(ctx, s.project, res); err != nil {
		return fmt.Errorf("pushing collection %q: %w", col.Name, err)
	}
	return nil
}

// pullEnvironment fetches the project's current resources and returns the
// parsed Environment named name.
func (s *Server) pullEnvironment(ctx context.Context, name string) (*api.EnvironmentFile, error) {
	resources, err := s.client.Pull(ctx, s.project)
	if err != nil {
		return nil, fmt.Errorf("pulling project %q: %w", s.project, err)
	}
	for _, r := range resources {
		if r.GetType() != nexus.ResourceTypeEnvironment || r.GetName() != name {
			continue
		}
		var env api.EnvironmentFile
		if err := json.Unmarshal(r.GetContent(), &env); err != nil {
			return nil, fmt.Errorf("parsing environment %q: %w", name, err)
		}
		return &env, nil
	}
	return nil, fmt.Errorf("environment %q not found", name)
}

// pushEnvironment serializes env to the wire Resource shape and pushes it.
func (s *Server) pushEnvironment(ctx context.Context, env *api.EnvironmentFile) error {
	res, err := nexus.ResourceFromEnvironment(env)
	if err != nil {
		return err
	}
	if _, err := s.client.Push(ctx, s.project, res); err != nil {
		return fmt.Errorf("pushing environment %q: %w", env.Name, err)
	}
	return nil
}

// listCollectionResources returns every "collection"-typed Resource
// currently stored for the project.
func (s *Server) listCollectionResources(ctx context.Context) ([]*nexuscurlv1.Resource, error) {
	resources, err := s.client.Pull(ctx, s.project)
	if err != nil {
		return nil, fmt.Errorf("pulling project %q: %w", s.project, err)
	}
	var out []*nexuscurlv1.Resource
	for _, r := range resources {
		if r.GetType() == nexus.ResourceTypeCollection {
			out = append(out, r)
		}
	}
	return out, nil
}

// listEnvironmentResources returns every "environment"-typed Resource
// currently stored for the project.
func (s *Server) listEnvironmentResources(ctx context.Context) ([]*nexuscurlv1.Resource, error) {
	resources, err := s.client.Pull(ctx, s.project)
	if err != nil {
		return nil, fmt.Errorf("pulling project %q: %w", s.project, err)
	}
	var out []*nexuscurlv1.Resource
	for _, r := range resources {
		if r.GetType() == nexus.ResourceTypeEnvironment {
			out = append(out, r)
		}
	}
	return out, nil
}

// navigateFolders walks col's folder tree along path (outermost to
// innermost folder name, case-sensitive exact match), returning pointers
// to the Folders/Requests slices at that level so callers can append to
// them in place. An empty path returns col's own top-level slices
// directly. If create is true, missing folders along the way are
// appended as they're encountered; if false, a missing folder at any
// level returns a "folder path ... not found" error.
func navigateFolders(col *api.CollectionFile, path []string, create bool) (*[]api.Folder, *[]api.CollectionRequest, error) {
	if len(path) == 0 {
		return &col.Folders, &col.Requests, nil
	}
	return navigateFolderSlice(&col.Folders, path, create)
}

func navigateFolderSlice(folders *[]api.Folder, path []string, create bool) (*[]api.Folder, *[]api.CollectionRequest, error) {
	name, rest := path[0], path[1:]

	for i := range *folders {
		if (*folders)[i].Name != name {
			continue
		}
		if len(rest) == 0 {
			return &(*folders)[i].Folders, &(*folders)[i].Requests, nil
		}
		return navigateFolderSlice(&(*folders)[i].Folders, rest, create)
	}

	if !create {
		return nil, nil, fmt.Errorf("folder path %q not found", strings.Join(path, "/"))
	}

	*folders = append(*folders, api.Folder{Name: name})
	idx := len(*folders) - 1
	if len(rest) == 0 {
		return &(*folders)[idx].Folders, &(*folders)[idx].Requests, nil
	}
	return navigateFolderSlice(&(*folders)[idx].Folders, rest, create)
}

// findRequestByID searches col's entire folder tree (root requests plus
// every nested folder's requests) for a CollectionRequest with the given
// ID, returning a pointer so the caller can update it in place.
func findRequestByID(col *api.CollectionFile, id string) (*api.CollectionRequest, bool) {
	if r, ok := findRequestInSlice(&col.Requests, id); ok {
		return r, true
	}
	return findRequestInFolders(&col.Folders, id)
}

func findRequestInSlice(reqs *[]api.CollectionRequest, id string) (*api.CollectionRequest, bool) {
	for i := range *reqs {
		if (*reqs)[i].ID == id {
			return &(*reqs)[i], true
		}
	}
	return nil, false
}

func findRequestInFolders(folders *[]api.Folder, id string) (*api.CollectionRequest, bool) {
	for i := range *folders {
		if r, ok := findRequestInSlice(&(*folders)[i].Requests, id); ok {
			return r, true
		}
		if r, ok := findRequestInFolders(&(*folders)[i].Folders, id); ok {
			return r, true
		}
	}
	return nil, false
}

// deleteRequestByID removes the CollectionRequest with the given ID from
// anywhere in col's folder tree, reporting whether it was found.
func deleteRequestByID(col *api.CollectionFile, id string) bool {
	if deleteRequestFromSlice(&col.Requests, id) {
		return true
	}
	return deleteRequestFromFolders(&col.Folders, id)
}

func deleteRequestFromSlice(reqs *[]api.CollectionRequest, id string) bool {
	for i := range *reqs {
		if (*reqs)[i].ID == id {
			*reqs = append((*reqs)[:i], (*reqs)[i+1:]...)
			return true
		}
	}
	return false
}

func deleteRequestFromFolders(folders *[]api.Folder, id string) bool {
	for i := range *folders {
		if deleteRequestFromSlice(&(*folders)[i].Requests, id) {
			return true
		}
		if deleteRequestFromFolders(&(*folders)[i].Folders, id) {
			return true
		}
	}
	return false
}

// countFolders recursively counts every folder in the tree (not just the
// top level).
func countFolders(folders []api.Folder) int {
	count := len(folders)
	for _, f := range folders {
		count += countFolders(f.Folders)
	}
	return count
}

// countRequests recursively counts every request in the tree, starting
// from a level's own requests plus every nested folder's requests.
func countRequests(requests []api.CollectionRequest, folders []api.Folder) int {
	count := len(requests)
	for _, f := range folders {
		count += countRequests(f.Requests, f.Folders)
	}
	return count
}

// findFolderAtLevel reports whether a folder literally named name
// already exists directly within folders (not recursively) - used by
// create_folder/bulk_create to avoid silently duplicating a folder at
// the same level.
func findFolderAtLevel(folders []api.Folder, name string) bool {
	for _, f := range folders {
		if f.Name == name {
			return true
		}
	}
	return false
}

// decodeInto marshals an arbitrary MCP tool argument value (already
// decoded from JSON by the SDK as map[string]any/[]any/etc.) back to
// JSON and unmarshals it into target - the simplest way to turn a loosely
// typed tool argument into one of our concrete api.* structs.
func decodeInto(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("re-marshaling argument: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decoding argument: %w", err)
	}
	return nil
}

func errResult(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(fmt.Errorf("marshaling result: %w", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
