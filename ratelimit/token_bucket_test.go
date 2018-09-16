package ratelimit

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQPSLikeRateLimit(t *testing.T) {
	l := NewTokenBucketRateLimiter(1000)
	defer l.Close()

	start := time.Now()
	for i := 0; i < 1000; i++ {
		if err := l.Take(context.TODO(), 1); err != nil {
			t.Fatalf("expect nil, got: %v", err)
		}
	}

	elapsed := time.Since(start)
	if !(elapsed <= time.Second+33*time.Millisecond && elapsed >= time.Second-10*time.Millisecond) {
		t.Fatalf("expect time range[1s, 1s+33ms], got: %v", elapsed)
	}
}

func TestBPSLikeRateLimit(t *testing.T) {
	MB10 := 10 * (1 << 20)
	l := NewTokenBucketRateLimiter(MB10)
	defer l.Close()

	start := time.Now()
	count := 0
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for count < MB10 {
		size := r.Intn((1 << 10))
		if size == 0 {
			size = 1
		}
		if err := l.Take(context.TODO(), size); err != nil {
			t.Fatalf("expect nil, got: %v", err)
		}
		count += size
	}

	elapsed := time.Since(start)
	if !(elapsed <= time.Second+33*time.Millisecond && elapsed >= time.Second-10*time.Millisecond) {
		t.Fatalf("expect time range[1s, 1s+33ms], got: %v", elapsed)
	}
}

func TestNoStarving(t *testing.T) {
	MB200 := 200 * (1 << 20)
	l := NewTokenBucketRateLimiter(MB200)
	defer l.Close()

	ctx := context.TODO()
	minsize := MB200/int(time.Second/time.Millisecond) - 1
	sizes := []int{minsize, minsize*2 + 3}
	ngo, sizego := 32, MB200/16

	var small, big int32
	wg := sync.WaitGroup{}
	for i := 0; i < ngo; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for count, next := 0, 0; count < sizego; next = (next + 1) % 2 {
				func() {
					cctx, cancel := context.WithTimeout(ctx, 33*time.Millisecond)
					defer cancel()
					size := r.Intn(sizes[next])
					if size == 0 {
						size = sizes[next]
					}
					if l.Take(cctx, size) == nil {
						count += size
						if next == 0 {
							atomic.AddInt32(&small, 1)
						} else {
							atomic.AddInt32(&big, 1)
						}
					} else {
						// panic("unbelievable")
					}
				}()
			}
		}()
	}
	wg.Wait()

	if (big >= small*2 || big*2 <= small) || small-big > int32(float64((big+big)/2)*0.08) {
		t.Fatalf("somebody is starving: %d vs %d", small, big)
	}
	fmt.Printf("B(%d) vs S(%v)\n", big, small)
}

func TestConcurrentOPS(t *testing.T) {
	MB512 := 512 * (1 << 20)
	l := NewTokenBucketRateLimiter(MB512)

	ctx := context.TODO()
	wg := sync.WaitGroup{}
	start := time.Now()

	ngo, sizego := 256, MB512/64
	var canceled int32
	for i := 0; i < ngo; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for count := 0; count < sizego; {
				func() {
					timeout := time.Duration(r.Intn(66)) * time.Millisecond
					if timeout == 0 {
						timeout = 1 * time.Second
					}
					cctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()

					size := r.Intn(5234790) + 8092
					if size == 0 {
						size = 1
					}
					if l.Take(cctx, size) == nil {
						count += size
					} else {
						atomic.AddInt32(&canceled, 1)
						time.Sleep(10 * time.Millisecond)
					}
				}()
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)
	l.Close()

	minexpect := time.Duration(ngo*sizego/MB512) * time.Second
	if !(elapsed >= minexpect && elapsed <= minexpect+2000*time.Millisecond) {
		t.Fatalf("expect time range[%v, %v+2000ms], got: %v", minexpect, minexpect, elapsed)
	}
	if canceled < 10 {
		t.Fatalf("expect 10 canceled, got: %d", canceled)
	}
}

func assert(t *testing.T, actual interface{}, expect interface{}) {
	_, fileName, line, _ := runtime.Caller(1)
	if actual != expect {
		t.Fatalf("expect %v, got %v at (%v:%v)\n", expect, actual, fileName, line)
	}
}
