package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/network"
)

// --- Test Network Settings: a secondary-progress popup ---

// netTestTarget is the endpoint the connectivity check probes. A plain,
// widely-reachable host so the test reflects general network health, not a
// specific mirror.
const netTestTarget = "https://repo-default.voidlinux.org/current"

type spinnerTickMsg struct{}
type netTestDoneMsg struct{ result string }

var spinnerFrames = []string{"|", "/", "-", "\\"}

// spinnerTickCmd schedules the next spinner frame.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// testNetCmd runs the connectivity check off the UI goroutine and reports
// the outcome as a message.
func testNetCmd() tea.Cmd {
	return func() tea.Msg {
		if err := network.VerifyConnectivity(netTestTarget); err != nil {
			return netTestDoneMsg{result: "✗ No connectivity: " + err.Error()}
		}
		return netTestDoneMsg{result: "✓ Network is reachable."}
	}
}

// RunFullscreen drives the install as a full-screen menu styled after the
// Clear Linux minimal-ISO installer: a blue panel with [R] Required / [A]
// Advanced tabs, each section a label over a "» value" line with a teal
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
	disks := blockdev.Disks(devs)
	if len(disks) == 0 {
		return nil, "", fmt.Errorf("tui: no disks found")
	}

	final, err := tea.NewProgram(newModel(disks), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, "", err
	}
	m := final.(model)
	if !m.confirmed {
		return nil, "", fmt.Errorf("cancelled")
	}

	hash, err := cryptHash(m.password)
	if err != nil {
		return nil, "", err
	}
	cfg := &config.Config{
		TargetMedia: []*config.Media{{
			Name: m.disks[m.diskIdx].Name, Type: "disk",
			Children: layoutToMedia(m.disks[m.diskIdx].Name, m.parts),
		}},
		Hostname: m.hostname, Keyboard: m.keyboard, Language: m.language, Timezone: m.timezone,
		Users:       []*config.User{{Login: m.login, Password: hash, Admin: true}},
		Kernel:      m.kernel,
		HTTPSProxy:  strings.TrimSpace(m.proxy),
		SwupdMirror: strings.TrimSpace(m.mirror),
	}
	if args := strings.Fields(m.kernelArgs); len(args) > 0 {
		cfg.KernelArguments = &config.KernelArguments{Add: args}
	}
	cfg.Packages = splitList(m.packages)
	if iface := staticInterface(m.netIface, m.netAddr, m.netGateway, m.netDNS); iface != nil {
		cfg.NetworkInterfaces = []*config.Interface{iface}
	}
	return cfg, m.passphrase, nil
}

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

// staticInterface builds a config.Interface for a single static interface
// from the network form's fields, parsing the CIDR address into an IP and a
// dotted netmask. A blank name (DHCP) or an unparseable address yields nil.
func staticInterface(name, cidr, gateway, dns string) *config.Interface {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	iface := &config.Interface{Name: name, DHCP: "false", Gateway: strings.TrimSpace(gateway), DNS: strings.TrimSpace(dns)}
	if a := parseCIDR(strings.TrimSpace(cidr)); a != nil {
		iface.Addrs = []*config.Address{a}
	}
	return iface
}

// parseCIDR turns "192.168.1.50/24" into a config.Address (dotted netmask
// for IPv4, prefix length kept for IPv6), or nil if it does not parse.
func parseCIDR(s string) *config.Address {
	if s == "" {
		return nil
	}
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		mask := ipnet.Mask
		return &config.Address{IP: v4.String(), Netmask: fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3]), Version: 0}
	}
	ones, _ := ipnet.Mask.Size()
	return &config.Address{IP: ip.String(), Netmask: strconv.Itoa(ones), Version: 1}
}

// --- the layout data model (separate from the UI, so it is testable) ---

type lvEntry struct {
	Name, FS, Mount, Size string
}

type partEntry struct {
	Type      string // "part", "crypt", "lvm"
	FS        string
	Mount     string
	Size      string
	VG        string // for lvm
	Encrypted bool   // for lvm: the PV is a LUKS container (LVM-on-LUKS)
	LVs       []lvEntry
}

