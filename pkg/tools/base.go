package tools

import "context"

// Tool is the interface that all tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// ContextualTool is an optional interface that tools can implement
// to receive the current message context (channel, chatID)
type ContextualTool interface {
	Tool
	SetContext(channel, chatID string)
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
// The result parameter contains the tool's execution result.
//
// Example usage in an async tool:
//
//	func (t *MyAsyncTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
//	    // Start async work in background
//	    go func() {
//	        result := doAsyncWork()
//	        if t.callback != nil {
//	            t.callback(ctx, result)
//	        }
//	    }()
//	    return AsyncResult("Async task started")
//	}
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncTool is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Async tools return immediately with an AsyncResult, then notify completion
// via the callback set by SetCallback.
//
// This is useful for:
// - Long-running operations that shouldn't block the agent loop
// - Subagent spawns that complete independently
// - Background tasks that need to report results later
//
// Example:
//
//	type SpawnTool struct {
//	    callback AsyncCallback
//	}
//
//	func (t *SpawnTool) SetCallback(cb AsyncCallback) {
//	    t.callback = cb
//	}
//
//	func (t *SpawnTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
//	    go t.runSubagent(ctx, args)
//	    return AsyncResult("Subagent spawned, will report back")
//	}
type AsyncTool interface {
	Tool
	// SetCallback registers a callback function to be invoked when the async operation completes.
	// The callback will be called from a goroutine and should handle thread-safety if needed.
	SetCallback(cb AsyncCallback)
}

// SequentialTool is an optional interface that tools implement to opt out of
// parallel execution within a single LLM iteration.
//
// The agent loop executes the tool calls of one LLM turn concurrently when
// every requested tool is concurrency-safe. Tools that mutate shared state —
// file writers, interactive sessions, tools that keep per-call context on the
// receiver, or async tools whose callback is a shared field — must implement
// this interface and return true. When at least one such tool is present the
// whole batch falls back to sequential execution, preserving the pre-parallel
// behaviour and avoiding data races.
//
// Tools that are pure/read-only (filesystem reads, web fetches, memory
// searches, ...) should NOT implement it, so they stay parallelisable.
type SequentialTool interface {
	Tool
	// IsSequential reports whether the tool must run sequentially with respect
	// to the other tool calls in the same iteration.
	IsSequential() bool
}

// IsSequential reports whether t must be executed sequentially. Tools that do
// not implement SequentialTool are treated as concurrency-safe.
func IsSequential(t Tool) bool {
	if st, ok := t.(SequentialTool); ok {
		return st.IsSequential()
	}
	return false
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
