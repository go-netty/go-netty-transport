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
	"io"
	"net"

	"github.com/go-netty/go-netty/transport"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type websocketTransport struct {
	conn    *net.TCPConn
	closer  func() error
	options *Options
	state   ws.State
	path    string
}

func (t *websocketTransport) Path() string {
	return t.path
}

func (t *websocketTransport) Read(p []byte) (n int, err error) {

	hdr, reader, err := t.nextPacket(ws.OpText | ws.OpBinary)
	if nil != err {
		if io.EOF == err && nil == reader {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}

	if n, err = reader.Read(p); nil != err {
		return
	}

	if int64(n) < hdr.Length {
		if err = reader.Discard(); nil == err {
			err = io.ErrShortBuffer
		}
	}

	return
}

func (t *websocketTransport) Write(p []byte) (n int, err error) {
	frame := t.buildFrame([][]byte{p})
	return len(p), ws.WriteFrame(t.conn, frame)
}

func (t *websocketTransport) LocalAddr() net.Addr {
	return t.conn.LocalAddr()
}

func (t *websocketTransport) RemoteAddr() net.Addr {
	return t.conn.RemoteAddr()
}

func (t *websocketTransport) Close() error {
	return t.closer()
}

func (t *websocketTransport) Writev(buffs transport.Buffers) (int64, error) {
	// header1 + payload1 | header2 + payload2 | ...
	var combineBuffers = make(net.Buffers, 0, len(buffs.Indexes)+len(buffs.Buffers))

	var i = 0
	for _, j := range buffs.Indexes {

		pkt := buffs.Buffers[i:j]
		i = j

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

func (t *websocketTransport) mode() ws.OpCode {
	if t.options.Binary {
		return ws.OpBinary
	}
	return ws.OpText
}

func (t *websocketTransport) buildFrame(buffs [][]byte) (frame ws.Frame) {

	// 二进制 & 文本模式
	frame = ws.NewFrame(t.mode(), true, nil)

	// 客户端需要对数据进行编码操作
	if t.state.ClientSide() {
		frame.Header.Masked = true
		frame.Header.Mask = ws.NewMask()
	}

	for _, buf := range buffs {
		frame.Header.Length += int64(len(buf))

		if frame.Header.Masked {
			ws.Cipher(buf, frame.Header.Mask, 0)
		}
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
