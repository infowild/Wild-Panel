package job

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The scrape used to re-read the WHOLE access log every 10 seconds while the log grew
// for an hour between truncations: 360 passes over an ever-larger file, three regexes
// per line. That cost scales with user traffic, which is how it arrives as "the panel
// suddenly saturates a core" with nothing in the config having changed.
//
// These pin the incremental read on the real production function.

func fmtLine(sec int, ip, email string) string {
	return fmt.Sprintf("2026/09/22 10:00:%02d from tcp:%s:12345 accepted tcp:example.com:443 [in -> direct] email: %s",
		sec, ip, email)
}

func appendLines(t *testing.T, path string, truncate bool, lines ...string) {
	t.Helper()
	flag := os.O_CREATE | os.O_WRONLY
	if truncate {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// scan opens the file and runs the production scanner, returning how many distinct
// (email, ip) pairs this pass observed and the new offset.
func scan(t *testing.T, path string, offset int64) (int, int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	observed, next := scanAccessLog(f, offset)
	n := 0
	for _, ips := range observed {
		n += len(ips)
	}
	return n, next
}

func TestAccessLogIsReadIncrementally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	appendLines(t, path, true, fmtLine(1, "1.1.1.1", "a@x"), fmtLine(2, "2.2.2.2", "b@x"))

	n, off := scan(t, path, 0)
	if n != 2 {
		t.Fatalf("first pass observed %d entries, want 2", n)
	}
	if off == 0 {
		t.Fatal("offset was not advanced")
	}

	// Nothing appended: the next pass must do no work at all. Before the fix this
	// returned 2 again, every 10 seconds, forever.
	n2, off2 := scan(t, path, off)
	if n2 != 0 {
		t.Fatalf("second pass re-parsed %d entries; it must read only new bytes", n2)
	}
	if off2 != off {
		t.Fatalf("offset moved (%d -> %d) with nothing appended", off, off2)
	}

	// One new line: exactly one entry of work, not three.
	appendLines(t, path, false, fmtLine(3, "3.3.3.3", "c@x"))
	n3, _ := scan(t, path, off2)
	if n3 != 1 {
		t.Fatalf("third pass observed %d entries, want exactly the 1 appended", n3)
	}
}

// clearAccessLog truncates hourly. The offset must reset, or every line Xray writes
// afterwards is skipped forever and the IP log silently stops updating.
func TestTruncationResetsTheOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	appendLines(t, path, true, fmtLine(1, "1.1.1.1", "a@x"), fmtLine(2, "2.2.2.2", "b@x"))
	_, off := scan(t, path, 0)

	// Truncate and write fresh, shorter content — what clearAccessLog leaves behind.
	appendLines(t, path, true, fmtLine(9, "9.9.9.9", "z@x"))

	n, _ := scan(t, path, off)
	if n != 1 {
		t.Fatalf("after truncation the scrape observed %d entries, want 1: a stale offset skips the new file", n)
	}
}

// Xray appends while we read, so the tail can be half a line. Committing it would make
// the next pass resume mid-line and lose that entry entirely.
func TestPartialTrailingLineIsNotLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	appendLines(t, path, true, fmtLine(1, "1.1.1.1", "a@x"))

	partial := fmtLine(2, "2.2.2.2", "b@x")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(partial[:len(partial)/2]) // no newline yet
	f.Close()

	n1, off := scan(t, path, 0)
	if n1 != 1 {
		t.Fatalf("first pass observed %d entries, want only the 1 complete line", n1)
	}

	// The rest of that line arrives.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(partial[len(partial)/2:] + "\n")
	f.Close()

	n2, _ := scan(t, path, off)
	if n2 != 1 {
		t.Fatalf("the completed line was observed %d times, want exactly 1", n2)
	}
}
