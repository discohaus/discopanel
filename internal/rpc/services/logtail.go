package services

import (
	"bytes"
	"io"
	"os"
)

// Reads at most maxBytes from file end, returns full size
func readFileTail(path string, maxBytes int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}

	readSize := info.Size()
	var offset int64
	if readSize > maxBytes {
		offset = readSize - maxBytes
		readSize = maxBytes
	}
	buf := make([]byte, readSize)
	n, err := file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, 0, err
	}
	content := buf[:n]

	// Drops partial first line after mid file start
	if offset > 0 {
		if i := bytes.IndexByte(content, '\n'); i >= 0 {
			content = content[i+1:]
		}
	}
	return content, info.Size(), nil
}

// Returns suffix holding the last n newline separated lines
func lastLines(content []byte, n int) []byte {
	if n <= 0 {
		return content
	}
	end := len(content)
	// A trailing newline ends the last line, not another
	if end > 0 && content[end-1] == '\n' {
		end--
	}
	for i := 0; i < n; i++ {
		idx := bytes.LastIndexByte(content[:end], '\n')
		if idx < 0 {
			return content
		}
		end = idx
	}
	return content[end+1:]
}
