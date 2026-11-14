package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/system"
)

// RunFullscreen drives the install as a full-screen menu styled after the
// Clear Linux minimal-ISO installer: a filled panel with [R] Required / [A]
// Advanced tabs, each section a label over a "» value" line with an accent
// selection bar, and Cancel/Install buttons at the foot. Required holds
// Installation Media (disk + a manual partition editor: plain, encrypted,
// LVM), Timezone, Keyboard, Language, Manage User and Hostname; Advanced
// holds the Network Interface, Network Proxy, Storage Mirror, Kernel
// Selection, Kernel Command Line, Additional Packages and Encryption
// Passphrase — mirroring clr-installer's section set. The passphrase and
// the not-ready warning are bordered popups floated over the hub. Sections
// are configured in any order; Install lights up once the layout is
// complete. It returns the config and the LUKS passphrase; cmd uses it on a
// real terminal, the prompt flow otherwise.
func RunFullscreen() (*config.Config, string, error) {
	devs, err := blockdev.List()
	if err != nil {
		return nil, "", err
	}
	// Only disks nothing on the running system uses: not the live medium,
	// nothing mounted, no active swap.
	disks := blockdev.Available(devs)
	if len(disks) == 0 {
		return nil, "", fmt.Errorf("tui: no disk to install to (disks that are mounted, in use as swap or the live medium are not offered)")
	}

	start := newModel(disks)
	start.consoles = system.DetectConsoles() // mirror the live consoles onto the target
	start.vmName, start.vm = system.VirtualMachine()
	start.cpuVendor = system.CPUVendor() // rewrite vendor-specific kernel args (intel/amd)
	start.epp = system.EPPCapable()      // EPP (active-mode) CPUs use only performance/powersave
	final, err := tea.NewProgram(start, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, "", err
	}
	m := final.(model)
	if !m.confirmed {
		return nil, "", fmt.Errorf("cancelled")
	}
	cfg, err := m.buildConfig()
	if err != nil {
		return nil, "", err
	}
	return cfg, m.passphrase, nil
}

// buildConfig turns the wizard's state into a config, in clr-installer's
// shape: the disk layout, the accounts (passwords hashed here), identity,
// and the Advanced-tab values. It is used both to run the install and to
// write the config out from the Save Configuration Settings section.
func (m model) buildConfig() (*config.Config, error) {
	users := make([]*config.User, 0, len(m.users))
	for _, u := range m.users {
		hash, err := cryptHash(u.Password)
		if err != nil {
			return nil, err
		}
		users = append(users, &config.User{Login: u.Login, UserName: u.UserName, Password: hash, Admin: u.Admin})
	}
	cfg := &config.Config{
		TargetMedia: []*config.Media{{
			Name: m.disks[m.diskIdx].Name, Type: "disk",
			Children: layoutToMedia(m.disks[m.diskIdx].Name, m.parts),
		}},
		Hostname: m.hostname, Keyboard: m.keyboard, Language: m.language, Timezone: m.timezone,
		Users:             users,
		Kernel:            m.kernel,
		HTTPSProxy:        strings.TrimSpace(m.proxy),
		CopyNetwork:       m.copyNetwork,
		AutoUpdate:        boolPtr(m.autoUpdate != 2),
		SwupdMirror:       strings.TrimSpace(m.mirror),
		AllowInsecureHTTP: m.allowInsecure,
		ReusePartitions:   m.safeInstall, // Safe install reuses the disk
	}
	// The kernel command line: the chosen arguments, plus the live system's
	// console= arguments so the installed system uses the same console
	// (important for a serial-console install). Duplicates are dropped.
	// A predefined profile already carries the chosen governor (profileArgs);
	// Custom arguments are the operator's own text, so the CPU Frequency
	// Governor section is applied to them here.
	args := m.kernelArgs
	if m.kernelProfile == "Custom" && m.governor != "" {
		args = setGovernor(args, m.governor)
	}
	add := strings.Fields(args)
	for _, c := range m.consoles {
		if !containsStr(add, c) {
			add = append(add, c)
		}
	}
	// kdump: reserve crash memory and, optionally, auto-reboot after a panic.
	if ck := strings.TrimSpace(m.crashkernel); ck != "" {
		if t := "crashkernel=" + ck; !containsStr(add, t) {
			add = append(add, t)
		}
		if ps := strings.TrimSpace(m.panicSecs); ps != "" {
			if _, err := strconv.Atoi(ps); err == nil {
				if t := "panic=" + ps; !containsStr(add, t) {
					add = append(add, t)
				}
			}
		}
	}
	if rem := strings.Fields(m.kernelArgsRemove); len(add) > 0 || len(rem) > 0 {
		cfg.KernelArguments = &config.KernelArguments{Add: add, Remove: rem}
	}
	// Record which Kernel Command Line profile the arguments came from.
	cfg.KernelProfile = m.kernelProfile
	// Console log level: the controller turns this into loglevel=N on the
	// kernel command line and kernel.printk = N 4 1 7 via sysctl.
	if m.logLevel >= 0 {
		lv := m.logLevel
		cfg.ConsoleLogLevel = &lv
	}
	cfg.Packages = splitList(m.packages)
	for _, ni := range m.netIfaces {
		if !ni.Static {
			continue
		}
		if iface := staticInterface(ni.Name, ni.Addr, ni.Gateway, ni.DNS); iface != nil {
			cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)
		}
	}
	return cfg, nil
}

