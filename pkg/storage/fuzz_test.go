package storage

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

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
