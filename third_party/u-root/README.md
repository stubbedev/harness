# u-root (vendored fork)

Vendored fork of [github.com/u-root/u-root](https://github.com/u-root/u-root)
at `v0.15.1-0.20251208185023-2f8c7e763cf8`, wired in through a `replace`
directive in the root `go.mod` so the import path stays
`github.com/u-root/u-root`. Only the packages imported (transitively via
`mvdan.cc/sh/x/coreutils`) are kept: `pkg/core` with its command packages,
plus `pkg/gzip`, `pkg/ls`, `pkg/tarutil`, `pkg/upath`, and `pkg/uroot/unixflag`.

## Why

`pkg/ls/fileinfo_unix.go` builds on every non-linux/non-openbsd Unix and
reads `syscall.Stat_t` fields `Atimespec`, `Ctimespec`, and `Birthtimespec`.
DragonFlyBSD's `syscall.Stat_t` names them `Atim`/`Ctim` and has no birth
time field at all, so `GOOS=dragonfly go build ./...` failed inside the
dependency. The module still has this bug on `main`, so the fix is carried
here until a patch lands upstream.

## Local changes

- `pkg/ls/fileinfo_unix.go`: `dragonfly` excluded from the build tag.
- `pkg/ls/fileinfo_dragonfly.go`: added, same conversion using DragonFly's
  `Atim`/`Ctim` fields; `BirthTime` stays the zero time since DragonFly's
  `Stat_t` has no birth time.

Everything else is byte-identical to the upstream pseudo-version (verified
with `diff -r` against the module cache).

## Dropping this fork

Delete this directory, remove the
`replace github.com/u-root/u-root => ./third_party/u-root` line from the
root `go.mod`, and run `go mod tidy` — provided upstream carries the
DragonFly fix by then.
