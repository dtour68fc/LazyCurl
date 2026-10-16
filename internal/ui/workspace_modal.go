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

// WorkspaceModal is a custom list-picker for configured NexusCurl
// collaborative workspaces (plus the always-present local one). It's
// hand-rolled rather than using components.Modal's "radio" field type,
// which is declared but never actually implemented anywhere.
//
// Flow, now that a workspace is just a server connection with no single
// fixed project:
//   - The list (Show/Update): a/d add/remove a workspace, enter switches
//     the view's project filter to it (local included - switching to
//     local shows only unclaimed collections/environments), "e" opens
//     that workspace's project checklist (a no-op on local, which has
//     no projects).
//   - addForm (UpdateAddForm): collects name+server only. On confirm the
//     caller just registers the workspace and returns to the list - no
//     project is picked at add time anymore.
//   - The project checklist (ShowProjectChecklist/UpdateChecklist): a
//     persistent multi-select of every project known on that server
//     (plus any already-selected project the server no longer reports,
//     so it isn't silently dropped) union a trailing "+ New project..."
//     row. space/enter toggles a row, "s" saves and returns the final
//     checked set to the caller (which imports newly-checked projects
//     and removes newly-unchecked ones), esc/q cancels without saving.
//   - newProjectForm is reused for two distinct purposes depending on
//     newProjectTarget: adding a freeform project name to the open
//     checklist, or (during ShowForImport) naming/confirming which
//     project an unresolvable push/pull should use on the chosen target
//     workspace.
type WorkspaceModal struct {
	visible    bool
	cursor     int
	items      []nexus.Workspace
	current    string // name of the workspace whose filter is currently active, for the "(active)" marker
	importMode bool   // true when picking a push/pull target (see ShowForImport) rather than managing projects

	// addForm is a real components.Modal reused only for its working
	// text-field flow, to collect name+server for a new collaborative
	// workspace.
	addForm *components.Modal

	// Project checklist stage (see ShowProjectChecklist).
	checklistVisible   bool
	checklistWorkspace string // name of the workspace being edited
	checklistItems     []string
	checklistChecked   map[string]bool
	checklistCursor    int

	// newProjectForm collects a freeform project name - either a brand
	// new entry for the open checklist, or (during the import flow) the
	// project name to push/pull-link an unresolvable resource against.
	newProjectForm *components.Modal
	// newProjectTarget distinguishes which of the two newProjectForm
	// uses is active, since confirming it needs to behave differently.
	newProjectTarget newProjectTarget

	// importWorkspace is set once the import-mode list picks a target
	// workspace, ahead of the project-name prompt that follows it.
	importWorkspace nexus.Workspace
}

// newProjectTarget says what a confirmed newProjectForm submission is for.
type newProjectTarget int

const (
	newProjectForChecklist newProjectTarget = iota
	newProjectForImport
)

// NewWorkspaceModal creates a hidden workspace-switcher modal.
func NewWorkspaceModal() *WorkspaceModal {
	return &WorkspaceModal{
		addForm: components.NewFormModal("Add Workspace", "nexus_add_workspace", []components.FormField{
			{Name: "name", Label: "Name", Type: "text", Placeholder: "e.g. staging"},
			{Name: "server", Label: "Server (host:port)", Type: "text", Placeholder: "e.g. nexus.example.com:7777"},
		}),
		newProjectForm: components.NewFormModal("Project", "nexus_new_project", []components.FormField{
			{Name: "project", Label: "Project", Type: "text", Placeholder: "e.g. PMC"},
		}),
	}
}

// Show displays the list of workspaces. current is the name of the
// workspace whose filter is presently applied to the view (shown with an
// "(active)" marker) - pass the empty string if unknown.
func (w *WorkspaceModal) Show(items []nexus.Workspace, current string) {
	w.visible = true
	w.importMode = false
	w.items = items
	w.current = current
	if w.cursor >= len(items) {
		w.cursor = 0
	}
}

// ShowForImport displays the list in "pick a push/pull target" mode -
// used to resolve an unresolvable collection/environment (no Project tag,
// or a Project no workspace currently has selected) against a
// collaborative workspace. Local itself is never a valid target, so
// callers should already have filtered it out of items.
func (w *WorkspaceModal) ShowForImport(items []nexus.Workspace) {
	w.visible = true
	w.importMode = true
	w.items = items
	w.cursor = 0
}

