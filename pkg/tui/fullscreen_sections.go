package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// bundleEntry is one row of the Select Additional Bundles checklist: a short
// name, a description, and the xbps packages it adds when ticked. Clear's
// bundle names do not map to Sunlight, so this is a small curated set of
// common extras.
type bundleEntry struct {
	name  string
	desc  string
	packs []string
}

var bundleOptions = []bundleEntry{
	{"vim", "Vi IMproved - improved text editor", []string{"vim"}},
	{"nano", "Nano - simple text editor", []string{"nano"}},
	{"git", "Git - version control", []string{"git"}},
	{"curl", "curl - command-line data transfer", []string{"curl"}},
	{"wget", "wget - file downloader", []string{"wget"}},
	{"htop", "htop - interactive process viewer", []string{"htop"}},
	{"tmux", "tmux - terminal multiplexer", []string{"tmux"}},
	{"rsync", "rsync - fast file copy and sync", []string{"rsync"}},
	{"man-pages", "Manual pages and reader", []string{"man-pages", "mdocml"}},
	{"bash-completion", "Bash tab-completion", []string{"bash-completion"}},
	{"openssh", "OpenSSH client and server", []string{"openssh"}},
	{"sudo", "sudo - run commands as another user", []string{"sudo"}},
	{"docker", "Docker Engine and containerd, started at boot", []string{"docker", "containerd"}},
}

