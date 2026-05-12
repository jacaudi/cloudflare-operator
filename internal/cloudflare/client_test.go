/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
