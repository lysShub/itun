// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package gonet provides a Go net package compatible wrapper for a tcpip stack.
package gonet

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"reflect"
	"time"

	"github.com/pkg/errors"

	"github.com/lysShub/fatun/ustack"
	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// A TCPListener is a wrapper around a TCP tcpip.Endpoint that implements
// net.Listener.
type TCPListener struct {
	stack      ustack.Ustack
	ep         tcpip.Endpoint
	wq         *waiter.Queue
	cancelOnce sync.Once
	cancel     chan struct{}
}

// NewTCPListener creates a new TCPListener from a listening tcpip.Endpoint.
func NewTCPListener(s ustack.Ustack, wq *waiter.Queue, ep tcpip.Endpoint) *TCPListener {
	return &TCPListener{
		stack:  s,
		ep:     ep,
		wq:     wq,
		cancel: make(chan struct{}),
	}
}

// maxListenBacklog is set to be reasonably high for most uses of gonet. Go net
// package uses the value in /proc/sys/net/core/somaxconn file in Linux as the
// default listen backlog. The value below matches the default in common linux
// distros.
//
// See: https://cs.opensource.google/go/go/+/refs/tags/go1.18.1:src/net/sock_linux.go;drc=refs%2Ftags%2Fgo1.18.1;l=66
const maxListenBacklog = 4096

// ListenTCP creates a new TCPListener.
func ListenTCP(s ustack.Ustack, addr netip.AddrPort, network tcpip.NetworkProtocolNumber) (*TCPListener, error) {
	fullAddr := toFullAddr(addr)
	// Create a TCP endpoint, bind it, then start listening.
	var wq waiter.Queue
	ep, err := s.Stack().NewEndpoint(tcp.ProtocolNumber, network, &wq)
	if err != nil {
		return nil, errors.New(err.String())
	}

	if err := ep.Bind(fullAddr); err != nil {
		ep.Close()
		return nil, &net.OpError{
			Op:   "bind",
			Net:  "tcp",
			Addr: fullToTCPAddr(fullAddr),
			Err:  errors.New(err.String()),
		}
	}

	if err := ep.Listen(maxListenBacklog); err != nil {
		ep.Close()
		return nil, &net.OpError{
			Op:   "listen",
			Net:  "tcp",
			Addr: fullToTCPAddr(fullAddr),
			Err:  errors.New(err.String()),
		}
	}

	return NewTCPListener(s, &wq, ep), nil
}

// Close implements net.Listener.Close.
func (l *TCPListener) Close() error {
	l.ep.Close()
	return nil
}

// Shutdown stops the HTTP server.
func (l *TCPListener) Shutdown() {
	l.ep.Shutdown(tcpip.ShutdownWrite | tcpip.ShutdownRead)
	l.cancelOnce.Do(func() {
		close(l.cancel) // broadcast cancellation
	})
}

// Addr implements net.Listener.Addr.
func (l *TCPListener) Addr() net.Addr {
	a, err := l.ep.GetLocalAddress()
	if err != nil {
		return nil
	}
	return fullToTCPAddr(a)
}

type deadlineTimer struct {
	// mu protects the fields below.
	mu sync.Mutex

	readTimer     *time.Timer
	readCancelCh  chan struct{}
	writeTimer    *time.Timer
	writeCancelCh chan struct{}
}

func (d *deadlineTimer) init() {
	d.readCancelCh = make(chan struct{})
	d.writeCancelCh = make(chan struct{})
}

func (d *deadlineTimer) readCancel() <-chan struct{} {
	d.mu.Lock()
	c := d.readCancelCh
	d.mu.Unlock()
	return c
}
func (d *deadlineTimer) writeCancel() <-chan struct{} {
	d.mu.Lock()
	c := d.writeCancelCh
	d.mu.Unlock()
	return c
}

