package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

var (
	shiftTab = tea.KeyMsg{Type: tea.KeyShiftTab}
	rightKey = tea.KeyMsg{Type: tea.KeyRight}
	backKey  = tea.KeyMsg{Type: tea.KeyBackspace}
	ctrlC    = tea.KeyMsg{Type: tea.KeyCtrlC}
)

func viewOf(m model) string { return stripANSIForTest(m.View()) }

// clearInput empties the focused text field.
func clearInput(m model) model {
	for i := len(m.input.Value()); i > 0; i-- {
		m = send(m, backKey)
	}
	return m
}

func typeText(m model, s string) model { return send(clearInput(m), runes(s)) }

// readyModel has everything an install needs.
func readyModel() model {
	m := withSize(newModel(twoTestDisks()))
	m.diskSet = true
	m.parts = autoLayout(false)
	m.users = []userEntry{{Login: "ion", Password: "correct horse", Admin: true}}
	return m
}

func TestHubKeys(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	if m.Init() == nil {
		t.Error("Init should start the cursor blinking")
	}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	if got := nm.(model); got.width != 120 || got.height != 50 {
		t.Errorf("size not taken: %dx%d", got.width, got.height)
	}
	// A message that is neither a key nor one of ours goes to the text field
	// and changes nothing else.
	if nm, _ = m.Update(struct{}{}); nm.(model).view != viewHub {
		t.Error("an unknown message changed the view")
	}

	last := len(m.visibleSections(0)) + 1
	if got := send(m, up); got.hubCursor != 0 {
		t.Error("Up moved above the first section")
	}
	for i := 0; i < last+3; i++ {
		m = send(m, down)
	}
	if m.hubCursor != last {
		t.Errorf("Down stops on Cancel: cursor %d, want %d", m.hubCursor, last)
	}
	if got := send(m, up); got.hubCursor != last-1 {
		t.Error("Up from Cancel did not reach Install")
	}
	// The tab keys switch tabs and start at the top; r and A name a tab.
	adv := send(m, tabKey)
	if adv.tab != 1 || adv.hubCursor != 0 {
		t.Errorf("Tab: tab %d cursor %d", adv.tab, adv.hubCursor)
	}
	if got := send(send(adv, down), runes("r")); got.tab != 0 || got.hubCursor != 0 {
		t.Errorf("r: tab %d cursor %d", got.tab, got.hubCursor)
	}
	// The Advanced list is longer than the window: the cursor's section
	// stays on screen as it moves down.
	for i := 0; i < len(adv.visibleSections(1))-1; i++ {
		adv = send(adv, down)
	}
	if view := viewOf(adv); !strings.Contains(view, sectionLabels[secSaveConfig]) || strings.Contains(view, sectionLabels[secNetwork]) {
		t.Errorf("the hub did not scroll to the last section:\n%s", view)
	}

	// Esc asks first: one Esc too many, after leaving a section, lost
	// every answer. y leaves; any other key stays on the menu.
	asked, cmd := m.Update(escKey)
	if cmd != nil || asked.(model).popup != popupQuit {
		t.Fatalf("esc: quit=%v popup=%d", cmd != nil, asked.(model).popup)
	}
	if v := asked.(model).View(); !strings.Contains(v, "Leave the installer?") {
		t.Errorf("the question is not shown:\n%s", v)
	}
	if stay, cmd := asked.Update(escKey); cmd != nil || stay.(model).popup != popupNone || stay.(model).view != viewHub {
		t.Errorf("esc twice: quit=%v popup=%d", cmd != nil, stay.(model).popup)
	}
	if _, cmd := asked.Update(runes("y")); cmd == nil {
		t.Error("y did not leave")
	}

	// Ctrl+C and Cancel quit without confirming.
	for name, got := range map[string]func() (tea.Model, tea.Cmd){
		"ctrl+c": func() (tea.Model, tea.Cmd) { return m.Update(ctrlC) },
		"cancel": func() (tea.Model, tea.Cmd) { return m.Update(enter) }, // cursor is on Cancel
	} {
		nm, cmd := got()
		if cmd == nil || nm.(model).confirmed {
			t.Errorf("%s: quit=%v confirmed=%v", name, cmd != nil, nm.(model).confirmed)
		}
	}
}

