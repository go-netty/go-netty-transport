package websocket

import (
	"context"
	"net/http"

	"github.com/go-netty/go-netty"
	"github.com/go-netty/go-netty/transport"
	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

type Upgrader interface {
	Upgrade(writer http.ResponseWriter, request *http.Request) (netty.Channel, error)
}

type channelServe interface {
	ServeChannel(ctx context.Context, transport transport.Transport, attachment netty.Attachment, childChannel bool) netty.Channel
}

type HTTPUpgrader struct {
	Upgrader   ws.HTTPUpgrader
	ctx        context.Context
	attachment netty.Attachment
	options    *Options
	serve      channelServe
}

func NewHTTPUpgrader(engine netty.Bootstrap, option ...transport.Option) HTTPUpgrader {
	options, err := transport.ParseOptions(engine.Context(), "ws://127.0.0.1:0", option...)
	if nil != err {
		panic(err)
	}

	wsOptions := FromContext(options.Context, DefaultOptions)

	if wsOptions.CompressEnabled && nil == wsOptions.Upgrader.Negotiate {
		wsOptions.Upgrader.Negotiate = func(option httphead.Option) (httphead.Option, error) {
			e := wsflate.Extension{
				Parameters: wsflate.DefaultParameters,
			}
			return e.Negotiate(option)
		}
	}

	return HTTPUpgrader{
		Upgrader:   wsOptions.Upgrader,
		ctx:        options.Context,
		attachment: options.Attachment,
		options:    wsOptions,
		serve:      engine.(channelServe),
	}
}

func (hu HTTPUpgrader) Upgrade(writer http.ResponseWriter, request *http.Request) (netty.Channel, error) {
	conn, _, _, err := hu.Upgrader.Upgrade(request, writer)
	if nil != err {
		if nil != conn {
			_ = conn.Close()
		}
		return nil, err
	}

	t, err := newWebsocketTransport(conn, hu.options, false, request)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}

	return hu.serve.ServeChannel(hu.ctx, t, hu.attachment, true), nil
}
