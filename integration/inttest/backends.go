package inttest

import "strings"

var LocalBackends = []string{
	"null",
	"sync",
	"workerpool",
	"sqlite",
}

var ExternalBackends = []string{
	"redis",
	"mysql",
	"postgres",
	"nats",
	"sqs",
	"rabbitmq",
}

// SelectedBackends returns the enabled backend set for INTEGRATION_BACKEND.
// When unset or "all", all known backends are enabled.
func SelectedBackends(envValue string) map[string]bool {
	selected := map[string]bool{}
	for _, name := range LocalBackends {
		selected[name] = true
	}
	for _, name := range ExternalBackends {
		selected[name] = false
	}

	value := strings.TrimSpace(strings.ToLower(envValue))
	if value == "" || value == "all" {
		for _, name := range ExternalBackends {
			selected[name] = true
		}
		return selected
	}

	for key := range selected {
		selected[key] = false
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		selected[part] = true
	}
	return selected
}

func BackendEnabled(envValue, name string) bool {
	return SelectedBackends(envValue)[strings.ToLower(strings.TrimSpace(name))]
}
