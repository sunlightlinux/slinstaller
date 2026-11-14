package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sunlightlinux/slinstaller/pkg/config"
)

// userEntry is one account in the Manage User table.
type userEntry struct {
	Login    string
	UserName string
	Password string // plaintext in the TUI; hashed when the config is built
	Admin    bool
}

// --- Manage User: the clr-style account table ---

// userButtons are the action rows below the table; the cursor runs over the
// user rows first, then these.
var userButtons = []string{"Add New User", "Revert", "Confirm", "Cancel"}

// updateUserList drives the Manage User table: move over the account rows
// and the action buttons, delete a selected account, or open the Add New
// User form.
func (m model) updateUserList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	last := len(m.users) + len(userButtons) - 1
	switch key.String() {
	case "esc":
		m.view = viewHub
	// Move backward over rows + buttons; Tab/arrows wrap so every control is
	// reachable (the four buttons sit on one line).
	case "up", "k", "left", "h", "shift+tab":
		if m.userCursor > 0 {
			m.userCursor--
		} else {
			m.userCursor = last
		}
	case "down", "j", "right", "l", "tab":
		if m.userCursor < last {
			m.userCursor++
		} else {
			m.userCursor = 0
		}
	case "a":
		return m.startUserForm()
	case "d":
		if m.userCursor < len(m.users) {
			m.users = append(m.users[:m.userCursor], m.users[m.userCursor+1:]...)
			if m.userCursor > 0 {
				m.userCursor--
			}
		}
	case "enter":
		if m.userCursor < len(m.users) {
			// On a user row: edit that account.
			return m.startUserEdit(m.userCursor)
		}
		switch m.userCursor - len(m.users) {
		case 0: // Add New User
			return m.startUserForm()
		case 1: // Revert
			m.users = nil
			m.userCursor = 0
		default: // Confirm / Cancel both return to the hub
			m.view = viewHub
		}
	}
	return m, nil
}

// startUserForm opens the form to add a new account.
func (m model) startUserForm() (tea.Model, tea.Cmd) {
	return m.openUserForm(-1, userEntry{})
}

// startUserEdit opens the form pre-filled with an existing account.
func (m model) startUserEdit(i int) (tea.Model, tea.Cmd) {
	return m.openUserForm(i, m.users[i])
}

// openUserForm opens the Add/Edit User form: idx is -1 to add, or the row
// being edited, and u seeds the fields.
func (m model) openUserForm(idx int, u userEntry) (tea.Model, tea.Cmd) {
	m.view = viewUserForm
	m.formStep = 0
	m.edCursor = 0
	m.editUserIdx = idx
	m.userForm = u
	m.formErr, m.pwConfirm = "", ""
	ti := textinput.New()
	ti.Focus()
	ti.SetValue(u.Login)
	m.input = ti
	return m, textinput.Blink
}

// The steps of the Add/Edit User form.
const (
	userStepLogin = iota
	userStepName
	userStepPassword
	userStepConfirm
	userStepAdmin
)

