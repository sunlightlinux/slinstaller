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
	// Resize shrinks this existing, kept partition to make room.
	Resize string
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
			Preserve: p.Preserve, Create: p.Create, Start: p.Start, Resize: p.Resize,
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
	{"Advanced Installation", "Lay out the disk's partitions yourself (the disk is erased)."},
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
		m.encrypt = layoutEncrypted(m.parts)
		m.restoreMediaDisk()
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
				m.toEditor()
			case 2: // Rescan Media
				if devs, err := blockdev.List(); err == nil {
					if disks := blockdev.Available(devs); len(disks) > 0 {
						m.rescanned(disks)
					}
				}
				m.mediaCursor = 0
			case 3: // Cancel
				m.encrypt = layoutEncrypted(m.parts)
				m.restoreMediaDisk()
				m.view = viewHub
			}
		}
	}
	return m, nil
}

// rescanned takes a new disk list, keeping the chosen disk by its path: by
// index, a USB stick plugged in ahead of it in lsblk's order moved the
// target — and the layout made for it — to another disk. A chosen disk that
// is gone is chosen no longer, and its layout dropped.
func (m *model) rescanned(disks []blockdev.Device) {
	was := ""
	if m.diskIdx < len(m.disks) {
		was = m.disks[m.diskIdx].Path
	}
	m.disks, m.diskIdx = disks, 0
	for i, d := range disks {
		if d.Path == was {
			m.diskIdx = i
			return
		}
	}
	if m.diskSet {
		m.diskSet, m.parts, m.safeInstall = false, nil, false
		m.popup = popupMessage
		m.popupMsg = []string{"Rescan Media", "", was + " is no longer there: choose the disk again."}
	}
}

// restoreMediaDisk puts back the disk chosen when the media dialog opened.
func (m *model) restoreMediaDisk() {
	for i, d := range m.disks {
		if d.Path == m.mediaWas {
			m.diskIdx, m.diskSet = i, m.mediaWasSet
			return
		}
	}
	m.diskSet = false // gone (a rescan dropped it): nothing is chosen
}

// confirmMedia applies the chosen installation mode.
func (m model) confirmMedia() (tea.Model, tea.Cmd) {
	m.diskSet = true // the first disk is selected by default
	// safeInstall is changed only by a mode that takes: cleared up front, a
	// Safe confirm that then refused (encryption ticked, no room) left the
	// old Safe layout no longer marked Safe — planned as a wipe, refused
	// at Install with a misleading message.
	switch m.mediaMode {
	case 0: // Destructive: a standard whole-disk layout
		m.safeInstall = false
		m.parts = autoLayout(m.encrypt)
		m.view = viewHub
		if m.encrypt && m.passphrase == "" {
			return m.openPassphrasePopup()
		}
		return m, nil
	case 1: // Advanced: the manual partition editor
		m.toEditor()
		return m, nil
	default: // Safe: install into the disk's free space, keeping what is there
		if m.encrypt {
			m.encrypt = layoutEncrypted(m.parts)
			m.popup = popupMessage
			m.popupMsg = []string{"Safe Installation", "",
				"A Safe install keeps the disk's partitions as they are, so it",
				"cannot encrypt the new system. Untick Enable Encryption to go",
				"on unencrypted, or use Destructive to encrypt."}
			return m, nil
		}
		return m.confirmSafe()
	}
}

// toEditor opens the partition editor. The editor lays out a new table on
// the whole disk; a Safe layout's entries — an existing ESP kept, a
// partition cut in free space — mean nothing there, and kept in the list
// they were numbered by position: adding a swap formatted the disk's
// existing third partition. Such a layout is cleared, for one laid out
// from scratch. Whatever is laid out here wipes the disk, as the
// confirmation then says.
func (m *model) toEditor() {
	if m.safeInstall || hasSafeEntries(m.parts) {
		m.parts = nil
	}
	m.safeInstall = false
	m.view = viewPartList
	m.partCursor = 0
}

// hasSafeEntries reports whether any entry is a Safe install's: named,
// kept, created in free space or shrunk.
func hasSafeEntries(parts []partEntry) bool {
	for _, p := range parts {
		if p.Name != "" || p.Preserve || p.Create || p.Start != "" || p.Resize != "" {
			return true
		}
	}
	return false
}

