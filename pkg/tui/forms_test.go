package tui

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/system"
)

func TestManageUsers(t *testing.T) {
	m := openSectionInTab(withSize(newModel(twoTestDisks())), 0, secUser)
	if m.view != viewUserList {
		t.Fatalf("Manage User opened view %d", m.view)
	}
	// With no accounts the cursor is on Add New User; Enter opens the form.
	m = send(m, enter)
	if m.view != viewUserForm || m.editUserIdx != -1 {
		t.Fatalf("Add New User: view %d", m.view)
	}
	m = send(typeText(m, "ion"), enter)
	if !strings.Contains(viewOf(m), "User Name") {
		t.Errorf("name step:\n%s", viewOf(m))
	}
	m = send(typeText(m, "Ion Pop"), enter)
	m = send(typeText(m, "correct horse"), enter)
	if !strings.Contains(viewOf(m), "Confirm Password") {
		t.Errorf("confirm step:\n%s", viewOf(m))
	}
	m = send(typeText(m, "correct horse"), enter)
	// A new account is an administrator unless changed.
	if view := viewOf(m); m.edCursor != 0 || !strings.Contains(view, "Yes") || !strings.Contains(view, "No") {
		t.Errorf("admin step: cursor %d\n%s", m.edCursor, view)
	}
	m = send(send(m, down), enter) // No
	if len(m.users) != 1 || m.users[0].Admin || m.users[0].UserName != "Ion Pop" || m.view != viewUserList {
		t.Fatalf("users = %+v", m.users)
	}
	if hasAdmin(m.users) {
		t.Error("an account that is not an administrator counted as one")
	}

	// Editing it starts from its values — the Admin choice on No — and
	// replaces it in place.
	m.userCursor = 0
	m = send(m, enter)
	if m.editUserIdx != 0 || m.input.Value() != "ion" || !strings.Contains(viewOf(m), "Edit User") {
		t.Fatalf("edit form: idx %d field %q", m.editUserIdx, m.input.Value())
	}
	m = send(m, enter)                            // login
	m = send(m, enter)                            // name
	m = send(typeText(m, "correct horse"), enter) // password
	m = send(typeText(m, "correct horse"), enter) // again
	if m.edCursor != 1 {
		t.Errorf("editing a non-admin should start on No: cursor %d", m.edCursor)
	}
	if !strings.Contains(viewOf(m), "save user") {
		t.Errorf("edit admin step:\n%s", viewOf(m))
	}
	m = send(send(m, leftKey), enter) // Yes
	if len(m.users) != 1 || !m.users[0].Admin || !hasAdmin(m.users) {
		t.Fatalf("after the edit: %+v", m.users)
	}
	// Esc in the form goes back to the table and changes nothing.
	if got := send(send(m, runes("a")), escKey); got.view != viewUserList || len(got.users) != 1 {
		t.Error("Esc in the form did not cancel it")
	}

	// The cursor wraps over the row and the four buttons.
	last := len(m.users) + len(userButtons) - 1
	m.userCursor = 0
	if got := send(m, up); got.userCursor != last {
		t.Errorf("Up from the top = %d, want %d", got.userCursor, last)
	}
	// Confirm and Cancel both return to the menu, keeping the accounts.
	for _, btn := range []int{2, 3} {
		m.userCursor = len(m.users) + btn
		if got := send(m, enter); got.view != viewHub || len(got.users) != 1 {
			t.Errorf("button %d: view %d users %d", btn, got.view, len(got.users))
		}
	}
	if send(m, escKey).view != viewHub {
		t.Error("Esc did not leave the table")
	}
}

func TestNetworkInterfaces(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	// No interfaces: the page says so rather than showing an empty list.
	m.view = viewNetList
	if !strings.Contains(viewOf(m), "No network interfaces found") {
		t.Errorf("empty list:\n%s", viewOf(m))
	}
	if got := send(m, enter); got.view != viewNetList {
		t.Error("Enter with no interfaces opened a form")
	}

	m.netIfaces = []netIfaceEntry{
		{Name: "eth0", Details: []string{"ipv4:    192.168.1.20", "netmask: 255.255.255.0"}},
		{Name: "wlan0", Details: []string{"(no address - link down or DHCP pending)"}},
	}
	if send(m, up).netCursor != 0 {
		t.Error("Up moved above the list")
	}
	m = send(send(send(m, down), down), up)
	m = send(m, down)
	if m.netCursor != 1 {
		t.Errorf("cursor = %d", m.netCursor)
	}
	// Static: address, gateway, DNS, each step saying what it wants.
	m = send(m, enter)
	if m.view != viewNetForm || m.edCursor != 0 {
		t.Fatalf("form: view %d cursor %d", m.view, m.edCursor)
	}
	m = send(send(m, down), enter)
	for _, step := range []struct{ title, value string }{
		{"Address in CIDR form", "10.0.0.2/24"},
		{"Gateway (blank for none)", "10.0.0.1"},
		{"DNS server (blank for none)", "10.0.0.53"},
	} {
		if view := viewOf(m); !strings.Contains(view, step.title) || !strings.Contains(view, "Configure wlan0 (static)") {
			t.Errorf("step %q:\n%s", step.title, view)
		}
		m = send(typeText(m, step.value), enter)
	}
	ni := m.netIfaces[1]
	if !ni.Static || ni.Addr != "10.0.0.2/24" || ni.Gateway != "10.0.0.1" || ni.DNS != "10.0.0.53" || m.view != viewNetList {
		t.Fatalf("wlan0 = %+v", ni)
	}
	// Reopening starts on Static with the values; Esc leaves them alone.
	m = send(m, enter)
	if m.edCursor != 1 {
		t.Errorf("a static interface should reopen on Static: %d", m.edCursor)
	}
	if got := send(send(m, enter), escKey); got.view != viewNetList || !got.netIfaces[1].Static {
		t.Error("Esc in the form changed the interface")
	}
	// Back to DHCP clears what was set.
	m = send(send(m, up), enter)
	if ni = m.netIfaces[1]; ni.Static || ni.Addr != "" || ni.Gateway != "" || ni.DNS != "" {
		t.Errorf("after DHCP: %+v", ni)
	}
	if send(m, escKey).view != viewHub {
		t.Error("Esc did not leave the list")
	}

	// The live listing leaves out loopback, and every interface says
	// something even with no address.
	for _, e := range liveInterfaces() {
		if e.Name == "lo" || len(e.Details) == 0 {
			t.Errorf("live interface %+v", e)
		}
	}
}

