//go:build cgo

package channel

import (
	"testing"
)

// TestSilkDecoderWiredUnderCGO verifies that with cgo enabled the real silk
// decoder is installed (silk_cgo.go), not the "unsupported" stub. This guards
// the Windows build, where cgo is mandatory so WeChat voice messages decode.
func TestWeixinSilkDecoderWiredUnderCGO(t *testing.T) {
	if weixinSilkDecode == nil {
		t.Fatal("weixinSilkDecode is nil under cgo build; silk_cgo.go init did not run")
	}
	// Empty input must not panic and must not return the nocgo "unsupported"
	// sentinel; the real decoder simply yields empty output / a decode error.
	out, err := weixinSilkDecode(nil, 24000)
	if err != nil && err.Error() == "silk voice decoding requires CGO (not available on Windows without CGO)" {
		t.Fatalf("nocgo stub is active under a cgo build: %v", err)
	}
	_ = out
}