// Hide closes the list and every sub-stage.
func (w *WorkspaceModal) Hide() {
	w.visible = false
	w.checklistVisible = false
	w.addForm.Hide()
	w.newProjectForm.Hide()
}

// IsVisible reports whether the list or any sub-stage is showing.
func (w *WorkspaceModal) IsVisible() bool {
	return w.visible || w.addForm.IsVisible() || w.checklistVisible || w.newProjectForm.IsVisible()
}

// WorkspaceModalAction is what the caller should do after Update
// processes a keypress - the modal itself never mutates the registry
// directly, it just reports intent (keeps Registry/Client ownership in
// model.go, matching how every other modal in this app reports Confirmed
// values up for the caller to act on).
type WorkspaceModalAction int

const (
	WorkspaceModalNone WorkspaceModalAction = iota
	WorkspaceModalRemove
	WorkspaceModalCancel
	WorkspaceModalAdded         // add-form confirmed - caller should Registry.Add(added) and return to the list
	WorkspaceModalSwitch        // enter on a workspace row - caller should switch the view's project filter to it
	WorkspaceModalOpenChecklist // "e" on a workspace row - caller should dial+ListProjects, then ShowProjectChecklist
	WorkspaceModalChecklistSaved
	// WorkspaceModalImportTarget fires once BOTH the target workspace and
	// the project name have been chosen during ShowForImport - the
	// returned Workspace carries Name/Server as usual, plus the chosen
	// project name as its single Projects[0] entry (not a real project
	// list - just a convenient way to return one extra string alongside
	// the workspace without a second return type).
	WorkspaceModalImportTarget
)

// Update handles a keypress against the list view. selected is only
// meaningful for WorkspaceModalRemove/WorkspaceModalOpenChecklist.
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
			if w.importMode {
				w.visible = false
				w.importWorkspace = sel
				w.newProjectTarget = newProjectForImport
				w.newProjectForm.SetFieldValue("project", "")
				w.newProjectForm.Show()
				return WorkspaceModalNone, nexus.Workspace{}
			}
			w.Hide()
			return WorkspaceModalSwitch, sel
		}
	case "e":
		if w.importMode {
			break // managing projects mid-import isn't supported
		}
		if w.cursor >= 0 && w.cursor < len(w.items) {
			sel := w.items[w.cursor]
			if sel.IsLocal() {
				break // local has no projects to manage
			}
			w.visible = false
			return WorkspaceModalOpenChecklist, sel
		}
	case "a":
		if w.importMode {
			break // adding a new workspace mid-import isn't supported - use W first
		}
		w.visible = false // keep registry state; hand off to the add sub-form
		w.addForm.SetFieldValue("name", "")
		w.addForm.SetFieldValue("server", "")
		w.addForm.Show()
	case "d":
		if w.importMode {
			break // removing a workspace mid-import isn't supported
		}
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

// UpdateAddForm forwards a key to the "add workspace" sub-form (name +
// server only). On confirm, returns WorkspaceModalAdded with those two
// fields set (the caller should Registry.Add it and return to the list);
// on cancel, returns WorkspaceModalCancel.
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
	return WorkspaceModalAdded, nexus.Workspace{Name: name, Server: server}, nil
}

// IsAddFormVisible reports whether the "add workspace" sub-form is the
// part currently showing (as opposed to the list).
func (w *WorkspaceModal) IsAddFormVisible() bool {
	return w.addForm.IsVisible()
}

// ShowProjectChecklist displays the persistent multi-select project
// checklist for the given workspace, after WorkspaceModalOpenChecklist
// has been returned and the caller has dialed the server and fetched its
// project list. selected is that workspace's current Projects - every
// entry in it is pre-checked and shown even if projects (the server's
// live list) no longer contains it, so a selected-but-removed-upstream
// project doesn't silently vanish from the checklist.
func (w *WorkspaceModal) ShowProjectChecklist(workspace string, projects []string, selected []string) {
	w.checklistVisible = true
	w.checklistWorkspace = workspace
	w.checklistCursor = 0

	w.checklistChecked = make(map[string]bool, len(selected))
	seen := make(map[string]bool, len(projects))
	items := make([]string, 0, len(projects)+len(selected))
	for _, p := range projects {
		items = append(items, p)
		seen[p] = true
	}
	for _, p := range selected {
		w.checklistChecked[p] = true
		if !seen[p] {
			items = append(items, p)
			seen[p] = true
		}
	}
	w.checklistItems = items
}

