package packet

import (
	"crypto/aes"
	"crypto/cipher"
	"io"
)

// Streaming aes cfb8 state for one direction
type cfb8 struct {
	block   cipher.Block
	iv      []byte
	decrypt bool
}

// Builds the cfb8 stream minecraft encrypts with
func newCFB8(key []byte, decrypt bool) (*cfb8, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, block.BlockSize())
	copy(iv, key)
	return &cfb8{block: block, iv: iv, decrypt: decrypt}, nil
}

// Transforms bytes in place one at a time
func (c *cfb8) xor(dst, src []byte) {
	scratch := make([]byte, c.block.BlockSize())
	for i := range src {
		c.block.Encrypt(scratch, c.iv)
		plain := src[i]
		out := plain ^ scratch[0]
		copy(c.iv, c.iv[1:])
		if c.decrypt {
			c.iv[len(c.iv)-1] = plain
		} else {
			c.iv[len(c.iv)-1] = out
		}
		dst[i] = out
	}
}

// Decrypting reader over a wrapped stream
type CipherReader struct {
	r io.Reader
	c *cfb8
}

// Wraps a reader with cfb8 decryption
func NewCipherReader(r io.Reader, key []byte) (*CipherReader, error) {
	c, err := newCFB8(key, true)
	if err != nil {
		return nil, err
	}
	return &CipherReader{r: r, c: c}, nil
}

func (cr *CipherReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.c.xor(p[:n], p[:n])
	}
	return n, err
}

// Encrypting writer over a wrapped stream
type CipherWriter struct {
	w io.Writer
	c *cfb8
}

// Wraps a writer with cfb8 encryption
func NewCipherWriter(w io.Writer, key []byte) (*CipherWriter, error) {
	c, err := newCFB8(key, false)
	if err != nil {
		return nil, err
	}
	return &CipherWriter{w: w, c: c}, nil
}

func (cw *CipherWriter) Write(p []byte) (int, error) {
	out := make([]byte, len(p))
	cw.c.xor(out, p)
	n, err := cw.w.Write(out)
	if err != nil {
		return n, err
	}
	return len(p), nil
}
