// Package nexus is LazyCurl's client for NexusCurlServer: the workspace
// registry (which collaborative servers are configured, which one is
// active), the gRPC client wrapper (Pull/Push/Subscribe), and the
// translation between on-disk Collections/Environments and the wire
// Resource shape.
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

// Workspace is one configured destination: either the special "local"
// entry, or a collaborative one pointing at a NexusCurlServer.
type Workspace struct {
	Name string `yaml:"name"`
	// Server is "host:port" - empty for the local workspace.
	Server string `yaml:"server,omitempty"`
	// Project scopes which Collections/Environments this workspace syncs
	// on that server - a single server can host more than one project.
	Project string `yaml:"project,omitempty"`
}

// IsLocal reports whether this is the local (non-synced) workspace.
func (w Workspace) IsLocal() bool {
	return w.Name == LocalWorkspaceName
}

// Registry is the full set of configured workspaces plus which one is
// currently active - persisted at config.GetNexusWorkspacesPath().
type Registry struct {
	Current    string      `yaml:"current"`
	Workspaces []Workspace `yaml:"workspaces"`
}

// Load reads the registry from disk, creating a fresh one (just the
// local workspace, active) if nothing's been saved yet.
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
		Current:    LocalWorkspaceName,
		Workspaces: []Workspace{{Name: LocalWorkspaceName}},
	}
}

// ensureLocal guarantees the local workspace exists (as the first entry)
// even if a hand-edited or corrupted file is missing it, and that
// Current always names something that actually exists.
func (r *Registry) ensureLocal() {
	hasLocal := false
	for _, w := range r.Workspaces {
		if w.IsLocal() {
			hasLocal = true
			break
		}
	}
	if !hasLocal {
		r.Workspaces = append([]Workspace{{Name: LocalWorkspaceName}}, r.Workspaces...)
	}
	if r.Current == "" {
		r.Current = LocalWorkspaceName
	}
	if _, ok := r.Get(r.Current); !ok {
		r.Current = LocalWorkspaceName
	}
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

// CurrentWorkspace returns the active workspace (falling back to local
// if Current somehow names something that no longer exists).
func (r *Registry) CurrentWorkspace() Workspace {
	if w, ok := r.Get(r.Current); ok {
		return w
	}
	return Workspace{Name: LocalWorkspaceName}
}

// Add registers a new collaborative workspace. Returns an error if the
// name is already taken (including "local", which is reserved) or if
// server/project are blank.
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
	if w.Project == "" {
		return fmt.Errorf("project name is required")
	}
	if _, exists := r.Get(w.Name); exists {
		return fmt.Errorf("a workspace named %q already exists", w.Name)
	}
	r.Workspaces = append(r.Workspaces, w)
	return nil
}

// Remove deletes a configured workspace. The local workspace can never
// be removed. If the removed workspace was current, Current reverts to
// local.
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
	if r.Current == name {
		r.Current = LocalWorkspaceName
	}
	return nil
}

// SetCurrent switches the active workspace. Returns an error if name
// isn't configured.
func (r *Registry) SetCurrent(name string) error {
	if _, ok := r.Get(name); !ok {
		return fmt.Errorf("no workspace named %q", name)
	}
	r.Current = name
	return nil
}
