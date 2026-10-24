# lib

Native helpers — small, non-Go support code — mirroring slinit's `lib/`,
where a C helper does the one thing Go cannot.

slinstaller may never need one: it drives standard tools (`parted`,
`mkfs`, `grub`, `efibootmgr`) rather than reimplementing them. This
directory is reserved to keep the layout parallel with slinit, and may
stay empty. Remove it if the parity is not worth keeping.
