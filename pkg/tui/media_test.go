package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
)

var (
	escKey   = tea.KeyMsg{Type: tea.KeyEsc}
	spaceKey = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	leftKey  = tea.KeyMsg{Type: tea.KeyLeft}
)

func twoTestDisks() []blockdev.Device {
	return []blockdev.Device{
		{Name: "sda", Path: "/dev/sda", Size: 64 << 30, Model: "First"},
		{Name: "sdb", Path: "/dev/sdb", Size: 128 << 30, Model: "Second"},
	}
}

// mediaDialog opens Installation Media on a machine with two disks.
func mediaDialog() model {
	return openSectionInTab(withSize(newModel(twoTestDisks())), 0, secMedia)
}

// fakeParted puts a parted that prints out first on PATH; with failAt > 0
// its failAt-th call fails instead.
func fakeParted(t *testing.T, out string, failAt int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nn=$(cat \"" + dir + "/n\" 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > \"" + dir + "/n\"\n" +
		"[ $n -eq " + string(rune('0'+failAt)) + " ] && exit 1\ncat <<'EOF'\n" + out + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "parted"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestMediaDialogNavigation(t *testing.T) {
	m := mediaDialog()
	if m.view != viewMedia {
		t.Fatalf("Installation Media did not open: view %d", m.view)
	}
	last := m.mediaLast()
	// The cursor wraps both ways over modes, disks, the checkbox and buttons.
	if got := send(m, up); got.mediaCursor != last {
		t.Errorf("Up from the top = %d, want %d", got.mediaCursor, last)
	}
	m.mediaCursor = last
	if got := send(m, down); got.mediaCursor != 0 {
		t.Errorf("Down from the bottom = %d", got.mediaCursor)
	}
	if got := send(send(m, leftKey), tabKey); got.mediaCursor != last {
		t.Errorf("Left then Tab = %d, want %d", got.mediaCursor, last)
	}

	// Space and Enter both choose the mode, the disk, and the checkbox.
	for _, k := range []tea.KeyMsg{spaceKey, enter} {
		m = mediaDialog()
		m.mediaCursor = 2
		if got := send(m, k); got.mediaMode != 2 {
			t.Errorf("%q on a mode did not choose it", k.String())
		}
		m.mediaCursor = len(mediaModes) + 1 // the second disk
		got := send(m, k)
		if got.diskIdx != 1 || !got.diskSet {
			t.Errorf("%q on a disk did not choose it: %d %v", k.String(), got.diskIdx, got.diskSet)
		}
		if view := stripANSIForTest(got.View()); !strings.Contains(view, "» /dev/sdb") || !strings.Contains(view, "128GiB") {
			t.Errorf("the chosen disk is not marked:\n%s", view)
		}
		m.mediaCursor = m.mediaEncIndex()
		got = send(m, k)
		if !got.encrypt || !strings.Contains(stripANSIForTest(got.View()), "[x] Enable Encryption") {
			t.Errorf("%q did not tick the encryption box", k.String())
		}
		if send(got, k).encrypt {
			t.Errorf("%q did not untick the encryption box", k.String())
		}
	}
	// Space on a button does nothing; Esc and Cancel go back to the menu.
	m = mediaDialog()
	m.mediaCursor = m.mediaEncIndex() + 1
	if got := send(m, spaceKey); got.view != viewMedia || got.diskSet {
		t.Error("Space activated a button")
	}
	if send(m, escKey).view != viewHub {
		t.Error("Esc did not leave the dialog")
	}
	m.mediaCursor = last // Cancel
	if send(m, enter).view != viewHub {
		t.Error("Cancel did not leave the dialog")
	}
}

func TestMediaConfirmModes(t *testing.T) {
	confirm := func(m model) model {
		m.mediaCursor = m.mediaEncIndex() + 1
		return send(m, enter)
	}
	// Destructive: the standard layout, back at the menu.
	m := confirm(mediaDialog())
	if m.view != viewHub || !m.diskSet || len(m.parts) != 3 || !hasRoot(m.parts) || !hasESP(m.parts) || hasCrypt(m.parts) {
		t.Errorf("Destructive: view %d parts %+v", m.view, m.parts)
	}
	// With encryption the root is a LUKS volume and the passphrase is asked
	// for at once — unless one was already given.
	m = mediaDialog()
	m.encrypt = true
	m = confirm(m)
	if !hasCrypt(m.parts) || m.popup != popupPassphrase {
		t.Errorf("Destructive, encrypted: crypt %v popup %d", hasCrypt(m.parts), m.popup)
	}
	m = mediaDialog()
	m.encrypt, m.passphrase = true, "a long passphrase"
	if m = confirm(m); m.popup != popupNone {
		t.Error("the passphrase was asked for again")
	}
	// Advanced: the partition editor.
	m = mediaDialog()
	m.mediaMode = 1
	if m = confirm(m); m.view != viewPartList || m.safeInstall {
		t.Errorf("Advanced: view %d", m.view)
	}
}

const partedWithRoom = `BYT;
/dev/sdb:64000MiB:scsi:512:512:gpt:Test:;
1:1.00MiB:513MiB:512MiB:fat32::boot, esp;
2:513MiB:20000MiB:19487MiB:ext4::;
1:20000MiB:64000MiB:44000MiB:free;`

// A Safe install adds Sunlight beside what is on the disk: it must keep the
// existing ESP and every partition, and create only a root in free space.
func TestMediaSafeInstall(t *testing.T) {
	safe := func() model {
		m := mediaDialog()
		m.mediaMode, m.diskIdx = 2, 1
		m.mediaCursor = m.mediaEncIndex() + 1
		return send(m, enter)
	}
	popupSays := func(m model, want string) {
		t.Helper()
		if m.popup != popupMessage || m.safeInstall || len(m.parts) != 0 {
			t.Errorf("want a refusal saying %q; popup %d, parts %+v", want, m.popup, m.parts)
		}
		if msg := strings.Join(m.popupMsg, "\n"); !strings.Contains(msg, want) {
			t.Errorf("popup %q does not say %q", msg, want)
		}
		if view := stripANSIForTest(m.View()); !strings.Contains(view, "Safe Installation") || !strings.Contains(view, "press any key") {
			t.Errorf("the popup is not drawn:\n%s", view)
		}
	}

	fakeParted(t, partedWithRoom, 0)
	m := safe()
	if m.view != viewHub || !m.safeInstall || m.popup != popupNone || len(m.parts) != 2 {
		t.Fatalf("Safe: view %d safe %v parts %+v", m.view, m.safeInstall, m.parts)
	}
	esp, root := m.parts[0], m.parts[1]
	if esp.Name != "sdb1" || !esp.Preserve || esp.Create || esp.Mount != "/boot/efi" {
		t.Errorf("the existing ESP must be kept, not formatted: %+v", esp)
	}
	// The new root is the third partition, inside the free region.
	if root.Name != "sdb3" || !root.Create || root.Preserve || root.Mount != "/" || root.Start != "20000M" || root.Size != "43999M" {
		t.Errorf("new root = %+v", root)
	}
	// What is planned from it names real devices: the new root is made on
	// /dev/sdb3, and the ESP is neither created nor formatted.
	cfg := planOf(t, m.parts)
	cfg.TargetMedia[0].Name = "sdb"
	cfg.ReusePartitions = true
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("planning the Safe layout: %v", err)
	}
	if plan.Wipe {
		t.Error("a Safe install plans to wipe the disk")
	}
	devices := map[string]bool{}
	for _, p := range plan.Parts {
		devices[p.Device] = true
		if p.Device == "/dev/sdb1" && (p.Create || !p.Preserve) {
			t.Errorf("the shared ESP would be created or formatted: %+v", p)
		}
	}
	if !devices["/dev/sdb1"] || !devices["/dev/sdb3"] || len(devices) != 2 {
		t.Errorf("planned devices = %v, want /dev/sdb1 and /dev/sdb3", devices)
	}

	// Confirming another mode afterwards is no longer a Safe install.
	m.view = viewMedia
	m.mediaMode = 0
	m.mediaCursor = m.mediaEncIndex() + 1
	if m = send(m, enter); m.safeInstall {
		t.Error("a Destructive confirm kept the Safe flag")
	}

	// Each reason it cannot be done is a message, never a guessed layout.
	fakeParted(t, "BYT;\n2:1MiB:20000MiB:19999MiB:ext4::;\n1:20000MiB:64000MiB:44000MiB:free;", 0)
	popupSays(safe(), "no EFI System Partition to share")
	fakeParted(t, "BYT;\n1:1.00MiB:513MiB:512MiB:fat32::boot, esp;\n1:513MiB:613MiB:100MiB:free;", 0)
	popupSays(safe(), "No unallocated region of at least")
	fakeParted(t, partedWithRoom, 1)
	popupSays(safe(), "Could not read the disk")
	fakeParted(t, partedWithRoom, 2)
	popupSays(safe(), "Could not read free space")
	fakeParted(t, partedWithRoom, 3)
	m = safe()
	popupSays(m, "Could not read the partition table")
	// Any key closes the message.
	if send(m, runes("x")).popup != popupNone {
		t.Error("a key did not close the message")
	}
}

func TestMediaRescan(t *testing.T) {
	rescan := func(m model) model {
		m.mediaCursor = m.mediaEncIndex() + 1 + 2
		return send(m, enter)
	}
	// A disk was unplugged: the list is replaced, and a selection that no
	// longer exists falls back to the first disk.
	fakeLsblk(t, `{"name":"sdc","path":"/dev/sdc","size":1000,"type":"disk","model":"New"}`)
	m := mediaDialog()
	m.diskIdx = 1
	m = rescan(m)
	if len(m.disks) != 1 || m.disks[0].Path != "/dev/sdc" || m.diskIdx != 0 || m.mediaCursor != 0 {
		t.Errorf("after the rescan: %+v idx %d cursor %d", m.disks, m.diskIdx, m.mediaCursor)
	}
	// A rescan that finds nothing, or fails, keeps the list it had.
	fakeLsblk(t, "")
	if m = rescan(mediaDialog()); len(m.disks) != 2 {
		t.Errorf("an empty rescan dropped the disks: %+v", m.disks)
	}
	fakeLsblk(t, "FAIL")
	if m = rescan(mediaDialog()); len(m.disks) != 2 {
		t.Errorf("a failed rescan dropped the disks: %+v", m.disks)
	}
}

func TestPartitionEditor(t *testing.T) {
	m := openMediaEditor(withSize(newModel(twoTestDisks())))
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "none yet - press [a] to add") || !strings.Contains(view, "/dev/sda  First") {
		t.Errorf("empty editor:\n%s", view)
	}
	// [l] adds a volume only to an LVM partition: with none, nothing opens.
	if got := send(m, runes("l")); got.view != viewPartList {
		t.Error("[l] opened a form with no LVM partition selected")
	}
	// [d] with nothing to delete does nothing.
	if got := send(m, runes("d")); len(got.parts) != 0 {
		t.Error("[d] on an empty list changed it")
	}

	// An ESP: part, vfat, /boot, 512M.
	m = send(m, runes("a"))
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "Partition type") || !strings.Contains(view, "lvm-encrypted") {
		t.Errorf("type step:\n%s", view)
	}
	m = send(m, enter) // part
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "Filesystem") || !strings.Contains(view, "swap") {
		t.Errorf("filesystem step:\n%s", view)
	}
	for i := 0; i < len(rootFilesystems); i++ {
		m = send(m, down)
	}
	m = send(send(m, down), up) // past vfat and back
	m = send(m, enter)
	if !strings.Contains(stripANSIForTest(m.View()), "Mountpoint (e.g. / or /boot)") {
		t.Error("mountpoint step not shown")
	}
	m = send(send(m, runes("/boot")), enter)
	if !strings.Contains(stripANSIForTest(m.View()), "Size (e.g. 512M, 30G; blank = fill)") {
		t.Error("size step not shown")
	}
	m = send(send(m, runes("512M")), enter)
	if m.view != viewPartList || len(m.parts) != 1 || partSummary(m.parts[0]) != "part  vfat  /boot  512M" || m.parts[0].Type != "part" {
		t.Fatalf("after adding the ESP: %+v", m.parts)
	}

	// Swap has no mountpoint: that step is skipped.
	m = send(send(m, runes("a")), enter)
	for i := 0; i < len(rootFilesystems)+1; i++ {
		m = send(m, down)
	}
	m = send(m, down) // already on the last choice
	m = send(m, enter)
	if !strings.Contains(stripANSIForTest(m.View()), "Size (e.g.") {
		t.Error("swap should go straight to its size")
	}
	m = send(send(m, runes("2G")), enter)

	// An encrypted volume group: its name defaults to vg0, and adding it
	// asks for the passphrase at once.
	m = send(m, runes("a"))
	m = send(send(send(send(m, down), down), down), enter) // lvm-encrypted
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "Volume group name") {
		t.Errorf("volume group step:\n%s", view)
	}
	m = send(m, enter) // blank name
	m = send(m, enter) // blank size: fill
	if len(m.parts) != 3 || m.parts[2].VG != "vg0" || !m.parts[2].Encrypted || m.parts[2].Type != "lvm" || m.popup != popupPassphrase {
		t.Fatalf("after adding the group: %+v popup %d", m.parts, m.popup)
	}
	m = send(m, escKey) // leave the passphrase for later
	if m.popup != popupNone || m.partCursor != 2 {
		t.Fatalf("popup %d cursor %d", m.popup, m.partCursor)
	}

	// A logical volume on it: the name is required.
	m = send(m, runes("l"))
	if !strings.Contains(stripANSIForTest(m.View()), "LV name") {
		t.Fatal("the volume form did not open")
	}
	if got := send(m, enter); got.formStep != 0 {
		t.Error("a volume with no name was accepted")
	}
	m = send(send(m, runes("root")), enter)
	if view := stripANSIForTest(m.View()); !strings.Contains(view, "LV filesystem") || !strings.Contains(view, "btrfs") {
		t.Errorf("volume filesystem step:\n%s", view)
	}
	m = send(m, enter) // ext4
	if !strings.Contains(stripANSIForTest(m.View()), "LV mountpoint (blank for none)") {
		t.Error("volume mountpoint step not shown")
	}
	m = send(send(m, runes("/")), enter)
	if !strings.Contains(stripANSIForTest(m.View()), "LV size (blank = fill the group)") {
		t.Error("volume size step not shown")
	}
	m = send(m, enter)
	if m.view != viewPartList || len(m.parts[2].LVs) != 1 || m.parts[2].LVs[0] != (lvEntry{Name: "root", FS: "ext4", Mount: "/"}) {
		t.Fatalf("after adding the volume: %+v", m.parts[2])
	}
	// The root is found inside the group, and the list shows it all.
	if !hasRoot(m.parts) || !hasESP(m.parts) || !hasCrypt(m.parts) {
		t.Errorf("layout checks: root %v esp %v crypt %v", hasRoot(m.parts), hasESP(m.parts), hasCrypt(m.parts))
	}
	view := stripANSIForTest(m.View())
	for _, want := range []string{"part  vfat  /boot  512M", "part  swap  (no mount)  2G", "lvm  group=vg0 (encrypted)  fill", "lv root   ext4  /  fill"} {
		if !strings.Contains(view, want) {
			t.Errorf("partition list missing %q:\n%s", want, view)
		}
	}
	if got := partSummary(partEntry{Type: "crypt", FS: "ext4", Mount: "/", Size: "rest"}); got != "crypt ext4  /  fill" {
		t.Errorf("crypt summary = %q", got)
	}

	// Moving stops at both ends; Esc in a form goes back without adding.
	m = send(send(m, down), down)
	if m.partCursor != 2 {
		t.Errorf("Down past the end: %d", m.partCursor)
	}
	m = send(send(send(send(m, up), up), up), up)
	if m.partCursor != 0 {
		t.Errorf("Up past the start: %d", m.partCursor)
	}
	if got := send(send(m, runes("a")), escKey); got.view != viewPartList || len(got.parts) != 3 {
		t.Error("Esc in the form did not cancel it")
	}
	// Deleting the last entry moves the cursor back onto the list.
	m.partCursor = 2
	m = send(m, runes("d"))
	if len(m.parts) != 2 || m.partCursor != 1 || hasRoot(m.parts) {
		t.Errorf("after deleting the group: %+v cursor %d", m.parts, m.partCursor)
	}
	// With neither a root nor an ESP the layout is not installable.
	if hasRoot(nil) || hasESP([]partEntry{{FS: "vfat", Mount: "/data"}}) {
		t.Error("a layout with no root or ESP passed the checks")
	}

	// [c] changes the target disk; Esc leaves the editor.
	pick := send(m, runes("c"))
	if pick.view != viewSectionChoice || pick.choices != nil {
		t.Fatalf("[c] did not open the disk list: view %d", pick.view)
	}
	if !strings.Contains(stripANSIForTest(pick.View()), "/dev/sdb") {
		t.Errorf("disk list:\n%s", stripANSIForTest(pick.View()))
	}
	pick = send(send(pick, down), enter)
	if pick.diskIdx != 1 || !pick.diskSet {
		t.Errorf("disk chosen = %d", pick.diskIdx)
	}
	if send(m, escKey).view != viewHub {
		t.Error("Esc did not leave the editor")
	}
}

