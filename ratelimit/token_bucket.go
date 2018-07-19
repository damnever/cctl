package ratelimit

import (
	"context"
	"sync"
	"time"
)

type tokenBucketRateLimiter struct {
	limit int
	reqc  chan *tokenReq
	stopc chan struct{}
	donec chan struct{}
}

// NewTokenBucketRateLimiter creates a new token bucket RateLimiter.
//
// QPS:
//     l := NewTokenBucketRateLimiter(1000) // 1000 queries per second
//     defer l.Close()
//     err := l.Take(ctx, 1) // take a token
//
// BPS:
//     l := NewTokenBucketRateLimiter(200*(1<<20)) // 200MB per second
//     defer l.Close()
//     err := l.Take(ctx, 1<<20) // take 1MB
func NewTokenBucketRateLimiter(limit int) RateLimiter {
	l := &tokenBucketRateLimiter{
		limit: limit,
		reqc:  make(chan *tokenReq, 4), // No buffer??
		stopc: make(chan struct{}),
		donec: make(chan struct{}),
	}
	go l.scheduling()
	return l
}

func (l *tokenBucketRateLimiter) scheduling() {
	defer close(l.donec)
	interval := time.Second / time.Duration(l.limit)
	if interval < time.Millisecond { // Try the best to avoid ticks droping..
		interval = time.Millisecond
	}

	bucket := 0
	token := l.limit / int(time.Second/interval) // Approximately..
	// Eventually, the size of ring buffer will stay constant, I think..
	pending := newTokenReqRing()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopc:
			for pending.length() > 0 && bucket > 0 {
				req := pending.popfirst()
				if !req.iscanceled() && bucket >= req.size {
					bucket -= req.markdone()
				}
			}
			return
		case <-ticker.C:
			if x := bucket + token; x < l.limit {
				bucket = x
			} else {
				bucket = l.limit
			}

			for pending.length() > 0 && bucket > 0 {
				req := pending.fisrt()
				if req.iscanceled() {
					pending.popfirst()
					continue
				}
				if bucket < req.size {
					break
				}
				bucket -= req.markdone()
				pending.popfirst()
			}
		case req := <-l.reqc:
			if req.size > bucket {
				pending.add(req)
			} else {
				bucket -= req.markdone()
			}
		}
	}
}

func (l *tokenBucketRateLimiter) Take(ctx context.Context, size int) error {
	cancelc := ctx.Done()
	// XXX(damnever): reuse tokenReq?
	req := &tokenReq{
		size:    size,
		cancelc: cancelc,
		donec:   make(chan struct{}),
	}

	select {
	case <-cancelc:
		return ctx.Err()
	case l.reqc <- req:
	}

	select {
	case <-cancelc:
		if !req.isdone() {
			return ctx.Err()
		}
	case <-req.donec:
	}
	return nil
}

func (l *tokenBucketRateLimiter) Close() error {
	close(l.stopc)
	<-l.donec
	return nil
}

type tokenReq struct {
	l       sync.Mutex
	size    int
	cancelc <-chan struct{}
	donec   chan struct{}
}

func (r *tokenReq) iscanceled() bool {
	select {
	case <-r.cancelc:
		return true
	default:
		return false
	}
}

func (r *tokenReq) markdone() (size int) {
	r.l.Lock()
	defer r.l.Unlock()
	select {
	case <-r.cancelc:
	default:
		if r.donec != nil {
			close(r.donec)
			size = r.size
		}
	}
	return
}

func (r *tokenReq) isdone() bool {
	r.l.Lock()
	defer r.l.Unlock()
	select {
	case <-r.donec:
		return true
	default:
		r.donec = nil
		return false
	}
}

type tokenReqRing struct {
	reqs  []*tokenReq
	start int
	end   int
	size  int
	cap   int
}

func newTokenReqRing() *tokenReqRing {
	return &tokenReqRing{
		reqs:  make([]*tokenReq, 2, 2),
		start: -1,
		end:   -1,
		size:  0,
		cap:   2,
	}
}

func (r *tokenReqRing) add(req *tokenReq) {
	if r.size < r.cap {
		r.end = (r.end + 1) % r.cap
		if r.size == 0 {
			r.start = 0
			r.end = 0
		}
	} else {
		old := r.reqs
		next := r.cap
		if cap := r.cap * 2; cap <= 512 {
			r.cap = cap
		} else {
			r.cap += 2
		}
		r.reqs = make([]*tokenReq, r.cap, r.cap)
		if r.end < r.start {
			copy(r.reqs, old[r.start:])
			copy(r.reqs[next-r.start:], old[:r.end+1])
		} else {
			copy(r.reqs, old[:])
		}
		r.end = next
		r.start = 0
	}
	r.size++
	r.reqs[r.end] = req
}

func (r *tokenReqRing) length() int {
	return r.size
}

func (r *tokenReqRing) fisrt() *tokenReq {
	return r.reqs[r.start]
}

func (r *tokenReqRing) popfirst() *tokenReq {
	// XXX(damnever): free up memory?
	r.size--
	first := r.reqs[r.start]
	r.reqs[r.start] = nil
	if r.start != r.end {
		r.start = (r.start + 1) % r.cap
	} else {
		r.start = -1
		r.end = -1
	}
	return first
}
