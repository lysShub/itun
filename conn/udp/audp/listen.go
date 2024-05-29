package audp

// acceptable udp conn

import (
	"net"
	"net/netip"
	"runtime"
	"sync"
	"time"

	"github.com/lysShub/netkit/debug"
	"github.com/lysShub/netkit/errorx"
)

type Conn interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Close() error
}

type Listener struct {
	udp *net.UDPConn

	pool *sync.Pool

	connsMu sync.RWMutex
	conns   map[netip.AddrPort]*acceptConn

	connCh chan Conn

	closeErr errorx.CloseErr
}

func Listen(addr *net.UDPAddr, maxRecvSize int) (*Listener, error) {
	var l = &Listener{
		conns:  map[netip.AddrPort]*acceptConn{},
		connCh: make(chan Conn, 32),
	}

	var err error
	if l.udp, err = net.ListenUDP("udp", addr); err != nil {
		return nil, l.close(err)
	}

	l.pool = &sync.Pool{
		New: func() any {
			b := make([]byte, maxRecvSize)
			return segment(&b)
		},
	}

	ncpu := runtime.NumCPU()
	if debug.Debug() {
		ncpu = 1
	}
	for i := 0; i < max(1, ncpu); i++ {
		go l.accpetService()
	}
	return l, nil
}

func (l *Listener) close(cause error) error {
	defer func() { close(l.connCh) }()
	return l.closeErr.Close(func() (errs []error) {
		return append(errs, cause)
	})
}

func (l *Listener) Accept() (Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, l.close(nil)
	}
	return conn, nil
}

func (l *Listener) accpetService() (_ error) {
	for {
		seg := l.pool.Get().(segment)
		seg.full()

		// todo: 应该设置较长的timeout，可能存在僵尸conn
		n, addr, err := l.udp.ReadFromUDPAddrPort(*seg)
		if err != nil {
			return l.close(err)
		}
		seg.data(n)
		// todo: 校验数据包

		l.connsMu.RLock()
		a, has := l.conns[addr]
		l.connsMu.RUnlock()
		if !has {
			a = newAcceptConn(l, addr)
			l.connsMu.Lock()
			l.conns[addr] = a
			l.connsMu.Unlock()
		}
		a.put(seg)
		if !has && !l.closeErr.Closed() {
			select {
			case l.connCh <- a:
			default:
			}
		}
	}
}
func (l *Listener) Addr() netip.AddrPort { return netip.MustParseAddrPort(l.udp.LocalAddr().String()) }
func (l *Listener) Close() error         { return l.close(nil) }

func (l *Listener) put(seg segment) {
	if seg == nil {
		return
	}
	l.pool.Put(seg)
}
func (l *Listener) del(raddr netip.AddrPort) {
	l.connsMu.RLock()
	n := len(l.conns)
	l.connsMu.RUnlock()

	if n == 1 {
		l._del(raddr)
	} else {
		// tcp可以根据ISN进行判断, udp只能等待一段时间
		time.AfterFunc(time.Second*5, func() { l._del(raddr) })
	}
}

func (l *Listener) _del(raddr netip.AddrPort) {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	delete(l.conns, raddr)
	if len(l.conns) == 0 && l.closeErr.Closed() {
		l.udp.Close()
	}
}