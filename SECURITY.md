# Security Policy

## Scope

slinstaller is the Sunlight Linux system installer. It runs with **root privileges** on the installation medium and performs **destructive, irreversible operations** — partitioning, formatting, and writing the bootloader to the target disk. Security-relevant areas include:

- **Disk / partition selection** — writing to the wrong device destroys data.
- **Filesystem creation and mounting** — path traversal, mount-option injection.
- **Bootloader / EFI manipulation** — `efibootmgr`, ESP contents, boot entries.
- **Package download and verification** — mirror integrity and signature checking.
- **Credential handling during install** — root password, user accounts, disk-encryption passphrases.
- **Network mirror selection** — HTTPS, trust decisions.

## Reporting a Vulnerability

If you discover a security vulnerability in slinstaller, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please send an email to: ionut_n2001@yahoo.com

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You should receive a response within 48 hours. We will work with you to understand the issue and coordinate a fix before any public disclosure.

## Reporting tips

- Never paste passphrases, private keys, or other secrets from an install log into a report.
- Note the firmware mode (Sunlight Linux is UEFI-only), the target disk layout, and whether disk encryption was in use.
