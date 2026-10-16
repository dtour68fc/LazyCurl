package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dtour68fc/LazyCurl/internal/config"
	"github.com/dtour68fc/LazyCurl/internal/nexus"
	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
)

// nexusNotifyMsg is delivered when the active collaborative workspace's
// Subscribe stream reports a change upstream - it only flips the
// "pending pull available" indicator, it never applies anything itself
// (manual pull/push only, per the design: last-write-wins makes
// auto-apply too risky for concurrent local edits).
type nexusNotifyMsg struct{}

// nexusGiveUpMsg is delivered when WatchWithReconnect exhausts its
// retries - the caller auto-reverts to the local workspace and shows an
// informational modal rather than silently retrying forever.
type nexusGiveUpMsg struct{ err error }

// waitForNexusEvent blocks on the model's nexus event channel and
// surfaces whatever comes out of it as a tea.Msg - standard bubbletea
// "listen on a channel fed by a background goroutine" pattern. Must be
// re-queued by the caller after handling each message to keep listening.
func (m Model) waitForNexusEvent() tea.Cmd {
	ch := m.nexusEvents
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}

// switchNexusWorkspace tears down any active collaborative connection,
// switches the registry to the named workspace, and - for a
// collaborative target - dials the server, pulls the current snapshot
// into that workspace's cache dir, redirects the Collections/
// Environments panels at it, and starts the Subscribe watch. Switching
// to local just redirects the panels back to the normal global
// directories; local files are never touched by any of this.
func (m Model) switchNexusWorkspace(name string) (tea.Model, tea.Cmd) {
	if m.nexusCancelWatch != nil {
		m.nexusCancelWatch()
		m.nexusCancelWatch = nil
	}
	if m.nexusClient != nil {
		_ = m.nexusClient.Close()
		m.nexusClient = nil
	}
	m.nexusEvents = nil
	m.nexusPending = false

	if err := m.nexusRegistry.SetCurrent(name); err != nil {
		m.statusBar.Error(err)
		return m, nil
	}
	if err := m.nexusRegistry.Save(); err != nil {
		m.statusBar.Error(err)
	}

	ws := m.nexusRegistry.CurrentWorkspace()
	m.statusBar.SetWorkspace(ws.Name)

	if ws.IsLocal() {
		m.leftPanel.GetCollections().SetCollectionsPath(config.GetGlobalCollectionsPath())
		m.leftPanel.GetEnvironments().SetEnvironmentsPath(config.GetGlobalEnvironmentsPath())
		m.statusBar.Success("Workspace", "switched to local")
		return m, nil
	}

	client, err := nexus.Dial(ws.Server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", ws.Server, err))
		return m, nil
	}
	m.nexusClient = client

	resources, err := client.Pull(context.Background(), ws.Project)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("pulling %q from %s: %w", ws.Project, ws.Server, err))
		return m, nil
	}

	cacheDir := config.GetNexusCacheDir(ws.Name)
	if err := nexus.WriteResourcesToCache(cacheDir, resources); err != nil {
		m.statusBar.Error(err)
		return m, nil
	}
	m.leftPanel.GetCollections().SetCollectionsPath(nexus.CacheCollectionsDir(cacheDir))
	m.leftPanel.GetEnvironments().SetEnvironmentsPath(nexus.CacheEnvironmentsDir(cacheDir))
	m.statusBar.Success("Workspace", "switched to "+ws.Name+" ("+fmt.Sprintf("%d", len(resources))+" items)")

	events := make(chan interface{}, 8)
	m.nexusEvents = events
	project := ws.Project
	m.nexusCancelWatch = nexus.WatchWithReconnect(client, project,
		func(notif *nexuscurlv1.ChangeNotification) {
			select {
			case events <- nexusNotifyMsg{}:
			default:
			}
		},
		func(err error) {
			select {
			case events <- nexusGiveUpMsg{err: err}:
			default:
			}
		},
	)

	return m, m.waitForNexusEvent()
}

// pushSelectedToNexus pushes whichever collection or environment is
// currently selected/focused (depending on which left-panel tab is
// active) up to the active collaborative workspace's server. No-op with
// a status message on the local workspace, since there's nowhere to push.
func (m Model) pushSelectedToNexus() (tea.Model, tea.Cmd) {
	ws := m.nexusRegistry.CurrentWorkspace()
	if ws.IsLocal() {
		m.statusBar.Info("Local workspace has nothing to push - switch to a collaborative workspace first (W)")
		return m, nil
	}
	if m.nexusClient == nil {
		m.statusBar.Error(fmt.Errorf("not connected to %s", ws.Server))
		return m, nil
	}

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
		pushed, err := m.nexusClient.Push(context.Background(), ws.Project, res)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		m.statusBar.Success("Push", fmt.Sprintf("%s (rev %d)", coll.Name, pushed.GetRevision()))

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
		pushed, err := m.nexusClient.Push(context.Background(), ws.Project, res)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		m.statusBar.Success("Push", fmt.Sprintf("%s (rev %d)", env.Name, pushed.GetRevision()))
	}

	return m, nil
}

