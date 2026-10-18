package utils

import (
	"sync"
	"testing"
	"time"
)

func TestRecoverSwallowsPanic(t *testing.T) {
	done := make(chan struct{})
	Go("test", "panics", func() {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not finish")
	}
}

func TestGoRunsFn(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	Go("test", "ok", func() {
		defer wg.Done()
	})

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not run fn")
	}
}
