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
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-netty/go-netty/transport"
)

// New websocket transport factory
func New() transport.Factory {
	return new(websocketFactory)
}

type websocketFactory struct{}

func (*websocketFactory) Schemes() transport.Schemes {
	return transport.Schemes{"ws", "wss"}
}

func (w *websocketFactory) Connect(options *transport.Options) (transport.Transport, error) {

	if err := w.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	wsOptions := FromContext(options.Context, DefaultOptions)

	wsDialer := wsOptions.Dialer // copy dialer

	headers := make(http.Header)
	wsDialer.OnHeader = func(key, value []byte) (err error) {
		headers.Add(string(key), string(value))
		return nil
	}

	u := &url.URL{Scheme: options.Address.Scheme, Host: options.Address.Host, Path: options.Address.Path}
	conn, _, _, err := wsDialer.Dial(options.Context, u.String())
	if nil != err {
		return nil, err
	}

	request := &http.Request{
		Method:     http.MethodGet,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     headers,
		Body:       http.NoBody,
		Host:       u.Host,
		RemoteAddr: conn.RemoteAddr().String(),
		RequestURI: u.RequestURI(),
	}

	tt, err := newWebsocketTransport(conn, wsOptions, true, request)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}
	return tt, nil
}

func (w *websocketFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := w.Schemes().FixScheme(options.Address); nil != err {
		return nil, err
	}

	listen, err := net.Listen("tcp", options.AddressWithoutHost())
	if nil != err {
		return nil, err
	}

	wsOptions := FromContext(options.Context, DefaultOptions)
	// websocket acceptor backlog size
	backlog := wsOptions.Backlog
	if backlog < 64 {
		backlog = 64
	}

	wa := &wsAcceptor{
		wsOptions:    wsOptions,
		incoming:     make(chan acceptEvent, backlog),
		httpServer:   &http.Server{Addr: listen.Addr().String(), Handler: wsOptions.ServeMux, TLSConfig: wsOptions.TLS},
		closedSignal: make(chan struct{}),
	}

	var routers = []string{options.Address.Path}
	if len(wa.wsOptions.Routers) > 0 {
		routers = wa.wsOptions.Routers
	}

	for _, router := range routers {
		wa.wsOptions.ServeMux.HandleFunc(router, wa.upgradeHTTP)
	}

	errorChan := make(chan error, 1)

	go func() {
		switch options.Address.Scheme {
		case "ws":
			errorChan <- wa.httpServer.Serve(listen)
		case "wss":
			errorChan <- wa.httpServer.ServeTLS(listen, "", "")
		}
	}()

	select {
	case err = <-errorChan:
		return nil, err
	case <-time.After(time.Second):
		// temporary plan
		// waiting for server initialization
		return wa, nil
	}
}

type acceptEvent struct {
	conn    net.Conn
	request *http.Request
}

type wsAcceptor struct {
	httpServer   *http.Server
	incoming     chan acceptEvent
	closedSignal chan struct{}
	wsOptions    *Options
}

func (w *wsAcceptor) upgradeHTTP(writer http.ResponseWriter, request *http.Request) {

	conn, _, _, err := w.wsOptions.Upgrader.Upgrade(request, writer)
	if nil != err {
		if nil != conn {
			_ = conn.Close()
		}
		return
	}

	select {
	case <-w.closedSignal:
		_ = conn.Close()
		return
	case w.incoming <- acceptEvent{conn: conn, request: request}:
		// post to acceptor
	}
}

func (w *wsAcceptor) Accept() (transport.Transport, error) {
	select {
	case ev := <-w.incoming:
		tt, err := newWebsocketTransport(ev.conn, w.wsOptions, false, ev.request)
		if nil != err {
			_ = ev.conn.Close()
			return nil, err
		}
		return tt, nil
	case <-w.closedSignal:
		// close all incoming connections
		for {
			select {
			case ev := <-w.incoming:
				_ = ev.conn.Close()
			default:
				return nil, errors.New("ws acceptor closed")
			}
		}
	}
}

func (w *wsAcceptor) Close() error {

	select {
	case <-w.closedSignal:
		return nil
	default:
		close(w.closedSignal)

		if w.httpServer != nil {
			return w.httpServer.Close()
		}
	}

	return nil
}
