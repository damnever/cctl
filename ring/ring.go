// Package ring provides a simple FIFO circular slice.
package ring

const (
	// 8K if item is pointers on X64..
	highWatermark = 1024
	// 512B if item is pointers on X64..
	lowWatermark = 64
)

// Ring is a FIFO circular slice.
type Ring struct {
	items []interface{}
	head  int
	tail  int
	size  int
	cap   int
}

// New creates a new Ring.
func New() *Ring {
	return &Ring{
		items: make([]interface{}, 2, 2),
		head:  0,
		tail:  -1,
		size:  0,
		cap:   2,
	}
}

// Append appends item to "tail" of the ring.
func (r *Ring) Append(item interface{}) {
	if r.size == r.cap {
		if bufcap := r.cap * 2; bufcap <= highWatermark {
			r.cap = bufcap
		} else {
			r.cap += highWatermark
		}
		r.resize()
	}

	r.tail = r.next(r.tail)
	r.items[r.tail] = item
	r.size++
}

// Pop pops the item from "head" of the ring.
func (r *Ring) Pop() interface{} {
	item := r.Peek()
	if item != nil {
		r.items[r.head] = nil
		if r.head == r.tail {
			r.head = 0
			r.tail = -1
		} else {
			r.head = r.next(r.head)
		}

		r.size--
		if r.cap > lowWatermark && r.size <= r.cap/2 {
			r.cap = lowWatermark
			r.resize()
		}
	}
	return item
}

// Peek peeks the item from "head" of the ring.
func (r *Ring) Peek() interface{} {
	if r.size == 0 {
		return nil
	}
	return r.items[r.head]
}

// Len returns length(not capacity) of the ring.
func (r *Ring) Len() int {
	return r.size
}

func (r *Ring) next(i int) int {
	return (i + 1) % r.cap
}

func (r *Ring) resize() {
	items := r.items
	r.items = make([]interface{}, r.cap, r.cap)
	if r.tail < r.head {
		n := copy(r.items, items[r.head:])
		copy(r.items[n:], items[:r.tail+1])
	} else {
		copy(r.items, items)
	}
	r.head = 0
	r.tail = r.size - 1
}