// IsChecklistVisible reports whether the project checklist (as opposed
// to its "+ New project..." text sub-form) is the part currently showing.
func (w *WorkspaceModal) IsChecklistVisible() bool {
	return w.checklistVisible
}

// IsNewProjectFormVisible reports whether the freeform project-name text
// sub-form is currently showing (used by both the checklist's
// "+ New project..." row and the import flow's project-name prompt).
func (w *WorkspaceModal) IsNewProjectFormVisible() bool {
	return w.newProjectForm.IsVisible()
}

// UpdateChecklist handles navigation/toggling on the project checklist.
// j/k move the cursor, space/enter toggle the row under the cursor (or,
// on the trailing "+ New project..." row, hand off to newProjectForm),
// "s" saves and returns WorkspaceModalChecklistSaved with the final
// checked set as the returned Workspace's Projects, esc/q cancels back to
// the list without saving.
func (w *WorkspaceModal) UpdateChecklist(msg tea.KeyMsg) (action WorkspaceModalAction, result nexus.Workspace) {
	if !w.checklistVisible {
		return WorkspaceModalNone, nexus.Workspace{}
	}
	total := len(w.checklistItems) + 1 // +1 for the trailing "+ New project..." entry
	switch msg.String() {
	case "j", "down":
		if w.checklistCursor < total-1 {
			w.checklistCursor++
		}
	case "k", "up":
		if w.checklistCursor > 0 {
			w.checklistCursor--
		}
	case " ":
		// Space toggles the checkbox under the cursor - it never applies
		// anything, just marks a project selected/deselected. No-op on
		// the trailing "+ New project..." row (nothing to toggle there).
		if w.checklistCursor < len(w.checklistItems) {
			project := w.checklistItems[w.checklistCursor]
			w.checklistChecked[project] = !w.checklistChecked[project]
		}
	case "enter":
		// Enter on "+ New project..." opens the text sub-form to add a
		// brand-new project (checked) to the list. Enter anywhere else
		// commits the current checked set and applies it - same action
		// "s" used to perform; "s" is kept as an alias below.
		if w.checklistCursor == len(w.checklistItems) {
			w.checklistVisible = false
			w.newProjectTarget = newProjectForChecklist
			w.newProjectForm.SetFieldValue("project", "")
			w.newProjectForm.Show()
			return WorkspaceModalNone, nexus.Workspace{}
		}
		fallthrough
	case "s":
		name := w.checklistWorkspace
		final := w.checkedProjects()
		w.checklistVisible = false
		return WorkspaceModalChecklistSaved, nexus.Workspace{Name: name, Projects: final}
	case "esc", "q":
		w.checklistVisible = false
		return WorkspaceModalCancel, nexus.Workspace{}
	}
	return WorkspaceModalNone, nexus.Workspace{}
}

// checkedProjects returns the currently-checked project names, in
// checklistItems order.
func (w *WorkspaceModal) checkedProjects() []string {
	var out []string
	for _, p := range w.checklistItems {
		if w.checklistChecked[p] {
			out = append(out, p)
		}
	}
	return out
}

// UpdateNewProjectForm forwards a key to the freeform project-name text
// sub-form. On confirm, behavior depends on which flow opened it:
//   - checklist: the typed name is added (checked) to the open checklist,
//     which is then re-shown (WorkspaceModalNone, checklist still active).
//   - import: returns WorkspaceModalImportTarget with the chosen
//     workspace + typed project name (see that action's doc comment).
//
// On cancel, the checklist flow returns to the checklist; the import
// flow returns to the top-level list.
func (w *WorkspaceModal) UpdateNewProjectForm(msg tea.KeyMsg) (action WorkspaceModalAction, result nexus.Workspace, cmd tea.Cmd) {
	var c tea.Cmd
	w.newProjectForm, c = w.newProjectForm.Update(msg)
	if c == nil {
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}
	closeMsg, ok := c().(components.ModalCloseMsg)
	if !ok {
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}
	if !closeMsg.Result.Confirmed {
		if w.newProjectTarget == newProjectForChecklist {
			w.checklistVisible = true
		} else {
			w.visible = true
		}
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}
	project, _ := closeMsg.Result.Values["project"].(string)

	if w.newProjectTarget == newProjectForChecklist {
		if project != "" {
			found := false
			for _, p := range w.checklistItems {
				if p == project {
					found = true
					break
				}
			}
			if !found {
				w.checklistItems = append(w.checklistItems, project)
			}
			if w.checklistChecked == nil {
				w.checklistChecked = map[string]bool{}
			}
			w.checklistChecked[project] = true
		}
		w.checklistVisible = true
		return WorkspaceModalNone, nexus.Workspace{}, nil
	}

	target := w.importWorkspace
	target.Projects = []string{project}
	return WorkspaceModalImportTarget, target, nil
}

