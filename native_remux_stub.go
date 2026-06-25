// Package sikasa: native_remux_stub.go
// Purpose: Stub implementations for the deprecated RemuxNative mode when the
// puregoremux build tag is not set. These always return an error indicating
// the build tag is needed.

//go:build !puregoremux

package sikasa

import (
	"fmt"
	"io"
)

func initNativeRemuxer() error {
	return fmt.Errorf("RemuxNative mode requires building with: -tags puregoremux")
}

func RemuxStream(reader io.Reader, outPath string) error {
	return fmt.Errorf("RemuxNative mode requires building with: -tags puregoremux")
}

func RemuxStreamToWriter(reader io.Reader, writer io.Writer) error {
	return fmt.Errorf("RemuxNative mode requires building with: -tags puregoremux")
}
