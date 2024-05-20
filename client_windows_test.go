//go:build windows
// +build windows

package fatun_test

import (
	"testing"
	"time"

	"github.com/lysShub/divert-go"
	"github.com/lysShub/fatcp"
	"github.com/lysShub/fatun"
	"github.com/lysShub/fatun/peer"
	"github.com/stretchr/testify/require"
)

func TestXxx(t *testing.T) {
	divert.MustLoad(divert.DLL)
	defer divert.Release()

	conn, err := fatcp.Dial[peer.Default]("8.137.91.200:443", &fatcp.Config{})
	require.NoError(t, err)
	defer conn.Close()

	c, err := fatun.NewClient[peer.Default](func(c *fatun.Client) { c.Conn = conn })
	require.NoError(t, err)

	filter, ok := c.Capture.(interface{ Enable(process string) })
	require.True(t, ok)

	err = c.Run()
	require.NoError(t, err)

	filter.Enable("curl.exe")

	time.Sleep(time.Hour)
}