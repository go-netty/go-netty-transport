package websocket

import (
	"compress/flate"
	"io"
	"net"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

// Regression test: if CompressEnabled=true globally but permessage-deflate is
// NOT negotiated for this connection (hs.Extensions empty), the transport
// must NOT send compressed frames (RSV1 should be unset).
func TestNoCompressWhenNotNegotiated(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// prepare options with global compression enabled
	opts := *DefaultOptions
	opts.CompressEnabled = true
	opts.CompressLevel = flate.BestSpeed
	opts.CompressThreshold = 1
	optsPtr := opts.Apply()

	hs := ws.Handshake{} // no extensions negotiated
	// create server-side transport (server sends to client)
	tsrv, err := newWebsocketTransport(serverConn, optsPtr, false, nil, hs)
	if err != nil {
		t.Fatalf("failed to create websocket transport: %v", err)
	}
	defer tsrv.Close()

	// write a payload that would normally be above threshold
	payload := []byte("this-message-is-long-enough-to-trigger-compression-by-threshold-if-enabled")
	go func() {
		if _, err := tsrv.Write(payload); err != nil {
			// signal test failure by closing connection
			_ = serverConn.Close()
		}
	}()

	// read header from client side
	hdr, err := ws.ReadHeader(clientConn)
	if err != nil {
		t.Fatalf("client failed to read frame header: %v", err)
	}

	compressed, err := wsflate.IsCompressed(hdr)
	if err != nil {
		t.Fatalf("failed to check compressed header: %v", err)
	}
	if compressed {
		t.Fatalf("frame was compressed despite permessage-deflate not negotiated")
	}

	// read payload to drain connection
	if _, err := io.CopyN(io.Discard, clientConn, hdr.Length); err != nil {
		t.Fatalf("failed to read payload: %v", err)
	}
}
