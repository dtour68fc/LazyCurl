package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kbrdn1/LazyCurl/internal/api"
	"github.com/kbrdn1/LazyCurl/internal/config"
	"github.com/kbrdn1/LazyCurl/internal/ui/components"
	"github.com/kbrdn1/LazyCurl/pkg/styles"
)

// EnvNodeType represents the type of environment tree node
type EnvNodeType int

const (
	ProjectNode EnvNodeType = iota
	EnvNode
	VarNode
)

// UngroupedProject is the bucket name used for environments with no Project
// set (legacy environments created before project grouping existed).
const UngroupedProject = "Ungrouped"

// EnvDeleteBlockedMsg is sent when a delete was refused rather than
// showing the confirm modal (e.g. trying to delete the last remaining
// environment of a project that's still linked to a collection).
type EnvDeleteBlockedMsg struct {
	Reason string
}

// ProjectDeletedMsg is sent after a ProjectNode delete actually goes
// through (all of its environments removed). Model uses this to cascade
// the deletion to the Collections side too - deleting "the project" from
// either tab should delete it from both, not leave a same-named collection
// behind with no environments to switch to.
type ProjectDeletedMsg struct {
	Project string
}

// EnvTreeNode represents a node in the environment tree
type EnvTreeNode struct {
	Name     string
	Type     EnvNodeType
	Variable *api.EnvironmentVariable // For VarNode
	Expanded bool                     // Only for EnvNode/ProjectNode
	Children []*EnvTreeNode
	Parent   *EnvTreeNode
	EnvFile  *api.EnvironmentFile // Reference to source environment (EnvNode/VarNode)
}

// Depth returns the nesting depth of the node (0 = project root).
func (n *EnvTreeNode) Depth() int {
	d := 0
	for p := n.Parent; p != nil; p = p.Parent {
		d++
	}
	return d
}

// EnvClipboard holds copied environment data
type EnvClipboard struct {
	Type    EnvNodeType
	Name    string
	EnvFile *api.EnvironmentFile     // For EnvNode
	VarData *api.EnvironmentVariable // For VarNode
}

// EnvironmentsView represents the environments panel
type EnvironmentsView struct {
	workspacePath    string
	environmentsPath string // where NEW environments get saved - global by default
	legacyLocalPath  string // old per-workspace .lazycurl/environments, still loaded (not saved to) for backward compat
	environments     []*api.EnvironmentFile
	tree             []*EnvTreeNode
	visible          []*EnvTreeNode
	cursor           int
	scrollOffset     int
	height           int
	activeEnvName    string            // Currently active environment (display name, may not be unique)
	activeEnvPath    string            // Currently active environment's file path (authoritative identity)
	activeEnvByProj  map[string]string // Remembers last-active environment per project
	clipboard        *EnvClipboard

	// Search
	search      *components.SearchInput
	searchQuery string

	// Modals
	deleteModal *components.Modal
	newVarModal *components.Modal
	newEnvModal *components.Modal
	editModal   *components.Modal
	renameModal *components.Modal
	caCertModal *components.Modal // Sets an EnvNode's custom CA cert path (T)
	pendingNode *EnvTreeNode // Node being acted upon
}

// NewEnvironmentsView creates a new environments view
func NewEnvironmentsView(workspacePath string) *EnvironmentsView {
	ev := &EnvironmentsView{
		workspacePath: workspacePath,
		// New environments save to global scope by default, same reasoning
		// as CollectionsView - accessible from anywhere, not siloed per-cwd.
		environmentsPath: config.GetGlobalEnvironmentsPath(),
		// Existing per-workspace environments (if any) keep loading for
		// backward compat, they just don't get NEW stuff written there.
		legacyLocalPath: filepath.Join(workspacePath, ".lazycurl", "environments"),
		cursor:          0,
		scrollOffset:    0,
		activeEnvName:   "",
		activeEnvByProj: make(map[string]string),
		search:          components.NewSearchInput(),
	}

	// Initialize modals
	ev.deleteModal = components.NewConfirmModal("Delete", "", "delete")
	ev.newVarModal = components.NewFormModal("New Variable", "new_var", []components.FormField{
		{Name: "name", Label: "Name", Type: "text", Placeholder: "variable_name"},
		{Name: "value", Label: "Value", Type: "text", Placeholder: "value"},
		{Name: "secret", Label: "Secret", Type: "checkbox", Value: "false"},
		{Name: "active", Label: "Active", Type: "checkbox", Value: "true"},
	})
	ev.newEnvModal = components.NewFormModal("New Environment", "new_env", []components.FormField{
		{Name: "project", Label: "Project", Type: "text", Placeholder: "e.g. PMC, PMV (new or existing)"},
		{Name: "name", Label: "Name", Type: "text", Placeholder: "e.g. Localhost, Staging, Production"},
		{Name: "description", Label: "Description", Type: "text", Placeholder: "optional description"},
	})
	ev.editModal = components.NewFormModal("Edit Value", "edit", []components.FormField{
		{Name: "value", Label: "Value", Type: "text"},
		{Name: "secret", Label: "Secret", Type: "checkbox"},
		{Name: "active", Label: "Active", Type: "checkbox"},
	})
	ev.renameModal = components.NewInputModal("Rename", "New Name", "", "rename")
	ev.caCertModal = components.NewFormModal("TLS Settings", "ca_cert", []components.FormField{
		{Name: "ca", Label: "CA Cert Path", Type: "text", Placeholder: "~/path/to/ca.crt"},
		{Name: "cert", Label: "Client Cert Path", Type: "text", Placeholder: "for mTLS, if server needs one"},
		{Name: "key", Label: "Client Key Path", Type: "text", Placeholder: "matching private key"},
	})

	ev.loadEnvironments()

	return ev
}

// loadEnvironments loads environments from the workspace path
func (e *EnvironmentsView) loadEnvironments() {
	local, err := api.LoadAllEnvironments(e.legacyLocalPath)
	if err != nil {
		local = []*api.EnvironmentFile{}
	}

	global, err := api.LoadAllEnvironments(e.environmentsPath)
	if err != nil {
		global = []*api.EnvironmentFile{}
	}

	e.environments = mergeEnvironmentsByPath(local, global)
	e.buildTree()
	e.refresh()

	// Set first environment as active by default
	if len(e.environments) > 0 && e.activeEnvName == "" {
		e.activeEnvName = e.environments[0].Name
		e.rememberActive(e.environments[0])
	}
}

