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
	"context"
	"net/http"

	"github.com/go-netty/go-netty/transport"
	"github.com/gobwas/ws"
)

// DefaultOptions default websocket options
var DefaultOptions = &Options{
	Dialer:   ws.DefaultDialer,
	Upgrader: ws.DefaultHTTPUpgrader,
	ServeMux: http.DefaultServeMux,
}

// Options to define the websocket
type Options struct {
	Cert     string          `json:"cert"`
	Key      string          `json:"key"`
	Binary   bool            `json:"binary,string"`
	Routers  []string        `json:"routers"`
	Dialer   ws.Dialer       `json:"-"`
	Upgrader ws.HTTPUpgrader `json:"-"`
	ServeMux *http.ServeMux  `json:"-"`
}

type contextKey struct{}

// WithOptions to wrap the websocket options
func WithOptions(option *Options) transport.Option {
	return func(options *transport.Options) error {
		options.Context = context.WithValue(options.Context, contextKey{}, option)
		return nil
	}
}

// FromContext to unwrap the websocket options
func FromContext(ctx context.Context, def *Options) *Options {
	if v, ok := ctx.Value(contextKey{}).(*Options); ok {
		return v
	}
	return def
}
