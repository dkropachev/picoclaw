package protocoltypes

import (
	"errors"
	"testing"
)

func TestRequireModel(t *testing.T) {
	t.Run("trims configured model", func(t *testing.T) {
		model, err := RequireModel("  gpt-test  ")
		if err != nil {
			t.Fatalf("RequireModel() error = %v", err)
		}
		if model != "gpt-test" {
			t.Fatalf("RequireModel() = %q, want gpt-test", model)
		}
	})

	t.Run("rejects missing model", func(t *testing.T) {
		_, err := RequireModel("  ")
		if !errors.Is(err, ErrNoModelConfigured) {
			t.Fatalf("RequireModel() error = %v, want ErrNoModelConfigured", err)
		}
		if err.Error() != "no model configured" {
			t.Fatalf("RequireModel() error = %q, want no model configured", err)
		}
	})
}
