package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func BenchmarkAskFastPath(b *testing.B) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) {
		return answer("ok"), nil
	}}
	s, _, _ := setup(b, f, 1)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		run := submit(b, s, app.SubmitRequest{Input: "hello", Mode: "ask"})
		wait(b, s, run.ID)
	}
	b.StopTimer()
	f.mu.Lock()
	calls := int64(len(f.requests))
	f.mu.Unlock()
	b.ReportMetric(float64(calls)/float64(b.N), "model_calls/op")
}

func BenchmarkCodeMultiTool(b *testing.B) {
	var calls atomic.Int64
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		n := calls.Add(1)
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" && strings.Contains(last.Content, "file content") {
			return answer("done"), nil
		}
		if n%2 == 1 {
			return toolCall("read", `{"filename":"fixture.txt"}`), nil
		}
		return answer("file content"), nil
	}}
	s, _, _ := setup(b, f, 1)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		calls.Store(0)
		run := submit(b, s, app.SubmitRequest{Input: "read fixture", Mode: "code"})
		wait(b, s, run.ID)
	}
	b.StopTimer()
	f.mu.Lock()
	total := int64(len(f.requests))
	f.mu.Unlock()
	b.ReportMetric(float64(total)/float64(b.N), "model_calls/op")
}

func BenchmarkDeliberateMode(b *testing.B) {
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if strings.Contains(req.Messages[0].Content, "Critique the proposed answer") {
			return answer("critique"), nil
		}
		if last.Role == "tool" && strings.Contains(last.Content, "critique") {
			return answer("judged"), nil
		}
		return answer("proposal"), nil
	}}
	s, _, _ := setup(b, f, 3)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		run := submit(b, s, app.SubmitRequest{Input: "deliberate this", Mode: "deliberate"})
		wait(b, s, run.ID)
	}
	b.StopTimer()
	f.mu.Lock()
	total := int64(len(f.requests))
	f.mu.Unlock()
	b.ReportMetric(float64(total)/float64(b.N), "model_calls/op")
}

func BenchmarkAskLongHistory(b *testing.B) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) {
		return answer("ok"), nil
	}}
	s, _, _ := setup(b, f, 1)
	warmup := submit(b, s, app.SubmitRequest{Input: "start", Mode: "ask"})
	wait(b, s, warmup.ID)
	for i := 0; i < 100; i++ {
		run := submit(b, s, app.SubmitRequest{SessionID: warmup.SessionID, Input: fmt.Sprintf("turn %d", i), Mode: "ask"})
		wait(b, s, run.ID)
	}
	b.ResetTimer()
	b.ReportAllocs()
	var startCount int
	f.mu.Lock()
	startCount = len(f.requests)
	f.mu.Unlock()
	for i := 0; i < b.N; i++ {
		run := submit(b, s, app.SubmitRequest{SessionID: warmup.SessionID, Input: fmt.Sprintf("bench %d", i), Mode: "ask"})
		wait(b, s, run.ID)
	}
	b.StopTimer()
	f.mu.Lock()
	total := int64(len(f.requests) - startCount)
	f.mu.Unlock()
	b.ReportMetric(float64(total)/float64(b.N), "model_calls/op")
}

func BenchmarkHasUnresolved(b *testing.B) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) {
		return answer("ok"), nil
	}}
	s, db, _ := setup(b, f, 1)
	var rootID domain.RunID
	for i := 0; i < 50; i++ {
		run := submit(b, s, app.SubmitRequest{Input: fmt.Sprintf("turn %d", i), Mode: "ask"})
		wait(b, s, run.ID)
		if i == 0 {
			rootID = run.ID
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := db.HasUnresolved(context.Background(), rootID); err != nil {
			b.Fatal(err)
		}
	}
}
