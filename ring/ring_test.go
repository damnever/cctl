package ring

import (
	"runtime"
	"testing"
)

func TestRing(t *testing.T) {
	r := New()
	r.Append(1)
	r.Append(2)
	assert(t, 2, r.cap)
	assert(t, 2, r.Len())
	assert(t, 0, r.head)
	assert(t, 1, r.tail)

	r.Append(3)
	assert(t, 4, r.cap)
	assert(t, r.Len(), 3)
	assert(t, 0, r.head)
	assert(t, 2, r.tail)
	r.Append(4)
	assert(t, 1, r.Pop())
	assert(t, 3, r.size)
	assert(t, 1, r.head)
	len := r.Len()
	for i := 2; i <= 4; i++ {
		assert(t, i, r.Pop())
		len--
		assert(t, len, r.Len())
	}

	assert(t, 0, r.Len())
	assert(t, nil, r.Pop())
	assert(t, nil, r.Peek())

	assert(t, 0, r.head)
	assert(t, -1, r.tail)
	r.items[0] = 3
	r.items[1] = 4
	r.items[2] = 1
	r.items[3] = 2
	r.tail = 1
	r.head = 2
	r.size = 4

	assert(t, 1, r.Peek())
	r.Append(5)
	assert(t, 4, r.tail)
	for i := 0; i < r.tail; i++ {
		assert(t, i+1, r.Pop())
	}
}

func assert(t *testing.T, expect interface{}, actual interface{}) {
	_, fileName, line, _ := runtime.Caller(1)
	if actual != expect {
		t.Fatalf("expect %v, got %v at (%v:%v)\n", expect, actual, fileName, line)
	}
}
