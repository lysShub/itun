//go:build windows
// +build windows

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_TLSCapByGoRequest(t *testing.T) {
	url := "https://dl.google.com/go/go1.20.4.linux-amd64.tar.gz"

	pss, err := CaptureTLSWithGolang(context.Background(), url, 1024*16, -(16 + 20 + 2))
	require.NoError(t, err)

	err = pss.Marshal("a.pss")
	require.NoError(t, err)
}
