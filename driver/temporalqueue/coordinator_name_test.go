package temporalqueue

import (
	"testing"

	"github.com/goforj/queue/internal/workflow"
	"go.temporal.io/sdk/converter"
)

// TestBatchNameRejectsNormalizationBeforeAcceptance preserves immutable input comparison across original-history recovery.
func TestBatchNameRejectsNormalizationBeforeAcceptance(t *testing.T) {
	for _, name := range []string{"", "daily", "毎日のレポート", "daily\xff"} {
		t.Run(name, func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			_, err := c.start(t.Context(), "batch", name, "", false, []workflow.Job{{Type: "business"}}, nil)
			if name == "daily\xff" {
				if err == nil || len(sdk.inputs) != 0 {
					t.Fatal("invalid UTF-8 name crossed service acceptance", err)
				}
				return
			}
			if err != nil || len(sdk.inputs) != 1 {
				t.Fatal("valid batch name rejected", err)
			}
			def := sdk.inputs[0].(workflowDefinition)
			input, err := converter.GetDefaultDataConverter().ToPayloads(def)
			if err != nil {
				t.Fatal(err)
			}
			var restored workflowDefinition
			if err := converter.GetDefaultDataConverter().FromPayloads(input, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.Name != name {
				t.Fatal("accepted name changed in original history", restored.Name, name)
			}
		})
	}
}