// layoutToMedia turns the editor's partition list into config children,
// numbering the physical partitions (sda1, sda2, ...) and nesting an
// lvm partition's logical volumes.
func layoutToMedia(disk string, parts []partEntry) []*config.Media {
	var out []*config.Media
	for i, p := range parts {
		name := partDevName(disk, i+1)
		m := &config.Media{Name: name, Type: p.Type, FsType: p.FS, Mountpoint: p.Mount, Size: p.Size}
		if p.Type == "lvm" {
			m.FsType, m.Mountpoint = "", ""
			m.VolumeGroup = p.VG
			m.Encrypted = p.Encrypted
			for _, lv := range p.LVs {
				m.Children = append(m.Children, &config.Media{
					Name: lv.Name, FsType: lv.FS, Mountpoint: lv.Mount, Size: lv.Size,
				})
			}
		}
		out = append(out, m)
	}
	return out
}

func hasRoot(parts []partEntry) bool {
	for _, p := range parts {
		if p.Mount == "/" {
			return true
		}
		for _, lv := range p.LVs {
			if lv.Mount == "/" {
				return true
			}
		}
	}
	return false
}

func hasESP(parts []partEntry) bool {
	for _, p := range parts {
		if p.FS == "vfat" && (p.Mount == "/boot" || p.Mount == "/boot/efi") {
			return true
		}
	}
	return false
}

func hasCrypt(parts []partEntry) bool {
	for _, p := range parts {
		if p.Type == "crypt" || p.Encrypted {
			return true
		}
	}
	return false
}

// --- the model ---

type section int

// The hub sections, named and ordered exactly as clr-installer's TUI menu:
// the Required tab first (Timezone … Telemetry), then the Advanced tab
// (Network … Save Configuration). This is the phase-1 1:1 match; some
// sections (Telemetry, Bundles, Automatic OS Updates) are presented for
// fidelity and are not yet wired to a Sunlight behaviour.
const (
	// Required options.
	secTimezone section = iota
	secLanguage
	secKeyboard
	secMedia
	secUser
	secTelemetry
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
	sectionCount
)

// sectionLabels are the hub rows (clr's menu titles); sectionTitles are the
// titles shown at the top of each section's own dialog.
var sectionLabels = [sectionCount]string{
	"Choose Timezone", "Choose Language", "Configure the Keyboard",
	"Configure Installation Media", "Manage User", "Telemetry",
	"Configure Network Interfaces", "Proxy", "Test Network Settings",
	"Select Additional Bundles", "Assign Hostname", "Kernel Command Line",
	"Kernel Selection", "Swupd Mirror", "Automatic OS Updates",
	"Save Configuration Settings",
}

var sectionTitles = [sectionCount]string{
	"Select System Timezone", "Select System Language", "Select Keyboard",
	"Select Installation Media", "Manage User", "Telemetry",
	"Configure Network Interfaces", "Configure the Network Proxy", "Test Network Settings",
	"Select Additional Bundles", "Assign Hostname", "Kernel Command Line",
	"Select Desired Kernel", "Swupd Mirror", "Automatic OS Updates",
	"Save Configuration Settings",
}

type view int

const (
	viewHub view = iota
	viewSectionChoice
	viewSectionText
	viewPartList
	viewPartForm
	viewNetForm
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

	diskIdx                     int
	diskSet, userSet            bool
	keyboard, language          string
	timezone, hostname          string
	login, password, passphrase string
	parts                       []partEntry

	// Advanced-tab values.
	proxy      string // httpsProxy
	mirror     string // swupdMirror
	kernel     string // clr bundle name: "", kernel-native, kernel-lts
	kernelArgs string // extra kernel command-line arguments, space-separated
	packages   string // extra xbps packages (shown as "bundles"), space/comma-separated
	// A single optional static interface; empty netIface means DHCP.
	netIface, netAddr, netGateway, netDNS string

	// Tri-state clr toggles: 0 = no choice, 1 = enabled, 2 = disabled.
	// telemetry and autoUpdate are presented for clr fidelity; autoUpdate
	// defaults to enabled as clr does.
	telemetry, autoUpdate int

	// Test Network Settings runs a connectivity check with a small spinner
	// popup — a secondary progress indicator like clr's.
	testingNet   bool
	spinnerFrame int
	netResult    string

	view          view
	tab           int // 0 = Required options, 1 = Advanced options
	hubCursor     int
	width, height int

	// simple section editors
	editing  section
	input    textinput.Model
	edCursor int
	choices  []string // options for a generic choice view (e.g. kernel)

	// popup: a bordered dialog drawn over the hub.
	popup    popupKind
	popupMsg []string

	// partition editor
	partCursor int       // index into parts (and -1.. for buttons)
	form       partEntry // the entry being built
	formLV     lvEntry
	formStep   int
	formIsLV   bool

	confirmed bool
}

