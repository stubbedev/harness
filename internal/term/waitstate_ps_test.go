//go:build unix && !linux

package term

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCPUTimeTicks(t *testing.T) {
	t.Parallel()
	require.Equal(t, uint64(150), cpuTimeTicks("0:01.50"))
	require.Equal(t, uint64(6100), cpuTimeTicks("1:01"))
	require.Equal(t, uint64(366100), cpuTimeTicks("1:01:01"))
	require.Equal(t, uint64(8640000+100), cpuTimeTicks("1-00:00:01"))
}
