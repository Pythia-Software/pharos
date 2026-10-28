//go:build darwin

package archive

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWALReaderHelperProcess holds a read snapshot of the catalog named in
// its environment until its stdin closes.
func TestWALReaderHelperProcess(t *testing.T) {
	path := os.Getenv("PHAROS_TEST_WAL_READER")
	if path == "" {
		t.Skip("helper process")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		os.Exit(2)
	}
	reader, err := db.Begin()
	if err != nil {
		os.Exit(3)
	}
	var count int
	if err := reader.QueryRow("SELECT COUNT(*) FROM meta").Scan(&count); err != nil {
		os.Exit(4)
	}
	fmt.Println("reading")
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

// Another process's read snapshot is named by pid and command line, found
// from the -shm file's read locks.
func TestWALPinNamesAnotherProcess(t *testing.T) {
	slowTest(t) // a helper process
	useDurableCatalog(t)
	override(t, &checkpointWaitMS, 50)
	override(t, &walPinBytes, 1)
	catalog, _ := testCatalog(t)
	writeRows(t, catalog.DB, "before-")
	helper := exec.Command(os.Args[0], "-test.run=^TestWALReaderHelperProcess$")
	helper.Env = append(os.Environ(), "PHAROS_TEST_WAL_READER="+catalog.Path)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); helper.Wait() }()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if strings.TrimSpace(line) != "reading" {
			t.Fatalf("helper: %q", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the helper did not start reading")
	}
	writeRows(t, catalog.DB, "after-")
	for range 2 { // a stall is the checkpoint not advancing between two checks
		catalog.wal.retryAt = time.Time{}
		catalog.boundWALAt(t.Context(), -1, priorityBackground)
	}
	pin := catalog.walPinned()
	want := fmt.Sprintf("another process: pid %d (", helper.Process.Pid)
	if pin == nil || !pin.Known || !strings.HasPrefix(pin.Holder, want) || !strings.Contains(pin.Holder, "TestWALReaderHelperProcess") || !strings.Contains(pin.Holder, "reading up to frame") {
		t.Fatalf("pinned by the helper: %+v", pin)
	}
}

// The probe borrows SQLite's descriptor of the -shm file and opens none of
// its own, so once the catalog closes, this process holds nothing on the
// library's volume.
func TestWALPinLeavesNoDescriptorAfterClose(t *testing.T) {
	useDurableCatalog(t)
	override(t, &checkpointWaitMS, 50)
	override(t, &walPinBytes, 1)
	catalog, _ := testCatalog(t)
	shm := catalog.Path + "-shm"
	writeRows(t, catalog.DB, "before-")
	reader, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow("SELECT COUNT(*) FROM meta").Scan(&count); err != nil {
		t.Fatal(err)
	}
	writeRows(t, catalog.DB, "after-")
	open := func() int {
		var want syscall.Stat_t
		if syscall.Stat(shm, &want) != nil {
			return 0
		}
		n := 0
		for _, fd := range openDescriptors() {
			var st syscall.Stat_t
			if syscall.Fstat(fd, &st) == nil && st.Dev == want.Dev && st.Ino == want.Ino {
				n++
			}
		}
		return n
	}
	before := open()
	for range 2 {
		catalog.wal.retryAt = time.Time{}
		catalog.boundWALAt(t.Context(), -1, priorityBackground)
	}
	if pin := catalog.walPinned(); pin == nil || !pin.Known {
		t.Fatalf("no diagnosis: %+v", pin)
	}
	if after := open(); after != before {
		t.Fatalf("the diagnosis changed the descriptors open on the -shm file: %d before, %d after", before, after)
	}
	reader.Rollback()
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if n := open(); n != 0 {
		t.Fatalf("%d descriptors of the -shm file are open after Close", n)
	}
}