// Install is refused, with the reasons, until the layout, an admin account
// and (when encrypted) a passphrase are all there.
func TestHubInstallButton(t *testing.T) {
	onInstall := func(m model) model {
		m.hubCursor = len(m.visibleSections(0))
		return m
	}
	m := onInstall(withSize(newModel(twoTestDisks())))
	nm, cmd := m.Update(enter)
	got := nm.(model)
	if got.confirmed || cmd != nil || got.popup != popupMessage {
		t.Fatalf("an incomplete install was started: confirmed=%v popup=%d", got.confirmed, got.popup)
	}
	if view := viewOf(got); !strings.Contains(view, "Not ready") || !strings.Contains(view, "an admin user") {
		t.Errorf("the reasons are not shown:\n%s", view)
	}

	m = onInstall(readyModel())
	nm, cmd = m.Update(enter)
	if !nm.(model).confirmed || cmd == nil {
		t.Error("a complete install was not started")
	}
	// An encrypted layout also needs its passphrase.
	m.parts = autoLayout(true)
	if nm, _ = m.Update(enter); nm.(model).confirmed {
		t.Error("an encrypted install started without a passphrase")
	}
	m.passphrase = "a long passphrase"
	if nm, _ = m.Update(enter); !nm.(model).confirmed {
		t.Error("an encrypted install with a passphrase was refused")
	}
	// A user who is not an administrator is not enough.
	m.users = []userEntry{{Login: "guest", Password: "correct horse"}}
	if nm, _ = m.Update(enter); nm.(model).confirmed {
		t.Error("an install with no admin user was started")
	}

	// What the installer would refuse after the wizard is refused here,
	// keeping everything entered: a bad hostname, a duplicate mountpoint.
	m = onInstall(readyModel())
	m.hostname = "-bad host"
	nm, cmd = m.Update(enter)
	got = nm.(model)
	if got.confirmed || cmd != nil || got.popup != popupMessage || !strings.Contains(viewOf(got), "hostname") {
		t.Errorf("an invalid hostname went through: confirmed=%v\n%s", got.confirmed, viewOf(got))
	}
	if got.hostname != "-bad host" || len(got.users) == 0 {
		t.Error("the answers were not kept")
	}
	// A root below the minimum is refused — unless the command line turned
	// size validation off, as the installer after the wizard honours.
	m = onInstall(readyModel())
	for i := range m.parts {
		if m.parts[i].Mount == "/" {
			m.parts[i].Size = "1G"
		}
	}
	if nm, _ = m.Update(enter); nm.(model).confirmed {
		t.Error("a 1G root went through")
	}
	m.skipSize = true
	if nm, _ = m.Update(enter); !nm.(model).confirmed {
		t.Errorf("--skip-validation-size was not honoured:\n%s", viewOf(nm.(model)))
	}
	// A layout larger than its disk is refused here too, not after the
	// wizard has gone.
	m = onInstall(readyModel())
	for i := range m.parts {
		if m.parts[i].Mount == "/" {
			m.parts[i].Size = "50000G"
		}
	}
	if nm, _ = m.Update(enter); nm.(model).confirmed || !strings.Contains(viewOf(nm.(model)), "layout needs") {
		t.Errorf("a layout bigger than the disk went through:\n%s", viewOf(nm.(model)))
	}
	// Kernel arguments this machine cannot meet are refused here too.
	m = onInstall(readyModel())
	m.kernelProfile, m.kernelArgs = "Custom", "isolcpus=100000-100010"
	if nm, _ = m.Update(enter); nm.(model).confirmed || !strings.Contains(viewOf(nm.(model)), "isolcpus") {
		t.Errorf("impossible kernel arguments went through:\n%s", viewOf(nm.(model)))
	}
	// Unless the command line turned validation off, as main does.
	m.skipAll = true
	if nm, _ = m.Update(enter); !nm.(model).confirmed {
		t.Errorf("--skip-validation-all not honoured for kernel arguments:\n%s", viewOf(nm.(model)))
	}
	m = onInstall(readyModel())
	m.parts = append(m.parts, m.parts[len(m.parts)-1])
	if nm, _ = m.Update(enter); nm.(model).confirmed {
		t.Error("a layout with / twice went through")
	}
}

