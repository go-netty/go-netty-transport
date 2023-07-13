module github.com/go-netty/go-netty-transport

go 1.16

require (
	github.com/go-netty/go-netty v1.6.3
	github.com/gobwas/httphead v0.1.0
	github.com/gobwas/ws v1.2.1
	github.com/libp2p/go-reuseport v0.3.0
	github.com/quic-go/quic-go v0.34.0
	github.com/smallnest/quick v0.1.0
	github.com/xtaci/kcp-go/v5 v5.6.2
	golang.org/x/crypto v0.6.0
)

//replace github.com/go-netty/go-netty => ../go-netty
