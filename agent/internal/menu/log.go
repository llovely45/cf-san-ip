package menu

import (
	"bytes"
	"io"
	"os"
)

const logTailChunkSize = 4096

func readLogTail(path string, maxLines int) ([]byte, error) {
	if maxLines <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	if info.Size() == 0 {
		return nil, nil
	}

	remaining := info.Size()
	var data []byte
	newlines := 0
	for remaining > 0 && newlines <= maxLines {
		chunkSize := int64(logTailChunkSize)
		if remaining < chunkSize {
			chunkSize = remaining
		}
		remaining -= chunkSize
		chunk := make([]byte, int(chunkSize))
		if _, err := file.ReadAt(chunk, remaining); err != nil && err != io.EOF {
			return nil, err
		}
		newlines += bytes.Count(chunk, []byte{'\n'})
		data = append(chunk, data...)
	}

	// When the beginning of the file was not read, discard the partial line.
	if remaining > 0 {
		if firstNewline := bytes.IndexByte(data, '\n'); firstNewline >= 0 {
			data = data[firstNewline+1:]
		} else {
			data = nil
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	endsWithNewline := data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte{'\n'})
	if endsWithNewline {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	result := bytes.Join(lines, []byte{'\n'})
	if endsWithNewline {
		result = append(result, '\n')
	}
	return result, nil
}
