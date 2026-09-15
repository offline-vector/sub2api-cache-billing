package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountIDWhitelist(t *testing.T) {
	require.Equal(t, "[7,42]", normalizeAccountIDWhitelist("42, 7, 42, 0, invalid"))
	require.Equal(t, "[7,42]", normalizeAccountIDWhitelist(`[42,7,42,-1]`))
	require.Equal(t, "[]", normalizeAccountIDWhitelist(""))
}

func TestParseAccountIDWhitelist(t *testing.T) {
	got := parseAccountIDWhitelist("7\n42")
	require.Equal(t, map[int64]struct{}{7: {}, 42: {}}, got)
}