func newModel(disks []blockdev.Device) model {
	return model{
		disks: disks, view: viewHub,
		keyboard: "us", language: "en_US.UTF-8", timezone: "UTC", hostname: "sunlight", login: "user",
		autoUpdate: 1, // clr defaults automatic updates on
	}
}

// The Clear Linux installer palette: a royal-blue dialog centred on a black
// screen, a black tab strip with a teal active tab, white labels over cyan
// "» value" lines, and a tall teal bar — with a magenta label — on the
// selected row. Every content line is rendered at the panel width so its
// background spans the dialog.
var (
	clrBlue    = lipgloss.Color("20")  // dialog background (royal blue)
	clrBlack   = lipgloss.Color("0")   // the screen around the dialog, and the tab strip
	clrTeal    = lipgloss.Color("37")  // selection / active tab
	clrWhite   = lipgloss.Color("231") // primary text
	clrValue   = lipgloss.Color("51")  // "» value" text (cyan)
	clrDim     = lipgloss.Color("152") // footer / log text
	clrRule    = lipgloss.Color("66")  // separator rules
	clrInk     = lipgloss.Color("16")  // dark text on a teal bar
	clrSelText = lipgloss.Color("201") // selected label (magenta), as clr renders it

	// The dialog: a filled blue block, no border, centred on the black
	// screen — the shape clr-installer draws.
	boxStyle = lipgloss.NewStyle().Background(clrBlue).Padding(1, 2)

	// Full-width line backgrounds.
	lineBG = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlue)
	selBG  = lipgloss.NewStyle().Foreground(clrInk).Background(clrTeal)

	titleBar = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrBlue)
	footer   = lipgloss.NewStyle().Foreground(clrDim).Background(clrBlue)
	ruleBG   = lipgloss.NewStyle().Foreground(clrRule).Background(clrBlue)

	// Tabs sit on a black strip; the active one is a teal block.
	tabStrip    = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(clrInk).Background(clrTeal)
	tabInactive = lipgloss.NewStyle().Foreground(clrWhite).Background(clrBlack)

	// Hub items: a bold label then a "» value" line, highlighted when selected.
	itemLabel    = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrBlue)
	itemValue    = lipgloss.NewStyle().Foreground(clrValue).Background(clrBlue)
	itemSelLabel = lipgloss.NewStyle().Bold(true).Foreground(clrSelText).Background(clrTeal)
	itemSelValue = lipgloss.NewStyle().Foreground(clrInk).Background(clrTeal)

	// Buttons.
	btnSel   = lipgloss.NewStyle().Bold(true).Foreground(clrInk).Background(clrTeal).Padding(0, 2)
	btnIdle  = lipgloss.NewStyle().Foreground(clrInk).Background(lipgloss.Color("250")).Padding(0, 2)
	btnOff   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("238")).Padding(0, 2)
	warnText = lipgloss.NewStyle().Foreground(lipgloss.Color("221")).Background(clrBlue)

	// Popup: a bordered window floated over the hub. A lighter blue fill and
	// a white border set it apart from the dialog behind it.
	clrPopup      = lipgloss.Color("25")
	popupBoxStyle = lipgloss.NewStyle().Background(clrPopup).Foreground(clrWhite).
			Border(lipgloss.RoundedBorder()).BorderForeground(clrWhite).
			BorderBackground(clrBlack).Padding(1, 3)
	popupTitle = lipgloss.NewStyle().Bold(true).Foreground(clrWhite).Background(clrPopup)
	popupHint  = lipgloss.NewStyle().Foreground(clrDim).Background(clrPopup)
	popupInput = lipgloss.NewStyle().Foreground(clrWhite).Background(lipgloss.Color("17")).Padding(0, 1)
)

