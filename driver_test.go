package queue

import "testing"

func TestDriverConstants(t *testing.T) {
	drivers := []Driver{DriverSync, DriverWorkerpool, DriverRedis, DriverDatabase, DriverNATS, DriverSQS}
	seen := map[Driver]bool{}
	for _, d := range drivers {
		if d == "" {
			t.Fatal("driver value must not be empty")
		}
		if seen[d] {
			t.Fatalf("duplicate driver value %q", d)
		}
		seen[d] = true
	}
}
