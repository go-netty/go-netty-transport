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
	"github.com/go-netty/go-netty/transport"
	"github.com/xtaci/kcp-go/v5"
)

// New a kcp transport factory
func New() transport.Factory {
	return new(kcpFactory)
}

type kcpFactory struct{}

func (*kcpFactory) Schemes() transport.Schemes {
	return transport.Schemes{"kcp"}
}

func (f *kcpFactory) Connect(options *transport.Options) (transport.Transport, error) {

	if err := f.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	kcpOptions := FromContext(options.Context, DefaultOptions)

	conn, err := kcp.DialWithOptions(options.Address.Host, kcpOptions.Block, kcpOptions.DataShard, kcpOptions.ParityShard)
	if nil != err {
		return nil, err
	}

	tt, err := newKcpTransport(conn, kcpOptions, true)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}
	return tt, nil
}

func (f *kcpFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := f.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	kcpOptions := FromContext(options.Context, DefaultOptions)

	l, err := kcp.ListenWithOptions(options.AddressWithoutHost(), kcpOptions.Block, kcpOptions.DataShard, kcpOptions.ParityShard)
	if nil != err {
		return nil, err
	}

	if err = l.SetDSCP(kcpOptions.DSCP); nil != err {
		_ = l.Close()
		return nil, err
	}

	if err = l.SetReadBuffer(kcpOptions.SockBuf); nil != err {
		_ = l.Close()
		return nil, err
	}

	if err = l.SetWriteBuffer(kcpOptions.SockBuf); nil != err {
		_ = l.Close()
		return nil, err
	}

	return &kcpAcceptor{listener: l, options: kcpOptions}, nil
}

type kcpAcceptor struct {
	listener *kcp.Listener
	options  *Options
}

func (k *kcpAcceptor) Accept() (transport.Transport, error) {
	conn, err := k.listener.AcceptKCP()
	if nil != err {
		return nil, err
	}

	tt, err := newKcpTransport(conn, k.options, false)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}
	return tt, nil
}

func (k *kcpAcceptor) Close() error {
	if k.listener != nil {
		defer func() { k.listener = nil }()
		return k.listener.Close()
	}
	return nil
}
