package invariant

import (
	"fmt"
	"testing"
)

func TestIs(t *testing.T) {
	err := fmt.Errorf("save: %w", New("lease/x", "detail"))
	if !Is(err, "lease/x") || Is(err, "lease/y") || Is(fmt.Errorf("plain"), "lease/x") {
		t.Error("Is does not match the wrapped type only")
	}
	if New("a/b", "c").Error() != "a/b: c" {
		t.Error("Error text")
	}
}
