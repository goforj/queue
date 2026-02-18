package queue

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type natsWorker struct {
	url string

	mu       sync.RWMutex
	handlers map[string]Handler

	conn  *nats.Conn
	sub   *nats.Subscription
	start sync.Once
}

func newNATSWorker(url string) Worker {
	return &natsWorker{
		url:      url,
		handlers: make(map[string]Handler),
	}
}

func (w *natsWorker) Driver() Driver {
	return DriverNATS
}

func (w *natsWorker) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	w.mu.Lock()
	w.handlers[taskType] = handler
	w.mu.Unlock()
}

func (w *natsWorker) Start() error {
	var startErr error
	w.start.Do(func() {
		nc, err := nats.Connect(w.url)
		if err != nil {
			startErr = err
			return
		}
		sub, err := nc.Subscribe("queue.*", func(message *nats.Msg) {
			w.processMessage(message)
		})
		if err != nil {
			nc.Close()
			startErr = err
			return
		}
		w.conn = nc
		w.sub = sub
	})
	return startErr
}

func (w *natsWorker) Shutdown() error {
	if w.sub != nil {
		_ = w.sub.Drain()
	}
	if w.conn != nil {
		_ = w.conn.Drain()
		w.conn.Close()
	}
	return nil
}

func (w *natsWorker) processMessage(message *nats.Msg) {
	var incoming natsMessage
	if err := json.Unmarshal(message.Data, &incoming); err != nil {
		return
	}
	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			time.AfterFunc(remaining, func() {
				w.republish(incoming)
			})
			return
		}
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		return
	}

	ctx := context.Background()
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		ctx,
		NewTask(incoming.Type).
			Payload(incoming.Payload).
			OnQueue(incoming.Queue).
			Retry(incoming.MaxRetry).
			withAttempt(incoming.Attempt),
	)
	if err == nil {
		return
	}
	if incoming.Attempt >= incoming.MaxRetry {
		return
	}
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	w.republish(incoming)
}

func (w *natsWorker) republish(message natsMessage) {
	if w.conn == nil {
		return
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	_ = w.conn.Publish(natsSubject(message.Queue), payload)
}
