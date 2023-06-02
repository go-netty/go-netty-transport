/*
 *  Copyright 2020 the go-netty project
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

package tls

import (
	"context"
	"crypto/tls"

	"github.com/go-netty/go-netty/transport"
)

// DefaultOptions default tls options
var DefaultOptions = &Options{
	TLS: &tls.Config{},
}

// Options to define the tls
type Options struct {
	CertFile        string      `json:"certFile"`
	KeyFile         string      `json:"keyFile"`
	ReadBufferSize  int         `json:"readBufferSize"`
	WriteBufferSize int         `json:"writeBufferSize"`
	TLS             *tls.Config `json:"-"`
}

func (o *Options) Apply() *Options {
	if nil == o.TLS {
		o.TLS = &tls.Config{}
	}

	if "" != o.CertFile && "" != o.KeyFile {
		if cer, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile); nil != err {
			panic(err)
		} else {
			o.TLS.Certificates = []tls.Certificate{cer}
		}
	}

	return o
}

type contextKey struct{}

// WithOptions to wrap the tls options
func WithOptions(option *Options) transport.Option {
	return func(options *transport.Options) error {
		options.Context = context.WithValue(options.Context, contextKey{}, option.Apply())
		return nil
	}
}

// FromContext to unwrap the tls options
func FromContext(ctx context.Context, def *Options) *Options {
	if v, ok := ctx.Value(contextKey{}).(*Options); ok {
		return v
	}
	return def
}
