/*
 *  Copyright 2020 the go-netty project
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

package tls

import (
	"crypto/tls"
	"errors"
	"github.com/go-netty/go-netty/transport"
	"net"
)

// New a tls transport factory
func New() transport.Factory {
	return new(tlsFactory)
}

type tlsFactory struct{}

func (t *tlsFactory) Schemes() transport.Schemes {
	return transport.Schemes{"tcp", "tcp4", "tcp6"}
}

func (t *tlsFactory) Connect(options *transport.Options) (transport.Transport, error) {

	if err := t.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	tlsOptions := FromContext(options.Context, DefaultOptions)

	conn, err := tls.Dial(options.Address.Scheme, options.Address.Host, tlsOptions.TLS)
	if nil != err {
		return nil, err
	}

	return &tlsTransport{Conn: conn}, nil
}

func (t *tlsFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := t.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	tlsOptions := FromContext(options.Context, DefaultOptions)

	l, err := tls.Listen(options.Address.Scheme, options.AddressWithoutHost(), tlsOptions.TLS)
	if nil != err {
		return nil, err
	}

	return &tlsAcceptor{listener: l, options: tlsOptions}, nil
}

type tlsAcceptor struct {
	listener net.Listener
	options  *Options
}

func (t *tlsAcceptor) Accept() (transport.Transport, error) {
	if nil == t.listener {
		return nil, errors.New("no listener")
	}

	conn, err := t.listener.Accept()
	if nil != err {
		return nil, err
	}

	return &tlsTransport{Conn: conn.(*tls.Conn)}, nil
}

func (t *tlsAcceptor) Close() error {
	if t.listener != nil {
		defer func() { t.listener = nil }()
		return t.listener.Close()
	}
	return nil
}
