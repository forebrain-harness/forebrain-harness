//go:build cgo

package channel

import (
	"bytes"

	"github.com/youthlin/silk"
)

func init() {
	weixinSilkDecode = func(data []byte, sampleRate int) ([]byte, error) {
		return silk.Decode(bytes.NewReader(data), silk.WithSampleRate(sampleRate))
	}
}