const partedFull = `BYT;
/dev/sdb:122070MiB:scsi:512:512:gpt:Test:;
1:1.00MiB:513MiB:512MiB:fat32::boot, esp;
2:513MiB:122069MiB:121556MiB:ext4::;`

// With no unallocated space, Safe Installation offers to shrink a partition
// that can give some up. Nothing is done to the disk in the dialog: the
// layout it records is shrunk by the install, after its own confirmation.
func TestSafeInstallOffersToShrink(t *testing.T) {
	safe := func() model {
		m := mediaDialog()
		m.mediaMode, m.diskIdx = 2, 1
		m.mediaCursor = m.mediaEncIndex() + 1
		return send(m, enter)
	}
	fakeParted(t, partedFull, 0)
	fakeLsblk(t, `{"name":"sdb","path":"/dev/sdb","size":128000000000,"type":"disk","children":[
		{"name":"sdb1","path":"/dev/sdb1","size":536870912,"type":"part","fstype":"vfat"},
		{"name":"sdb2","path":"/dev/sdb2","size":127460704256,"type":"part","fstype":"ext4"}]}`)
	m := safe()
	if m.popup != popupShrink || m.shrinkPart.Path != "/dev/sdb2" {
		t.Fatalf("no shrink offered: popup %d, candidate %+v", m.popup, m.shrinkPart)
	}
	view := viewOf(m)
	for _, want := range []string{"/dev/sdb2 (ext4, 119GiB) can be shrunk", "Have a backup", "New size for sdb2"} {
		if !strings.Contains(view, want) {
			t.Errorf("shrink dialog missing %q:\n%s", want, view)
		}
	}
	// Half is offered.
	if m.input.Value() != "59G" {
		t.Errorf("offered size = %q", m.input.Value())
	}
	// A size that is not one, that does not shrink, or that frees too little.
	for size, want := range map[string]string{"lots": "not a size", "200G": "would not shrink it", "118G": "frees too little",
		"60000": "not a size"} { // a bare number: bytes, refused at Install before

		got := send(typeText(m, size), enter)
		if got.popup != popupShrink || !strings.Contains(got.popupErr, want) || len(got.parts) != 0 {
			t.Errorf("size %q: popup %d, error %q", size, got.popup, got.popupErr)
		}
	}
	if got := send(m, escKey); got.popup != popupNone || got.safeInstall || len(got.parts) != 0 {
		t.Error("Esc did not cancel the shrink")
	}

	// A decimal size, as the config takes it, in whole MiB.
	if half := send(typeText(m, "59.5G"), enter); half.popup != popupNone || len(half.parts) != 3 || half.parts[1].Resize != "60928M" {
		t.Errorf("59.5G: popup %d, error %q, parts %+v", half.popup, half.popupErr, half.parts)
	}
	done := send(typeText(m, "60G"), enter)
	if done.popup != popupNone || done.view != viewHub || !done.safeInstall || len(done.parts) != 3 {
		t.Fatalf("after choosing a size: popup %d view %d parts %+v", done.popup, done.view, done.parts)
	}
	// The ESP is shared, the old partition kept and shrunk, the new root
	// created in the room that leaves.
	cfg := planOf(t, done.parts)
	cfg.TargetMedia[0].Name, cfg.ReusePartitions = "sdb", true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the layout does not validate: %v", err)
	}
	plan, err := cfg.Plan()
	if err != nil || plan.Wipe {
		t.Fatalf("plan: %v", err)
	}
	esp, old, root := plan.Parts[0], plan.Parts[1], plan.Parts[2]
	if esp.Device != "/dev/sdb1" || !esp.Preserve || esp.Mountpoint != "/boot/efi" {
		t.Errorf("ESP = %+v", esp)
	}
	if old.Device != "/dev/sdb2" || !old.Preserve || old.ResizeMiB != 61440 || old.Filesystem != "ext4" {
		t.Errorf("shrunk partition = %+v", old)
	}
	if root.Device != "/dev/sdb3" || !root.Create || !root.CreateInFree || root.Mountpoint != "/" {
		t.Errorf("new root = %+v", root)
	}

	// Nothing that can be shrunk (xfs here): the plain refusal, as before.
	fakeLsblk(t, `{"name":"sdb","path":"/dev/sdb","size":128000000000,"type":"disk","children":[
		{"name":"sdb2","path":"/dev/sdb2","size":127460704256,"type":"part","fstype":"xfs"}]}`)
	if m = safe(); m.popup != popupMessage || !strings.Contains(strings.Join(m.popupMsg, " "), "No partition on the disk can be shrunk") {
		t.Errorf("no candidate: popup %d %v", m.popup, m.popupMsg)
	}
	// A partition in use, or too small to give up a root's worth, is not offered.
	fakeLsblk(t, `{"name":"sdb","path":"/dev/sdb","size":128000000000,"type":"disk","children":[
		{"name":"sdb2","path":"/dev/sdb2","size":127460704256,"type":"part","fstype":"ext4","mountpoint":"/mnt"},
		{"name":"sdb3","path":"/dev/sdb3","size":1073741824,"type":"part","fstype":"ext4"}]}`)
	if m = safe(); m.popup != popupMessage {
		t.Errorf("a mounted or tiny partition was offered: popup %d", m.popup)
	}
	fakeLsblk(t, "FAIL")
	if m = safe(); m.popup != popupMessage {
		t.Errorf("with no listing: popup %d", m.popup)
	}
}

