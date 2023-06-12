package websocket

import (
	"context"
	"net/http"

	"github.com/go-netty/go-netty"
	"github.com/go-netty/go-netty/transport"
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

	e := wsflate.Extension{
		Parameters: wsflate.Parameters{
			ServerNoContextTakeover: true,
			ClientNoContextTakeover: true,
		},
	}

	upgrader := ws.HTTPUpgrader{}
	upgrader.Negotiate = e.Negotiate

	return HTTPUpgrader{
		Upgrader:   upgrader,
		ctx:        options.Context,
		attachment: options.Attachment,
		options:    FromContext(options.Context, DefaultOptions),
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

	t, err := newWebsocketTransport(conn, request.URL.Path, hu.options, false)
	if nil != err {
		_ = conn.Close()
		return nil, err
	}

	return hu.serve.ServeChannel(hu.ctx, t, hu.attachment, true), nil
}
