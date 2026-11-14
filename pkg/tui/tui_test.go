package tui

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinstaller/pkg/config"
)

func TestAssemblePlain(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "nvme0n1", RootFS: "ext4", Swap: "partition", SwapSize: "2G",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})

	if cfg.Hostname != "box" || cfg.Keyboard != "us" || cfg.Language != "en_US.UTF-8" {
		t.Errorf("identity = %q/%q/%q", cfg.Hostname, cfg.Keyboard, cfg.Language)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Login != "ion" || !cfg.Users[0].Admin ||
		cfg.Users[0].Password != "$6$abc$def" {
		t.Fatalf("user = %+v", cfg.Users)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("assembled config invalid: %v", err)
	}

	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// ESP, swap partition, root — with nvme partition naming.
	wantDev := []string{"/dev/nvme0n1p1", "/dev/nvme0n1p2", "/dev/nvme0n1p3"}
	if len(plan.Parts) != 3 {
		t.Fatalf("got %d partitions, want 3", len(plan.Parts))
	}
	for i, p := range plan.Parts {
		if p.Device != wantDev[i] {
			t.Errorf("part %d = %q, want %q", i, p.Device, wantDev[i])
		}
	}
	if plan.Parts[2].Encrypted {
		t.Error("root should not be encrypted")
	}
}

func TestAssembleEncryptedSwapFile(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "sda", RootFS: "btrfs", Encrypt: true, Swap: "file", SwapSize: "4G",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})

	if cfg.SwapFileSize != "4G" {
		t.Errorf("SwapFileSize = %q, want 4G", cfg.SwapFileSize)
	}
	plan, err := cfg.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// No swap partition: just ESP + root.
	if len(plan.Parts) != 2 {
		t.Fatalf("got %d partitions, want 2 (ESP + root, no swap)", len(plan.Parts))
	}
	root := plan.Parts[1]
	if !root.Encrypted || root.MapName != "cryptroot" || root.Filesystem != "btrfs" {
		t.Errorf("root = %+v, want encrypted btrfs cryptroot", root)
	}
	if root.EffectiveDevice() != "/dev/mapper/cryptroot" {
		t.Errorf("EffectiveDevice = %q", root.EffectiveDevice())
	}
}

func TestAssembleNoSwap(t *testing.T) {
	cfg := assemble(assembly{
		Disk: "sda", RootFS: "ext4", Swap: "none",
		Hostname: "box", Login: "ion", PassHash: "$6$abc$def",
	})
	plan, _ := cfg.Plan()
	if len(plan.Parts) != 2 {
		t.Fatalf("got %d partitions, want 2", len(plan.Parts))
	}
	for _, p := range plan.Parts {
		if p.Filesystem == "swap" {
			t.Error("no-swap layout has a swap partition")
		}
	}
	// Root fills the disk.
	cmds, _ := plan.Commands()
	flat := ""
	for _, c := range cmds {
		flat += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(flat, " 100%") {
		t.Errorf("root does not fill the disk:\n%s", flat)
	}
}

func TestMirrorConsoles(t *testing.T) {
	// A serial install keeps its serial console on the installed system.
	cfg := &config.Config{}
	mirrorConsoles(cfg, []string{"console=tty0", "console=ttyS0,115200n8"})
	if cfg.KernelArguments == nil || len(cfg.KernelArguments.Add) != 2 ||
		cfg.KernelArguments.Add[1] != "console=ttyS0,115200n8" {
		t.Fatalf("kernel arguments = %+v", cfg.KernelArguments)
	}
	// Already present: not added twice. Other arguments are kept.
	cfg = &config.Config{KernelArguments: &config.KernelArguments{Add: []string{"quiet", "console=tty0"}}}
	mirrorConsoles(cfg, []string{"console=tty0", "console=ttyS0,115200n8"})
	if got := strings.Join(cfg.KernelArguments.Add, " "); got != "quiet console=tty0 console=ttyS0,115200n8" {
		t.Errorf("kernel arguments = %q", got)
	}
	// No console detected: the config is left without kernel arguments.
	cfg = &config.Config{}
	mirrorConsoles(cfg, nil)
	if cfg.KernelArguments != nil {
		t.Errorf("kernel arguments created for no consoles: %+v", cfg.KernelArguments)
	}
}

func TestPromptFlowStartsFromTheWizardsKernelProfile(t *testing.T) {
	cfg := assemble(assembly{Disk: "sda", RootFS: "ext4", Swap: "none", Hostname: "box", Login: "ion", PassHash: "$6$x"})
	if cfg.KernelProfile != defaultKernelProfile {
		t.Errorf("kernel profile = %q, want %q", cfg.KernelProfile, defaultKernelProfile)
	}
	// Whatever the wizard would install untouched, this flow installs too.
	wizard, err := func() (*config.Config, error) {
		m := newModel(oneDisk())
		m.users = []userEntry{{Login: "ion", Password: "pw", Admin: true}}
		return m.buildConfig()
	}()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cfg.KernelArguments.Add, " ")
	if want := strings.Join(wizard.KernelArguments.Add, " "); got != want || got == "" {
		t.Errorf("prompt flow kernel arguments = %q, wizard default = %q", got, want)
	}
}

