# completions

Shell completion scripts for the slinstaller command line (bash, zsh,
fish), as in slinit's `completions/`.

`slinstaller.bash` and `slinstaller.fish` complete every flag, the stage
names after `--through` and `--resume-from`, and config files otherwise.
They are generated from the flags themselves:

```sh
slinstaller --completion bash > completions/slinstaller.bash
slinstaller --completion fish > completions/slinstaller.fish
```

and a test fails when a shipped script no longer matches what the binary
generates. Install them as `/usr/share/bash-completion/completions/slinstaller`
and `/usr/share/fish/vendor_completions.d/slinstaller.fish`; zsh can use
the bash one through `bashcompinit`.