// The partition editor lays out a new table: a Safe layout's entries (the
// kept ESP, the root cut in free space) do not follow it there, where they
// were numbered by position and an added swap formatted an existing
// partition. Encryption a Safe install cannot do is refused, and a box
// ticked but not confirmed does not stay ticked over a plain layout.
func TestSafeLayoutStaysOutOfTheEditor(t *testing.T) {
	m := mediaDialog()
	m.parts = []partEntry{
		{Name: "sdb1", Type: "part", FS: "vfat", Mount: "/boot/efi", Preserve: true},
		{Name: "sdb5", Type: "part", FS: "ext4", Mount: "/", Create: true, Start: "60000M", Size: "20000M"},
	}
	m.safeInstall = true
	m.mediaCursor = m.mediaEncIndex() + 2 // Partition
	got := send(m, enter)
	if got.view != viewPartList || len(got.parts) != 0 || got.safeInstall {
		t.Errorf("editor after a Safe layout: view %d, parts %+v, safe %v", got.view, got.parts, got.safeInstall)
	}
	// Advanced confirm, likewise.
	m.mediaMode, m.mediaCursor = 1, m.mediaEncIndex()+1
	if got = send(m, enter); len(got.parts) != 0 || got.safeInstall {
		t.Errorf("Advanced after a Safe layout: parts %+v", got.parts)
	}

	// Safe with encryption ticked: refused, saying why.
	m = mediaDialog()
	m.encrypt, m.mediaMode, m.mediaCursor = true, 2, m.mediaEncIndex()+1
	if got = send(m, enter); got.popup != popupMessage || !strings.Contains(viewOf(got), "cannot encrypt") || len(got.parts) != 0 {
		t.Errorf("Safe + encryption: popup %d\n%s", got.popup, viewOf(got))
	}

	// Ticked, then left with Esc: back to what the layout is.
	m = mediaDialog()
	m.parts = autoLayout(false)
	m.mediaCursor = m.mediaEncIndex()
	m = send(m, enter)
	if !m.encrypt {
		t.Fatal("the box did not tick")
	}
	if got = send(m, tea.KeyMsg{Type: tea.KeyEsc}); got.encrypt {
		t.Error("left ticked over an unencrypted layout")
	}
}

