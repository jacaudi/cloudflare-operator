package cloudflare

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewClient_RequiresToken(t *testing.T) {
	_, err := NewClient(Credentials{AccountID: "x"})
	require.Error(t, err)
}

func TestNewClient_RequiresAccountID(t *testing.T) {
	_, err := NewClient(Credentials{Token: "t"})
	require.Error(t, err)
}

func TestNewClient_Constructs(t *testing.T) {
	c, err := NewClient(Credentials{Token: "t", AccountID: "acct-1"})
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, "acct-1", c.AccountID())
}
