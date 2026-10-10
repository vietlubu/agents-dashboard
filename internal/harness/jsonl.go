package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// DefaultMaxLineBytes bounds one JSONL record. Real usage records are a few hundred bytes;
// some harnesses write records of several hundred kilobytes because the same line also
// carries the assistant's text. Anything larger than this is skipped rather than buffered,
// so a corrupt or unusual file cannot make a scan allocate without limit.
const DefaultMaxLineBytes = 4 << 20

// chunkSize is the read granularity. Small enough to stay cache-friendly, large enough
// that a 90 MB rollout costs ~1.5k reads instead of tens of thousands.
const chunkSize = 64 << 10

// ReadResult describes what a scan consumed.
type ReadResult struct {
	// NewOffset is the absolute position just past the last complete line. It never
	// advances past a partially written line, so that line is re-read next time.
	NewOffset int64
	Mtime     int64
	Size      int64
	Inode     int64
	// Truncated is true when the file was smaller than the requested offset (the file was
	// replaced or truncated), in which case NewOffset is 0 and the caller must drop the
	// events previously parsed from this file.
	Truncated      bool
	OversizedLines int
	Lines          int
}

// FileScanner reads newline-delimited records from an append-only file.
//
// One scanner is reused across every file of a scan. Its two buffers (a read chunk and the
// partial-line assembly buffer) are therefore allocated once per scan instead of once per
// file: with ~1,400 session files and records up to a megabyte, reallocating them per file
// was the dominant source of allocation churn during a cold scan.
//
// A FileScanner is not safe for concurrent use.
type FileScanner struct {
	// MaxLineBytes caps one record; the default is DefaultMaxLineBytes.
	MaxLineBytes int64
	// OversizedLines counts records skipped for exceeding MaxLineBytes, for diagnostics.
	OversizedLines int

	chunk   []byte
	pending []byte
}

// ScanFile scans one file with a throwaway scanner. Use a FileScanner directly when
// scanning more than one file.
func ScanFile(ctx context.Context, path string, offset int64, markers [][]byte, fn func(line []byte) error) (ReadResult, error) {
	var fs FileScanner
	return fs.Scan(ctx, path, offset, markers, fn)
}

// ScanFileLimit is ScanFile with an explicit record cap.
func ScanFileLimit(ctx context.Context, path string, offset int64, markers [][]byte, maxLine int64, fn func(line []byte) error) (ReadResult, error) {
	fs := FileScanner{MaxLineBytes: maxLine}
	return fs.Scan(ctx, path, offset, markers, fn)
}

// Scan streams every complete record that contains one of the markers, calling fn for
// each. The line slice is only valid for the duration of the callback.
func (fs *FileScanner) Scan(ctx context.Context, path string, offset int64, markers [][]byte, fn func(line []byte) error) (ReadResult, error) {
	maxLine := fs.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	if fs.chunk == nil {
		fs.chunk = make([]byte, chunkSize)
	}

	var res ReadResult

	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return res, err
	}
	res.Mtime = fi.ModTime().UnixMilli()
	res.Size = fi.Size()
	res.Inode = FileInode(fi)

	if offset > res.Size {
		res.Truncated = true
		offset = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return res, err
		}
		res.NewOffset = offset
	}

	pending := fs.pending[:0]
	oversized := false
	consumed := offset
	buf := fs.chunk

	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		n, readErr := f.Read(buf)

		if n > 0 {
			chunk := buf[:n]
			for {
				idx := bytes.IndexByte(chunk, '\n')
				if idx < 0 {
					break
				}
				raw := chunk[:idx]
				consumed += int64(len(raw)) + 1
				res.Lines++
				switch {
				case oversized:
					// The oversized line already counted when it first exceeded the cap.
					oversized = false
				case len(pending) > 0:
					pending = append(pending, raw...)
					if err := emitLine(pending, maxLine, markers, &res, fn); err != nil {
						res.NewOffset = consumed
						fs.pending = pending
						return res, err
					}
				default:
					if err := emitLine(raw, maxLine, markers, &res, fn); err != nil {
						res.NewOffset = consumed
						fs.pending = pending
						return res, err
					}
				}
				pending = pending[:0]
				chunk = chunk[idx+1:]
			}
			if len(chunk) > 0 {
				switch {
				case oversized:
					// Keep discarding until the terminating newline arrives.
				case int64(len(pending)+len(chunk)) > maxLine:
					// Too long to be a usage record: drop it, count it, and resume at the
					// next newline so the stream stays aligned.
					pending = pending[:0]
					oversized = true
					res.OversizedLines++
				default:
					pending = append(pending, chunk...)
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			fs.pending = pending
			return res, fmt.Errorf("read %s: %w", path, readErr)
		}
	}

	// A trailing partial line stays pending: NewOffset is not advanced past it.
	res.NewOffset = consumed
	res.OversizedLines += 0
	fs.OversizedLines += res.OversizedLines
	fs.pending = pending
	return res, nil
}