// overlayCenter composites a popup box centred over a base screen: the base
// rows above and below the popup stay visible, so the popup reads as a
// window floating on the hub. The popup's own rows (black-backed, holding
// the bordered box centred horizontally) replace the base rows they cover.
func overlayCenter(base, popup string, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	baseLines := strings.Split(base, "\n")
	popLines := strings.Split(popup, "\n")
	start := (len(baseLines) - len(popLines)) / 2
	if start < 0 {
		start = 0
	}
	for i, pl := range popLines {
		row := start + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = lipgloss.PlaceHorizontal(width, lipgloss.Center, pl,
			lipgloss.WithWhitespaceBackground(clrBlack))
	}
	return strings.Join(baseLines, "\n")
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinnerTickMsg:
		if m.testingNet {
			m.spinnerFrame++
			return m, spinnerTickCmd()
		}
		return m, nil
	case netTestDoneMsg:
		// The check finished: swap the spinner for a result message popup.
		m.testingNet = false
		m.netResult = msg.result
		m.popup = popupMessage
		m.popupMsg = []string{"Test Network Settings", "", msg.result}
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
	// passphrase
	switch key.String() {
	case "esc":
		m.popup = popupNone
		return m, nil
	case "enter":
		m.passphrase = m.input.Value()
		m.popup = popupNone
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// updateNetForm drives the single-interface static-network form: a name
// (blank = leave everything on DHCP), then address (CIDR), gateway and DNS.
func (m model) updateNetForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		switch m.formStep {
		case 0:
			if v == "" {
				// DHCP: clear any previous static config.
				m.netIface, m.netAddr, m.netGateway, m.netDNS = "", "", "", ""
				m.view = viewHub
				return m, nil
			}
			m.netIface = v
		case 1:
			m.netAddr = v
		case 2:
			m.netGateway = v
		case 3:
			m.netDNS = v
			m.view = viewHub
			return m, nil
		}
		m.formStep++
		ti := textinput.New()
		ti.Focus()
		switch m.formStep {
		case 1:
			ti.SetValue(m.netAddr)
		case 2:
			ti.SetValue(m.netGateway)
		case 3:
			ti.SetValue(m.netDNS)
		}
		m.input = ti
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// --- hub ---

func (m model) canInstall() bool {
	return m.diskSet && m.userSet && hasRoot(m.parts) && hasESP(m.parts) &&
		(!hasCrypt(m.parts) || m.passphrase != "")
}

// visibleSections lists the sections shown under a tab. Required holds the
// everyday choices; Advanced the ones most installs leave at their defaults —
// mirroring clr-installer's [R] Required / [A] Advanced split.
func visibleSections(tab int) []section {
	if tab == 1 {
		return []section{secNetwork, secProxy, secTestNet, secBundles, secHostname,
			secKernelArgs, secKernel, secMirror, secAutoUpdate, secSaveConfig}
	}
	return []section{secTimezone, secLanguage, secKeyboard, secMedia, secUser, secTelemetry}
}

func (m model) updateHub(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := visibleSections(m.tab)
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
		// Installation Media: the disk and its partitions in one dialog.
		m.view = viewPartList
		m.partCursor = 0
		return m, nil
	case secKernel:
		m.view = viewSectionChoice
		m.choices = kernelOptions
		m.edCursor = indexOf(kernelOptions, m.kernel)
		return m, nil
	case secTelemetry:
		m.view = viewSectionChoice
		m.choices = yesNoOptions
		m.edCursor = triToIndex(m.telemetry)
		return m, nil
	case secAutoUpdate:
		m.view = viewSectionChoice
		m.choices = yesNoOptions
		m.edCursor = triToIndex(m.autoUpdate)
		return m, nil
	case secNetwork:
		// A small multi-field form for one static interface (blank = DHCP).
		m.view = viewNetForm
		m.formStep = 0
		ti := textinput.New()
		ti.Focus()
		ti.SetValue(m.netIface)
		m.input = ti
		return m, textinput.Blink
	case secTestNet:
		// A secondary-progress popup: run a connectivity check while a small
		// spinner animates, like clr's "Test Network Settings".
		m.popup = popupSpinner
		m.testingNet = true
		m.spinnerFrame = 0
		m.netResult = ""
		return m, tea.Batch(spinnerTickCmd(), testNetCmd())
	case secSaveConfig:
		// The config is always written during the install (the controller's
		// save-config stage). Here, say so, clr-style.
		m.popup = popupMessage
		m.popupMsg = []string{
			"Save Configuration Settings",
			"",
			"The installation configuration is saved automatically",
			"during the install, to /root/slinstaller.yaml on the",
			"target system.",
		}
		return m, nil
	default: // text sections
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
		m.formStep = 0 // for the two-field user editor
		return m, textinput.Blink
	}
}

// kernelOptions are the kernels offered in the selection dialog; the empty
// first entry keeps the base system's default kernel.
var kernelOptions = []string{"", "kernel-native", "kernel-lts"}

// yesNoOptions back the Enabled/Disabled choice pages (Telemetry, Automatic
// OS Updates).
var yesNoOptions = []string{"Enabled", "Disabled"}

// triToIndex maps a tri-state toggle (0 unset, 1 on, 2 off) to the
// Enabled/Disabled list index (defaulting to Enabled when unset).
func triToIndex(t int) int {
	if t == 2 {
		return 1
	}
	return 0
}

// kernelLabel is how a kernel option reads in the dialog and the hub.
func kernelLabel(k string) string {
	switch k {
	case "":
		return "(default)"
	case "kernel-native":
		return "linux (current)"
	case "kernel-lts":
		return "linux-lts (long-term support)"
	default:
		return k
	}
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
		if m.userSet {
			return m.login + " (admin)"
		}
		return "No users added"
	case secTelemetry:
		return triLabel(m.telemetry)
	case secNetwork:
		if m.netIface == "" {
			return "Automatic (DHCP)"
		}
		return fmt.Sprintf("%s: %s", m.netIface, m.netAddr)
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
		if strings.TrimSpace(m.kernelArgs) == "" {
			return "No kernel command line configuration defined"
		}
		return m.kernelArgs
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
		return "—"
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
			switch m.editing {
			case secTelemetry:
				m.telemetry = m.edCursor + 1 // 1 Enabled, 2 Disabled
			case secAutoUpdate:
				m.autoUpdate = m.edCursor + 1
			default: // kernel
				m.kernel = m.choices[m.edCursor]
			}
			m.choices = nil
			m.view = viewHub
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
		case secUser:
			if m.formStep == 0 {
				if v == "" {
					return m, nil
				}
				m.login = v
				m.formStep = 1
				ti := textinput.New()
				ti.Focus()
				ti.EchoMode = textinput.EchoPassword
				m.input = ti
				return m, textinput.Blink
			}
			m.password = m.input.Value()
			m.userSet = true
		}
		m.view = viewHub
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// --- partition editor ---

func (m model) updatePartList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "c": // change the target disk
		m.view = viewSectionChoice
		m.choices = nil // disk mode
		m.edCursor = m.diskIdx
		return m, nil
	case "up", "k":
		if m.partCursor > 0 {
			m.partCursor--
		}
	case "down", "j":
		if m.partCursor < len(m.parts)-1 {
			m.partCursor++
		}
	case "a":
		return m.startForm(false)
	case "l": // add a logical volume to the selected lvm partition
		if m.partCursor < len(m.parts) && m.parts[m.partCursor].Type == "lvm" {
			return m.startForm(true)
		}
	case "d":
		if len(m.parts) > 0 {
			m.parts = append(m.parts[:m.partCursor], m.parts[m.partCursor+1:]...)
			if m.partCursor > 0 {
				m.partCursor--
			}
		}
	}
	return m, nil
}

