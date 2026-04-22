package queue

import "encoding/json"

type observedBusEnvelope struct {
	Job struct {
		Type string `json:"type"`
	} `json:"job"`
}

// ResolveObservedJobType returns the effective application job type that should
// be emitted to observers. External workers may process internal bus wrapper
// jobs (for example, "bus:job") whose payload embeds the real application job
// type. When possible, this helper unwraps that payload so dashboards and
// metrics reflect the user-facing job type instead of the transport wrapper.
func ResolveObservedJobType(rawType string, payload []byte) string {
	if rawType == "" {
		return ""
	}
	if len(payload) == 0 || len(rawType) < 4 || rawType[:4] != "bus:" {
		return rawType
	}

	var env observedBusEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return rawType
	}
	if env.Job.Type == "" {
		return rawType
	}
	return env.Job.Type
}
