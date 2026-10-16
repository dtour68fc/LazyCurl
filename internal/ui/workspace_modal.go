package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dtour68fc/LazyCurl/internal/nexus"
	"github.com/dtour68fc/LazyCurl/internal/ui/components"
	"github.com/dtour68fc/LazyCurl/pkg/styles"
)

// WorkspaceModal is a custom list-picker for switching between the local
// workspace and configured NexusCurl collaborative workspaces. It's
// hand-rolled rather than using components.Modal's "radio" field type,
// which is declared but never actually implemented anywhere.
type WorkspaceModal struct {
	visible bool
	cursor  int
	items   []nexus.Workspace
	current string

	// addForm is a real components.Modal reused only for its working
	// text-field flow, to collect name/server/project for a new
	// collaborative workspace.
	addForm *components.Modal
}

// NewWorkspaceModal creates a hidden workspace-switcher modal.
func NewWorkspaceModal() *WorkspaceModal {
	return &WorkspaceModal{
		addForm: components.NewFormModal("Add Workspace", "nexus_add_workspace", []components.FormField{
			{Name: "name", Label: "Name", Type: "text", Placeholder: "e.g. staging"},
			{Name: "server", Label: "Server (host:port)", Type: "text", Placeholder: "e.g. nexus.example.com:7777"},
			{Name: "project", Label: "Project", Type: "text", Placeholder: "e.g. PMC"},
		}),
	}
}

// Show displays the list, highlighting the currently-active workspace.
func (w *WorkspaceModal) Show(items []nexus.Workspace, current string) {
	w.visible = true
	w.items = items
	w.current = current
	w.cursor = 0
	for i, it := range items {
		if it.Name == current {
			w.cursor = i
			break
		}
	}
}

// Hide closes the list (and any open add-form sub-modal).
func (w *WorkspaceModal) Hide() {
	w.visible = false
	w.addForm.Hide()
}

// IsVisible reports whether either the list or its add-form sub-modal is showing.
func (w *WorkspaceModal) IsVisible() bool {
	return w.visible || w.addForm.IsVisible()
}

// WorkspaceModalAction is what the caller should do after Update
// processes a keypress - the modal itself never mutates the registry
// directly, it just reports intent (keeps Registry/Client ownership in
// model.go, matching how every other modal in this app reports Confirmed
// values up for the caller to act on).
type WorkspaceModalAction int

const (
	WorkspaceModalNone WorkspaceModalAction = iota
	WorkspaceModalSwitch
	WorkspaceModalAdd
	WorkspaceModalRemove
	WorkspaceModalCancel
)

// Update handles a keypress against the list view. selected is only
// meaningful for WorkspaceModalSwitch/WorkspaceModalRemove. added is only
// populated for WorkspaceModalAdd (once the add-form sub-modal is itself
// confirmed, which takes a separate call - see UpdateAddForm).
func (w *WorkspaceModal) Update(msg tea.KeyMsg) (action WorkspaceModalAction, selected nexus.Workspace) {
	if !w.visible {
		return WorkspaceModalNone, nexus.Workspace{}
	}

	switch msg.String() {
	case "j", "down":
		if w.cursor < len(w.items)-1 {
			w.cursor++
		}
	case "k", "up":
		if w.cursor > 0 {
			w.cursor--
		}
	case "enter":
		if w.cursor >= 0 && w.cursor < len(w.items) {
			sel := w.items[w.cursor]
			w.Hide()
			return WorkspaceModalSwitch, sel
		}
	case "a":
		w.visible = false // keep registry state; hand off to the add sub-form
		w.addForm.SetFieldValue("name", "")
		w.addForm.SetFieldValue("server", "")
		w.addForm.SetFieldValue("project", "")
		w.addForm.Show()
	case "d":
		if w.cursor >= 0 && w.cursor < len(w.items) {
			sel := w.items[w.cursor]
			if !sel.IsLocal() {
				w.Hide()
				return WorkspaceModalRemove, sel
			}
		}
	case "esc", "q":
		w.Hide()
		return WorkspaceModalCancel, nexus.Workspace{}
	}

	return WorkspaceModalNone, nexus.Workspace{}
}

// UpdateAddForm forwards a key to the "add workspace" sub-form. When it
// closes (confirmed or canceled), returns WorkspaceModalAdd + the new
// Workspace on confirm, or WorkspaceModalCancel otherwise - the caller
// (model.go) is responsible for validating/persisting via Registry.Add.
func (w *WorkspaceModal) UpdateAddForm(msg tea.KeyMsg) (action WorkspaceModalAction, added nexus.Workspace, cmd tea.Cmd) {
	var c tea.Cmd
	w.addForm, c = w.addForm.Update(msg)
	if c == nil {
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}
	closeMsg, ok := c().(components.ModalCloseMsg)
	if !ok {
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}
	if !closeMsg.Result.Confirmed {
		return WorkspaceModalCancel, nexus.Workspace{}, nil
	}
	name, _ := closeMsg.Result.Values["name"].(string)
	server, _ := closeMsg.Result.Values["server"].(string)
	project, _ := closeMsg.Result.Values["project"].(string)
	return WorkspaceModalAdd, nexus.Workspace{Name: name, Server: server, Project: project}, nil
}

// IsAddFormVisible reports whether the "add workspace" sub-form is the
// part currently showing (as opposed to the list).
func (w *WorkspaceModal) IsAddFormVisible() bool {
	return w.addForm.IsVisible()
}

// ReturnToList re-shows the list after the add-form sub-modal closes,
// refreshed with the given items (so a just-added workspace shows up).
func (w *WorkspaceModal) ReturnToList(items []nexus.Workspace, current string) {
	w.Show(items, current)
}

// View renders either the list or the add-form, whichever is active.
func (w *WorkspaceModal) View(screenWidth, screenHeight int) string {
	if w.addForm.IsVisible() {
		return w.addForm.View(screenWidth, screenHeight)
	}
	if !w.visible {
		return ""
	}

	width := 50
	if width > screenWidth-4 {
		width = screenWidth - 4
	}

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(styles.Mauve)
	b.WriteString(titleStyle.Render("Switch Workspace"))
	b.WriteString("\n\n")

	for i, ws := range w.items {
		marker := "  "
		if i == w.cursor {
			marker = "> "
		}
		label := ws.Name
		if ws.Name == w.current {
			label += " (active)"
		}
		if !ws.IsLocal() {
			label += fmt.Sprintf("  [%s]", ws.Server)
		}
		lineStyle := lipgloss.NewStyle()
		if i == w.cursor {
			lineStyle = lineStyle.Foreground(styles.Crust).Background(styles.Mauve).Bold(true)
		}
		line := marker + label
		if lipgloss.Width(line) < width-4 {
			line += strings.Repeat(" ", width-4-lipgloss.Width(line))
		}
		b.WriteString(lineStyle.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(styles.Subtext0)
	b.WriteString(hintStyle.Render("enter:switch  a:add  d:remove  esc:cancel"))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Mauve).
		Padding(1, 2).
		Width(width)

	box := boxStyle.Render(b.String())

	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, box)
}
