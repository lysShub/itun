package filter

import (
	"encoding/hex"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lysShub/itun"
	"github.com/lysShub/itun/app/client/filter/mapping"
	"github.com/lysShub/itun/session"
	"github.com/lysShub/sockit/test"
	"github.com/lysShub/sockit/test/debug"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type filter struct {
	// default rule
	defaultEnable atomic.Bool
	tcps          *tcpSyn

	// process rule
	processEnable atomic.Bool
	processes     []string
	processMu     sync.RWMutex
}

func newFilter() *filter {
	return &filter{
		tcps: newAddrSyn(time.Second * 15),
	}
}

func (f *filter) Hit(ip []byte) (bool, error) {
	ep := mapping.FromIP(ip)
	if !ep.Valid() {
		session.FromIP(ip)
		return false, errors.Errorf("capture invalid ip packet: %s", hex.EncodeToString(ip))
	}
	if debug.Debug() {
		require.True(test.T(), ep.Addr.Addr().IsPrivate() || ep.Addr.Addr().IsUnspecified() || ep.Addr.Addr().IsLoopback())
	}

	if f.defaultEnable.Load() {
		// // todo: from config
		// notice: require filter is capture-read(not sniff-read) tcp SYN packet
		const maxsyn = 3

		if ep.Proto == itun.TCP {
			n := f.tcps.Upgrade(ep.Addr)
			if n >= maxsyn {
				return true, nil
			}
		}
	}

	if f.processEnable.Load() {
		name, err := Global.Name(ep)
		if err != nil {
			return false, err
		} else if name == "" {
			return false, errors.WithStack(ErrNotRecord{})
		}

		f.processMu.RLock()
		defer f.processMu.RUnlock()
		return slices.Contains(f.processes, name), nil
	}

	return false, nil
}

func (f *filter) EnableDefault() error {
	f.defaultEnable.Store(true)
	return nil
}
func (f *filter) DisableDefault() error {
	f.defaultEnable.Store(false)
	return nil
}
func (f *filter) AddProcess(process string) error {
	f.processMu.Lock()
	defer f.processMu.Unlock()

	if !slices.Contains(f.processes, process) {
		f.processes = append(f.processes, process)
		f.processEnable.Store(true)
	}
	return nil
}
func (f *filter) DelProcess(process string) error {
	f.processMu.Lock()
	defer f.processMu.Unlock()

	f.processes = slices.DeleteFunc(f.processes,
		func(s string) bool { return s == process },
	)
	if len(f.processes) == 0 {
		f.processEnable.Store(false)
	}
	return nil
}

type tcpSyn struct {
	mu        sync.RWMutex
	addrs     map[netip.AddrPort]uint8
	times     *heap[info]
	keepalive time.Duration
}

func newAddrSyn(keepalive time.Duration) *tcpSyn {
	return &tcpSyn{
		addrs:     map[netip.AddrPort]uint8{},
		times:     NewHeap[info](),
		keepalive: keepalive,
	}
}

func (f *tcpSyn) Upgrade(addr netip.AddrPort) uint8 {
	f.mu.Lock()
	defer f.mu.Unlock()

	// clear expired addr
	i := f.times.Peek()
	for i.valid() && time.Since(i.Time) > f.keepalive {
		delete(f.addrs, f.times.Pop().AddrPort)
		i = f.times.Peek()
	}

	n, has := f.addrs[addr]
	f.addrs[addr] = n + 1
	if !has {
		f.times.Put(info{addr, time.Now()})
	}
	return n + 1
}

type info struct {
	netip.AddrPort
	time.Time
}

func (i info) valid() bool {
	return i.Time != time.Time{} && i.AddrPort.IsValid()
}

type heap[T info | int] struct {
	vals []T
	s, n int // start-idx, heap-size
}

func NewHeap[T info | int]() *heap[T] {
	return &heap[T]{
		vals: make([]T, initHeapCap),
	}
}

const initHeapCap = 16

func (h *heap[T]) Put(t T) {
	i := (h.s + h.n) % len(h.vals)
	h.vals[i] = t
	h.n += 1

	if len(h.vals) == h.n {
		h.grow()
	}
}

func (h *heap[T]) Pop() T {
	if h.n == 0 {
		return *new(T)
	}

	defer func() { h.s = (h.s + 1) % len(h.vals) }()
	h.n -= 1
	return h.vals[h.s]
}

func (h *heap[T]) Peek() T {
	return h.vals[h.s]
}

func (h *heap[T]) grow() {
	tmp := make([]T, len(h.vals)*2)

	n1 := copy(tmp, h.vals[h.s:])
	copy(tmp[n1:h.n], h.vals[0:])

	h.vals = tmp
	h.s = 0
}
