// SPDX-License-Identifier: Apache-2.0

package fixture

import "fmt"

func ExampleParse() {
	fmt.Println("hello")
	// Output: hello
}

type Parser struct{}

func (p *Parser) Parse(s string) string { return s }

func ExampleParser_Parse() {
	p := &Parser{}
	fmt.Println(p.Parse("ok"))
	// Output: ok
}