// pullFromNexus re-pulls the active collaborative workspace's full
// snapshot and fully replaces the Collections/Environments panel
// contents with it - always manual, never triggered by a Subscribe
// notification on its own.
func (m Model) pullFromNexus() (tea.Model, tea.Cmd) {
	ws := m.nexusRegistry.CurrentWorkspace()
	if ws.IsLocal() {
		m.statusBar.Info("Local workspace - nothing to pull")
		return m, nil
	}
	if m.nexusClient == nil {
		m.statusBar.Error(fmt.Errorf("not connected to %s", ws.Server))
		return m, nil
	}

	resources, err := m.nexusClient.Pull(context.Background(), ws.Project)
	if err != nil {
		m.statusBar.Error(err)
		return m, nil
	}

	cacheDir := config.GetNexusCacheDir(ws.Name)
	if err := nexus.WriteResourcesToCache(cacheDir, resources); err != nil {
		m.statusBar.Error(err)
		return m, nil
	}
	m.leftPanel.GetCollections().ReloadCollections()
	m.leftPanel.GetEnvironments().ReloadEnvironments()
	m.nexusPending = false
	m.statusBar.Success("Pull", fmt.Sprintf("%d items from %s", len(resources), ws.Name))

	return m, nil
}

// openImportWorkspacePicker captures the currently-selected collection or
// environment (whichever left-panel tab is active) as a pending resource
// and shows the workspace list in "pick a push target" mode. Works from
// any workspace, including local - that's the whole point: importing a
// local resource into a collaborative one without first switching away
// from local and losing sight of it (switching does a full panel swap).
func (m Model) openImportWorkspacePicker() (tea.Model, tea.Cmd) {
	var res *nexuscurlv1.Resource
	var label string

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
		r, err := nexus.ResourceFromCollection(coll)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		res, label = r, coll.Name

	case EnvironmentsTab:
		node := m.leftPanel.GetEnvironments().getCurrentNode()
		env := m.leftPanel.GetEnvironments().getEnvForNode(node)
		if env == nil {
			m.statusBar.Info("Select an environment to import")
			return m, nil
		}
		r, err := nexus.ResourceFromEnvironment(env)
		if err != nil {
			m.statusBar.Error(err)
			return m, nil
		}
		res, label = r, env.Name

	default:
		return m, nil
	}

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

	m.pendingImportResource = res
	m.pendingImportLabel = label
	m.workspaceModal.ShowForImport(targets)
	return m, nil
}

// completeImport pushes the pending resource (captured by
// openImportWorkspacePicker) to the chosen target workspace's server,
// then switches into that workspace so the import is immediately visible
// (consistent with "full swap on switch" - without this the just-pushed
// resource wouldn't show up until a manual U pull anyway).
func (m Model) completeImport(target nexus.Workspace) (tea.Model, tea.Cmd) {
	res := m.pendingImportResource
	label := m.pendingImportLabel
	m.pendingImportResource = nil
	m.pendingImportLabel = ""

	if res == nil {
		return m, nil
	}

	client := m.nexusClient
	isTemp := false
	if client == nil || m.nexusRegistry.Current != target.Name {
		c, err := nexus.Dial(target.Server)
		if err != nil {
			m.statusBar.Error(fmt.Errorf("connecting to %s: %w", target.Server, err))
			return m, nil
		}
		client = c
		isTemp = true
	}

	pushed, err := client.Push(context.Background(), target.Project, res)
	if isTemp {
		_ = client.Close()
	}
	if err != nil {
		m.statusBar.Error(fmt.Errorf("importing %q into %s: %w", label, target.Name, err))
		return m, nil
	}

	m.statusBar.Success("Import", fmt.Sprintf("%s -> %s (rev %d)", label, target.Name, pushed.GetRevision()))

	// Switch into the target workspace so the import is visible right
	// away, rather than leaving the panel showing local/whatever was
	// active before.
	return m.switchNexusWorkspace(target.Name)
}

// connectAndShowProjectPick is called once the "add workspace" form's
// name+server fields are confirmed. It dials the new server, fetches its
// current project list, and moves the modal on to the project-pick
// stage - or shows an error and bounces back to the main list if the
// server can't be reached at all. This blocks the UI briefly (same
// synchronous-dial tradeoff already made by completeImport/
// switchNexusWorkspace) rather than threading a tea.Cmd through, to keep
// the add-workspace flow's error handling in one place.
func (m Model) connectAndShowProjectPick(name, server string) {
	client, err := nexus.Dial(server)
	if err != nil {
		m.statusBar.Error(fmt.Errorf("connecting to %s: %w", server, err))
		m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces, m.nexusRegistry.Current)
		return
	}
	defer client.Close()

	projects, err := client.ListProjects(context.Background())
	if err != nil {
		m.statusBar.Error(fmt.Errorf("listing projects on %s: %w", server, err))
		m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces, m.nexusRegistry.Current)
		return
	}

	m.workspaceModal.ShowProjectPick(projects)
}

// finishAddWorkspace persists the workspace once a project has been
// picked (or typed) in the add-workspace flow, then returns to the main
// workspace list.
func (m Model) finishAddWorkspace(ws nexus.Workspace) {
	if err := m.nexusRegistry.Add(ws); err != nil {
		m.statusBar.Error(err)
	} else if err := m.nexusRegistry.Save(); err != nil {
		m.statusBar.Error(err)
	} else {
		m.statusBar.Success("Workspace", "added "+ws.Name)
	}
	m.workspaceModal.ReturnToList(m.nexusRegistry.Workspaces, m.nexusRegistry.Current)
}
