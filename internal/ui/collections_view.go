package ui

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kbrdn1/LazyCurl/internal/api"
	"github.com/kbrdn1/LazyCurl/internal/config"
	"github.com/kbrdn1/LazyCurl/internal/ui/components"
)

// CollectionsView represents the collections panel
type CollectionsView struct {
	workspacePath   string
	collectionsPath string // where NEW collections get saved - global by default
	legacyLocalPath string // old per-workspace .lazycurl/collections, still loaded (not saved to) for backward compat
	tree            *components.Tree
	collections     []*api.CollectionFile
	clipboard       *components.TreeNode // For yank/paste
}

// NewCollectionsView creates a new collections view
func NewCollectionsView(workspacePath string) *CollectionsView {
	cv := &CollectionsView{
		workspacePath: workspacePath,
		// New collections save to global scope by default, so they show up
		// no matter which directory you launch lazycurl from - previously
		// this defaulted to a per-workspace `.lazycurl/collections`, which
		// meant collections were siloed per-cwd unless you deliberately
		// tagged them into a global-scope one.
		collectionsPath: config.GetGlobalCollectionsPath(),
		// Any collections that already exist in the old per-workspace
		// location keep loading (and can still be edited/deleted in place)
		// so nothing already saved there silently disappears.
		legacyLocalPath: filepath.Join(workspacePath, ".lazycurl", "collections"),
	}

	// Load collections from workspace
	cv.loadCollections()

	return cv
}

// loadCollections loads collections from the legacy per-workspace path (for
// backward compat with anything saved there before global-by-default),
// merged with the global (cross-workspace) collections directory, which is
// also where new collections get saved.
func (c *CollectionsView) loadCollections() {
	local, err := api.LoadAllCollections(c.legacyLocalPath)
	if err != nil {
		local = []*api.CollectionFile{}
	}

	global, err := api.LoadAllCollections(c.collectionsPath)
	if err != nil {
		global = []*api.CollectionFile{}
	}

	c.collections = mergeCollectionsByPath(local, global)
	c.tree = components.NewTree(c.collections)
}

// mergeCollectionsByPath combines two collection slices, de-duplicating by
// file path (the same collection loaded twice would otherwise show up twice).
func mergeCollectionsByPath(a, b []*api.CollectionFile) []*api.CollectionFile {
	seen := make(map[string]bool, len(a))
	merged := make([]*api.CollectionFile, 0, len(a)+len(b))
	for _, c := range a {
		seen[c.FilePath] = true
		merged = append(merged, c)
	}
	for _, c := range b {
		if !seen[c.FilePath] {
			merged = append(merged, c)
		}
	}
	return merged
}

// ReloadCollections reloads collections from disk while preserving tree state
func (c *CollectionsView) ReloadCollections() {
	// Save current tree state before reload
	var state *components.TreeState
	if c.tree != nil {
		state = c.tree.SaveState()
	}

	// Reload collections
	c.loadCollections()

	// Restore tree state after reload
	if state != nil && c.tree != nil {
		c.tree.RestoreState(state)
	}
}

// Update handles messages for the collections view
func (c CollectionsView) Update(msg tea.Msg, cfg *config.GlobalConfig) (CollectionsView, tea.Cmd) {
	// Forward all messages to tree component (including SearchUpdateMsg, SearchCloseMsg)
	allowNavigation := true
	tree, cmd := c.tree.Update(msg, allowNavigation)
	c.tree = tree
	return c, cmd
}

// View renders the collections view
func (c CollectionsView) View(width, height int, active bool) string {
	return c.tree.View(width, height, active)
}

// Selected returns the currently selected tree node
func (c CollectionsView) Selected() *components.TreeNode {
	return c.tree.Selected()
}

// GetTree returns the tree component for external access
func (c CollectionsView) GetTree() *components.Tree {
	return c.tree
}

// SetClipboard sets the clipboard node for copy/paste
func (c *CollectionsView) SetClipboard(node *components.TreeNode) {
	c.clipboard = node
}

// GetClipboard returns the clipboard node
func (c *CollectionsView) GetClipboard() *components.TreeNode {
	return c.clipboard
}

// GetCollectionsPath returns the path to collections directory
func (c *CollectionsView) GetCollectionsPath() string {
	return c.collectionsPath
}

// GetCollections returns the loaded collections
func (c *CollectionsView) GetCollections() []*api.CollectionFile {
	return c.collections
}

