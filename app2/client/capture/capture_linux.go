//go:build linux
// +build linux

package capture

import (
	"context"
	"time"

	"github.com/lysShub/itun/app2/client/filter"
	"github.com/lysShub/itun/cctx"
	"github.com/lysShub/rsocket"
	"github.com/pkg/errors"
)

type capture struct {
}

func newCapture(ctx cctx.CancelCtx, hit filter.Hitter, opt any) Capture {
	// return nil, errors.New("implement")
	panic(errors.New("todo implement"))
	return nil
}

func (c *capture) Capture(ctx context.Context, pkt *rsocket.Packet) (err error) {
	time.Sleep(time.Hour)
	return nil
}
func (c *capture) Inject(pkt *rsocket.Packet) error {
	return nil
}
func (c *capture) Close() error {
	return nil
}
