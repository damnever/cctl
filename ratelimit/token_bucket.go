package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/damnever/goctl/ring"
)

type tokenBucketRateLimiter struct {
	reqc  chan *tokenReq
	semc  chan struct{}
	stopc chan struct{}
	donec chan struct{}
}

// NewTokenBucketRateLimiter creates a new token bucket RateLimiter.
//
// NOTE: If the size you Take is too large or too small(compare to limit/500),
// it will not reach the max possible limit, except you only have few goroutines
// in your nonbusy system, if that matters, test it before using it.
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
		reqc:  make(chan *tokenReq),
		stopc: make(chan struct{}),
		donec: make(chan struct{}),
	}
	go l.scheduling(limit)
	return l
}

func (l *tokenBucketRateLimiter) scheduling(limit int) {
	defer close(l.donec)

	interval := time.Second / time.Duration(limit)
	if interval < 2*time.Millisecond { // Try the best to avoid ticks droping..
		interval = 2 * time.Millisecond
	}
	token := limit / int(time.Second/interval) // Approximately..
	bucket := token

	// Eventually, the size of ring buffer will stay constant=maxpending
	maxpending := (token + 1) & (^1)
	if maxpending < 1 {
		maxpending = 4
	} else if maxpending > 64 {
		maxpending = 64
	}
	pending := ring.New()
	tryFeedPending := func() {
		for pending.Len() > 0 && bucket > 0 {
			req := pending.Peek().(*tokenReq)
			if req.iscanceled() {
				pending.Pop()
				continue
			}
			if bucket < req.size {
				break
			}
			bucket -= req.markdone()
			pending.Pop()
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	reqc := l.reqc
	for {
		select {
		case <-l.stopc:
			for pending.Len() > 0 && bucket > 0 {
				req := pending.Pop().(*tokenReq)
				if !req.iscanceled() && bucket >= req.size {
					bucket -= req.markdone()
				}
			}
			return
		case <-ticker.C:
			if x := bucket + token; x < limit {
				bucket = x
			} else {
				bucket = limit
			}
			tryFeedPending()
			if pending.Len() >= maxpending {
				reqc = nil // Try the best? to reduce scheduling time.
			} else {
				reqc = l.reqc
			}
		case req := <-reqc:
			// Since the reqc is unbuffered, we have no need to check if it's canceled.
			// If there is pending requests and we let the newest pass, the largest
			// requests may be starved.
			if pending.Len() == 0 && req.size <= bucket {
				bucket -= req.markdone()
			} else {
				pending.Append(req)
				tryFeedPending()
			}
			if pending.Len() >= maxpending {
				reqc = nil
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
