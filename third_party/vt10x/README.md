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

Everything else is byte-identical to the upstream pseudo-version.

## Dropping this fork

Delete this directory, remove the
`replace github.com/hinshun/vt10x => ./third_party/vt10x` line from the
root `go.mod`, and run `go mod tidy`.
