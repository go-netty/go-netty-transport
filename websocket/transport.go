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
	"strconv"
	"sync"

	"github.com/go-netty/go-netty-transport/websocket/internal/wsutils"
	"github.com/go-netty/go-netty/transport"
	"github.com/go-netty/go-netty/utils/pool/pbuffer"
	"github.com/go-netty/go-netty/utils/pool/pbytes"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
	kpflate "github.com/klauspost/compress/flate"
)

type websocketTransport struct {
	transport.Transport
	options     *Options
	state       ws.State  // StateClientSide or StateServerSide
	opCode      ws.OpCode // OpText or OpBinary
	request     *http.Request
	reader      *wsutils.FrameReader
	msgReader   io.Reader
	writeLocker sync.Mutex
	// negotiated compression params for this connection
	negotiated struct {
		enabled             bool
		clientNoContextTake bool
		serverNoContextTake bool
		clientMaxWindowBits int
		serverMaxWindowBits int
	}
	// persistent flate instances (used when context takeover is allowed)
	persistentFlateReader *wsutils.FlateReader
	persistentFlateWriter *wsutils.FlateWriter
}

func newWebsocketTransport(conn net.Conn, wsOptions *Options, client bool, request *http.Request, hs ws.Handshake) (*websocketTransport, error) {

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
	t.reader = &wsutils.FrameReader{
		Source:          t.Transport,
		State:           t.state | ws.StateExtended,
		CheckUTF8:       wsOptions.CheckUTF8,
		SkipHeaderCheck: false,
		MaxFrameSize:    wsOptions.MaxFrameSize,
		OnIntermediate:  wsutils.ControlFrameHandler(t.Transport, &t.writeLocker, t.state),
		GetFlateReader: func(reader io.Reader) *wsutils.FlateReader {
			flateReader := t.options.flateReaderPool.Get().(*wsutils.FlateReader)
			flateReader.Reset(reader)
			return flateReader
		},
		PutFlateReader: func(reader *wsutils.FlateReader) {
			reader.Reset(nil)
			t.options.flateReaderPool.Put(reader)
		},
	}

	// If handshake response includes negotiated extensions, parse them.
	if len(hs.Extensions) > 0 {
		parsePerMessageDeflate(hs, &t.negotiated)
	}

	// If compression is enabled (globally) and negotiated on this connection,
	// configure reader/writer behavior. We add a RecvExtension that clears
	// RSV1 so subsequent processing won't see extension bits.
	if t.options.CompressEnabled && t.negotiated.enabled {
		t.reader.Extensions = []wsutil.RecvExtension{perMessageDeflateRecvExt{}}

		// If peer allows context takeover (peer is sender of compressed frames),
		// try to keep a persistent flate reader to reuse between messages.
		peerNoCtx := false
		if t.state.ServerSide() {
			// peer is client
			peerNoCtx = t.negotiated.clientNoContextTake
		} else {
			// peer is server
			peerNoCtx = t.negotiated.serverNoContextTake
		}

		if !peerNoCtx {
			// reuse a flate reader for multiple messages
			fr := t.options.flateReaderPool.Get().(*wsutils.FlateReader)
			// initialize with nil; GetFlateReader will Reset with real reader later
			fr.Reset(nil)
			t.persistentFlateReader = fr
			// override Get/Put to use persistent reader and avoid putting it back
			t.reader.GetFlateReader = func(reader io.Reader) *wsutils.FlateReader {
				t.persistentFlateReader.Reset(reader)
				return t.persistentFlateReader
			}
			t.reader.PutFlateReader = func(reader *wsutils.FlateReader) {
				// no-op: do not return persistent reader to pool now
			}
		}

		// For writer side: if we are allowed to keep context takeover for our
		// outgoing messages, create persistent writer to reuse; otherwise use pool per message.
		ourNoCtx := false
		if t.state.ServerSide() {
			// we are server => our side corresponds to server params
			ourNoCtx = t.negotiated.serverNoContextTake
		} else {
			ourNoCtx = t.negotiated.clientNoContextTake
		}
		if !ourNoCtx {
			// create persistent writer
			// If negotiated max window bits for our side is present, use
			// klauspost's flate writer with the requested window size.
			ourWindowBits := 0
			if t.state.ServerSide() {
				ourWindowBits = t.negotiated.serverMaxWindowBits
			} else {
				ourWindowBits = t.negotiated.clientMaxWindowBits
			}

			if ourWindowBits > 0 {
				windowSize := 1 << uint(ourWindowBits)
				fw := wsutils.NewFlateWriter(nil, func(w io.Writer) wsflate.Compressor {
					wp, _ := kpflate.NewWriterWindow(w, windowSize)
					return wp
				})
				t.persistentFlateWriter = fw
			} else {
				fw := t.options.flateWriterPool.Get().(*wsutils.FlateWriter)
				fw.Reset(nil)
				t.persistentFlateWriter = fw
			}
		}
	}

	return t, nil
}

func (t *websocketTransport) Route() string {
	return t.request.URL.Path
}

