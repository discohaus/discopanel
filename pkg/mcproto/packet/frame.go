package packet

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
)

// Caps any single frame body
const MaxFrameLength = 8 << 20

// Reads one uncompressed frame body
func ReadFrame(r io.Reader) ([]byte, error) {
	length, err := mcproto.ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	if length < 1 || length > MaxFrameLength {
		return nil, fmt.Errorf("frame length %d out of bounds", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// Writes one uncompressed frame body
func WriteFrame(w io.Writer, body []byte) error {
	return mcproto.WriteFramed(w, body)
}

// Reads one frame under a compression threshold
func ReadFrameZ(r io.Reader, threshold int) ([]byte, error) {
	if threshold < 0 {
		return ReadFrame(r)
	}
	length, err := mcproto.ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	if length < 1 || length > MaxFrameLength {
		return nil, fmt.Errorf("frame length %d out of bounds", length)
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	buf := bytes.NewReader(raw)
	dataLength, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return nil, err
	}
	if dataLength == 0 {
		body := make([]byte, buf.Len())
		if _, err := io.ReadFull(buf, body); err != nil {
			return nil, err
		}
		return body, nil
	}
	if dataLength < 0 || dataLength > MaxFrameLength {
		return nil, fmt.Errorf("data length %d out of bounds", dataLength)
	}
	zr, err := zlib.NewReader(buf)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	body := make([]byte, dataLength)
	if _, err := io.ReadFull(zr, body); err != nil {
		return nil, err
	}
	return body, nil
}

// Writes one frame under a compression threshold
func WriteFrameZ(w io.Writer, body []byte, threshold int) error {
	if threshold < 0 {
		return WriteFrame(w, body)
	}
	var payload bytes.Buffer
	if len(body) < threshold {
		if err := mcproto.WriteVarInt(&payload, 0); err != nil {
			return err
		}
		payload.Write(body)
	} else {
		if err := mcproto.WriteVarInt(&payload, mcproto.VarInt(len(body))); err != nil {
			return err
		}
		zw := zlib.NewWriter(&payload)
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
	}
	return mcproto.WriteFramed(w, payload.Bytes())
}