func (m model) startForm(isLV bool) (tea.Model, tea.Cmd) {
	m.view = viewPartForm
	m.formStep = 0
	m.formIsLV = isLV
	m.edCursor = 0
	if isLV {
		m.formLV = lvEntry{}
		m.initFormInput()
	} else {
		m.form = partEntry{Type: "part"}
	}
	return m, textinput.Blink
}

// formChoices returns the options for the current form step, or nil for
// a text step.
func (m model) formChoices() []string {
	if m.formIsLV {
		if m.formStep == 1 {
			return rootFilesystems
		}
		return nil
	}
	switch m.formStep {
	case 0:
		return []string{"part", "crypt", "lvm", "lvm-encrypted"}
	case 1:
		if m.form.Type == "lvm" {
			return nil // volumeGroup is text
		}
		return append(append([]string{}, rootFilesystems...), "vfat", "swap")
	default:
		return nil
	}
}

func (m *model) initFormInput() {
	ti := textinput.New()
	ti.Focus()
	m.input = ti
}

func (m model) updatePartForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.view = viewPartList
		return m, nil
	}
	if m.formChoices() != nil {
		return m.updateFormChoice(key)
	}
	return m.updateFormText(key)
}

func (m model) updateFormChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	ch := m.formChoices()
	switch key.String() {
	case "up", "k":
		if m.edCursor > 0 {
			m.edCursor--
		}
	case "down", "j":
		if m.edCursor < len(ch)-1 {
			m.edCursor++
		}
	case "enter":
		return m.commitFormField(ch[m.edCursor])
	}
	return m, nil
}

