// Package system configures the installed target: its hostname,
// timezone, console keymap, locale and user accounts — the first-boot
// settings that make a freshly installed disk usable.
//
// hostname and timezone are the same on any Linux; keymap uses
// /etc/rc.conf, which both Void's core services and slinit's OpenRC
// compatibility read; locale follows the glibc convention. EnableService
// turns on a runit service in the target. Network interface
// configuration itself lives in pkg/network.
package system

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Hostname writes /etc/hostname in the target. An empty name is a no-op.
func Hostname(root, name string) error {
	if name == "" {
		return nil
	}
	path := filepath.Join(root, "etc", "hostname")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("system: write hostname: %w", err)
	}
	return nil
}

// MachineID writes /etc/machine-id in the target: a random 128-bit ID as
// 32 lowercase hex digits, the format systemd defines and slinit reads.
// slinit's journal and slinit-logind key on it, and slinit reads it as
// PID 1 before any boot service runs, so it must exist when the system
// first boots — on Void nothing else creates it. An existing non-empty
// file is kept: the ID is meant to stay with the machine.
func MachineID(root string) error {
	path := filepath.Join(root, "etc", "machine-id")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("system: machine-id: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(id[:])+"\n"), 0o444); err != nil {
		return fmt.Errorf("system: write machine-id: %w", err)
	}
	return nil
}

// Where slinit-seedrng keeps the random seed between boots, the name of a
// seed whose entropy is mixed in but not counted, and how many bytes one
// holds (what slinit-seedrng itself writes for the kernel's 256-bit pool).
const (
	seedDir  = "/var/lib/seedrng"
	seedName = "seed.no-credit"
	seedLen  = 32
)

