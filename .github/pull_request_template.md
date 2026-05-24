## Description

Brief description of the changes.

## Type of Change

- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing

- [ ] Builds (`go build ./...`) / shell lints cleanly
- [ ] Unit tests pass (`go test ./...`)
- [ ] Exercised in a VM (QEMU + OVMF, UEFI) against a scratch image
- [ ] New tests added for new logic

## Safety Checklist

- [ ] Destructive operations stay gated and confirmable
- [ ] No change to *which* device/partition is wiped as a side effect
- [ ] Stays within the UEFI-only model (no incidental BIOS/MBR path)
- [ ] No secrets/passphrases logged
- [ ] Changes are focused and minimal