// updateMiniForm drives the two-field forms: Kernel Command Line (Add then
// Remove arguments) and Swupd Mirror (URL then an allow-insecure-http
// checkbox).
func (m model) updateMiniForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.view = viewHub
		return m, nil
	}
	switch m.editing {
	case secKernelArgs:
		if key.String() == "enter" {
			v := strings.TrimSpace(m.input.Value())
			if m.formStep == 0 {
				m.kernelArgs = v
				m.formStep = 1
				ti := textinput.New()
				ti.Focus()
				ti.SetValue(m.kernelArgsRemove)
				m.input = ti
				return m, textinput.Blink
			}
			m.kernelArgsRemove = v
			m.view = viewHub
			return m, nil
		}
	case secKdump:
		if key.String() == "enter" {
			// A space is never part of either value, and one inside
			// crashkernel= would split it into two arguments at boot.
			v := strings.Join(strings.Fields(m.input.Value()), "")
			if m.formStep == 0 {
				m.crashkernel = v // empty turns kdump off
				m.formStep = 1
				ps := m.panicSecs
				if ps == "" && v != "" {
					ps = defaultPanicSecs
				}
				ti := textinput.New()
				ti.Focus()
				ti.SetValue(ps)
				m.input = ti
				return m, textinput.Blink
			}
			if _, err := strconv.Atoi(v); v != "" && err != nil {
				m.formErr = "panic= takes a whole number of seconds (or leave it empty)"
				return m, nil
			}
			m.panicSecs, m.formErr = v, ""
			m.view = viewHub
			return m, nil
		}
	case secMirror:
		if m.formStep == 1 { // the allow-insecure checkbox
			switch key.String() {
			case " ", "x", "left", "right", "up", "down", "tab":
				m.allowInsecure = !m.allowInsecure
			case "enter":
				m.view = viewHub
			}
			return m, nil
		}
		if key.String() == "enter" { // URL step
			m.mirror = strings.TrimSpace(m.input.Value())
			m.formStep = 1
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// updateBundles drives the Select Additional Bundles checklist: arrows move,
// Space toggles, Enter confirms.
// Focus on a list page with a Cancel / Confirm pair below it.
const (
	focusList = iota
	focusCancel
	focusConfirm
	focusDetails
)

// focusStops are the focusable positions below the current page's list, in
// Tab order. The kernel-profile picker adds a Details button; other list
// pages have just Cancel / Confirm.
func (m model) focusStops() []int {
	if m.view == viewList && m.editing == secKernelArgs {
		return []int{focusList, focusDetails, focusCancel, focusConfirm}
	}
	return []int{focusList, focusCancel, focusConfirm}
}

// moveButtonFocus handles the keys that move focus between a page's list
// and its Cancel / Confirm buttons: Tab and Shift+Tab cycle list, Cancel,
// Confirm; Left and Right move between the buttons; Up from a button goes
// back to the list. It reports whether it used the key.
func (m *model) moveButtonFocus(key tea.KeyMsg) bool {
	stops := m.focusStops()
	cur := 0
	for i, s := range stops {
		if s == m.btnFocus {
			cur = i
		}
	}
	switch key.String() {
	case "tab":
		m.btnFocus = stops[(cur+1)%len(stops)]
	case "shift+tab":
		m.btnFocus = stops[(cur-1+len(stops))%len(stops)]
	case "left", "right":
		if m.btnFocus == focusList {
			return false
		}
		btns := stops[1:] // the buttons, excluding the list itself
		bi := 0
		for i, s := range btns {
			if s == m.btnFocus {
				bi = i
			}
		}
		if key.String() == "right" {
			bi = (bi + 1) % len(btns)
		} else {
			bi = (bi - 1 + len(btns)) % len(btns)
		}
		m.btnFocus = btns[bi]
	case "up":
		if m.btnFocus == focusList {
			return false
		}
		m.btnFocus = focusList
	default:
		return false
	}
	return true
}

// cancelConfirm renders a page's Cancel / Confirm pair, the focused one
// highlighted; with the list focused neither is.
func (m model) cancelConfirm(w int) string {
	return buttonLine(w,
		button{"Cancel", pick(m.btnFocus == focusCancel)},
		button{"Confirm", pick(m.btnFocus == focusConfirm)})
}

func (m model) updateBundles(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.moveButtonFocus(key) {
		return m, nil
	}
	if key.String() == "enter" && m.btnFocus == focusCancel {
		m.view = viewHub
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "up", "k":
		if m.bundleCursor > 0 {
			m.bundleCursor--
		}
	case "down", "j":
		if m.bundleCursor < len(bundleOptions)-1 {
			m.bundleCursor++
		}
	case " ", "x":
		m.bundleChecked[m.bundleCursor] = !m.bundleChecked[m.bundleCursor]
	case "enter":
		m.packages = m.bundlesToPackages()
		m.view = viewHub
	}
	return m, nil
}

// bundlesToPackages is the space-separated package list for the ticked
// bundles, de-duplicated and sorted.
func (m model) bundlesToPackages() string {
	seen := map[string]bool{}
	var out []string
	for i, on := range m.bundleChecked {
		if !on {
			continue
		}
		for _, p := range bundleOptions[i].packs {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// filtered returns the list items matching the current type-to-filter text
// (case-insensitive substring), or all of them when the filter is empty.
func (m model) filtered() []string {
	if m.listFilter == "" {
		return m.listItems
	}
	f := strings.ToLower(m.listFilter)
	var out []string
	for _, it := range m.listItems {
		if strings.Contains(strings.ToLower(it), f) {
			out = append(out, it)
		}
	}
	return out
}

// updateList drives the scrollable selection list: arrows move, typing
// filters, Enter selects, Esc cancels.
func (m model) updateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.filtered()
	if m.moveButtonFocus(key) {
		return m, nil
	}
	if key.String() == "enter" && m.btnFocus == focusCancel {
		m.view = viewHub
		return m, nil
	}
	// Details: open the highlighted profile's description/parameters in a
	// separate window, so the list itself stays full height.
	if key.String() == "enter" && m.btnFocus == focusDetails {
		if m.editing == secKernelArgs && len(items) > 0 {
			m.popupMsg = m.profileDetails(kernelProfileByName(items[m.listCursor]))
			m.popup = popupMessage
		}
		return m, nil
	}
	switch {
	case key.String() == "esc":
		m.view = viewHub
		return m, nil
	case key.String() == "up":
		if m.listCursor > 0 {
			m.listCursor--
		}
	case key.String() == "down":
		if m.listCursor < len(items)-1 {
			m.listCursor++
		}
	case key.String() == "enter":
		if len(items) > 0 {
			sel := items[m.listCursor]
			switch m.editing {
			case secTimezone:
				m.timezone = sel
			case secLanguage:
				m.language = sel
			case secKeyboard:
				m.keyboard = sel
			case secKernelArgs:
				p := kernelProfileByName(sel)
				wasCustom := m.kernelProfile == "Custom"
				m.kernelProfile = p.name
				// Whether a P-State mode applies depends on the profile, so
				// the set of governors can change with it.
				staleGov := m.dropStaleGovernor()
				if p.name == "Custom" {
					// Open the manual Add/Remove form. Keep the fields when the
					// user is re-editing their own Custom args; start blank when
					// switching to Custom from a predefined profile (the profile's
					// args, e.g. the Server default, are not the user's own).
					if !wasCustom {
						m.kernelArgs, m.kernelArgsRemove = "", ""
					}
					if staleGov != "" {
						m.popup = popupMessage
						m.popupMsg = append([]string{"Kernel Command Line", ""}, staleGovernorMsg(staleGov)...)
					}
					m.view = viewMiniForm
					m.formStep = 0
					ti := textinput.New()
					ti.Focus()
					ti.SetValue(m.kernelArgs)
					m.input = ti
					return m, textinput.Blink
				}
				m.kernelArgs, m.kernelArgsRemove = m.profileArgs(p), ""
				warn := m.busyIdleWarning(m.kernelArgs)
				if staleGov != "" {
					if warn != nil {
						warn = append(warn, "")
					}
					warn = append(warn, staleGovernorMsg(staleGov)...)
				}
				if warn != nil {
					m.view = viewHub
					m.popup = popupMessage
					m.popupMsg = append([]string{"Kernel Command Line", ""}, warn...)
					return m, nil
				}
			}
		}
		m.view = viewHub
		return m, nil
	case key.Type == tea.KeyBackspace:
		if m.listFilter != "" {
			m.listFilter = m.listFilter[:len(m.listFilter)-1]
			m.listCursor = 0
		}
	case key.Type == tea.KeyRunes && len(key.Runes) == 1:
		m.listFilter += string(key.Runes)
		m.listCursor = 0
	}
	// Keep the cursor within the (possibly re-filtered) list.
	if n := len(m.filtered()); m.listCursor >= n {
		m.listCursor = n - 1
	}
	if m.listCursor < 0 {
		m.listCursor = 0
	}
	return m, nil
}

// visibleSections lists the sections shown under a tab. Required holds the
// everyday choices; Advanced the ones most installs leave at their defaults —
// mirroring clr-installer's [R] Required / [A] Advanced split. The AMD and
// Intel P-State sections are each shown only on the matching CPU vendor, since
// amd_pstate= / intel_pstate= would do nothing on the other.
func (m model) visibleSections(tab int) []section {
	if tab == 1 {
		adv := []section{secNetwork, secProxy, secTestNet, secBundles, secHostname,
			secKernel, secLogLevel, secKdump, secGovernor}
		if m.cpuVendor == "amd" {
			adv = append(adv, secAmdPstate)
		}
		if m.cpuVendor == "intel" {
			adv = append(adv, secIntelPstate)
		}
		return append(adv, secMirror, secAutoUpdate, secSaveConfig)
	}
	return []section{secTimezone, secLanguage, secKeyboard, secMedia, secUser, secKernelArgs}
}

func (m model) updateHub(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := m.visibleSections(m.tab)
	last := len(vis) + 1 // Install at len(vis), Cancel at len(vis)+1
	switch key.String() {
	case "up", "k":
		if m.hubCursor > 0 {
			m.hubCursor--
		}
	case "down", "j":
		if m.hubCursor < last {
			m.hubCursor++
		}
	case "tab", "right", "shift+tab", "left", "h", "l":
		m.tab = (m.tab + 1) % 2
		m.hubCursor = 0
	case "r":
		m.tab, m.hubCursor = 0, 0
	case "A":
		m.tab, m.hubCursor = 1, 0
	case "esc":
		return m, tea.Quit
	case "enter":
		switch {
		case m.hubCursor < len(vis):
			return m.openSection(vis[m.hubCursor])
		case m.hubCursor == len(vis):
			if m.canInstall() {
				m.confirmed = true
				return m, tea.Quit
			}
			// Install pressed before the layout is complete: say what is
			// missing in a bordered popup, the way clr warns.
			m.popup = popupMessage
			m.popupMsg = []string{
				"Not ready",
				"",
				"The install is not ready yet. Still required:",
				"  • a target disk and an EFI System Partition",
				"  • a partition mounted at /",
				"  • an admin user",
				"  • a passphrase, if any partition is encrypted",
			}
			return m, nil
		default:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) openSection(s section) (tea.Model, tea.Cmd) {
	m.editing = s
	switch s {
	case secMedia:
		// Installation Media: clr's front dialog (mode + disk + encryption).
		m.view = viewMedia
		m.mediaCursor = 0
		return m, nil
	case secUser:
		// Manage User: the clr-style table of accounts.
		m.view = viewUserList
		m.userCursor = 0
		return m, nil
	case secKernel:
		m.view = viewSectionChoice
		m.choices = kernelOptions
		m.edCursor = indexOf(kernelOptions, m.kernel)
		return m, nil
	case secTimezone:
		if items := system.ListTimezones(); len(items) > 0 {
			return m.openList(items, m.timezone), nil
		}
		return m.openTextEditor(s), textinput.Blink
	case secLanguage:
		if items := system.ListLocales(); len(items) > 0 {
			return m.openList(items, m.language), nil
		}
		return m.openTextEditor(s), textinput.Blink
	case secKeyboard:
		if items := system.ListKeymaps(); len(items) > 0 {
			return m.openList(items, m.keyboard), nil
		}
		return m.openTextEditor(s), textinput.Blink
	case secBundles:
		if m.bundleChecked == nil {
			m.bundleChecked = make([]bool, len(bundleOptions))
		}
		m.view = viewBundles
		m.bundleCursor = 0
		m.btnFocus = focusList
		return m, nil
	case secKernelArgs:
		// A scrollable, type-to-filter list of command-line profiles; the
		// last ("Custom") opens the manual Add/Remove form. Compute the CPU
		// isolation and hugepages for this machine so an isolation profile can
		// fill them in.
		m.cpuIso = system.ComputeCPUIsolation()
		m.hugeGiB = system.HugepagesGiB()
		return m.openList(kernelProfileNames(), m.kernelProfile), nil
	case secMirror:
		m.view = viewMiniForm
		m.formStep = 0
		ti := textinput.New()
		ti.Focus()
		ti.SetValue(m.mirror)
		m.input = ti
		return m, textinput.Blink
	case secAutoUpdate:
		m.view = viewSectionChoice
		m.choices = yesNoOptions
		m.edCursor = triToIndex(m.autoUpdate)
		return m, nil
	case secLogLevel:
		m.view = viewSectionChoice
		m.choices = logLevelOptions
		m.edCursor = logLevelToIndex(m.logLevel)
		return m, nil
	case secAmdPstate:
		m.view = viewSectionChoice
		m.choices = pstateOptions
		m.edCursor = indexOf(pstateValues, m.amdPstate)
		return m, nil
	case secIntelPstate:
		m.view = viewSectionChoice
		m.choices = intelPstateOptions
		m.edCursor = indexOf(intelPstateValues, m.intelPstate)
		return m, nil
	case secGovernor:
		opts, vals := m.governorChoices()
		m.view = viewSectionChoice
		m.choices = opts
		m.edCursor = indexOf(vals, m.governor)
		return m, nil
	case secKdump:
		// Two-field form: crashkernel reservation, then panic-reboot seconds.
		// Suggest the RAM-scaled default when nothing is set yet; clearing the
		// crashkernel field turns kdump off.
		m.view = viewMiniForm
		m.formStep = 0
		m.formErr = ""
		ck := m.crashkernel
		if ck == "" {
			ck = defaultCrashkernel
		}
		ti := textinput.New()
		ti.Focus()
		ti.SetValue(ck)
		m.input = ti
		return m, textinput.Blink
	case secNetwork:
		// The clr-style interface list: the live interfaces, each DHCP until
		// configured static.
		if len(m.netIfaces) == 0 {
			m.netIfaces = liveInterfaces()
		}
		m.view = viewNetList
		m.netCursor = 0
		return m, nil
	case secTestNet:
		// A secondary-progress popup: run a connectivity check while a
		// progress bar animates, like clr's "Test Network Settings".
		m.popup = popupSpinner
		m.testingNet = true
		m.netProgress = 0
		m.netResult = ""
		return m, tea.Batch(spinnerTickCmd(), testNetCmd())
	case secSaveConfig:
		// Write the current configuration to a file, as clr's Save
		// Configuration Settings does, and report the outcome.
		m.popup = popupMessage
		if cfg, err := m.buildConfig(); err != nil {
			m.popupMsg = []string{"Save Configuration Settings", "", "Could not build the config: " + err.Error()}
		} else if err := config.Save(saveConfigPath, cfg); err != nil {
			m.popupMsg = []string{"Save Configuration Settings", "", "Could not save: " + err.Error()}
		} else {
			abs, _ := filepath.Abs(saveConfigPath)
			m.popupMsg = []string{
				"Save Configuration Settings", "",
				"Saved to " + abs,
				"(re-run it later with: slinstaller --apply <file>)",
			}
		}
		return m, nil
	default: // text sections
		return m.openTextEditor(s), textinput.Blink
	}
}

// openTextEditor opens the single-field text editor for a section, seeded
// with its current value.
func (m model) openTextEditor(s section) model {
	m.view = viewSectionText
	ti := textinput.New()
	ti.Focus()
	switch s {
	case secKeyboard:
		ti.SetValue(m.keyboard)
	case secLanguage:
		ti.SetValue(m.language)
	case secTimezone:
		ti.SetValue(m.timezone)
	case secHostname:
		ti.SetValue(m.hostname)
	case secProxy:
		ti.SetValue(m.proxy)
	case secMirror:
		ti.SetValue(m.mirror)
	case secKernelArgs:
		ti.SetValue(m.kernelArgs)
	case secBundles:
		ti.SetValue(m.packages)
	}
	m.input = ti
	m.formStep = 0
	return m
}

// openList opens the scrollable, type-to-filter selection list, with the
// cursor on the current value when it is present.
func (m model) openList(items []string, current string) model {
	m.view = viewList
	m.listItems = items
	m.listFilter = ""
	m.listCursor = 0
	m.btnFocus = focusList
	for i, it := range items {
		if it == current {
			m.listCursor = i
			break
		}
	}
	return m
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// yesNoOptions back the Enabled/Disabled choice page (Automatic OS Updates).
var yesNoOptions = []string{"Enabled", "Disabled"}

// triToIndex maps a tri-state toggle (0 unset, 1 on, 2 off) to the
// Enabled/Disabled list index (defaulting to Enabled when unset).
func triToIndex(t int) int {
	if t == 2 {
		return 1
	}
	return 0
}

// valueAt is the inverse of indexOf for a choice list: the value at cursor i,
// or the first (default) one when i is out of range.
func valueAt(vals []string, i int) string {
	if i < 0 || i >= len(vals) {
		return vals[0]
	}
	return vals[i]
}

func indexOf(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return 0
}

func (m model) sectionValue(s section) string {
	switch s {
	case secTimezone:
		return m.timezone
	case secLanguage:
		return "[" + m.language + "]"
	case secKeyboard:
		return m.keyboard
	case secMedia:
		if !m.diskSet {
			return "Warning: No Media Selected"
		}
		return fmt.Sprintf("%s (%d parts)", m.disks[m.diskIdx].Path, len(m.parts))
	case secUser:
		switch len(m.users) {
		case 0:
			return "No users added"
		case 1:
			return m.users[0].Login + adminTag(m.users[0].Admin)
		default:
			return fmt.Sprintf("%d users (%s, ...)", len(m.users), m.users[0].Login)
		}
	case secNetwork:
		var statics []string
		for _, ni := range m.netIfaces {
			if ni.Static {
				statics = append(statics, ni.Name+": "+ni.Addr)
			}
		}
		if len(statics) == 0 {
			return "Automatic (DHCP)"
		}
		return strings.Join(statics, ", ")
	case secProxy:
		if strings.TrimSpace(m.proxy) == "" {
			return "No HTTPS proxy URL set"
		}
		return m.proxy
	case secTestNet:
		return "Check if all the required network settings are properly set"
	case secBundles:
		if strings.TrimSpace(m.packages) == "" {
			return "No bundles selected"
		}
		return m.packages
	case secHostname:
		if strings.TrimSpace(m.hostname) == "" {
			return "No target system hostname assigned"
		}
		return m.hostname
	case secKernelArgs:
		switch {
		case m.kernelProfile != "" && m.kernelProfile != "Custom":
			return m.kernelProfile + " profile"
		case strings.TrimSpace(m.kernelArgs) != "" || strings.TrimSpace(m.kernelArgsRemove) != "":
			return "Custom: " + strings.TrimSpace(m.kernelArgs+" "+m.kernelArgsRemove)
		default:
			return "No kernel command line configuration defined"
		}
	case secKernel:
		if m.kernel == "" {
			return "Select desired kernel"
		}
		return kernelLabel(m.kernel)
	case secMirror:
		if strings.TrimSpace(m.mirror) == "" {
			return "No swupd mirror set"
		}
		return m.mirror
	case secAutoUpdate:
		return triLabel(m.autoUpdate)
	case secSaveConfig:
		return "Save the installation configuration to a file"
	case secLogLevel:
		if m.logLevel < 0 {
			return "Kernel default (not set)"
		}
		return fmt.Sprintf("loglevel=%d, kernel.printk = %d 4 1 7", m.logLevel, m.logLevel)
	case secAmdPstate:
		if m.amdPstate == "" {
			return "Not set (AMD RT/telco profiles)"
		}
		return fmt.Sprintf("amd_pstate=%s (AMD RT/telco profiles)", m.amdPstate)
	case secIntelPstate:
		if m.intelPstate == "" {
			return "Not set (keep HWP; Intel RT/telco profiles)"
		}
		return fmt.Sprintf("intel_pstate=%s (Intel RT/telco profiles)", m.intelPstate)
	case secGovernor:
		if m.governor == "" {
			return "Profile default"
		}
		return "cpufreq.default_governor=" + m.governor
	case secKdump:
		if strings.TrimSpace(m.crashkernel) == "" {
			return "Off (no crash dump)"
		}
		s := "crashkernel=" + m.crashkernel
		if m.panicSecs != "" {
			s += ", panic=" + m.panicSecs
		}
		return s
	}
	return ""
}

// triLabel renders a tri-state toggle: no choice yet, enabled or disabled.
func triLabel(t int) string {
	switch t {
	case 1:
		return "Enabled"
	case 2:
		return "Disabled"
	default:
		return "No choice made"
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func orDefault(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(default)"
	}
	return s
}

// --- simple section editors ---

// updateSectionChoice drives both choice lists: the disk chooser (when
// m.choices is nil, reached from the Installation Media dialog) and a
// generic option list such as the kernel selection (from the hub).
func (m model) updateSectionChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	diskMode := m.choices == nil
	n := len(m.choices)
	if diskMode {
		n = len(m.disks)
	}
	switch key.String() {
	case "esc":
		if diskMode {
			m.view = viewPartList // back to the media dialog
		} else {
			m.view = viewHub
		}
	case "up", "k":
		if m.edCursor > 0 {
			m.edCursor--
		}
	case "down", "j":
		if m.edCursor < n-1 {
			m.edCursor++
		}
	case "enter":
		if diskMode {
			m.diskIdx = m.edCursor
			m.diskSet = true
			m.view = viewPartList
		} else {
			staleGov := ""
			switch m.editing {
			case secAutoUpdate:
				m.autoUpdate = m.edCursor + 1 // 1 Enabled, 2 Disabled
			case secLogLevel:
				m.logLevel = logLevelFromIndex(m.edCursor)
			case secAmdPstate:
				// The mode decides which governors exist, and amd_pstate is
				// part of an isolation profile's args: drop a governor the new
				// mode does not have, then re-sync the profile's args.
				m.amdPstate = valueAt(pstateValues, m.edCursor)
				staleGov = m.dropStaleGovernor()
				m.resyncProfileArgs()
			case secIntelPstate:
				m.intelPstate = valueAt(intelPstateValues, m.edCursor)
				staleGov = m.dropStaleGovernor()
				m.resyncProfileArgs()
			case secGovernor:
				_, vals := m.governorChoices()
				m.governor = valueAt(vals, m.edCursor)
				m.resyncProfileArgs()
			default: // kernel
				m.kernel = m.choices[m.edCursor]
			}
			m.choices = nil
			m.view = viewHub
			if staleGov != "" {
				m.popup = popupMessage
				m.popupMsg = append([]string{sectionLabels[m.editing], ""}, staleGovernorMsg(staleGov)...)
			}
		}
	}
	return m, nil
}

func (m model) updateSectionText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		switch m.editing {
		case secKeyboard:
			if v != "" {
				m.keyboard = v
			}
		case secLanguage:
			if v != "" {
				m.language = v
			}
		case secTimezone:
			if v != "" {
				m.timezone = v
			}
		case secHostname:
			if v != "" {
				m.hostname = v
			}
		case secProxy:
			m.proxy = v
		case secMirror:
			m.mirror = v
		case secKernelArgs:
			m.kernelArgs = v
		case secBundles:
			m.packages = v
		}
		m.view = viewHub
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// hubRowsPerSection is the height, in lines, of one hub section: a label
// line, a value line and a separator rule.
const hubRowsPerSection = 3

// hubVisibleCount is how many sections the hub shows at once, from the
// panel's fixed height; the rest scroll, with a scrollbar on the right.
// The chrome is 7 rows: title, tab strip and rule above the list; the
// buttons, the "Need:" line, the rule and the footer below it. It used to
// be counted as 8, with a blank line under the list besides, which left
// three empty rows between the last section and the buttons.
func hubVisibleCount(height int) int {
	rows := panelHeight(height) - 7
	if rows < hubRowsPerSection {
		rows = hubRowsPerSection
	}
	return rows / hubRowsPerSection
}

func (m model) hubView() string {
	w := m.panelWidth()
	var body []string

	// Tab row: [R] Required options   [A] Advanced options.
	req, adv := tabInactive, tabInactive
	if m.tab == 0 {
		req = tabActive
	} else {
		adv = tabActive
	}
	tabs := req.Render(" [R] Required options ") + tabInactive.Render("  ") + adv.Render(" [A] Advanced options ")
	body = append(body, tabStrip.Width(w).Render("  "+tabs), rule(w))

	// The section list, windowed to a fixed height with a scrollbar on the
	// right — the way clr-installer shows there is more below.
	vis := m.visibleSections(m.tab)
	win := hubVisibleCount(m.height)
	if win > len(vis) {
		win = len(vis)
	}
	if win < 1 {
		win = 1
	}
	start := 0
	if m.hubCursor >= win { // keep the cursor's section in view
		start = m.hubCursor - win + 1
	}
	if max := len(vis) - win; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}

	cw := w - 2 // leave two columns for the scrollbar
	var secLines []string
	for i := start; i < start+win && i < len(vis); i++ {
		s := vis[i]
		sel := m.hubCursor == i
		secLines = append(secLines, item(cw, sel, sectionLabels[s], m.sectionValue(s))...)
		// The selected row's separator is drawn in the accent selection colour
		// too, so the highlight reads as a taller bar, as clr-installer shows.
		if sel {
			secLines = append(secLines, selBG.Width(cw).Render(""))
		} else {
			secLines = append(secLines, rule(cw))
		}
	}
	sb := scrollbarCol(len(secLines), start, win, len(vis))
	secBlock := lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(secLines, "\n"), strings.Join(sb, "\n"))
	body = append(body, strings.Split(secBlock, "\n")...)

	// Cancel / Install buttons, held at the bottom by screenButtons.
	cancel := button{"Cancel", pick(m.hubCursor == len(vis)+1)}
	install := button{"Install", btnOff}
	if m.canInstall() {
		install.style = pick(m.hubCursor == len(vis))
	}
	btnLines := []string{buttonLine(w, cancel, install)}
	if !m.canInstall() {
		btnLines = append(btnLines, warnText.Width(w).Render("  Need: a disk, a /, an ESP, an admin user (+ passphrase if encrypting)."))
	}

	return m.screenButtons(body, btnLines, "Use [Tab] or the arrow keys [Up and Down] to navigate")
}

// choiceView renders the disk chooser (m.choices nil) or a generic option
// list such as the kernel selection.
func (m model) choiceView() string {
	w := m.panelWidth()
	if m.choices == nil { // disk mode
		body := []string{
			itemLabel.Width(w).Render("  Select Installation Media"),
			warnText.Width(w).Render("  The chosen disk will be ERASED."),
			lineBG.Width(w).Render(""),
		}
		for i, d := range m.disks {
			body = append(body, row(w, i == m.edCursor, fmt.Sprintf("%-14s %s", d.Path, d.Model)))
		}
		return m.screen(body, "[Up/Down] move · [Enter] select · [Esc] back")
	}
	body := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[m.editing]),
		lineBG.Width(w).Render(""),
	}
	// Automatic OS Updates carries an explanatory paragraph above the
	// Enabled/Disabled choice, the way clr-installer explains it.
	for _, line := range choiceBlurb(m.editing) {
		body = append(body, lineBG.Width(w).Render("  "+line))
	}
	if len(choiceBlurb(m.editing)) > 0 {
		body = append(body, lineBG.Width(w).Render(""))
	}
	for i, c := range m.choices {
		label := c
		if m.editing == secKernel {
			label = kernelLabel(c)
		}
		body = append(body, row(w, i == m.edCursor, label))
	}
	return m.screen(body, "[Up/Down] move · [Enter] select · [Esc] back")
}

// choiceBlurb is the explanatory text shown above a choice page, for the
// sections clr-installer explains.
func choiceBlurb(s section) []string {
	switch s {
	case secAutoUpdate:
		return []string{
			"Allow the system to update automatically as new versions",
			"are released - the recommended default. Disabling it risks",
			"missing security fixes. Updates run once a day; turn them",
			"on or off later with slinitctl enable/disable sunlight-autoupdate.",
		}
	}
	return nil
}

// listView renders a scrollable, type-to-filter selection list (Timezone,
// Language, Keyboard) with a scrollbar on the right, Cancel/Confirm buttons,
// in clr's style.
func (m model) listView() string {
	w := m.panelWidth()
	items := m.filtered()

	header := []string{itemLabel.Width(w).Render("  " + sectionTitles[m.editing])}
	if m.editing == secKernelArgs && len(m.consoles) > 0 {
		header = append(header, footer.Width(w).Render("  consoles (auto): "+clip(strings.Join(m.consoles, " "), w-20)))
	}
	if m.listFilter != "" {
		header = append(header, footer.Width(w).Render("  filter: "+m.listFilter))
	}
	header = append(header, rule(w))

	// Window the list to the remaining height, leaving room for a blank, the
	// buttons and the footer. The kernel-profile picker shows no inline
	// preview — its Details button opens a separate window instead — so the
	// list uses the full height.
	rows := panelHeight(m.height) - len(header) - 4
	if rows < 3 {
		rows = 3
	}
	win := rows
	if win > len(items) {
		win = len(items)
	}
	start := 0
	if m.listCursor >= win {
		start = m.listCursor - win + 1
	}
	if max := len(items) - win; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}

	body := header
	if len(items) == 0 {
		body = append(body, footer.Width(w).Render("  (no match)"))
	} else {
		cw := w - 2
		var lines []string
		for i := start; i < start+win && i < len(items); i++ {
			lines = append(lines, row(cw, i == m.listCursor, items[i]))
		}
		sb := scrollbarCol(len(lines), start, win, len(items))
		block := lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(lines, "\n"), strings.Join(sb, "\n"))
		body = append(body, strings.Split(block, "\n")...)
	}

	body = append(body, lineBG.Width(w).Render(""))

	// The kernel-profile picker adds a Details button to the button row.
	btnLine := m.cancelConfirm(w)
	hint := "type to filter · [Tab] buttons · [Enter] select · [Esc] back"
	if m.editing == secKernelArgs {
		btnLine = buttonLine(w,
			button{"Details", pick(m.btnFocus == focusDetails)},
			button{"Cancel", pick(m.btnFocus == focusCancel)},
			button{"Confirm", pick(m.btnFocus == focusConfirm)})
		hint = "type to filter · [Tab] Details/buttons · [Enter] select · [Esc] back"
	}
	return m.screenButtons(body, []string{btnLine}, hint)
}

// miniFormView renders the two-field forms. Kernel Command Line shows an
// Add or Remove arguments field; Swupd Mirror shows the URL, then an
// allow-insecure checkbox — as clr-installer lays them out.
func (m model) miniFormView() string {
	w := m.panelWidth()
	switch m.editing {
	case secKernelArgs:
		title, label := "Add Kernel Command-Line Arguments", "Add Extra Arguments"
		if m.formStep == 1 {
			label = "Remove Arguments"
		}
		body := []string{
			itemLabel.Width(w).Render("  " + title),
			footer.Width(w).Render("  Boot-time kernel arguments; space-separated."),
			rule(w),
			itemLabel.Width(w).Render("  " + label + ":"),
			lineBG.Width(w).Render(""),
			lineBG.Width(w).Render("  " + m.input.View()),
		}
		return m.screen(body, "type · [Enter] next · [Esc] cancel")
	case secMirror:
		body := []string{
			itemLabel.Width(w).Render("  Swupd Mirror"),
			footer.Width(w).Render("  Installation source (mirror) URL; HTTPS needs a public CA."),
			rule(w),
		}
		if m.formStep == 0 {
			body = append(body,
				itemLabel.Width(w).Render("  Mirror URL:"),
				lineBG.Width(w).Render(""),
				lineBG.Width(w).Render("  "+m.input.View()))
			return m.screen(body, "type · [Enter] next · [Esc] cancel")
		}
		box := "[ ]"
		if m.allowInsecure {
			box = "[x]"
		}
		body = append(body,
			lineBG.Width(w).Render("  Mirror URL: "+orDash(m.mirror)),
			lineBG.Width(w).Render(""),
			row(w, true, box+" Allow installation over insecure connections (http://)"))
		return m.screen(body, "[Space] toggle · [Enter] confirm · [Esc] cancel")
	case secKdump:
		body := []string{
			itemLabel.Width(w).Render("  Crash Dump (kdump)"),
			footer.Width(w).Render("  Reserve memory for a crash kernel; leave empty to disable."),
			footer.Width(w).Render("  This reserves the memory only: saving a dump also needs"),
			footer.Width(w).Render("  kexec-tools, makedumpfile and a boot service to load it."),
			rule(w),
		}
		if m.formStep == 0 {
			body = append(body,
				itemLabel.Width(w).Render("  crashkernel= (size or range, empty = off):"),
				lineBG.Width(w).Render(""),
				lineBG.Width(w).Render("  "+m.input.View()))
			return m.screen(body, "type · [Enter] next · [Esc] cancel")
		}
		body = append(body,
			lineBG.Width(w).Render("  crashkernel = "+orDash(m.crashkernel)),
			lineBG.Width(w).Render(""),
			itemLabel.Width(w).Render("  panic= (seconds to reboot after a panic, empty = none):"),
			lineBG.Width(w).Render(""),
			lineBG.Width(w).Render("  "+m.input.View()))
		if m.formErr != "" {
			body = append(body, lineBG.Width(w).Render(""), warnText.Width(w).Render("  "+m.formErr))
		}
		return m.screen(body, "type · [Enter] confirm · [Esc] cancel")
	}
	return m.screen(nil, "")
}

// bundlesView renders the Select Additional Bundles checklist, with a
// scrollbar on the right when it does not all fit.
func (m model) bundlesView() string {
	w := m.panelWidth()
	header := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[secBundles]),
		rule(w),
	}
	rows := panelHeight(m.height) - len(header) - 4
	if rows < 3 {
		rows = 3
	}
	win := rows
	if win > len(bundleOptions) {
		win = len(bundleOptions)
	}
	start := 0
	if m.bundleCursor >= win {
		start = m.bundleCursor - win + 1
	}
	if max := len(bundleOptions) - win; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}

	cw := w - 2
	var lines []string
	for i := start; i < start+win && i < len(bundleOptions); i++ {
		b := bundleOptions[i]
		box := "[ ]"
		if m.bundleChecked[i] {
			box = "[x]"
		}
		lines = append(lines, row(cw, i == m.bundleCursor, fmt.Sprintf("%s %s: %s", box, b.name, b.desc)))
	}
	sb := scrollbarCol(len(lines), start, win, len(bundleOptions))
	block := lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(lines, "\n"), strings.Join(sb, "\n"))
	body := append(header, strings.Split(block, "\n")...)
	body = append(body, lineBG.Width(w).Render(""))
	return m.screenButtons(body, []string{m.cancelConfirm(w)},
		"[Tab] buttons · [Space] toggle · [Enter] confirm · [Esc] back")
}

func (m model) textView() string {
	w := m.panelWidth()
	body := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[m.editing]),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	return m.screen(body, "type · [Enter] confirm · [Esc] back")
}
