package worker

import (
	"context"
	"testing"
	"time"
)

func TestDurableContextSurvivesParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	ctx, cancel := durableContext(parent)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("durable context must not be cancelled when parent is: %v", err)
	}
	// Still has a deadline so shutdown cannot hang.
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("durable context must carry a deadline")
	}
	if time.Until(deadline) > durableTimeout || time.Until(deadline) < 0 {
		t.Errorf("deadline unexpected: until=%v want ~%v", time.Until(deadline), durableTimeout)
	}
}