// updateUserForm drives the Add/Edit User form: Login, User Name,
// Password, the password again, then an Admin yes/no choice. Each entry is
// checked as clr-installer's form checks it (config.ValidLogin, the
// password length, the two passwords matching), and a refused one stays
// on its step with the reason shown, rather than failing the install.
func (m model) updateUserForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.view = viewUserList
		return m, nil
	}
	if m.formStep == userStepAdmin {
		switch key.String() {
		case "up", "k", "down", "j", "left", "h", "right", "l", "tab":
			m.edCursor = 1 - m.edCursor
		case "enter":
			m.userForm.Admin = m.edCursor == 0 // index 0 = Yes
			if m.editUserIdx >= 0 && m.editUserIdx < len(m.users) {
				m.users[m.editUserIdx] = m.userForm // edit in place
				m.userCursor = m.editUserIdx
			} else {
				m.users = append(m.users, m.userForm)
				m.userCursor = len(m.users) - 1
			}
			m.view = viewUserList
		}
		return m, nil
	}
	if key.String() == "enter" {
		m.formErr = ""
		switch m.formStep {
		case userStepLogin:
			v := strings.TrimSpace(m.input.Value())
			if err := config.ValidLogin(v); err != nil {
				m.formErr = err.Error()
				return m, nil
			}
			for i, u := range m.users {
				if i != m.editUserIdx && u.Login == v {
					m.formErr = fmt.Sprintf("login %q is already in the list", v)
					return m, nil
				}
			}
			m.userForm.Login = v
		case userStepName: // optional
			m.userForm.UserName = strings.TrimSpace(m.input.Value())
		case userStepPassword: // not trimmed
			if err := config.ValidPassword(m.input.Value()); err != nil {
				m.formErr = err.Error()
				return m, nil
			}
			m.userForm.Password = m.input.Value()
		case userStepConfirm:
			if m.input.Value() != m.userForm.Password {
				// Back to the first entry: either may hold the typo.
				m.formErr = "the passwords do not match; type it again"
				m.formStep = userStepPassword
				m.input = passwordInput("")
				return m, textinput.Blink
			}
		}
		m.formStep++
		if m.formStep == userStepAdmin {
			// A new account defaults to administrator; editing keeps the
			// account's current value.
			m.edCursor = 0
			if m.editUserIdx >= 0 && !m.userForm.Admin {
				m.edCursor = 1
			}
			return m, nil
		}
		switch m.formStep {
		case userStepName:
			m.input = textInputWith(m.userForm.UserName)
		case userStepPassword:
			m.input = passwordInput(m.userForm.Password)
		case userStepConfirm:
			// Editing an account keeps its password unless it is changed:
			// the confirmation starts filled in with the same one.
			pre := ""
			if m.editUserIdx >= 0 && m.editUserIdx < len(m.users) && m.users[m.editUserIdx].Password == m.userForm.Password {
				pre = m.userForm.Password
			}
			m.input = passwordInput(pre)
		}
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func textInputWith(v string) textinput.Model {
	ti := textinput.New()
	ti.Focus()
	ti.SetValue(v)
	return ti
}

func passwordInput(v string) textinput.Model {
	ti := textInputWith(v)
	ti.EchoMode = textinput.EchoPassword
	return ti
}

// hasAdmin reports whether at least one account is an administrator — the
// install needs one (for sudo/wheel), as clr requires.
func hasAdmin(users []userEntry) bool {
	for _, u := range users {
		if u.Admin {
			return true
		}
	}
	return false
}

// adminTag is " (admin)" for an administrator account, "" otherwise.
func adminTag(admin bool) string {
	if admin {
		return " (admin)"
	}
	return ""
}

// userListView renders the Manage User table (Login / User Name / Password
// / Admin) and the action buttons, in clr's layout.
func (m model) userListView() string {
	w := m.panelWidth()
	body := []string{
		itemLabel.Width(w).Render("  Manage User"),
		rule(w),
		itemLabel.Width(w).Render(fmt.Sprintf("  %-16s %-18s %-10s %s", "Login", "User Name", "Password", "Admin")),
	}
	if len(m.users) == 0 {
		body = append(body, footer.Width(w).Render("  *** No Custom Users Defined ***"))
	}
	for i, u := range m.users {
		admin := "no"
		if u.Admin {
			admin = "yes"
		}
		line := fmt.Sprintf("%-16s %-18s %-10s %s", u.Login, orDash(u.UserName), "****", admin)
		body = append(body, row(w, i == m.userCursor, line))
	}
	body = append(body, lineBG.Width(w).Render(""))

	// Buttons: Add New User / Revert / Confirm / Cancel.
	var btns []button
	for i, b := range userButtons {
		btns = append(btns, button{b, pick(m.userCursor == len(m.users)+i)})
	}
	return m.screenButtons(body, []string{buttonLine(w, btns...)},
		"[Tab/Arrows] move · [Enter] select/edit · [a] add · [d] delete · [Esc] back")
}

// userFormView renders the Add/Edit User form: one field at a time, then the
// Admin yes/no choice.
func (m model) userFormView() string {
	w := m.panelWidth()
	heading := "Add New User"
	done := "add user"
	if m.editUserIdx >= 0 {
		heading = "Edit User"
		done = "save user"
	}
	if m.formStep == userStepAdmin {
		body := []string{
			itemLabel.Width(w).Render("  " + heading),
			rule(w),
			itemLabel.Width(w).Render("  Administrator (sudo / wheel)?"),
			lineBG.Width(w).Render(""),
		}
		for i, opt := range []string{"Yes", "No"} {
			body = append(body, row(w, i == m.edCursor, opt))
		}
		return m.screen(body, "[Left/Right] choose · [Enter] "+done+" · [Esc] cancel")
	}
	var title, hint string
	switch m.formStep {
	case 0:
		title, hint = "Login", "the account name, e.g. ionut"
	case 1:
		title, hint = "User Name", "the full name (optional)"
	case userStepPassword:
		title, hint = "Password", fmt.Sprintf("at least %d characters, not shown as you type", config.MinPasswordLength)
	case userStepConfirm:
		title, hint = "Confirm Password", "type the password again"
	}
	body := []string{
		itemLabel.Width(w).Render("  " + heading),
		rule(w),
		itemLabel.Width(w).Render("  " + title),
		footer.Width(w).Render("  " + hint),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	if m.formErr != "" {
		body = append(body, lineBG.Width(w).Render(""), warnText.Width(w).Render("  "+m.formErr))
	}
	return m.screen(body, "type · [Enter] next · [Esc] cancel")
}
