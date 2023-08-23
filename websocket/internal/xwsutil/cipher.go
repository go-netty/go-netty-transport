package xwsutil

import (
	"io"
	"unsafe"

	"github.com/go-netty/go-netty/utils/pool/pbytes"
)

// CipherReader implements io.Reader that applies xor-cipher to the bytes read
// from source.
// It could help to unmask WebSocket frame payload on the fly.
type CipherReader struct {
	r    io.Reader
	mask [4]byte
	pos  int
}

// NewCipherReader creates xor-cipher reader from r with given mask.
func NewCipherReader(r io.Reader, mask [4]byte) *CipherReader {
	return &CipherReader{r, mask, 0}
}

// Reset resets CipherReader to read from r with given mask.
func (c *CipherReader) Reset(r io.Reader, mask [4]byte) {
	c.r = r
	c.mask = mask
	c.pos = 0
}

// Read implements io.Reader interface. It applies mask given during
// initialization to every read byte.
func (c *CipherReader) Read(p []byte) (n int, err error) {
	n, err = c.r.Read(p)
	FastCipher(p[:n], c.mask, c.pos)
	c.pos += n
	return n, err
}

// CipherWriter implements io.Writer that applies xor-cipher to the bytes
// written to the destination writer. It does not modify the original bytes.
type CipherWriter struct {
	w    io.Writer
	mask [4]byte
	pos  int
}

// NewCipherWriter creates xor-cipher writer to w with given mask.
func NewCipherWriter(w io.Writer, mask [4]byte) *CipherWriter {
	return &CipherWriter{w, mask, 0}
}

// Reset reset CipherWriter to write to w with given mask.
func (c *CipherWriter) Reset(w io.Writer, mask [4]byte) {
	c.w = w
	c.mask = mask
	c.pos = 0
}

// Write implements io.Writer interface. It applies masking during
// initialization to every sent byte. It does not modify original slice.
func (c *CipherWriter) Write(p []byte) (n int, err error) {
	cp := pbytes.Get(len(p))
	defer pbytes.Put(cp)

	buf := (*cp)[:len(p)]

	copy(buf, p)
	FastCipher(buf, c.mask, c.pos)
	n, err = c.w.Write(buf)
	c.pos += n

	return n, err
}

const wordSize = int(unsafe.Sizeof(uintptr(0)))

func FastCipher(b []byte, key [4]byte, pos int) int {
	// Mask one byte at a time for small buffers.
	if len(b) < 2*wordSize {
		for i := range b {
			b[i] ^= key[pos&3]
			pos++
		}
		return pos & 3
	}

	// Mask one byte at a time to word boundary.
	if n := int(uintptr(unsafe.Pointer(&b[0]))) % wordSize; n != 0 {
		n = wordSize - n
		for i := range b[:n] {
			b[i] ^= key[pos&3]
			pos++
		}
		b = b[n:]
	}

	// Create aligned word size key.
	var k [wordSize]byte
	for i := range k {
		k[i] = key[(pos+i)&3]
	}
	kw := *(*uintptr)(unsafe.Pointer(&k))

	// Mask one word at a time.
	n := (len(b) / wordSize) * wordSize
	for i := 0; i < n; i += wordSize {
		*(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(&b[0])) + uintptr(i))) ^= kw
	}

	// Mask one byte at a time for remaining bytes.
	b = b[n:]
	for i := range b {
		b[i] ^= key[pos&3]
		pos++
	}

	return pos & 3
}