func TestSectionValues(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	m.kernelProfile, m.kernelArgs, m.hostname = "", "", ""
	m.autoUpdate, m.amdPstate = 0, "" // the model starts with these chosen
	lv := func(s section, want string) {
		t.Helper()
		if got := m.sectionValue(s); got != want {
			t.Errorf("section %d value = %q, want %q", s, got, want)
		}
	}
	// Nothing chosen yet.
	lv(secMedia, "Warning: No Media Selected")
	lv(secUser, "No users added")
	lv(secNetwork, "Automatic (DHCP)")
	lv(secProxy, "No HTTPS proxy URL set")
	lv(secBundles, "No bundles selected")
	lv(secHostname, "No target system hostname assigned")
	lv(secKernelArgs, "No kernel command line configuration defined")
	lv(secMirror, "No swupd mirror set")
	lv(secAutoUpdate, "No choice made")
	lv(secAmdPstate, "Not set (AMD RT/telco profiles)")
	lv(secIntelPstate, "Not set (keep HWP; Intel RT/telco profiles)")
	lv(secGovernor, "Profile default")
	lv(secKdump, "Off (no crash dump)")
	lv(section(-1), "")
	m.kernel = ""
	lv(secKernel, "Select desired kernel")

	// Everything chosen.
	m.diskSet, m.diskIdx, m.parts = true, 1, autoLayout(false)
	m.users = []userEntry{{Login: "ion", Admin: true}}
	m.netIfaces = []netIfaceEntry{{Name: "eth0"}, {Name: "eth1", Static: true, Addr: "10.0.0.2/24"}}
	m.proxy, m.packages, m.hostname, m.mirror = "http://proxy:3128", "git vim", "box", "https://mirror.example"
	m.kernelArgs, m.kernelArgsRemove = "quiet", "rhgb"
	m.autoUpdate, m.amdPstate, m.intelPstate, m.governor = 2, "guided", "passive", "schedutil"
	m.crashkernel, m.panicSecs = "256M", "10"
	m.kernel = kernelOptions[len(kernelOptions)-1]
	lv(secMedia, "/dev/sdb (3 parts)")
	lv(secUser, "ion (admin)")
	lv(secNetwork, "eth1: 10.0.0.2/24")
	lv(secProxy, "http://proxy:3128")
	lv(secBundles, "git vim")
	lv(secHostname, "box")
	lv(secKernelArgs, "Custom: quiet -rhgb") // rhgb is removed, not added
	lv(secMirror, "https://mirror.example")
	lv(secAutoUpdate, "Disabled")
	lv(secAmdPstate, "amd_pstate=guided (AMD RT/telco profiles)")
	lv(secIntelPstate, "intel_pstate=passive (Intel RT/telco profiles)")
	lv(secGovernor, "cpufreq.default_governor=schedutil")
	lv(secKdump, "crashkernel=256M, panic=10")
	lv(secKernel, kernelLabel(kernelOptions[len(kernelOptions)-1]))
	m.panicSecs = ""
	lv(secKdump, "crashkernel=256M")
	m.users = append(m.users, userEntry{Login: "ana"})
	lv(secUser, "2 users (ion, ...)")
	m.users = []userEntry{{Login: "guest"}}
	lv(secUser, "guest")
	m.autoUpdate = 1
	lv(secAutoUpdate, "Enabled")

	// The small helpers behind the choice lists.
	if triToIndex(2) != 1 || triToIndex(0) != 0 {
		t.Error("triToIndex")
	}
	if valueAt([]string{"a", "b"}, 5) != "a" || valueAt([]string{"a", "b"}, 1) != "b" {
		t.Error("valueAt should fall back to the first value")
	}
	if indexOf([]string{"a", "b"}, "zzz") != 0 || indexOf([]string{"a", "b"}, "b") != 1 {
		t.Error("indexOf should fall back to the first entry")
	}
	if orDash("  ") != "-" || orDash("x") != "x" {
		t.Error("orDash")
	}
	if kernelLabel("linux-something-else") != "linux-something-else" {
		t.Error("an unknown kernel should show under its own name")
	}
	if logLevelToIndex(3) != 4 || logLevelFromIndex(0) != -1 || logLevelFromIndex(4) != 3 {
		t.Errorf("log level index: %d %d %d", logLevelToIndex(3), logLevelFromIndex(0), logLevelFromIndex(4))
	}
	if kernelProfileIndex("no such profile") != 0 {
		t.Error("an unknown profile should fall back to the first")
	}
}

