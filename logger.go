package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Severity 日志等级
type Severity int32

/*
 */
const (
	SeverityDebug Severity = iota
	SeverityInfo
	SeverityWarning
	SeverityError
	severityCount
)

const flushInterval = 30 * time.Second

var severityChar = "DIWE"

var severityName = []string{
	SeverityDebug:   "DEBUG",
	SeverityInfo:    "INFO",
	SeverityWarning: "WARNING",
	SeverityError:   "ERROR",
}

func (s *Severity) get() Severity {
	return Severity(atomic.LoadInt32((*int32)(s)))
}

func (s *Severity) set(val Severity) {
	atomic.StoreInt32((*int32)(s), int32(val))
}

type flushSyncWriter interface {
	Flush() error
	Sync() error
	io.Writer
}

// Logger 记录器
type Logger struct {
	mu            sync.Mutex
	file          [severityCount]flushSyncWriter
	maxSize       uint64
	logDir        string
	logName       string
	severityLimit Severity
}

func init() {
	go DefaultLogger.flushDaemon()
}

func (l *Logger) formatHeader(s Severity, file string, line int) *buffer {
	now := time.Now()
	if line < 0 {
		line = 0 // not a real line number, but acceptable to someDigits
	}
	if s >= severityCount {
		s = SeverityInfo // for safety.
	}
	buf := _bufferPool.getBuffer()

	// Avoid Fprintf, for speed. The format is so simple that we can do it quickly by hand.
	// It's worth about 3X. Fprintf is hard.
	_, month, day := now.Date()
	hour, minute, second := now.Clock()
	// [mm-dd hh:mm:ss.uuuuuu L file:line]
	buf.tmp[0] = '['
	buf.twoDigits(1, int(month))
	buf.tmp[3] = '-'
	buf.twoDigits(4, day)
	buf.tmp[6] = ' '
	buf.twoDigits(7, hour)
	buf.tmp[9] = ':'
	buf.twoDigits(10, minute)
	buf.tmp[12] = ':'
	buf.twoDigits(13, second)
	buf.tmp[15] = '.'
	buf.nDigits(6, 16, now.Nanosecond()/1000, '0')
	buf.tmp[22] = ' '
	buf.tmp[23] = severityChar[s]
	buf.tmp[24] = ' '
	buf.Write(buf.tmp[:25])
	buf.WriteString(file)
	buf.tmp[0] = ':'
	n := buf.someDigits(1, line)
	buf.tmp[n+1] = ']'
	buf.tmp[n+2] = ' '
	buf.Write(buf.tmp[:n+3])
	return buf
}

func (l *Logger) header(s Severity, depth int) *buffer {
	_, file, line, ok := runtime.Caller(3 + depth)
	if !ok {
		file = "???"
		line = 1
	} else {
		slash := strings.LastIndex(file, "/")
		if slash >= 0 {
			file = file[slash+1:]
		}
	}
	return l.formatHeader(s, file, line)
}

// createFiles creates all the log files for Severity from sev down to infoLog.
// l.mu is held.
func (l *Logger) createFiles(sev Severity) error {
	now := time.Now()
	// Files are created in decreasing Severity order, so as soon as we find one
	// has already been created, we can stop.
	for s := sev; s >= l.severityLimit && l.file[s] == nil; s-- {
		sb := &syncBuffer{
			logger: l,
			sev:    s,
		}
		if err := sb.rotateFile(now); err != nil {
			return err
		}
		l.file[s] = sb
	}
	return nil
}

// output writes the data to the log files and releases the buffer.
func (l *Logger) output(s Severity, buf *buffer) {
	l.mu.Lock()
	data := buf.Bytes()
	if l.file[s] == nil {
		if err := l.createFiles(s); err != nil {
			return
		}
	}
	switch s {
	case SeverityError:
		l.file[SeverityError].Write(data)
		fallthrough
	case SeverityWarning:
		l.file[SeverityWarning].Write(data)
		fallthrough
	case SeverityInfo:
		l.file[SeverityInfo].Write(data)
		fallthrough
	case SeverityDebug:
		l.file[SeverityDebug].Write(data)
		os.Stderr.Write(data)
	}

	l.mu.Unlock()
	_bufferPool.putBuffer(buf)
	if s >= SeverityError {
		l.Flush()
	}
}

func convDirAbs(dir string) string {
	workDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return dir
	}
	return filepath.Join(workDir, dir)
}

func (l *Logger) getLogDir() string {
	if l.logDir == "" {
		l.logDir = convDirAbs("./log/")
	}
	return l.logDir
}

