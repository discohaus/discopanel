package minecraft

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// World identity read from a level.dat file
type LevelInfo struct {
	LevelName   string
	VersionName string
}

const (
	tagEnd       = 0
	tagByte      = 1
	tagShort     = 2
	tagInt       = 3
	tagLong      = 4
	tagFloat     = 5
	tagDouble    = 6
	tagByteArray = 7
	tagString    = 8
	tagList      = 9
	tagCompound  = 10
	tagIntArray  = 11
	tagLongArray = 12
)

// Reads world name and version testimony from level.dat
func ReadLevelDat(path string) (*LevelInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("level.dat is not gzip compressed: %w", err)
	}
	defer gz.Close()

	r := &nbtReader{r: io.LimitReader(gz, 8<<20)}
	rootType := r.byte()
	if rootType != tagCompound {
		return nil, fmt.Errorf("level.dat root is not a compound tag")
	}
	r.string()

	info := &LevelInfo{}
	r.walkCompound("", info)
	if r.err != nil && r.err != io.EOF {
		return nil, r.err
	}
	if info.LevelName == "" && info.VersionName == "" {
		return nil, fmt.Errorf("level.dat carries no level name or version")
	}
	return info, nil
}

type nbtReader struct {
	r     io.Reader
	err   error
	depth int
}

func (n *nbtReader) read(buf []byte) {
	if n.err != nil {
		return
	}
	_, n.err = io.ReadFull(n.r, buf)
}

func (n *nbtReader) byte() byte {
	var b [1]byte
	n.read(b[:])
	return b[0]
}

func (n *nbtReader) int16() int16 {
	var b [2]byte
	n.read(b[:])
	return int16(binary.BigEndian.Uint16(b[:]))
}

func (n *nbtReader) int32() int32 {
	var b [4]byte
	n.read(b[:])
	return int32(binary.BigEndian.Uint32(b[:]))
}

func (n *nbtReader) string() string {
	length := int(n.int16())
	if n.err != nil || length <= 0 {
		return ""
	}
	buf := make([]byte, length)
	n.read(buf)
	return string(buf)
}

func (n *nbtReader) skip(count int) {
	if n.err != nil || count <= 0 {
		return
	}
	_, n.err = io.CopyN(io.Discard, n.r, int64(count))
}

// Walks a compound capturing the two fields the panel needs
func (n *nbtReader) walkCompound(path string, info *LevelInfo) {
	for n.err == nil {
		tagType := n.byte()
		if tagType == tagEnd || n.err != nil {
			return
		}
		name := n.string()
		full := path + "/" + name
		if tagType == tagString {
			value := n.string()
			switch full {
			case "/Data/LevelName":
				info.LevelName = value
			case "/Data/Version/Name":
				info.VersionName = value
			}
			continue
		}
		n.walkValue(tagType, full, info)
	}
}

func (n *nbtReader) walkValue(tagType byte, path string, info *LevelInfo) {
	// Depth cap keeps a crafted file from blowing the stack
	n.depth++
	defer func() { n.depth-- }()
	if n.depth > 128 {
		n.err = fmt.Errorf("nbt nesting too deep at %s", path)
		return
	}
	switch tagType {
	case tagByte:
		n.skip(1)
	case tagShort:
		n.skip(2)
	case tagInt, tagFloat:
		n.skip(4)
	case tagLong, tagDouble:
		n.skip(8)
	case tagByteArray:
		n.skip(int(n.int32()))
	case tagString:
		n.skip(int(n.int16()))
	case tagIntArray:
		n.skip(int(n.int32()) * 4)
	case tagLongArray:
		n.skip(int(n.int32()) * 8)
	case tagList:
		elemType := n.byte()
		count := int(n.int32())
		for i := 0; i < count && n.err == nil; i++ {
			n.walkValue(elemType, path, info)
		}
	case tagCompound:
		n.walkCompound(path, info)
	default:
		n.err = fmt.Errorf("unknown nbt tag %d at %s", tagType, path)
	}
}
