package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootVersionFlag(t *testing.T) {
	cmd := newRootCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--version"})

	err := cmd.Execute()
	require.NoError(t, err)
	require.Equal(t, "bns version dev\n", out.String())
}