// layoutEncrypted reports whether the layout encrypts anything, which is
// what the Enable Encryption box shows once the dialog is left without
// Confirm: ticked and left, it showed [x] over an unencrypted layout.
func layoutEncrypted(parts []partEntry) bool {
	for _, p := range parts {
		if p.Type == "crypt" || p.Encrypted {
			return true
		}
	}
	return false
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
		// No room as the disk stands — but a partition on it may be able to
		// give some up.
		if cand, found := shrinkCandidate(disk); found {
			return m.openShrinkPopup(esp, cand)
		}
		return fail(
			fmt.Sprintf("No unallocated region of at least %d MiB was found.", minMiB),
			"No partition on the disk can be shrunk to make room either",
			"(ext4, ntfs or btrfs, not mounted). Use Destructive, or",
			"free some space first.")
	}
	num, err := storage.NextPartitionNumber(disk)
	if err != nil {
		return fail("Could not read the partition table: " + err.Error())
	}

	base := m.disks[m.diskIdx].Name // sda
	root := partEntry{
		Name: partDevName(base, num), Type: "part", FS: "ext4", Mount: "/",
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

// shrinkCandidate picks the partition on disk that a Safe install could
// shrink to make room: the largest one holding a filesystem that can be
// shrunk, not mounted, and big enough to give up a root's worth and still
// keep as much.
func shrinkCandidate(disk string) (blockdev.Device, bool) {
	devs, err := blockdev.List()
	if err != nil {
		return blockdev.Device{}, false
	}
	d := blockdev.FindByPath(devs, disk)
	if d == nil {
		return blockdev.Device{}, false
	}
	var best blockdev.Device
	for _, p := range d.Children {
		if p.Type == "part" && storage.Resizable(p.FsType) && p.Mountpoint == "" &&
			p.Size >= 2*storage.MinRootSize && p.Size > best.Size {
			best = p
		}
	}
	return best, best.Size > 0
}

// openShrinkPopup asks how far to shrink the candidate, offering half.
func (m model) openShrinkPopup(esp string, cand blockdev.Device) (tea.Model, tea.Cmd) {
	m.popup, m.popupErr = popupShrink, ""
	m.shrinkESP, m.shrinkPart = esp, cand
	ti := textinput.New()
	ti.Focus()
	ti.SetValue(fmt.Sprintf("%dG", (cand.Size>>30)/2))
	m.input = ti
	return m, textinput.Blink
}

func (m model) shrinkPopupLines() []string {
	p := m.shrinkPart
	lines := []string{
		popupTitle.Render("Safe Installation"),
		"",
		"This disk has no unallocated space, but",
		fmt.Sprintf("%s (%s, %s) can be shrunk to make room.", p.Path, p.FsType, gib(p.Size)),
		"What is on it is kept. Have a backup: a shrink",
		"that is interrupted can lose data.",
		"",
		"New size for " + p.Name + " (e.g. 60G):",
		popupInput.Render(m.input.View()),
		"",
	}
	if m.popupErr != "" {
		lines = append(lines, m.popupErr, "")
	}
	return append(lines, popupHint.Render("[Enter] shrink and install beside it · [Esc] cancel"))
}

// updateShrinkPopup takes the new size: it has to leave the partition
// smaller, and free at least a root's worth. Nothing is changed on the disk
// here — the layout is recorded, and the shrink happens in the install,
// after its confirmation.
func (m model) updateShrinkPopup(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.popup = popupNone
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		// Read as the install reads it (59.5G is a size), and with a unit:
		// a bare number was taken as bytes here and refused at Install.
		size, err := config.SizeBytes(v)
		p := m.shrinkPart
		switch {
		case err != nil || size < 1<<20 || strings.Trim(v, "0123456789.") == "":
			m.popupErr = "not a size (e.g. 60G)"
			return m, nil
		case size >= p.Size:
			m.popupErr = "that would not shrink it: it is " + gib(p.Size) + " now"
			return m, nil
		case p.Size-size < storage.MinRootSize:
			m.popupErr = fmt.Sprintf("that frees too little: Sunlight needs at least %d MiB", storage.MinRootSize>>20)
			return m, nil
		}
		disk := m.disks[m.diskIdx]
		num, err := storage.NextPartitionNumber(disk.Path)
		if err != nil {
			m.popupErr = "could not read the partition table: " + err.Error()
			return m, nil
		}
		m.parts = []partEntry{
			{Name: strings.TrimPrefix(m.shrinkESP, "/dev/"), Type: "part", FS: "vfat", Mount: "/boot/efi", Preserve: true},
			// In whole MiB, as the shrink is carried out.
			{Name: p.Name, Type: "part", FS: p.FsType, Preserve: true, Resize: fmt.Sprintf("%dM", size>>20)},
			// No start: it goes in the room the shrink leaves.
			{Name: partDevName(disk.Name, num), Type: "part", FS: "ext4", Mount: "/", Create: true},
		}
		m.safeInstall, m.diskSet = true, true
		m.popup, m.view = popupNone, viewHub
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
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