// mergeEnvironmentsByPath combines two environment slices, de-duplicating by
// file path (the same environment loaded twice would otherwise show up twice).
func mergeEnvironmentsByPath(a, b []*api.EnvironmentFile) []*api.EnvironmentFile {
	seen := make(map[string]bool, len(a))
	merged := make([]*api.EnvironmentFile, 0, len(a)+len(b))
	for _, e := range a {
		seen[e.FilePath] = true
		merged = append(merged, e)
	}
	for _, e := range b {
		if !seen[e.FilePath] {
			merged = append(merged, e)
		}
	}
	return merged
}

// buildTree builds the tree structure from environments
func (e *EnvironmentsView) buildTree() {
	// Preserve expanded state from old tree
	expandedEnvs := make(map[string]bool)
	expandedProjects := make(map[string]bool)
	for _, node := range e.tree {
		e.collectExpandedState(node, expandedProjects, expandedEnvs)
	}

	// Group environments by project (empty Project -> Ungrouped bucket)
	byProject := make(map[string][]*api.EnvironmentFile)
	for _, env := range e.environments {
		proj := env.Project
		if proj == "" {
			proj = UngroupedProject
		}
		byProject[proj] = append(byProject[proj], env)
	}

	projectNames := make([]string, 0, len(byProject))
	for name := range byProject {
		projectNames = append(projectNames, name)
	}
	sort.Strings(projectNames)

	e.tree = make([]*EnvTreeNode, 0, len(projectNames))

	for _, projName := range projectNames {
		envs := byProject[projName]
		sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })

		projNode := &EnvTreeNode{
			Name:     projName,
			Type:     ProjectNode,
			Expanded: expandedProjects[projName],
			Children: make([]*EnvTreeNode, 0, len(envs)),
		}

		for _, env := range envs {
			expanded := expandedEnvs[env.FilePath]

			envNode := &EnvTreeNode{
				Name:     env.Name,
				Type:     EnvNode,
				Expanded: expanded,
				EnvFile:  env,
				Parent:   projNode,
				Children: make([]*EnvTreeNode, 0),
			}

			// Sort variable names for consistent display
			varNames := make([]string, 0, len(env.Variables))
			for name := range env.Variables {
				varNames = append(varNames, name)
			}
			sort.Strings(varNames)

			// Create child nodes for each variable
			for _, name := range varNames {
				variable := env.Variables[name]
				varNode := &EnvTreeNode{
					Name:     name,
					Type:     VarNode,
					Variable: variable,
					Parent:   envNode,
					EnvFile:  env,
				}
				envNode.Children = append(envNode.Children, varNode)
			}

			projNode.Children = append(projNode.Children, envNode)
		}

		e.tree = append(e.tree, projNode)
	}
}

// collectExpandedState walks an existing tree (before a rebuild) recording
// which project/environment nodes were expanded, so buildTree can restore it.
func (e *EnvironmentsView) collectExpandedState(node *EnvTreeNode, projects, envs map[string]bool) {
	switch node.Type {
	case ProjectNode:
		projects[node.Name] = node.Expanded
	case EnvNode:
		if node.EnvFile != nil {
			envs[node.EnvFile.FilePath] = node.Expanded
		}
	}
	for _, c := range node.Children {
		e.collectExpandedState(c, projects, envs)
	}
}

// projectOfNode returns the project name a given node belongs to, regardless
// of whether it's a ProjectNode itself, an EnvNode, or a VarNode.
func (e *EnvironmentsView) projectOfNode(node *EnvTreeNode) string {
	for n := node; n != nil; n = n.Parent {
		if n.Type == ProjectNode {
			if n.Name == UngroupedProject {
				return ""
			}
			return n.Name
		}
	}
	return ""
}

// refresh rebuilds the visible list
func (e *EnvironmentsView) refresh() {
	e.visible = make([]*EnvTreeNode, 0)

	for _, node := range e.tree {
		e.flattenNode(node)
	}

	// Ensure cursor is within bounds
	if e.cursor >= len(e.visible) {
		e.cursor = len(e.visible) - 1
	}
	if e.cursor < 0 {
		e.cursor = 0
	}
}

// flattenNode recursively adds visible nodes to the list
func (e *EnvironmentsView) flattenNode(node *EnvTreeNode) {
	// If searching, check if this node or any child matches
	if e.searchQuery != "" {
		if !e.nodeMatchesSearch(node) {
			return
		}
	}

	e.visible = append(e.visible, node)
	if (node.Expanded || e.searchQuery != "") && (node.Type == EnvNode || node.Type == ProjectNode) {
		// When searching, show all matching children regardless of expanded state
		for _, child := range node.Children {
			e.flattenNode(child)
		}
	}
}

// nodeMatchesSearch checks if node or any child matches the search query
func (e *EnvironmentsView) nodeMatchesSearch(node *EnvTreeNode) bool {
	// Check if this node matches
	if components.MatchesQuery(node.Name, e.searchQuery) {
		return true
	}

	// For EnvNode/ProjectNode, check if any child matches
	if node.Type == EnvNode || node.Type == ProjectNode {
		for _, child := range node.Children {
			if e.nodeMatchesSearch(child) {
				return true
			}
		}
	}

	return false
}

// scrollIntoView ensures cursor is visible
func (e *EnvironmentsView) scrollIntoView() {
	if e.cursor < e.scrollOffset {
		e.scrollOffset = e.cursor
	}
	if e.height > 0 && e.cursor >= e.scrollOffset+e.height {
		e.scrollOffset = e.cursor - e.height + 1
	}
}

// getCurrentNode returns the currently selected node
func (e *EnvironmentsView) getCurrentNode() *EnvTreeNode {
	if e.cursor >= 0 && e.cursor < len(e.visible) {
		return e.visible[e.cursor]
	}
	return nil
}

// getEnvForNode returns the environment file for a node
func (e *EnvironmentsView) getEnvForNode(node *EnvTreeNode) *api.EnvironmentFile {
	if node == nil {
		return nil
	}
	return node.EnvFile
}

// envNameExists checks if an environment with the given name already exists
func (e *EnvironmentsView) envNameExists(name string) bool {
	for _, env := range e.environments {
		if env.Name == name {
			return true
		}
	}
	return false
}

// saveEnvironment saves an environment to disk
func (e *EnvironmentsView) saveEnvironment(env *api.EnvironmentFile) error {
	if env.FilePath == "" {
		base := env.Name
		if env.Project != "" {
			base = env.Project + "-" + env.Name
		}
		env.FilePath = filepath.Join(e.environmentsPath, strings.ToLower(strings.ReplaceAll(base, " ", "-"))+".json")
	}
	return api.SaveEnvironment(env, env.FilePath)
}

