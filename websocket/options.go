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
	"compress/flate"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"sync"

	"github.com/go-netty/go-netty-transport/websocket/internal/xwsflate"
	"github.com/go-netty/go-netty/transport"
	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

// DefaultOptions default websocket options
var DefaultOptions = (&Options{
	OpCode:            ws.OpText,
	Dialer:            ws.DefaultDialer,
	Upgrader:          ws.DefaultHTTPUpgrader,
	ServeMux:          http.DefaultServeMux,
	Backlog:           128,
	NoDelay:           true,
	CompressLevel:     flate.BestSpeed,
	CompressThreshold: 512,
}).Apply()

// Options to define the websocket
type Options struct {
	CertFile          string          `json:"certFile"`
	KeyFile           string          `json:"keyFile"`
	OpCode            ws.OpCode       `json:"opCode"`
	Routers           []string        `json:"routers"`
	CheckUTF8         bool            `json:"checkUTF8"`
	MaxFrameSize      int64           `json:"maxFrameSize"`
	ReadBufferSize    int             `json:"readBufferSize"`
	WriteBufferSize   int             `json:"writeBufferSize"`
	Backlog           int             `json:"backlog"`
	NoDelay           bool            `json:"nodelay"`
	CompressEnabled   bool            `json:"compressEnabled"`
	CompressLevel     int             `json:"compressLevel"`
	CompressThreshold int64           `json:"compressThreshold"`
	TLS               *tls.Config     `json:"-"`
	Dialer            ws.Dialer       `json:"-"`
	Upgrader          ws.HTTPUpgrader `json:"-"`
	ServeMux          *http.ServeMux  `json:"-"`
	flateReaderPool   sync.Pool
	flateWriterPool   sync.Pool
}

func (o *Options) Apply() *Options {
	o.flateReaderPool.New = func() interface{} {
		return xwsflate.NewReader(nil, func(reader io.Reader) wsflate.Decompressor {
			return flate.NewReader(reader)
		})
	}

	if o.CompressEnabled {
		compressLv := o.CompressLevel
		o.flateWriterPool.New = func() interface{} {
			return xwsflate.NewWriter(nil, func(writer io.Writer) wsflate.Compressor {
				w, _ := flate.NewWriter(writer, compressLv)
				return w
			})
		}

		if nil == o.Upgrader.Negotiate {
			o.Upgrader.Negotiate = func(option httphead.Option) (httphead.Option, error) {
				e := wsflate.Extension{
					Parameters: wsflate.DefaultParameters,
				}
				return e.Negotiate(option)
			}
		}

		if nil == o.Dialer.Extensions {
			o.Dialer.Extensions = []httphead.Option{wsflate.DefaultParameters.Option()}
		}
	}

	if "" != o.CertFile && "" != o.KeyFile {
		if cer, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile); nil != err {
			panic(err)
		} else {
			o.TLS.Certificates = append(o.TLS.Certificates, cer)
		}
	}

	return o
}

type contextKey struct{}

// WithOptions to wrap the websocket options
func WithOptions(option *Options) transport.Option {
	return func(options *transport.Options) error {
		options.Context = context.WithValue(options.Context, contextKey{}, option.Apply())
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
