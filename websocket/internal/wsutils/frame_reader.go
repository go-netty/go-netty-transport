package wsutils

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

// FrameReader is adapted from internal xwsutil.Reader. Renamed to avoid
// name collision and live inside websocket package.
type FrameReader struct {
	Source io.Reader
	State  ws.State

	SkipHeaderCheck bool
	CheckUTF8       bool
	Extensions      []wsutil.RecvExtension
	MaxFrameSize    int64

	OnContinuation wsutil.FrameHandlerFunc
	OnIntermediate wsutil.FrameHandlerFunc

	GetFlateReader func(reader io.Reader) *FlateReader
	PutFlateReader func(reader *FlateReader)

	opCode       ws.OpCode
	frame        io.Reader
	raw          io.LimitedReader
	utf8         wsutil.UTF8Reader
	cipherReader *CipherReader
	flateReader  *FlateReader
	headerBuff   [ws.MaxHeaderSize]byte
}

func NewFrameReader(r io.Reader, s ws.State) *FrameReader {
	return &FrameReader{Source: r, State: s}
}

func (r *FrameReader) Read(p []byte) (n int, err error) {
	if r.frame == nil {
		if !r.fragmented() {
			return 0, wsutil.ErrNoFrameAdvance
		}
		_, err := r.NextFrame()
		if err != nil {
			return 0, err
		}
		if r.frame == nil {
			return 0, nil
		}
	}
	n, err = r.frame.Read(p)
	if err != nil && err != io.EOF {
		return n, err
	}
	if err == nil && r.raw.N != 0 {
		return n, nil
	}

	switch {
	case r.raw.N != 0:
		err = io.ErrUnexpectedEOF
	case r.fragmented():
		err = nil
		r.resetFragment()
	case r.CheckUTF8 && !r.utf8.Valid():
		n = r.utf8.Accepted()
		err = wsutil.ErrInvalidUTF8
	default:
		r.reset()
		err = io.EOF
	}
	return n, err
}

func (r *FrameReader) Discard() (err error) {
	for {
		_, err = io.Copy(ioutil.Discard, &r.raw)
		if err != nil {
			break
		}
		if !r.fragmented() {
			break
		}
		if _, err = r.NextFrame(); err != nil {
			break
		}
	}
	r.reset()
	return err
}

func (r *FrameReader) NextFrame() (hdr ws.Header, err error) {
	hdr, err = ReadHeader(r.Source, r.headerBuff[:])
	if err == io.EOF && r.fragmented() {
		err = io.ErrUnexpectedEOF
	}
	if err == nil && !r.SkipHeaderCheck {
		err = ws.CheckHeader(hdr, r.State)
	}
	if err != nil {
		return hdr, err
	}
	if n := r.MaxFrameSize; n > 0 && hdr.Length > n {
		return hdr, wsutil.ErrFrameTooLarge
	}

	r.raw = io.LimitedReader{R: r.Source, N: hdr.Length}
	frame := io.Reader(&r.raw)
	if hdr.Masked {
		if nil == r.cipherReader {
			r.cipherReader = NewCipherReader(frame, hdr.Mask)
		} else {
			r.cipherReader.Reset(frame, hdr.Mask)
		}
		frame = r.cipherReader
	}

	compressed, err := wsflate.IsCompressed(hdr)
	switch {
	case err != nil:
		return hdr, err
	case compressed:
		if r.GetFlateReader == nil {
			return hdr, fmt.Errorf("required `GetFlateReader`")
		}
		r.flateReader = r.GetFlateReader(frame)
		frame = r.flateReader
	}

	for _, x := range r.Extensions {
		hdr, err = x.UnsetBits(hdr)
		if err != nil {
			return hdr, err
		}
	}

	if r.fragmented() {
		if hdr.OpCode.IsControl() {
			if cb := r.OnIntermediate; cb != nil {
				err = cb(hdr, frame)
			}
			if err == nil {
				_, err = io.Copy(ioutil.Discard, &r.raw)
			}
			return hdr, err
		}
	} else {
		r.opCode = hdr.OpCode
	}
	if r.CheckUTF8 && (hdr.OpCode == ws.OpText || (r.fragmented() && r.opCode == ws.OpText)) {
		r.utf8.Source = frame
		frame = &r.utf8
	}

	r.frame = frame
	if hdr.OpCode == ws.OpContinuation {
		if cb := r.OnContinuation; cb != nil {
			err = cb(hdr, frame)
		}
	}
	if hdr.Fin {
		r.State = r.State.Clear(ws.StateFragmented)
	} else {
		r.State = r.State.Set(ws.StateFragmented)
	}
	return hdr, err
}

func (r *FrameReader) fragmented() bool { return r.State.Fragmented() }

func (r *FrameReader) resetFragment() {
	r.raw = io.LimitedReader{}
	r.frame = nil
	r.utf8.Source = nil
	if r.cipherReader != nil {
		r.cipherReader.Reset(nil, [4]byte{})
	}
}

func (r *FrameReader) reset() {
	r.raw = io.LimitedReader{}
	r.frame = nil
	r.utf8 = wsutil.UTF8Reader{}
	r.opCode = 0
	if r.cipherReader != nil {
		r.cipherReader.Reset(nil, [4]byte{})
	}
	if r.flateReader != nil {
		if r.PutFlateReader != nil {
			r.PutFlateReader(r.flateReader)
		}
		r.flateReader = nil
	}
}

// NextReader convenience
func NextReader(r io.Reader, s ws.State) (ws.Header, io.Reader, error) {
	rd := &FrameReader{Source: r, State: s}
	header, err := rd.NextFrame()
	if err != nil {
		return header, nil, err
	}
	return header, rd, nil
}

// ReadHeader reads a frame header; copied from xwsutil implementation.
func ReadHeader(r io.Reader, bts []byte) (h ws.Header, err error) {
	const (
		bit0 = 0x80
		bit1 = 0x40
		bit2 = 0x20
		bit3 = 0x10
		bit4 = 0x08
		bit5 = 0x04
		bit6 = 0x02
		bit7 = 0x01

		len7  = int64(125)
		len16 = int64(^(uint16(0)))
		len64 = int64(^(uint64(0)) >> 1)
	)
	bts = bts[: 2 : ws.MaxHeaderSize-2]
	_, err = io.ReadFull(r, bts)
	if err != nil {
		return h, err
	}
	h.Fin = bts[0]&bit0 != 0
	h.Rsv = (bts[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(bts[0] & 0x0f)
	var extra int
	if bts[1]&bit0 != 0 {
		h.Masked = true
		extra += 4
	}
	length := bts[1] & 0x7f
	switch {
	case length < 126:
		h.Length = int64(length)
	case length == 126:
		extra += 2
	case length == 127:
		extra += 8
	default:
		err = ws.ErrHeaderLengthUnexpected
		return h, err
	}
	if extra == 0 {
		return h, err
	}
	bts = bts[:extra]
	_, err = io.ReadFull(r, bts)
	if err != nil {
		return h, err
	}
	switch {
	case length == 126:
		h.Length = int64(binary.BigEndian.Uint16(bts[:2]))
		bts = bts[2:]
	case length == 127:
		if bts[0]&0x80 != 0 {
			err = ws.ErrHeaderLengthMSB
			return h, err
		}
		h.Length = int64(binary.BigEndian.Uint64(bts[:8]))
		bts = bts[8:]
	}
	if h.Masked {
		copy(h.Mask[:], bts)
	}
	return h, nil
}