func TestTextSections(t *testing.T) {
	base := withSize(newModel(twoTestDisks()))
	edit := func(s section, text string) model {
		m := base
		m.editing = s
		m = m.openTextEditor(s)
		return send(typeText(m, text), enter)
	}
	if got := edit(secHostname, "box"); got.hostname != "box" || got.view != viewHub {
		t.Errorf("hostname = %q view %d", got.hostname, got.view)
	}
	// These keep their value when the field is left empty …
	if got := edit(secHostname, ""); got.hostname != base.hostname {
		t.Errorf("an empty hostname replaced %q with %q", base.hostname, got.hostname)
	}
	if got := edit(secTimezone, "Europe/Bucharest"); got.timezone != "Europe/Bucharest" {
		t.Errorf("timezone = %q", got.timezone)
	}
	if got := edit(secLanguage, "ro_RO.UTF-8"); got.language != "ro_RO.UTF-8" {
		t.Errorf("language = %q", got.language)
	}
	if got := edit(secKeyboard, "ro"); got.keyboard != "ro" {
		t.Errorf("keyboard = %q", got.keyboard)
	}
	for _, s := range []section{secTimezone, secLanguage, secKeyboard} {
		got := edit(s, "")
		if got.timezone != base.timezone || got.language != base.language || got.keyboard != base.keyboard {
			t.Errorf("an empty entry cleared section %d", s)
		}
	}
	// … while a proxy can be removed by clearing it.
	withProxy := base
	withProxy.proxy = "http://proxy:3128"
	withProxy.editing = secProxy
	m := withProxy.openTextEditor(secProxy)
	if m.input.Value() != "http://proxy:3128" {
		t.Errorf("the editor did not start from the current value: %q", m.input.Value())
	}
	if !strings.Contains(viewOf(m), sectionTitles[secProxy]) {
		t.Errorf("text editor view:\n%s", viewOf(m))
	}
	if got := send(clearInput(m), enter); got.proxy != "" {
		t.Errorf("proxy not cleared: %q", got.proxy)
	}
	// Esc leaves without changing anything.
	if got := send(typeText(m, "http://other"), escKey); got.proxy != "http://proxy:3128" || got.view != viewHub {
		t.Errorf("Esc changed the proxy to %q", got.proxy)
	}
	// From the hub, a text section opens this editor.
	if got := openSectionInTab(base, 1, secHostname); got.view != viewSectionText || got.input.Value() != base.hostname {
		t.Errorf("Hostname opened view %d with %q", got.view, got.input.Value())
	}
}

// Timezone, language and keyboard are picked from what the live system
// has; where it has no list, they are typed.
func TestLocaleSectionsOpenAListWhenTheSystemHasOne(t *testing.T) {
	base := withSize(newModel(twoTestDisks()))
	for s, items := range map[section][]string{
		secTimezone: system.ListTimezones(),
		secLanguage: system.ListLocales(),
		secKeyboard: system.ListKeymaps(),
	} {
		got := openSectionInTab(base, 0, s)
		want := viewSectionText
		if len(items) > 0 {
			want = viewList
		}
		if got.view != want {
			t.Errorf("section %d opened view %d, want %d (%d items)", s, got.view, want, len(items))
		}
	}
}

func TestSelectionList(t *testing.T) {
	base := withSize(newModel(twoTestDisks()))
	open := func(s section, current string) model {
		m := base
		m.editing = s
		return m.openList([]string{"Africa/Cairo", "Europe/Berlin", "Europe/Bucharest", "UTC"}, current)
	}
	// The list opens on the current value.
	m := open(secTimezone, "Europe/Berlin")
	if m.listCursor != 1 {
		t.Errorf("cursor = %d, want it on the current value", m.listCursor)
	}
	// Typing filters, and the cursor returns to the first match.
	m = send(send(send(m, runes("b")), runes("u")), runes("c"))
	if got := m.filtered(); len(got) != 1 || got[0] != "Europe/Bucharest" || m.listCursor != 0 {
		t.Errorf("filter %q -> %v cursor %d", m.listFilter, got, m.listCursor)
	}
	if !strings.Contains(viewOf(m), "filter: buc") {
		t.Errorf("the filter is not shown:\n%s", viewOf(m))
	}
	if got := send(m, enter); got.timezone != "Europe/Bucharest" || got.view != viewHub {
		t.Errorf("selected %q", got.timezone)
	}
	// A filter nothing matches says so, and Enter then changes nothing.
	none := send(m, runes("z"))
	if !strings.Contains(viewOf(none), "(no match)") || none.listCursor != 0 {
		t.Errorf("no-match view:\n%s", viewOf(none))
	}
	if got := send(none, enter); got.timezone != base.timezone {
		t.Errorf("Enter with no match set the timezone to %q", got.timezone)
	}
	// Backspace widens the filter again; on an empty filter it does nothing.
	if got := send(none, backKey); got.listFilter != "buc" {
		t.Errorf("Backspace: filter %q", got.listFilter)
	}
	if got := send(open(secTimezone, ""), backKey); got.listFilter != "" || got.view != viewList {
		t.Error("Backspace on an empty filter did something")
	}
	// Up and Down stop at the ends.
	m = open(secLanguage, "")
	if send(m, up).listCursor != 0 {
		t.Error("Up moved above the list")
	}
	for i := 0; i < 10; i++ {
		m = send(m, down)
	}
	if m.listCursor != 3 {
		t.Errorf("Down past the end: %d", m.listCursor)
	}
	if got := send(m, enter); got.language != "UTC" {
		t.Errorf("language = %q", got.language)
	}
	if got := send(open(secKeyboard, ""), enter); got.keyboard != "Africa/Cairo" {
		t.Errorf("keyboard = %q", got.keyboard)
	}
	// Esc, and Enter on Cancel, leave without choosing.
	if got := send(open(secTimezone, ""), escKey); got.view != viewHub || got.timezone != base.timezone {
		t.Error("Esc chose something")
	}
	if got := send(send(open(secTimezone, ""), tabKey), enter); got.view != viewHub || got.timezone != base.timezone {
		t.Error("Enter on Cancel chose something")
	}
	// A long list scrolls to keep the cursor on screen.
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, "Zone/"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	m = base
	m.editing = secTimezone
	m = m.openList(many, many[59])
	if view := viewOf(m); !strings.Contains(view, many[59]) || strings.Contains(view, many[0]) {
		t.Errorf("the list did not scroll to the current value:\n%s", view)
	}
}

