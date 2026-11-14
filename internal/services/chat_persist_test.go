package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"alphahack/internal/queries"
)

// chatHangingPersistStore lets the first InTx call (create run + user message)
// pass through to the embedded fake, then blocks the post-model persistence
// transaction until its context is done. It never calls fn, so a persistence
// context without a deadline hangs forever.
type chatHangingPersistStore struct {
	*chatFakeStore
	inTxCalls int
}

func (s *chatHangingPersistStore) InTx(ctx context.Context, fn func(q queries.Querier) error) error {
	s.inTxCalls++
	// Call 1: pre-model run + user message. Call 2: post-model persistence
	// (tool calls, assistant message, finish run). Call 3+: failRun, if any.
	if s.inTxCalls == 2 {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.chatFakeStore.InTx(ctx, fn)
}

func TestChatPersistenceHasDeadline(t *testing.T) {
	orig := chatPersistTimeout
	chatPersistTimeout = 50 * time.Millisecond
	t.Cleanup(func() { chatPersistTimeout = orig })

	st := &chatHangingPersistStore{chatFakeStore: chatNewFakeStore()}
	cl := &chatFakeClient{result: chatOKResult()}
	svc := NewChatService(st, cl, DefaultTools(), ChatOptions{})

	type result struct {
		out *ChatResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
		done <- result{out, err}
	}()

	var res result
	select {
	case res = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("persistence hung without a deadline")
	}

	if res.err == nil {
		t.Fatalf("err = nil, want internal error from timed-out persistence (out = %+v)", res.out)
	}
	if code := chatErrCode(res.err); code != CodeInternal {
		t.Errorf("err code = %q, want %q (err = %v)", code, CodeInternal, res.err)
	}
	if !errors.Is(res.err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", res.err)
	}
}
