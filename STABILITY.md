# Stability commitments

This document says what slinstaller promises not to break, for how long,
and what it does instead when something has to change. It is written for
people who automate installs — anyone who commits a config to a
repository and expects it to keep working — and for contributors
deciding whether a change is allowed.

## The short version

slinstaller is in **early bootstrap**. Releases are tagged (see
[CHANGELOG.md](CHANGELOG.md)), but **nothing is stable yet**: package
layout, command-line flags, and behaviour may still change from one
release to the next.

One contract is being designed to outlast that, because it is the one
that travels:

- **clr-installer config compatibility.** slinstaller reads
  clr-installer's config format, and intends to keep reading it: a
  config that installs on clr-installer should describe the same install
  here. Where slinstaller already honours a field, its meaning will not
  be quietly changed — a different meaning gets a different field — so an
unattended install does not change behaviour
  under a config that did not change. Fields slinstaller does not yet act
  on (and the Clear Linux specifics it maps or ignores) are documented as
  they are implemented.

  A config that would have failed partway through an install — after the
  disk was wiped — or installed a system that cannot boot may be refused
  before any disk is touched, by a newer slinstaller. That is not a break
  of config compatibility: the install it described never worked.

## What is not an interface (yet)

- **The Go packages under `pkg/`.** Import them at your own risk; they
  move as the port proceeds.
- **Command-line flags and output.** `--apply`, the dry-run format, exit
  codes — all provisional.

This section will tighten into real commitments as surfaces settle. Until
then, assume anything undocumented here can change.

## Versioning

Release numbers follow [Semantic Versioning
2.0.0](https://semver.org/spec/v2.0.0.html),
applied to the stable surfaces listed above — of which, today,
clr-installer config compatibility is the only one. A release that
changes what an unchanged config does says so in its CHANGELOG entry
(2.3.0's "Changes a config may notice" is the model).

## Supported versions

[SECURITY.md](SECURITY.md) says how to report a vulnerability. There is
no list of supported versions yet: every release so far, v1.10.55 to
v2.3.1, was cut from `main`, and a fix ships in the next one.