// HasProject returns true if any collection is tagged with the given project.
func (c *CollectionsView) HasProject(project string) bool {
	if project == "" {
		return false
	}
	for _, col := range c.collections {
		if col.Project == project {
			return true
		}
	}
	return false
}

// DeleteCollectionsForProject removes every collection file tagged with the
// given project (used when a project is deleted from the Environments side -
// deleting "the project" should remove it from Collections too, not leave a
// same-named collection behind with no environment left to switch to).
// Returns the number of collections removed.
func (c *CollectionsView) DeleteCollectionsForProject(project string) int {
	if project == "" {
		return 0
	}
	removed := 0
	var remaining []*api.CollectionFile
	for _, col := range c.collections {
		if col.Project != project {
			remaining = append(remaining, col)
			continue
		}
		if col.FilePath != "" {
			_ = os.Remove(col.FilePath) // Error intentionally ignored for UI responsiveness
		}
		removed++
	}
	c.collections = remaining
	c.tree = components.NewTree(c.collections)
	return removed
}

// FindCollectionByNode finds the collection that contains a tree node
func (c *CollectionsView) FindCollectionByNode(node *components.TreeNode) *api.CollectionFile {
	if node == nil {
		return nil
	}

	// Find the root collection node by walking up the parent chain
	root := node
	for root.Parent != nil {
		root = root.Parent
	}

	// Find the collection with matching name
	for _, col := range c.collections {
		if col.Name == root.Name {
			return col
		}
	}

	return nil
}

// GetFolderPath returns the folder path from a node to its collection
func (c *CollectionsView) GetFolderPath(node *components.TreeNode) []string {
	if node == nil {
		return nil
	}

	var path []string
	current := node

	// Walk up to collection (skip the collection itself)
	for current.Parent != nil {
		if current.Type == components.FolderNode {
			path = append([]string{current.Name}, path...)
		}
		current = current.Parent
	}

	return path
}

// AddRequestToCollection adds a new request to the appropriate collection
func (c *CollectionsView) AddRequestToCollection(name, method, url string, parentNode *components.TreeNode) error {
	col := c.FindCollectionByNode(parentNode)
	if col == nil {
		// No collection exists, create one
		return c.createDefaultCollectionWithRequest(name, method, url)
	}

	req := &api.CollectionRequest{
		ID:     api.GenerateID(),
		Name:   name,
		Method: api.HTTPMethod(method),
		URL:    url,
		Headers: []api.KeyValueEntry{
			{Key: "Content-Type", Value: "application/json", Enabled: true},
			{Key: "Accept", Value: "*/*", Enabled: true},
			{Key: "User-Agent", Value: "LazyCurl/1.0", Enabled: true},
		},
	}

	// Get folder path
	folderPath := c.GetFolderPath(parentNode)

	// If parent is a folder, use its path; if it's a request, use its parent's path
	if parentNode != nil && parentNode.Type == components.RequestNode && parentNode.Parent != nil {
		folderPath = c.GetFolderPath(parentNode.Parent)
	}

	if err := col.AddRequestToFolder(folderPath, req); err != nil {
		return err
	}

	return col.Save()
}

// createDefaultCollectionWithRequest creates a new collection with a request
func (c *CollectionsView) createDefaultCollectionWithRequest(name, method, url string) error {
	col := &api.CollectionFile{
		Name:     "New Collection",
		Requests: []api.CollectionRequest{},
		Folders:  []api.Folder{},
		FilePath: filepath.Join(c.collectionsPath, "collection.json"),
	}

	req := &api.CollectionRequest{
		ID:     api.GenerateID(),
		Name:   name,
		Method: api.HTTPMethod(method),
		URL:    url,
		Headers: []api.KeyValueEntry{
			{Key: "Content-Type", Value: "application/json", Enabled: true},
			{Key: "Accept", Value: "*/*", Enabled: true},
			{Key: "User-Agent", Value: "LazyCurl/1.0", Enabled: true},
		},
	}

	col.AddRequest(req)
	return col.Save()
}

// CreateEmptyCollection creates a brand-new, empty collection - optionally
// tagged with a Project - and adds it to the tree. This is the entry point
// for bootstrapping a project from scratch (e.g. pressing P in the
// Collections panel with nothing selected yet, since there's no existing
// collection node to assign a project onto).
func (c *CollectionsView) CreateEmptyCollection(name, project string) (*api.CollectionFile, error) {
	if name == "" {
		name = "New Collection"
	}
	filename := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if filename == "" {
		filename = "collection"
	}
	col := &api.CollectionFile{
		Name:     name,
		Project:  project,
		Requests: []api.CollectionRequest{},
		Folders:  []api.Folder{},
		FilePath: filepath.Join(c.collectionsPath, filename+".json"),
	}
	if err := col.Save(); err != nil {
		return nil, err
	}
	c.ReloadCollections()
	return col, nil
}

