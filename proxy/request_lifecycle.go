package proxy

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
)

// requestWriter tracks the response actually sent, not individual upstream attempts.
type requestWriter struct {
	http.ResponseWriter
	ctx    context.Context
	cancel context.CancelFunc
	status int
	failed bool
}

func (w *requestWriter) WriteHeader(status int) {
	w.failed = w.failed || status >= 400
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *requestWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.failed = true
		w.cancel()
	}
	return n, err
}

func (w *requestWriter) Flush() {
	if w.ctx.Err() != nil {
		return
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		w.failed = true
		w.cancel()
	}
}

func requestContext(w http.ResponseWriter) context.Context {
	if tracked, ok := w.(*requestWriter); ok {
		return tracked.ctx
	}
	return context.Background()
}

func markResponseFailed(w http.ResponseWriter) {
	if tracked, ok := w.(*requestWriter); ok {
		tracked.failed = true
	}
}

func (h *Handler) trackRequest(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	tracked := &requestWriter{ResponseWriter: w, ctx: ctx, cancel: cancel}
	return tracked, func() {
		panicValue := recover()
		if panicValue == nil && !tracked.failed && ctx.Err() == nil && tracked.status != 0 {
			tracked.Flush()
		}
		success := panicValue == nil && !tracked.failed && ctx.Err() == nil && tracked.status != 0
		cancel()
		h.recordRequestOutcome(success)
		if panicValue != nil {
			panic(panicValue)
		}
	}
}

func (h *Handler) recordRequestOutcome(success bool) {
	atomic.AddInt64(&h.totalRequests, 1)
	if success {
		atomic.AddInt64(&h.successRequests, 1)
	} else {
		atomic.AddInt64(&h.failedRequests, 1)
	}
}
