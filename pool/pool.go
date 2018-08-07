// Package pool manages reuseable resources, such as connections.
//
// Unlike other pool implementations, they fill the pool to full
// before reusing resources, this implementation only maintains
// possible minimal resources in the pool no matter how big the capacity is.
package pool

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrPoolClosed indicates pool is closed.
	ErrPoolClosed = errors.New("pool closed")
	// ErrPoolIsBusy indicates no available resource in pool currently.
	ErrPoolIsBusy = errors.New("pool is busy")
	// ErrMismatch indicates no Get/GetNoWait matched in previous call,
	// you will never see this error unless you do some thing wrong,
	// it will panic :-)
	ErrMismatch       = errors.New("no Get/GetNoWait matched in previous call")
	errNoIdleResource = errors.New("no idle resource available")

	_ctx         = context.TODO()
	_placeholder = struct{}{}
)

// Resource defines the interface that every resource must provide.
type Resource interface {
	Err() error
	Close() error
}

// TestableResource indicates the resource is testable, Test() will be called
// before every old resource return if TestOnBorrow/TestWhileIdle is set.
type TestableResource interface {
	Resource
	Test() error
}

// Options is used to configurate the Pool.
// Set TestOnBorrow(true), TestWhileIdle(true) and IdleTimeout(>0) together
// will always test the resource if resource is testable.
type Options struct {
	// ResourceFactory creates new resource.
	ResourceFactory func() (Resource, error)
	// Capacity sets the max pool size.
	Capacity int
	// IdleTimeout 0 indicate no idle timeout.
	IdleTimeout time.Duration
	// TestOnBorrow will test the resource before borrow and not idle for IdleTimeout,
	// the resource must implement the TestableResource interface.
	TestOnBorrow bool
	// TestWhileIdle will test the resource if resource idle for IdleTimeout before borrow,
	// if IdleTimeout less and equal than 0, this option is ignored.
	// the resource must implement the TestableResource interface.
	TestWhileIdle bool
}

func (opts Options) validate() error {
	if opts.ResourceFactory == nil {
		return errors.New("ResourceFactory can not be nil")
	}
	if opts.Capacity <= 0 {
		return errors.New("Capacity must be greater than 0")
	}
	return nil
}

type resourceWrapper struct {
	resource Resource
	idleAt   time.Time
}

func (r resourceWrapper) test(d time.Duration, testOnBorrow, testWhileIdle bool) bool {
	if d <= 0 { // No IdleTimeout
		if testOnBorrow {
			goto TEST
		}
	} else if r.idleAt.Add(d).After(time.Now()) {
		if testOnBorrow {
			goto TEST
		}
	} else { // Idle too long
		if testWhileIdle {
			goto TEST
		}
		goto BAD // No test, close directly
	}

	return true
TEST:
	if tr, ok := r.resource.(TestableResource); !ok || tr.Test() == nil {
		return true
	}
BAD:
	r.resource.Close()
	return false
}

// Pool is the container for resources.
type Pool struct {
	opts Options

	closel sync.RWMutex
	closed bool
	closec chan struct{}
	slotsc chan struct{}
	// Benchmark:
	// *resourceWrapper: 300000  5508 ns/op  960 B/op  20 allocs/op
	// resourceWrapper:  300000  4366 ns/op    0 B/op   0 allocs/op
	idlec chan resourceWrapper
}

// New creates a new Pool.
func New(opts Options) (*Pool, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	return &Pool{
		opts:   opts,
		closec: make(chan struct{}),
		slotsc: make(chan struct{}, opts.Capacity),
		idlec:  make(chan resourceWrapper, opts.Capacity),
	}, nil
}

// Get gets a resource from pool, block until resource available or context done.
func (p *Pool) Get(ctx context.Context) (Resource, error) {
	if r, err := p.getResource(ctx); err != errNoIdleResource {
		return r, err
	}

	done := ctx.Done()
	for {
		select {
		case <-done:
			return nil, ctx.Err()
		case <-p.closec:
			return nil, ErrPoolClosed
		case r := <-p.idlec:
			if r.test(p.opts.IdleTimeout, p.opts.TestOnBorrow, p.opts.TestWhileIdle) {
				return r.resource, nil
			}
			p.makeSlot()
		case p.slotsc <- _placeholder:
			r, err := p.newResource()
			if err != nil {
				p.makeSlot()
				return nil, err
			}
			return r, nil
		}
	}
}

// GetNoWait gets a resource from pool, return immediately.
func (p *Pool) GetNoWait() (Resource, error) {
	if r, err := p.getResource(_ctx); err != errNoIdleResource {
		return r, err
	}

	select {
	case p.slotsc <- _placeholder:
	case <-p.closec:
		return nil, ErrPoolClosed
	default:
		return nil, ErrPoolIsBusy
	}
	r, err := p.newResource()
	if err != nil {
		p.makeSlot()
		return nil, err
	}
	return r, nil
}

func (p *Pool) getResource(ctx context.Context) (Resource, error) {
	done := ctx.Done()
	for {
		select {
		case <-done:
			return nil, ctx.Err()
		case <-p.closec:
			return nil, ErrPoolClosed
		default:
			select {
			case r := <-p.idlec:
				if r.test(p.opts.IdleTimeout, p.opts.TestOnBorrow, p.opts.TestWhileIdle) {
					return r.resource, nil
				}
				p.makeSlot()
			default:
				return nil, errNoIdleResource
			}
		}
	}
}

func (p *Pool) newResource() (Resource, error) {
	r, err := p.opts.ResourceFactory()
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Put puts back resource into pool, must record the error which can
// make resource unusable and let it returned by Err()
// so clean up work could be done properly.
// After pool close, Put is responsible for closing all in using resources.
func (p *Pool) Put(r Resource) error {
	if r == nil {
		return nil
	}

	p.closel.RLock()
	defer p.closel.RUnlock()
	if p.closed {
		return r.Close()
	}
	if r.Err() != nil {
		p.makeSlot()
		return r.Close()
	}

	rw := resourceWrapper{resource: r, idleAt: time.Now()}
	select {
	case p.idlec <- rw:
		return nil
	default:
		panic(ErrMismatch)
	}
}

func (p *Pool) makeSlot() {
	select {
	case <-p.slotsc:
	default:
		panic(ErrMismatch)
	}
}

// Close closes the pool and all idle resources in the pool.
func (p *Pool) Close() error {
	// Resources may still get chance Get/GetNoWait from the pool when closing,
	// but we ensure no resource can put back into pool, so after close,
	// Put operation will close in using resources.
	p.closel.Lock()
	defer p.closel.Unlock()

	if p.closed {
		return ErrPoolClosed
	}
	p.closed = true
	close(p.closec)

FILL_SLOTS:
	for {
		select {
		case p.slotsc <- _placeholder:
		default:
			break FILL_SLOTS
		}
	}

	for {
		select {
		case r := <-p.idlec:
			r.resource.Close()
		default:
			return nil
		}
	}
}
