package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/sunlightlinux/slinstaller/pkg/blockdev"
)

// The pages with a list as long as the machine makes it scroll the list in
// a window instead of growing past the terminal: bubbletea's renderer
// drops the top lines of a view taller than the screen, which took the
// title, the install modes and the first disks with them.

var scrollSizes = []struct{ w, h int }{{80, 24}, {100, 30}}

func manyDisks(n int) []blockdev.Device {
	var ds []blockdev.Device
	for i := 0; i < n; i++ {
		name := "sd" + string(rune('a'+i))
		model := fmt.Sprintf("Model%02d", i)
		if i == 1 { // a model too long for the row is clipped, not wrapped
			model += strings.Repeat(" with a very long name", 4)
		}
		ds = append(ds, blockdev.Device{Name: name, Path: "/dev/" + name, Size: uint64(i+1) << 30, Model: model})
	}
	return ds
}

func sized(m model, w, h int) model {
	m.width, m.height = w, h
	return m
}

// checkScreen fails when the view is taller than the terminal or misses
// any of want (the title, the buttons, the cursor's row).
func checkScreen(t *testing.T, m model, step string, want ...string) {
	t.Helper()
	raw := m.View()
	if h := lipgloss.Height(raw); h > m.height {
		t.Fatalf("%s: view is %d lines on a %dx%d terminal", step, h, m.width, m.height)
	}
	view := stripANSIForTest(raw)
	for _, s := range append([]string{"[Sunlight Linux Installer]"}, want...) {
		if !strings.Contains(view, s) {
			t.Fatalf("%s: %q is not on screen:\n%s", step, s, view)
		}
	}
}

func TestMediaDialogScrollsManyDisks(t *testing.T) {
	disks := manyDisks(20)
	nModes := len(mediaModes)
	for _, sz := range scrollSizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := openSectionInTab(sized(newModel(disks), sz.w, sz.h), 0, secMedia)
			if m.view != viewMedia {
				t.Fatalf("view %d", m.view)
			}
			fixed := []string{"Select Installation Media", mediaModes[0].name, mediaModes[2].name, "Enable Encryption", "Confirm", "Cancel"}
			checkScreen(t, m, "open", append(fixed, "/dev/sda", "↓ more")...)
			// Walk the cursor through every disk: each stays visible.
			for m.mediaCursor < nModes+len(disks)-1 {
				m = send(m, down)
				if c := m.mediaCursor - nModes; c >= 0 {
					checkScreen(t, m, fmt.Sprintf("cursor on disk %d", c), append(fixed, disks[c].Path)...)
				} else {
					checkScreen(t, m, "cursor on a mode", fixed...)
				}
			}
			checkScreen(t, m, "last disk", append(fixed, "/dev/sdt", "↑ more")...)
			if v := viewOf(m); strings.Contains(v, "↓ more") {
				t.Errorf("the last disk shown with more below:\n%s", v)
			}
			m = send(m, enter)
			if m.diskIdx != 19 || !m.diskSet {
				t.Fatalf("Enter on the last disk chose %d (set %v)", m.diskIdx, m.diskSet)
			}
			checkScreen(t, m, "chosen", append(fixed, "» /dev/sdt")...)
			// Down to the buttons: the chosen disk stays in the window.
			for m.mediaCursor != m.mediaLast()-len(mediaButtons)+1 {
				m = send(m, down)
				checkScreen(t, m, "below the disks", append(fixed, "» /dev/sdt")...)
			}
			m = send(m, enter) // Confirm
			if m.view == viewMedia {
				t.Fatalf("Confirm stayed on the dialog")
			}
			if got := m.sectionValue(secMedia); !strings.HasPrefix(got, "/dev/sdt ") {
				t.Errorf("hub names %q, want /dev/sdt", got)
			}
			// Back up from the top wraps to Cancel; the top still shows.
			m = openSectionInTab(sized(newModel(disks), sz.w, sz.h), 0, secMedia)
			checkScreen(t, send(m, up), "wrapped to Cancel", fixed...)
		})
	}
}