// SeedRNG gives a slinit target a random seed for its first boot.
//
// slinit-seedrng carries entropy from one boot to the next: at shutdown it
// saves a seed, at boot it feeds the saved one to the kernel and removes
// it. The first boot has no previous one, so nothing is there — and the
// first boot is when the machine generates its SSH host keys. The seed
// written here comes from the live system's generator, which has been
// running for the whole install.
//
// It is written as a seed that is not credited: its bytes are mixed into
// the kernel's pool, which can only help, but are not counted as entropy.
// An installed disk is sometimes copied before it is first booted — a VM
// template, a batch of identical machines — and every copy would then
// start from the same "random" seed; crediting it would have the kernel
// believe otherwise.
//
// A seed already in the target (an image that brought one, an install being
// resumed) is left alone. A target that does not boot slinit gets nothing.
func SeedRNG(root string) error {
	if !IsSlinitSystem(root) {
		return nil
	}
	dir := filepath.Join(root, seedDir)
	for _, name := range []string{"seed.credit", seedName} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return nil
		}
	}
	seed := make([]byte, seedLen)
	if _, err := rand.Read(seed); err != nil {
		return fmt.Errorf("system: random seed: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	// MkdirAll leaves an existing directory's mode as it was.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	path := filepath.Join(dir, seedName)
	_ = os.Remove(path) // an empty one, read-only
	if err := os.WriteFile(path, seed, 0o400); err != nil {
		return fmt.Errorf("system: write random seed: %w", err)
	}
	return nil
}

// Timezone points /etc/localtime at the zoneinfo file for tz, the way
// every glibc system expects. An empty tz is a no-op. The link target is
// an absolute path inside the installed system, so it resolves once the
// target is booted, not from where the installer runs.
func Timezone(root, tz string) error {
	if tz == "" {
		return nil
	}
	link := filepath.Join(root, "etc", "localtime")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	_ = os.Remove(link)
	if err := os.Symlink("/usr/share/zoneinfo/"+tz, link); err != nil {
		return fmt.Errorf("system: link localtime: %w", err)
	}
	return nil
}

// zoneinfoDir and keymapsDir are the live system's timezone and console-
// keymap data, used by the validators below. Variables so tests can point
// them elsewhere.
var (
	zoneinfoDir = "/usr/share/zoneinfo"
	keymapsDir  = "/usr/share/kbd/keymaps"
)

// localesFile lists the locales the live system supports (glibc's
// SUPPORTED). A variable so tests can point it elsewhere.
var localesFile = "/usr/share/i18n/SUPPORTED"

// libcLocalesFile is Void's list of the locales glibc can generate, each
// line commented until enabled (see Locale). A variable for tests.
var libcLocalesFile = "/etc/default/libc-locales"

// ListTimezones returns the zoneinfo zones on the live system, sorted — the
// candidates for the Choose Timezone list. The tab/list metadata files and
// the posix/right duplicate trees are skipped. Empty when there is no
// zoneinfo data.
func ListTimezones() []string {
	skipTop := map[string]bool{"posix": true, "right": true}
	skipFile := map[string]bool{
		"zone.tab": true, "zone1970.tab": true, "iso3166.tab": true,
		"leapseconds": true, "tzdata.zi": true, "leap-seconds.list": true,
	}
	var out []string
	_ = filepath.WalkDir(zoneinfoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(zoneinfoDir, path)
		if rerr != nil || rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if d.IsDir() {
			if skipTop[top] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipFile[d.Name()] || strings.HasSuffix(d.Name(), ".tab") {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// ListKeymaps returns the console keymap names on the live system, sorted
// and de-duplicated — the candidates for the Configure the Keyboard list.
func ListKeymaps() []string {
	seen := map[string]bool{}
	_ = filepath.WalkDir(keymapsDir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".map.gz"):
			name = strings.TrimSuffix(name, ".map.gz")
		case strings.HasSuffix(name, ".map"):
			name = strings.TrimSuffix(name, ".map")
		default:
			return nil
		}
		seen[name] = true
		return nil
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// localesFallback is a small set of common UTF-8 locales, used when the
// live system lists none, so the Choose Language list is never empty.
var localesFallback = []string{
	"en_US.UTF-8", "en_GB.UTF-8", "de_DE.UTF-8", "fr_FR.UTF-8",
	"es_ES.UTF-8", "it_IT.UTF-8", "ro_RO.UTF-8", "ru_RU.UTF-8",
	"pt_BR.UTF-8", "zh_CN.UTF-8", "ja_JP.UTF-8",
}

// ListLocales returns the UTF-8 locales the live system supports, sorted —
// the candidates for the Choose Language list. It reads glibc's SUPPORTED,
// then Void's commented /etc/default/libc-locales, and falls back to a
// built-in common set so the list is never empty.
func ListLocales() []string {
	for _, path := range []string{localesFile, libcLocalesFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var out []string
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if line == "" {
				continue
			}
			name := strings.Fields(line)[0]
			if strings.Contains(strings.ToUpper(name), "UTF-8") && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}
	out := append([]string(nil), localesFallback...)
	sort.Strings(out)
	return out
}

// ValidTimezone reports whether tz names a real zoneinfo zone. checked is
// false when the live system has no zoneinfo data to check against, in
// which case ok is not meaningful and the caller should not reject tz.
func ValidTimezone(tz string) (ok, checked bool) {
	if _, err := os.Stat(zoneinfoDir); err != nil {
		return false, false
	}
	// Reject traversal and absolute paths before touching the filesystem.
	if tz == "" || strings.Contains(tz, "..") || strings.HasPrefix(tz, "/") {
		return false, true
	}
	fi, err := os.Stat(filepath.Join(zoneinfoDir, tz))
	return err == nil && !fi.IsDir(), true
}

// ValidLocale reports whether lang is a locale the live system's C library
// can generate: listed in glibc's SUPPORTED or in libc-locales (enabled or
// not), or one every libc has (C, POSIX). The codeset is compared as glibc
// normalises it, so en_US.utf8 is en_US.UTF-8. checked is false when there
// is no list to check against (a musl system).
func ValidLocale(lang string) (ok, checked bool) {
	switch lang {
	case "C", "POSIX", "C.UTF-8", "C.utf8":
		return true, true
	}
	want := normLocale(lang)
	for _, path := range []string{localesFile, libcLocalesFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		checked = true
		for _, line := range strings.Split(string(data), "\n") {
			// SUPPORTED: "en_US.UTF-8 UTF-8" (glibc's source tree writes
			// "en_US.UTF-8/UTF-8 \"); libc-locales: "#en_US.UTF-8 UTF-8".
			f := strings.Fields(strings.TrimLeft(line, "# \t"))
			if len(f) > 0 {
				name, _, _ := strings.Cut(f[0], "/")
				if normLocale(name) == want {
					return true, true
				}
			}
		}
	}
	return false, checked
}

// normLocale lower-cases a locale's codeset and drops its punctuation, as
// glibc does when it looks a locale up (UTF-8, utf8 and UTF8 are one).
func normLocale(lang string) string {
	name, codeset, ok := strings.Cut(lang, ".")
	if !ok {
		return lang
	}
	codeset = strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(codeset))
	return name + "." + codeset
}

// ValidKeymap reports whether code names a console keymap the live system
// carries (a <code>.map / <code>.map.gz under the kbd keymaps tree).
// checked is false when there is no keymap data to check against.
func ValidKeymap(code string) (ok, checked bool) {
	if _, err := os.Stat(keymapsDir); err != nil {
		return false, false
	}
	if code == "" || strings.ContainsAny(code, "/.") {
		return false, true
	}
	wanted := map[string]bool{code + ".map": true, code + ".map.gz": true}
	found := false
	_ = filepath.WalkDir(keymapsDir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() && wanted[d.Name()] {
			found = true
		}
		return nil
	})
	return found, true
}

// Keymap sets the console keymap by appending KEYMAP to /etc/rc.conf,
// which is where both Void's core services and slinit's OpenRC
// compatibility read it. An empty code is a no-op.
func Keymap(root, code string) error {
	if code == "" {
		return nil
	}
	path := filepath.Join(root, "etc", "rc.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	line := fmt.Sprintf("\n# console keymap set by slinstaller\nKEYMAP=%q\n", code)
	if has(path, line) { // written by an earlier attempt at this stage
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("system: open rc.conf: %w", err)
	}
	_, werr := f.WriteString(line)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("system: write rc.conf: %w", werr)
	}
	return cerr
}

// Locale sets the system locale. It writes /etc/locale.conf and, on a
// glibc system, uncomments the matching line in /etc/default/libc-locales
// so the locale can be generated. It reports whether a glibc locale was
// enabled, which tells the caller to regenerate locales; musl has neither
// the file nor the need, so there it is a no-op beyond locale.conf. An
// empty lang is a no-op.
func Locale(root, lang string) (bool, error) {
	if lang == "" {
		return false, nil
	}

	conf := filepath.Join(root, "etc", "locale.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		return false, fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(conf, []byte("LANG="+lang+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("system: write locale.conf: %w", err)
	}

	locales := filepath.Join(root, "etc", "default", "libc-locales")
	data, err := os.ReadFile(locales)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // musl: nothing to generate
		}
		return false, fmt.Errorf("system: read libc-locales: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		// Compared as glibc compares them (ValidLocale lets en_US.utf8
		// through for the en_US.UTF-8 line): an exact match left the line
		// commented, no locale generated, and LANG naming none.
		if fields := strings.Fields(strings.TrimLeft(line, "# ")); len(fields) > 0 && normLocale(fields[0]) == normLocale(lang) {
			lines[i] = strings.TrimPrefix(line, "#")
			changed = true
		}
	}
	if changed {
		if err := os.WriteFile(locales, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return false, fmt.Errorf("system: write libc-locales: %w", err)
		}
	}
	return changed, nil
}

// LocaleEnabled reports whether lang's line in the target's
// /etc/default/libc-locales is enabled (uncommented) — by this install or
// before it. A resumed first-boot regenerates then: the attempt that
// enabled the line may have stopped before generating it, and Locale,
// finding it enabled, reports no change.
func LocaleEnabled(root, lang string) bool {
	data, err := os.ReadFile(filepath.Join(root, "etc", "default", "libc-locales"))
	if err != nil || lang == "" {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && !strings.HasPrefix(line, "#") && normLocale(fields[0]) == normLocale(lang) {
			return true
		}
	}
	return false
}

// WheelSudo gives the wheel group sudo by writing a sudoers drop-in in
// the target. Admin accounts are put in wheel, so this is what turns
// their sudo on — with a password; passwordless is a separate choice.
// The 0440 mode is what sudo requires of files in sudoers.d.
func WheelSudo(root string) error {
	dir := filepath.Join(root, "etc", "sudoers.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	path := filepath.Join(dir, "wheel")
	// Read-only once written: taken away first, so the stage can be run
	// again without depending on who runs it.
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("%wheel ALL=(ALL:ALL) ALL\n"), 0o440); err != nil {
		return fmt.Errorf("system: write sudoers wheel: %w", err)
	}
	// WriteFile's mode is subject to umask; force it, since sudo rejects
	// a group- or world-writable drop-in.
	return os.Chmod(path, 0o440)
}

// AutoUpdateServiceName is the slinit service that keeps an installed
// system updated.
const AutoUpdateServiceName = "sunlight-autoupdate"

// autoUpdateService updates the system once a day, the way Clear Linux's
// swupd-update.timer does: a slinit calendar timer (cron-*) on an internal
// service, which needs slinit 3.13.0 (an older one ignores cron-* there, and
// the system would silently stop updating), run at a random but fixed time within the hour
// after midnight so a fleet does not hit the mirror at once, and caught up
// at the next boot when the machine was off. xbps updates itself first,
// as it refuses a system update while a newer xbps is available; the
// system update runs whatever that first step returns (an xbps already up
// to date is not a reason to skip it).
const autoUpdateService = `# sunlight-autoupdate -- updates the system from its xbps repositories
# once a day, as Clear Linux's swupd-update.timer does. Written by
# slinstaller (the config's autoUpdate).
#
#     slinitctl enable sunlight-autoupdate     # turn it on
#     slinitctl disable sunlight-autoupdate    # turn it off
#     slinitctl start sunlight-autoupdate      # for this boot only
type = internal
cron-command = /bin/sh -c "xbps-install -Syu xbps; xbps-install -Suy"
cron-calendar = daily
cron-randomized-delay = 1h
cron-fixed-random-delay = yes
cron-persistent = yes
cron-on-error = continue
waits-for: network
`

// AutoUpdate writes the update service into the target's /etc/slinit.d
// and, when on, enables it; off, the service is installed but not
// enabled, so turning it on later is one slinitctl enable.
func AutoUpdate(root string, on bool) error {
	dir := filepath.Join(root, "etc", "slinit.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, AutoUpdateServiceName), []byte(autoUpdateService), 0o644); err != nil {
		return fmt.Errorf("system: write %s: %w", AutoUpdateServiceName, err)
	}
	if !on {
		return nil
	}
	return EnableSlinitService(root, AutoUpdateServiceName)
}

// NetLinksServiceName is the slinit service that creates the bonds, VLANs
// and bridges configured at install time.
const NetLinksServiceName = "sunlight-netlinks"

// netLinksScript is where that service's script lives in the target.
const netLinksScript = "/etc/slinit.d/sunlight-netlinks.sh"

const netLinksService = `# sunlight-netlinks -- creates the bonds, VLANs and bridges configured at
# install time, before the network service addresses them. Written by
# slinstaller; the links themselves are in ` + netLinksScript + `.
type = scripted
command = ` + netLinksScript + `
depends-on: filesystems
restart = false
`

// NetLinks installs script — the commands that create the configured bonds,
// VLANs and bridges — to run at each boot of the target. On a slinit system
// it is a service of its own, enabled; on a runit one it is run from
// /etc/rc.local. An empty script installs nothing.
func NetLinks(root, script string) error {
	if script == "" {
		return nil
	}
	path := filepath.Join(root, netLinksScript)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("system: write %s: %w", netLinksScript, err)
	}
	if !IsSlinitSystem(root) {
		rc := filepath.Join(root, "etc", "rc.local")
		if has(rc, netLinksScript+"\n") {
			return nil
		}
		f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("system: %w", err)
		}
		_, werr := f.WriteString("\n# bonds, VLANs and bridges configured by slinstaller\n" + netLinksScript + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("system: write rc.local: %w", werr)
		}
		return nil
	}
	svc := filepath.Join(root, "etc", "slinit.d", NetLinksServiceName)
	if err := os.WriteFile(svc, []byte(netLinksService), 0o644); err != nil {
		return fmt.Errorf("system: write %s: %w", NetLinksServiceName, err)
	}
	return EnableSlinitService(root, NetLinksServiceName)
}

