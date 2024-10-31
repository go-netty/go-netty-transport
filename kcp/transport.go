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

type kcpTransport struct {
	*kcp.UDPSession
	client bool
}

func newKcpTransport(conn *kcp.UDPSession, kcpOptions *Options, client bool) (*kcpTransport, error) {
	conn.SetStreamMode(true)
	conn.SetWriteDelay(false)
	conn.SetNoDelay(kcpOptions.NoDelay, kcpOptions.Interval, kcpOptions.Resend, kcpOptions.NoCongestion)
	conn.SetMtu(kcpOptions.MTU)
	conn.SetWindowSize(kcpOptions.SndWnd, kcpOptions.RcvWnd)
	conn.SetACKNoDelay(kcpOptions.AckNodelay)

	if client {
		if err := conn.SetDSCP(kcpOptions.DSCP); nil != err {
			return nil, err
		}

		if err := conn.SetReadBuffer(kcpOptions.SockBuf); nil != err {
			return nil, err
		}

		if err := conn.SetWriteBuffer(kcpOptions.SockBuf); nil != err {
			return nil, err
		}
	}

	return &kcpTransport{UDPSession: conn, client: client}, nil
}

func (t *kcpTransport) Writev(buffs transport.Buffers) (int64, error) {
	n, err := t.UDPSession.WriteBuffers(buffs)
	return int64(n), err
}

func (t *kcpTransport) Flush() error {
	return nil
}

func (t *kcpTransport) RawTransport() interface{} {
	return t.UDPSession
}