func TestDiskChoiceScrollsManyDisks(t *testing.T) {
	disks := manyDisks(20)
	for _, sz := range scrollSizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := sized(newModel(disks), sz.w, sz.h)
			m.view, m.diskSet = viewPartList, true
			m = send(m, runes("c"))
			if m.view != viewSectionChoice || m.choices != nil {
				t.Fatalf("[c] opened view %d", m.view)
			}
			checkScreen(t, m, "open", "Select Installation Media", "/dev/sda")
			for i := 1; i < len(disks); i++ {
				m = send(m, down)
				checkScreen(t, m, fmt.Sprintf("disk %d", i), "Select Installation Media", disks[i].Path)
			}
			if m = send(m, enter); m.diskIdx != 19 {
				t.Errorf("Enter on the last disk chose %d", m.diskIdx)
			}
		})
	}
}

func TestUserTableScrollsManyUsers(t *testing.T) {
	var users []userEntry
	for i := 0; i < 30; i++ {
		users = append(users, userEntry{Login: fmt.Sprintf("user%02d", i), Password: "correct horse", Admin: i == 0})
	}
	for _, sz := range scrollSizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := sized(newModel(twoTestDisks()), sz.w, sz.h)
			m.users = users
			m = openSectionInTab(m, 0, secUser)
			if m.view != viewUserList {
				t.Fatalf("view %d", m.view)
			}
			fixed := []string{"Manage User", "Login", "Add New User", "Cancel"}
			checkScreen(t, m, "open", append(fixed, "user00", "↓ more")...)
			for i := 1; i < len(users); i++ {
				m = send(m, down)
				checkScreen(t, m, fmt.Sprintf("user %d", i), append(fixed, users[i].Login)...)
			}
			// On the buttons the end of the table stays shown.
			checkScreen(t, send(m, down), "on Add New User", append(fixed, "user29")...)
			m = send(m, enter)
			if m.view != viewUserForm || m.editUserIdx != 29 || m.input.Value() != "user29" {
				t.Errorf("Enter on the last user edits %d (%q), view %d", m.editUserIdx, m.input.Value(), m.view)
			}
		})
	}
}

func TestPartitionListScrolls(t *testing.T) {
	many := func() []partEntry {
		var ps []partEntry
		for i := 0; i < 40; i++ {
			ps = append(ps, partEntry{Type: "part", FS: "ext4", Mount: fmt.Sprintf("/mnt/p%02d", i), Size: "1G"})
		}
		return ps
	}
	lvs := func() []partEntry {
		var lv []lvEntry
		for i := 0; i < 30; i++ {
			lv = append(lv, lvEntry{Name: fmt.Sprintf("lv%02d", i), FS: "ext4", Mount: fmt.Sprintf("/lv/%02d", i), Size: "1G"})
		}
		return []partEntry{
			{Type: "part", FS: "vfat", Mount: "/mnt/p00", Size: "512M"},
			{Type: "lvm", VG: "big", Size: "", LVs: lv},
			{Type: "part", FS: "ext4", Mount: "/mnt/p02", Size: "1G"},
			{Type: "lvm", VG: "small", Size: "", LVs: lv[:2]},
		}
	}
	for _, c := range []struct {
		name  string
		parts func() []partEntry
	}{{"40 partitions", many}, {"long LV list", lvs}} {
		for _, sz := range scrollSizes {
			t.Run(fmt.Sprintf("%s/%dx%d", c.name, sz.w, sz.h), func(t *testing.T) {
				m := sized(newModel(manyDisks(2)), sz.w, sz.h)
				m.view, m.diskSet, m.parts = viewPartList, true, c.parts()
				fixed := []string{sectionTitles[secMedia], "Target disk", "Partitions"}
				checkScreen(t, m, "open", append(fixed, "↓ more", partSummary(m.parts[0]))...)
				for i := 1; i < len(m.parts); i++ {
					m = send(m, down)
					checkScreen(t, m, fmt.Sprintf("partition %d", i), append(fixed, partSummary(m.parts[i]))...)
				}
				// [d] on the last row deletes that partition, not a row's index.
				last := m.parts[len(m.parts)-1]
				n := len(m.parts)
				m = send(m, runes("d"))
				if len(m.parts) != n-1 {
					t.Fatalf("[d] left %d partitions", len(m.parts))
				}
				for _, p := range m.parts {
					if p.Mount == last.Mount && p.VG == last.VG {
						t.Errorf("[d] on the last row deleted another partition; %+v remains", last)
					}
				}
				checkScreen(t, m, "after delete", fixed...)
			})
		}
	}
}

