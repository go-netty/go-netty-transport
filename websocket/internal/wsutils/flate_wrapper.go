package wsutils

import (
	"fmt"
	"io"

	"github.com/gobwas/ws/wsflate"
)

var (
	compressionTail = [4]byte{0, 0, 0xff, 0xff}
	// compressionReadTail must provide bytes appended to the raw DEFLATE
	// stream so the decompressor can finish properly. The canonical tail
	// for a sync-flush raw DEFLATE block is 0x00 0x00 0xff 0xff (4 bytes).
	// Using a longer/incorrect suffix may confuse the inflater and lead to
	// protocol errors (close 1002). Keep it 4 bytes to match expectations.
	compressionReadTail = [4]byte{0, 0, 0xff, 0xff}
)

// Decompressor is alias for wsflate decompressor.
type Decompressor = wsflate.Decompressor

// Compressor is alias for wsflate compressor.
type Compressor = wsflate.Compressor

// ReadResetter is optional interface for decompressors.
type ReadResetter interface {
	Reset(io.Reader, []byte) error
}

// FlateReader implements decompression wrapper (renamed from xwsflate.Reader).
type FlateReader struct {
	src  io.Reader
	ctor func(io.Reader) Decompressor
	d    Decompressor
	sr   suffixedReader
	err  error
}

// NewFlateReader returns a new FlateReader.
func NewFlateReader(r io.Reader, ctor func(io.Reader) Decompressor) *FlateReader {
	ret := &FlateReader{
		src:  r,
		ctor: ctor,
		sr:   suffixedReader{suffix: compressionReadTail},
	}
	ret.Reset(r)
	return ret
}

// Reset resets reader to new source.
func (r *FlateReader) Reset(src io.Reader) {
	r.err = nil
	r.src = src
	r.sr.reset(src)
	if x, ok := r.d.(ReadResetter); ok {
		x.Reset(r.sr.iface(), nil)
	} else {
		r.d = r.ctor(r.sr.iface())
	}
}

// Read implements io.Reader.
func (r *FlateReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return r.d.Read(p)
}

// Close closes underlying decompressor if closable.
func (r *FlateReader) Close() error {
	if r.err != nil {
		return r.err
	}
	if c, ok := r.d.(io.Closer); ok {
		r.err = c.Close()
	}
	return r.err
}

// Err returns last error.
func (r *FlateReader) Err() error { return r.err }

// WriteResetter is optional interface for compressors.
type WriteResetter interface {
	Reset(io.Writer)
}

// FlateWriter implements compression wrapper (renamed from xwsflate.Writer).
type FlateWriter struct {
	dest io.Writer
	ctor func(io.Writer) Compressor
	c    Compressor
	cbuf cbuf
	err  error
}

// NewFlateWriter returns a new FlateWriter.
func NewFlateWriter(w io.Writer, ctor func(io.Writer) Compressor) *FlateWriter {
	ret := &FlateWriter{dest: w, ctor: ctor}
	ret.Reset(w)
	return ret
}

// Reset resets writer to dest.
func (w *FlateWriter) Reset(dest io.Writer) {
	w.err = nil
	w.cbuf.reset(dest)
	if x, ok := w.c.(WriteResetter); ok {
		x.Reset(&w.cbuf)
	} else {
		w.c = w.ctor(&w.cbuf)
	}
}

// Write writes through compressor.
func (w *FlateWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.c.Write(p)
	if err != nil {
		w.err = err
	}
	return n, w.err
}

// Flush flushes compressor and checks tail.
func (w *FlateWriter) Flush() error {
	if w.err != nil {
		return w.err
	}
	w.err = w.c.Flush()
	w.checkTail()
	return w.err
}

// Close closes compressor if closable and checks tail.
func (w *FlateWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if c, ok := w.c.(io.Closer); ok {
		w.err = c.Close()
	}
	w.checkTail()
	return w.err
}

func (w *FlateWriter) Err() error { return w.err }

func (w *FlateWriter) checkTail() {
	if w.err == nil && w.cbuf.buf != compressionTail {
		w.err = fmt.Errorf("wsflate: bad compressor: unexpected stream tail: %#x vs %#x", w.cbuf.buf, compressionTail)
	}
}

// cbuf and suffixedReader copied/adapted below
type cbuf struct {
	buf [4]byte
	n   int
	dst io.Writer
	err error
}

func (c *cbuf) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	head, tail := c.split(p)
	n := c.n + len(tail)
	if n > len(c.buf) {
		x := n - len(c.buf)
		c.flush(c.buf[:x])
		copy(c.buf[:], c.buf[x:])
		c.n -= x
	}
	if len(head) > 0 {
		c.flush(head)
	}
	copy(c.buf[c.n:], tail)
	c.n = min(c.n+len(tail), len(c.buf))
	return len(p), c.err
}

func (c *cbuf) flush(p []byte) {
	if c.err == nil {
		_, c.err = c.dst.Write(p)
	}
}

func (c *cbuf) split(p []byte) (head, tail []byte) {
	if n := len(p); n > len(c.buf) {
		x := n - len(c.buf)
		head = p[:x]
		tail = p[x:]
		return head, tail
	}
	return nil, p
}

func (c *cbuf) reset(dst io.Writer) {
	c.n = 0
	c.err = nil
	c.buf = [4]byte{0, 0, 0, 0}
	c.dst = dst
}

type suffixedReader struct {
	r      io.Reader
	pos    int
	suffix [4]byte
	rx     struct{ io.Reader }
}

func (r *suffixedReader) iface() io.Reader {
	if _, ok := r.r.(io.ByteReader); ok {
		return r
	}
	r.rx.Reader = r
	return &r.rx
}

func (r *suffixedReader) Read(p []byte) (n int, err error) {
	if r.r != nil {
		n, err = r.r.Read(p)
		if err == io.EOF {
			err = nil
			r.r = nil
		}
		return n, err
	}
	if r.pos >= len(r.suffix) {
		return 0, io.EOF
	}
	n = copy(p, r.suffix[r.pos:])
	r.pos += n
	return n, nil
}

func (r *suffixedReader) ReadByte() (b byte, err error) {
	if r.r != nil {
		br, ok := r.r.(io.ByteReader)
		if !ok {
			panic("wsflate: internal error: incorrect use of suffixedReader")
		}
		b, err = br.ReadByte()
		if err == io.EOF {
			err = nil
			r.r = nil
		}
		return b, err
	}
	if r.pos >= len(r.suffix) {
		return 0, io.EOF
	}
	b = r.suffix[r.pos]
	r.pos++
	return b, nil
}

func (r *suffixedReader) reset(src io.Reader) {
	r.r = src
	r.pos = 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