// setDeadline contains the shared logic for setting a deadline.
//
// cancelCh and timer must be pointers to deadlineTimer.readCancelCh and
// deadlineTimer.readTimer or deadlineTimer.writeCancelCh and
// deadlineTimer.writeTimer.
//
// setDeadline must only be called while holding d.mu.
func (d *deadlineTimer) setDeadline(cancelCh *chan struct{}, timer **time.Timer, t time.Time) {
	if *timer != nil && !(*timer).Stop() {
		*cancelCh = make(chan struct{})
	}

	// Create a new channel if we already closed it due to setting an already
	// expired time. We won't race with the timer because we already handled
	// that above.
	select {
	case <-*cancelCh:
		*cancelCh = make(chan struct{})
	default:
	}

	// "A zero value for t means I/O operations will not time out."
	// - net.Conn.SetDeadline
	if t.IsZero() {
		*timer = nil
		return
	}

	timeout := t.Sub(time.Now())
	if timeout <= 0 {
		close(*cancelCh)
		return
	}

	// Timer.Stop returns whether or not the AfterFunc has started, but
	// does not indicate whether or not it has completed. Make a copy of
	// the cancel channel to prevent this code from racing with the next
	// call of setDeadline replacing *cancelCh.
	ch := *cancelCh
	*timer = time.AfterFunc(timeout, func() {
		close(ch)
	})
}

// SetReadDeadline implements net.Conn.SetReadDeadline and
// net.PacketConn.SetReadDeadline.
func (d *deadlineTimer) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	d.setDeadline(&d.readCancelCh, &d.readTimer, t)
	d.mu.Unlock()
	return nil
}

// SetWriteDeadline implements net.Conn.SetWriteDeadline and
// net.PacketConn.SetWriteDeadline.
func (d *deadlineTimer) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	d.setDeadline(&d.writeCancelCh, &d.writeTimer, t)
	d.mu.Unlock()
	return nil
}

// SetDeadline implements net.Conn.SetDeadline and net.PacketConn.SetDeadline.
func (d *deadlineTimer) SetDeadline(t time.Time) error {
	d.mu.Lock()
	d.setDeadline(&d.readCancelCh, &d.readTimer, t)
	d.setDeadline(&d.writeCancelCh, &d.writeTimer, t)
	d.mu.Unlock()
	return nil
}

// A TCPConn is a wrapper around a TCP tcpip.Endpoint that implements the net.Conn
// interface.
type TCPConn struct {
	deadlineTimer

	wq *waiter.Queue
	ep tcpip.Endpoint

	// readMu serializes reads and implicitly protects read.
	//
	// Lock ordering:
	// If both readMu and deadlineTimer.mu are to be used in a single
	// request, readMu must be acquired before deadlineTimer.mu.
	readMu sync.Mutex

	// read contains bytes that have been read from the endpoint,
	// but haven't yet been returned.
	read []byte
}

// NewTCPConn creates a new TCPConn.
func NewTCPConn(wq *waiter.Queue, ep tcpip.Endpoint) *TCPConn {
	c := &TCPConn{
		wq: wq,
		ep: ep,
	}
	c.deadlineTimer.init()
	return c
}

// Accept implements net.Conn.Accept.
func (l *TCPListener) Accept(ctx context.Context) (*TCPConn, error) {
	n, wq, err := l.ep.Accept(nil)

	if _, ok := err.(*tcpip.ErrWouldBlock); ok {
		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(waiter.ReadableEvents)
		l.wq.EventRegister(&waitEntry)
		defer l.wq.EventUnregister(&waitEntry)

		for {
			n, wq, err = l.ep.Accept(nil)

			if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
				break
			}

			select {
			case <-l.cancel:
				return nil, errors.WithStack(context.Canceled)
			case <-notifyCh:
			case <-ctx.Done():
				return nil, errors.WithStack(ctx.Err())
			}
		}
	}

	if err != nil {
		return nil, &net.OpError{
			Op:   "accept",
			Net:  "tcp",
			Addr: l.Addr(),
			Err:  errors.New(err.String()),
		}
	}

	return NewTCPConn(wq, n), nil
}