func TestButtonFocus(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secBundles)
	if m.view != viewBundles || m.btnFocus != focusList {
		t.Fatalf("bundles: view %d focus %d", m.view, m.btnFocus)
	}
	// Left and Right mean nothing while the list has the focus.
	if got := send(m, rightKey); got.btnFocus != focusList {
		t.Error("Right moved the focus off the list")
	}
	// Tab: list, Cancel, Confirm, list. Shift+Tab the other way.
	c := send(m, tabKey)
	cf := send(c, tabKey)
	if c.btnFocus != focusCancel || cf.btnFocus != focusConfirm || send(cf, tabKey).btnFocus != focusList {
		t.Errorf("Tab order: %d %d", c.btnFocus, cf.btnFocus)
	}
	if send(m, shiftTab).btnFocus != focusConfirm {
		t.Error("Shift+Tab from the list should reach Confirm")
	}
	// Between the buttons, Left and Right wrap; Up returns to the list.
	if send(c, rightKey).btnFocus != focusConfirm || send(c, leftKey).btnFocus != focusConfirm || send(cf, rightKey).btnFocus != focusCancel {
		t.Error("Left/Right between the buttons")
	}
	if send(cf, up).btnFocus != focusList {
		t.Error("Up from a button did not return to the list")
	}
	// The kernel-profile list has a Details button before them.
	k := openSectionInTab(withSize(newModel(twoTestDisks())), 0, secKernelArgs)
	if d := send(k, tabKey); d.btnFocus != focusDetails || send(d, rightKey).btnFocus != focusCancel {
		t.Errorf("kernel list focus: %d", d.btnFocus)
	}
}

func TestBundles(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secBundles)
	if send(m, up).bundleCursor != 0 {
		t.Error("Up moved above the list")
	}
	// Tick vim and man-pages (which is two packages), then confirm.
	m = send(m, spaceKey)
	for bundleOptions[m.bundleCursor].name != "man-pages" {
		m = send(m, down)
	}
	m = send(m, runes("x"))
	if view := viewOf(m); !strings.Contains(view, "[x] man-pages") {
		t.Errorf("ticked bundle not shown:\n%s", view)
	}
	done := send(m, enter)
	if done.packages != "man-pages mdocml vim" || done.view != viewHub {
		t.Errorf("packages = %q", done.packages)
	}
	// Unticking takes it out again.
	if got := send(send(m, runes("x")), enter); got.packages != "vim" {
		t.Errorf("after unticking: %q", got.packages)
	}
	// Cancel and Esc leave the package list as it was.
	if got := send(send(m, tabKey), enter); got.packages != "" || got.view != viewHub {
		t.Errorf("Cancel set packages to %q", got.packages)
	}
	if got := send(m, escKey); got.packages != "" || got.view != viewHub {
		t.Errorf("Esc set packages to %q", got.packages)
	}
	// The cursor stops on the last bundle, which is on screen.
	for i := 0; i < len(bundleOptions)+3; i++ {
		m = send(m, down)
	}
	if m.bundleCursor != len(bundleOptions)-1 || !strings.Contains(viewOf(m), bundleOptions[len(bundleOptions)-1].name+":") {
		t.Errorf("cursor %d; view:\n%s", m.bundleCursor, viewOf(m))
	}
}

