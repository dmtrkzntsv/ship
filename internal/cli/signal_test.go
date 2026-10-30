package cli

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSignalContext: SIGINT, SIGTERM and SIGHUP cancel the command context
// instead of killing ship, so an interrupted update still reverts.
func TestSignalContext(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		ctx, stop := signalContext()
		require.NoError(t, syscall.Kill(os.Getpid(), sig))
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not cancel the context", sig)
		}
		stop()
	}
}