func TestNetworkListScrolls(t *testing.T) {
	for _, sz := range scrollSizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := sized(newModel(twoTestDisks()), sz.w, sz.h)
			for i := 0; i < 30; i++ {
				m.netIfaces = append(m.netIfaces, netIfaceEntry{Name: fmt.Sprintf("eth%02d", i)})
			}
			m.view = viewNetList
			title := sectionTitles[secNetwork]
			checkScreen(t, m, "open", title, "eth00", "↓ more")
			for i := 1; i < 30; i++ {
				m = send(m, down)
				checkScreen(t, m, fmt.Sprintf("iface %d", i), title, m.netIfaces[i].Name)
			}
			if m = send(m, enter); m.view != viewNetForm || m.netEdit != 29 || !strings.Contains(viewOf(m), "interface: eth29") {
				t.Errorf("Enter on the last interface edits %d, view %d", m.netEdit, m.view)
			}
		})
	}
}

// windowLines keeps the focus, marks hidden rows, and never exceeds the
// budget.
func TestWindowLines(t *testing.T) {
	lines := func(n int) []string {
		var l []string
		for i := 0; i < n; i++ {
			l = append(l, fmt.Sprintf("r%02d", i))
		}
		return l
	}
	for _, c := range []struct {
		n, lo, hi, budget int
		want              string
	}{
		{3, 0, 0, 5, "r00 r01 r02"},
		{5, 4, 4, 5, "r00 r01 r02 r03 r04"},
		{10, 0, 0, 5, "r00 r01 r02 r03 ↓"},
		{10, 3, 3, 5, "r00 r01 r02 r03 ↓"},
		{10, 4, 4, 5, "↑ r02 r03 r04 ↓"},
		{10, 6, 6, 5, "↑ r06 r07 r08 r09"},
		{10, 9, 9, 5, "↑ r06 r07 r08 r09"},
		{10, 4, 6, 5, "↑ r04 r05 r06 ↓"},
		{10, 4, 9, 5, "↑ r04 r05 r06 ↓"}, // focus taller than the window: lo wins
		{10, 1, 9, 5, "r00 r01 r02 r03 ↓"},
		{10, 5, 5, 2, "r05 ↓"},
		{10, 9, 9, 2, "↑ r09"},
		{10, 5, 5, 1, "r05"},
		{10, 5, 5, 0, "r05"},
		{10, -3, -3, 5, "r00 r01 r02 r03 ↓"},
		{10, 42, 42, 5, "↑ r06 r07 r08 r09"},
	} {
		got := windowLines(lines(c.n), c.lo, c.hi, c.budget, 40)
		var short []string
		for _, g := range got {
			s := strings.TrimSpace(stripANSIForTest(g))
			s = strings.TrimSuffix(strings.TrimSuffix(s, " more"), " more")
			short = append(short, s)
		}
		if strings.Join(short, " ") != c.want {
			t.Errorf("windowLines(n=%d, %d..%d, budget %d) = %q, want %q", c.n, c.lo, c.hi, c.budget, strings.Join(short, " "), c.want)
		}
		if c.budget > 0 && len(got) > max(c.budget, 1) {
			t.Errorf("windowLines(n=%d, budget %d) gave %d lines", c.n, c.budget, len(got))
		}
	}
}

// The lists that already scrolled counted one line too few (the title bar)
// and a wrapped error was counted as one line: both overfilled an 80x24
// dialog by a line, and the renderer dropped its title bar.
func TestScreensFitTheTerminal(t *testing.T) {
	for _, c := range []struct {
		name string
		open func(model) model
		want string
	}{
		{"timezone list", func(m model) model { return openSectionInTab(m, 0, secTimezone) }, "Cancel"},
		{"bundles", func(m model) model { return openSectionInTab(m, 1, secBundles) }, "Confirm"},
		{"hub long value", func(m model) model {
			m.proxy = "http://proxy.example.com:3128/" + strings.Repeat("a-very-long-path/", 6)
			m = send(m, runes("A"))
			for _, s := range m.visibleSections(1) {
				if s == secProxy {
					break
				}
				m = send(m, down)
			}
			return m
		}, "http://proxy.example.com"},
		{"user form error", func(m model) model {
			m = send(openSectionInTab(m, 0, secUser), enter)
			return send(typeText(m, "u0RRx1"), enter)
		}, "valid Unix name"},
	} {
		for _, sz := range scrollSizes {
			t.Run(fmt.Sprintf("%s/%dx%d", c.name, sz.w, sz.h), func(t *testing.T) {
				m := c.open(sized(newModel(twoTestDisks()), sz.w, sz.h))
				checkScreen(t, m, c.name, c.want)
			})
		}
	}
}