func (m model) updateFormText(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		return m.commitFormField(strings.TrimSpace(m.input.Value()))
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// openPassphrasePopup floats the masked LUKS passphrase prompt over the
// current view (the media dialog), the way clr asks for it when an
// encrypted partition is added.
func (m model) openPassphrasePopup() (tea.Model, tea.Cmd) {
	m.popup = popupPassphrase
	ti := textinput.New()
	ti.Focus()
	ti.EchoMode = textinput.EchoPassword
	ti.SetValue(m.passphrase)
	m.input = ti
	return m, textinput.Blink
}

// commitFormField records the current field and moves to the next, or
// finishes the form.
func (m model) commitFormField(v string) (tea.Model, tea.Cmd) {
	if m.formIsLV {
		switch m.formStep {
		case 0: // name
			if v == "" {
				return m, nil
			}
			m.formLV.Name = v
		case 1: // fs
			m.formLV.FS = v
		case 2: // mount
			m.formLV.Mount = v
		case 3: // size
			m.formLV.Size = v
			p := &m.parts[m.partCursor]
			p.LVs = append(p.LVs, m.formLV)
			m.view = viewPartList
			return m, nil
		}
		m.formStep++
		m.edCursor = 0
		m.initFormInput()
		return m, textinput.Blink
	}

	switch m.formStep {
	case 0: // type
		if v == "lvm-encrypted" {
			m.form.Type, m.form.Encrypted = "lvm", true
		} else {
			m.form.Type = v
		}
	case 1: // fs or vg
		if m.form.Type == "lvm" {
			if v == "" {
				v = "vg0"
			}
			m.form.VG = v
		} else {
			m.form.FS = v
		}
	case 2: // mountpoint (skipped for lvm/swap below)
		m.form.Mount = v
	case 3: // size
		m.form.Size = v
		m.parts = append(m.parts, m.form)
		m.view = viewPartList
		m.partCursor = len(m.parts) - 1
		// clr collects the LUKS passphrase from the media dialog: the moment
		// an encrypted partition is added and none is set, pop the prompt.
		if (m.form.Type == "crypt" || m.form.Encrypted) && m.passphrase == "" {
			return m.openPassphrasePopup()
		}
		return m, nil
	}

	m.formStep++
	// Skip the mountpoint for an LVM PV and for swap.
	if m.formStep == 2 && (m.form.Type == "lvm" || m.form.FS == "swap") {
		m.formStep = 3
	}
	m.edCursor = 0
	if m.formChoices() == nil {
		m.initFormInput()
	}
	return m, textinput.Blink
}

// --- views ---

// contentWidth is the width every content line is rendered to: the inner
// width of the centred dialog, not the terminal. Capped so the UI stays
// readable on a wide screen and bounded so it fits a small one.
func contentWidth(width int) int {
	if width <= 0 {
		width = 80
	}
	cw := width - 10 // leave room for the border, padding and margins
	if cw > 86 {
		cw = 86
	}
	if cw < 40 {
		cw = 40
	}
	return cw
}

// frameScreen frames a view the way clr-installer does: a filled blue dialog
// — bracketed title, the body, a rule and the footer hint — centred on a
// black screen. Both front-ends (the hub and the install progress view)
// render through it, so they match.
func frameScreen(width, height int, body []string, foot string) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	cw := contentWidth(width)
	lines := []string{
		titleBar.Width(cw).Render("[Sunlight Linux Installer]"),
	}
	lines = append(lines, body...)
	lines = append(lines, rule(cw), footer.Width(cw).Render(foot))
	box := boxStyle.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(clrBlack))
}

