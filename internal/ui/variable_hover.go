package ui

import (
	"errors"
	"regexp"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kbrdn1/LazyCurl/internal/api"
	"github.com/kbrdn1/LazyCurl/internal/ui/components"
)

var errNoActiveEnvironment = errors.New("no active environment - activate one in the Envs tab first")

// variableTokenPattern matches {{varname}} tokens (mirrors internal/api's pattern).
var variableTokenPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.$]+)\s*\}\}`)

// findVariableAtCursor scans a line of text for {{var}} tokens and returns the
// variable name if the given rune-index cursor position falls within one
// (including the surrounding braces).
func findVariableAtCursor(line string, cursor int) (string, bool) {
	runes := []rune(line)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	locs := variableTokenPattern.FindAllStringSubmatchIndex(line, -1)
	if locs == nil {
		return "", false
	}

	// Convert byte offsets to rune offsets since the string may contain
	// multi-byte runes before the match (unlikely here, but be safe).
	byteToRune := make(map[int]int, len(runes)+1)
	ri := 0
	for bi := range line {
		byteToRune[bi] = ri
		ri++
	}
	byteToRune[len(line)] = ri

	for _, loc := range locs {
		startRune := byteToRune[loc[0]]
		endRune := byteToRune[loc[1]]
		// Cursor is "on" the token if it's anywhere from the opening brace
		// through the closing brace (inclusive), matching how a user would
		// think of "hovering over" {{token}}.
		if cursor >= startRune && cursor <= endRune {
			name := line[loc[2]:loc[3]]
			return name, true
		}
	}
	return "", false
}

// findFirstVariable returns the name of the first {{var}} token found in a
// string, used for contexts without a precise cursor (table row values).
func findFirstVariable(s string) (string, bool) {
	loc := variableTokenPattern.FindStringSubmatch(s)
	if loc == nil {
		return "", false
	}
	return loc[1], true
}

// findHoveredVariable figures out which {{var}} token, if any, the user is
// currently "on" in the Request panel, depending on which sub-tab/context is
// focused:
//   - editing the URL: uses the real cursor position in the URL string
//   - Body tab (JSON editor): uses the editor's real cursor position
//   - Authorization tab: uses whichever field is currently focused
//     (Token/Prefix/Username/Password/API Key name or value)
//   - Headers/Params tabs: no sub-cursor exists in a table row, so it takes
//     the first {{var}} found in the currently selected row's value
func (m *Model) findHoveredVariable() (string, bool) {
	r := m.requestPanel

	if r.IsEditingURL() {
		return findVariableAtCursor(r.url, r.urlCursor)
	}

	if r.tabs.GetActive() == "Body" && r.bodyEditor != nil {
		row, col := r.bodyEditor.GetCursorPosition()
		lines := splitLines(r.bodyEditor.GetContent())
		if row >= 0 && row < len(lines) {
			return findVariableAtCursor(lines[row], col)
		}
		return "", false
	}

	// Authorization tab doesn't use a table like Headers/Params - it has its
	// own bespoke fields (Type/Token/Prefix/Username/etc), tracked via
	// r.authField. Check whichever one is currently focused.
	if r.tabs.GetActive() == "Authorization" {
		var val string
		switch r.authField {
		case AuthFieldToken:
			val = r.authToken
		case AuthFieldPrefix:
			val = r.authPrefix
		case AuthFieldUsername:
			val = r.authUsername
		case AuthFieldPassword:
			val = r.authPassword
		case AuthFieldAPIKeyName:
			val = r.authAPIKeyName
		case AuthFieldAPIKeyValue:
			val = r.authAPIKeyValue
		}
		return findFirstVariable(val)
	}

	if table := r.getCurrentTable(); table != nil {
		if table.Cursor >= 0 && table.Cursor < len(table.Rows) {
			row := table.Rows[table.Cursor]
			if name, ok := findFirstVariable(row.Value); ok {
				return name, true
			}
			return findFirstVariable(row.Key)
		}
	}

	return "", false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

// openVariableEditModal opens the quick-edit modal for the given variable
// name, prefilled from the active environment if it already exists there.
func (m *Model) openVariableEditModal(name string) {
	env := m.leftPanel.GetEnvironments().GetActiveEnvironment()
	if env == nil {
		m.statusBar.Error(errNoActiveEnvironment)
		return
	}

	m.pendingVarName = name
	if v, exists := env.Variables[name]; exists {
		m.pendingVarIsNew = false
		m.variableEditModal.SetFieldValue("value", v.Value)
		m.variableEditModal.SetFieldValue("secret", boolToStr(v.Secret))
		m.variableEditModal.SetFieldValue("active", boolToStr(v.Active))
		m.variableEditModal.Title = "Edit Variable: " + name
	} else {
		m.pendingVarIsNew = true
		m.variableEditModal.SetFieldValue("value", "")
		m.variableEditModal.SetFieldValue("secret", "false")
		m.variableEditModal.SetFieldValue("active", "true")
		m.variableEditModal.Title = "New Variable: " + name + " (in " + env.Name + ")"
	}
	m.variableEditModal.Show()
}

// handleVariableModalClose persists the edit (or cancels) and closes the modal.
func (m Model) handleVariableModalClose(msg components.ModalCloseMsg) (tea.Model, tea.Cmd) {
	m.variableEditModal.Hide()
	if !msg.Result.Confirmed {
		return m, nil
	}

	env := m.leftPanel.GetEnvironments().GetActiveEnvironment()
	if env == nil || m.pendingVarName == "" {
		return m, nil
	}

	value, _ := msg.Result.Values["value"].(string)
	secret, _ := msg.Result.Values["secret"].(bool)
	active, _ := msg.Result.Values["active"].(bool)

	if env.Variables == nil {
		env.Variables = make(map[string]*api.EnvironmentVariable)
	}
	if v, exists := env.Variables[m.pendingVarName]; exists {
		v.Value = value
		v.Secret = secret
		v.Active = active
	} else {
		env.Variables[m.pendingVarName] = &api.EnvironmentVariable{
			Value:  value,
			Secret: secret,
			Active: active,
		}
	}

	if err := m.leftPanel.GetEnvironments().SaveActiveEnvironment(); err != nil {
		m.statusBar.Error(err)
	} else {
		m.statusBar.Success("Variable", m.pendingVarName+" saved to "+env.Name)
	}

	return m, nil
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

