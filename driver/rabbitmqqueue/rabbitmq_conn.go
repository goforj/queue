package rabbitmqqueue

import (
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func dialRabbitMQWithRetry(url string, timeout time.Duration) (*amqp.Connection, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := amqp.Dial(url)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil, fmt.Errorf("rabbitmq dial failed after %s: %w", timeout, lastErr)
}