func boolPtr(b bool) *bool { return &b }

// saveConfigPath is where the Save Configuration Settings section writes the
// config. A variable so a test can point it elsewhere.
var saveConfigPath = "slinstaller-config.yaml"

// splitList splits a comma/space-separated list into trimmed, non-empty
// items.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// --- the model ---

type section int

// The hub sections, named and ordered as clr-installer's TUI menu: the
// Required tab first (Timezone … Manage User), then the Advanced tab
// (Network … Save Configuration). clr's Telemetry section is left out:
// Sunlight Linux collects none, so it would have nothing to turn on.
const (
	// Required options.
	secTimezone section = iota
	secLanguage
	secKeyboard
	secMedia
	secUser
	// Advanced options.
	secNetwork
	secProxy
	secTestNet
	secBundles
	secHostname
	secKernelArgs
	secKernel
	secMirror
	secAutoUpdate
	secSaveConfig
	secLogLevel
	secAmdPstate
	secKdump
	secIntelPstate
	secGovernor
	sectionCount
)

// sectionLabels are the hub rows (clr's menu titles); sectionTitles are the
// titles shown at the top of each section's own dialog.
// Both are keyed by section, so adding a section cannot shift another's text;
// TestEverySectionIsLabelled catches one left without.
var sectionLabels = [sectionCount]string{
	secTimezone:    "Choose Timezone",
	secLanguage:    "Choose Language",
	secKeyboard:    "Configure the Keyboard",
	secMedia:       "Configure Installation Media",
	secUser:        "Manage User",
	secNetwork:     "Configure Network Interfaces",
	secProxy:       "Proxy",
	secTestNet:     "Test Network Settings",
	secBundles:     "Select Additional Bundles",
	secHostname:    "Assign Hostname",
	secKernelArgs:  "Kernel Command Line",
	secKernel:      "Kernel Selection",
	secMirror:      "Swupd Mirror",
	secAutoUpdate:  "Automatic OS Updates",
	secSaveConfig:  "Save Configuration Settings",
	secLogLevel:    "Kernel Log Level",
	secAmdPstate:   "AMD P-State Driver",
	secKdump:       "Crash Dump (kdump)",
	secIntelPstate: "Intel P-State Driver",
	secGovernor:    "CPU Frequency Governor",
}

var sectionTitles = [sectionCount]string{
	secTimezone:    "Select System Timezone",
	secLanguage:    "Select System Language",
	secKeyboard:    "Select Keyboard",
	secMedia:       "Select Installation Media",
	secUser:        "Manage User",
	secNetwork:     "Configure Network Interfaces",
	secProxy:       "Configure the Network Proxy",
	secTestNet:     "Test Network Settings",
	secBundles:     "Select Additional Bundles",
	secHostname:    "Assign Hostname",
	secKernelArgs:  "Kernel Command Line",
	secKernel:      "Select Desired Kernel",
	secMirror:      "Swupd Mirror",
	secAutoUpdate:  "Automatic OS Updates",
	secSaveConfig:  "Save Configuration Settings",
	secLogLevel:    "Select Console Log Level",
	secAmdPstate:   "Select AMD P-State Driver Mode",
	secKdump:       "Configure Crash Dump (kdump)",
	secIntelPstate: "Select Intel P-State Driver Mode",
	secGovernor:    "Select CPU Frequency Governor",
}