// AddFolderToCollection adds a new folder to the appropriate collection
func (c *CollectionsView) AddFolderToCollection(name string, parentNode *components.TreeNode) error {
	col := c.FindCollectionByNode(parentNode)
	if col == nil {
		// Create a new collection with the folder
		return c.createDefaultCollectionWithFolder(name)
	}

	// Get folder path for parent
	folderPath := c.GetFolderPath(parentNode)

	if err := col.CreateFolderInPath(folderPath, name); err != nil {
		return err
	}

	return col.Save()
}

// createDefaultCollectionWithFolder creates a new collection with a folder
func (c *CollectionsView) createDefaultCollectionWithFolder(name string) error {
	col := &api.CollectionFile{
		Name:     "New Collection",
		Requests: []api.CollectionRequest{},
		Folders:  []api.Folder{},
		FilePath: filepath.Join(c.collectionsPath, "collection.json"),
	}

	col.CreateFolder(name)
	return col.Save()
}

// RenameNode renames a tree node (request or folder)
func (c *CollectionsView) RenameNode(node *components.TreeNode, newName string) error {
	if node == nil {
		return nil
	}

	col := c.FindCollectionByNode(node)
	if col == nil {
		return nil
	}

	switch node.Type {
	case components.CollectionNode:
		col.Name = newName
	case components.FolderNode:
		// Get parent path
		parentPath := c.GetFolderPath(node.Parent)
		col.RenameFolder(parentPath, node.Name, newName)
	case components.RequestNode:
		col.RenameRequest(node.ID, newName)
	}

	return col.Save()
}

// UpdateRequest updates a request node's name, method, and URL
func (c *CollectionsView) UpdateRequest(node *components.TreeNode, name, method, url string) error {
	if node == nil || node.Type != components.RequestNode {
		return nil
	}

	col := c.FindCollectionByNode(node)
	if col == nil {
		return nil
	}

	col.UpdateRequest(node.ID, name, api.HTTPMethod(method), url)
	return col.Save()
}

// UpdateRequestURLByID finds a request by ID across all collections and updates its URL
func (c *CollectionsView) UpdateRequestURLByID(requestID, newURL string) error {
	if requestID == "" {
		return nil
	}

	// Search through all collections
	for _, col := range c.collections {
		if col.UpdateRequestURL(requestID, newURL) {
			return col.Save()
		}
	}

	return nil
}

// UpdateRequestBodyByID finds a request by ID across all collections and updates its body
func (c *CollectionsView) UpdateRequestBodyByID(requestID, bodyType, content string) error {
	if requestID == "" {
		return nil
	}

	// Search through all collections
	for _, col := range c.collections {
		if col.UpdateRequestBody(requestID, bodyType, content) {
			return col.Save()
		}
	}

	return nil
}

// UpdateRequestScriptsByID finds a request by ID across all collections and updates its scripts
func (c *CollectionsView) UpdateRequestScriptsByID(requestID, preRequest, postRequest string) error {
	if requestID == "" {
		return nil
	}

	// Search through all collections
	for _, col := range c.collections {
		if col.UpdateRequestScripts(requestID, preRequest, postRequest) {
			return col.Save()
		}
	}

	return nil
}

// UpdateRequestAuthByID finds a request by ID across all collections and updates its auth
func (c *CollectionsView) UpdateRequestAuthByID(requestID string, auth *api.AuthConfig) error {
	if requestID == "" {
		return nil
	}

	// Search through all collections
	for _, col := range c.collections {
		if col.UpdateRequestAuth(requestID, auth) {
			return col.Save()
		}
	}

	return nil
}

