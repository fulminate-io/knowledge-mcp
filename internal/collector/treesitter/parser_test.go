// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGo(t *testing.T) {
	src := []byte(`package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`)
	p := NewParser()
	defer p.Close()

	tree, err := p.Parse(context.Background(), src, LangGo)
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	assert.Equal(t, "source_file", root.Type())
	assert.Positive(t, root.ChildCount())
}

func TestParseTypeScript(t *testing.T) {
	src := []byte(`import { useState } from 'react';

function App(): JSX.Element {
  const [count, setCount] = useState(0);
  return <div>{count}</div>;
}

export default App;
`)
	p := NewParser()
	defer p.Close()

	tree, err := p.Parse(context.Background(), src, LangTypeScript)
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	assert.Equal(t, "program", root.Type())
	assert.Positive(t, root.ChildCount())
}

func TestParseUnsupported(t *testing.T) {
	p := NewParser()
	defer p.Close()

	_, err := p.Parse(context.Background(), []byte("hello"), LangUnknown)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported language")
}

func TestParseEmptySource(t *testing.T) {
	p := NewParser()
	defer p.Close()

	tree, err := p.Parse(context.Background(), []byte(""), LangGo)
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	assert.Equal(t, "source_file", root.Type())
}
