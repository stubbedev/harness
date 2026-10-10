# vt10x (vendored fork)

Vendored fork of [github.com/hinshun/vt10x](https://github.com/hinshun/vt10x)
at `v0.0.0-20220301184237-5011da428d02`, wired in through a `replace`
directive in the root `go.mod` so the import path stays
`github.com/hinshun/vt10x`.

## Why

Upstream's `ioctl_posix.go` issued the TIOCSWINSZ ioctl through
`syscall.Syscall(syscall.SYS_IOCTL, ...)`. Solaris's `syscall` package does
not define `SYS_IOCTL`, so `GOOS=solaris go build ./...` failed inside the
dependency and blocked the whole module
([#23](https://github.com/stubbedev/harness/issues/23)). The module is
pinned to a 2022 pseudo-version and looks dormant upstream, so the fix is
carried here until a patch lands there.

## Local changes

- `ioctl_posix.go`: `ResizePty` now sets the window size via
  `golang.org/x/sys/unix.IoctlSetWinsize`, which is defined for every
  platform the build tag covers, including Solaris, instead of the raw
  `syscall.SYS_IOCTL` call.
- `str.go`: colour values parsed out of an escape sequence are bounded
  before they are converted to the 32-bit `Color` type. `setColorName`
  already range-checked its index; the two OSC response paths checked
  only the sign, and the RGB composition shifted unbounded channels into
  each other. `colorValue` and `rgbColor` are now the only conversions,
  and both check. Reported by CodeQL as `go/incorrect-integer-conversion`.

Everything else is byte-identical to the upstream pseudo-version.

## Dropping this fork

Delete this directory, remove the
`replace github.com/hinshun/vt10x => ./third_party/vt10x` line from the
root `go.mod`, and run `go mod tidy`.
- Throughput. The terminal session feeds every byte of a command's output
  through the emulator on its read loop, so parsing speed is command
  latency. Measured on 600KB of build output at 280x80
  (`bench_test.go`): plain 133 ms to about 12 ms, colourised 195 ms to
  about 16 ms, and 1.3 million allocations to under 200.
  - `parse.go`: the per-rune trace log built its `string(c)` argument
    before checking for a logger; `traceRune` checks first.
  - `state.go`/`parse.go`/`vt_*.go`: the parser state is a method
    expression (`(*State).parse`) rather than a bound method value, which
    allocated a closure on every state change.
  - `state.go`: `clear` fills a row with doubling copies instead of cell
    by cell; `scrollUp` rotates the region's row headers instead of
    swapping them pairwise (the cleared rows may land in a different
    order; they are blank either way - `scroll_test.go` checks the screen
    against the upstream implementation).
  - `csi.go`: CSI arguments are parsed from the buffer in place, with
    `atoiBytes` matching `strconv.Atoi`'s acceptance, instead of a string
    conversion, a split and an Atoi per argument.
