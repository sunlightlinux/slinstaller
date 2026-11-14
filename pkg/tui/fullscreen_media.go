package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
	"github.com/sunlightlinux/slinstaller/pkg/config"
	"github.com/sunlightlinux/slinstaller/pkg/storage"
)

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

	// Safe/alongside install only: Name overrides the partition device name
	// (so a reused ESP keeps its number), Preserve reuses it without
	// formatting, and Create cuts a new one at Start in unallocated space.
	Name     string
	Preserve bool
	Create   bool
	Start    string
}

// layoutToMedia turns the editor's partition list into config children,
// numbering the physical partitions (sda1, sda2, ...) and nesting an
// lvm partition's logical volumes.
func layoutToMedia(disk string, parts []partEntry) []*config.Media {
	var out []*config.Media
	for i, p := range parts {
		name := partDevName(disk, i+1)
		if p.Name != "" { // Safe install: explicit device name
			name = p.Name
		}
		m := &config.Media{
			Name: name, Type: p.Type, FsType: p.FS, Mountpoint: p.Mount, Size: p.Size,
			Preserve: p.Preserve, Create: p.Create, Start: p.Start,
		}
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

// mediaModes are the installation modes, in clr's order.
var mediaModes = []struct{ name, desc string }{
	{"Destructive Installation", "Erase the whole disk and install with a standard layout."},
	{"Advanced Installation", "Lay out or reuse partitions yourself (the partition editor)."},
	{"Safe Installation", "Add Sunlight in the disk's free space, keeping what is there."},
}

// mediaButtons are the action buttons on the Installation Media dialog.
var mediaButtons = []string{"Confirm", "Partition", "Rescan Media", "Cancel"}

// mediaEncIndex and mediaLast give the cursor positions on the media dialog:
// modes (len(mediaModes)), then the disks, the encryption checkbox, and the
// buttons.
func (m model) mediaEncIndex() int { return len(mediaModes) + len(m.disks) }

func (m model) mediaLast() int { return m.mediaEncIndex() + 1 + len(mediaButtons) - 1 }

// updateMedia drives the Installation Media front dialog: choose a mode and a
// disk, toggle encryption, and Confirm / open the partition editor / rescan.
func (m model) updateMedia(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	nModes, encIdx, last := len(mediaModes), m.mediaEncIndex(), m.mediaLast()
	btnBase := encIdx + 1
	switch key.String() {
	case "esc":
		m.view = viewHub
	case "up", "k", "shift+tab", "left", "h":
		if m.mediaCursor > 0 {
			m.mediaCursor--
		} else {
			m.mediaCursor = last
		}
	case "down", "j", "tab", "right", "l":
		if m.mediaCursor < last {
			m.mediaCursor++
		} else {
			m.mediaCursor = 0
		}
	case " ", "x":
		switch {
		case m.mediaCursor < nModes:
			m.mediaMode = m.mediaCursor
		case m.mediaCursor < encIdx: // a disk row, as Enter
			m.diskIdx = m.mediaCursor - nModes
			m.diskSet = true
		case m.mediaCursor == encIdx:
			m.encrypt = !m.encrypt
		}
	case "enter":
		switch {
		case m.mediaCursor < nModes:
			m.mediaMode = m.mediaCursor
		case m.mediaCursor < encIdx: // a disk row
			m.diskIdx = m.mediaCursor - nModes
			m.diskSet = true
		case m.mediaCursor == encIdx:
			m.encrypt = !m.encrypt
		default:
			switch m.mediaCursor - btnBase {
			case 0: // Confirm
				return m.confirmMedia()
			case 1: // Partition (manual editor)
				m.diskSet = true
				m.view = viewPartList
				m.partCursor = 0
			case 2: // Rescan Media
				if devs, err := blockdev.List(); err == nil {
					if disks := blockdev.Available(devs); len(disks) > 0 {
						m.disks = disks
						if m.diskIdx >= len(m.disks) {
							m.diskIdx = 0
						}
					}
				}
				m.mediaCursor = 0
			case 3: // Cancel
				m.view = viewHub
			}
		}
	}
	return m, nil
}

// confirmMedia applies the chosen installation mode.
func (m model) confirmMedia() (tea.Model, tea.Cmd) {
	m.diskSet = true      // the first disk is selected by default
	m.safeInstall = false // reset; only a Safe confirm sets it
	switch m.mediaMode {
	case 0: // Destructive: a standard whole-disk layout
		m.parts = autoLayout(m.encrypt)
		m.view = viewHub
		if m.encrypt && m.passphrase == "" {
			return m.openPassphrasePopup()
		}
		return m, nil
	case 1: // Advanced: the manual partition editor
		m.view = viewPartList
		m.partCursor = 0
		return m, nil
	default: // Safe: install into the disk's free space, keeping what is there
		return m.confirmSafe()
	}
}

// confirmSafe builds a Safe/alongside layout: reuse the disk's existing EFI
// System Partition and add a root partition in its largest unallocated
// region, leaving every existing partition intact. It inspects the live disk
// for the ESP, the free region and the next partition number; any problem
// becomes a popup rather than a layout.
func (m model) confirmSafe() (tea.Model, tea.Cmd) {
	disk := m.disks[m.diskIdx].Path // /dev/sda
	fail := func(lines ...string) (tea.Model, tea.Cmd) {
		m.popup = popupMessage
		m.popupMsg = append([]string{"Safe Installation", ""}, lines...)
		return m, nil
	}

	esp, ok, err := storage.ExistingESP(disk)
	if err != nil {
		return fail("Could not read the disk: " + err.Error())
	}
	if !ok {
		return fail(
			"This disk has no EFI System Partition to share.",
			"A Safe install adds Sunlight beside an existing system,",
			"so it needs one. Use Destructive for a fresh install.")
	}
	minMiB := storage.MinRootSize >> 20
	region, ok, err := storage.LargestFree(disk, minMiB)
	if err != nil {
		return fail("Could not read free space: " + err.Error())
	}
	if !ok {
		return fail(
			fmt.Sprintf("No unallocated region of at least %d MiB was found.", minMiB),
			"Free some space on the disk first (Sunlight does not yet",
			"resize existing partitions), or use Destructive.")
	}
	num, err := storage.NextPartitionNumber(disk)
	if err != nil {
		return fail("Could not read the partition table: " + err.Error())
	}

	base := m.disks[m.diskIdx].Name // sda
	root := partEntry{
		Name: partDevName("/dev/"+base, num), Type: "part", FS: "ext4", Mount: "/",
		Create: true,
		Start:  fmt.Sprintf("%dM", region.StartMiB),
		Size:   fmt.Sprintf("%dM", region.SizeMiB-1), // a MiB of slack at the end
	}
	espEntry := partEntry{
		Name: strings.TrimPrefix(esp, "/dev/"), Type: "part", FS: "vfat",
		Mount: "/boot/efi", Preserve: true,
	}
	m.parts = []partEntry{espEntry, root}
	m.safeInstall = true
	m.view = viewHub
	return m, nil
}

// autoLayout is the standard whole-disk layout for a Destructive install:
// a 512M EFI System Partition at /boot, a 2G swap, and the rest as root.
// With encryption the root is a LUKS volume (the ESP stays plain so the
// kernel is readable), giving a single passphrase prompt at boot.
func autoLayout(encrypt bool) []partEntry {
	root := partEntry{Type: "part", FS: "ext4", Mount: "/"}
	if encrypt {
		root.Type = "crypt"
	}
	return []partEntry{
		{Type: "part", FS: "vfat", Mount: "/boot", Size: "512M"},
		{Type: "part", FS: "swap", Size: "2G"},
		root,
	}
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
	m.passFirst, m.popupErr = "", ""
	m.input = passwordInput(m.passphrase)
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

// mediaView renders the Installation Media front dialog: the three modes,
// the disk list, an Enable Encryption checkbox and the action buttons — the
// layout clr-installer uses.
func (m model) mediaView() string {
	w := m.panelWidth()
	nModes, encIdx := len(mediaModes), m.mediaEncIndex()
	btnBase := encIdx + 1
	body := []string{itemLabel.Width(w).Render("  Select Installation Media"), rule(w)}

	for i, mode := range mediaModes {
		radio := "( )"
		if m.mediaMode == i {
			radio = "(•)"
		}
		sel := m.mediaCursor == i
		body = append(body,
			row(w, sel, radio+" "+mode.name),
			func() string {
				if sel {
					return itemSelValue.Width(w).Render("      " + mode.desc)
				}
				return footer.Width(w).Render("      " + mode.desc)
			}())
	}
	body = append(body, rule(w))

	for i, d := range m.disks {
		mark := "  "
		if m.diskSet && m.diskIdx == i {
			mark = "» "
		}
		body = append(body, row(w, m.mediaCursor == nModes+i,
			fmt.Sprintf("%s%-14s %-9s %s", mark, d.Path, gib(d.Size), d.Model)))
	}
	body = append(body, rule(w))

	encBox := "[ ]"
	if m.encrypt {
		encBox = "[x]"
	}
	body = append(body, row(w, m.mediaCursor == encIdx, encBox+" Enable Encryption"))
	body = append(body, lineBG.Width(w).Render(""))

	var btns []button
	for i, b := range mediaButtons {
		btns = append(btns, button{b, pick(m.mediaCursor == btnBase+i)})
	}
	return m.screenButtons(body, []string{buttonLine(w, btns...)},
		"[Tab/Arrows] move · [Space] choose · [Enter] select/activate · [Esc] back")
}

// gib formats a byte count as a rounded GiB figure for the disk list.
func gib(b uint64) string {
	return fmt.Sprintf("%dGiB", (b+(1<<29))>>30)
}

func (m model) partListView() string {
	w := m.panelWidth()
	disk := "(none)"
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
		body = append(body, footer.Width(w).Render("  none yet - press [a] to add"))
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
