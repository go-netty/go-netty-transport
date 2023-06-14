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
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"sync"

	"github.com/go-netty/go-netty-transport/websocket/internal/xwsflate"
	"github.com/go-netty/go-netty-transport/websocket/internal/xwsutil"
	"github.com/go-netty/go-netty/transport"
	"github.com/go-netty/go-netty/utils"
	"github.com/go-netty/go-netty/utils/pool/pbuffer"
	"github.com/go-netty/go-netty/utils/pool/pbytes"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

type websocketTransport struct {
	transport.Transport
	options     *Options
	state       ws.State  // StateClientSide or StateServerSide
	opCode      ws.OpCode // OpText or OpBinary
	route       string
	reader      *xwsutil.Reader
	msgReader   io.Reader
	writeLocker sync.Mutex
}

func newWebsocketTransport(conn net.Conn, route string, wsOptions *Options, client bool) (*websocketTransport, error) {
	t := &websocketTransport{
		Transport: transport.NewTransport(conn, wsOptions.ReadBufferSize, wsOptions.WriteBufferSize),
		options:   wsOptions,
		route:     route,
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

	packetBuffers := pbytes.GetLen(ws.MaxHeaderSize + len(p))
	defer pbytes.Put(packetBuffers)

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()

	// raw payload length
	dataSize := len(p)

	// pack websocket header
	var hn, e = t.packHeader(packetBuffers[:ws.MaxHeaderSize], true, func(mask [4]byte) (payloadLength int64, compressed bool, err error) {
		if t.state.ClientSide() {
			ws.Cipher(p, mask, 0)
		}
		// raw payload length
		payloadLength = int64(dataSize)
		return
	})

	// pack header failed
	if nil != e {
		return 0, e
	}

	// copy payload
	hn += copy(packetBuffers[hn:], p)

	// write websocket frame
	if _, err = t.Transport.Write(packetBuffers[:hn]); nil == err {
		// return data-size
		n = dataSize
	}

	return
}

func (t *websocketTransport) writeCompress(p []byte) (n int, err error) {

	headerBuffers := pbytes.GetLen(ws.MaxHeaderSize)
	defer pbytes.Put(headerBuffers)

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

	t.writeLocker.Lock()
	defer t.writeLocker.Unlock()

	// raw payload length
	dataSize := len(p)

	// pack websocket header
	var hn, e = t.packHeader(headerBuffers, true, func(mask [4]byte) (payloadLength int64, compressed bool, err error) {
		if t.state.ClientSide() {
			ws.Cipher(p, mask, 0)
		}

		// raw payload length
		payloadLength = int64(dataSize)
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
		return
	})

	// pack header failed
	if nil != e {
		return 0, e
	}

	// write frame header
	if _, err = t.Transport.Write(headerBuffers[:hn]); nil == err {
		// write payload body
		if _, err = t.Transport.Write(p); nil == err {
			// return unpressed data-size
			n = dataSize
		}
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

func (t *websocketTransport) packHeader(bts []byte, fin bool, getLen func(mask [4]byte) (int64, bool, error)) (n int, err error) {
	const (
		bit0  = 0x80
		len7  = int64(125)
		len16 = int64(^(uint16(0)))
		len64 = int64(^(uint64(0)) >> 1)
	)

	var h = ws.Header{Fin: fin, OpCode: t.opCode, Masked: t.state.ClientSide()}
	if h.Masked {
		binary.BigEndian.PutUint32(h.Mask[:], rand.Uint32())
	}

	var compressed bool
	if h.Length, compressed, err = getLen(h.Mask); nil != err {
		return
	}

	if compressed {
		r1, r2, r3 := ws.RsvBits(h.Rsv)
		if r1 {
			err = wsflate.ErrUnexpectedCompressionBit
			return
		}
		if h.OpCode.IsData() && h.OpCode != ws.OpContinuation {
			h.Rsv = ws.Rsv(true, r2, r3)
		}
	}

	if h.Fin {
		bts[0] |= bit0
	}
	bts[0] |= h.Rsv << 4
	bts[0] |= byte(h.OpCode)

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
		err = ws.ErrHeaderLengthUnexpected
	}

	if h.Masked {
		bts[1] |= bit0
		n += copy(bts[n:], h.Mask[:])
	}
	return
}
