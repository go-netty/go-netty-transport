/*
 *  Copyright 2019 the go-netty project
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *       https://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

package quic

import (
	"net"

	"github.com/go-netty/go-netty-transport/quic/internal/quick"
	"github.com/go-netty/go-netty/transport"
)

// New quic transport factory
func New() transport.Factory {
	return new(quicFactory)
}

type quicFactory struct{}

func (qf *quicFactory) Schemes() transport.Schemes {
	return transport.Schemes{"quic"}
}

func (qf *quicFactory) Connect(options *transport.Options) (transport.Transport, error) {

	if err := qf.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	quicOptions := FromContext(options.Context, DefaultOptions)

	conn, err := quick.Dial(options.Address.Host, quicOptions.TLS, quicOptions.Config)
	if nil != err {
		return nil, err
	}

	tt, err := newQuicTransport(conn, quicOptions, true)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}
	return tt, nil
}

func (qf *quicFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := qf.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	quicOptions := FromContext(options.Context, DefaultOptions)

	l, err := quick.Listen("udp", options.AddressWithoutHost(), quicOptions.TLS, quicOptions.Config)
	if nil != err {
		return nil, err
	}

	return &quicAcceptor{listener: l, options: quicOptions}, nil
}

type quicAcceptor struct {
	listener net.Listener
	options  *Options
}

func (q *quicAcceptor) Accept() (transport.Transport, error) {

	conn, err := q.listener.Accept()
	if nil != err {
		return nil, err
	}

	tt, err := newQuicTransport(conn, q.options, false)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}
	return tt, nil
}

func (q *quicAcceptor) Close() error {
	if q.listener != nil {
		defer func() { q.listener = nil }()
		return q.listener.Close()
	}
	return nil
}