// Accept implements net.Conn.Accept.
func (l *TCPListener) AcceptBy(ctx context.Context, src netip.AddrPort) (*TCPConn, error) {
	fullSrc := toFullAddr(src)
	n, wq, err := l.ep.Accept(&fullSrc)
	if _, ok := err.(*tcpip.ErrWouldBlock); ok {
		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(waiter.ReadableEvents)
		l.wq.EventRegister(&waitEntry)
		defer l.wq.EventUnregister(&waitEntry)

		for {
			n, wq, err = l.ep.Accept(&fullSrc)

			if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
				break
			}

			select {
			case <-l.cancel:
				return nil, errors.WithStack(context.Canceled)
			case <-notifyCh:
			case <-ctx.Done():
				return nil, errors.WithStack(ctx.Err())
			}
		}
	}

	if err != nil {
		return nil, &net.OpError{
			Op:   "accept",
			Net:  "tcp",
			Addr: l.Addr(),
			Err:  errors.New(err.String()),
		}
	}

	return NewTCPConn(wq, n), nil
}

type opErrorer interface {
	newOpError(op string, err error) error
}

// commonRead implements the common logic between net.Conn.Read and
// net.PacketConn.ReadFrom.
func commonRead(ctx context.Context, b []byte, ep tcpip.Endpoint, wq *waiter.Queue, deadline <-chan struct{}, addr *tcpip.FullAddress, errorer opErrorer) (int, error) {
	select {
	case <-deadline:
		return 0, errorer.newOpError("read", errors.WithStack(os.ErrDeadlineExceeded))
	case <-ctx.Done():
		return 0, errors.WithStack(ctx.Err())
	default:
	}

	w := tcpip.SliceWriter(b)
	opts := tcpip.ReadOptions{NeedRemoteAddr: addr != nil}
	res, err := ep.Read(&w, opts)

	if _, ok := err.(*tcpip.ErrWouldBlock); ok {
		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(waiter.ReadableEvents)
		wq.EventRegister(&waitEntry)
		defer wq.EventUnregister(&waitEntry)
		for {
			res, err = ep.Read(&w, opts)
			if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
				break
			}
			select {
			case <-deadline:
				return 0, errorer.newOpError("read", errors.WithStack(os.ErrDeadlineExceeded))
			case <-ctx.Done():
				return 0, errors.WithStack(ctx.Err())
			case <-notifyCh:
			}
		}
	}

	if _, ok := err.(*tcpip.ErrClosedForReceive); ok {
		return 0, io.EOF
	}
	if _, ok := err.(*tcpip.ErrConnectionReset); ok {
		return 0, errorer.newOpError("read", errors.WithStack(ErrConnectReset))
	}

	if err != nil {
		return 0, errorer.newOpError("read", errors.New(err.String()))
	}

	if addr != nil {
		*addr = res.RemoteAddr
	}
	return res.Count, nil
}

// Read implements net.Conn.Read.
func (c *TCPConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	deadline := c.readCancel()
	n, err := commonRead(backgroundCtx, b, c.ep, c.wq, deadline, nil, c)
	if n != 0 {
		c.ep.ModerateRecvBuf(n)
	}
	return n, err
}

// Write implements net.Conn.Write.
func (c *TCPConn) Write(b []byte) (int, error) {
	deadline := c.writeCancel()

	// Check if deadlineTimer has already expired.
	select {
	case <-deadline:
		return 0, c.newOpError("write", errors.WithStack(os.ErrDeadlineExceeded))
	default:
	}

	// We must handle two soft failure conditions simultaneously:
	//  1. Write may write nothing and return *tcpip.ErrWouldBlock.
	//     If this happens, we need to register for notifications if we have
	//     not already and wait to try again.
	//  2. Write may write fewer than the full number of bytes and return
	//     without error. In this case we need to try writing the remaining
	//     bytes again. I do not need to register for notifications.
	//
	// What is more, these two soft failure conditions can be interspersed.
	// There is no guarantee that all of the condition #1s will occur before
	// all of the condition #2s or visa-versa.
	var (
		r      bytes.Reader
		nbytes int
		entry  waiter.Entry
		ch     <-chan struct{}
	)
	for nbytes != len(b) {
		r.Reset(b[nbytes:])
		n, err := c.ep.Write(&r, tcpip.WriteOptions{})
		nbytes += int(n)
		switch err.(type) {
		case nil:
		case *tcpip.ErrWouldBlock:
			if ch == nil {
				entry, ch = waiter.NewChannelEntry(waiter.WritableEvents)
				c.wq.EventRegister(&entry)
				defer c.wq.EventUnregister(&entry)
			} else {
				// Don't wait immediately after registration in case more data
				// became available between when we last checked and when we setup
				// the notification.
				select {
				case <-deadline:
					return nbytes, c.newOpError("write", errors.WithStack(os.ErrDeadlineExceeded))
				case <-ch:
					continue
				}
			}
		default:
			return nbytes, c.newOpError("write", errors.New(err.String()))
		}
	}
	return nbytes, nil
}