type view int

const (
	viewHub view = iota
	viewSectionChoice
	viewSectionText
	viewPartList
	viewPartForm
	viewNetForm
	viewNetList
	viewUserList
	viewUserForm
	viewList     // scrollable, filterable selection list (timezone, language, keyboard)
	viewBundles  // the Select Additional Bundles checklist
	viewMiniForm // small multi-field forms: Kernel Command Line, Swupd Mirror
	viewMedia    // the Select Installation Media front dialog
)

// popupKind selects the bordered dialog drawn centred over the hub, the way
// clr-installer floats its passphrase and message windows.
type popupKind int

const (
	popupNone popupKind = iota
	popupPassphrase
	popupMessage
	popupSpinner // a running operation with a small animated indicator
)

type model struct {
	disks []blockdev.Device

	diskIdx            int
	diskSet            bool
	keyboard, language string
	timezone, hostname string
	passphrase         string
	parts              []partEntry

	// Accounts, shown in the clr-style Manage User table. Password is the
	// plaintext typed in the form; it is hashed when the config is built.
	users []userEntry

	// Advanced-tab values.
	proxy            string // httpsProxy
	mirror           string // swupdMirror
	allowInsecure    bool   // allowInsecureHTTP, toggled in the Swupd Mirror form
	kernel           string // clr bundle name: "", kernel-native, kernel-lts
	kernelArgs       string // kernel arguments to add, space-separated
	kernelArgsRemove string // kernel arguments to remove, space-separated
	kernelProfile    string // the chosen Kernel Command Line profile name

	// Computed from the live machine when the kernel-profile list opens, so a
	// CPU-isolation profile can fill isolcpus=/nohz_full=/… and hugepages.
	cpuIso      system.CPUIsolation
	hugeGiB     int
	consoles    []string // live console= args, applied to the installed system
	cpuVendor   string   // "intel"/"amd"/""; rewrites vendor-specific kernel args
	vm          bool     // running under a hypervisor
	vmName      string   // its name from the firmware, when it gives one
	logLevel    int      // console log level: -1 unset, else 0..7 (loglevel= + kernel.printk)
	amdPstate   string   // amd_pstate mode for AMD RT/telco profiles: active/passive/guided or ""
	intelPstate string   // intel_pstate mode for Intel RT/telco profiles: active/passive/… or "" (keep HWP)
	governor    string   // cpufreq.default_governor= override; "" = keep the profile's own
	epp         bool     // CPU is EPP-capable (active mode): only performance/powersave governors
	crashkernel string   // kdump memory reservation (crashkernel=); "" = kdump off
	panicSecs   string   // seconds to auto-reboot after a panic (panic=); "" = not set
	packages    string   // extra xbps packages (shown as "bundles")

	// Network interfaces, listed in the clr-style sub-page. Each is DHCP by
	// default; a static one carries its CIDR address, gateway and DNS.
	netIfaces []netIfaceEntry
	netCursor int // cursor in the interface list
	netEdit   int // interface being configured in the form

	// Tri-state clr toggles: 0 = no choice, 1 = enabled, 2 = disabled.
	// autoUpdate sets the config's autoUpdate, and defaults to enabled as
	// clr does.
	autoUpdate int

	// Test Network Settings runs a connectivity check behind an animated
	// progress bar in a popup, as clr-installer does.
	testingNet  bool
	netProgress float64
	netBar      progress.Model
	netResult   string

	view          view
	tab           int // 0 = Required options, 1 = Advanced options
	hubCursor     int
	width, height int

	// simple section editors
	editing  section
	input    textinput.Model
	edCursor int
	choices  []string // options for a generic choice view (e.g. kernel)

	// Scrollable, type-to-filter list (Timezone, Language, Keyboard):
	// listItems are the candidates, listFilter narrows them, listCursor is
	// the position within the filtered view.
	listItems  []string
	listFilter string
	listCursor int

	// Select Additional Bundles checklist: which bundles are ticked, and the
	// cursor over them.
	bundleChecked []bool
	bundleCursor  int

	// btnFocus is where keyboard focus is on a page whose content is a list
	// above a Cancel / Confirm pair (the selection lists, the bundles):
	// focusList, focusCancel or focusConfirm. Tab moves it, as in
	// clr-installer.
	btnFocus int

	// Installation Media front dialog: the chosen mode, the cursor over the
	// dialog's rows, and whether encryption is ticked. safeInstall marks a
	// Safe/alongside layout (reuse the disk, add Sunlight in free space).
	mediaMode   int // 0 Destructive, 1 Advanced, 2 Safe
	mediaCursor int
	encrypt     bool
	safeInstall bool

	// popup: a bordered dialog drawn over the hub.
	popup    popupKind
	popupMsg []string

	// partition editor
	partCursor int       // index into parts (and -1.. for buttons)
	form       partEntry // the entry being built
	formLV     lvEntry
	formStep   int
	formIsLV   bool

	// Manage User editor: a cursor over the table rows/buttons and the
	// account being built in the Add/Edit User form. editUserIdx is the row
	// being edited, or -1 when adding a new account.
	userCursor  int
	userForm    userEntry
	editUserIdx int
	formErr     string // why a form's last entry was refused (user form, kdump)
	pwConfirm   string // the password typed a second time

	copyNetwork bool // the network test passed: copy the live network config

	// The passphrase popup: the first entry while the second is typed, and
	// why the last one was refused.
	passFirst, popupErr string

	confirmed bool
}

