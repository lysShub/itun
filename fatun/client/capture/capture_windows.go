//go:build windows
// +build windows

package capture

import (
	"context"
	"os"
	"sync/atomic"

	"github.com/lysShub/divert-go"
	"github.com/lysShub/fatun/fatun"
	"github.com/lysShub/netkit/packet"
)

type capture struct {
	c Client

	handle *divert.Handle
	addr   divert.Address

	srvCtx   context.Context
	cancel   context.CancelFunc
	closeErr atomic.Pointer[error]
}

var _ Capture = (*capture)(nil)

func newCapture(client Client) (cap *capture, err error) {
	var c = &capture{
		c: client,
	}
	c.srvCtx, c.cancel = context.WithCancel(context.Background())

	var filter = "outbound and !loopback and ip and (tcp or udp)"
	c.handle, err = divert.Open(filter, divert.Network, c.c.DivertPriority(), 0)
	if err != nil {
		return nil, c.close(err)
	}

	go c.captureService()
	return c, nil
}

func (s *capture) close(cause error) error {
	if s.closeErr.CompareAndSwap(nil, &os.ErrClosed) {
		if s.cancel != nil {
			s.cancel()
		}

		if s.handle != nil {
			if err := s.handle.Close(); err != nil {
				cause = err
			}
		}

		if cause != nil {
			s.closeErr.Store(&cause)
		}
		return cause
	}
	return *s.closeErr.Load()
}

func (c *capture) captureService() error {
	var ip = packet.Make(c.c.MaxRecvBuffSize())

	for {
		n, err := c.handle.RecvCtx(c.srvCtx, ip.Sets(fatun.Overhead, 0xffff).Bytes(), &c.addr)
		if err != nil {
			return c.close(err)
		}
		ip.SetData(n)

		if !c.c.Hit(ip) {
			if _, err = c.handle.Send(ip.Bytes(), &c.addr); err != nil {
				return c.close(err)
			}
		}
	}
}

func (c *capture) Close() error { return c.close(os.ErrClosed) }
