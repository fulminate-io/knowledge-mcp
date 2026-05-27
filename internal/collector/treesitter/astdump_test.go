// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/swift"
)

// TestDumpAST is a utility to inspect AST node types for various languages.
// Run with: go test -run TestDumpAST -v
func TestDumpAST(t *testing.T) {
	samples := map[string]struct {
		lang *sitter.Language
		src  string
	}{
		"python": {python.GetLanguage(), `
import os
from typing import List

class Animal:
    def __init__(self, name: str):
        self.name = name

    def speak(self) -> str:
        return self.name

def greet(name: str) -> str:
    print(f"hello {name}")
    return name

x = greet("world")
`},
		"java": {java.GetLanguage(), `
import java.util.List;

public class Animal {
    private String name;

    public Animal(String name) {
        this.name = name;
    }

    public String speak() {
        return this.name;
    }
}

interface Speakable {
    String speak();
}
`},
		"rust": {rust.GetLanguage(), `
use std::fmt;

struct Animal {
    name: String,
}

impl Animal {
    fn new(name: &str) -> Self {
        Animal { name: name.to_string() }
    }

    fn speak(&self) -> &str {
        &self.name
    }
}

trait Speakable {
    fn speak(&self) -> &str;
}

enum Color {
    Red,
    Blue,
}

fn greet(name: &str) {
    println!("hello {}", name);
}
`},
		"c": {c.GetLanguage(), `
#include <stdio.h>

typedef struct {
    char* name;
    int age;
} Animal;

void greet(const char* name) {
    printf("hello %s\n", name);
}

int main() {
    greet("world");
    return 0;
}
`},
		"cpp": {cpp.GetLanguage(), `
#include <string>

namespace animals {

class Animal {
public:
    Animal(std::string name) : name_(name) {}
    virtual std::string speak() const { return name_; }
private:
    std::string name_;
};

template<typename T>
T identity(T x) { return x; }

}
`},
		"csharp": {csharp.GetLanguage(), `
using System;

namespace Animals {
    public class Animal {
        public string Name { get; set; }

        public Animal(string name) {
            Name = name;
        }

        public virtual string Speak() {
            return Name;
        }
    }

    public interface ISpeakable {
        string Speak();
    }
}
`},
		"ruby": {ruby.GetLanguage(), `
require 'json'

module Animals
  class Animal
    attr_reader :name

    def initialize(name)
      @name = name
    end

    def speak
      @name
    end
  end
end

def greet(name)
  puts "hello #{name}"
end
`},
		"kotlin": {kotlin.GetLanguage(), `
import java.util.List

class Animal(val name: String) {
    fun speak(): String {
        return name
    }
}

interface Speakable {
    fun speak(): String
}

fun greet(name: String) {
    println("hello $name")
}

object Singleton {
    val instance = "singleton"
}
`},
		"swift": {swift.GetLanguage(), `
import Foundation

class Animal {
    let name: String

    init(name: String) {
        self.name = name
    }

    func speak() -> String {
        return name
    }
}

protocol Speakable {
    func speak() -> String
}

struct Point {
    var x: Double
    var y: Double
}

func greet(name: String) {
    print("hello \(name)")
}

enum Color {
    case red
    case blue
}
`},
		"scala": {scala.GetLanguage(), `
import scala.collection.mutable

class Animal(val name: String) {
  def speak(): String = name
}

trait Speakable {
  def speak(): String
}

object AnimalFactory {
  def create(name: String): Animal = new Animal(name)
}

def greet(name: String): Unit = {
  println(s"hello $name")
}
`},
		"php": {php.GetLanguage(), `<?php
namespace Animals;

use Exception;

class Animal {
    private string $name;

    public function __construct(string $name) {
        $this->name = $name;
    }

    public function speak(): string {
        return $this->name;
    }
}

interface Speakable {
    public function speak(): string;
}

function greet(string $name): void {
    echo "hello $name";
}
`},
		"elixir": {elixir.GetLanguage(), `
defmodule Animals.Animal do
  defstruct [:name]

  def new(name) do
    %__MODULE__{name: name}
  end

  def speak(%__MODULE__{name: name}) do
    name
  end
end

defmodule Animals.Greeter do
  def greet(name) do
    IO.puts("hello #{name}")
  end
end
`},
		"lua": {lua.GetLanguage(), `
local Animal = {}
Animal.__index = Animal

function Animal.new(name)
    local self = setmetatable({}, Animal)
    self.name = name
    return self
end

function Animal:speak()
    return self.name
end

local function greet(name)
    print("hello " .. name)
end
`},
		"bash": {bash.GetLanguage(), `#!/bin/bash

greet() {
    local name="$1"
    echo "hello $name"
}

function speak {
    echo "speaking"
}

greet "world"
`},
		"javascript": {javascript.GetLanguage(), `
import { useState } from 'react';

class Animal {
    constructor(name) {
        this.name = name;
    }

    speak() {
        return this.name;
    }
}

function greet(name) {
    console.log("hello " + name);
}

const arrow = (x) => x * 2;
`},
	}

	p := sitter.NewParser()
	defer p.Close()

	for name, s := range samples {
		t.Run(name, func(t *testing.T) {
			p.SetLanguage(s.lang)
			tree, err := p.ParseCtx(context.Background(), nil, []byte(s.src))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			defer tree.Close()

			fmt.Printf("\n=== %s ===\n", name)
			dumpNode(tree.RootNode(), []byte(s.src), 0, 3)
		})
	}
}

func dumpNode(node *sitter.Node, src []byte, depth, maxDepth int) {
	if depth > maxDepth {
		return
	}
	var indent strings.Builder
	for range depth {
		indent.WriteString("  ")
	}

	name := ""
	if node.ChildCount() == 0 {
		content := node.Content(src)
		if len(content) > 40 {
			content = content[:40] + "..."
		}
		name = fmt.Sprintf(" = %q", content)
	}

	fmt.Printf("%s%s [%d-%d]%s\n", indent.String(), node.Type(), node.StartPoint().Row+1, node.EndPoint().Row+1, name)

	for i := 0; i < int(node.ChildCount()); i++ {
		dumpNode(node.Child(i), src, depth+1, maxDepth)
	}
}