// LockRootCommands returns the chrooted commands that lock the target's
// root password and stamp it as changed today, as clr-installer does once
// an administrator account exists: root is then reached through sudo, and
// nobody can log in as root with a password. The change date keeps sudo
// and ssh from asking for a password change on an account that has none.
func LockRootCommands(root string, today time.Time) [][]string {
	days := today.UTC().Unix() / 86400
	return [][]string{
		{"chroot", root, "usermod", "--lock", "root"},
		{"chroot", root, "chage", "--lastday", strconv.FormatInt(days, 10), "root"},
	}
}

// EnableService enables a runit service in the target by symlinking it
// into the default runsvdir, which is what runit scans at boot. It
// points straight at /etc/runit/runsvdir/default rather than /var/service
// (a symlink that may not resolve from outside the installed system), the
// chroot-free way to enable a Void service at install time.
func EnableService(root, name string) error {
	dir := filepath.Join(root, "etc", "runit", "runsvdir", "default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	link := filepath.Join(dir, name)
	_ = os.Remove(link)
	if err := os.Symlink("/etc/sv/"+name, link); err != nil {
		return fmt.Errorf("system: enable service %s: %w", name, err)
	}
	return nil
}

// efiFirmwarePath is the sysfs directory the kernel creates only when the
// machine was booted via UEFI. It is a variable so tests can point it
// elsewhere.
var efiFirmwarePath = "/sys/firmware/efi"

// secureBootVar is the EFI variable that says whether Secure Boot is
// enforced (a variable for tests): 4 bytes of attributes, then the value.
var secureBootVar = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

// SecureBoot reports whether the firmware enforces Secure Boot. The
// installer writes an unsigned GRUB, which such firmware will not start.
func SecureBoot() bool {
	data, err := os.ReadFile(secureBootVar)
	return err == nil && len(data) >= 5 && data[4] == 1
}

// IsUEFI reports whether the running (live) system was booted in UEFI
// mode. Sunlight Linux is UEFI-only: grub-install and efibootmgr need
// /sys/firmware/efi/efivars, which exists only after a UEFI boot. A
// BIOS/legacy boot of the installer medium has no such directory, and an
// install started there would fail at the bootloader stage — after the
// disk is already wiped — so the installer checks this before touching
// anything.
func IsUEFI() bool {
	fi, err := os.Stat(efiFirmwarePath)
	return err == nil && fi.IsDir()
}

// SlinitServicesDir is where slinit-services installs its service files.
const SlinitServicesDir = "/usr/lib/slinit.d"

// IsSlinitSystem reports whether the target carries slinit's boot
// services, i.e. boots slinit rather than runit.
func IsSlinitSystem(root string) bool {
	_, err := os.Stat(filepath.Join(root, SlinitServicesDir, "boot"))
	return err == nil
}

// EnableSlinitService enables a slinit service in the target by linking
// it into /etc/slinit.d/boot.d, the directory slinit-services' boot
// service reads its optional dependencies from. It is what `slinitctl
// enable` does, without a running slinit. The service file must exist,
// in /etc/slinit.d (the administrator's, which slinit reads first) or in
// SlinitServicesDir (the packages'), so a typo cannot leave a dangling
// link.
func EnableSlinitService(root, name string) error {
	target := filepath.Join("/etc/slinit.d", name)
	if _, err := os.Stat(filepath.Join(root, target)); err != nil {
		target = filepath.Join(SlinitServicesDir, name)
		if _, err := os.Stat(filepath.Join(root, target)); err != nil {
			return fmt.Errorf("system: enable slinit service %s: %w", name, err)
		}
	}
	dir := filepath.Join(root, "etc", "slinit.d", "boot.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	link := filepath.Join(dir, name)
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("system: enable slinit service %s: %w", name, err)
	}
	return nil
}

// User is an account to create in the target.
type User struct {
	Login        string
	FullName     string // GECOS / display name; "" leaves it empty
	PasswordHash string // a crypt(3) hash, or "" for no password
	Shell        string // "" leaves useradd's default
	Admin        bool   // added to the wheel group
	SSHKeys      []string
}

// AddCommand returns the chrooted useradd command that creates the user.
// The password is passed as its crypt hash via -p, never in the clear.
func (u User) AddCommand(root string) []string {
	args := []string{"chroot", root, "useradd", "-m"}
	if u.Admin {
		args = append(args, "-G", "wheel")
	}
	if u.Shell != "" {
		args = append(args, "-s", u.Shell)
	}
	if u.FullName != "" {
		args = append(args, "-c", u.FullName)
	}
	if u.PasswordHash != "" {
		args = append(args, "-p", u.PasswordHash)
	}
	return append(args, u.Login)
}

// WriteSSHKeys writes the user's authorized_keys into the target and
// returns the chrooted command that hands the .ssh directory to them.
// With no keys it writes nothing and returns nil.
//
// The home can be a preserved partition, so everything in it belongs to
// whoever had the account before. No link of theirs is followed: a home
// that is a symlink is refused, and a .ssh or authorized_keys that is one
// (or a hard link) is replaced. Followed, "authorized_keys ->
// ../../../etc/shadow" wrote the keys over the shadow file useradd had
// just written.
func (u User) WriteSSHKeys(root string) ([]string, error) {
	if len(u.SSHKeys) == 0 {
		return nil, nil
	}
	home := filepath.Join(root, "home", u.Login)
	if fi, err := os.Lstat(home); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("system: %s is a symbolic link; not writing ssh keys through it", home)
	}
	dir := filepath.Join(home, ".ssh")
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dir); err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("system: %w", err)
	}
	file := filepath.Join(dir, "authorized_keys")
	if fi, err := os.Lstat(file); err == nil {
		st, ok := fi.Sys().(*syscall.Stat_t)
		if fi.Mode()&os.ModeSymlink != 0 || (fi.Mode().IsRegular() && ok && st.Nlink > 1) {
			if err := os.Remove(file); err != nil {
				return nil, fmt.Errorf("system: %w", err)
			}
		}
	}
	content := strings.Join(u.SSHKeys, "\n") + "\n"
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("system: write authorized_keys: %w", err)
	}
	_, werr := f.WriteString(content)
	if werr == nil {
		werr = f.Chmod(0o600) // a kept file keeps its old mode otherwise
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("system: write authorized_keys: %w", werr)
	}
	// The directory and the key file only, not -R: anything else in a
	// preserved .ssh may be a hard link to another user's file (another
	// home on the same filesystem), and chown -R would hand it over.
	return []string{"chroot", root, "chown", u.Login + ":" + u.Login,
		"/home/" + u.Login + "/.ssh", "/home/" + u.Login + "/.ssh/authorized_keys"}, nil
}

// has reports whether the file at path already contains text.
func has(path, text string) bool {
	// Only an ordinary file is read: a path that leads to a device has no
	// end to read to.
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}

// CheckLocale refuses a time zone, keymap or language this system does not
// know (each only where the system has the data to tell). Empty means
// "leave the target's default" and is not checked.
func CheckLocale(tz, keymap, lang string) error {
	if tz != "" {
		if ok, checked := ValidTimezone(tz); checked && !ok {
			return fmt.Errorf("timezone %q is not a known zoneinfo zone (e.g. UTC, Europe/Bucharest)", tz)
		}
	}
	if keymap != "" {
		if ok, checked := ValidKeymap(keymap); checked && !ok {
			return fmt.Errorf("keymap %q is not among the system's console keymaps (e.g. us, de, ro)", keymap)
		}
	}
	if lang != "" {
		if ok, checked := ValidLocale(lang); checked && !ok {
			return fmt.Errorf("language %q is not a locale the system's C library supports (e.g. en_US.UTF-8)", lang)
		}
	}
	return nil
}
