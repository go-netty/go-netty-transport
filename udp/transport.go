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

package udp

import (
	"fmt"
	"io"
	"net"

	"github.com/go-netty/go-netty/transport"
)

func newUDPClientTransport(conn *net.UDPConn) *udpClientTransport {
	return &udpClientTransport{UDPConn: conn}
}

type udpClientTransport struct {
	*net.UDPConn // connected
}

func (u *udpClientTransport) Writev(buffs transport.Buffers) (n int64, err error) {
	for _, pkt := range buffs {

		sent, e := u.UDPConn.Write(pkt)
		if sent > 0 {
			n += int64(sent)
		}

		if nil != e {
			err = e
			return
		}
	}

	return
}

func (u *udpClientTransport) Flush() error {
	return nil
}

func (u *udpClientTransport) RawTransport() interface{} {
	return u.UDPConn
}

func newUDPServerTransport(conn *net.UDPConn, raddr *net.UDPAddr) *udpServerTransport {
	return &udpServerTransport{
		UDPConn:       conn,
		raddr:         raddr,
		receivedQueue: make(chan []byte, 128),
		closed:        make(chan struct{}),
	}
}

type udpServerTransport struct {
	*net.UDPConn  // unconnected
	raddr         *net.UDPAddr
	receivedQueue chan []byte
	closed        chan struct{}
	recvPkt       []byte
}

func (u *udpServerTransport) RemoteAddr() net.Addr {
	return u.raddr
}

func (u *udpServerTransport) Writev(buffs transport.Buffers) (n int64, err error) {
	for _, pkt := range buffs {

		sent, e := u.UDPConn.WriteToUDP(pkt, u.raddr)
		if sent > 0 {
			n += int64(sent)
		}

		if e != nil {
			err = e
			return
		}
	}

	return
}

func (u *udpServerTransport) Write(data []byte) (int, error) {
	return u.UDPConn.WriteToUDP(data, u.raddr)
}

func (u *udpServerTransport) Read(p []byte) (n int, err error) {

	if nil != u.recvPkt {
		packet, ok := <-u.receivedQueue
		if !ok {
			return 0, fmt.Errorf("broken pipe")
		}
		u.recvPkt = packet
	}

	if len(p) < len(u.recvPkt) {
		return 0, fmt.Errorf("%w: want: %d, got: %d", io.ErrShortBuffer, len(u.recvPkt), len(p))
	}

	if length := len(u.recvPkt); len(p) > length {
		p = p[:length]
	}

	n = copy(p, u.recvPkt)
	// read completed
	u.recvPkt = nil
	return n, io.EOF
}

func (u *udpServerTransport) Flush() error {
	return nil
}

func (u *udpServerTransport) RawTransport() interface{} {
	return u.UDPConn
}

func (u *udpServerTransport) Close() error {
	select {
	case <-u.closed:
	default:
		close(u.closed)
		close(u.receivedQueue)
	}
	return nil
}

func (u *udpServerTransport) received(data []byte) bool {

	select {
	case <-u.closed:
		return false
	case u.receivedQueue <- data:
		return true
	}
}
