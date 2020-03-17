package logger

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// bufferSize sizes the buffer associated with each log file. It's large
// so that log records can accumulate without the logging thread blocking
// on disk I/O. The flushDaemon will block instead.
const bufferSize = 256 * 1024

var (
	pid = os.Getpid()
)

// shortHostname returns its argument, truncating at the first period.
// For instance, given "www.google.com" it returns "www".
func shortHostname(hostname string) string {
	if i := strings.Index(hostname, "."); i >= 0 {
		return hostname[:i]
	}
	return hostname
}

// syncBuffer joins a bufio.Writer to its underlying file, providing access to the
// file's Sync method and providing a wrapper for the Write method that provides log
// file rotation. There are conflicting methods, so the file cannot be embedded.
// l.mu is held for all its methods.
type syncBuffer struct {
	logger *Logger
	*bufio.Writer
	file   *os.File
	sev    Severity
	nbytes uint64 // The number of bytes written to this file
}

func (sb *syncBuffer) Sync() error {
	return sb.file.Sync()
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	if sb.nbytes+uint64(len(p)) >= sb.logger.getMaxSize() {
		sb.rotateFile(time.Now())
	}
	n, err = sb.Writer.Write(p)
	sb.nbytes += uint64(n)

	return
}

// rotateFile closes the syncBuffer's file and starts a new one.
func (sb *syncBuffer) rotateFile(now time.Time) error {
	if sb.file != nil {
		sb.Flush()
		sb.file.Close()
	}
	var err error
	sb.file, _, err = sb.create(severityName[sb.sev], now)
	sb.nbytes = 0
	if err != nil {
		return err
	}

	sb.Writer = bufio.NewWriterSize(sb.file, bufferSize)

	// Write header.
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Log file created at: %s\n", now.Format("2006/01/02 15:04:05"))
	fmt.Fprintf(&buf, "Binary: Built with %s %s for %s/%s\n", runtime.Compiler, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&buf, "Log line format: [mm-dd hh:mm:ss.uuuuuu L file:line] msg\n")
	n, err := sb.file.Write(buf.Bytes())
	sb.nbytes += uint64(n)
	return err
}

// logName returns a new log file name containing tag, with start time t, and
// the name for the symlink for tag.
func (sb *syncBuffer) logName(tag string, t time.Time) (name, link string) {
	name = fmt.Sprintf("%s.%s.%04d%02d%02d-%02d%02d%02d.%d.log",
		sb.logger.getLogName(),
		tag,
		t.Year(),
		t.Month(),
		t.Day(),
		t.Hour(),
		t.Minute(),
		t.Second(),
		pid)
	return name, sb.logger.getLogName() + "." + tag
}

// create creates a new log file and returns the file and its filename, which
// contains tag ("INFO", "FATAL", etc.) and t.  If the file is created
// successfully, create also attempts to update the symlink for that tag, ignoring
// errors.
func (sb *syncBuffer) create(tag string, t time.Time) (f *os.File, filename string, err error) {
	name, link := sb.logName(tag, t)

	dir := sb.logger.getLogDir()
	os.MkdirAll(dir, 0666)
	fname := filepath.Join(dir, name)
	f, err = os.OpenFile(fname, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err == nil {
		symlink := filepath.Join(dir, link)
		os.Remove(symlink)        // ignore err
		os.Symlink(name, symlink) // ignore err
		return f, fname, nil
	}

	return nil, "", err
}
