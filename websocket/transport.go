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

package websocket

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-netty/go-netty/transport"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type websocketTransport struct {
	conn    net.Conn
	options *Options
	state   ws.State
	request *http.Request
	hdr     ws.Header
	reader  *wsutil.Reader
}

func (t *websocketTransport) Path() string {
	return t.request.URL.Path
}

func (t *websocketTransport) Read(p []byte) (n int, err error) {

	if nil == t.reader {
		t.hdr, t.reader, err = t.nextPacket(ws.OpText | ws.OpBinary)
		if nil != err {
			if io.EOF == err && nil == t.reader {
				// connection closed
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
	}

	if int64(len(p)) < t.hdr.Length {
		err = fmt.Errorf("%w: want: %d, got: %d", io.ErrShortBuffer, t.hdr.Length, len(p))
		return
	}

	p = p[:t.hdr.Length]
	n, err = io.ReadFull(t.reader, p)
	if n == len(p) {
		t.hdr = ws.Header{}
		t.reader = nil
		// read completed
		err = io.EOF
	}

	return
}

func (t *websocketTransport) Write(p []byte) (n int, err error) {

	// copy buffer on client side
	if t.state.ClientSide() {
		buf := make([]byte, len(p))
		copy(buf, p)
		p = buf
	}

	frame := t.buildFrame([][]byte{p})
	return len(p), ws.WriteFrame(t.conn, frame)
}

func (t *websocketTransport) SetDeadline(time time.Time) error {
	return t.conn.SetDeadline(time)
}

func (t *websocketTransport) SetReadDeadline(time time.Time) error {
	return t.conn.SetReadDeadline(time)
}

func (t *websocketTransport) SetWriteDeadline(time time.Time) error {
	return t.conn.SetWriteDeadline(time)
}

func (t *websocketTransport) LocalAddr() net.Addr {
	return t.conn.LocalAddr()
}

func (t *websocketTransport) RemoteAddr() net.Addr {
	return t.conn.RemoteAddr()
}

func (t *websocketTransport) Close() error {
	return t.conn.Close()
}

func (t *websocketTransport) Writev(buffs transport.Buffers) (int64, error) {
	// header1 + payload1 | header2 + payload2 | ...
	var combineBuffers = make(net.Buffers, 0, len(buffs.Indexes)+len(buffs.Buffers))

	var i = 0
	for _, j := range buffs.Indexes {

		pkt := buffs.Buffers[i:j]
		i = j

		// copy buffer on client side
		if t.state.ClientSide() {
			buffVec := make([][]byte, len(pkt))
			for index := range pkt {
				buffVec[index] = make([]byte, len(pkt[index]))
				copy(buffVec[index], pkt[index])
			}
			pkt = buffVec
		}

		var header = [ws.MaxHeaderSize]byte{}
		headerWriter := bytes.NewBuffer(header[:])
		headerWriter.Reset()

		frame := t.buildFrame(pkt)
		if err := ws.WriteHeader(headerWriter, frame.Header); nil != err {
			return 0, err
		}

		// header
		combineBuffers = append(combineBuffers, headerWriter.Bytes())
		// payload
		combineBuffers = append(combineBuffers, pkt...)
	}

	return combineBuffers.WriteTo(t.conn)
}

func (t *websocketTransport) Flush() error {
	return nil
}

func (t *websocketTransport) RawTransport() interface{} {
	return t.conn
}

func (t *websocketTransport) HttpRequest() *http.Request {
	return t.request
}

func (t *websocketTransport) mode() ws.OpCode {
	if t.options.Binary {
		return ws.OpBinary
	}
	return ws.OpText
}

func (t *websocketTransport) buildFrame(buffs [][]byte) (frame ws.Frame) {

	// new websocket frame
	frame = ws.NewFrame(t.mode(), true, nil)

	// XOR cipher to the payload using mask
	if t.state.ClientSide() {
		frame.Header.Masked = true
		frame.Header.Mask = ws.NewMask()
	}

	for _, buf := range buffs {
		if frame.Header.Masked {
			ws.Cipher(buf, frame.Header.Mask, int(frame.Header.Length))
		}
		frame.Header.Length += int64(len(buf))
	}

	return
}

func (t *websocketTransport) nextPacket(want ws.OpCode) (hdr ws.Header, reader *wsutil.Reader, err error) {

	controlHandler := wsutil.ControlFrameHandler(t.conn, t.state)
	rd := &wsutil.Reader{
		Source:          t.conn,
		State:           t.state,
		CheckUTF8:       !t.options.Binary,
		SkipHeaderCheck: false,
		OnIntermediate:  controlHandler,
	}

	for {
		// read frame.
		hdr, err := rd.NextFrame()
		if err != nil {
			return hdr, nil, err
		}

		// handle websocket control frame.
		if hdr.OpCode.IsControl() {
			if err := controlHandler(hdr, rd); err != nil {
				return hdr, nil, err
			}
			continue
		}

		// check frame.
		if hdr.OpCode&want == 0 {
			if err := rd.Discard(); err != nil {
				return hdr, nil, err
			}
			continue
		}

		return hdr, rd, nil
	}
}

func (t *websocketTransport) applyOptions(wsOptions *Options, client bool) (*websocketTransport, error) {
	// save options.
	t.options = wsOptions
	// client or server
	if t.state = ws.StateServerSide; client {
		t.state = ws.StateClientSide
	}
	return t, nil
}
