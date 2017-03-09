package synctrigger

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestDoDupSuppress(t *testing.T) {
	var g Group
	const n = 10
	c := make(chan string, n)
	start := make(chan bool)
	var calls int32
	var ignored int32
	fn := func() {
		<-start
		atomic.AddInt32(&calls, 1)
		c <- "done"
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			if started := g.Go("key", fn); !started {
				atomic.AddInt32(&ignored, 1)
			}
			wg.Done()
		}()
	}
	// wait until all work is trigered
	wg.Wait()

	// now allow work to start
	close(start)

	// wait for result
	what := <-c
	if what != "done" {
		t.Errorf("got = %q; want %q", what, "done")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("number of calls = %d; want 1", got)
	}
	if got := atomic.LoadInt32(&ignored); got != n-1 {
		t.Errorf("number of ignored calls = %d; want %d", got, n-1)
	}
}
