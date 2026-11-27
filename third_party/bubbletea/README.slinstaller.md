# bubbletea v1.2.4, without its terminal query at start

This is github.com/charmbracelet/bubbletea v1.2.4 (MIT, see LICENSE),
unchanged except that `tea_init.go` and the tests are left out.

`tea_init.go` holds an `init()` that asks the terminal for its background
colour (OSC 11, then DSR) as the program starts — in every program that
links bubbletea, before `main` runs, whatever it then does. slinstaller's
serial/text flow never draws with bubbletea, yet on a terminal that does not
answer (a QEMU serial console, many real ones) it waited termenv's 5 second
timeout at every start and left the query's escape bytes on the screen; a
late reply landed in the answers typed to the prompts.

slinstaller makes the same query itself, just before a full-screen program
starts (`tui.RunFullscreen`, `tui.RunInstall`), which is what the init was
for. Upstream removes the init in v2; drop this copy and the `replace` in
go.mod then.
