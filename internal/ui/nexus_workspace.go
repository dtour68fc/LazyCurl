package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dtour68fc/LazyCurl/internal/api"
	"github.com/dtour68fc/LazyCurl/internal/config"
	"github.com/dtour68fc/LazyCurl/internal/nexus"
	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
)

// pushSelectedResource pushes whichever collection or environment is
// currently selected/focused (depending on which left-panel tab is
// active) up to the server whose workspace has its Project selected.
// If the resource has no Project tag, or no configured workspace has
// that project selected yet, falls back to the link-then-retry flow
// (openImportWorkspacePicker) so the user can pick a workspace+project
// once and have it remembered for next time.
func (m Model) pushSelectedResource() (tea.Model, tea.Cmd) {
	switch m.leftPanel.GetActiveTab() {
	case CollectionsTab:
		selected := m.leftPanel.GetCollections().Selected()
		if selected == nil {
			m.statusBar.Info("Nothing selected to push")
			return m, nil
		}
		coll := m.leftPanel.GetCollections().FindCollectionByNode(selected)
		if coll == nil {
			m.statusBar.Info("Select a collection (or something inside one) to push")
			return m, nil
		}
		res, err := nexus.ResourceFromCollection(coll)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		ws, ok := m.nexusRegistry.FindProjectWorkspace(coll.Project)
		if !ok {
			return m.beginLinkFlow(res, coll.Name, false, coll, nil)
		}
		return m.pushResource(ws, coll.Project, res, coll.Name)

	case EnvironmentsTab:
		node := m.leftPanel.GetEnvironments().getCurrentNode()
		env := m.leftPanel.GetEnvironments().getEnvForNode(node)
		if env == nil {
			m.statusBar.Info("Select an environment to push")
			return m, nil
		}
		res, err := nexus.ResourceFromEnvironment(env)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		ws, ok := m.nexusRegistry.FindProjectWorkspace(env.Project)
		if !ok {
			return m.beginLinkFlow(res, env.Name, false, nil, env)
		}
		return m.pushResource(ws, env.Project, res, env.Name)
	}

	return m, nil
}

// pushResource dials ws's server and pushes res under project, reporting
// success/error on the status bar.
func (m Model) pushResource(ws nexus.Workspace, project string, res *nexuscurlv1.Resource, label string) (tea.Model, tea.Cmd) {
	client, err := nexus.Dial(ws.Server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", ws.Server, err))
		return m, nil
	}
	defer client.Close()

	pushed, err := client.Push(context.Background(), project, res)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("pushing %q: %w", label, err))
		return m, nil
	}
	m.statusBar.Success("Push", fmt.Sprintf("%s -> %s/%s (rev %d)", label, ws.Name, project, pushed.GetRevision()))
	return m, nil
}

// pullSelectedResource re-pulls just the currently selected collection or
// environment from the server whose workspace has its Project selected,
// overwriting only that one local file. Falls back to the
// link-then-retry flow when the resource's Project can't be resolved to
// a workspace, same as pushSelectedResource.
func (m Model) pullSelectedResource() (tea.Model, tea.Cmd) {
	switch m.leftPanel.GetActiveTab() {
	case CollectionsTab:
		selected := m.leftPanel.GetCollections().Selected()
		if selected == nil {
			m.statusBar.Info("Nothing selected to pull")
			return m, nil
		}
		coll := m.leftPanel.GetCollections().FindCollectionByNode(selected)
		if coll == nil {
			m.statusBar.Info("Select a collection (or something inside one) to pull")
			return m, nil
		}
		res, err := nexus.ResourceFromCollection(coll)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		ws, ok := m.nexusRegistry.FindProjectWorkspace(coll.Project)
		if !ok {
			return m.beginLinkFlow(res, coll.Name, true, coll, nil)
		}
		return m.pullResource(ws, coll.Project, res, coll.Name)

	case EnvironmentsTab:
		node := m.leftPanel.GetEnvironments().getCurrentNode()
		env := m.leftPanel.GetEnvironments().getEnvForNode(node)
		if env == nil {
			m.statusBar.Info("Select an environment to pull")
			return m, nil
		}
		res, err := nexus.ResourceFromEnvironment(env)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		ws, ok := m.nexusRegistry.FindProjectWorkspace(env.Project)
		if !ok {
			return m.beginLinkFlow(res, env.Name, true, nil, env)
		}
		return m.pullResource(ws, env.Project, res, env.Name)
	}

	return m, nil
}

