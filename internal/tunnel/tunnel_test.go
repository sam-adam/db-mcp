package tunnel

import (
	"testing"
	"time"
)

func TestJitterBounded(t *testing.T) {
	for i := 0; i < 50; i++ {
		v := jitter(time.Second)
		if v < 800*time.Millisecond || v > 1200*time.Millisecond {
			t.Fatalf("out of range: %v", v)
		}
	}
}