func (t *websocketTransport) Header() http.Header {
	return t.request.Header
}

func (t *websocketTransport) Request() *http.Request {
	return t.request
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

	if compressed := t.options.CompressEnabled && t.negotiated.enabled && int64(len(p)) >= t.options.CompressThreshold; compressed {
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
		wsutils.FastCipher(p, mask, 0)
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
	var flateWriter *wsutils.FlateWriter
	defer func() {
		if nil != payloadBuffer {
			pbuffer.Put(payloadBuffer)
		}

		// if writer is persistent, do not put it back here; persistent writer
		// kept in t.persistentFlateWriter and will be returned on Close.
		if nil != flateWriter && flateWriter != t.persistentFlateWriter {
			flateWriter.Reset(nil)
			t.options.flateWriterPool.Put(flateWriter)
		}
	}()

	// raw payload length
	var dataSize = len(p)

	// raw payload length
	var payloadLength = int64(dataSize)
	var compressed bool

	// payload compression
	if compressed = t.options.CompressEnabled && payloadLength >= t.options.CompressThreshold; compressed {
		payloadBuffer = pbuffer.Get(int(payloadLength))

		if t.persistentFlateWriter != nil {
			flateWriter = t.persistentFlateWriter
			flateWriter.Reset(payloadBuffer)
		} else {
			flateWriter = t.options.flateWriterPool.Get().(*wsutils.FlateWriter)
			flateWriter.Reset(payloadBuffer)
		}

		if _, err = flateWriter.Write(p); nil == err {
			err = flateWriter.Flush()
		}
		// compressed length
		payloadLength = int64(payloadBuffer.Len())
		// compressed data
		p = payloadBuffer.Bytes()
	}

	// If compression failed, return the error
	if err != nil {
		return 0, err
	}

	var mask [4]byte
	// xor bytes if client side
	if t.state.ClientSide() {
		binary.BigEndian.PutUint32(mask[:], rand.Uint32())
		wsutils.FastCipher(p, mask, 0)
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

	var writeSize int64
	for _, pkt := range buffs {
		if _, err := t.Write(pkt); nil != err {
			return 0, err
		}
		writeSize += int64(len(pkt))
	}

	return writeSize, nil
}

func (t *websocketTransport) WriteClose(code int, reason string) (err error) {
	closeFrame := ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusCode(code), reason))

	// xor bytes if client side
	if t.state.ClientSide() {
		closeFrame.Header.Masked = true
		binary.BigEndian.PutUint32(closeFrame.Header.Mask[:], rand.Uint32())
		wsutils.FastCipher(closeFrame.Payload, closeFrame.Header.Mask, 0)
	}

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

// Close closes the underlying transport and releases any persistent flate
// instances back to pools.
func (t *websocketTransport) Close() error {
	// release persistent flate reader
	if t.persistentFlateReader != nil {
		t.persistentFlateReader.Reset(nil)
		t.options.flateReaderPool.Put(t.persistentFlateReader)
		t.persistentFlateReader = nil
	}
	if t.persistentFlateWriter != nil {
		t.persistentFlateWriter.Reset(nil)
		t.options.flateWriterPool.Put(t.persistentFlateWriter)
		t.persistentFlateWriter = nil
	}
	return t.Transport.Close()
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

// parsePerMessageDeflate parses Sec-WebSocket-Extensions header values and fills negotiated params
func parsePerMessageDeflate(hs ws.Handshake, out *struct {
	enabled             bool
	clientNoContextTake bool
	serverNoContextTake bool
	clientMaxWindowBits int
	serverMaxWindowBits int
}) {
	if out == nil {
		return
	}
	for _, opt := range hs.Extensions {
		if string(opt.Name) != "permessage-deflate" {
			continue
		}
		out.enabled = true
		// parameters could be present without values
		if _, ok := opt.Parameters.Get("client_no_context_takeover"); ok {
			out.clientNoContextTake = true
		}
		if _, ok := opt.Parameters.Get("server_no_context_takeover"); ok {
			out.serverNoContextTake = true
		}
		if v, ok := opt.Parameters.Get("client_max_window_bits"); ok {
			if len(v) > 0 {
				if n, err := strconv.Atoi(string(v)); err == nil {
					out.clientMaxWindowBits = n
				}
			}
		}
		if v, ok := opt.Parameters.Get("server_max_window_bits"); ok {
			if len(v) > 0 {
				if n, err := strconv.Atoi(string(v)); err == nil {
					out.serverMaxWindowBits = n
				}
			}
		}
		// we consider only first permessage-deflate extension occurrence
		return
	}
}

// perMessageDeflateRecvExt clears RSV1 bit on received headers so that
// subsequent processing won't see extension bits set.
type perMessageDeflateRecvExt struct{}

func (perMessageDeflateRecvExt) UnsetBits(hdr ws.Header) (ws.Header, error) {
	// clear RSV1 bit
	hdr.Rsv &^= 0x4
	return hdr, nil
}
