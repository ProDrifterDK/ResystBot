package utils

import (
	"fmt"
	"runtime/debug"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Recover is meant to be used as the outermost defer of a goroutine, e.g.
//
//	go func() {
//	    defer utils.Recover("agent", "summarize")
//	    ...
//	}()
//
// It converts a panic inside the goroutine into a logged error so a single
// misbehaving background task cannot take down the whole process.
//
// Port of upstream b292defd (panic recovery for core-path goroutines), adapted
// to our fork as a reusable helper instead of per-site boilerplate.
func Recover(component, name string) {
	if r := recover(); r != nil {
		logger.ErrorCF(component, "goroutine panic recovered", map[string]any{
			"name":  name,
			"panic": fmt.Sprintf("%v", r),
			"stack": string(debug.Stack()),
		})
	}
}

// Go starts fn in a new goroutine guarded by Recover. Use it for core-path
// background work (dispatchers, watchers, summarizers) that must never crash
// the process.
func Go(component, name string, fn func()) {
	go func() {
		defer Recover(component, name)
		fn()
	}()
}