func newModel(disks []blockdev.Device) model {
	bar := progress.New(progress.WithSolidFill(barFill(termenv.ColorProfile())))
	bar.Width = 40
	bar.ShowPercentage = false
	return model{
		disks: disks, view: viewHub,
		keyboard: "us", language: "en_US.UTF-8", timezone: "UTC", hostname: "sunlight",
		autoUpdate: 1, // clr defaults automatic updates on
		netBar:     bar,
		// Default the kernel command line to the Server profile so a plain
		// install gets sensible server tuning without opening the section.
		kernelProfile: defaultKernelProfile,
		kernelArgs:    kernelProfileByName(defaultKernelProfile).args,
		logLevel:      -1,       // console log level unset: keep the kernel default
		amdPstate:     "active", // AMD RT/telco default: hardware-managed P-states
	}
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinnerTickMsg:
		if m.testingNet {
			// The check has no real percentage, so the bar pulses: fill, then
			// wrap and fill again until the result arrives.
			m.netProgress += 0.06
			if m.netProgress > 1 {
				m.netProgress = 0
			}
			return m, spinnerTickCmd()
		}
		return m, nil
	case netTestDoneMsg:
		// The check finished: swap the spinner for a result message popup.
		m.testingNet = false
		m.netResult = msg.result
		m.popup = popupMessage
		m.popupMsg = []string{"Test Network Settings", "", msg.result}
		if msg.ok {
			// As clr-installer does: a network shown to work on the live
			// system is carried into the installed one (copyNetwork), so a
			// Wi-Fi or hand-set connection still works after the reboot.
			m.copyNetwork = true
			m.popupMsg = append(m.popupMsg, "", "This network setup will be copied to the installed system.")
		}
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	// A popup, when open, takes all keys until it is dismissed.
	if m.popup != popupNone {
		return m.updatePopup(key)
	}
	switch m.view {
	case viewHub:
		return m.updateHub(key)
	case viewSectionChoice:
		return m.updateSectionChoice(key)
	case viewSectionText:
		return m.updateSectionText(key)
	case viewPartList:
		return m.updatePartList(key)
	case viewPartForm:
		return m.updatePartForm(key)
	case viewNetForm:
		return m.updateNetForm(key)
	case viewNetList:
		return m.updateNetList(key)
	case viewUserList:
		return m.updateUserList(key)
	case viewUserForm:
		return m.updateUserForm(key)
	case viewList:
		return m.updateList(key)
	case viewBundles:
		return m.updateBundles(key)
	case viewMiniForm:
		return m.updateMiniForm(key)
	case viewMedia:
		return m.updateMedia(key)
	}
	return m, nil
}

