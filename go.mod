module github.com/go-netty/go-netty-transport

go 1.16

require (
	github.com/go-netty/go-netty v0.0.0-20230223104942-ec2ab6d5f2a8
	github.com/gobwas/ws v1.1.0
	github.com/libp2p/go-reuseport v0.2.0
	github.com/marten-seemann/quic-conn v0.0.0-20191204020628-6e719687462b
	github.com/xtaci/kcp-go/v5 v5.6.1
	golang.org/x/crypto v0.6.0
)

//replace github.com/go-netty/go-netty => ../go-netty
