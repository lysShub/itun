package links

import (
	"fmt"
	"net/netip"

	"github.com/lysShub/fatcp"
	"github.com/lysShub/fatun/peer"
	"github.com/lysShub/netkit/packet"
	"github.com/pkg/errors"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// LinksManager link manager, use for proxy-server
type LinksManager interface {
	Add(link Uplink, conn fatcp.Conn[peer.Peer]) (localPort uint16, err error)
	Downlink(link Downlink) (conn fatcp.Conn[peer.Peer], clientPort uint16, has bool) // todo: return error
	Uplink(link Uplink) (localPort uint16, has bool)
	Close() error
}

type Uplink struct {
	Process netip.AddrPort // client process address(notice NAT)
	Proto   tcpip.TransportProtocolNumber
	Server  netip.AddrPort // client process request server address
}

func (d Uplink) String() string { return fmt.Sprintf("%#v", d) }

type Downlink struct {
	Server netip.AddrPort // client process address(notice NAT)
	Proto  tcpip.TransportProtocolNumber
	Local  netip.AddrPort // proxy-server alloced local address
}

func (d Downlink) String() string { return fmt.Sprintf("%#v", d) }

func StripIP(ip *packet.Packet) (Downlink, error) {
	if header.IPVersion(ip.Bytes()) != 4 {
		return Downlink{}, errors.New("only support ipv4")
	}

	hdr := header.IPv4(ip.Bytes())
	switch hdr.TransportProtocol() {
	case tcp.ProtocolNumber, udp.ProtocolNumber:
	default:
		return Downlink{}, errors.Errorf("not support protocol %d", hdr.Protocol())
	}
	t := header.UDP(hdr.Payload())

	ip.SetHead(ip.Head() + int(hdr.HeaderLength()))
	return Downlink{
		Server: netip.AddrPortFrom(netip.AddrFrom4(hdr.SourceAddress().As4()), t.SourcePort()),
		Proto:  hdr.TransportProtocol(),
		Local:  netip.AddrPortFrom(netip.AddrFrom4(hdr.DestinationAddress().As4()), t.DestinationPort()),
	}, nil
}

type Heap[T any] struct {
	vals       []T
	sart, size int
}

func NewHeap[T any](initCap uint) *Heap[T] {
	return &Heap[T]{
		vals: make([]T, initCap),
	}
}

func (h *Heap[T]) Put(t T) {
	if len(h.vals) == h.size {
		h.grow()
	}

	i := (h.sart + h.size)
	if i >= len(h.vals) {
		i = i - len(h.vals)
	}

	h.vals[i] = t
	h.size += 1

}

func (h *Heap[T]) Pop() (val T) {
	if h.size == 0 {
		return *new(T)
	}

	val = h.vals[h.sart]

	h.size -= 1
	h.sart = (h.sart + 1)
	if h.sart >= len(h.vals) {
		h.sart = h.sart - len(h.vals)
	}
	return val
}

func (h *Heap[T]) Peek() T {
	if h.size == 0 {
		return *new(T)
	}
	return h.vals[h.sart]
}

func (h *Heap[T]) Size() int {
	return h.size
}
func (h *Heap[T]) grow() {
	tmp := make([]T, len(h.vals)*2)

	n1 := copy(tmp, h.vals[h.sart:])
	copy(tmp[n1:h.size], h.vals[0:])

	h.vals = tmp
	h.sart = 0
}
