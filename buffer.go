package logger

import (
	"bytes"
	"sync"
)

const digits = "0123456789"

type buffer struct {
	bytes.Buffer
	tmp  [64]byte
	next *buffer
}

// twoDigits formats a zero-prefixed two-digit integer at buf.tmp[i].
func (buf *buffer) twoDigits(i, d int) {
	buf.tmp[i+1] = digits[d%10]
	d /= 10
	buf.tmp[i] = digits[d%10]
}

// nDigits formats an n-digit integer at buf.tmp[i],
// padding with pad on the left.
// It assumes d >= 0.
func (buf *buffer) nDigits(n, i, d int, pad byte) {
	j := n - 1
	for ; j >= 0 && d > 0; j-- {
		buf.tmp[i+j] = digits[d%10]
		d /= 10
	}
	for ; j >= 0; j-- {
		buf.tmp[i+j] = pad
	}
}

// someDigits formats a zero-prefixed variable-width integer at buf.tmp[i].
func (buf *buffer) someDigits(i, d int) int {
	// Print into the top, then copy down. We know there's space for at least
	// a 10-digit number.
	j := len(buf.tmp)
	for {
		j--
		buf.tmp[j] = digits[d%10]
		d /= 10
		if d == 0 {
			break
		}
	}
	return copy(buf.tmp[i:], buf.tmp[j:])
}

var _bufferPool bufferPool

type bufferPool struct {
	freeList *buffer
	mtx      sync.Mutex
}

func (bp *bufferPool) getBuffer() *buffer {
	bp.mtx.Lock()
	b := bp.freeList
	if b != nil {
		bp.freeList = b.next
	}
	bp.mtx.Unlock()
	if b == nil {
		b = new(buffer)
	} else {
		b.next = nil
		b.Reset()
	}
	return b
}

func (bp *bufferPool) putBuffer(b *buffer) {
	if b.Len() >= 512 {
		return
	}
	bp.mtx.Lock()
	b.next = bp.freeList
	bp.freeList = b
	bp.mtx.Unlock()
}