// The docker bundle says "started at boot": its package installs the
// service without enabling it, so the bundle enables it through services:.
// containerd starts through docker's dependency, and a bundle without a
// service enables nothing.
func TestDockerBundleEnablesItsService(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secBundles)
	m = send(m, spaceKey) // vim: packages only
	for bundleOptions[m.bundleCursor].name != "docker" {
		m = send(m, down)
	}
	m = send(m, spaceKey)
	done := send(m, enter)
	if done.packages != "containerd docker vim" || done.services != "docker" {
		t.Errorf("packages = %q, services = %q", done.packages, done.services)
	}
	cfg, err := done.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Services, " ") != "docker" {
		t.Errorf("config services = %q, want [docker]", cfg.Services)
	}
	// Unticking docker takes its service out again.
	got := send(send(m, spaceKey), enter)
	if got.services != "" || got.packages != "vim" {
		t.Errorf("after unticking docker: packages %q, services %q", got.packages, got.services)
	}
	if cfg, _ := got.buildConfig(); cfg.Services != nil {
		t.Errorf("config services with no service bundle = %q", cfg.Services)
	}
}

func TestMirrorForm(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secMirror)
	if m.view != viewMiniForm || !strings.Contains(viewOf(m), "Mirror URL:") {
		t.Fatalf("mirror form:\n%s", viewOf(m))
	}
	m = send(typeText(m, " http://mirror.example/current "), enter)
	if m.mirror != "http://mirror.example/current" || m.formStep != 1 {
		t.Fatalf("mirror %q step %d", m.mirror, m.formStep)
	}
	if view := viewOf(m); !strings.Contains(view, "[ ] Allow installation over insecure connections") || !strings.Contains(view, "Mirror URL: http://mirror.example/current") {
		t.Errorf("checkbox step:\n%s", view)
	}
	// Plain http is off unless ticked.
	if send(m, enter).allowInsecure {
		t.Error("insecure http allowed without ticking the box")
	}
	m = send(m, spaceKey)
	if !m.allowInsecure || !strings.Contains(viewOf(m), "[x] Allow installation") {
		t.Error("the box was not ticked")
	}
	if got := send(m, enter); !got.allowInsecure || got.view != viewHub {
		t.Error("the ticked box was not kept")
	}
	// Another key on the checkbox step does nothing; Esc leaves.
	if got := send(m, runes("q")); got.view != viewMiniForm || !got.allowInsecure {
		t.Error("a stray key changed the checkbox step")
	}
	if send(m, escKey).view != viewHub {
		t.Error("Esc did not leave the form")
	}
	// An empty URL shows as none.
	e := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secMirror)
	if e = send(e, enter); !strings.Contains(viewOf(e), "Mirror URL: -") {
		t.Errorf("empty mirror:\n%s", viewOf(e))
	}
}

func TestKdumpForm(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secKdump)
	if m.input.Value() != defaultCrashkernel || !strings.Contains(viewOf(m), "crashkernel= (size or range, empty = off):") {
		t.Fatalf("kdump form starts with %q:\n%s", m.input.Value(), viewOf(m))
	}
	// A space would split the argument in two at boot: it is taken out.
	m = send(typeText(m, "1G-4G:192M, 4G-:256M"), enter)
	if m.crashkernel != "1G-4G:192M,4G-:256M" || m.input.Value() != defaultPanicSecs {
		t.Fatalf("crashkernel %q, panic field %q", m.crashkernel, m.input.Value())
	}
	if !strings.Contains(viewOf(m), "crashkernel = 1G-4G:192M,4G-:256M") {
		t.Errorf("second step:\n%s", viewOf(m))
	}
	// panic= is a whole number of seconds.
	bad := send(typeText(m, "soon"), enter)
	if bad.view != viewMiniForm || bad.panicSecs != "" || !strings.Contains(viewOf(bad), "panic= takes a whole number of seconds") {
		t.Errorf("a bad panic value was accepted:\n%s", viewOf(bad))
	}
	ok := send(typeText(bad, "30"), enter)
	if ok.panicSecs != "30" || ok.formErr != "" || ok.view != viewHub {
		t.Errorf("panic %q err %q", ok.panicSecs, ok.formErr)
	}
	if got := send(clearInput(m), enter); got.panicSecs != "" || got.view != viewHub {
		t.Errorf("an empty panic should mean none: %q", got.panicSecs)
	}
	// Clearing crashkernel turns kdump off, and no panic timeout is offered.
	off := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secKdump)
	off = send(clearInput(off), enter)
	if off.crashkernel != "" || off.input.Value() != "" {
		t.Errorf("kdump off: crashkernel %q, panic field %q", off.crashkernel, off.input.Value())
	}
	// Reopening starts from what was set.
	again := ok
	again.tab, again.hubCursor = 0, 0
	again = openSectionInTab(again, 1, secKdump)
	if again.input.Value() != "1G-4G:192M,4G-:256M" {
		t.Errorf("reopened with %q", again.input.Value())
	}
}

