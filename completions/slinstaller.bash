# bash completion for slinstaller (slinstaller --completion bash)
_slinstaller() {
	local cur prev
	cur="${COMP_WORDS[COMP_CWORD]}"
	prev="${COMP_WORDS[COMP_CWORD-1]}"
	case "$prev" in
	--through) COMPREPLY=($(compgen -W "pre-install partition mount meta-fs base-install fstab swapfile bootloader first-boot network save-config post-install" -- "$cur")); return ;;
	--resume-from) COMPREPLY=($(compgen -W "base-install fstab swapfile bootloader first-boot network save-config post-install" -- "$cur")); return ;;
	--completion) COMPREPLY=($(compgen -W "bash fish" -- "$cur")); return ;;
	esac
	if [[ "$cur" == -* ]]; then
		COMPREPLY=($(compgen -W "--allow-insecure-http --apply --arch -b --block-device -c --completion --config --copy-network --crypt-file --diagnostics --discover --extra-repo --force-destructive --genpass --image --in-target -j --json-yaml --keys -l --list-disks --local-repo --log --log-level --message-url --offline --packages --reboot --repo --report-url --resume --resume-from --root --skip-validation-all --skip-validation-size --swap-file-size --system-check --template --text --through --unmount --version" -- "$cur"))
		return
	fi
	COMPREPLY=($(compgen -f -X '!*.@(yaml|yml|json)' -- "$cur") $(compgen -d -- "$cur"))
}
complete -o filenames -F _slinstaller slinstaller
