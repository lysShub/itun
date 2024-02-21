package sconn

import (
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net"

	"github.com/lysShub/itun/sconn/crypto"
)

type SecretKeyClient interface {
	secretKey
	client()
}
type clientImpl struct{}

func (clientImpl) client() {}

type SecretKeyServer interface {
	secretKey
	server()
}

type serverImpl struct{}

func (serverImpl) server() {}

type secretKey interface {
	// SecretKey  get crypto secret key, return Key{} mean not crypto
	SecretKey(ctx context.Context, conn net.Conn) (Key, error)
}

type Key = [crypto.Bytes]byte

type NotCryptoClient struct{ clientImpl }
type NotCryptoServer struct{ serverImpl }

var _ secretKey = (*NotCryptoClient)(nil)

func (c *NotCryptoClient) SecretKey(ctx context.Context, conn net.Conn) (Key, error) {
	var key = Key{}

	n, err := conn.Write(key[:])
	if err != nil {
		return key, err
	} else if n != crypto.Bytes {
		return key, fmt.Errorf("SecretKey write interrupt")
	}

	n, err = io.ReadFull(conn, key[:])
	if err != nil {
		return key, err
	} else if n != crypto.Bytes {
		return key, fmt.Errorf("SecretKey read interrupt")
	}

	if key != (Key{}) {
		return key, fmt.Errorf("SecretKey NotCrypto faild")
	}

	return key, nil
}

func (c *NotCryptoServer) SecretKey(ctx context.Context, conn net.Conn) (Key, error) {
	var key = (Key{})

	n, err := io.ReadFull(conn, key[:])
	if err != nil {
		return key, err
	} else if n != crypto.Bytes {
		return key, fmt.Errorf("SecretKey read interrupt")
	}

	if key != (Key{}) {
		return key, fmt.Errorf("SecretKey NotCrypto faild")
	}

	n, err = conn.Write(key[:])
	if err != nil {
		return key, err
	} else if n != crypto.Bytes {
		return key, fmt.Errorf("SecretKey write interrupt")
	}

	return key, nil
}

type TokenClient struct {
	clientImpl
	Tokener interface {
		Token() (tk []byte, key Key, err error)
	}
}

type TokenServer struct {
	serverImpl
	Valider interface {
		Valid(tk []byte) (key Key, err error)
	}
}

func (c *TokenClient) SecretKey(ctx context.Context, conn net.Conn) (Key, error) {
	tk, key, err := c.Tokener.Token()
	if err != nil {
		return Key{}, err
	}

	err = gob.NewEncoder(conn).Encode(tk)
	if err != nil {
		return Key{}, err
	}

	var resp string
	err = gob.NewDecoder(conn).Decode(&resp)
	if err != nil {
		return Key{}, err
	}

	if resp != "" {
		return Key{}, fmt.Errorf("SecretKey Token faild, %s", resp)
	}
	return key, nil
}

func (c *TokenServer) SecretKey(ctx context.Context, conn net.Conn) (Key, error) {
	var req []byte
	err := gob.NewDecoder(conn).Decode(&req)
	if err != nil {
		return Key{}, err
	}

	var resp string
	key, err := c.Valider.Valid(req)
	if err != nil {
		resp = err.Error()
	}

	err = gob.NewEncoder(conn).Encode(resp)
	if err != nil {
		return Key{}, err
	}

	return key, nil
}
