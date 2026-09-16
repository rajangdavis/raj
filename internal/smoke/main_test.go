//go:build smoke

package smoke

import (
	"os"
	"testing"
)

// TestMain removes the cached build tree binary() creates. Without it every
// `make smoke` leaves a raj-smoke* directory in the temp dir, since the binary
// is built once per run and cached for the whole package.
func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}
