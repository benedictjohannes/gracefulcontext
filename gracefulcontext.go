package gracefulcontext

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/pkg/errors"
)

var ErrContextCancelled = errors.New("graceful context is cancelled")
var ErrContextCancelPending = errors.New("graceful context delayed cancellation pending")
var ErrCleanupFuncPending = errors.New("graceful context cleanupFunc is running")
var ErrCleanupFuncTimeout = errors.New("graceful context cleanupFunc exceeds timeout")
var ErrContextCancelDone = errors.New("graceful context cancelDoneChan is already closed")

type gracefulContext struct {
	// set only on initiation
	parent                     context.Context
	immediateCancelPropagation bool

	cancel context.CancelFunc

	cleanupFuncStartChan chan struct{}
	cleanupFuncDoneChan  chan struct{}
	cleanupFuncErrChan   chan error

	doneChan chan struct{}

	mu  sync.Mutex
	err error
}

const (
	chanCleanupFuncStart = iota
	chanCleanupFuncDone
	chanDone
)

type gracefulContextConfig struct {
	cleanupTimeout             time.Duration
	cleanupFunc                func() error
	immediateCancelPropagation bool

	deadline time.Time
	timeout  time.Duration
	parent   context.Context

	selfCreatedTimerCtxCanceler func()

	key   interface{}
	value interface{}
}

func NewGracefulContext(parent context.Context) *gracefulContextConfig {
	gcc := &gracefulContextConfig{}
	if parent == nil {
		gcc.parent = context.Background()
	} else {
		gcc.parent = parent
	}
	return gcc
}

// WithValue creates a context.WithValue context
// replacing the current parent context,
// it can be called multiple times.
// instead of panicking, it will
// fail silently when provided
// key is not comparable
func (gcc *gracefulContextConfig) WithValue(key interface{}, value interface{}) *gracefulContextConfig {
	if reflect.TypeOf(key).Comparable() {
		gcc.parent = context.WithValue(gcc.parent, key, value)
	}
	return gcc
}

// WithDeadline sets a deadline that will be make the parent
// recreated from context.WithDeadline on initialization.
// Previous WithDeadline and WithTimeout calls are overwritten.
func (gcc *gracefulContextConfig) WithDeadline(duration time.Time) *gracefulContextConfig {
	gcc.deadline = duration
	gcc.timeout = time.Duration(0)
	return gcc
}

// WithTimeout sets a deadline that will be make the parent
// recreated from context.WithTimeout on initialization.
// Previous WithDeadline and WithTimeout calls are overwritten.
func (gcc *gracefulContextConfig) WithTimeout(timeout time.Duration) *gracefulContextConfig {
	gcc.timeout = timeout
	gcc.deadline = time.Time{}
	return gcc
}

// WithCleanupFunc assigns a cleanup function to be called when the gracefulContext is cancelled
//
// the cleanup function is spawned in its goroutine, and the
// returned error will be wrapped in the context's error.
// It's intended to run blocking functions concurrently.
func (gcc *gracefulContextConfig) WithCleanupFunc(cleanupFunc CleanupFunc) *gracefulContextConfig {
	gcc.cleanupFunc = cleanupFunc
	return gcc
}

// WithCleanupTimeout sets a timeout after which the gracefulContext is cancelled
//
// When the cleanup timeout expires, the context will have ErrCleanupFuncTimeout
// wrapped in the gracefulContext's Err(). The cleanup function itself
// will still run and its error will still be wrapped in the Err()
func (gcc *gracefulContextConfig) WithCleanupTimeout(cleanupFuncTimeout time.Duration) *gracefulContextConfig {
	gcc.cleanupTimeout = cleanupFuncTimeout
	return gcc
}

// ImmediatelyPropagateCancel will immediately cancel the context,
// not waiting for the cleanupFunc or cleanupTimeout completion.
func (gcc *gracefulContextConfig) ImmediatelyPropagateCancel(immediateCancelPropagation bool) *gracefulContextConfig {
	gcc.immediateCancelPropagation = immediateCancelPropagation
	return gcc
}

// Make initializes the gracefulContext from its configuration
func (gcc *gracefulContextConfig) Make() *gracefulContext {
	// offload the ValueContext to context
	if gcc.key != nil {
		gcc.parent = context.WithValue(gcc.parent, gcc.key, gcc.value)
	}
	// offload deadline to context
	if !gcc.deadline.IsZero() {
		ctx, cancel := context.WithDeadline(gcc.parent, gcc.deadline)
		gcc.selfCreatedTimerCtxCanceler = cancel
		gcc.parent = ctx
	}
	// offload timeout to context
	if gcc.timeout != time.Duration(0) {
		ctx, cancel := context.WithTimeout(gcc.parent, gcc.timeout)
		gcc.selfCreatedTimerCtxCanceler = cancel
		gcc.parent = ctx
	}
	gc := &gracefulContext{
		parent:                     gcc.parent,
		immediateCancelPropagation: gcc.immediateCancelPropagation,
		cleanupFuncDoneChan:        make(chan struct{}),
		cleanupFuncStartChan:       make(chan struct{}),
		doneChan:                   make(chan struct{}),
	}
	gc.cancel = func() {
		if gcc.selfCreatedTimerCtxCanceler != nil {
			gcc.selfCreatedTimerCtxCanceler()
		}
		gc.setError(ErrCleanupFuncPending)
		gc.safelyCloseStrucChan(chanCleanupFuncStart)
	}
	go cleanupFuncDoneErrWatcher(gc)
	go cleanupFuncStartWatcher(gc, gcc.cleanupFunc, gcc.cleanupTimeout)
	propagateCancel(gc, gc.parent)
	return gc
}

