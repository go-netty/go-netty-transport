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
	"errors"
	"github.com/go-netty/go-netty/transport"
	"github.com/xtaci/kcp-go"
)

func New() transport.Factory {
	return new(kcpFactory)
}

type kcpFactory struct {
	listener *kcp.Listener
	options  *Options
}

func (*kcpFactory) Schemes() transport.Schemes {
	return transport.Schemes{"kcp"}
}

func (f *kcpFactory) Connect(options *transport.Options) (transport.Transport, error) {

	if err := f.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	kcpOptions := FromContext(options.Context, DefaultOptions)

	conn, err := kcp.DialWithOptions(options.Address.Host, kcpOptions.Block, kcpOptions.DataShard, kcpOptions.ParityShard)
	if nil != err {
		return nil, err
	}

	return (&kcpTransport{UDPSession: conn}).applyOptions(kcpOptions, true)
}

func (f *kcpFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := f.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	_ = f.Close()

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

	f.listener = l
	f.options = kcpOptions
	return f, nil
}

func (f *kcpFactory) Accept() (transport.Transport, error) {

	if nil == f.listener {
		return nil, errors.New("no listener")
	}

	conn, err := f.listener.AcceptKCP()
	if nil != err {
		return nil, err
	}

	return (&kcpTransport{UDPSession: conn}).applyOptions(f.options, false)
}

func (f *kcpFactory) Close() error {
	if f.listener != nil {
		defer func() { f.listener = nil }()
		return f.listener.Close()
	}
	return nil
}