func TestGovernorFollowsTheDriverMode(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	m.cpuVendor, m.kernelProfile = "intel", "Realtime"
	// On Intel the P-State mode chosen for an isolation profile decides
	// which governors the installed system will have.
	for mode, active := range map[string]bool{"active": true, "no_hwp": true, "force": true, "passive": false, "disable": false} {
		m.intelPstate = mode
		if m.activeMode() != active {
			t.Errorf("intel_pstate=%s: active mode = %v", mode, m.activeMode())
		}
	}
	// A governor the new mode still has is kept.
	m.intelPstate, m.governor = "active", "performance"
	if old := m.dropStaleGovernor(); old != "" || m.governor != "performance" {
		t.Errorf("a valid governor was dropped: %q", old)
	}

	// Choosing a profile that changes the mode resets a governor it does not
	// have, and says so — together with the VM warning when both apply.
	pick := func(m model, profile string) model {
		m = openSectionInTab(m, 0, secKernelArgs)
		m = kernelTop(m)
		for i := 0; i < kernelProfileIndex(profile); i++ {
			m = send(m, down)
		}
		return send(m, enter)
	}
	base := withSize(newModel(twoTestDisks()))
	base.cpuVendor, base.epp = "intel", false
	base.intelPstate, base.governor = "active", "schedutil" // valid while no isolation profile is chosen
	got := pick(base, "Realtime")
	if got.governor != "" || got.popup != popupMessage || !strings.Contains(strings.Join(got.popupMsg, " "), "schedutil governor does not exist") {
		t.Errorf("Realtime with a stale governor: governor %q popup %v", got.governor, got.popupMsg)
	}
	vm := base
	vm.vm, vm.vmName = true, "KVM"
	got = pick(vm, "Telco/RAN")
	msg := strings.Join(got.popupMsg, " ")
	if !strings.Contains(msg, "virtual machine (KVM)") || !strings.Contains(msg, "schedutil governor does not exist") {
		t.Errorf("VM and stale governor together: %v", got.popupMsg)
	}
	// Custom, reached from an isolation profile with a governor that only
	// its mode had: the form opens with the notice over it.
	iso := base
	iso.kernelProfile, iso.intelPstate, iso.governor, iso.epp = "Realtime", "passive", "schedutil", true
	got = pick(iso, "Custom")
	if got.view != viewMiniForm || got.popup != popupMessage || got.governor != "" {
		t.Errorf("Custom with a stale governor: view %d popup %d governor %q", got.view, got.popup, got.governor)
	}
}

func TestProfileDetails(t *testing.T) {
	m := withSize(newModel(twoTestDisks()))
	// Custom has no arguments of its own.
	if d := strings.Join(m.profileDetails(kernelProfileByName("Custom")), "\n"); !strings.Contains(d, "(none - type them in Custom)") {
		t.Errorf("Custom details:\n%s", d)
	}
	// An isolation profile on a machine too small to isolate says so.
	m.cpuIso = system.CPUIsolation{OK: false}
	if note := m.profileNoteBase(kernelProfileByName("Realtime")); !strings.Contains(note, "Too few CPUs to isolate automatically") {
		t.Errorf("note = %q", note)
	}
}

func TestSmallAndUnsizedScreens(t *testing.T) {
	// Before the first size message the dialog is drawn at its standard
	// size, popups included.
	m := newModel(twoTestDisks())
	m.popup, m.popupMsg = popupMessage, []string{"Title", "", "body"}
	view := viewOf(m)
	if !strings.Contains(view, "Title") || !strings.Contains(view, "press any key") {
		t.Errorf("popup with no size:\n%s", view)
	}
	if contentWidth(0) != dialogWidth-4 || contentWidth(20) != 40 || panelHeight(0) != dialogHeight-2 {
		t.Errorf("sizes: %d %d %d", contentWidth(0), contentWidth(20), panelHeight(0))
	}
	// A popup taller than what is under it is cut to it, not drawn past.
	base := "one\ntwo"
	tall := strings.Repeat("popup-row\n", 5) + "last"
	if got := overlayCenter(base, tall, 0, 0); len(strings.Split(got, "\n")) != 2 {
		t.Errorf("overlay grew the screen: %q", got)
	}
	// Text that does not fit its lines ends in an ellipsis.
	lines := wrapWords("one two three four five six seven eight nine ten", 9, 2)
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "...") || len(lines[1]) > 9 {
		t.Errorf("wrapWords = %q", lines)
	}
	// The hub draws the Install button as available once the install is.
	ready := readyModel()
	ready.hubCursor = len(ready.visibleSections(0))
	if !strings.Contains(viewOf(ready), "Install") {
		t.Error("the hub does not show Install")
	}
}
