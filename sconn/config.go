package sconn

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/pkg/errors"

	"github.com/lysShub/fatun/sconn/crypto"
)

type Config struct {
	// client set first tcp packet, server recv and check it, then replay
	// second tcp packet, etc.
	PrevPackets PrevPackets //todo: support mutiple data set

	// swap secret key
	SwapKey SwapKey

	HandshakeMTU int
}

func (c *Config) init() error {
	if c == nil {
		return errors.New("xx")
	}

	if c.HandshakeMTU <= 0 {
		return errors.New("invalid mtu")
	}

	return nil
}

type PrevPackets [][]byte

type ErrPrevPacketInvalid int

func (e ErrPrevPacketInvalid) Error() string {
	return fmt.Sprintf("previous pakcet %d is invalid", e)
}

func (pps PrevPackets) Client(ctx context.Context, conn net.Conn) (err error) {
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Now())
	})
	defer stop()

	for i := 0; i < len(pps); i++ {
		if i%2 == 0 {
			_, err := conn.Write(pps[i])
			if err != nil {
				return errors.WithStack(err)
			}
		} else {
			var b = make([]byte, len(pps[i]))

			if _, err := io.ReadFull(conn, b); err != nil {
				return errors.WithStack(err)
			}
			if !bytes.Equal(b, pps[i]) {
				return ErrPrevPacketInvalid(i)
			}
		}
	}
	return nil
}

func (pps PrevPackets) Server(ctx context.Context, conn net.Conn) (err error) {
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Now())
	})
	defer stop()

	for i := 0; i < len(pps); i++ {
		if i%2 == 0 {
			var b = make([]byte, len(pps[i]))

			if _, err := io.ReadFull(conn, b); err != nil {
				return errors.WithStack(err)
			}
			if !bytes.Equal(b, pps[i]) {
				return ErrPrevPacketInvalid(i)
			}
		} else {
			_, err := conn.Write(pps[i])
			if err != nil {
				return errors.WithStack(err)
			}
		}
	}
	return nil
}

type SwapKey interface {
	Client(ctx context.Context, conn net.Conn) (crypto.Key, error)
	Server(ctx context.Context, conn net.Conn) (crypto.Key, error)
}

// Sign sign can't guarantee transport security
type Sign struct {
	Sign   []byte
	Parser func(sign []byte) (crypto.Key, error)
}

func (t *Sign) Client(ctx context.Context, conn net.Conn) (crypto.Key, error) {
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Now())
	})
	defer stop()

	key, err := t.Parser(t.Sign)
	if err != nil {
		return crypto.Key{}, err
	}

	err = gob.NewEncoder(conn).Encode(t.Sign)
	if err != nil {
		return crypto.Key{}, errors.WithStack(err)
	}

	return key, nil
}

func (t *Sign) Server(ctx context.Context, conn net.Conn) (crypto.Key, error) {
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Now())
	})
	defer stop()

	var sign []byte
	err := gob.NewDecoder(conn).Decode(&sign)
	if err != nil {
		select {
		case <-ctx.Done():
			return crypto.Key{}, errors.WithStack(ctx.Err())
		default:
			return crypto.Key{}, errors.WithStack(err)
		}
	}

	return t.Parser(sign)
}
