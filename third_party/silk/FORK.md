# Vendored fork of github.com/youthlin/silk

Source: `github.com/youthlin/silk@v0.0.4`

This is a minimal in-tree fork that lives as a normal package inside the main
module. Import it as:

    github.com/forebrain-harness/forebrain-harness/third_party/silk

## Why we fork

The upstream package fails to compile under cgo on Windows/amd64. In
`internal/decode.go`, `malloc` was called as `C.malloc(C.ulong(size))`. On
Windows (LLP64) `unsigned long` is 32-bit while `size_t` is 64-bit, so the
argument type does not match `malloc(size_t)` and the build fails with:

    cannot use _Ctype_ulong(size) (value of uint32 type _Ctype_ulong)
    as _Ctype_size_t value in argument to (_Cfunc__CMalloc)

forebrain requires cgo on every platform (the weixin channel decodes WeChat SILK
voice messages through this package), so Windows must build cleanly.

## Local changes vs upstream

- `internal/decode.go`: `C.malloc(C.ulong(size))` → `C.malloc(C.size_t(size))`.
  `size_t` is correct on every platform; on LP64 (Linux/macOS) it is identical
  to the previous behavior, so this is a safe, portable fix.
- Removed `cmd/testdata/` (~12 MB of sample audio) — not needed to build the
  library and only bloats the repository.

## Updating

If upstream releases a version that fixes the Windows cgo issue, delete
`third_party/silk`, switch the `pkg/channel/silk_cgo.go` import back to the
upstream path, and `go get` that upstream version. Do not reintroduce a
`replace` directive: it makes `go install` of this module fail.
