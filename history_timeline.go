package queue

import (
	"sync"
	"time"
)

const (
	historyTimelineRetention   = 8 * 24 * time.Hour
	historyTimelineMaxPoints   = 30000
	historyTimelineMinInterval = 30 * time.Second
)

type historyTimeline struct {
	mu      sync.Mutex
	byQueue map[string][]QueueHistoryPoint
}

var defaultHistoryTimeline = &historyTimeline{byQueue: make(map[string][]QueueHistoryPoint)}

func timelineDuration(window QueueHistoryWindow) time.Duration {
	switch window {
	case QueueHistoryWeek:
		return 7 * 24 * time.Hour
	case QueueHistoryDay:
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

// TimelineHistoryFromSnapshot records queue counters and returns windowed points.
// This is intended for drivers that don't expose native multi-point history.
// @group Admin
//
// Example: timeline history from snapshots
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Processed: 5, Failed: 1},
//		},
//	}
//	points := queue.TimelineHistoryFromSnapshot(snapshot, "default", queue.QueueHistoryHour)
//	fmt.Println(len(points) >= 1)
//	// Output: true
func TimelineHistoryFromSnapshot(snapshot StatsSnapshot, queueName string, window QueueHistoryWindow) []QueueHistoryPoint {
	return defaultHistoryTimeline.recordAndRead(snapshot, normalizeQueueName(queueName), window, time.Now())
}

func (t *historyTimeline) recordAndRead(snapshot StatsSnapshot, queueName string, window QueueHistoryWindow, now time.Time) []QueueHistoryPoint {
	if t == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := now.Add(-historyTimelineRetention)
	for name, counters := range snapshot.ByQueue {
		if name == "" {
			continue
		}
		points := t.byQueue[name]
		entry := QueueHistoryPoint{At: now, Processed: counters.Processed, Failed: counters.Failed}
		if len(points) > 0 {
			last := points[len(points)-1]
			if last.Processed == entry.Processed && last.Failed == entry.Failed && now.Sub(last.At) < historyTimelineMinInterval {
				continue
			}
		}
		points = append(points, entry)

		start := 0
		for start < len(points) && points[start].At.Before(cutoff) {
			start++
		}
		if start > 0 {
			points = append([]QueueHistoryPoint(nil), points[start:]...)
		}
		if len(points) > historyTimelineMaxPoints {
			points = append([]QueueHistoryPoint(nil), points[len(points)-historyTimelineMaxPoints:]...)
		}
		t.byQueue[name] = points
	}

	if queueName == "" {
		return nil
	}
	points := t.byQueue[queueName]
	if len(points) == 0 {
		return nil
	}
	windowCutoff := now.Add(-timelineDuration(window))
	out := make([]QueueHistoryPoint, 0, len(points))
	for _, point := range points {
		if point.At.Before(windowCutoff) {
			continue
		}
		out = append(out, point)
	}
	return out
}