// ReturnToList re-shows the list after a sub-stage closes, refreshed
// with the given items and current workspace (so a just-added/changed
// workspace shows up, and the "(active)" marker stays accurate).
func (w *WorkspaceModal) ReturnToList(items []nexus.Workspace, current string) {
	w.Show(items, current)
}

// View renders whichever stage is active: the main list, the add-form,
// the project checklist, or the new-project text form.
func (w *WorkspaceModal) View(screenWidth, screenHeight int) string {
	if w.addForm.IsVisible() {
		return w.addForm.View(screenWidth, screenHeight)
	}
	if w.newProjectForm.IsVisible() {
		return w.newProjectForm.View(screenWidth, screenHeight)
	}
	if w.checklistVisible {
		return w.renderChecklist(screenWidth, screenHeight)
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
	title := "Workspaces"
	if w.importMode {
		title = "Push/Pull Via Workspace"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	for i, ws := range w.items {
		marker := "  "
		if i == w.cursor {
			marker = "> "
		}
		label := ws.Name
		if !ws.IsLocal() {
			label += fmt.Sprintf("  [%s]", ws.Server)
			if len(ws.Projects) > 0 {
				label += fmt.Sprintf("  (%s)", strings.Join(ws.Projects, ", "))
			}
		}
		if !w.importMode && ws.Name == w.current {
			label += "  (active)"
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
	hint := "enter:switch  e:projects  a:add  d:remove  esc:cancel"
	if w.importMode {
		hint = "enter:use this workspace  esc:cancel"
	}
	b.WriteString(hintStyle.Render(hint))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Mauve).
		Padding(1, 2).
		Width(width)

	box := boxStyle.Render(b.String())

	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, box)
}

// renderChecklist draws the per-workspace project multi-select, with a
// trailing "+ New project..." entry that's always present (a brand-new
// server has no projects listed yet, since none have had anything pushed
// to them).
func (w *WorkspaceModal) renderChecklist(screenWidth, screenHeight int) string {
	width := 50
	if width > screenWidth-4 {
		width = screenWidth - 4
	}

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(styles.Mauve)
	b.WriteString(titleStyle.Render("Select Projects: " + w.checklistWorkspace))
	b.WriteString("\n\n")

	for i, project := range w.checklistItems {
		marker := "  "
		if i == w.checklistCursor {
			marker = "> "
		}
		box := "[ ]"
		if w.checklistChecked[project] {
			box = "[x]"
		}
		lineStyle := lipgloss.NewStyle()
		if i == w.checklistCursor {
			lineStyle = lineStyle.Foreground(styles.Crust).Background(styles.Mauve).Bold(true)
		}
		line := marker + box + " " + project
		if lipgloss.Width(line) < width-4 {
			line += strings.Repeat(" ", width-4-lipgloss.Width(line))
		}
		b.WriteString(lineStyle.Render(line))
		b.WriteString("\n")
	}

	addLabel := "+ New project..."
	addMarker := "  "
	if w.checklistCursor == len(w.checklistItems) {
		addMarker = "> "
	}
	addLineStyle := lipgloss.NewStyle()
	if w.checklistCursor == len(w.checklistItems) {
		addLineStyle = addLineStyle.Foreground(styles.Crust).Background(styles.Mauve).Bold(true)
	}
	addLine := addMarker + addLabel
	if lipgloss.Width(addLine) < width-4 {
		addLine += strings.Repeat(" ", width-4-lipgloss.Width(addLine))
	}
	b.WriteString(addLineStyle.Render(addLine))
	b.WriteString("\n\n")

	hintStyle := lipgloss.NewStyle().Foreground(styles.Subtext0)
	b.WriteString(hintStyle.Render("space:toggle  enter:apply  esc:cancel"))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Mauve).
		Padding(1, 2).
		Width(width)

	rendered := boxStyle.Render(b.String())

	return lipgloss.Place(screenWidth, screenHeight, lipgloss.Center, lipgloss.Center, rendered)
}
