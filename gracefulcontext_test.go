package gracefulcontext

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// no error on initializing gracefulContext
func TestGracefulContextInitialization(t *testing.T) {
	ctx := NewGracefulContext(context.Background()).Make()
	if ctx == nil {
		t.Fail()
	}
	isGracefulContext := func(gc GracefulContext) error {
		return nil
	}
	err := isGracefulContext(ctx)
	if err != nil {
		t.Error("gracefulContext fails implement the GracefulContext interface")
	}
}

// no error when the parent vanila context.WithCancel is being cancelled and its cancelFunc (itself and its childContexts) are being executed properly
func TestOutsideGracefulContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := testMkWriter(t, false)
	ctx1 := NewGracefulContext(ctx).WithCleanupFunc(testMkCancelWriterFunc(t, "a", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	ctx2 := NewGracefulContext(ctx1).WithCleanupFunc(testMkCancelWriterFunc(t, "b", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	ctx3 := NewGracefulContext(ctx2).WithCleanupFunc(testMkCancelWriterFunc(t, "c", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	NewGracefulContext(ctx3).WithCleanupFunc(testMkCancelWriterFunc(t, "d", w)).WithCleanupTimeout(time.Second).Make()
	result := w.String()
	if len(result) != 0 {
		t.Error("result is not zero before any cancellations", result)
	}
	cancel()
	time.Sleep(time.Millisecond)
	result = w.String()
	cancelersCount := 4
	if len(result) != cancelersCount {
		t.Errorf("%d cancelers resulted in %d characters: %s", cancelersCount, len(result), result)
	}
}

// no error for cancellation propagation
func TestGracefulContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := testMkWriter(t, false)
	ctx1 := NewGracefulContext(ctx).WithCleanupFunc(testMkCancelWriterFunc(t, "a", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	ctx2 := NewGracefulContext(ctx1).WithCleanupFunc(testMkCancelWriterFunc(t, "b", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	ctx3 := NewGracefulContext(ctx2).WithCleanupFunc(testMkCancelWriterFunc(t, "c", w)).WithCleanupTimeout(time.Second).ImmediatelyPropagateCancel(true).Make()
	NewGracefulContext(ctx3).WithCleanupFunc(testMkCancelWriterFunc(t, "d", w)).WithCleanupTimeout(time.Second).Make()
	result := w.String()
	if len(result) != 0 {
		t.Error("result is not zero before any cancellations", result)
	}
	ctx2.Cancel()
	time.Sleep(time.Millisecond)
	result = w.String()
	cancelersCount := 3
	if len(result) != cancelersCount {
		t.Errorf("%d cancelers resulted in %d characters: %s", cancelersCount, len(result), result)
	}
	ctx1.Cancel()
	time.Sleep(time.Millisecond)
	result = w.String()
	cancelersCount = 4
	if len(result) != cancelersCount {
		t.Errorf("%d cancelers resulted in %d characters: %s", cancelersCount, len(result), result)
	}
}

// no error when the context is being cancelled with timeout for completion of cancelFuncs
func testGracefulContextDelayedCancellation(t *testing.T) {
	ctx := context.Background()
	w := testMkWriter(t, false)
	x := testMkDelayedCancelWriterFunc(t, "x", w, time.Millisecond*time.Duration(200))
	y := testMkDelayedCancelWriterFunc(t, "y", w, time.Millisecond*time.Duration(200))
	z := testMkCancelWriterFunc(t, "z", w)
	ctx1 := NewGracefulContext(ctx).WithCleanupFunc(x).WithCleanupTimeout(time.Millisecond * time.Duration(100)).Make()
	ctx2 := NewGracefulContext(ctx1).WithCleanupFunc(y).WithCleanupTimeout(time.Millisecond * time.Duration(100)).ImmediatelyPropagateCancel(true).Make()
	ctx3 := NewGracefulContext(ctx2).WithCleanupFunc(z).ImmediatelyPropagateCancel(true).Make()
	err := ctx1.Cancel()
	if err != nil {
		t.Error("Error: ", time.Now(), "ctx1: cancelling a new gracefulContext result in error, error: ", err)
	}
	err = ctx1.Cancel()
	if err == nil {
		t.Error("Error: ", time.Now(), "no error when cancelling a cancelled gracefulContext, error: ", err)
	}
	err = ctx2.Cancel()
	if err != nil {
		t.Error("Error: ", time.Now(), "ctx2: cancelling a new gracefulContext result in error")
	}
	// give the goroutine some time to start the cancellation
	time.Sleep(time.Millisecond * time.Duration(10))
	err = ctx2.Cancel()
	if err == nil {
		t.Error("Error:", time.Now(), "ctx2: no error when cancelling a cancelled gracefulContext, error: ", err)
	}
	err = ctx3.Err()
	if err != nil && !errors.Is(err, ErrCleanupFuncPending) {
		t.Error("Error:", time.Now(), "ctx3 is cancelled before the graceContext timeout (1 second) is over, error: ", err)
	}
	// sleep 1s, by now, the ctx3 should have been cancelled
	time.Sleep(time.Millisecond * time.Duration(100))
	err = ctx3.Cancel()
	if err == nil {
		t.Error("Error:", time.Now(), "ctx3: no error when cancelling a cancelled gracefulContext, error: ", err)
	}
	// ensure the ctx2 cancelFunc has finished writing
	time.Sleep(time.Millisecond * time.Duration(200))
	result := w.String()
	if result != "zxy" && result != "zyx" {
		t.Error("Error:", time.Now(), "resulting written is not as expected: ", result)
	}
}

// concurrent run: no error when the context is being cancelled with timeout for completion of cancelFuncs
func TestGracefulContextDelayedCancellation(t *testing.T) {
	wg := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			testGracefulContextDelayedCancellation(t)
			wg.Done()
			wg.Wait()
		}()
	}

}

// no error on propagation of a context to its subscription
func testGracefulContextSubscription(t *testing.T) (bool, string) {
	ctxbg := context.Background()
	w := testMkWriter(t, true)
	ii := testMkDelayedCancelWriterFunc(t, "ii", w, tetsRandMsAbout(50))
	jj := testMkDelayedCancelWriterFunc(t, "jj", w, tetsRandMsAbout(100))
	j1 := testMkDelayedCancelWriterFunc(t, "j1", w, tetsRandMsAbout(100))
	j2 := testMkDelayedCancelWriterFunc(t, "j2", w, tetsRandMsAbout(100))
	kk := testMkDelayedCancelWriterFunc(t, "kk", w, tetsRandMsAbout(100))
	ll := testMkDelayedCancelWriterFunc(t, "ll", w, tetsRandMsAbout(100))
	ctxii := NewGracefulContext(ctxbg).WithCleanupFunc(ii).ImmediatelyPropagateCancel(false).Make() // ii should be before kk and ll and after any jj
	ctxjj := NewGracefulContext(ctxii).WithCleanupFunc(jj).ImmediatelyPropagateCancel(true).Make()  // any jj,j1,j2 must be at the start
	NewGracefulContext(ctxjj).WithCleanupFunc(j1).ImmediatelyPropagateCancel(true).Make()
	ctxj2 := NewGracefulContext(ctxjj).WithCleanupFunc(j2).ImmediatelyPropagateCancel(false).Make()
	ctxkk := NewGracefulContext(ctxii).WithCleanupFunc(kk).ImmediatelyPropagateCancel(false).Make() // should be after ii
	ctxll := NewGracefulContext(ctxbg).WithCleanupFunc(ll).ImmediatelyPropagateCancel(false).Make() // should be after ii
	ctxii.SubscribeCancellation(ctxj2)
	ctxjj.Cancel()
	ctxll.SubscribeCancellation(ctxkk)

	time.Sleep(time.Second)
	result := w.String()
	resultArr := testGetStrTuples(result)
	iiIdx := getStrIdx(resultArr, "ii")
	jjIdx := getStrIdx(resultArr, "jj")
	j1Idx := getStrIdx(resultArr, "j1")
	j2Idx := getStrIdx(resultArr, "j2")
	kkIdx := getStrIdx(resultArr, "kk")
	llIdx := getStrIdx(resultArr, "ll")
	pass := true
	if llIdx < iiIdx {
		t.Error("ll is before ii")
		pass = false
	}
	if kkIdx > llIdx {
		t.Error("ll is before ii")
		pass = false
	}
	if iiIdx < j1Idx || iiIdx < j2Idx || iiIdx < jjIdx {
		t.Error("ii is not before jj, j1 or j2")
		pass = false
	}
	if !pass {
		t.Fail()
	}
	failedStr := "[OK]"
	if !pass {
		failedStr = "[FAILED]"
	}
	t.Log(time.Now(), "result: ", result, failedStr)
	return pass, result
}

// single context tree: no error on propagation of a context to its subscription
func TestGracefulContextSubscription(t *testing.T) {
	pass, _ := testGracefulContextSubscription(t)
	if !pass {
		t.Fail()
	}
}

// multiple context tree run concurrently: no error on propagation of a context to its subscription
func TestGracefulContextSubscriptionConcurrent(t *testing.T) {
	var ok, err int
	okC := make(chan bool)
	errC := make(chan bool)
	doneC := make(chan bool)
	var wg sync.WaitGroup
	go func() {
		for {
			select {
			case <-okC:
				ok++
			case <-errC:
				err++
			case <-doneC:
				return
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pass, _ := testGracefulContextSubscription(t)
			if pass {
				okC <- true
			} else {
				errC <- true
			}
		}(i)
	}
	go func() {
		wg.Wait()
		doneC <- true
		close(doneC)
	}()
	<-doneC
	t.Log(time.Now(), "Concurrent test result stats: ")
	t.Log("                                                       ok results: ", ok)
	t.Log("                                                       err results: ", err)
}

// helper: a simple writer to provide an io.Writer to store what the cancelFuncs do
type testWriter struct {
	b                 *bytes.Buffer
	t                 *testing.T
	mu                sync.Mutex
	writeToTestingLog bool
}

// helper: makes the testWriter
func testMkWriter(t *testing.T, writeToTestingLog bool) *testWriter {
	b := bytes.NewBuffer([]byte{})
	w := testWriter{b: b, t: t, writeToTestingLog: writeToTestingLog}
	return &w
}
func (w *testWriter) Write(p []byte) (n int, err error) {
	defer w.mu.Unlock()
	w.mu.Lock()
	str := string(p)
	if w.writeToTestingLog {
		w.t.Log(time.Now(), "cancelFunc wrote: ", str)
	}
	return w.b.Write(p)
}
func (w *testWriter) String() string {
	defer w.mu.Unlock()
	w.mu.Lock()
	return w.b.String()
}

// helper: creates a cancelFunc that writes with a predefined text
func testMkCancelWriterFunc(t *testing.T, s string, w io.Writer) func() error {
	return func() error {
		w.Write([]byte(s))
		return errors.New(s)
	}
}

// helper: creates a cancelFunc that writes with a predefined text with a delay
func testMkDelayedCancelWriterFunc(t *testing.T, s string, w io.Writer, delay time.Duration) func() error {
	return func() error {
		time.Sleep(delay)
		w.Write([]byte(s))
		return errors.New(s)
	}
}

// helper: randomly generate a +-10ms random time centered on centerPoint
func tetsRandMsAbout(centerPoint int) time.Duration {
	i := rand.Int()
	return time.Duration((i^1)%20+centerPoint) * time.Millisecond
}

// helper: get 2 letters pairs from a string as string array
func testGetStrTuples(src string) []string {
	ln := len(src) / 2
	tuples := make([]string, ln)
	for i := 0; i < ln; i++ {
		tuples[i] = string(src[i*2] + src[i*2+1])
	}
	return tuples
}

// helper: get index of a string within a string array
func getStrIdx(src []string, search string) int {
	for i, s := range src {
		if s == search {
			return i
		}
	}
	return -1
}
