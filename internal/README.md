# internal

Packages private to slinstaller — helpers the binary and the `pkg/`
subsystems share but that are not meant to be imported from outside the
module. Go enforces that: nothing outside `github.com/sunlightlinux/slinstaller`
can import anything under here.

The public building blocks (the descriptor, the storage core) live under
`pkg/`. This directory is for what supports them without being part of
that surface. Empty until something needs to be shared privately.
