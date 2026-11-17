package disk

import (
	"bytes"
	"os"
)

// CapFile cuts a file larger than max to its newest max/2 bytes, from the
// start of a line, behind the line note returns (given the first line
// kept). The newer half is written to a new file renamed over the old, so a
// reader sees either whole file. It returns the file's size after.
//
// The file must not be open for writing: on Windows the rename fails, and
// elsewhere the writer would go on writing to the old, unlinked file.
func CapFile(path string, max int64, note func(first []byte) string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	size := info.Size()
	if size <= max {
		return size
	}
	f, err := os.Open(path)
	if err != nil {
		return size
	}
	keep := max / 2
	tail := make([]byte, keep)
	n, err := f.ReadAt(tail, info.Size()-keep)
	prev := make([]byte, 1)
	_, perr := f.ReadAt(prev, info.Size()-keep-1)
	// Closed before the rename below: Windows won't rename over an open file.
	f.Close()
	if err != nil && n < len(tail) {
		return size
	}
	// Drop the line the cut lands in, unless it lands right after a newline.
	if perr != nil || prev[0] != '\n' {
		i := bytes.IndexByte(tail, '\n')
		if i < 0 {
			tail = nil // one line longer than keep: nothing whole to keep
		} else {
			tail = tail[i+1:]
		}
	}
	first, _, _ := bytes.Cut(tail, []byte("\n"))
	tmp := path + ".trim"
	out, err := os.Create(tmp)
	if err != nil {
		return size
	}
	head := note(first)
	_, werr := out.WriteString(head)
	if werr == nil {
		_, werr = out.Write(tail)
	}
	if cerr := out.Close(); werr != nil || cerr != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return size
	}
	return int64(len(head) + len(tail))
}

// TrimmedNote is the note CapFile's callers put where a log was cut.
func TrimmedNote(max int64) string {
	return "✂ earlier output trimmed: this log keeps about its newest " + Size(uint64(max/2))
}