// pullResource dials ws's server, pulls project, and overwrites just the
// single local file matching res's Type+Name (not the whole project).
func (m Model) pullResource(ws nexus.Workspace, project string, res *nexuscurlv1.Resource, label string) (tea.Model, tea.Cmd) {
	client, err := nexus.Dial(ws.Server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", ws.Server, err))
		return m, nil
	}
	defer client.Close()

	resources, err := client.Pull(context.Background(), project)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("pulling %q: %w", label, err))
		return m, nil
	}

	match := findMatchingResource(resources, res)
	if match == nil {
		m.statusBar.Info(fmt.Sprintf("%s not found in %s/%s", label, ws.Name, project))
		return m, nil
	}

	if _, err := nexus.ImportResourcesToGlobal(
		config.GetGlobalCollectionsPath(), config.GetGlobalEnvironmentsPath(), []*nexuscurlv1.Resource{match},
	); err != nil {
		m.statusBar.Error(err)
		return m, nil
	}
	m.reloadNexusPanels()
	m.statusBar.Success("Pull", fmt.Sprintf("%s <- %s/%s", label, ws.Name, project))
	return m, nil
}

// findMatchingResource returns the resource in resources with the same
// Type+Name as want, or nil if none matches.
func findMatchingResource(resources []*nexuscurlv1.Resource, want *nexuscurlv1.Resource) *nexuscurlv1.Resource {
	for _, r := range resources {
		if r.GetType() == want.GetType() && r.GetName() == want.GetName() {
			return r
		}
	}
	return nil
}

// reloadNexusPanels refreshes the Collections/Environments panels from
// disk - called after anything imports/removes files directly, since
// those panels otherwise only reload on their own navigation.
func (m Model) reloadNexusPanels() {
	m.leftPanel.GetCollections().ReloadCollections()
	m.leftPanel.GetEnvironments().ReloadEnvironments()
}

// beginLinkFlow opens the workspace list in import/link mode for a
// resource whose Project couldn't be resolved to a configured workspace
// (or that the user explicitly wants to [re]link via "I"). Once a target
// workspace and project name are chosen, completeImport finishes the
// push (or pull) and remembers the mapping for next time.
func (m Model) beginLinkFlow(res *nexuscurlv1.Resource, label string, isPull bool, coll *api.CollectionFile, env *api.EnvironmentFile) (tea.Model, tea.Cmd) {
	var targets []nexus.Workspace
	for _, w := range m.nexusRegistry.Workspaces {
		if !w.IsLocal() {
			targets = append(targets, w)
		}
	}
	if len(targets) == 0 {
		m.statusBar.Info("No collaborative workspaces configured yet - press W to add one")
		return m, nil
	}

	m.pendingLinkResource = res
	m.pendingLinkLabel = label
	m.pendingLinkIsPull = isPull
	m.pendingLinkColl = coll
	m.pendingLinkEnv = env
	m.workspaceModal.ShowForImport(targets)
	return m, nil
}

// openImportWorkspacePicker captures the currently-selected collection or
// environment (whichever left-panel tab is active) and opens the
// workspace+project link flow for it unconditionally - the explicit "I"
// command, independent of whether its Project already resolves to a
// workspace.
func (m Model) openImportWorkspacePicker() (tea.Model, tea.Cmd) {
	switch m.leftPanel.GetActiveTab() {
	case CollectionsTab:
		selected := m.leftPanel.GetCollections().Selected()
		if selected == nil {
			m.statusBar.Info("Nothing selected to import")
			return m, nil
		}
		coll := m.leftPanel.GetCollections().FindCollectionByNode(selected)
		if coll == nil {
			m.statusBar.Info("Select a collection (or something inside one) to import")
			return m, nil
		}
		res, err := nexus.ResourceFromCollection(coll)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		return m.beginLinkFlow(res, coll.Name, false, coll, nil)

	case EnvironmentsTab:
		node := m.leftPanel.GetEnvironments().getCurrentNode()
		env := m.leftPanel.GetEnvironments().getEnvForNode(node)
		if env == nil {
			m.statusBar.Info("Select an environment to import")
			return m, nil
		}
		res, err := nexus.ResourceFromEnvironment(env)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		return m.beginLinkFlow(res, env.Name, false, nil, env)
	}

	return m, nil
}