// DeleteNode deletes a tree node (request or folder)
func (c *CollectionsView) DeleteNode(node *components.TreeNode) error {
	if node == nil {
		return nil
	}

	col := c.FindCollectionByNode(node)
	if col == nil {
		return nil
	}

	switch node.Type {
	case components.CollectionNode:
		// Delete the entire collection: remove its file from disk, drop it
		// from the in-memory list, and rebuild the tree. This used to be a
		// silent no-op ("not implemented for safety") - the confirm dialog
		// above (are you sure you want to delete 'X'?) already gates this,
		// so there's no reason for it to do nothing after you confirm.
		if col.FilePath != "" {
			if err := os.Remove(col.FilePath); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		for i, existing := range c.collections {
			if existing == col {
				c.collections = append(c.collections[:i], c.collections[i+1:]...)
				break
			}
		}
		c.tree = components.NewTree(c.collections)
		return nil
	case components.FolderNode:
		parentPath := c.GetFolderPath(node.Parent)
		col.DeleteFolder(parentPath, node.Name)
	case components.RequestNode:
		col.DeleteRequest(node.ID)
	}

	return col.Save()
}

// DuplicateNode duplicates a tree node (request or folder)
func (c *CollectionsView) DuplicateNode(node *components.TreeNode) error {
	if node == nil {
		return nil
	}

	col := c.FindCollectionByNode(node)
	if col == nil {
		return nil
	}

	switch node.Type {
	case components.RequestNode:
		col.DuplicateRequest(node.ID)
	case components.FolderNode:
		parentPath := c.GetFolderPath(node.Parent)
		col.DuplicateFolder(parentPath, node.Name)
	case components.CollectionNode:
		// Cannot duplicate collection
		return nil
	}

	return col.Save()
}

// PasteNode pastes clipboard content to target location
// Target logic:
// - If target is a folder/collection: paste inside it
// - If target is a request: paste in same folder as the request
func (c *CollectionsView) PasteNode(clipboard *components.TreeNode, target *components.TreeNode) error {
	if clipboard == nil {
		return nil
	}

	// Find source collection
	sourceCol := c.FindCollectionByNode(clipboard)
	if sourceCol == nil {
		return nil
	}

	// Find target collection
	targetCol := c.FindCollectionByNode(target)
	if targetCol == nil {
		// If no target, use source collection root
		targetCol = sourceCol
	}

	// Determine target folder path based on cursor position
	var targetFolderPath []string
	if target != nil {
		switch target.Type {
		case components.CollectionNode:
			// Paste at collection root
			targetFolderPath = nil
		case components.FolderNode:
			// Paste inside the folder
			targetFolderPath = c.GetFolderPathIncluding(target)
		case components.RequestNode:
			// Paste in same folder as the request
			targetFolderPath = c.GetFolderPath(target.Parent)
		}
	}

	// Copy based on clipboard type
	switch clipboard.Type {
	case components.RequestNode:
		targetCol.CopyRequestToFolder(clipboard.ID, targetFolderPath)
	case components.FolderNode:
		sourcePath := c.GetFolderPath(clipboard.Parent)
		targetCol.CopyFolderToFolder(sourcePath, clipboard.Name, targetFolderPath)
	case components.CollectionNode:
		// Cannot paste collection
		return nil
	}

	return targetCol.Save()
}

// GetFolderPathIncluding returns the folder path including the node itself
func (c *CollectionsView) GetFolderPathIncluding(node *components.TreeNode) []string {
	if node == nil || node.Type != components.FolderNode {
		return nil
	}

	path := c.GetFolderPath(node.Parent)
	return append(path, node.Name)
}

// SelectIndex selects an item by its visual index in the tree
func (c *CollectionsView) SelectIndex(index int) {
	if c.tree != nil {
		c.tree.SelectIndex(index)
	}
}

// GetJumpTargets returns jump targets for visible items in the tree viewport.
// startRow and startCol define the offset for label positioning in the panel.
// Only items within the visible viewport height are included as targets.
func (c *CollectionsView) GetJumpTargets(startRow, startCol int) []JumpTarget {
	if c.tree == nil {
		return nil
	}

	items := c.tree.GetVisibleItems()
	scrollOffset := c.tree.GetScrollOffset()
	viewportHeight := c.tree.GetHeight()

	targets := make([]JumpTarget, 0, len(items))

	for i, node := range items {
		// Calculate visible row (accounting for scroll offset and panel header)
		visibleIdx := i - scrollOffset
		if visibleIdx < 0 {
			continue // Item is scrolled above view
		}

		// Stop if item is below the visible viewport
		if viewportHeight > 0 && visibleIdx >= viewportHeight {
			break
		}

		// Determine action based on node type
		action := JumpSelect
		if node.Type == components.FolderNode || node.Type == components.CollectionNode {
			action = JumpActivate // Expand/collapse folders
		}

		target := JumpTarget{
			Panel:     CollectionsPanel,
			Row:       startRow + visibleIdx + 1,   // +1 for header row
			Col:       startCol + (node.Depth * 2), // Indent by depth
			Index:     i,
			ElementID: node.ID,
			Action:    action,
		}
		targets = append(targets, target)
	}

	return targets
}
