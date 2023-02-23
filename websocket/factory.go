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
	"github.com/gobwas/ws"
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

	if err := w.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	wsOptions := FromContext(options.Context, DefaultOptions)

	u := url.URL{Scheme: options.Address.Scheme, Host: options.Address.Host, Path: options.Address.Path}
	conn, _, _, err := wsOptions.Dialer.Dial(options.Context, u.String())
	if nil != err {
		return nil, err
	}

	return (&websocketTransport{conn: conn, closer: conn.Close, path: u.Path}).applyOptions(wsOptions, true)
}

func (w *websocketFactory) Listen(options *transport.Options) (transport.Acceptor, error) {

	if err := w.Schemes().FixedURL(options.Address); nil != err {
		return nil, err
	}

	listen, err := net.Listen("tcp", options.AddressWithoutHost())
	if nil != err {
		return nil, err
	}

	wsOptions := FromContext(options.Context, DefaultOptions)

	wa := &wsAcceptor{
		wsOptions:    wsOptions,
		incoming:     make(chan acceptEvent, 128),
		httpServer:   &http.Server{Addr: listen.Addr().String(), Handler: wsOptions.ServeMux},
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
			errorChan <- wa.httpServer.ServeTLS(listen, wa.wsOptions.Cert, wa.wsOptions.Key)
		}
	}()

	select {
	case err := <-errorChan:
		return nil, err
	case <-time.After(time.Second):
		// temporary plan
		// waiting for server initialization
		return wa, nil
	}
}

type acceptEvent struct {
	conn    net.Conn
	closer  func() error
	path    string
	request *http.Request
}

type wsAcceptor struct {
	httpServer   *http.Server
	incoming     chan acceptEvent
	closedSignal chan struct{}
	wsOptions    *Options
}

func (w *wsAcceptor) upgradeHTTP(writer http.ResponseWriter, request *http.Request) {

	conn, _, _, err := ws.UpgradeHTTP(request, writer)
	if conn != nil {
		defer conn.Close()
	}

	if nil != err {
		return
	}

	connCloseSignal := make(chan struct{})

	select {
	case <-w.closedSignal:
		return
	case w.incoming <- acceptEvent{conn: conn, closer: func() error {
		select {
		case <-connCloseSignal:
		default:
			close(connCloseSignal)
		}
		return nil
	}, path: request.URL.Path, request: request}:
	}

	// waiting for connection to close
	<-connCloseSignal
}

func (w *wsAcceptor) Accept() (transport.Transport, error) {

	accept, ok := <-w.incoming
	if !ok {
		return nil, errors.New("server has been closed")
	}

	return (&websocketTransport{conn: accept.conn, closer: accept.closer, path: accept.path, request: accept.request}).applyOptions(w.wsOptions, false)
}

func (w *wsAcceptor) Close() error {

	if nil == w.closedSignal {
		return nil
	}

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
