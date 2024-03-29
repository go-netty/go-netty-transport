/*
 * Copyright 2019 the go-netty project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package kcp

import (
	"context"
	"crypto/sha1"
	"strings"

	"github.com/go-netty/go-netty/transport"
	"github.com/xtaci/kcp-go/v5"
	"golang.org/x/crypto/pbkdf2"
)

// DefaultOptions default kcp options
var DefaultOptions = (&Options{
	Key:          "it's a secrecy",
	Crypt:        "",
	Mode:         "fast",
	MTU:          1350,
	SndWnd:       32,
	RcvWnd:       32,
	DataShard:    0,
	ParityShard:  0,
	DSCP:         0,
	AckNodelay:   false,
	NoDelay:      0,
	Interval:     30,
	Resend:       2,
	NoCongestion: 1,
	SockBuf:      4194304,
}).Apply()

// Options to define the kcp
type Options struct {
	Key          string         `json:"key"`
	Crypt        string         `json:"crypt"`              // aes, aes-128, aes-192, salsa20, blowfish, twofish, cast5, 3des, tea, xtea, xor, sm4, none
	Mode         string         `json:"mode"`               // fast3, fast2, fast, normal, manual
	MTU          int            `json:"mtu,string"`         // set maximum transmission unit for UDP packets
	SndWnd       int            `json:"sndwnd,string"`      // set send window size(num of packets)
	RcvWnd       int            `json:"rcvwnd,string"`      // set receive window size(num of packets)
	DataShard    int            `json:"datashard,string"`   // set reed-solomon erasure coding - datashard
	ParityShard  int            `json:"parityshard,string"` // set reed-solomon erasure coding - parityshard
	DSCP         int            `json:"dscp,string"`        // set DSCP(6bit)
	AckNodelay   bool           `json:"acknodelay,string"`  // flush ack immediately when a packet is received
	NoDelay      int            `json:"nodelay,string"`
	Interval     int            `json:"interval,string"`
	Resend       int            `json:"resend,string"`
	NoCongestion int            `json:"nc,string"`
	SockBuf      int            `json:"sockbuf,string"` // per-socket buffer in bytes
	Block        kcp.BlockCrypt `json:"-"`
}

// Apply the kcp mode & encryption options
func (o *Options) Apply() *Options {

	switch strings.ToLower(o.Mode) {
	case "normal":
		o.NoDelay, o.Interval, o.Resend, o.NoCongestion = 0, 40, 2, 1
	case "fast":
		o.NoDelay, o.Interval, o.Resend, o.NoCongestion = 0, 30, 2, 1
	case "fast2":
		o.NoDelay, o.Interval, o.Resend, o.NoCongestion = 1, 20, 2, 1
	case "fast3":
		o.NoDelay, o.Interval, o.Resend, o.NoCongestion = 1, 10, 2, 1
	}

	pass := pbkdf2.Key([]byte(o.Key), []byte("kcp-go"), 4096, 32, sha1.New)

	var err error
	switch strings.ToLower(o.Crypt) {
	case "sm4":
		o.Block, err = kcp.NewSM4BlockCrypt(pass[:16])
	case "tea":
		o.Block, err = kcp.NewTEABlockCrypt(pass[:16])
	case "xor":
		o.Block, err = kcp.NewSimpleXORBlockCrypt(pass)
	case "none":
		o.Block, err = kcp.NewNoneBlockCrypt(pass)
	case "aes-128":
		o.Block, err = kcp.NewAESBlockCrypt(pass[:16])
	case "aes-192":
		o.Block, err = kcp.NewAESBlockCrypt(pass[:24])
	case "blowfish":
		o.Block, err = kcp.NewBlowfishBlockCrypt(pass)
	case "twofish":
		o.Block, err = kcp.NewTwofishBlockCrypt(pass)
	case "cast5":
		o.Block, err = kcp.NewCast5BlockCrypt(pass[:16])
	case "3des":
		o.Block, err = kcp.NewTripleDESBlockCrypt(pass[:24])
	case "xtea":
		o.Block, err = kcp.NewXTEABlockCrypt(pass[:16])
	case "salsa20":
		o.Block, err = kcp.NewSalsa20BlockCrypt(pass)
	default:
	}

	if nil != err {
		panic(err)
	}

	return o
}

type contextKey struct{}

// WithOptions to wrap the kcp options
func WithOptions(option *Options) transport.Option {
	return func(options *transport.Options) error {
		options.Context = context.WithValue(options.Context, contextKey{}, option.Apply())
		return nil
	}
}

// FromContext to unwrap the kcp options
func FromContext(ctx context.Context, def *Options) *Options {
	if v, ok := ctx.Value(contextKey{}).(*Options); ok {
		return v
	}
	return def
}