// ReadLines streams newline-delimited records from an arbitrary reader, calling fn for
// every line that matches a marker and is short enough to be a usage record. It exists
// for adapters whose source is not a plain file on disk — the DSH adapter wraps a
// Zstandard decoder in it — and it enforces the same two guarantees as FileScanner:
// records longer than DefaultMaxLineBytes are dropped rather than buffered without limit,
// and marker filtering happens before the line is offered to the callback.
//
// It returns the number of lines offered to fn. A read error from r (including the decode
// error a torn trailing Zstandard frame produces) is returned so the caller can decide
// whether to re-read from the start.
func ReadLines(r io.Reader, markers [][]byte, fn func([]byte)) (int, error) {
	br := bufio.NewReaderSize(r, chunkSize)
	var pending []byte
	oversized := false
	lines := 0

	emit := func() {
		line := bytes.TrimRight(pending, "\r\n")
		pending = pending[:0]
		if len(line) == 0 || int64(len(line)) > DefaultMaxLineBytes {
			return
		}
		if hasMarker(line, markers) {
			fn(line)
			lines++
		}
	}

	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case oversized:
			// Discard the tail of an over-long line until its terminating newline.
			if err == nil {
				oversized = false
			}
		case err == nil:
			pending = append(pending, chunk...)
			emit()
		default:
			pending = append(pending, chunk...)
			if int64(len(pending)) > DefaultMaxLineBytes {
				pending = pending[:0]
				oversized = true
			}
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			// A trailing line without a newline is still a complete record.
			if !oversized {
				emit()
			}
			return lines, nil
		default:
			return lines, err
		}
	}
}

// emitLine hands one complete line to the callback unless it is too long to be a usage
// record. An over-long line is counted and dropped, which keeps the stream aligned without
// buffering it.
func emitLine(line []byte, maxLine int64, markers [][]byte, res *ReadResult, fn func([]byte) error) error {
	if int64(len(line)) > maxLine {
		res.OversizedLines++
		return nil
	}
	if !hasMarker(line, markers) {
		return nil
	}
	return fn(trimCR(line))
}

func hasMarker(line []byte, markers [][]byte) bool {
	if len(markers) == 0 {
		return true
	}
	for _, m := range markers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

func trimCR(line []byte) []byte {
	return bytes.TrimRight(line, "\r")
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// anyDirExists reports whether at least one path is an existing directory.
func anyDirExists(paths []string) bool {
	for _, p := range paths {
		if dirExists(p) {
			return true
		}
	}
	return false
}

// firstLine reads at most max bytes from the start of a file and returns the first line.
// Used to read a session header without touching the rest of a multi-megabyte file.
func firstLine(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	limit := max
	if limit <= 0 {
		limit = 8 << 10
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	line := buf[:n]
	if idx := bytes.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return bytes.TrimRight(line, "\r"), nil
}
