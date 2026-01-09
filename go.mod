module github.com/go-netty/go-netty-transport

go 1.24.0

require (
	github.com/go-netty/go-netty v1.6.7
	github.com/gobwas/httphead v0.1.0
	github.com/gobwas/ws v1.4.0
	github.com/klauspost/compress v1.18.2
	github.com/libp2p/go-reuseport v0.4.0
	github.com/quic-go/quic-go v0.58.0
	github.com/xtaci/kcp-go/v5 v5.6.61
	golang.org/x/crypto v0.45.0
)

require (
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/klauspost/reedsolomon v1.12.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)

//replace github.com/go-netty/go-netty => ../go-netty