// SetLogDir 设置日志文件路径
func (l *Logger) SetLogDir(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	dir = convDirAbs(dir)
	if dir == l.logDir {
		return
	}
	l.flushAll()
	l.resetFiles()
	l.logDir = dir
}

func (l *Logger) getLogName() string {
	if l.logName == "" {
		l.logName = filepath.Base(os.Args[0])
	}
	return l.logName
}

// SetLogName 设置日志文件名
func (l *Logger) SetLogName(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if name == l.logName {
		return
	}
	l.flushAll()
	l.resetFiles()
	l.logName = name
}

// SetSeverityLimit 设置日志打印级别
func (l *Logger) SetSeverityLimit(s Severity) {
	l.severityLimit.set(s)
}

// flushDaemon periodically flushes the log file buffers.
func (l *Logger) flushDaemon() {
	for range time.NewTicker(flushInterval).C {
		l.Flush()
	}
}

func (l *Logger) getMaxSize() uint64 {
	if l.maxSize == 0 {
		l.maxSize = 1024 * 1024 * 4
	}
	return l.maxSize
}

// SetMaxSize 设置日志文件size
func (l *Logger) SetMaxSize(s uint64) {
	l.maxSize = s
}

func (l *Logger) resetFiles() {
	for idx := range l.file {
		l.file[idx] = nil
	}
}

// Flush 将缓冲写入文件
func (l *Logger) Flush() {
	l.mu.Lock()
	l.flushAll()
	l.mu.Unlock()
}

// flushAll flushes all the logs and attempts to "sync" their data to disk.
// l.mu is held.
func (l *Logger) flushAll() {
	// Flush from fatal down, in case there's trouble flushing.
	for s := SeverityError; s >= SeverityDebug; s-- {
		file := l.file[s]
		if file != nil {
			file.Flush() // ignore error
			file.Sync()  // ignore error
		}
	}
}

func (l *Logger) println(s Severity, args ...interface{}) {
	if s < l.severityLimit.get() {
		return
	}
	buf := l.header(s, 0)
	fmt.Fprintln(buf, args...)
	l.output(s, buf)
}

func (l *Logger) printf(s Severity, format string, args ...interface{}) {
	if s < l.severityLimit.get() {
		return
	}
	buf := l.header(s, 0)
	fmt.Fprintf(buf, format, args...)
	if buf.Bytes()[buf.Len()-1] != '\n' {
		buf.WriteByte('\n')
	}
	l.output(s, buf)
}

// Debug 写Debug日志
func (l *Logger) Debug(args ...interface{}) {
	l.println(SeverityDebug, args...)
}

// Info 写Info日志
func (l *Logger) Info(args ...interface{}) {
	l.println(SeverityInfo, args...)
}

// Warning 写Warning日志
func (l *Logger) Warning(args ...interface{}) {
	l.println(SeverityWarning, args...)
}

// Error 写Error日志
func (l *Logger) Error(args ...interface{}) {
	l.println(SeverityError, args...)
}

// Debugf 写格式化Debug日志
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.printf(SeverityDebug, format, args...)
}

// Infof 写格式化Info日志
func (l *Logger) Infof(format string, args ...interface{}) {
	l.printf(SeverityInfo, format, args...)
}

// Warningf 写格式化Warning日志
func (l *Logger) Warningf(format string, args ...interface{}) {
	l.printf(SeverityWarning, format, args...)
}

// Errorf 写格式化Error日志
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.printf(SeverityError, format, args...)
}

// DefaultLogger 默认日志记录器
var DefaultLogger Logger

// Debug 默认logger快捷调用
func Debug(args ...interface{}) {
	DefaultLogger.println(SeverityDebug, args...)
}

// Info 默认logger快捷调用
func Info(args ...interface{}) {
	DefaultLogger.println(SeverityInfo, args...)
}

// Warning 默认logger快捷调用
func Warning(args ...interface{}) {
	DefaultLogger.println(SeverityWarning, args...)
}

// Error 默认logger快捷调用
func Error(args ...interface{}) {
	DefaultLogger.println(SeverityError, args...)
}

// Debugf 默认logger快捷调用
func Debugf(format string, args ...interface{}) {
	DefaultLogger.printf(SeverityDebug, format, args...)
}

// Infof 默认logger快捷调用
func Infof(format string, args ...interface{}) {
	DefaultLogger.printf(SeverityInfo, format, args...)
}

// Warningf 默认logger快捷调用
func Warningf(format string, args ...interface{}) {
	DefaultLogger.printf(SeverityWarning, format, args...)
}

// Errorf 默认logger快捷调用
func Errorf(format string, args ...interface{}) {
	DefaultLogger.printf(SeverityError, format, args...)
}
