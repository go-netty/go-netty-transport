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
	"crypto/tls"
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"

	"github.com/go-netty/go-netty-transport/websocket/internal/xwsflate"
	"github.com/go-netty/go-netty-transport/websocket/internal/xwsutil"
	"github.com/go-netty/go-netty/transport"
	"github.com/go-netty/go-netty/utils"
	"github.com/go-netty/go-netty/utils/pool/pbuffer"
	"github.com/go-netty/go-netty/utils/pool/pbytes"
	"github.com/gobwas/ws"
)

type websocketTransport struct {
	transport.Transport
	options     *Options
	state       ws.State  // StateClientSide or StateServerSide
	opCode      ws.OpCode // OpText or OpBinary
	route       string
	headers     http.Header
	reader      *xwsutil.Reader
	msgReader   io.Reader
	writeLocker sync.Mutex
}

func newWebsocketTransport(conn net.Conn, route string, wsOptions *Options, client bool, headers http.Header) (*websocketTransport, error) {

	var err error
	switch t := conn.(type) {
	case *net.TCPConn:
		err = t.SetNoDelay(wsOptions.NoDelay)
	case *tls.Conn:
		if tc, ok := t.NetConn().(*net.TCPConn); ok {
			err = tc.SetNoDelay(wsOptions.NoDelay)
		}
	}

	if nil != err {
		return nil, err
	}

	t := &websocketTransport{
		Transport: transport.NewTransport(conn, wsOptions.ReadBufferSize, wsOptions.WriteBufferSize),
		options:   wsOptions,
		route:     route,
		headers:   headers,
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
		OnIntermediate:  xwsutil.ControlFrameHandler(t.Transport, &t.writeLocker, t.state),
		GetFlateReader: func(reader io.Reader) *xwsflate.Reader {
			flateReader := t.options.flateReaderPool.Get().(*xwsflate.Reader)
			flateReader.Reset(reader)
			return flateReader
		},
		PutFlateReader: func(reader *xwsflate.Reader) {
			reader.Reset(nil)
			t.options.flateReaderPool.Put(reader)
		},
	}

	return t, nil
}

func (t *websocketTransport) Route() string {
	return t.route
}

func (t *websocketTransport) Header() http.Header {
	return t.headers
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

	if nil == t.msgReader {
		for {
			hdr, err := t.reader.NextFrame()
			if nil != err {
				// connection closed
				if io.EOF == err {
					err = io.ErrUnexpectedEOF
				}
				return 0, err
			}

			if hdr.OpCode.IsControl() {
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
				// close the connection because it has received a type of data it cannot accept
				_ = t.WriteClose(int(ws.StatusUnsupportedData), "unsupported data type")
				continue
			}

			t.msgReader = t.reader
			break
		}
	}

	n, err := t.msgReader.Read(p)
	if io.EOF == err {
		// all of message bytes were read
		t.msgReader = nil
	}

	return n, err
}

func (t *websocketTransport) Write(p []byte) (n int, err error) {

	if compressed := t.options.CompressEnabled && int64(len(p)) >= t.options.CompressThreshold; compressed {
		return t.writeCompress(p)
	}

	packetBuffers := pbytes.Get(ws.MaxHeaderSize + len(p))
	defer pbytes.Put(packetBuffers)

	// raw payload length
	var dataSize = len(p)
	var mask [4]byte

	// xor bytes if client side
	if t.state.ClientSide() {
		binary.BigEndian.PutUint32(mask[:], rand.Uint32())
		xwsutil.FastCipher(p, mask, 0)
	}

	// pack websocket header
	var hn, e = t.packHeader((*packetBuffers)[:ws.MaxHeaderSize], true, mask, int64(dataSize), false)
	// pack header failed
	if nil != e {
		return 0, e
	}

	// copy payload
	hn += copy((*packetBuffers)[hn:hn+len(p)], p)

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()

	// write websocket frame
	if _, err = t.Transport.Write((*packetBuffers)[:hn]); nil == err {
		// return data-size
		n = dataSize
	}

	return
}

func (t *websocketTransport) writeCompress(p []byte) (n int, err error) {

	var payloadBuffer *bytes.Buffer
	var flateWriter *xwsflate.Writer
	defer func() {
		if nil != payloadBuffer {
			pbuffer.Put(payloadBuffer)
		}

		if nil != flateWriter {
			flateWriter.Reset(nil)
			t.options.flateWriterPool.Put(flateWriter)
		}
	}()

	// raw payload length
	var dataSize = len(p)
	var mask [4]byte

	// xor bytes if client side
	if t.state.ClientSide() {
		binary.BigEndian.PutUint32(mask[:], rand.Uint32())
		xwsutil.FastCipher(p, mask, 0)
	}

	// raw payload length
	var payloadLength = int64(dataSize)
	var compressed bool

	// payload compression
	if compressed = t.options.CompressEnabled && payloadLength >= t.options.CompressThreshold; compressed {
		payloadBuffer = pbuffer.Get(int(payloadLength))
		flateWriter = t.options.flateWriterPool.Get().(*xwsflate.Writer)
		flateWriter.Reset(payloadBuffer)

		if _, err = flateWriter.Write(p); nil == err {
			err = flateWriter.Close()
		}
		// compressed length
		payloadLength = int64(payloadBuffer.Len())
		// compressed data
		p = payloadBuffer.Bytes()
	}

	packetBuffers := pbytes.Get(ws.MaxHeaderSize + len(p))
	defer pbytes.Put(packetBuffers)

	// pack websocket header
	var hn, e = t.packHeader((*packetBuffers)[:ws.MaxHeaderSize], true, mask, payloadLength, compressed)

	// pack header failed
	if nil != e {
		return 0, e
	}

	// copy payload
	hn += copy((*packetBuffers)[hn:hn+len(p)], p)

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()

	// write websocket frame
	if _, err = t.Transport.Write((*packetBuffers)[:hn]); nil == err {
		// return data-size
		n = dataSize
	}
	return
}

func (t *websocketTransport) Writev(buffs transport.Buffers) (int64, error) {

	var i = 0
	var writeSize int64
	for _, j := range buffs.Indexes {

		pkt := buffs.Buffers[i:j]
		i = j

		switch len(pkt) {
		case 1:
			writeSize += int64(len(pkt[0]))
			if _, err := t.Write(pkt[0]); nil != err {
				return 0, err
			}
		default:
			pktSize := utils.CountOf(pkt)
			dataBuffer := pbuffer.Get(int(pktSize))
			for _, p := range pkt {
				dataBuffer.Write(p)
			}
			writeSize += int64(dataBuffer.Len())

			if _, err := t.Write(dataBuffer.Bytes()); nil != err {
				pbuffer.Put(dataBuffer)
				return 0, err
			}
			pbuffer.Put(dataBuffer)
		}
	}

	return writeSize, nil
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

func (t *websocketTransport) packHeader(bts []byte, fin bool, mask [4]byte, length int64, compressed bool) (n int, err error) {
	const (
		bit0  = 0x80
		bit1  = 0x40
		len7  = int64(125)
		len16 = int64(^(uint16(0)))
		len64 = int64(^(uint64(0)) >> 1)
	)

	bts[0] = byte(t.opCode)

	if fin {
		bts[0] |= bit0
	}

	if compressed {
		bts[0] |= bit1
	}

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
		err = ws.ErrHeaderLengthUnexpected
	}

	if t.state.ClientSide() {
		bts[1] |= bit0
		n += copy(bts[n:], mask[:])
	}
	return
}
