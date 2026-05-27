// SPDX-License-Identifier: Apache-2.0

// Command bench runs the PDF collector against an input file with
// runtime/pprof CPU and heap profiles attached. Throwaway investigation
// tool — not a maintained benchmark surface.
//
// Usage:
//
//	go build -o bin/bench ./cmd/bench
//	./bin/bench --pdf /abs/path/file.pdf --cpuprofile cpu.out --memprofile mem.out -n 3
//	go tool pprof -http=: cpu.out
//	go tool pprof -http=: mem.out
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func main() {
	pdfPath := flag.String("pdf", "", "absolute path to a PDF file (required)")
	cpuProfile := flag.String("cpuprofile", "", "write CPU profile to this file")
	memProfile := flag.String("memprofile", "", "write heap profile to this file")
	iterations := flag.Int("n", 1, "iterations to run (>=1)")
	dumpNodeType := flag.String("dump", "", "after collect, print all SymbolNames of this node type (e.g. 'section')")
	dumpLimit := flag.Int("dump-limit", 0, "cap on dump output (0 = no cap)")
	dumpRepeats := flag.Bool("dump-repeats", false, "after collect, print the top text-repeating block fingerprints (running-header detector)")
	dumpAround := flag.String("dump-around", "", "after collect, print neighbors of the node whose Description contains this substring")
	regenGoldens := flag.String("regen-goldens", "", "absolute path to a fixture directory containing source.pdf; rewrites chunks.golden.json + sections.golden.json from current chunker output")
	flag.Parse()

	if *regenGoldens != "" {
		if err := regenGoldensInDir(*regenGoldens); err != nil {
			log.Fatalf("regen-goldens: %v", err)
		}
		return
	}

	if *pdfPath == "" {
		log.Fatal("--pdf is required (absolute path to a PDF file)")
	}
	if !filepath.IsAbs(*pdfPath) {
		log.Fatalf("--pdf must be absolute, got %q", *pdfPath)
	}
	if *iterations < 1 {
		log.Fatalf("-n must be >= 1, got %d", *iterations)
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatalf("create cpu profile: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("start cpu profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	c := &pdfcollector.PDFCollector{}
	ctx := context.Background()
	opts := collector.CollectOptions{}

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	var totalDuration time.Duration
	var nodes, edges int
	for i := 0; i < *iterations; i++ {
		start := time.Now()
		result, err := c.Collect(ctx, *pdfPath, opts)
		elapsed := time.Since(start)
		if err != nil {
			log.Fatalf("iter %d: collect: %v", i, err)
		}
		nodes = len(result.Nodes)
		edges = len(result.Edges)
		totalDuration += elapsed
		fmt.Printf("iter=%d nodes=%d edges=%d duration=%s\n", i, nodes, edges, elapsed)
	}

	runtime.ReadMemStats(&memAfter)

	avg := totalDuration / time.Duration(*iterations)
	fmt.Println()
	fmt.Println("--- summary ---")
	fmt.Printf("pdf=%s\n", *pdfPath)
	fmt.Printf("iterations=%d\n", *iterations)
	fmt.Printf("avg_duration=%s\n", avg)
	fmt.Printf("nodes=%d\n", nodes)
	fmt.Printf("edges=%d\n", edges)
	fmt.Printf("heap_alloc_delta=%d bytes\n", int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc))
	fmt.Printf("total_alloc_delta=%d bytes\n", memAfter.TotalAlloc-memBefore.TotalAlloc)
	fmt.Printf("mallocs_delta=%d\n", memAfter.Mallocs-memBefore.Mallocs)

	lastResult, lastErr := c.Collect(ctx, *pdfPath, opts)
	if lastErr != nil {
		log.Fatalf("final collect for inspection: %v", lastErr)
	}
	printTypeBreakdown(lastResult.Nodes)
	if *dumpRepeats {
		printRepeatedFingerprints(lastResult.Nodes)
	}
	if *dumpNodeType != "" {
		printNodesOfType(lastResult.Nodes, *dumpNodeType, *dumpLimit)
	}
	if *dumpAround != "" {
		printNodesAround(lastResult.Nodes, *dumpAround)
	}

	if *memProfile != "" {
		f, err := os.Create(*memProfile)
		if err != nil {
			log.Fatalf("create mem profile: %v", err)
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatalf("write heap profile: %v", err)
		}
	}
}

func printTypeBreakdown(nodes []*knowledgev1.Node) {
	counts := make(map[string]int)
	for _, n := range nodes {
		counts[n.GetType()]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	fmt.Println()
	fmt.Println("--- type breakdown ---")
	for _, k := range keys {
		fmt.Printf("  %-12s %d\n", k, counts[k])
	}
}

func printNodesOfType(nodes []*knowledgev1.Node, want string, limit int) {
	fmt.Printf("\n--- dump type=%s ---\n", want)
	count := 0
	for _, n := range nodes {
		if n.GetType() != want {
			continue
		}
		preview := n.GetSymbolName()
		if preview == "" {
			preview = n.GetDescription()
		}
		preview = strings.ReplaceAll(preview, "\n", " ⏎ ")
		if len(preview) > 140 {
			preview = preview[:137] + "..."
		}
		hlevel := n.GetMetadata()["heading_level"]
		pf := n.GetMetadata()["page_first"]
		pl := n.GetMetadata()["page_last"]
		page := pf
		if pl != "" && pl != pf {
			page = pf + "-" + pl
		}
		fmt.Printf("  [%s] p=%-7s lvl=%-1s %s\n", n.GetId(), page, hlevel, preview)
		count++
		if limit > 0 && count >= limit {
			fmt.Printf("  ... (truncated at %d)\n", limit)
			return
		}
	}
	fmt.Printf("  (%d total)\n", count)
}

func printRepeatedFingerprints(nodes []*knowledgev1.Node) {
	fp := make(map[string]int)
	first := make(map[string]string)
	for _, n := range nodes {
		f := stripDigitsLower(n.GetSymbolName())
		if len(f) < 3 {
			continue
		}
		fp[f]++
		if _, ok := first[f]; !ok {
			first[f] = n.GetSymbolName()
		}
	}
	type entry struct {
		fp    string
		first string
		count int
	}
	rows := make([]entry, 0, len(fp))
	for f, c := range fp {
		if c < 3 {
			continue
		}
		rows = append(rows, entry{f, first[f], c})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	fmt.Println()
	fmt.Println("--- top repeated SymbolName fingerprints (count >= 3, after digit-strip + lowercase) ---")
	for i, r := range rows {
		if i >= 25 {
			fmt.Printf("  ... %d more\n", len(rows)-25)
			break
		}
		fmt.Printf("  %4d  %q\n", r.count, r.first)
	}
}

func printNodesAround(nodes []*knowledgev1.Node, needle string) {
	idx := -1
	for i, n := range nodes {
		if strings.Contains(n.GetDescription(), needle) {
			idx = i
			break
		}
	}
	if idx < 0 {
		fmt.Printf("\n--- nodes around %q: not found ---\n", needle)
		return
	}
	lo := max(idx-5, 0)
	hi := idx + 5
	if hi >= len(nodes) {
		hi = len(nodes) - 1
	}
	fmt.Printf("\n--- nodes around %q (idx=%d, range=[%d..%d]) ---\n", needle, idx, lo, hi)
	for i := lo; i <= hi; i++ {
		n := nodes[i]
		text := n.GetDescription()
		text = strings.ReplaceAll(text, "\n", " ⏎ ")
		if len(text) > 100 {
			text = text[:97] + "..."
		}
		marker := "   "
		if i == idx {
			marker = "→  "
		}
		fmt.Printf("  %s%4d [%s] type=%-12s page=%s text=%s\n", marker, i, n.GetId(), n.GetType(), n.GetMetadata()["page_first"], text)
	}
}

func stripDigitsLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
