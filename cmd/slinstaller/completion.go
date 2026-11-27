package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sunlightlinux/slinstaller/pkg/controller"
)

// printCompletion writes a completion script for shell, made from the flag
// set itself so it cannot fall behind the flags: every flag, the stage
// names after --through and --resume-from, and config files otherwise.
func printCompletion(w io.Writer, fs *flag.FlagSet, shell string) error {
	type flagInfo struct {
		name, usage string
		boolean     bool
	}
	var flags []flagInfo
	fs.VisitAll(func(f *flag.Flag) {
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		flags = append(flags, flagInfo{f.Name, f.Usage, ok && b.IsBoolFlag()})
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].name < flags[j].name })
	stages := strings.Join(controller.StageNames(), " ")
	resumable := strings.Join(controller.ResumableStages(), " ")

	switch shell {
	case "bash":
		var names []string
		for _, f := range flags {
			if len(f.name) == 1 {
				names = append(names, "-"+f.name) // a short form, as fish has it
				continue
			}
			names = append(names, "--"+f.name)
		}
		fmt.Fprintf(w, `# bash completion for slinstaller (slinstaller --completion bash)
_slinstaller() {
	local cur prev
	cur="${COMP_WORDS[COMP_CWORD]}"
	prev="${COMP_WORDS[COMP_CWORD-1]}"
	case "$prev" in
	--through) COMPREPLY=($(compgen -W %q -- "$cur")); return ;;
	--resume-from) COMPREPLY=($(compgen -W %q -- "$cur")); return ;;
	--completion) COMPREPLY=($(compgen -W "bash fish" -- "$cur")); return ;;
	esac
	if [[ "$cur" == -* ]]; then
		COMPREPLY=($(compgen -W %q -- "$cur"))
		return
	fi
	COMPREPLY=($(compgen -f -X '!*.@(yaml|yml|json)' -- "$cur") $(compgen -d -- "$cur"))
}
complete -o filenames -F _slinstaller slinstaller
`, stages, resumable, strings.Join(names, " "))
	case "fish":
		fmt.Fprintln(w, "# fish completion for slinstaller (slinstaller --completion fish)")
		for _, f := range flags {
			desc := strings.ReplaceAll(firstLine(f.usage), "'", `\'`)
			args := ""
			switch {
			case f.name == "through":
				args = fmt.Sprintf(" -x -a '%s'", stages)
			case f.name == "resume-from":
				args = fmt.Sprintf(" -x -a '%s'", resumable)
			case f.name == "completion":
				args = " -x -a 'bash fish'"
			case !f.boolean:
				args = " -r"
			}
			opt := "-l " + f.name
			if len(f.name) == 1 {
				opt = "-s " + f.name // -b, -l: fish's short options
			}
			fmt.Fprintf(w, "complete -c slinstaller %s%s -d '%s'\n", opt, args, desc)
		}
	default:
		return fmt.Errorf("--completion: %q is not bash or fish", shell)
	}
	return nil
}

// firstLine is a flag's usage up to its first clause, short enough for a
// completion menu.
func firstLine(s string) string {
	for _, sep := range []string{"; ", " (", ": "} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}
