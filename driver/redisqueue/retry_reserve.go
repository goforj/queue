package redisqueue

import (
	"strconv"

	backend "github.com/hibiken/asynq"
)

const redisApplicationMaxRetryHeader = "goforj-queue-application-max-retry"

// redisApplicationMaxRetry restores the public retry budget when a new task carries one reserved Asynq transport slot.
func redisApplicationMaxRetry(task *backend.Task, transportMaxRetry int) int {
	if task == nil {
		return transportMaxRetry
	}
	return redisApplicationMaxRetryFromHeaders(task.Headers(), transportMaxRetry)
}

// redisApplicationMaxRetryFromHeaders keeps worker delivery and administrative snapshots on the same public budget.
func redisApplicationMaxRetryFromHeaders(headers map[string]string, transportMaxRetry int) int {
	raw, ok := headers[redisApplicationMaxRetryHeader]
	if !ok {
		return transportMaxRetry
	}
	applicationMaxRetry, err := strconv.Atoi(raw)
	if err != nil || applicationMaxRetry < 0 || applicationMaxRetry == int(^uint(0)>>1) {
		return transportMaxRetry
	}
	if applicationMaxRetry+1 != transportMaxRetry {
		return transportMaxRetry
	}
	return applicationMaxRetry
}
