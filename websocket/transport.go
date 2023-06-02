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
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/go-netty/go-netty/transport"
	"github.com/go-netty/go-netty/utils"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

var netBuffersPool = sync.Pool{
	New: func() interface{} {
		return make(net.Buffers, 0, 8)
	},
}

type websocketTransport struct {
	transport.Buffered
	options     *Options
	state       ws.State  // StateClientSide or StateServerSide
	opCode      ws.OpCode // OpText or OpBinary
	request     *http.Request
	hdr         *ws.Header
	reader      *wsutil.Reader
	writeLocker utils.SpinLocker
	msgState    wsflate.MessageState
}

func newWebsocketTransport(conn net.Conn, request *http.Request, wsOptions *Options, client bool) (*websocketTransport, error) {
	t := &websocketTransport{
		Buffered: transport.NewBuffered(conn, wsOptions.ReadBufferSize, wsOptions.WriteBufferSize),
		options:  wsOptions,
		request:  request,
	}

	// setup opcode
	if t.opCode = ws.OpText; 0 == (t.options.OpCode & ws.OpText) {
		t.opCode = ws.OpBinary
	}

	// client or server
	if t.state = ws.StateServerSide; client {
		t.state = ws.StateClientSide
	}
	// message reader
	t.reader = &wsutil.Reader{
		Source:          t.Buffered,
		State:           t.state | ws.StateExtended,
		CheckUTF8:       wsOptions.CheckUTF8,
		SkipHeaderCheck: false,
		MaxFrameSize:    wsOptions.MaxFrameSize,
		OnIntermediate:  wsutil.ControlFrameHandler(t.Buffered, t.state),
		Extensions:      []wsutil.RecvExtension{&t.msgState},
	}

	return t, nil
}

func (t *websocketTransport) Path() string {
	return t.request.URL.Path
}

// Read implements io.Reader. It reads the next message payload into p.
// It takes care on fragmented messages.
//
// The error is io.EOF only if all of message bytes were read.
// If an io.EOF happens during reading some but not all the message bytes
// Read() returns io.ErrUnexpectedEOF.
//
// The error is ErrNoFrameAdvance if no NextFrame() call was made before
// reading next message bytes.
func (t *websocketTransport) Read(p []byte) (int, error) {

	if nil == t.hdr {
		for {
			hdr, err := t.reader.NextFrame()
			if nil != err {
				// connection closed
				if io.EOF == err {
					err = io.ErrUnexpectedEOF
				}
				return 0, err
			}

			if hdr.OpCode.IsControl() && t.reader.OnIntermediate != nil {
				if err = t.reader.OnIntermediate(hdr, t.reader); nil != err {
					return 0, err
				}
				continue
			}

			if 0 == (hdr.OpCode & t.options.OpCode) {
				if err = t.reader.Discard(); nil != err {
					// connection closed
					if io.EOF == err {
						err = io.ErrUnexpectedEOF
					}
					return 0, err
				}
				continue
			}

			t.hdr = &hdr
			break
		}
	}

	n, err := t.reader.Read(p)
	if nil != err && io.EOF == err {
		// all of message bytes were read
		t.hdr = nil
	}

	return n, err
}

func (t *websocketTransport) Write(p []byte) (n int, err error) {

	// copy buffer on client side
	if t.state.ClientSide() {
		buf := make([]byte, len(p))
		copy(buf, p)
		p = buf
	}

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	return len(p), ws.WriteFrame(t.Buffered, t.buildFrame([][]byte{p}))
}

func (t *websocketTransport) Writev(buffs transport.Buffers) (int64, error) {

	// header1 + payload1 | header2 + payload2 | ...
	var combineBuffers = netBuffersPool.Get().(net.Buffers)
	defer netBuffersPool.Put(combineBuffers)
	// reset buffer
	combineBuffers = combineBuffers[:0]

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

		frame := t.buildFrame(pkt)

		// pack packet header
		var header = [ws.MaxHeaderSize]byte{}
		var hn = packHeader(header[:], frame.Header)

		// header
		combineBuffers = append(combineBuffers, header[:hn])
		// payload
		combineBuffers = append(combineBuffers, pkt...)
	}

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	return combineBuffers.WriteTo(t.Buffered)
}

func (t *websocketTransport) WriteClose(code int, reason string) error {
	closeFrame := ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusCode(code), reason))

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	if err := ws.WriteFrame(t.Buffered, closeFrame); nil != err {
		return err
	}
	return t.Flush()
}

func (t *websocketTransport) HttpRequest() *http.Request {
	return t.request
}

func (t *websocketTransport) buildFrame(buffs [][]byte) (frame ws.Frame) {

	// new websocket frame
	frame = ws.NewFrame(t.opCode, true, nil)

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

func packHeader(bts []byte, h ws.Header) int {
	const (
		bit0  = 0x80
		len7  = int64(125)
		len16 = int64(^(uint16(0)))
		len64 = int64(^(uint64(0)) >> 1)
	)

	if h.Fin {
		bts[0] |= bit0
	}
	bts[0] |= h.Rsv << 4
	bts[0] |= byte(h.OpCode)

	var n int
	switch {
	case h.Length <= len7:
		bts[1] = byte(h.Length)
		n = 2

	case h.Length <= len16:
		bts[1] = 126
		binary.BigEndian.PutUint16(bts[2:4], uint16(h.Length))
		n = 4

	case h.Length <= len64:
		bts[1] = 127
		binary.BigEndian.PutUint64(bts[2:10], uint64(h.Length))
		n = 10

	default:
		panic(ws.ErrHeaderLengthUnexpected)
	}

	if h.Masked {
		bts[1] |= bit0
		n += copy(bts[n:], h.Mask[:])
	}
	return n
}
