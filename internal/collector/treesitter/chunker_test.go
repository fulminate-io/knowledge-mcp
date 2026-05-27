// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkGoFunction(t *testing.T) {
	src := []byte(`package main

import "fmt"

func hello(name string) string {
	return fmt.Sprintf("hello %s", name)
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "main.go", src)
	require.NoError(t, err)
	assert.Equal(t, "main.go", result.FilePath)
	assert.Equal(t, LangGo, result.Language)

	// Should have exactly one function chunk.
	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.Len(t, funcChunks, 1)

	chunk := funcChunks[0]
	assert.Equal(t, "hello", chunk.Name)
	assert.Equal(t, "function_declaration", chunk.ChunkType)
	assert.Contains(t, chunk.Content, "func hello")
	assert.Contains(t, chunk.Content, "return fmt.Sprintf")
	assert.Equal(t, 5, chunk.StartLine)
	assert.Equal(t, 7, chunk.EndLine)

	// Context should include imports and package name.
	assert.Equal(t, "main", chunk.Context.PackageName)
	assert.Contains(t, chunk.Context.Imports, "fmt")
	assert.NotEmpty(t, chunk.Context.Signature)
}

func TestChunkGoMethod(t *testing.T) {
	src := []byte(`package service

type UserService struct {
	db *DB
}

func (s *UserService) GetUser(id string) (*User, error) {
	return s.db.FindUser(id)
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "service.go", src)
	require.NoError(t, err)

	methodChunks := filterChunks(result.Chunks, "method_declaration")
	require.Len(t, methodChunks, 1)

	chunk := methodChunks[0]
	assert.Equal(t, "GetUser", chunk.Name)
	assert.Equal(t, "UserService", chunk.ParentName)
	assert.Contains(t, chunk.Context.Signature, "func (s *UserService) GetUser")
}

func TestChunkGoStruct(t *testing.T) {
	src := []byte(`package model

type User struct {
	ID    string
	Name  string
	Email string
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "model.go", src)
	require.NoError(t, err)

	typeChunks := filterChunks(result.Chunks, "type_declaration")
	require.Len(t, typeChunks, 1)

	chunk := typeChunks[0]
	assert.Equal(t, "User", chunk.Name)
	assert.Contains(t, chunk.Content, "type User struct")
	assert.Contains(t, chunk.Content, "ID    string")
}

func TestChunkGoImports(t *testing.T) {
	src := []byte(`package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println(os.Args)
	strings.Join(os.Args, " ")
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "main.go", src)
	require.NoError(t, err)

	// Check that imports are extracted as context.
	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.NotEmpty(t, funcChunks)

	ctx := funcChunks[0].Context
	assert.Contains(t, ctx.Imports, "fmt")
	assert.Contains(t, ctx.Imports, "os")
	assert.Contains(t, ctx.Imports, "strings")
	assert.Equal(t, "main", ctx.PackageName)
}

