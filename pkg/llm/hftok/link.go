package hftok

/*
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/darwin_arm64
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/darwin_amd64
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/linux_amd64
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/linux_arm64
// windows: the Rust-built libtokenizers.a references NT symbols (NtCreateFile,
// RtlNtStatusToDosError, ...) that only ntdll exports. GNU ld resolves archives
// left to right and these flags precede the daulet module's own -ltokenizers,
// so the tokenizers family and -lntdll must share one archive group.
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/windows_amd64 -Wl,--start-group -ltokenizers -lntdll -lm -lstdc++ -lws2_32 -luserenv -Wl,--end-group
*/
import "C"
