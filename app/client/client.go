package client

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/lysShub/itun"
	"github.com/lysShub/itun/app/client/capture"
	cs "github.com/lysShub/itun/app/client/session"
	"github.com/lysShub/itun/control"
	"github.com/lysShub/itun/errorx"
	"github.com/lysShub/itun/sconn"
	"github.com/lysShub/itun/session"
	"github.com/lysShub/itun/ustack"
	"github.com/lysShub/itun/ustack/gonet"
	"github.com/lysShub/itun/ustack/link"
	"github.com/lysShub/relraw"
	"github.com/pkg/errors"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

type Config struct {
	sconn.Config
	Logger slog.Handler
}

type Client struct {
	cfg    *Config
	logger *slog.Logger

	conn *sconn.Conn

	sessMgr *cs.SessionMgr

	ep  ustack.Endpoint
	ctr control.Client

	srvCtx    context.Context
	srvCancel context.CancelFunc
	closeErr  atomic.Pointer[error]
}

func NewClient(ctx context.Context, conn *sconn.Conn, cfg *Config) (*Client, error) {
	var c = &Client{
		cfg: cfg,
		logger: slog.New(cfg.Logger.WithGroup("client").WithAttrs([]slog.Attr{
			{Key: "local", Value: slog.StringValue(conn.LocalAddr().String())},
			{Key: "proxy", Value: slog.StringValue(conn.RemoteAddr().String())},
		})),
		conn:    conn,
		sessMgr: cs.NewSessionMgr(),
	}
	c.srvCtx, c.srvCancel = context.WithCancel(context.Background())

	if stack, err := ustack.NewUstack(
		link.NewList(8, cfg.MTU),
		conn.LocalAddr().Addr(),
	); err != nil {
		return nil, c.close(err)
	} else {
		c.ep, err = ustack.NewEndpoint(
			stack, conn.LocalAddr().Port(),
			conn.RemoteAddr(),
		)
		if err != nil {
			return nil, c.close(err)
		}
	}

	go c.uplinkService()
	go c.downService()

	if tcp, err := gonet.DialTCPWithBind(
		ctx, c.ep.Stack(),
		conn.LocalAddr(), conn.RemoteAddr(),
		header.IPv4ProtocolNumber,
	); err != nil {
		return nil, c.close(err)
	} else {
		c.ctr = control.NewClient(tcp)
	}

	// todo: init config
	// if err := c.ctr.EndConfig(ctx); err != nil {
	// 	return nil, c.close(err)
	// }

	return c, nil
}

func (c *Client) close(cause error) (err error) {
	if cause == nil {
		cause = os.ErrClosed
	}

	if c.closeErr.CompareAndSwap(nil, &cause) {
		err = errorx.Join(cause, c.ctr.Close())
		c.srvCancel()
		err = errorx.Join(err, c.ep.Close())
		err = errorx.Join(err, c.sessMgr.Close())
		err = errorx.Join(err, c.conn.Close())

		c.logger.Info("close", "cause", err.Error())

		c.closeErr.Store(&err)
		return err
	} else {
		return *c.closeErr.Load()
	}
}

func (c *Client) uplinkService() {
	var (
		tcp = relraw.NewPacket(0, c.cfg.MTU)
		err error
	)

	for {
		tcp.Sets(0, c.cfg.MTU)
		err = c.ep.Outbound(c.srvCtx, tcp)
		if err != nil {
			c.close(err)
			return
		}

		err = c.uplink(c.srvCtx, tcp, session.CtrSessID)
		if err != nil {
			c.close(err)
			return
		}
	}
}

func (c *Client) downService() {
	var (
		pkt     = relraw.NewPacket(0, c.cfg.MTU)
		tinyCnt uint8
		id      session.ID
		err     error
	)

	for tinyCnt < 8 { // todo: from config
		pkt.Sets(0, c.cfg.MTU)
		id, err = c.conn.Recv(c.srvCtx, pkt)
		if err != nil {
			tinyCnt++
			c.logger.Warn(err.Error())
			continue
		}

		if id == session.CtrSessID {
			c.ep.Inbound(pkt)
		} else {
			s, err := c.sessMgr.Get(id)
			if err != nil {
				tinyCnt++
				c.logger.Warn(err.Error())
				continue
			}

			err = s.Inject(pkt)
			if err != nil {
				c.close(err)
				return
			}
		}
	}
	err = errors.WithStack(ErrTooManyInvalidPacket{})
	c.logger.Error(err.Error(), errorx.TraceAttr(err))
	c.close(err)
}

type ErrTooManyInvalidPacket struct{}

func (e ErrTooManyInvalidPacket) Error() string {
	return "recv too many invalid packet"
}

func (c *Client) uplink(ctx context.Context, pkt *relraw.Packet, id session.ID) error {
	return c.conn.Send(ctx, pkt, id)
}

func (c *Client) AddSession(ctx context.Context, s capture.Session) error {
	self := session.Session{
		Src:   c.conn.LocalAddr(),
		Proto: itun.TCP,
		Dst:   c.conn.RemoteAddr(),
	}
	if s.Session() == self {
		return errors.Errorf("can't proxy self %s", self.String())
	}

	resp, err := c.ctr.AddSession(ctx, s.Session())
	if err != nil {
		return err
	} else if resp.Err != nil {
		return resp.Err
	} else {
		return c.sessMgr.Add(sessionImplPtr(c), s, resp.ID)
	}
}