// HasEnvironmentInProject returns true if any environment is already
// tagged with the given project.
func (e *EnvironmentsView) HasEnvironmentInProject(project string) bool {
	if project == "" {
		return false
	}
	for _, env := range e.environments {
		if env.Project == project {
			return true
		}
	}
	return false
}

// IsLastEnvironmentOfLinkedProject returns true if the given project (any
// project besides Ungrouped) has exactly one remaining environment. Used to
// guard against silently zeroing out the environments of a real project -
// both from the direct EnvNode delete key, and from cascading deletes
// triggered elsewhere (e.g. deleting the last collection tagged with that
// project). Deliberately does NOT require the project to be the currently
// "active" one in the session - that bookkeeping isn't reliably set (e.g.
// environments created via the Envs tab's own "N" are never auto-activated),
// so it was silently letting the guard get bypassed. A project's last
// environment is now protected unconditionally; the explicit escape hatch
// is deleting the whole project (ProjectNode, "d" on the project row).
func (e *EnvironmentsView) IsLastEnvironmentOfLinkedProject(project string) bool {
	if project == "" {
		return false
	}
	count := 0
	for _, env := range e.environments {
		if env.Project == project {
			count++
		}
	}
	return count == 1
}

// DeleteEnvironmentsForProject removes every environment file tagged with
// the given project (accepts either the raw project name or the
// UngroupedProject sentinel used by the tree UI). Used both by deleting a
// ProjectNode directly in the Envs tab, and by deleting the last collection
// tagged with a project in the Collections tab - either action "deletes the
// project", so both should cascade to its environments. Returns the number
// of environments removed.
func (e *EnvironmentsView) DeleteEnvironmentsForProject(project string) int {
	if project == UngroupedProject {
		project = ""
	}
	removed := 0
	var remaining []*api.EnvironmentFile
	for _, env := range e.environments {
		if env.Project != project {
			remaining = append(remaining, env)
			continue
		}
		if env.FilePath != "" {
			_ = os.Remove(env.FilePath) // Error intentionally ignored for UI responsiveness
		}
		if e.activeEnvPath == env.FilePath {
			e.activeEnvName = ""
			e.activeEnvPath = ""
		}
		removed++
	}
	e.environments = remaining
	delete(e.activeEnvByProj, project)
	// Set first remaining environment as active if none selected
	if e.activeEnvName == "" && len(e.environments) > 0 {
		e.activeEnvName = e.environments[0].Name
		e.rememberActive(e.environments[0])
	}
	e.buildTree()
	e.refresh()
	return removed
}

// CreateEnvironment creates and persists a new environment, mirroring the
// "new_env" modal flow, and refreshes the tree. Returns the created
// environment, or nil if name is empty.
func (e *EnvironmentsView) CreateEnvironment(name, description, project string) *api.EnvironmentFile {
	if name == "" {
		return nil
	}
	newEnv := &api.EnvironmentFile{
		Name:        name,
		Description: description,
		Project:     project,
		Variables:   make(map[string]*api.EnvironmentVariable),
	}
	e.environments = append(e.environments, newEnv)
	_ = e.saveEnvironment(newEnv) // Error intentionally ignored for UI responsiveness
	e.buildTree()
	e.refresh()
	return newEnv
}


// hasActiveModal returns true if any modal is visible
func (e *EnvironmentsView) hasActiveModal() bool {
	return e.deleteModal.IsVisible() ||
		e.newVarModal.IsVisible() ||
		e.newEnvModal.IsVisible() ||
		e.editModal.IsVisible() ||
		e.renameModal.IsVisible() ||
		e.caCertModal.IsVisible()
}

// IsSearching returns true if search is active
func (e *EnvironmentsView) IsSearching() bool {
	return e.search.IsVisible()
}

// moveToFirstMatch moves cursor to the first node that directly matches the search query
func (e *EnvironmentsView) moveToFirstMatch() {
	if e.searchQuery == "" {
		return
	}
	for i, node := range e.visible {
		if components.MatchesQuery(node.Name, e.searchQuery) {
			e.cursor = i
			e.scrollIntoView()
			return
		}
	}
}

// nextMatch moves cursor to the next matching node
func (e *EnvironmentsView) nextMatch() {
	if e.searchQuery == "" || len(e.visible) == 0 {
		return
	}
	for i := 1; i <= len(e.visible); i++ {
		idx := (e.cursor + i) % len(e.visible)
		if components.MatchesQuery(e.visible[idx].Name, e.searchQuery) {
			e.cursor = idx
			e.scrollIntoView()
			return
		}
	}
}

// prevMatch moves cursor to the previous matching node
func (e *EnvironmentsView) prevMatch() {
	if e.searchQuery == "" || len(e.visible) == 0 {
		return
	}
	for i := 1; i <= len(e.visible); i++ {
		idx := (e.cursor - i + len(e.visible)) % len(e.visible)
		if components.MatchesQuery(e.visible[idx].Name, e.searchQuery) {
			e.cursor = idx
			e.scrollIntoView()
			return
		}
	}
}

// HasSearchQuery returns true if there's an active search query (not input visible)
func (e *EnvironmentsView) HasSearchQuery() bool {
	return e.searchQuery != "" && !e.search.IsVisible()
}