func TestCustomKernelArguments(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 0, secKernelArgs)
	m.consoles = []string{"console=ttyS0,115200", "console=tty0"}
	if !strings.Contains(viewOf(m), "consoles (auto): console=ttyS0,115200 console=tty0") {
		t.Errorf("the detected consoles are not shown:\n%s", viewOf(m))
	}
	// Details shows the highlighted profile without choosing it.
	d := send(send(m, tabKey), enter)
	if d.popup != popupMessage || d.view != viewList || d.kernelProfile != m.kernelProfile {
		t.Errorf("Details: popup %d view %d", d.popup, d.view)
	}
	// Custom opens the Add field empty when coming from a profile …
	for range kernelProfiles {
		m = send(m, down)
	}
	m = send(m, enter)
	if m.view != viewMiniForm || m.kernelProfile != "Custom" || m.input.Value() != "" {
		t.Fatalf("Custom: view %d profile %q field %q", m.view, m.kernelProfile, m.input.Value())
	}
	if !strings.Contains(viewOf(m), "Add Extra Arguments:") {
		t.Errorf("add step:\n%s", viewOf(m))
	}
	m = send(typeText(m, " quiet loglevel=3 "), enter)
	if m.kernelArgs != "quiet loglevel=3" || !strings.Contains(viewOf(m), "Remove Arguments:") {
		t.Fatalf("after Add: %q\n%s", m.kernelArgs, viewOf(m))
	}
	m = send(typeText(m, "rhgb"), enter)
	if m.kernelArgsRemove != "rhgb" || m.view != viewHub {
		t.Fatalf("after Remove: %q view %d", m.kernelArgsRemove, m.view)
	}
	// … and keeps the operator's own arguments when Custom is reopened.
	m.hubCursor = 0
	again := openSectionInTab(m, 0, secKernelArgs)
	again = send(again, enter) // the list opens on Custom
	if again.input.Value() != "quiet loglevel=3" {
		t.Errorf("Custom reopened with %q", again.input.Value())
	}
	if got := send(send(again, enter), escKey); got.view != viewHub {
		t.Error("Esc did not leave the form")
	}
	// An unknown section in the form view draws an empty panel, not a crash.
	again.editing = secHostname
	_ = again.View()
}

func TestChoiceSections(t *testing.T) {
	base := withSize(newModel(twoTestDisks()))
	m := openSectionInTab(base, 1, secAutoUpdate)
	if m.view != viewSectionChoice {
		t.Fatalf("auto update opened view %d", m.view)
	}
	if got := send(send(m, down), enter); got.autoUpdate != 2 || got.view != viewHub {
		t.Errorf("autoUpdate = %d", got.autoUpdate)
	}
	if got := send(send(send(m, down), up), enter); got.autoUpdate != 1 {
		t.Errorf("autoUpdate = %d", got.autoUpdate)
	}
	if got := send(send(m, down), escKey); got.view != viewHub || got.autoUpdate != base.autoUpdate {
		t.Error("Esc chose something")
	}
	// It reopens on what was chosen.
	chosen := send(send(m, down), enter)
	chosen.tab, chosen.hubCursor = 0, 0
	if got := openSectionInTab(chosen, 1, secAutoUpdate); got.edCursor != 1 {
		t.Errorf("reopened on %d", got.edCursor)
	}

	k := openSectionInTab(base, 1, secKernel)
	if len(kernelOptions) > 1 {
		if got := send(send(k, down), enter); got.kernel != kernelOptions[1] {
			t.Errorf("kernel = %q", got.kernel)
		}
	}
	// Leaving the disk list goes back to the partition editor.
	d := send(openMediaEditor(base), runes("c"))
	if got := send(d, escKey); got.view != viewPartList {
		t.Errorf("Esc from the disk list: view %d", got.view)
	}
}

