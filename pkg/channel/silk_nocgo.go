//go:build !cgo

package channel

import (
	"errors"
)

func init() {
	// On Windows without CGO, silk decoding is not available
	// Users must build with CGO enabled if they need silk voice decoding
	weixinSilkDecode = func(data []byte, sampleRate int) ([]byte, error) {
		return nil, errors.New("silk voice decoding requires CGO (not available on Windows without CGO)")
	}
}