// A rescan keeps the chosen disk by path, not by index; a chosen disk that
// is gone is chosen no longer.
func TestRescanKeepsTheDiskByPath(t *testing.T) {
	m := readyModel()
	m.diskIdx = 1 // sdb
	usb := blockdev.Device{Name: "sdc", Path: "/dev/sdc", Size: 16 << 30}
	m.rescanned(append([]blockdev.Device{usb}, twoTestDisks()...))
	if got := m.disks[m.diskIdx].Path; got != "/dev/sdb" || !m.diskSet {
		t.Errorf("after a rescan the target is %s", got)
	}
	m.rescanned([]blockdev.Device{usb, twoTestDisks()[0]})
	if m.diskSet || m.parts != nil || m.popup != popupMessage {
		t.Errorf("a vanished disk is still chosen: set %v parts %d", m.diskSet, len(m.parts))
	}
}

// Esc and Cancel on the media dialog leave the disk it was opened with.
func TestMediaCancelKeepsTheDisk(t *testing.T) {
	for _, leave := range []string{"esc", "cancel"} {
		m := openSectionInTab(readyModel(), 0, secMedia)
		m.mediaCursor = len(mediaModes) + 1 // the sdb row
		m = send(m, enter)
		if m.diskIdx != 1 {
			t.Fatalf("sdb not picked: %d", m.diskIdx)
		}
		if leave == "esc" {
			m = send(m, escKey)
		} else {
			m.mediaCursor = m.mediaEncIndex() + 1 + 3
			m = send(m, enter)
		}
		if m.diskIdx != 0 || m.view != viewHub {
			t.Errorf("%s kept the disk picked: %d", leave, m.diskIdx)
		}
	}
}

// Encryption ticked and a layout that encrypts nothing is not installed as if
// it were encrypted.
func TestEncryptTickedLayoutPlain(t *testing.T) {
	m := readyModel()
	m.encrypt = true
	if p := m.installProblem(); !strings.Contains(p, "encrypts nothing") {
		t.Errorf("problem %q", p)
	}
	m.encrypt = false
	if p := m.installProblem(); strings.Contains(p, "encrypts nothing") {
		t.Errorf("unticked: %q", p)
	}
}
