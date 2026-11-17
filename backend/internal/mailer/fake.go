package mailer

import (
	"context"
	"sync"
)

// Recorder is a fake Sender for tests: it records every message and fails
// with Err when set (or with what Fail returns for a message).
type Recorder struct {
	mu   sync.Mutex
	sent []Message
	// Err, when non-nil, is returned by every Send (nothing is recorded).
	Err error
	// Fail, when set, decides per message: a non-nil result fails it.
	Fail func(Message) error
}

// Send records m (or fails, see Err / Fail).
func (r *Recorder) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	if r.Fail != nil {
		if err := r.Fail(m); err != nil {
			return err
		}
	}
	r.sent = append(r.sent, m)
	return nil
}

// Sent returns a copy of the recorded messages, oldest first.
func (r *Recorder) Sent() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.sent...)
}

// SetErr sets Err under the recorder's lock.
func (r *Recorder) SetErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Err = err
}