// Close implements net.Conn.Close.
func (c *TCPConn) Close() error {
	c.ep.Close()
	return nil
}

// CloseRead shuts down the reading side of the TCP connection. Most callers
// should just use Close.
//
// A TCP Half-Close is performed the same as CloseRead for *net.TCPConn.
func (c *TCPConn) CloseRead() error {
	if terr := c.ep.Shutdown(tcpip.ShutdownRead); terr != nil {
		return c.newOpError("close", errors.New(terr.String()))
	}
	return nil
}

// CloseWrite shuts down the writing side of the TCP connection. Most callers
// should just use Close.
//
// A TCP Half-Close is performed the same as CloseWrite for *net.TCPConn.
func (c *TCPConn) CloseWrite() error {
	if terr := c.ep.Shutdown(tcpip.ShutdownWrite); terr != nil {
		return c.newOpError("close", errors.New(terr.String()))
	}
	return nil
}

// LocalAddr implements net.Conn.LocalAddr.
func (c *TCPConn) LocalAddr() net.Addr {
	a, err := c.ep.GetLocalAddress()
	if err != nil {
		return nil
	}
	return fullToTCPAddr(a)
}

// RemoteAddr implements net.Conn.RemoteAddr.
func (c *TCPConn) RemoteAddr() net.Addr {
	a, err := c.ep.GetRemoteAddress()
	if err != nil {
		return nil
	}
	return fullToTCPAddr(a)
}

// WaitBeforeDataTransmitted wait before writen data is recved by peer. firstly wait
// send-buff drain, secondly wait SndUna==SndNxt.
//
// require doesn't Write data actively when call WaitBeforeDataTransmitted.
func (c *TCPConn) WaitBeforeDataTransmitted(ctx context.Context) (sndnxt, rcvnxt uint32, err error) {
	ep, ok := c.ep.(interface {
		LockUser()
		UnlockUser()
	})
	if !ok {
		return 0, 0, errors.Errorf("not support endpoint type %T", c.ep)
	}

	var (
		used           reflect.Value
		sndUna, sndNxt reflect.Value
		rcvNxt         reflect.Value
	)
	func() {
		defer func() { _ = recover() }()
		info := reflect.Indirect(reflect.ValueOf(c.ep)).FieldByName("sndQueueInfo")
		used = info.FieldByName("SndBufUsed")

		snd := reflect.Indirect(reflect.Indirect(reflect.ValueOf(c.ep)).FieldByName("snd")).FieldByName("TCPSenderState")
		sndUna = snd.FieldByName("SndUna")
		sndNxt = snd.FieldByName("SndNxt")

		rcv := reflect.Indirect(reflect.Indirect(reflect.ValueOf(c.ep)).FieldByName("rcv")).FieldByName("TCPReceiverState")
		rcvNxt = rcv.FieldByName("RcvNxt")
	}()
	if !used.IsValid() || !sndUna.IsValid() || !sndNxt.IsValid() || !rcvNxt.IsValid() {
		return 0, 0, errors.Errorf("not support endpoint type %T", c.ep)
	}

	t := time.NewTicker(time.Millisecond * 100)
	defer t.Stop()
	for {
		ep.LockUser()
		vused := used.Int()
		ep.UnlockUser()
		if vused <= 0 {
			break
		}

		select {
		case <-ctx.Done():
			return 0, 0, errors.WithStack(ctx.Err())
		case <-t.C:
		}
	}

	for {
		ep.LockUser()
		snduna := sndUna.Uint()
		sndnxt := sndNxt.Uint()
		ep.UnlockUser()
		if snduna >= sndnxt {
			ep.LockUser()
			rcvnxt := rcvNxt.Uint()
			ep.UnlockUser()

			return uint32(sndnxt), uint32(rcvnxt), nil
		}

		select {
		case <-ctx.Done():
			return 0, 0, errors.WithStack(ctx.Err())
		case <-t.C:
		}
	}
}

