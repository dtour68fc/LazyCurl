package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/kbrdn1/LazyCurl/internal/ui/components"
)

// handleProjectAssignModalClose persists the Project field on whichever
// collection the user pressed "P" on, so future request selections from
// that collection auto-switch to the right environment project.
func (m Model) handleProjectAssignModalClose(msg components.ModalCloseMsg) (tea.Model, tea.Cmd) {
	m.projectAssignModal.Hide()
	coll := m.pendingProjectColl
	m.pendingProjectColl = nil

	if !msg.Result.Confirmed || coll == nil {
		return m, nil
	}

	project, _ := msg.Result.Values["input"].(string)
	coll.Project = project

	if err := coll.Save(); err != nil {
		m.statusBar.Error(err)
		return m, nil
	}

	if project == "" {
		m.statusBar.Info(coll.Name + " no longer linked to a project")
	} else {
		m.statusBar.Success("Project", coll.Name+" -> "+project)
		// If a request from this collection is already loaded, switch now
		// rather than waiting for the next selection.
		m.leftPanel.GetEnvironments().SwitchToProject(project)
	}

	return m, nil
}
