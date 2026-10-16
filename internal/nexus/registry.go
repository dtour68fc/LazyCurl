// Package nexus is LazyCurl's client for NexusCurlServer: the workspace
// registry (which collaborative servers are configured, and which
// projects on each one are currently imported), the gRPC client wrapper
// (Pull/Push/ListProjects), and the translation between on-disk
// Collections/Environments and the wire Resource shape.
package nexus

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dtour68fc/LazyCurl/internal/config"
	"gopkg.in/yaml.v3"
)

// LocalWorkspaceName is the always-present, undeletable workspace backed
// by the normal global Collections/Environments directories - no server
// involved at all. Every registry has exactly one entry with this name.
const LocalWorkspaceName = "local"

// Workspace is one configured server connection: either the special
// "local" entry, or a collaborative one pointing at a NexusCurlServer.
// A collaborative workspace has no single fixed project - Projects is
// the set of that server's projects currently "selected", meaning their
// Collections/Environments have been imported as normal local files
// (tagged with api.CollectionFile.Project/api.EnvironmentFile.Project
// set to the project name) and kept importable/removable over time.
type Workspace struct {
	Name string `yaml:"name"`
	// Server is "host:port" - empty for the local workspace.
	Server string `yaml:"server,omitempty"`
	// Projects is the set of this server's projects currently imported
	// into the global Collections/Environments directories. Unused for
	// the local workspace.
	Projects []string `yaml:"projects,omitempty"`
}

// IsLocal reports whether this is the local (non-synced) workspace.
func (w Workspace) IsLocal() bool {
	return w.Name == LocalWorkspaceName
}

// Registry is the full set of configured workspaces - persisted at
// config.GetNexusWorkspacesPath().
type Registry struct {
	Workspaces []Workspace `yaml:"workspaces"`
}

// Load reads the registry from disk, creating a fresh one (just the
// local workspace) if nothing's been saved yet.
func Load() (*Registry, error) {
	path := config.GetNexusWorkspacesPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultRegistry(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", path, err)
	}
	reg.ensureLocal()
	return &reg, nil
}

func defaultRegistry() *Registry {
	return &Registry{
		Workspaces: []Workspace{{Name: LocalWorkspaceName}},
	}
}

// ensureLocal guarantees the local workspace exists (as the first entry)
// even if a hand-edited or corrupted file is missing it.
func (r *Registry) ensureLocal() {
	for _, w := range r.Workspaces {
		if w.IsLocal() {
			return
		}
	}
	r.Workspaces = append([]Workspace{{Name: LocalWorkspaceName}}, r.Workspaces...)
}

// Save persists the registry to disk.
func (r *Registry) Save() error {
	data, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshaling workspace registry: %w", err)
	}
	path := config.GetNexusWorkspacesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %q: %w", path, err)
	}
	return nil
}

// Get returns the workspace with the given name, if configured.
func (r *Registry) Get(name string) (Workspace, bool) {
	for _, w := range r.Workspaces {
		if w.Name == name {
			return w, true
		}
	}
	return Workspace{}, false
}

// Add registers a new collaborative workspace - just name+server;
// project selection happens afterward via SetProjects. Returns an error
// if the name is already taken (including "local", which is reserved) or
// if the name/server are blank.
func (r *Registry) Add(w Workspace) error {
	if w.Name == "" {
		return fmt.Errorf("workspace name is required")
	}
	if w.IsLocal() {
		return fmt.Errorf("%q is reserved for the built-in local workspace", LocalWorkspaceName)
	}
	if w.Server == "" {
		return fmt.Errorf("server address is required")
	}
	if _, exists := r.Get(w.Name); exists {
		return fmt.Errorf("a workspace named %q already exists", w.Name)
	}
	r.Workspaces = append(r.Workspaces, w)
	return nil
}

// SetProjects replaces the full set of selected/imported projects for a
// configured workspace. The local workspace has no projects and can't be
// edited this way.
func (r *Registry) SetProjects(name string, projects []string) error {
	for i, w := range r.Workspaces {
		if w.Name == name {
			if w.IsLocal() {
				return fmt.Errorf("%q has no projects to change", LocalWorkspaceName)
			}
			r.Workspaces[i].Projects = projects
			return nil
		}
	}
	return fmt.Errorf("no workspace named %q", name)
}

// FindProjectWorkspace returns the first non-local workspace whose
// Projects includes the given project name - used to resolve which
// server a local collection/environment's Project tag maps to, for
// push/pull without the user having to pick a workspace every time.
func (r *Registry) FindProjectWorkspace(project string) (Workspace, bool) {
	if project == "" {
		return Workspace{}, false
	}
	for _, w := range r.Workspaces {
		if w.IsLocal() {
			continue
		}
		for _, p := range w.Projects {
			if p == project {
				return w, true
			}
		}
	}
	return Workspace{}, false
}

// Remove deletes a configured workspace. The local workspace can never
// be removed. Note this only forgets the server connection - it does not
// remove any already-imported local collections/environments tagged with
// that server's projects; use RemoveProjectResources first if that
// cleanup is wanted.
func (r *Registry) Remove(name string) error {
	if name == LocalWorkspaceName {
		return fmt.Errorf("the local workspace can't be deleted")
	}
	idx := -1
	for i, w := range r.Workspaces {
		if w.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("no workspace named %q", name)
	}
	r.Workspaces = append(r.Workspaces[:idx], r.Workspaces[idx+1:]...)
	return nil
}
