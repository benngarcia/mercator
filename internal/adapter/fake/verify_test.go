package fake

import (
	"context"
	"errors"
	"testing"
)

func TestFakeVerifyOK(t *testing.T) {
	if err := New().Verify(context.Background()); err != nil {
		t.Fatalf("fake verify should pass: %v", err)
	}
}

func TestFakeVerifyWithError(t *testing.T) {
	want := errors.New("credentials invalid")
	a := New(WithVerifyError(want))
	if got := a.Verify(context.Background()); got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
