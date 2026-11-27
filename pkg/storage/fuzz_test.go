package storage

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// withSecrets runs f with exactly the given secrets registered, and puts
// back what was registered before: the list is process-wide.
func withSecrets(t *testing.T, values []string, f func()) {
	t.Helper()
	secretsMu.Lock()
	saved := secrets
	secrets = nil
	secretsMu.Unlock()
	defer func() {
		secretsMu.Lock()
		secrets = saved
		secretsMu.Unlock()
	}()
	AddSecret(values...)
	f()
}

// FuzzMaskWriterChunks checks what a command prints reaches the console
// and the log masked the same however the pipe happens to split it: the
// masking may not depend on where a read ended, so a secret printed across
// two reads — or across the writer's long-line flush — is masked as one.
func FuzzMaskWriterChunks(f *testing.F) {
	f.Add("tok=s3cr3t-token\ndone\n", "s3cr3t-token", uint16(7), uint16(3))
	f.Add("10%\r20%\rs3cr3t-token", "s3cr3t-token", uint16(1), uint16(1))
	f.Add(strings.Repeat("a", 64<<10-4)+"s3cr3t-token\n", "s3cr3t-token", uint16(65535), uint16(2))
	f.Fuzz(func(t *testing.T, stream, secret string, first, step uint16) {
		if len(secret) < 4 || strings.ContainsAny(secret, "\r\n") || strings.Contains(secret, "***") {
			t.Skip() // AddSecret keeps only these; a secret never spans lines
		}
		if strings.HasPrefix(secret, "*") || strings.HasSuffix(secret, "*") {
			// The mask beside the text after it spells such a secret
			// again ("*000" in "*000000" is masked to "***000") without
			// a byte of it shown: not a leak this check can tell apart.
			t.Skip()
		}
		withSecrets(t, []string{secret}, func() {
			var b strings.Builder
			m := &maskWriter{w: &b}
			rest := []byte(stream)
			n := int(first)
			for len(rest) > 0 {
				n = min(max(n, 1), len(rest))
				if _, err := m.Write(rest[:n]); err != nil {
					t.Fatal(err)
				}
				rest = rest[n:]
				n = int(step)
			}
			m.flush()
			got := b.String()
			if strings.Contains(got, secret) {
				t.Errorf("secret %q shown in %q", secret, got)
			}
			if strings.Count(stream, "\n")+strings.Count(stream, "\r") == strings.Count(got, "\n")+strings.Count(got, "\r") &&
				!strings.Contains(stream, secret) && got != stream {
				t.Errorf("output without a secret changed:\n in  %q\n out %q", stream, got)
			}
		})
	})
}

// FuzzMaskSecretURL checks the password in a URL's user information is
// never shown, whatever it holds, the host and user are kept.
func FuzzMaskSecretURL(f *testing.F) {
	f.Add("http", "user", "p@ss#w?rd", "proxy:3128")
	f.Add("https", "", "pw", "mirror.example")
	f.Fuzz(func(t *testing.T, scheme, user, pw, host string) {
		// What RFC 3986 lets each part hold, raw @ # ? in the password
		// included (people paste them unescaped and curl takes them).
		for _, part := range []string{scheme, user, pw, host} {
			for _, r := range part {
				if r <= ' ' || r > '~' {
					t.Skip() // no URL part holds a space or a control character
				}
			}
		}
		if scheme == "" || strings.ContainsAny(scheme, ":/?#@ \t\r\n\"'") ||
			strings.ContainsAny(user, ":/?#@ \t\r\n\"'") ||
			pw == "" || strings.ContainsAny(pw, "/ \t\r\n\"'") || strings.Contains(pw, "$") ||
			host == "" || strings.ContainsAny(host, "@/ \t\r\n\"'") {
			t.Skip()
		}
		u := scheme + "://" + user + ":" + pw + "@" + host + "/path"
		got := MaskSecret("https_proxy=" + u)
		if want := "https_proxy=" + scheme + "://" + user + ":***@" + host + "/path"; got != want {
			t.Errorf("%q masked as %q, want %q", u, got, want)
		}
	})
}

// FuzzParseSize checks a size is either refused or exactly what it says,
// never wrapped past 2^64 into a small number (a 16E partition must not
// come out as a few bytes).
func FuzzParseSize(f *testing.F) {
	for _, s := range []string{"512M", "10G", "1T", "157286400", "18446744073709551615", "16777216T", "0", "-1G", "1.5G", "G", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseSize(s)
		if err != nil {
			return
		}
		mult, digits := 1.0, s
		switch s[len(s)-1] {
		case 'K', 'k':
			mult = 1 << 10
		case 'M', 'm':
			mult = 1 << 20
		case 'G', 'g':
			mult = 1 << 30
		case 'T', 't':
			mult = 1 << 40
		}
		if mult != 1 {
			digits = s[:len(s)-1]
		}
		n, perr := strconv.ParseUint(digits, 10, 64)
		if perr != nil {
			t.Fatalf("%q accepted as %d, but %q is not a count", s, got, digits)
		}
		if want := float64(n) * mult; math.Abs(float64(got)-want) > want/1e12+1 {
			t.Errorf("%q = %d, want about %.0f", s, got, want)
		}
	})
}

// FuzzParsePartedFree checks the free-space parse never panics on what a
// parted we have not seen might print, and never makes up a region: every
// region it reports is one the output holds a free row for.
func FuzzParsePartedFree(f *testing.F) {
	f.Add("BYT;\n/dev/sda:122070MiB:scsi:512:512:gpt:Test:;\n1:1.00MiB:513MiB:512MiB:fat32:EFI:boot, esp;\n1:513MiB:122070MiB:121557MiB:free;\n")
	f.Add("1:NaN:Inf:-Inf:free;\n2:1e300MiB:x:y:ext4::;\n")
	f.Fuzz(func(t *testing.T, out string) {
		parts, free := parsePartedFree(out)
		rows := 0
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "free") {
				rows++
			}
		}
		if len(free) > rows {
			t.Errorf("%d free regions from %d free rows", len(free), rows)
		}
		if len(parts)+len(free) > strings.Count(out, "\n")+1 {
			t.Errorf("more rows parsed than lines")
		}
		for _, r := range free {
			// A size parted cannot have printed: a disk of 2^63 MiB.
			if r.StartMiB > 1<<53 || r.EndMiB > 1<<53 || r.SizeMiB > 1<<53 {
				t.Errorf("region %+v from %q", r, out)
			}
		}
		for _, p := range parts {
			if p.StartMiB > 1<<53 || p.EndMiB > 1<<53 {
				t.Errorf("partition %+v from %q", p, out)
			}
		}
	})
}