func (m model) panelWidth() int { return contentWidth(m.width) }

func (m model) screen(body []string, foot string) string {
	return frameScreen(m.width, m.height, body, foot)
}

func rule(w int) string { return ruleBG.Width(w).Render(strings.Repeat("─", w)) }

// row renders a plain full-width line, highlighted as a teal bar when selected.
func row(w int, selected bool, text string) string {
	if selected {
		return selBG.Width(w).Render("  " + text)
	}
	return lineBG.Width(w).Render("  " + text)
}

// item renders a hub entry: a bold label line and a "» value" line. When
// selected it becomes a taller teal bar — a blank teal line above and below
// the two lines — the way clr-installer highlights the focused row.
func item(w int, selected bool, label, value string) []string {
	if selected {
		blank := selBG.Width(w).Render("")
		return []string{
			blank,
			itemSelLabel.Width(w).Render("  " + label),
			itemSelValue.Width(w).Render("    » " + value),
			blank,
		}
	}
	return []string{
		itemLabel.Width(w).Render("  " + label),
		itemValue.Width(w).Render("    » " + value),
	}
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
		inner = []string{
			popupTitle.Render("Encryption Passphrase"),
			"",
			"Enter the LUKS passphrase:",
			popupInput.Render(m.input.View()),
			"",
			popupHint.Render("[Enter] confirm · [Esc] cancel"),
		}
	case popupMessage:
		// popupMsg[0] is the title; the rest is the body.
		title, body := "", m.popupMsg
		if len(m.popupMsg) > 0 {
			title, body = m.popupMsg[0], m.popupMsg[1:]
		}
		inner = append([]string{popupTitle.Render(title), ""}, body...)
		inner = append(inner, "", popupHint.Render("press any key to continue"))
	case popupSpinner:
		frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		inner = []string{
			popupTitle.Render("Test Network Settings"),
			"",
			frame + " Checking connectivity to the mirror...",
		}
	}
	return popupBoxStyle.Render(strings.Join(inner, "\n"))
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

	vis := visibleSections(m.tab)
	for i, s := range vis {
		body = append(body, item(w, m.hubCursor == i, sectionLabels[s], m.sectionValue(s))...)
		body = append(body, rule(w))
	}

	body = append(body, lineBG.Width(w).Render(""))

	// Cancel / Install buttons.
	cancel := btnIdle.Render("Cancel")
	if m.hubCursor == len(vis)+1 {
		cancel = btnSel.Render("Cancel")
	}
	install := btnOff.Render("Install")
	if m.canInstall() {
		install = btnIdle.Render("Install")
		if m.hubCursor == len(vis) {
			install = btnSel.Render("Install")
		}
	}
	body = append(body, lineBG.Width(w).Render("  "+cancel+"   "+install))
	if !m.canInstall() {
		body = append(body, warnText.Width(w).Render("  Need: a disk, a /, an ESP, an admin user (+ passphrase if encrypting)."))
	}

	return m.screen(body, "Use [Tab] or the arrow keys [Up and Down] to navigate")
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
	for i, c := range m.choices {
		label := c
		if m.editing == secKernel {
			label = kernelLabel(c)
		}
		body = append(body, row(w, i == m.edCursor, label))
	}
	return m.screen(body, "[Up/Down] move · [Enter] select · [Esc] back")
}

func (m model) netFormView() string {
	w := m.panelWidth()
	var title, hint string
	switch m.formStep {
	case 0:
		title = "Interface name (blank = DHCP on all interfaces)"
		hint = "e.g. eth0, enp0s3"
	case 1:
		title = "Address in CIDR form"
		hint = "e.g. 192.168.1.50/24"
	case 2:
		title = "Gateway (blank for none)"
		hint = "e.g. 192.168.1.1"
	case 3:
		title = "DNS server (blank for none)"
		hint = "e.g. 192.168.1.1"
	}
	body := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[secNetwork]),
		rule(w),
		itemLabel.Width(w).Render("  " + title),
		footer.Width(w).Render("  " + hint),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	return m.screen(body, "type · [Enter] next · [Esc] cancel")
}

