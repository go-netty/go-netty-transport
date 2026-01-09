package wsutils

import (
	"io"
	"io/ioutil"
	"sync"

	"github.com/go-netty/go-netty/utils/pool/pbytes"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// CipherReader is a tiny helper to XOR mask.
type CipherReader struct {
	r   io.Reader
	m   [4]byte
	pos int
}

func NewCipherReader(r io.Reader, mask [4]byte) *CipherReader {
	return &CipherReader{r: r, m: mask}
}

func (c *CipherReader) Reset(r io.Reader, mask [4]byte) {
	c.r = r
	c.m = mask
	c.pos = 0
}

func (c *CipherReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		FastCipher(p[:n], c.m, c.pos)
		c.pos = (c.pos + n) & 3
	}
	return n, err
}

// FastCipher in-place xors p with mask, starting at offset.
func FastCipher(p []byte, mask [4]byte, off int) {
	if len(p) == 0 {
		return
	}
	var i int
	for ; i < len(p) && (off&3) != 0; i++ {
		p[i] ^= mask[off&3]
		off++
	}
	for ; i+4 <= len(p); i += 4 {
		p[i] ^= mask[0]
		p[i+1] ^= mask[1]
		p[i+2] ^= mask[2]
		p[i+3] ^= mask[3]
	}
	for ; i < len(p); i++ {
		p[i] ^= mask[off&3]
		off++
	}
}

// ControlHandler contains logic of handling control frames.
type ControlHandler struct {
	Src                 io.Reader
	Dst                 io.Writer
	State               ws.State
	WriterLocker        sync.Locker
	DisableSrcCiphering bool
}

func (c ControlHandler) Handle(h ws.Header) error {
	switch h.OpCode {
	case ws.OpPing:
		return c.HandlePing(h)
	case ws.OpPong:
		return c.HandlePong(h)
	case ws.OpClose:
		return c.HandleClose(h)
	}
	return wsutil.ErrNotControlFrame
}

func (c ControlHandler) HandlePing(h ws.Header) error {
	if h.Length == 0 {
		c.WriterLocker.Lock()
		defer c.WriterLocker.Unlock()
		return ws.WriteHeader(c.Dst, ws.Header{Fin: true, OpCode: ws.OpPong, Masked: c.State.ClientSide()})
	}
	bufSize := int(h.Length) + ws.HeaderSize(ws.Header{Length: h.Length, Masked: c.State.ClientSide()})
	p := pbytes.Get(bufSize)
	defer pbytes.Put(p)

	c.WriterLocker.Lock()
	defer c.WriterLocker.Unlock()

	w := wsutil.NewControlWriterBuffer(c.Dst, c.State, ws.OpPong, (*p)[:bufSize])
	r := c.Src
	if c.State.ServerSide() && !c.DisableSrcCiphering {
		r = wsutil.NewCipherReader(r, h.Mask)
	}
	_, err := io.Copy(w, r)
	if err == nil {
		err = w.Flush()
	}
	return err
}

func (c ControlHandler) HandlePong(h ws.Header) error {
	if h.Length == 0 {
		return nil
	}
	buf := pbytes.Get(int(h.Length))
	defer pbytes.Put(buf)
	_, err := io.CopyBuffer(ioutil.Discard, c.Src, (*buf)[:h.Length])
	return err
}

func (c ControlHandler) HandleClose(h ws.Header) error {
	if h.Length == 0 {
		c.WriterLocker.Lock()
		defer c.WriterLocker.Unlock()
		err := ws.WriteHeader(c.Dst, ws.Header{Fin: true, OpCode: ws.OpClose, Masked: c.State.ClientSide()})
		if err != nil {
			return err
		}
		return wsutil.ClosedError{Code: ws.StatusNoStatusRcvd}
	}
	bufSize := int(h.Length) + ws.HeaderSize(ws.Header{Length: h.Length, Masked: c.State.ClientSide()})
	p := pbytes.Get(bufSize)
	defer pbytes.Put(p)
	subp := (*p)[:h.Length]
	r := c.Src
	if c.State.ServerSide() && !c.DisableSrcCiphering {
		r = wsutil.NewCipherReader(r, h.Mask)
	}
	if _, err := io.ReadFull(r, subp); err != nil {
		return err
	}
	code, reason := ws.ParseCloseFrameData(subp)
	if err := ws.CheckCloseFrameData(code, reason); err != nil {
		c.closeWithProtocolError(err)
		return err
	}
	c.WriterLocker.Lock()
	defer c.WriterLocker.Unlock()
	w := wsutil.NewControlWriterBuffer(c.Dst, c.State, ws.OpClose, (*p)[:bufSize])
	_, err := w.Write((*p)[:2])
	if err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return wsutil.ClosedError{Code: code, Reason: reason}
}

func (c ControlHandler) closeWithProtocolError(reason error) error {
	f := ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusProtocolError, reason.Error()))
	if c.State.ClientSide() {
		ws.MaskFrameInPlace(f)
	}
	c.WriterLocker.Lock()
	defer c.WriterLocker.Unlock()
	return ws.WriteFrame(c.Dst, f)
}

func ControlFrameHandler(w io.Writer, wlock sync.Locker, state ws.State) wsutil.FrameHandlerFunc {
	return func(h ws.Header, r io.Reader) error {
		return (ControlHandler{DisableSrcCiphering: true, Src: r, Dst: w, WriterLocker: wlock, State: state}).Handle(h)
	}
}