func cleanupFuncDoneErrWatcher(gc *gracefulContext) {
	err := <-gc.cleanupFuncErrChan
	if err == nil {
		err = ErrContextCancelDone
	}
	gc.setError(err)
	gc.safelyCloseStrucChan(chanCleanupFuncDone)
}
func cleanupFuncStartWatcher(
	gc *gracefulContext,
	cleanupFunc CleanupFunc,
	cleanupFuncTimeout time.Duration,
) {
	if gc.safelyCheckStructChanClosed(chanCleanupFuncDone) {
		return
	}
	if cleanupFunc != nil {
		<-gc.cleanupFuncStartChan
		gc.safelyCloseStrucChan(chanCleanupFuncStart)
		go runCleanupFunc(cleanupFunc, gc.cleanupFuncErrChan)
		if cleanupFuncTimeout != time.Duration(0) {
			go runCleanupTimer(cleanupFuncTimeout, gc.cleanupFuncErrChan)
		}
		if gc.immediateCancelPropagation {
			safelyNonblockinglySendErrChan(gc.cleanupFuncErrChan, ErrContextCancelled)
			gc.safelyCloseStrucChan(chanDone)
		}
	} else {
		gc.safelyCloseStrucChan(chanCleanupFuncStart)
		gc.safelyCloseStrucChan(chanCleanupFuncDone)
		gc.safelyCloseStrucChan(chanDone)
		safelyNonblockinglySendErrChan(gc.cleanupFuncErrChan, ErrContextCancelled)
	}
}
func runCleanupFunc(cleanupFunc func() error, errC chan error) {
	err := cleanupFunc()
	if err != nil {
		err = ErrContextCancelDone
	}
	select {
	case errC <- err:
	default:
	}
}
func runCleanupTimer(cleanupTimeout time.Duration, errC chan error) {
	time.Sleep(cleanupTimeout)
	select {
	case errC <- ErrCleanupFuncTimeout:
	default:
	}
}
func propagateCancel(subscriber *gracefulContext, sender context.Context) {
	subscriberDoneChan := subscriber.doneChan
	senderDoneChan := sender.Done()
	if senderDoneChan != nil {
		go func() {
			select {
			case <-senderDoneChan:
				subscriber.cancel()
			case <-subscriberDoneChan:
				return
			}
		}()
	}
}

// Deadline returns the graceful context's deadline.
//
// gracefulContext does not handle the deadline of
// context itself, instead it is offloaded to
// context.WithTimeout upon Make(), to
// assure full context.Context
// compability.
func (gc *gracefulContext) Deadline() (time.Time, bool) {
	return gc.parent.Deadline()
}

// Done returns a receive channel on which
// the context's cancellation can be
// monitored analogous to the
// context.Context
func (gc *gracefulContext) Done() <-chan struct{} {
	return gc.doneChan
}

// Err differentiate a bit from the usual context.Context
//
// It will wrap multiple context errors, to identify
// the errors, using errors.Is() is useful
// to check the error chain
func (gc *gracefulContext) Err() error {
	defer gc.mu.Unlock()
	gc.mu.Lock()
	return gc.err
}

// Value returns any key associated with
// the context's ancestry
func (gc *gracefulContext) Value(key interface{}) interface{} {
	return gc.parent.Value(key)
}

// Cancel initiates a cancellation of the gracefulContext,
// with respect to the cleanupTimeout and
// ImmediatelyPropagateCancel
func (gc *gracefulContext) Cancel() (err error) {
	err = gc.Err()
	if gc.safelyCheckStructChanClosed(chanDone) {
		return
	}
	gc.cancel()
	return
}

// SubscribeCancellation accepts another context.Context,
// which cancellation will trigger cancellation
// of this particular graceful context.
func (gc *gracefulContext) SubscribeCancellation(ctx context.Context) {
	propagateCancel(gc, ctx)
}

// Context returns the graceful context as context.Context,
// along with context.CancelFunc.
//
// It eliminates the interfaces outside of context.Context,
// which can be is useful to ensure that cancellation
// is called to prevent context leaks
//
// Note that subscribing to a cancelled context
// will immediately cancel the gracefulContext.
func (gcc *gracefulContext) Context() (context context.Context, cancel context.CancelFunc) {
	return gcc, gcc.cancel
}

func (gc *gracefulContext) setError(err error) {
	gc.mu.Lock()
	if gc.err == nil {
		gc.err = err
	} else {
		gc.err = errors.Wrap(err, gc.err.Error())
	}
	gc.mu.Unlock()
}
func (gc *gracefulContext) safelyCloseStrucChan(c int) {
	theChan := gc.getStructChan(c)
	defer gc.mu.Unlock()
	gc.mu.Lock()
	if theChan == nil {
		return
	}
	select {
	case <-theChan:
	default:
		close(theChan)
	}
}
func (gc *gracefulContext) safelyCheckStructChanClosed(c int) (closed bool) {
	theChan := gc.getStructChan(c)
	defer gc.mu.Unlock()
	gc.mu.Lock()
	if theChan == nil {
		return
	}
	select {
	case <-theChan:
		closed = true
	default:
	}
	return
}
func (gc *gracefulContext) getStructChan(c int) (theChan chan struct{}) {
	switch c {
	case chanCleanupFuncStart:
		theChan = gc.cleanupFuncStartChan
	case chanCleanupFuncDone:
		theChan = gc.cleanupFuncDoneChan
	case chanDone:
		theChan = gc.doneChan
	}
	return
}

func safelyNonblockinglySendErrChan(errC chan error, err error) {
	select {
	case errC <- err:
	default:
	}
}

type CleanupFunc func() error
