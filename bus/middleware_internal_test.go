package bus

import (
	"errors"
	"testing"
)

func TestFatalErrorUnwrap(t *testing.T) {
	base := errors.New("boom")
	err := fatalError{cause: base}
	if !errors.Is(err, base) {
		t.Fatal("expected fatalError to unwrap to base error")
	}
}