func TestChunkGoEdges(t *testing.T) {
	src := []byte(`package service

import "database/sql"

type BaseService struct{}

type UserService struct {
	BaseService
	db *sql.DB
}

func (s *UserService) GetUser(id string) error {
	return s.db.QueryRow(id).Scan()
}

func CreateService() *UserService {
	return &UserService{}
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "service.go", src)
	require.NoError(t, err)

	// CONTAINS edges: file → declarations.
	containsEdges := filterEdges(result.Edges, EdgeContains)
	assert.NotEmpty(t, containsEdges, "should have CONTAINS edges")

	// Check for file → function CONTAINS edges.
	hasFileToGetUser := false
	hasFileToCreateService := false
	hasTypeToMethod := false
	for _, e := range containsEdges {
		// Methods use receiver-qualified names: "service.UserService.GetUser"
		if e.FromID == "service.go" && e.ToID == "service.UserService.GetUser" {
			hasFileToGetUser = true
		}
		if e.FromID == "service.go" && e.ToID == "service.CreateService" {
			hasFileToCreateService = true
		}
		if e.FromID == "service.UserService" && e.ToID == "service.UserService.GetUser" {
			hasTypeToMethod = true
		}
	}
	assert.True(t, hasFileToGetUser, "should have file→UserService.GetUser CONTAINS edge")
	assert.True(t, hasFileToCreateService, "should have file→CreateService CONTAINS edge")
	assert.True(t, hasTypeToMethod, "should have UserService→UserService.GetUser CONTAINS edge")

	// CALLS edges.
	callsEdges := filterEdges(result.Edges, EdgeCalls)
	assert.NotEmpty(t, callsEdges, "should have CALLS edges")

	// IMPORTS edge.
	importEdges := filterEdges(result.Edges, EdgeImports)
	hasDBImport := false
	for _, e := range importEdges {
		if e.ToID == "database/sql" {
			hasDBImport = true
		}
	}
	assert.True(t, hasDBImport, "should have IMPORTS edge for database/sql")

	// EMBEDS edge.
	embedEdges := filterEdges(result.Edges, EdgeEmbeds)
	hasBaseEmbed := false
	for _, e := range embedEdges {
		if e.ToID == "BaseService" {
			hasBaseEmbed = true
		}
	}
	assert.True(t, hasBaseEmbed, "should have EMBEDS edge for BaseService")
}

func TestChunkTSFunction(t *testing.T) {
	src := []byte(`import { useState } from 'react';

function App() {
  const [count, setCount] = useState(0);
  return count;
}

export default App;
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "App.ts", src)
	require.NoError(t, err)

	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.NotEmpty(t, funcChunks)

	// Find the App function.
	var appChunk *Chunk
	for i := range funcChunks {
		if funcChunks[i].Name == "App" {
			appChunk = &funcChunks[i]
			break
		}
	}
	require.NotNil(t, appChunk, "should find App function chunk")
	assert.Contains(t, appChunk.Content, "function App")

	// Imports.
	assert.Contains(t, appChunk.Context.Imports, "react")
}

