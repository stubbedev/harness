# go-pty (vendored fork)

Vendored fork of [github.com/aymanbagabas/go-pty](https://github.com/aymanbagabas/go-pty)
at `v0.2.3`, wired in through a `replace` directive in the root `go.mod` so
the import path stays `github.com/aymanbagabas/go-pty`. It is the
pseudo-terminal behind `internal/term`: Unix PTYs through `creack/pty`, and
ConPTY on Windows, which `os/exec` alone cannot start a process under.

## Why

Upstream ships an `ApplyTerminalModes` helper for SSH servers in
`ssh.go`/`ssh_unix.go`/`ssh_other.go`. Harness never calls it, but importing
the package pulls in what it needs: `golang.org/x/crypto/ssh` and
`github.com/u-root/u-root/pkg/termios`. That termios package has no
DragonFly BSD or Solaris implementation, so `GOOS=dragonfly` and
`GOOS=solaris` builds of the dependency fail before any of it is used.

## Local changes

- `ssh.go`, `ssh_unix.go`, `ssh_other.go`: removed, together with the
  `golang.org/x/crypto` and `github.com/u-root/u-root` requirements they
  carried. `examples/` and the tests are not vendored.
- `go.mod`: reduced to the two requirements the remaining files import.

Everything else is byte-identical to the upstream tag. Drop the fork once
upstream moves the SSH helper behind its own package or build tag.
