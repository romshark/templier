package log

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDurStr(t *testing.T) {
	t.Parallel()

	f := func(input time.Duration, expect string) {
		t.Helper()
		fmt.Println(input.String())
		require.Equal(t, expect, DurStr(input))
	}

	// Don't show decimal places
	f(time.Nanosecond, "1ns")
	f(999*time.Nanosecond, "999ns")
	f(time.Microsecond, "1µs")
	f(999*time.Microsecond, "999µs")

	// Show decimal places
	f(time.Millisecond, "1.00ms")
	f(999*time.Millisecond, "999.00ms")
	f(time.Second, "1.00s")
	f(59*time.Second, "59.00s")

	// Round up/down
	f(time.Microsecond+500*time.Nanosecond, "2µs")    // Round up
	f(time.Millisecond+999*time.Nanosecond, "1.00ms") // Round down

	// Combine
	f(time.Minute, "1m0s")
	f(time.Millisecond+560*time.Microsecond, "1.56ms")
	f(time.Minute+30*time.Second, "1m30s")
	f(10*time.Minute+30*time.Second+500*time.Millisecond, "10m30.5s")
	f(60*time.Minute+30*time.Second+500*time.Millisecond, "1h0m30.5s")
}