func (c *TCPConn) newOpError(op string, err error) error {
	return &net.OpError{
		Op:     op,
		Net:    "tcp",
		Source: c.LocalAddr(),
		Addr:   c.RemoteAddr(),
		Err:    err,
	}
}

func fullToTCPAddr(addr tcpip.FullAddress) *net.TCPAddr {
	return &net.TCPAddr{IP: net.IP(addr.Addr.AsSlice()), Port: int(addr.Port)}
}

func fullToUDPAddr(addr tcpip.FullAddress) *net.UDPAddr {
	return &net.UDPAddr{IP: net.IP(addr.Addr.AsSlice()), Port: int(addr.Port)}
}

// DialTCP creates a new TCPConn connected to the specified address.
func DialTCP(s ustack.Ustack, addr netip.AddrPort, network tcpip.NetworkProtocolNumber) (*TCPConn, error) {
	return DialContextTCP(context.Background(), s, addr, network)
}

// DialTCPWithBind creates a new TCPConn connected to the specified
// remoteAddress with its local address bound to localAddr.
func DialTCPWithBind(ctx context.Context, s ustack.Ustack, localAddr, remoteAddr netip.AddrPort, network tcpip.NetworkProtocolNumber) (*TCPConn, error) {
	localFullAddr, remoteFullAddr := toFullAddr(localAddr), toFullAddr(remoteAddr)
	// Create TCP endpoint, then connect.
	var wq waiter.Queue
	ep, err := s.Stack().NewEndpoint(tcp.ProtocolNumber, network, &wq)
	if err != nil {
		return nil, errors.New(err.String())
	}

	// Create wait queue entry that notifies a channel.
	//
	// We do this unconditionally as Connect will always return an error.
	waitEntry, notifyCh := waiter.NewChannelEntry(waiter.WritableEvents)
	wq.EventRegister(&waitEntry)
	defer wq.EventUnregister(&waitEntry)

	select {
	case <-ctx.Done():
		return nil, errors.WithStack(ctx.Err())
	default:
	}

	// Bind before connect if requested.
	if localFullAddr != (tcpip.FullAddress{}) {
		if err = ep.Bind(localFullAddr); err != nil {
			return nil, fmt.Errorf("ep.Bind(%+v) = %s", localFullAddr, err)
		}
	}

	err = ep.Connect(remoteFullAddr)
	if _, ok := err.(*tcpip.ErrConnectStarted); ok {
		select {
		case <-ctx.Done():
			ep.Close()
			return nil, errors.WithStack(ctx.Err())
		case <-notifyCh:
		}

		err = ep.LastError()
	}
	if err != nil {
		ep.Close()
		return nil, &net.OpError{
			Op:   "connect",
			Net:  "tcp",
			Addr: fullToTCPAddr(remoteFullAddr),
			Err:  errors.New(err.String()),
		}
	}

	return NewTCPConn(&wq, ep), nil
}

// DialContextTCP creates a new TCPConn connected to the specified address
// with the option of adding cancellation and timeouts.
func DialContextTCP(ctx context.Context, s ustack.Ustack, addr netip.AddrPort, network tcpip.NetworkProtocolNumber) (*TCPConn, error) {
	return DialTCPWithBind(ctx, s, netip.AddrPortFrom(netip.IPv4Unspecified(), 0) /* localAddr */, addr /* remoteAddr */, network)
}

// A UDPConn is a wrapper around a UDP tcpip.Endpoint that implements
// net.Conn and net.PacketConn.
type UDPConn struct {
	deadlineTimer

	ep tcpip.Endpoint
	wq *waiter.Queue
}

// NewUDPConn creates a new UDPConn.
func NewUDPConn(wq *waiter.Queue, ep tcpip.Endpoint) *UDPConn {
	c := &UDPConn{
		ep: ep,
		wq: wq,
	}
	c.deadlineTimer.init()
	return c
}