// Update handles messages for the environments view
func (e EnvironmentsView) Update(msg tea.Msg, cfg *config.GlobalConfig) (EnvironmentsView, tea.Cmd) {
	// Handle search messages first (they come from the search input component)
	switch msg := msg.(type) {
	case components.SearchUpdateMsg:
		e.searchQuery = msg.Query
		e.refresh()
		// Move cursor to first matching node
		e.moveToFirstMatch()
		return e, nil

	case components.SearchCloseMsg:
		if msg.Canceled {
			e.searchQuery = ""
			e.refresh()
		}
		return e, nil
	}

	// Handle search input when visible
	if e.search.IsVisible() {
		var cmd tea.Cmd
		e.search, cmd = e.search.Update(msg)
		return e, cmd
	}

	// Handle modal updates first - capture commands to get ModalCloseMsg
	var cmd tea.Cmd
	if e.deleteModal.IsVisible() {
		e.deleteModal, cmd = e.deleteModal.Update(msg)
		if cmd != nil {
			// Execute the command to get ModalCloseMsg
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}
	if e.newVarModal.IsVisible() {
		e.newVarModal, cmd = e.newVarModal.Update(msg)
		if cmd != nil {
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}
	if e.newEnvModal.IsVisible() {
		e.newEnvModal, cmd = e.newEnvModal.Update(msg)
		if cmd != nil {
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}
	if e.editModal.IsVisible() {
		e.editModal, cmd = e.editModal.Update(msg)
		if cmd != nil {
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}
	if e.renameModal.IsVisible() {
		e.renameModal, cmd = e.renameModal.Update(msg)
		if cmd != nil {
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}
	if e.caCertModal.IsVisible() {
		e.caCertModal, cmd = e.caCertModal.Update(msg)
		if cmd != nil {
			closeMsg := cmd()
			if closeMsg, ok := closeMsg.(components.ModalCloseMsg); ok {
				return e.handleModalClose(closeMsg)
			}
		}
	}

	switch msg := msg.(type) {
	case components.ModalCloseMsg:
		return e.handleModalClose(msg)

	case tea.KeyMsg:
		// If modal is active, don't process other keys
		if e.hasActiveModal() {
			return e, nil
		}

		switch msg.String() {
		case "j", "down":
			if e.cursor < len(e.visible)-1 {
				e.cursor++
				e.scrollIntoView()
			}
		case "k", "up":
			if e.cursor > 0 {
				e.cursor--
				e.scrollIntoView()
			}
		case "l", "right", " ":
			// Expand project or environment
			if node := e.getCurrentNode(); node != nil {
				if (node.Type == EnvNode || node.Type == ProjectNode) && !node.Expanded {
					node.Expanded = true
					e.refresh()
				}
			}
		case "h", "left":
			// Collapse project/environment or go to parent
			if node := e.getCurrentNode(); node != nil {
				if (node.Type == EnvNode || node.Type == ProjectNode) && node.Expanded {
					node.Expanded = false
					e.refresh()
				} else if node.Parent != nil {
					// Go to parent node (variable -> env, or env -> project)
					for i, n := range e.visible {
						if n == node.Parent {
							e.cursor = i
							e.scrollIntoView()
							break
						}
					}
				}
			}

		case "s":
			// Toggle secret
			if node := e.getCurrentNode(); node != nil && node.Type == VarNode {
				env := e.getEnvForNode(node)
				if env != nil {
					env.ToggleVariableSecret(node.Name)
					_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
				}
			}

		case "a", "A":
			// Toggle active for variable, or select env
			if node := e.getCurrentNode(); node != nil {
				if node.Type == VarNode {
					env := e.getEnvForNode(node)
					if env != nil {
						env.ToggleVariableActive(node.Name)
						_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
					}
				} else if node.Type == EnvNode {
					e.setActiveEnvironmentNode(node)
				}
			}

		case "S":
			// Select environment
			if node := e.getCurrentNode(); node != nil {
				if node.Type == EnvNode {
					e.setActiveEnvironmentNode(node)
				} else if node.Type == VarNode && node.Parent != nil {
					e.setActiveEnvironmentNode(node.Parent)
				}
			}

		case "enter":
			// Set as active environment
			if node := e.getCurrentNode(); node != nil {
				if node.Type == EnvNode {
					e.setActiveEnvironmentNode(node)
				} else if node.Type == VarNode && node.Parent != nil {
					e.setActiveEnvironmentNode(node.Parent)
				}
			}

		case "c", "i":
			// In search mode: "i" reopens search input
			if e.HasSearchQuery() {
				e.search.Show()
				return e, nil
			}
			// Edit value
			if node := e.getCurrentNode(); node != nil && node.Type == VarNode {
				e.pendingNode = node
				e.editModal.SetFieldValue("value", node.Variable.Value)
				if node.Variable.Secret {
					e.editModal.SetFieldValue("secret", "true")
				} else {
					e.editModal.SetFieldValue("secret", "false")
				}
				if node.Variable.Active {
					e.editModal.SetFieldValue("active", "true")
				} else {
					e.editModal.SetFieldValue("active", "false")
				}
				e.editModal.Title = "Edit: " + node.Name
				e.editModal.Show()
			}

		case "R":
			// Rename
			if node := e.getCurrentNode(); node != nil {
				e.pendingNode = node
				e.renameModal.SetFieldValue("input", node.Name)
				switch node.Type {
				case ProjectNode:
					e.renameModal.Title = "Rename Project"
				case EnvNode:
					e.renameModal.Title = "Rename Environment"
				default:
					e.renameModal.Title = "Rename Variable"
				}
				e.renameModal.Show()
			}

		case "T":
			// Set TLS settings for this environment: a custom CA to trust
			// (talking to a server on a private/self-signed CA, e.g. a local
			// docker-compose stack's shared certs) and/or a client cert+key
			// for mTLS (server responds "tls: certificate required" without one)
			if node := e.getCurrentNode(); node != nil {
				env := e.getEnvForNode(node)
				if env != nil {
					e.pendingNode = node
					e.caCertModal.SetFieldValue("ca", env.CACertPath)
					e.caCertModal.SetFieldValue("cert", env.ClientCertPath)
					e.caCertModal.SetFieldValue("key", env.ClientKeyPath)
					e.caCertModal.Title = "TLS Settings: " + env.Name
					e.caCertModal.Show()
				}
			}

		case "d":
			// Delete
			if node := e.getCurrentNode(); node != nil {
				e.pendingNode = node
				switch node.Type {
				case ProjectNode:
					count := 0
					for _, env := range e.environments {
						envProject := env.Project
						if envProject == "" {
							envProject = UngroupedProject
						}
						if envProject == node.Name {
							count++
						}
					}
					plural := "environment"
					if count != 1 {
						plural = "environments"
					}
					e.deleteModal.Message = fmt.Sprintf("Delete project '%s' and its %d %s? This cannot be undone.", node.Name, count, plural)
					e.deleteModal.Show()
				case EnvNode:
					// Refuse to delete the last environment of a real
					// project - that would silently orphan it with zero
					// environments. Deleting the whole project
					// (ProjectNode) is the explicit way to do that.
					project := node.EnvFile.Project
					if e.IsLastEnvironmentOfLinkedProject(project) {
						e.pendingNode = nil
						return e, func() tea.Msg {
							return EnvDeleteBlockedMsg{
								Reason: "Can't delete '" + node.Name + "' - it's the only environment left in project '" + project + "'. Delete the project instead.",
							}
						}
					}
					e.deleteModal.Message = "Delete environment: " + node.Name + "?"
					e.deleteModal.Show()
				case VarNode:
					if node.Parent != nil {
						path := node.Parent.Name + "/" + node.Name
						e.deleteModal.Message = "Delete variable: " + path + "?"
						e.deleteModal.Show()
					}
				}
			}

		case "D":
			// Duplicate
			if node := e.getCurrentNode(); node != nil {
				if node.Type == EnvNode {
					// Duplicate environment
					if node.EnvFile != nil {
						newEnv := node.EnvFile.Clone()
						newName := node.Name + "_copy"
						// Check for unique name
						counter := 1
						for e.envNameExists(newName) {
							counter++
							newName = node.Name + "_copy" + fmt.Sprintf("%d", counter)
						}
						newEnv.Name = newName
						// Generate new file path
						envDir := e.environmentsPath
						newFilePath := filepath.Join(envDir, newName+".json")
						newEnv.FilePath = newFilePath
						if err := api.SaveEnvironment(newEnv, newFilePath); err == nil {
							e.loadEnvironments()
						}
					}
				} else if node.Type == VarNode && node.Parent != nil {
					// Duplicate variable
					targetEnv := e.getEnvForNode(node)
					if targetEnv != nil && node.Variable != nil {
						newName := node.Name + "_copy"
						counter := 1
						for {
							if _, exists := targetEnv.Variables[newName]; !exists {
								break
							}
							counter++
							newName = node.Name + "_copy" + fmt.Sprintf("%d", counter)
						}
						targetEnv.Variables[newName] = &api.EnvironmentVariable{
							Value:  node.Variable.Value,
							Secret: node.Variable.Secret,
							Active: node.Variable.Active,
						}
						if err := api.SaveEnvironment(targetEnv, targetEnv.FilePath); err == nil {
							e.loadEnvironments()
						}
					}
				}
			}

		case "y":
			// Yank (copy) - not supported for ProjectNode (spans multiple files)
			if node := e.getCurrentNode(); node != nil && node.Type != ProjectNode {
				e.clipboard = &EnvClipboard{
					Type: node.Type,
					Name: node.Name,
				}
				if node.Type == EnvNode {
					e.clipboard.EnvFile = node.EnvFile.Clone()
				} else if node.Variable != nil {
					e.clipboard.VarData = &api.EnvironmentVariable{
						Value:  node.Variable.Value,
						Secret: node.Variable.Secret,
						Active: node.Variable.Active,
					}
				}
			}

		case "p":
			// Paste
			if e.clipboard != nil {
				if node := e.getCurrentNode(); node != nil {
					targetEnv := e.getEnvForNode(node)
					if targetEnv == nil {
						break
					}

					if e.clipboard.Type == VarNode && e.clipboard.VarData != nil {
						// Paste variable into current env
						newName := e.clipboard.Name
						// Check for duplicates and add suffix
						i := 1
						for targetEnv.HasVariable(newName) {
							newName = e.clipboard.Name + "_copy"
							if i > 1 {
								newName = e.clipboard.Name + "_copy" + string(rune('0'+i))
							}
							i++
						}
						targetEnv.SetVariableFull(newName, &api.EnvironmentVariable{
							Value:  e.clipboard.VarData.Value,
							Secret: e.clipboard.VarData.Secret,
							Active: e.clipboard.VarData.Active,
						})
						_ = e.saveEnvironment(targetEnv) // Error intentionally ignored for UI responsiveness
						e.buildTree()
						e.refresh()
					}
				}
			}

		case "n":
			// In search mode: next match, otherwise: new variable
			if e.HasSearchQuery() {
				e.nextMatch()
				return e, nil
			}
			if node := e.getCurrentNode(); node != nil && node.Type != ProjectNode {
				e.pendingNode = node
				// Reset form
				e.newVarModal.SetFieldValue("name", "")
				e.newVarModal.SetFieldValue("value", "")
				e.newVarModal.SetFieldValue("secret", "false")
				e.newVarModal.SetFieldValue("active", "true")
				e.newVarModal.Show()
			}

		case "N":
			// In search mode: previous match, otherwise: new environment
			if e.HasSearchQuery() {
				e.prevMatch()
				return e, nil
			}
			e.pendingNode = nil
			e.newEnvModal.SetFieldValue("name", "")
			e.newEnvModal.SetFieldValue("description", "")
			// Prefill the project from whatever's currently selected, so adding
			// another environment to the same project doesn't require retyping it
			if node := e.getCurrentNode(); node != nil {
				e.newEnvModal.SetFieldValue("project", e.projectOfNode(node))
			} else {
				e.newEnvModal.SetFieldValue("project", "")
			}
			e.newEnvModal.Show()

		case "g":
			e.cursor = 0
			e.scrollIntoView()
		case "G":
			if len(e.visible) > 0 {
				e.cursor = len(e.visible) - 1
				e.scrollIntoView()
			}
		case "/":
			// Open search
			e.search.Show()
			return e, nil
		case "esc":
			// Clear search filter if active
			if e.searchQuery != "" {
				e.searchQuery = ""
				e.refresh()
				return e, nil
			}
		}
	}

	return e, nil
}

// handleModalClose handles modal close events
func (e EnvironmentsView) handleModalClose(msg components.ModalCloseMsg) (EnvironmentsView, tea.Cmd) {
	if !msg.Result.Confirmed {
		e.pendingNode = nil
		return e, nil
	}

	switch msg.Tag {
	case "delete":
		if e.pendingNode != nil {
			deletedProject := ""
			switch e.pendingNode.Type {
			case ProjectNode:
				// Delete every environment file grouped under this project.
				project := e.pendingNode.Name
				e.DeleteEnvironmentsForProject(project)
				if project != UngroupedProject {
					deletedProject = project
				}
			case EnvNode:
				// Delete environment file from disk
				if e.pendingNode.EnvFile.FilePath != "" {
					_ = os.Remove(e.pendingNode.EnvFile.FilePath)
				}
				// Clear active environment if it was the deleted one
				if e.pendingNode.EnvFile != nil && e.activeEnvPath == e.pendingNode.EnvFile.FilePath {
					e.activeEnvName = ""
					e.activeEnvPath = ""
				}
				// Remove from list
				for i, env := range e.environments {
					if env == e.pendingNode.EnvFile {
						e.environments = append(e.environments[:i], e.environments[i+1:]...)
						break
					}
				}
				// Set first remaining environment as active if none selected
				if e.activeEnvName == "" && len(e.environments) > 0 {
					e.activeEnvName = e.environments[0].Name
					e.rememberActive(e.environments[0])
				}
			default:
				// Delete variable
				env := e.getEnvForNode(e.pendingNode)
				if env != nil {
					env.DeleteVariable(e.pendingNode.Name)
					_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
				}
			}
			e.buildTree()
			e.refresh()
			if deletedProject != "" {
				// Deleting a project from the Envs side should delete it
				// from the Collections side too - it's the same "project",
				// just viewed from the other tab. Signal Model to cascade.
				return e, func() tea.Msg {
					return ProjectDeletedMsg{Project: deletedProject}
				}
			}
		}

	case "edit":
		if e.pendingNode != nil && e.pendingNode.Type == VarNode {
			env := e.getEnvForNode(e.pendingNode)
			if env != nil {
				e.pendingNode.Variable.Value = msg.Result.Values["value"].(string)
				e.pendingNode.Variable.Secret = msg.Result.Values["secret"].(bool)
				e.pendingNode.Variable.Active = msg.Result.Values["active"].(bool)
				_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
			}
		}

	case "rename":
		if e.pendingNode != nil {
			newName := msg.Result.Values["input"].(string)
			if newName != "" && newName != e.pendingNode.Name {
				if e.pendingNode.Type == ProjectNode {
					// Renaming a project = updating the Project field on every
					// environment currently grouped under the old name.
					oldName := e.pendingNode.Name
					if oldName == UngroupedProject {
						oldName = ""
					}
					for _, env := range e.environments {
						if env.Project == oldName {
							env.Project = newName
							_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
						}
					}
					// Remember the active-env-per-project mapping under the new name too
					if v, ok := e.activeEnvByProj[oldName]; ok {
						e.activeEnvByProj[newName] = v
						delete(e.activeEnvByProj, oldName)
					}
				} else {
					env := e.getEnvForNode(e.pendingNode)
					if e.pendingNode.Type == EnvNode {
						env.Name = newName
						_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
					} else if env != nil {
						// Rename variable
						v := env.Variables[e.pendingNode.Name]
						delete(env.Variables, e.pendingNode.Name)
						env.Variables[newName] = v
						_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
					}
				}
				e.buildTree()
				e.refresh()
			}
		}

	case "ca_cert":
		if e.pendingNode != nil {
			env := e.getEnvForNode(e.pendingNode)
			if env != nil {
				ca, _ := msg.Result.Values["ca"].(string)
				cert, _ := msg.Result.Values["cert"].(string)
				key, _ := msg.Result.Values["key"].(string)
				env.CACertPath = strings.TrimSpace(ca)
				env.ClientCertPath = strings.TrimSpace(cert)
				env.ClientKeyPath = strings.TrimSpace(key)
				_ = e.saveEnvironment(env) // Error intentionally ignored for UI responsiveness
			}
		}

	case "new_var":
		name := msg.Result.Values["name"].(string)
		value := msg.Result.Values["value"].(string)
		secret := msg.Result.Values["secret"].(bool)
		active := msg.Result.Values["active"].(bool)

		if name != "" && e.pendingNode != nil {
			var targetEnv *api.EnvironmentFile
			if e.pendingNode.Type == EnvNode {
				targetEnv = e.pendingNode.EnvFile
			} else {
				targetEnv = e.pendingNode.EnvFile
			}

			if targetEnv != nil {
				targetEnv.SetVariableFull(name, &api.EnvironmentVariable{
					Value:  value,
					Secret: secret,
					Active: active,
				})
				_ = e.saveEnvironment(targetEnv) // Error intentionally ignored for UI responsiveness
				e.buildTree()
				e.refresh()
			}
		}

	case "new_env":
		name := msg.Result.Values["name"].(string)
		desc := msg.Result.Values["description"].(string)
		project := ""
		if p, ok := msg.Result.Values["project"].(string); ok {
			project = strings.TrimSpace(p)
		}

		e.CreateEnvironment(name, desc, project)
	}

	e.pendingNode = nil
	return e, nil
}

// View renders the environments view
func (e EnvironmentsView) View(width, height int, active bool) string {
	var output []string

	// Count matches for search display
	matchCount := 0
	totalCount := e.countAllNodes()
	if e.searchQuery != "" {
		matchCount = e.countDirectMatches()
	}

	// Render search box if visible
	if e.search.IsVisible() {
		searchBox := e.search.ViewCompact(width, matchCount, totalCount)
		output = append(output, searchBox)
		height -= lipgloss.Height(searchBox) + 1
	} else if e.searchQuery != "" {
		// Show compact filter indicator with count
		filterStyle := lipgloss.NewStyle().
			Foreground(styles.Yellow)
		countStyle := lipgloss.NewStyle().
			Foreground(styles.Subtext0)
		escStyle := lipgloss.NewStyle().
			Foreground(styles.Subtext0).
			Italic(true)
		filterText := filterStyle.Render("/"+e.searchQuery) + countStyle.Render(fmt.Sprintf(" %d/%d", matchCount, totalCount)) + escStyle.Render(" esc")
		output = append(output, filterText)
		height--
	}

	e.height = height

	if len(e.visible) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(styles.Subtext0).
			Width(width).
			Align(lipgloss.Center)
		if e.searchQuery != "" {
			output = append(output, emptyStyle.Render("No matches found"))
		} else {
			output = append(output, emptyStyle.Render("No environments found\n\nPress N to create one\n\n~/.config/lazycurl/environments/"))
		}
		return strings.Join(output, "\n")
	}

	var lines []string
	start := e.scrollOffset
	end := e.scrollOffset + height
	if end > len(e.visible) {
		end = len(e.visible)
	}

	for i := start; i < end && i < len(e.visible); i++ {
		node := e.visible[i]
		line := e.renderNode(node, width, i == e.cursor, active)
		lines = append(lines, line)
	}

	output = append(output, strings.Join(lines, "\n"))
	return strings.Join(output, "\n")
}

// countAllNodes counts total nodes in tree
func (e *EnvironmentsView) countAllNodes() int {
	count := 0
	var walk func(n *EnvTreeNode)
	walk = func(n *EnvTreeNode) {
		count++
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, node := range e.tree {
		walk(node)
	}
	return count
}

// countDirectMatches counts nodes that directly match the search query
func (e *EnvironmentsView) countDirectMatches() int {
	if e.searchQuery == "" {
		return 0
	}
	count := 0
	var walk func(n *EnvTreeNode)
	walk = func(n *EnvTreeNode) {
		if components.MatchesQuery(n.Name, e.searchQuery) {
			count++
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, node := range e.tree {
		walk(node)
	}
	return count
}

// renderNode renders a single tree node with worktree style
func (e *EnvironmentsView) renderNode(node *EnvTreeNode, width int, selected bool, panelActive bool) string {
	var content string

	// Check if this node directly matches the search query
	isDirectMatch := e.searchQuery != "" && components.MatchesQuery(node.Name, e.searchQuery)
	isSearching := e.searchQuery != ""

	// Indent nested nodes (Project -> Environment -> Variable) so the
	// hierarchy is visible, same idea as the Collections tree.
	indent := strings.Repeat("  ", node.Depth())

	switch node.Type {
	case ProjectNode:
		// Project node: ▶/▼ ProjectName
		icon := "▶ "
		if node.Expanded {
			icon = "▼ "
		}
		iconStyle := lipgloss.NewStyle().Bold(true)
		nameStyle := lipgloss.NewStyle().Bold(true)
		if isSearching {
			if isDirectMatch {
				iconStyle = iconStyle.Foreground(styles.SearchMatch)
				nameStyle = nameStyle.Foreground(styles.SearchMatch)
			} else {
				iconStyle = iconStyle.Foreground(styles.SearchDimmed)
				nameStyle = nameStyle.Foreground(styles.SearchDimmed)
			}
		}
		content = indent + iconStyle.Render(icon) + nameStyle.Render(node.Name)

	case EnvNode:
		// Environment node: ▶/▼ EnvName ●
		icon := "▶ "
		if node.Expanded {
			icon = "▼ "
		}

		// Active indicator - compare by file path, not name, since names
		// aren't unique across projects (two projects can both have "Localhost").
		// "●" = actually active right now (this is what requests use today).
		// "○" = this project's remembered pick, but a different project is
		// currently in charge (would become active if you switch back here).
		activeIndicator := ""
		isGloballyActive := node.EnvFile != nil && node.EnvFile.FilePath == e.activeEnvPath
		if e.activeEnvPath == "" && node.Name == e.activeEnvName {
			isGloballyActive = true // legacy fallback before a path was ever recorded
		}
		if isGloballyActive {
			activeIndicator = " ●"
		} else if e.activeEnvByProj[e.projectOfNode(node)] == node.Name {
			activeIndicator = " ○"
		}

		// Apply search styling
		iconStyle := lipgloss.NewStyle()
		nameStyle := lipgloss.NewStyle()
		if isSearching {
			if isDirectMatch {
				iconStyle = iconStyle.Foreground(styles.SearchMatch)
				nameStyle = nameStyle.Foreground(styles.SearchMatch).Bold(true)
			} else {
				iconStyle = iconStyle.Foreground(styles.SearchDimmed)
				nameStyle = nameStyle.Foreground(styles.SearchDimmed)
			}
		}

		content = indent + iconStyle.Render(icon) + nameStyle.Render(node.Name+activeIndicator)

	case VarNode:
		// Worktree style: > []  value_name   value
		// Checkbox for active state (Unicode squares)
		checkbox := "☐"
		checkStyle := lipgloss.NewStyle().Foreground(styles.CheckboxOff)
		if node.Variable.Active {
			checkbox = "☑"
			checkStyle = checkStyle.Foreground(styles.CheckboxOn)
		}

		// Key name
		key := node.Name
		keyStyle := lipgloss.NewStyle().Foreground(styles.Subtext1)

		// Value (masked if secret)
		value := node.Variable.Value
		valueStyle := lipgloss.NewStyle().Foreground(styles.Text)

		if node.Variable.Secret {
			valueStyle = valueStyle.Foreground(styles.SecretColor)
			if len(value) > 0 {
				value = strings.Repeat("*", min(len(value), 10))
			} else {
				value = "***"
			}
		}

		if !node.Variable.Active {
			keyStyle = keyStyle.Foreground(styles.InactiveColor)
			valueStyle = valueStyle.Foreground(styles.InactiveColor)
			checkStyle = checkStyle.Foreground(styles.InactiveColor)
		}

		// Apply search styling (overrides other styles)
		if isSearching {
			if isDirectMatch {
				keyStyle = lipgloss.NewStyle().Foreground(styles.SearchMatch).Bold(true)
				valueStyle = lipgloss.NewStyle().Foreground(styles.SearchMatch)
				checkStyle = lipgloss.NewStyle().Foreground(styles.SearchMatch)
			} else {
				keyStyle = lipgloss.NewStyle().Foreground(styles.SearchDimmed)
				valueStyle = lipgloss.NewStyle().Foreground(styles.SearchDimmed)
				checkStyle = lipgloss.NewStyle().Foreground(styles.SearchDimmed)
			}
		}

		// Build prefix: "> " for selected or "  " for others
		linePrefix := "  "
		if selected {
			linePrefix = "> "
		}

		// Calculate spacing for worktree format: > []  key   value
		checkboxWidth := 3  // "[] " with space
		prefixWidth := 2    // "> " or "  "
		separatorWidth := 3 // "   " between key and value
		availableWidth := width - prefixWidth - checkboxWidth - separatorWidth - len(indent)
		if availableWidth < 10 {
			availableWidth = 10
		}

		// Key width (max 20, min 5)
		keyWidth := availableWidth / 2
		if keyWidth > 20 {
			keyWidth = 20
		}
		if keyWidth < 5 {
			keyWidth = 5
		}

		// Truncate key to fit (no ellipsis - just cut)
		if len(key) > keyWidth {
			key = key[:keyWidth]
		}
		// Pad key to align values
		keyPadded := key + strings.Repeat(" ", keyWidth-len(key))

		// Calculate remaining width for value
		valueWidth := availableWidth - keyWidth
		if valueWidth < 3 {
			valueWidth = 3
		}
		// Truncate value to fit (no ellipsis - just cut)
		if len(value) > valueWidth {
			value = value[:valueWidth]
		}

		content = linePrefix + indent + checkStyle.Render(checkbox) + " " + keyStyle.Render(keyPadded) + "   " + valueStyle.Render(value)
	}

	// Apply selection styling
	style := lipgloss.NewStyle().Width(width)
	if selected {
		if panelActive {
			style = style.Background(styles.SelectedPanelBg).Foreground(styles.SelectedPanelFg).Bold(true)
		} else {
			style = style.Background(styles.SelectedRequestBg).Foreground(styles.SelectedRequestFg)
		}
	}
	// Don't override foreground if not selected - content already has correct colors

	return style.Render(content)
}

// RenderModal renders any active modal
func (e *EnvironmentsView) RenderModal(screenWidth, screenHeight int) string {
	if e.deleteModal.IsVisible() {
		return e.deleteModal.View(screenWidth, screenHeight)
	}
	if e.newVarModal.IsVisible() {
		return e.newVarModal.View(screenWidth, screenHeight)
	}
	if e.newEnvModal.IsVisible() {
		return e.newEnvModal.View(screenWidth, screenHeight)
	}
	if e.editModal.IsVisible() {
		return e.editModal.View(screenWidth, screenHeight)
	}
	if e.renameModal.IsVisible() {
		return e.renameModal.View(screenWidth, screenHeight)
	}
	if e.caCertModal.IsVisible() {
		return e.caCertModal.View(screenWidth, screenHeight)
	}
	return ""
}

// HasActiveModal returns true if any modal is visible
func (e *EnvironmentsView) HasActiveModal() bool {
	return e.hasActiveModal()
}

// GetActiveEnvironment returns the currently active environment
func (e *EnvironmentsView) GetActiveEnvironment() *api.EnvironmentFile {
	// Prefer matching by file path - environment Names are no longer
	// guaranteed unique now that they're grouped by project (two projects
	// can each have a "Localhost" environment).
	if e.activeEnvPath != "" {
		for _, env := range e.environments {
			if env.FilePath == e.activeEnvPath {
				return env
			}
		}
	}
	// Fall back to name-only match (legacy sessions / envs with no FilePath yet)
	for _, env := range e.environments {
		if env.Name == e.activeEnvName {
			return env
		}
	}
	return nil
}

// GetActiveEnvironmentName returns the name of the active environment
func (e *EnvironmentsView) GetActiveEnvironmentName() string {
	return e.activeEnvName
}

// SetActiveEnvironmentName sets the active environment by name
func (e *EnvironmentsView) SetActiveEnvironmentName(name string) {
	// Verify the environment exists before setting
	for _, env := range e.environments {
		if env.Name == name {
			e.activeEnvName = name
			e.rememberActive(env)
			return
		}
	}
	// If not found, keep current or use first available
	if e.activeEnvName == "" && len(e.environments) > 0 {
		e.activeEnvName = e.environments[0].Name
		e.rememberActive(e.environments[0])
	}
}

// SetActiveEnvironmentInProject activates the named environment within a
// specific project - use this over SetActiveEnvironmentName whenever the
// project is known, since environment names aren't guaranteed unique across
// projects (e.g. two projects can each have a "Localhost" environment).
func (e *EnvironmentsView) SetActiveEnvironmentInProject(project, name string) bool {
	for _, env := range e.environments {
		if env.Name == name && env.Project == project {
			e.activeEnvName = env.Name
			e.rememberActive(env)
			return true
		}
	}
	return false
}

// rememberActive records the given environment as the last-active one for
// its project, so switching projects and back restores it.
func (e *EnvironmentsView) rememberActive(env *api.EnvironmentFile) {
	if e.activeEnvByProj == nil {
		e.activeEnvByProj = make(map[string]string)
	}
	e.activeEnvByProj[env.Project] = env.Name
	e.activeEnvPath = env.FilePath
}

// setActiveEnvironmentNode activates the environment an EnvNode points to.
func (e *EnvironmentsView) setActiveEnvironmentNode(node *EnvTreeNode) {
	if node == nil || node.EnvFile == nil {
		return
	}
	e.activeEnvName = node.EnvFile.Name
	e.rememberActive(node.EnvFile)
}

// SwitchToProject activates the appropriate environment for a project when
// the user navigates into a collection bound to it:
//  1. if that project already has a remembered last-active environment, use it
//  2. otherwise, if the project has any environments, activate the first (sorted)
//  3. if the project is empty (no Project given, e.g. collection has none set),
//     do nothing - leave whatever's active alone
//
// Returns true if the active environment changed.
func (e *EnvironmentsView) SwitchToProject(project string) bool {
	if project == "" {
		return false
	}

	// Already on an environment belonging to this project? nothing to do.
	if active := e.GetActiveEnvironment(); active != nil && active.Project == project {
		return false
	}

	if remembered, ok := e.activeEnvByProj[project]; ok {
		if e.SetActiveEnvironmentInProject(project, remembered) {
			e.buildTree()
			e.refresh()
			return true
		}
	}

	// Fall back to the first environment (alphabetically) in that project
	var candidates []*api.EnvironmentFile
	for _, env := range e.environments {
		if env.Project == project {
			candidates = append(candidates, env)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	e.activeEnvName = candidates[0].Name
	e.rememberActive(candidates[0])
	e.buildTree()
	e.refresh()
	return true
}

// GetActiveEnvironmentByProject returns the remembered last-active environment
// name for a project, if any.
func (e *EnvironmentsView) GetActiveEnvironmentByProject() map[string]string {
	return e.activeEnvByProj
}

// SetActiveEnvironmentByProject restores the project->env memory map (used
// when loading a saved session).
func (e *EnvironmentsView) SetActiveEnvironmentByProject(m map[string]string) {
	if m == nil {
		m = make(map[string]string)
	}
	e.activeEnvByProj = m
}

// GetActiveEnvironmentVariables returns the variables of the active environment
func (e *EnvironmentsView) GetActiveEnvironmentVariables() map[string]string {
	env := e.GetActiveEnvironment()
	if env == nil {
		return make(map[string]string)
	}
	// Convert active variables to map
	vars := make(map[string]string)
	for key, v := range env.Variables {
		if v.Active {
			vars[key] = v.Value
		}
	}
	return vars
}

// SaveActiveEnvironment saves the active environment to disk
func (e *EnvironmentsView) SaveActiveEnvironment() error {
	env := e.GetActiveEnvironment()
	if env == nil || env.FilePath == "" {
		return nil
	}
	return api.SaveEnvironment(env, env.FilePath)
}

// GetBreadcrumb returns the breadcrumb path for the current cursor position
func (e *EnvironmentsView) GetBreadcrumb() []string {
	node := e.getCurrentNode()
	if node == nil {
		return []string{}
	}

	if node.Type == ProjectNode {
		return []string{node.Name}
	}

	if node.Type == EnvNode {
		if node.Parent != nil {
			return []string{node.Parent.Name, node.Name}
		}
		return []string{node.Name}
	}

	// VarNode - show project > environment > variable
	if node.Parent != nil {
		if node.Parent.Parent != nil {
			return []string{node.Parent.Parent.Name, node.Parent.Name, node.Name}
		}
		return []string{node.Parent.Name, node.Name}
	}
	return []string{node.Name}
}

// ReloadEnvironments reloads environments from disk
func (e *EnvironmentsView) ReloadEnvironments() {
	e.loadEnvironments()
}