func (m model) textView() string {
	w := m.panelWidth()
	title := sectionLabels[m.editing]
	if m.editing == secUser {
		if m.formStep == 0 {
			title = "Admin username"
		} else {
			title = "Password for " + m.login
		}
	}
	body := []string{
		itemLabel.Width(w).Render("  " + title),
		lineBG.Width(w).Render(""),
		lineBG.Width(w).Render("  " + m.input.View()),
	}
	return m.screen(body, "type · [Enter] confirm · [Esc] back")
}

func (m model) partListView() string {
	w := m.panelWidth()
	disk := "— none —"
	if m.diskSet {
		disk = m.disks[m.diskIdx].Path + "  " + m.disks[m.diskIdx].Model
	}
	body := []string{
		itemLabel.Width(w).Render("  " + sectionTitles[secMedia]),
		rule(w),
		itemLabel.Width(w).Render("  Target disk"),
		itemValue.Width(w).Render("    » " + disk + "   [c] change"),
		rule(w),
		itemLabel.Width(w).Render("  Partitions"),
	}
	if len(m.parts) == 0 {
		body = append(body, footer.Width(w).Render("  none yet — press [a] to add"))
	}
	for i, p := range m.parts {
		body = append(body, row(w, i == m.partCursor, partSummary(p)))
		for _, lv := range p.LVs {
			body = append(body, lineBG.Width(w).Render("      lv "+lvSummary(lv)))
		}
	}
	return m.screen(body, "[c] change disk · [a] add · [l] add LV · [d] delete · [Esc] back")
}

func partSummary(p partEntry) string {
	switch p.Type {
	case "lvm":
		enc := ""
		if p.Encrypted {
			enc = " (encrypted)"
		}
		return fmt.Sprintf("lvm  group=%s%s  %s", p.VG, enc, orFill(p.Size))
	case "crypt":
		return fmt.Sprintf("crypt %s  %s  %s", p.FS, orNone(p.Mount), orFill(p.Size))
	default:
		return fmt.Sprintf("part  %s  %s  %s", p.FS, orNone(p.Mount), orFill(p.Size))
	}
}

func lvSummary(lv lvEntry) string {
	return fmt.Sprintf("%-6s %s  %s  %s", lv.Name, lv.FS, orNone(lv.Mount), orFill(lv.Size))
}

func orNone(s string) string {
	if s == "" {
		return "(no mount)"
	}
	return s
}
func orFill(s string) string {
	if s == "" || strings.EqualFold(s, "rest") {
		return "fill"
	}
	return s
}

func (m model) partFormView() string {
	w := m.panelWidth()
	var title string
	var isChoice bool
	if m.formIsLV {
		switch m.formStep {
		case 0:
			title = "LV name"
		case 1:
			title, isChoice = "LV filesystem", true
		case 2:
			title = "LV mountpoint (blank for none)"
		case 3:
			title = "LV size (blank = fill the group)"
		}
	} else {
		switch m.formStep {
		case 0:
			title, isChoice = "Partition type", true
		case 1:
			if m.form.Type == "lvm" {
				title = "Volume group name"
			} else {
				title, isChoice = "Filesystem", true
			}
		case 2:
			title = "Mountpoint (e.g. / or /boot)"
		case 3:
			title = "Size (e.g. 512M, 30G; blank = fill)"
		}
	}

	body := []string{
		itemLabel.Width(w).Render("  " + title),
		lineBG.Width(w).Render(""),
	}
	foot := "type · [Enter] next · [Esc] cancel"
	if isChoice {
		for i, c := range m.formChoices() {
			body = append(body, row(w, i == m.edCursor, c))
		}
		foot = "[Up/Down] move · [Enter] next · [Esc] cancel"
	} else {
		body = append(body, lineBG.Width(w).Render("  "+m.input.View()))
	}
	return m.screen(body, foot)
}