// answers is an input holding the given lines.
func answers(lines ...string) *bufio.Reader {
	text := ""
	for _, l := range lines {
		text += l + "\n"
	}
	return bufio.NewReader(strings.NewReader(text))
}

func TestSecretPromptsGiveUpWhenTheInputEnds(t *testing.T) {
	// An ended input reads as an empty answer every time. A prompt that
	// re-asks on an invalid answer must stop, not re-ask forever.
	var out bytes.Buffer
	if _, err := PromptNewPassphrase(answers(), &out); !errors.Is(err, ErrNoInput) {
		t.Errorf("passphrase on ended input: %v, want ErrNoInput", err)
	}
	// Ended after the first entry, before it could be confirmed.
	if _, err := PromptNewPassphrase(answers("a long enough passphrase"), &out); !errors.Is(err, ErrNoInput) {
		t.Errorf("passphrase with no confirmation: %v, want ErrNoInput", err)
	}
	// Refused answers, then the input ends.
	if _, err := PromptNewPassphrase(answers("x", "x", "y", "z"), &out); !errors.Is(err, ErrNoInput) {
		t.Errorf("only refused answers: %v, want ErrNoInput", err)
	}
}

func TestPromptNewPassphraseAsksUntilValidAndMatching(t *testing.T) {
	var out bytes.Buffer
	good := "correct horse battery staple"
	got, err := PromptNewPassphrase(answers(
		"short", "short", // too short: refused with the reason
		good, "a typo instead", // do not match
		good, good,
	), &out)
	if err != nil || got != good {
		t.Fatalf("passphrase = %q, %v", got, err)
	}
	if log := out.String(); !strings.Contains(log, "try again") || !strings.Contains(log, "they do not match") {
		t.Errorf("the refusals were not explained:\n%s", log)
	}
	// A last line without its newline is still an answer.
	in := bufio.NewReader(strings.NewReader(good + "\n" + good))
	if got, err := PromptNewPassphrase(in, &out); err != nil || got != good {
		t.Errorf("unterminated last line: %q, %v", got, err)
	}
}

func TestPromptChoices(t *testing.T) {
	var out bytes.Buffer
	opts := []string{"ext4", "btrfs", "xfs"}
	if got := promptChoice(answers("btrfs"), &out, "Root filesystem", opts, "ext4"); got != "btrfs" {
		t.Errorf("choice = %q", got)
	}
	// Empty, unrecognised or ended input: the default, never a guess.
	for _, in := range []*bufio.Reader{answers(""), answers("ntfs"), answers("EXT4"), answers()} {
		if got := promptChoice(in, &out, "Root filesystem", opts, "ext4"); got != "ext4" {
			t.Errorf("choice = %q, want the default", got)
		}
	}
	if got := promptDefault(answers("  box  "), &out, "Hostname", "sunlight"); got != "box" {
		t.Errorf("promptDefault = %q", got)
	}
	if got := promptDefault(answers(""), &out, "Hostname", "sunlight"); got != "sunlight" {
		t.Errorf("promptDefault empty = %q", got)
	}
}

func TestPromptYesNoOnlyYesMeansYes(t *testing.T) {
	var out bytes.Buffer
	for _, yes := range []string{"y", "Y", "yes", "YES", " yes "} {
		if !promptYesNo(answers(yes), &out, "Proceed?", false) {
			t.Errorf("%q should be yes", yes)
		}
	}
	// "Proceed with this layout?" defaults to no: anything that is not a
	// clear yes — including nothing typed and an ended input — is no.
	for _, in := range []*bufio.Reader{answers(""), answers("n"), answers("maybe"), answers("ye"), answers("1"), answers()} {
		if promptYesNo(in, &out, "Proceed?", false) {
			t.Error("a non-yes answer proceeded")
		}
	}
	// With a yes default, only an explicit other answer is no.
	if !promptYesNo(answers(""), &out, "Encrypt?", true) || promptYesNo(answers("n"), &out, "Encrypt?", true) {
		t.Error("yes-default prompt")
	}
}

func TestPromptIndex(t *testing.T) {
	var out bytes.Buffer
	// Out-of-range and non-numeric answers are asked again.
	if got := promptIndex(answers("0", "9", "two", "2"), &out, "Install to which disk", 3); got != 2 {
		t.Errorf("index = %d, want 2", got)
	}
	if n := strings.Count(out.String(), "Install to which disk"); n != 4 {
		t.Errorf("asked %d times, want 4", n)
	}
	// It ends rather than spinning when the input does.
	if got := promptIndex(answers("9"), &out, "Install to which disk", 3); got != 1 {
		t.Errorf("index on ended input = %d", got)
	}
}
