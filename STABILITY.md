# Stability commitments

This document says what slinstaller promises not to break, for how long,
and what it does instead when something has to change. It is written for
people who automate installs — anyone who commits a descriptor to a
repository and expects it to keep working — and for contributors
deciding whether a change is allowed.

## The short version

slinstaller is in **early bootstrap**. There are no releases, and
**nothing is stable yet**: package layout, command-line flags, and
behaviour may change without notice until the first tagged release.

One contract is being designed to outlast that, because it is the one
that travels:

- **The descriptor is versioned.** Every descriptor carries a `version`
  field, and the installer writes the version it understands
  (`descriptor.CurrentVersion`). A descriptor found on an installed
  system years later can be read by the installer that wrote it, or
  rejected clearly by one that cannot. Once releases begin, a given
  schema version will not be reinterpreted — a new meaning gets a new
  version — so an unattended install does not silently change behaviour
  under a descriptor that did not change.

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
which, today, the descriptor schema version is the only one.

## Supported versions

Which versions receive fixes is covered separately, in
[SECURITY.md](SECURITY.md).