func TestChunkTSClass(t *testing.T) {
	src := []byte(`class Animal {
  name: string;

  constructor(name: string) {
    this.name = name;
  }

  speak(): string {
    return this.name;
  }
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "animal.ts", src)
	require.NoError(t, err)

	typeChunks := filterChunks(result.Chunks, "class_declaration")
	require.NotEmpty(t, typeChunks)

	assert.Equal(t, "Animal", typeChunks[0].Name)
	assert.Contains(t, typeChunks[0].Content, "class Animal")
}

func TestLargeFunctionSingleChunk(t *testing.T) {
	// Create a function with a very large body — should remain a single chunk.
	var builder strings.Builder
	builder.WriteString("package main\n\n")
	builder.WriteString("func bigFunc() {\n")
	for range 100 {
		builder.WriteString("\tprintln(\"this is line number ")
		builder.WriteString(strings.Repeat("x", 20))
		builder.WriteString("\")\n")
	}
	builder.WriteString("}\n")

	src := []byte(builder.String())

	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "big.go", src)
	require.NoError(t, err)

	// Large function should NOT be split — kept as a single chunk.
	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.Len(t, funcChunks, 1, "large function should be a single chunk")
	assert.Equal(t, "bigFunc", funcChunks[0].Name)
	assert.NotEmpty(t, funcChunks[0].Content)
	assert.Contains(t, funcChunks[0].Content, "func bigFunc()")
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input   string
		wantMin int
		wantMax int
	}{
		{"", 0, 0},
		{"abc", 1, 1},
		{"func hello() {}", 5, 6},
		{strings.Repeat("x", 300), 99, 101},
	}

	for _, tt := range tests {
		got := estimateTokens(tt.input)
		assert.GreaterOrEqual(t, got, tt.wantMin, "for input len %d", len(tt.input))
		assert.LessOrEqual(t, got, tt.wantMax, "for input len %d", len(tt.input))
	}
}

func TestChunkEmptyFile(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "empty.go", []byte("package empty\n"))
	require.NoError(t, err)
	assert.Empty(t, result.Chunks)
}

func TestChunkUnsupportedFile(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	_, err := chunker.ChunkFile(context.Background(), "image.png", []byte{0x89, 0x50, 0x4e, 0x47})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported file type")
}

func TestChunkSyntaxError(t *testing.T) {
	// Tree-sitter is error-tolerant — it should still produce partial results.
	src := []byte(`package broken

func incomplete( {
	fmt.Println("missing close paren")
}

func valid() {
	fmt.Println("this is valid")
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "broken.go", src)
	require.NoError(t, err)
	// Should still extract at least the valid function.
	assert.NotEmpty(t, result.Chunks)
}

func TestChunkWithoutContext(t *testing.T) {
	src := []byte(`package main

import "fmt"

func hello() {
	fmt.Println("hello")
}
`)
	chunker := NewChunker()
	chunker.config.includeContext = false // disable context
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "main.go", src)
	require.NoError(t, err)

	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.NotEmpty(t, funcChunks)

	// Context should be empty when context is disabled.
	assert.Empty(t, funcChunks[0].Context.Imports)
	assert.Empty(t, funcChunks[0].Context.PackageName)
}

func TestChunkGoInitFunction(t *testing.T) {
	src := []byte(`package main

func init() {
	setup()
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "main.go", src)
	require.NoError(t, err)

	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.NotEmpty(t, funcChunks)
	assert.Equal(t, "init", funcChunks[0].Name)
}

func TestChunkGoInterface(t *testing.T) {
	src := []byte(`package service

type Repository interface {
	Find(id string) (*Entity, error)
	Save(entity *Entity) error
	Delete(id string) error
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "repo.go", src)
	require.NoError(t, err)

	typeChunks := filterChunks(result.Chunks, "type_declaration")
	require.NotEmpty(t, typeChunks)
	assert.Equal(t, "Repository", typeChunks[0].Name)
	assert.Contains(t, typeChunks[0].Content, "interface")
}

func TestChunkTSExportedFunction(t *testing.T) {
	src := []byte(`export function greet(name: string): string { return name; }

function helper() { return 1; }
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "greet.ts", src)
	require.NoError(t, err)

	funcChunks := filterChunks(result.Chunks, "function_declaration")
	require.Len(t, funcChunks, 2, "expected exactly 2 function_declaration chunks, got %d (no duplicates from export double-matching)", len(funcChunks))

	// Find greet and helper chunks.
	var greetChunk, helperChunk *Chunk
	for i := range funcChunks {
		switch funcChunks[i].Name {
		case "greet":
			greetChunk = &funcChunks[i]
		case "helper":
			helperChunk = &funcChunks[i]
		}
	}

	require.NotNil(t, greetChunk, "should find greet chunk")
	assert.True(t, greetChunk.Exported, "greet should be Exported=true")
	assert.Equal(t, "greet", greetChunk.Name)
	assert.Equal(t, ChunkType("function_declaration"), greetChunk.ChunkType)

	require.NotNil(t, helperChunk, "should find helper chunk")
	assert.False(t, helperChunk.Exported, "helper should be Exported=false")
}

func TestChunkHTMLScriptAndStyle(t *testing.T) {
	src := []byte(`<html><head><style>body { color: red; }</style></head><body><div><p>Hello</p></div><script>console.log("hi")</script></body></html>`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "page.html", src)
	require.NoError(t, err)

	// Should have script_element and style_element chunks from the HTML queries.
	scriptChunks := filterChunks(result.Chunks, "script_element")
	styleChunks := filterChunks(result.Chunks, "style_element")
	assert.NotEmpty(t, scriptChunks, "should have at least one script_element chunk")
	assert.NotEmpty(t, styleChunks, "should have at least one style_element chunk")

	// The old over-matching behavior produced an "element" chunk for EVERY nested
	// tag (<html>, <head>, <body>, <div>, <p>, etc.). The fixed query only captures
	// script_element and style_element. The outer <html> wrapper may appear as a
	// single orphan block chunk, but there must be at most 1 such element chunk —
	// not the 5+ that the old (element) @decl query would have produced.
	elementChunks := filterChunks(result.Chunks, "element")
	assert.LessOrEqual(t, len(elementChunks), 1, "should have at most 1 element chunk (outer wrapper orphan), not one per nested tag")
}

// Helper functions.

func filterChunks(chunks []Chunk, chunkType ChunkType) []Chunk {
	var filtered []Chunk
	for _, c := range chunks {
		if c.ChunkType == chunkType {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func filterEdges(edges []Edge, edgeType EdgeType) []Edge {
	var filtered []Edge
	for _, e := range edges {
		if e.Type == edgeType {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
