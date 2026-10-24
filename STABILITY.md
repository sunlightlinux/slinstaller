# Stability commitments

This document says what slinstaller promises not to break, for how long,
and what it does instead when something has to change. It is written for
people who automate installs — anyone who commits a config to a
repository and expects it to keep working — and for contributors
deciding whether a change is allowed.

## The short version

slinstaller is in **early bootstrap**. There are no releases, and
**nothing is stable yet**: package layout, command-line flags, and
behaviour may change without notice until the first tagged release.

One contract is being designed to outlast that, because it is the one
that travels:

- **clr-installer config compatibility.** slinstaller reads
  clr-installer's config format, and intends to keep reading it: a
  config that installs on clr-installer should describe the same install
  here. Where slinstaller already honours a field, its meaning will not
  be quietly changed once releases begin — a different meaning gets a
  different field — so an unattended install does not change behaviour
  under a config that did not change. Fields slinstaller does not yet act
  on (and the Clear Linux specifics it maps or ignores) are documented as
  they are implemented.

## What is not an interface (yet)

- **The Go packages under `pkg/`.** Import them at your own risk; they
  move as the port proceeds.
- **Command-line flags and output.** `--apply`, the dry-run format, exit
  codes — all provisional.

This section will tighten into real commitments as surfaces settle. Until
then, assume anything undocumented here can change.

## Versioning

slinstaller will use [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html)
once releases begin, applied to the stable surfaces listed above — of
which, today, clr-installer config compatibility is the only one.

## Supported versions

Which versions receive fixes is covered separately, in
[SECURITY.md](SECURITY.md).
