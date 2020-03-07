/*
 *  Copyright 2019 the go-netty project
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *       https://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

package quic

import (
	"context"
	"crypto/tls"
	"github.com/go-netty/go-netty/transport"
)

// DefaultOptions default quic options
var DefaultOptions = &Options{}

// Options to define the quic
type Options struct {
	TLS *tls.Config
}

var contextKey = struct{ key string }{"go-netty-transport-quic-options"}

// WithOptions to wrap the quic options
func WithOptions(option *Options) transport.Option {
	return func(options *transport.Options) error {
		options.Context = context.WithValue(options.Context, contextKey, option)
		return nil
	}
}

// FromContext to unwrap the quic options
func FromContext(ctx context.Context, def *Options) *Options {
	if v, ok := ctx.Value(contextKey).(*Options); ok {
		return v
	}
	return def
}