// completeImport finishes the link-then-retry flow once a target
// workspace and project name have both been chosen (target.Projects[0]
// carries the project, per WorkspaceModalImportTarget's doc comment). It
// pushes (or pulls) the pending resource under that project, registers
// the project into the target workspace's Projects if it wasn't already
// there, and tags the source collection/environment with that project if
// it didn't already have one.
func (m Model) completeImport(target nexus.Workspace) (tea.Model, tea.Cmd) {
	res := m.pendingLinkResource
	label := m.pendingLinkLabel
	isPull := m.pendingLinkIsPull
	coll := m.pendingLinkColl
	env := m.pendingLinkEnv
	m.pendingLinkResource = nil
	m.pendingLinkLabel = ""
	m.pendingLinkColl = nil
	m.pendingLinkEnv = nil

	if res == nil || len(target.Projects) == 0 {
		return m, nil
	}
	project := target.Projects[0]
	if project == "" {
		m.statusBar.Info("Project name is required")
		return m, nil
	}

	client, err := nexus.Dial(target.Server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", target.Server, err))
		return m, nil
	}
	defer client.Close()

	if isPull {
		resources, err := client.Pull(context.Background(), project)
		if err != nil {
			m.statusBar.Error(fmt.Errorf("pulling %q: %w", label, err))
			return m, nil
		}
		if match := findMatchingResource(resources, res); match != nil {
			if _, err := nexus.ImportResourcesToGlobal(
				config.GetGlobalCollectionsPath(), config.GetGlobalEnvironmentsPath(), []*nexuscurlv1.Resource{match},
			); err != nil {
				m.statusBar.Error(err)
				return m, nil
			}
			m.reloadNexusPanels()
			m.statusBar.Success("Pull", fmt.Sprintf("%s <- %s/%s", label, target.Name, project))
		} else {
			// Nothing upstream yet under this brand-new project - still
			// finish linking it (below) so the next push/pull just works,
			// but there's genuinely nothing to apply locally right now.
			m.statusBar.Info(fmt.Sprintf("Nothing to pull yet for %q in %s/%s", label, target.Name, project))
		}
	} else {
		pushed, err := client.Push(context.Background(), project, res)
		if err != nil {
			m.statusBar.Error(fmt.Errorf("pushing %q: %w", label, err))
			return m, nil
		}
		m.statusBar.Success("Push", fmt.Sprintf("%s -> %s/%s (rev %d)", label, target.Name, project, pushed.GetRevision()))
	}

	m.linkProjectToWorkspace(target.Name, project)

	if coll != nil && coll.Project == "" {
		coll.Project = project
		if err := coll.Save(); err != nil {
			m.statusBar.Error(err)
		}
	}
	if env != nil && env.Project == "" {
		env.Project = project
		if err := api.SaveEnvironment(env, env.FilePath); err != nil {
			m.statusBar.Error(err)
		}
	}

	return m, nil
}

// linkProjectToWorkspace adds project to the named workspace's Projects
// (if it isn't already there) and persists the registry.
func (m Model) linkProjectToWorkspace(workspaceName, project string) {
	ws, ok := m.nexusRegistry.Get(workspaceName)
	if !ok {
		return
	}
	for _, p := range ws.Projects {
		if p == project {
			return // already selected
		}
	}
	projects := append(append([]string{}, ws.Projects...), project)
	if err := m.nexusRegistry.SetProjects(workspaceName, projects); err != nil {
		m.statusBar.Error(err)
		return
	}
	if err := m.nexusRegistry.Save(); err != nil {
		m.statusBar.Error(err)
	}
}

