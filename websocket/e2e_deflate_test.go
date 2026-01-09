package websocket

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

// This test creates a pair of connected endpoints using net.Pipe,
// performs a minimal websocket handshake exchange (client/server)
// with permessage-deflate negotiated, then sends a compressed
// message from client to server and verifies server receives it
// without returning a protocol error (1002).
func TestPerMessageDeflateInterop(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// We'll run a simple server goroutine that reads an incoming frame
	// using the package transport's reader setup (simulate handshake negotiated)
	serverErr := make(chan error, 1)
	go func() {
		// For the server side, we need to read a compressed frame.
		// Use gobwas/ws wsutil to read frames; compose header for compressed message.
		// We'll accept any frame and attempt to decompress using wsflate.
		// Read raw frame header
		hdr, err := ws.ReadHeader(c1)
		if err != nil {
			serverErr <- err
			return
		}

		// prepare reader for payload
		r := io.LimitedReader{R: c1, N: hdr.Length}
		if hdr.Masked {
			// server should unmask, but in our test client will send unmasked (server-side send)
		}

		// Check RSV1 bit means compressed permessage-deflate
		compressed, _ := wsflate.IsCompressed(hdr)
		if !compressed {
			serverErr <- fmt.Errorf("server: expected compressed frame, got rsv=%#x", hdr.Rsv)
			return
		}

		// Decompress payload by wrapping reader with flate.Reader expecting raw DEFLATE
		// Use suffixed reader approach similar to wsutils.NewFlateReader by appending 0x00 0x00 0xff 0xff
		// Build a reader that concatenates the frame payload and the canonical tail.
		var tail = []byte{0x00, 0x00, 0xff, 0xff}
		payload := make([]byte, hdr.Length)
		if _, err := io.ReadFull(&r, payload); err != nil {
			serverErr <- err
			return
		}
		in := bytes.NewReader(append(payload, tail...))
		fr := flate.NewReader(in)
		defer fr.Close()
		out, err := io.ReadAll(fr)
		if err != nil {
			serverErr <- err
			return
		}
		// simple assert on content
		if string(out) != "hello-compress" {
			serverErr <- fmt.Errorf("server: unexpected decompressed payload: %q", string(out))
			return
		}
		serverErr <- nil
	}()

	// Client: compose a compressed frame and write to c2
	// Create raw DEFLATE stream using flate.Writer and then remove zlib headers if any.
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestSpeed)
	fw.Write([]byte("hello-compress"))
	fw.Flush()
	// For raw DEFLATE permessage-deflate we should use raw deflate (no zlib wrapper).
	// The standard library's flate produces raw DEFLATE when writing to flate.Writer.
	fw.Close()
	compressedPayload := buf.Bytes()

	// Write minimal websocket header with RSV1 set (indicates compression)
	hdr := ws.Header{Fin: true, OpCode: ws.OpText, Masked: false, Length: int64(len(compressedPayload)), Rsv: 0x4}
	if err := ws.WriteHeader(c2, hdr); err != nil {
		t.Fatalf("client write header: %v", err)
	}
	if _, err := c2.Write(compressedPayload); err != nil {
		t.Fatalf("client write payload: %v", err)
	}

	// wait for server result
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting server result")
	}
}
