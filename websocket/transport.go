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
	"math/rand"
	"net"
	"net/http"
	"sync"

	"github.com/go-netty/go-netty-transport/websocket/internal/xwsutil"
	"github.com/go-netty/go-netty/transport"
	"github.com/go-netty/go-netty/utils/pool/pbytes"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

var netBuffersPool sync.Pool

type websocketTransport struct {
	transport.Transport
	options     *Options
	state       ws.State  // StateClientSide or StateServerSide
	opCode      ws.OpCode // OpText or OpBinary
	request     *http.Request
	reader      *xwsutil.Reader
	msgReading  bool
	writeLocker sync.Mutex
	msgState    wsflate.MessageState
}

func newWebsocketTransport(conn net.Conn, request *http.Request, wsOptions *Options, client bool) (*websocketTransport, error) {
	t := &websocketTransport{
		Transport: transport.NewTransport(conn, wsOptions.ReadBufferSize, wsOptions.WriteBufferSize),
		options:   wsOptions,
		request:   request,
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
	t.reader = &xwsutil.Reader{
		Source:          t.Transport,
		State:           t.state | ws.StateExtended,
		CheckUTF8:       wsOptions.CheckUTF8,
		SkipHeaderCheck: false,
		MaxFrameSize:    wsOptions.MaxFrameSize,
		OnIntermediate:  wsutil.ControlFrameHandler(t.Transport, t.state),
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

	if !t.msgReading {
		for {
			hdr, err := t.reader.NextFrame()
			if nil != err {
				// connection closed
				if io.EOF == err {
					err = io.ErrUnexpectedEOF
				}
				return 0, err
			}

			if 0 == (hdr.OpCode & t.options.OpCode) {
				if hdr.OpCode.IsControl() {
					if err = t.reader.OnIntermediate(hdr, t.reader); nil != err {
						return 0, err
					}
					continue
				}

				if err = t.reader.Discard(); nil != err {
					// connection closed
					if io.EOF == err {
						err = io.ErrUnexpectedEOF
					}
					return 0, err
				}
				continue
			}

			t.msgReading = true
			break
		}
	}

	n, err := t.reader.Read(p)
	if io.EOF == err {
		// all of message bytes were read
		t.msgReading = false
	}

	return n, err
}

func (t *websocketTransport) Write(p []byte) (n int, err error) {
	headerBuffers := pbytes.GetLen(ws.MaxHeaderSize)
	defer pbytes.Put(headerBuffers)

	var hn = packHeader(headerBuffers, t.opCode, true, t.state, func(mask [4]byte) int64 {
		if t.state.ClientSide() {
			ws.Cipher(p, mask, 0)
		}
		return int64(len(p))
	})

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	// write header
	if _, err = t.Transport.Write(headerBuffers[:hn]); nil == err {
		// write payload
		n, err = t.Transport.Write(p)
	}
	return
}

func (t *websocketTransport) Writev(buffs transport.Buffers) (int64, error) {

	// header1 + payload1 | header2 + payload2 | ...
	//var combineBuffers = make(net.Buffers, 0, len(buffs.Indexes)+(len(buffs.Buffers)*2))

	var combineBuffersPtr *net.Buffers
	var combineBuffers net.Buffers
	if buff := netBuffersPool.Get(); nil != buff {
		combineBuffersPtr = buff.(*net.Buffers)
		combineBuffers = (*combineBuffersPtr)[:0]
	} else {
		combineBuffers = make(net.Buffers, 0, len(buffs.Indexes)+(len(buffs.Buffers)*2))
		combineBuffersPtr = &combineBuffers
	}

	defer func() {
		for index := range combineBuffers {
			combineBuffers[index] = nil // avoid memory leak
		}
		buffs.Buffers = nil
		netBuffersPool.Put(combineBuffersPtr)
	}()

	headerBuffers := pbytes.GetLen(ws.MaxHeaderSize * len(buffs.Indexes))
	defer pbytes.Put(headerBuffers)

	var i = 0
	var sendBytes int64
	for index, j := range buffs.Indexes {

		pkt := buffs.Buffers[i:j]
		i = j

		// count of raw message size
		for _, b := range pkt {
			sendBytes += int64(len(b))
		}

		// pack packet header
		var offset = index * ws.MaxHeaderSize
		var header = headerBuffers[offset : offset+ws.MaxHeaderSize]
		var hn = packHeader(header, t.opCode, true, t.state, func(mask [4]byte) int64 {
			var length int64
			for _, buf := range pkt {
				if t.state.ClientSide() {
					ws.Cipher(buf, mask, int(length))
				}
				length += int64(len(buf))
			}
			return length
		})

		// header
		combineBuffers = append(combineBuffers, header[:hn])
		// payload
		combineBuffers = append(combineBuffers, pkt...)
	}

	// prepare to writev
	buffs.Buffers = combineBuffers
	buffs.Indexes = nil

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	n, err := t.Transport.Writev(buffs)
	if nil == err {
		// return the write bytes without header size
		n = sendBytes
	}
	return n, err
}

func (t *websocketTransport) WriteClose(code int, reason string) (err error) {
	closeFrame := ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusCode(code), reason))

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	if err = ws.WriteFrame(t.Transport, closeFrame); nil == err {
		err = t.Transport.Flush()
	}
	return err
}

func (t *websocketTransport) Flush() error {
	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()
	return t.Transport.Flush()
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
		binary.BigEndian.PutUint32(frame.Header.Mask[:], rand.Uint32())
	}

	for _, buf := range buffs {
		if frame.Header.Masked {
			ws.Cipher(buf, frame.Header.Mask, int(frame.Header.Length))
		}
		frame.Header.Length += int64(len(buf))
	}

	return
}

func packHeader(bts []byte, op ws.OpCode, fin bool, state ws.State, getLen func(mask [4]byte) int64) int {
	const (
		bit0  = 0x80
		len7  = int64(125)
		len16 = int64(^(uint16(0)))
		len64 = int64(^(uint64(0)) >> 1)

		rsv = 0
	)

	var length int64
	var mask [4]byte

	// for client side
	if state.ClientSide() {
		binary.BigEndian.PutUint32(mask[:], rand.Uint32())
	}

	length = getLen(mask)

	if fin {
		bts[0] |= bit0
	}
	bts[0] |= rsv << 4
	bts[0] |= byte(op)

	var n int
	switch {
	case length <= len7:
		bts[1] = byte(length)
		n = 2

	case length <= len16:
		bts[1] = 126
		binary.BigEndian.PutUint16(bts[2:4], uint16(length))
		n = 4

	case length <= len64:
		bts[1] = 127
		binary.BigEndian.PutUint64(bts[2:10], uint64(length))
		n = 10

	default:
		panic(ws.ErrHeaderLengthUnexpected)
	}

	if state.ClientSide() {
		bts[1] |= bit0
		n += copy(bts[n:], mask[:])
	}
	return n
}
