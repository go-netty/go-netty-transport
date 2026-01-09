package websocket

import (
	"net"
	"net/http"
	"testing"

	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

func TestParsePerMessageDeflate(t *testing.T) {
	var hs ws.Handshake
	hs.Extensions = []httphead.Option{
		(wsflate.Parameters{
			ClientNoContextTakeover: true,
			ServerNoContextTakeover: true,
			ClientMaxWindowBits:     8,
			ServerMaxWindowBits:     10,
		}).Option(),
	}
	var out struct {
		enabled             bool
		clientNoContextTake bool
		serverNoContextTake bool
		clientMaxWindowBits int
		serverMaxWindowBits int
	}
	parsePerMessageDeflate(hs, &out)
	if !out.enabled {
		t.Fatalf("expected enabled")
	}
	if !out.clientNoContextTake || !out.serverNoContextTake {
		t.Fatalf("expected no_context_takeover flags set: %+v", out)
	}
	if out.clientMaxWindowBits != 8 {
		t.Fatalf("unexpected client max window bits: %d", out.clientMaxWindowBits)
	}
	if out.serverMaxWindowBits != 10 {
		t.Fatalf("unexpected server max window bits: %d", out.serverMaxWindowBits)
	}
}

func TestNewWebsocketTransport_PersistentWriterCreatedWithMaxWindowBits(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	wsOptions := DefaultOptions.Apply()
	wsOptions.CompressEnabled = true

	// Prepare handshake with permessage-deflate and server_max_window_bits=9
	hs := ws.Handshake{
		Extensions: []httphead.Option{(wsflate.Parameters{ServerMaxWindowBits: 9}).Option()},
	}

	req := &http.Request{Method: "GET", URL: nil, Header: http.Header{}}
	// create transport as server side
	tp, err := newWebsocketTransport(c1, wsOptions, false, req, hs)
	if err != nil {
		t.Fatalf("newWebsocketTransport error: %v", err)
	}
	defer tp.Close()

	wt := tp
	if wt.persistentFlateWriter == nil {
		t.Fatalf("expected persistent flate writer to be created when max_window_bits negotiated")
	}
}

func TestNewWebsocketTransport_PersistentReaderCreatedWhenContextTakeoverAllowed(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	wsOptions := DefaultOptions.Apply()
	wsOptions.CompressEnabled = true

	// Prepare handshake with permessage-deflate, no no_context_takeover flags
	hs := ws.Handshake{
		Extensions: []httphead.Option{(wsflate.Parameters{}).Option()},
	}

	req := &http.Request{Method: "GET", URL: nil, Header: http.Header{}}
	tp, err := newWebsocketTransport(c1, wsOptions, false, req, hs)
	if err != nil {
		t.Fatalf("newWebsocketTransport error: %v", err)
	}
	defer tp.Close()

	if tp.persistentFlateReader == nil {
		t.Fatalf("expected persistent flate reader to be created when peer allows context takeover")
	}
}

func TestNewWebsocketTransport_NoPersistentWriterWhenNoContextTakeover(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	wsOptions := DefaultOptions.Apply()
	wsOptions.CompressEnabled = true

	// Prepare handshake with permessage-deflate and server_no_context_takeover
	hs := ws.Handshake{
		Extensions: []httphead.Option{(wsflate.Parameters{ServerNoContextTakeover: true}).Option()},
	}

	req := &http.Request{Method: "GET", URL: nil, Header: http.Header{}}
	tp, err := newWebsocketTransport(c1, wsOptions, false, req, hs)
	if err != nil {
		t.Fatalf("newWebsocketTransport error: %v", err)
	}
	defer tp.Close()

	if tp.persistentFlateWriter != nil {
		t.Fatalf("expected no persistent flate writer when server_no_context_takeover negotiated")
	}
}
