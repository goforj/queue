package queue

import "runtime"

func defaultWorkerCount(configured int) int {
	if configured > 0 {
		return configured
	}
	workers := runtime.NumCPU()
	if workers <= 0 {
		return 1
	}
	return workers
}