// updatePopup handles the bordered dialog over the hub: a message popup
// closes on any key; the passphrase popup edits an input and commits on
// Enter (Esc discards).
func (m model) updatePopup(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.popup == popupSpinner {
		// A running operation ignores keys until it reports back.
		return m, nil
	}
	if m.popup == popupMessage {
		m.popup = popupNone
		return m, nil
	}
	// passphrase: typed twice, as a mistake here cannot be undone later. A
	// mistyped LUKS passphrase locks the disk it was set on.
	switch key.String() {
	case "esc":
		m.popup = popupNone
		return m, nil
	case "enter":
		v := m.input.Value()
		if m.passFirst == "" {
			if err := config.ValidPassphrase(v); err != nil {
				m.popupErr = err.Error()
				return m, nil
			}
			m.popupErr, m.passFirst = "", v
			m.input = passwordInput("")
			return m, textinput.Blink
		}
		if v != m.passFirst {
			// Back to the first entry: either may hold the typo.
			m.passFirst, m.popupErr = "", "the passphrases do not match; type it again"
			m.input = passwordInput("")
			return m, textinput.Blink
		}
		m.passphrase, m.passFirst, m.popupErr = v, "", ""
		m.popup = popupNone
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// --- hub ---

func (m model) canInstall() bool {
	return m.diskSet && hasAdmin(m.users) && hasRoot(m.parts) && hasESP(m.parts) &&
		(!hasCrypt(m.parts) || m.passphrase != "")
}

func (m model) View() string {
	var base string
	switch m.view {
	case viewHub:
		base = m.hubView()
	case viewSectionChoice:
		base = m.choiceView()
	case viewSectionText:
		base = m.textView()
	case viewPartList:
		base = m.partListView()
	case viewPartForm:
		base = m.partFormView()
	case viewNetForm:
		base = m.netFormView()
	case viewNetList:
		base = m.netListView()
	case viewUserList:
		base = m.userListView()
	case viewUserForm:
		base = m.userFormView()
	case viewList:
		base = m.listView()
	case viewBundles:
		base = m.bundlesView()
	case viewMiniForm:
		base = m.miniFormView()
	case viewMedia:
		base = m.mediaView()
	}
	if m.popup != popupNone {
		return overlayCenter(base, m.popupBox(), m.width, m.height)
	}
	return base
}

// popupBox renders the current popup as a bordered box: a title, the body,
// and a hint — the floating window clr-installer draws over its menu.
func (m model) popupBox() string {
	var inner []string
	switch m.popup {
	case popupPassphrase:
		prompt := fmt.Sprintf("Enter the LUKS passphrase (%d to %d characters):", config.MinPassphraseLength, config.MaxPassphraseLength)
		if m.passFirst != "" {
			prompt = "Type the passphrase again:"
		}
		inner = []string{
			popupTitle.Render("Encryption Passphrase"),
			"",
			prompt,
			popupInput.Render(m.input.View()),
			"",
		}
		if m.popupErr != "" {
			inner = append(inner, m.popupErr, "")
		}
		inner = append(inner, popupHint.Render("[Enter] confirm · [Esc] cancel"))
	case popupMessage:
		// popupMsg[0] is the title; the rest is the body.
		title, body := "", m.popupMsg
		if len(m.popupMsg) > 0 {
			title, body = m.popupMsg[0], m.popupMsg[1:]
		}
		inner = append([]string{popupTitle.Render(title), ""}, body...)
		inner = append(inner, "", popupHint.Render("press any key to continue"))
	case popupSpinner:
		inner = []string{
			popupTitle.Render("Test Network Settings"),
			"",
			m.netBar.ViewAs(m.netProgress),
			"",
			"Testing connectivity...",
		}
	}
	return popupBoxStyle.Render(strings.Join(inner, "\n"))
}
