package sconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/lysShub/itun"
	"github.com/lysShub/itun/cctx"
	"github.com/lysShub/itun/fake/link"
	"github.com/lysShub/relraw"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

type ustack struct {
	raw   itun.RawConn
	stack *stack.Stack
	link  *link.Endpoint
}

func newUserStack(ctx cctx.CancelCtx, raw *itun.RawConn) *ustack {

	st := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
		// HandleLocal:        true,
	})
	l := link.New(4, uint32(raw.MTU()))

	const nicid tcpip.NICID = 1234
	if err := st.CreateNIC(nicid, l); err != nil {
		ctx.Cancel(errors.New(err.String()))
		return nil
	}
	st.AddProtocolAddress(nicid, tcpip.ProtocolAddress{
		Protocol:          header.IPv4ProtocolNumber,
		AddressWithPrefix: raw.LocalAddr().Addr.WithPrefix(),
	}, stack.AddressProperties{})
	st.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicid}})

	var u = &ustack{
		stack: st,
		link:  l,
	}

	go u.uplink(ctx, raw)
	go u.downlink(ctx, raw)
	return u
}

func (u *ustack) uplink(ctx cctx.CancelCtx, raw *itun.RawConn) {
	var p = relraw.ToPacket(0, make([]byte, raw.MTU()))

	for {
		err := raw.ReadCtx(ctx, p)
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				ctx.Cancel(fmt.Errorf("uplink %s", err.Error()))
			}
			return
		}
		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p.Data())})
		u.link.InjectInbound(header.IPv4ProtocolNumber, pkb)
	}
}

func (u *ustack) downlink(ctx cctx.CancelCtx, raw *itun.RawConn) {
	for {
		pkb := u.link.ReadContext(ctx)
		if pkb.IsNil() {
			return // ctx cancel
		}

		_, err := raw.Write(pkb.ToView().AsSlice())
		if err != nil {
			ctx.Cancel(fmt.Errorf("downlink %s", err.Error()))
			return
		}
	}
}

func (s *ustack) SeqAck() (seg, ack uint32) {
	return s.link.SeqAck()
}

func (s *ustack) Accept(ctx cctx.CancelCtx) net.Conn {
	l, err := gonet.ListenTCP(s.stack, s.raw.LocalAddr(), s.raw.NetworkProtocolNumber())
	if err != nil {
		ctx.Cancel(err)
		return nil
	}

	acceptCtx := cctx.WithTimeout(ctx, time.Second*5) // todo: from config

	var conn net.Conn
	go func() {
		var err error
		conn, err = l.Accept()
		if err != nil {
			acceptCtx.Cancel(err)
		}
		acceptCtx.Cancel(nil)
	}()
	<-acceptCtx.Done()

	err = acceptCtx.Err()
	if err != nil && !errors.Is(err, context.Canceled) {
		ctx.Cancel(errors.Join(err, l.Close()))
		return nil
	}
	return conn // todo: validate remote addr
}

func (s *ustack) Connect(ctx cctx.CancelCtx) net.Conn {
	conn, err := gonet.DialTCPWithBind(
		ctx, s.stack,
		s.raw.LocalAddr(), s.raw.RemoteAddr(),
		s.raw.NetworkProtocolNumber(),
	)
	if err != nil {
		ctx.Cancel(err)
		return nil
	}
	return conn
}