// handleAddWorkspace registers a brand-new workspace (name+server only,
// no projects yet) and returns to the workspace list.
func (m Model) handleAddWorkspace(ws nexus.Workspace) (tea.Model, tea.Cmd) {
	if err := m.nexusRegistry.Add(ws); err != nil {
		m.statusBar.Error(err)
	} else if err := m.nexusRegistry.Save(); err != nil {
		m.statusBar.Error(err)
	} else {
		m.statusBar.Success("Workspace", "added "+ws.Name)
	}
	m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces)
	return m, nil
}

// openProjectChecklistFor dials ws's server, fetches its current project
// list, and shows the multi-select checklist seeded from both that list
// and whatever's already selected. Shows an error and bounces back to the
// list if the server can't be reached - this blocks the UI briefly (same
// synchronous-dial tradeoff as pushResource/pullResource) rather than
// threading a tea.Cmd through, to keep error handling in one place.
func (m Model) openProjectChecklistFor(ws nexus.Workspace) {
	client, err := nexus.Dial(ws.Server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", ws.Server, err))
		m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces)
		return
	}
	defer client.Close()

	projects, err := client.ListProjects(context.Background())
	if err != nil {
		m.statusBar.Error(fmt.Errorf("listing projects on %s: %w", ws.Server, err))
		m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces)
		return
	}

	m.workspaceModal.ShowProjectChecklist(ws.Name, projects, ws.Projects)
}

// applyProjectChecklist diffs finalProjects against the workspace's
// current Projects: newly-checked projects are pulled and imported into
// the global Collections/Environments directories, newly-unchecked ones
// have their previously-imported local files removed. Then persists the
// new Projects set and reloads both panels.
func (m Model) applyProjectChecklist(workspaceName string, finalProjects []string) (tea.Model, tea.Cmd) {
	ws, ok := m.nexusRegistry.Get(workspaceName)
	if !ok {
		return m, nil
	}

	wasSelected := make(map[string]bool, len(ws.Projects))
	for _, p := range ws.Projects {
		wasSelected[p] = true
	}
	isSelected := make(map[string]bool, len(finalProjects))
	for _, p := range finalProjects {
		isSelected[p] = true
	}

	var added, removed []string
	for _, p := range finalProjects {
		if !wasSelected[p] {
			added = append(added, p)
		}
	}
	for _, p := range ws.Projects {
		if !isSelected[p] {
			removed = append(removed, p)
		}
	}

	collectionsDir := config.GetGlobalCollectionsPath()
	environmentsDir := config.GetGlobalEnvironmentsPath()

	var client *nexus.Client
	if len(added) > 0 {
		c, err := nexus.Dial(ws.Server)
		if err != nil {
			m.statusBar.Error(fmt.Errorf("connecting to %s: %w", ws.Server, err))
			m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces)
			return m, nil
		}
		client = c
		defer client.Close()
	}

	importedTotal, removedTotal := 0, 0
	for _, project := range added {
		resources, err := client.Pull(context.Background(), project)
		if err != nil {
			m.statusBar.Error(fmt.Errorf("pulling %q from %s: %w", project, ws.Name, err))
			continue
		}
		n, err := nexus.ImportResourcesToGlobal(collectionsDir, environmentsDir, resources)
		if err != nil {
			m.statusBar.Error(err)
			continue
		}
		importedTotal += n
	}
	for _, project := range removed {
		n, err := nexus.RemoveProjectResources(collectionsDir, environmentsDir, project)
		if err != nil {
			m.statusBar.Error(err)
			continue
		}
		removedTotal += n
	}

	if err := m.nexusRegistry.SetProjects(workspaceName, finalProjects); err != nil {
		m.statusBar.Error(err)
	} else if err := m.nexusRegistry.Save(); err != nil {
		m.statusBar.Error(err)
	}

	m.reloadNexusPanels()
	m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces)
	m.statusBar.Success("Workspace", fmt.Sprintf("%s: imported %d, removed %d", ws.Name, importedTotal, removedTotal))

	return m, nil
}
