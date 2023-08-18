package xwsutil

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"reflect"
	"testing"

	"github.com/gobwas/ws"
)

func TestCipherReader(t *testing.T) {
	for i, test := range []struct {
		label string
		data  []byte
		chop  int
	}{
		{
			label: "simple",
			data:  []byte("hello, websockets!"),
			chop:  512,
		},
		{
			label: "chopped",
			data:  []byte("hello, websockets!"),
			chop:  3,
		},
	} {
		t.Run(fmt.Sprintf("%s#%d", test.label, i), func(t *testing.T) {
			mask := ws.NewMask()
			masked := make([]byte, len(test.data))
			copy(masked, test.data)
			FastCipher(masked, mask, 0)

			src := &chopReader{bytes.NewReader(masked), test.chop}
			rd := NewCipherReader(src, mask)

			bts, err := ioutil.ReadAll(rd)
			if err != nil {
				t.Errorf("unexpected error: %s", err)
				return
			}
			if !reflect.DeepEqual(bts, test.data) {
				t.Errorf("read data is not equal:\n\tact:\t%#v\n\texp:\t%#x\n", bts, test.data)
				return
			}
		})
	}
}

// remain maps position in masking key [0,4) to number
// of bytes that need to be processed manually inside Cipher().
var remain = [4]int{0, 3, 2, 1}

func TestCipher(t *testing.T) {
	type test struct {
		name   string
		in     []byte
		mask   [4]byte
		offset int
	}
	cases := []test{
		{
			name: "simple",
			in:   []byte("Hello, XOR!"),
			mask: [4]byte{1, 2, 3, 4},
		},
		{
			name: "simple",
			in:   []byte("Hello, XOR!"),
			mask: [4]byte{255, 255, 255, 255},
		},
	}
	for offset := 0; offset < 4; offset++ {
		for tail := 0; tail < 8; tail++ {
			for b64 := 0; b64 < 3; b64++ {
				var (
					ln = remain[offset]
					rn = tail
					n  = b64*8 + ln + rn
				)

				p := make([]byte, n)
				rand.Read(p)

				var m [4]byte
				rand.Read(m[:])

				cases = append(cases, test{
					in:     p,
					mask:   m,
					offset: offset,
				})
			}
		}
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// naive implementation of xor-cipher
			exp := cipherNaive(test.in, test.mask, test.offset)

			res := make([]byte, len(test.in))
			copy(res, test.in)
			FastCipher(res, test.mask, test.offset)

			if !reflect.DeepEqual(res, exp) {
				t.Errorf("Cipher(%v, %v):\nact:\t%v\nexp:\t%v\n", test.in, test.mask, res, exp)
			}
		})
	}
}

func TestCipherChops(t *testing.T) {
	for n := 2; n <= 1024; n <<= 1 {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			p := make([]byte, n)
			b := make([]byte, n)
			var m [4]byte

			_, err := rand.Read(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rand.Read(m[:])
			if err != nil {
				t.Fatal(err)
			}

			exp := cipherNaive(p, m, 0)

			for i := 1; i <= n; i <<= 1 {
				copy(b, p)
				s := n / i

				for j := s; j <= n; j += s {
					l, r := j-s, j
					FastCipher(b[l:r], m, l)
					if !reflect.DeepEqual(b[l:r], exp[l:r]) {
						t.Errorf("unexpected Cipher([%d:%d]) = %x; want %x", l, r, b[l:r], exp[l:r])
						return
					}
				}
			}

			l := 0
			copy(b, p)
			for l < n {
				r := rand.Intn(n-l) + l + 1
				FastCipher(b[l:r], m, l)
				if !reflect.DeepEqual(b[l:r], exp[l:r]) {
					t.Errorf("unexpected Cipher([%d:%d]):\nact:\t%v\nexp:\t%v\nact:\t%#x\nexp:\t%#x\n\n", l, r, b[l:r], exp[l:r], b[l:r], exp[l:r])
					return
				}
				l = r
			}
		})
	}
}

func cipherNaive(p []byte, m [4]byte, pos int) []byte {
	r := make([]byte, len(p))
	copy(r, p)
	cipherNaiveNoCp(r, m, pos)
	return r
}

func cipherNaiveNoCp(p []byte, m [4]byte, pos int) []byte {
	for i := 0; i < len(p); i++ {
		p[i] ^= m[(pos+i)%4]
	}
	return p
}

func BenchmarkCipher(b *testing.B) {
	for _, bench := range []struct {
		size   int
		offset int
	}{
		{
			size:   7,
			offset: 1,
		},
		{
			size: 125,
		},
		{
			size: 1024,
		},
		{
			size: 4096,
		},
		{
			size:   4100,
			offset: 4,
		},
		{
			size:   4099,
			offset: 3,
		},
		{
			size:   (1 << 15) + 7,
			offset: 49,
		},
	} {
		bts := make([]byte, bench.size)
		_, err := rand.Read(bts)
		if err != nil {
			b.Fatal(err)
		}

		var mask [4]byte
		_, err = rand.Read(mask[:])
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("naive_bytes=%d;offset=%d", bench.size, bench.offset), func(b *testing.B) {
			var sink int64
			for i := 0; i < b.N; i++ {
				r := cipherNaiveNoCp(bts, mask, bench.offset)
				sink += int64(len(r))
			}
			sinkValue(sink)
		})
		b.Run(fmt.Sprintf("bytes=%d;offset=%d", bench.size, bench.offset), func(b *testing.B) {
			var sink int64
			for i := 0; i < b.N; i++ {
				FastCipher(bts, mask, bench.offset)
				sink += int64(len(bts))
			}
			sinkValue(sink)
		})
	}
}

// sinkValue makes variable used and prevents dead code elimination.
func sinkValue(v int64) {
	if r := rand.Float32(); r > 2 {
		panic(fmt.Sprintf("impossible %g: %v", r, v))
	}
}