func TestSaveConfiguration(t *testing.T) {
	if _, err := cryptHash("probe-password"); err != nil {
		t.Skipf("no openssl: %v", err)
	}
	defer func(p string) { saveConfigPath = p }(saveConfigPath)
	dir := t.TempDir()
	saveConfigPath = filepath.Join(dir, "saved.yaml")

	m := readyModel()
	m.netIfaces = []netIfaceEntry{{Name: "eth0"}, {Name: "eth1", Static: true, Addr: "10.0.0.2/24", Gateway: "10.0.0.1", DNS: "10.0.0.1"}}
	saved := openSectionInTab(m, 1, secSaveConfig)
	if saved.popup != popupMessage || !strings.Contains(strings.Join(saved.popupMsg, "\n"), "Saved to "+saveConfigPath) {
		t.Fatalf("save popup: %v", saved.popupMsg)
	}
	if strings.Contains(strings.Join(saved.popupMsg, "\n"), "would refuse") {
		t.Errorf("a complete config said to be refused: %v", saved.popupMsg)
	}
	cfg, err := config.Load(saveConfigPath)
	if err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
	// What was saved is what would be installed: the password only as a
	// hash, and only the interface that was set static.
	data, _ := os.ReadFile(saveConfigPath)
	if strings.Contains(string(data), "correct horse") || !strings.HasPrefix(cfg.Users[0].Password, "$6$") {
		t.Error("the saved config carries the password itself")
	}
	if len(cfg.NetworkInterfaces) != 1 || cfg.NetworkInterfaces[0].Name != "eth1" || cfg.NetworkInterfaces[0].Addrs[0].IP != "10.0.0.2" {
		t.Errorf("saved interfaces = %+v", cfg.NetworkInterfaces)
	}

	// Nowhere to write it.
	// One with no disk chosen is saved, with a word that it would not install.
	saveConfigPath = filepath.Join(dir, "bare.yaml")
	bare := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secSaveConfig)
	if msg := strings.Join(bare.popupMsg, "\n"); !strings.Contains(msg, "Saved to") || !strings.Contains(msg, "would refuse") {
		t.Errorf("no disk chosen: %v", bare.popupMsg)
	}
	saveConfigPath = filepath.Join(dir, "no-such-dir", "saved.yaml")
	if got := openSectionInTab(m, 1, secSaveConfig); !strings.Contains(strings.Join(got.popupMsg, "\n"), "Could not save:") {
		t.Errorf("save failure popup: %v", got.popupMsg)
	}
	// No way to hash the password: nothing is written.
	saveConfigPath = filepath.Join(dir, "unhashed.yaml")
	t.Setenv("PATH", t.TempDir())
	if got := openSectionInTab(m, 1, secSaveConfig); !strings.Contains(strings.Join(got.popupMsg, "\n"), "Could not build the config:") {
		t.Errorf("build failure popup: %v", got.popupMsg)
	}
	if _, err := os.Stat(saveConfigPath); err == nil {
		t.Error("a config was written without a password hash")
	}
}

func TestNetworkTestPopup(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 1, secTestNet)
	if m.popup != popupSpinner || !m.testingNet {
		t.Fatalf("popup %d testing %v", m.popup, m.testingNet)
	}
	// While the check runs, keys do nothing (except Ctrl+C).
	if got := send(m, enter); got.popup != popupSpinner {
		t.Error("a key dismissed the running check")
	}
	if _, cmd := m.Update(ctrlC); cmd == nil {
		t.Error("Ctrl+C did not quit during the check")
	}
	// The tick that animates the bar schedules the next one.
	if _, ok := spinnerTickCmd()().(spinnerTickMsg); !ok {
		t.Error("the spinner tick did not arrive")
	}
}

// Allow insecure installation is ticked for a plain-http mirror; once the
// mirror is cleared or https, the config does not carry it any more.
func TestAllowInsecureOnlyWithAnHTTPMirror(t *testing.T) {
	m := readyModel()
	m.allowInsecure = true
	for mirror, want := range map[string]bool{"http://mirror.lan/current": true, "": false, "https://m.example/current": false, "/srv/repo": false} {
		m.mirror = mirror
		c, err := m.buildConfig()
		if err != nil {
			t.Fatalf("%q: %v", mirror, err)
		}
		if c.AllowInsecureHTTP != want {
			t.Errorf("mirror %q: allowInsecureHTTP %v, want %v", mirror, c.AllowInsecureHTTP, want)
		}
	}
}

// The leave question does not say nothing was installed: the wizard runs
// again after an install, failed or done, when the final screen's "Back
// to menu" is chosen, and by then the disk may be wiped or installed.
func TestQuitQuestionClaimsNothingAboutTheDisk(t *testing.T) {
	m := send(readyModel(), escKey)
	if m.popup != popupQuit {
		t.Fatalf("popup %d", m.popup)
	}
	v := viewOf(m)
	if strings.Contains(v, "Nothing has been installed") {
		t.Errorf("the question says nothing was installed, which is false after Back to menu:\n%s", v)
	}
	if !strings.Contains(v, "lost") {
		t.Errorf("the question does not say the answers are lost:\n%s", v)
	}
}