// DialUDP creates a new UDPConn.
//
// If laddr is nil, a local address is automatically chosen.
//
// If raddr is nil, the UDPConn is left unconnected.
func DialUDP(s ustack.Ustack, laddr, raddr *tcpip.FullAddress, network tcpip.NetworkProtocolNumber) (*UDPConn, error) {
	var wq waiter.Queue
	ep, err := s.Stack().NewEndpoint(udp.ProtocolNumber, network, &wq)
	if err != nil {
		return nil, errors.New(err.String())
	}

	if laddr != nil {
		if err := ep.Bind(*laddr); err != nil {
			ep.Close()
			return nil, &net.OpError{
				Op:   "bind",
				Net:  "udp",
				Addr: fullToUDPAddr(*laddr),
				Err:  errors.New(err.String()),
			}
		}
	}

	c := NewUDPConn(&wq, ep)

	if raddr != nil {
		if err := c.ep.Connect(*raddr); err != nil {
			c.ep.Close()
			return nil, &net.OpError{
				Op:   "connect",
				Net:  "udp",
				Addr: fullToUDPAddr(*raddr),
				Err:  errors.New(err.String()),
			}
		}
	}

	return c, nil
}

func (c *UDPConn) newOpError(op string, err error) error {
	return c.newRemoteOpError(op, nil, err)
}

func (c *UDPConn) newRemoteOpError(op string, remote net.Addr, err error) error {
	return errors.WithStack(&net.OpError{
		Op:     op,
		Net:    "udp",
		Source: c.LocalAddr(),
		Addr:   remote,
		Err:    err,
	})
}

// RemoteAddr implements net.Conn.RemoteAddr.
func (c *UDPConn) RemoteAddr() net.Addr {
	a, err := c.ep.GetRemoteAddress()
	if err != nil {
		return nil
	}
	return fullToUDPAddr(a)
}

// Read implements net.Conn.Read
func (c *UDPConn) Read(b []byte) (int, error) {
	bytesRead, _, err := c.ReadFrom(b)
	return bytesRead, err
}

// ReadFrom implements net.PacketConn.ReadFrom.
func (c *UDPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	deadline := c.readCancel()

	var addr tcpip.FullAddress
	n, err := commonRead(backgroundCtx, b, c.ep, c.wq, deadline, &addr, c)
	if err != nil {
		return 0, nil, err
	}
	return n, fullToUDPAddr(addr), nil
}

func (c *UDPConn) Write(b []byte) (int, error) {
	return c.WriteTo(b, nil)
}

// WriteTo implements net.PacketConn.WriteTo.
func (c *UDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	deadline := c.writeCancel()

	// Check if deadline has already expired.
	select {
	case <-deadline:
		return 0, c.newRemoteOpError("write", addr, os.ErrDeadlineExceeded)
	default:
	}

	// If we're being called by Write, there is no addr
	writeOptions := tcpip.WriteOptions{}
	if addr != nil {
		ua := addr.(*net.UDPAddr)
		writeOptions.To = &tcpip.FullAddress{
			Addr: tcpip.AddrFromSlice(ua.IP),
			Port: uint16(ua.Port),
		}
	}

	var r bytes.Reader
	r.Reset(b)
	n, err := c.ep.Write(&r, writeOptions)
	if _, ok := err.(*tcpip.ErrWouldBlock); ok {
		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(waiter.WritableEvents)
		c.wq.EventRegister(&waitEntry)
		defer c.wq.EventUnregister(&waitEntry)
		for {
			select {
			case <-deadline:
				return int(n), c.newRemoteOpError("write", addr, os.ErrDeadlineExceeded)
			case <-notifyCh:
			}

			n, err = c.ep.Write(&r, writeOptions)
			if _, ok := err.(*tcpip.ErrWouldBlock); !ok {
				break
			}
		}
	}

	if err == nil {
		return int(n), nil
	}

	return int(n), c.newRemoteOpError("write", addr, errors.New(err.String()))
}

// Close implements net.PacketConn.Close.
func (c *UDPConn) Close() error {
	c.ep.Close()
	return nil
}

// LocalAddr implements net.PacketConn.LocalAddr.
func (c *UDPConn) LocalAddr() net.Addr {
	a, err := c.ep.GetLocalAddress()
	if err != nil {
		return nil
	}
	return fullToUDPAddr(a)
}

var backgroundCtx = context.Background()

func toFullAddr(addr netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{
		Addr: tcpip.AddrFromSlice(addr.Addr().AsSlice()),
		Port: addr.Port(),
	}
}
