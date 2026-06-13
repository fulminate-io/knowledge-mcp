// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// maxToolArgLen bounds how much of a tool_use input / function_call arguments
// blob is rendered per line — enough to identify what the tool did without
// dumping a full file body into the supervisor's context.
const maxToolArgLen = 200

// FormatTranscript renders the most-recent maxBytes of a worker transcript into
// a compact, human-/LLM-readable role/tool line stream for the Tier-2 hive
// supervisor to judge. It dispatches on handle.Format and returns the rendered
// text.
//
// Only the trailing maxBytes of the file are read: the supervisor judges from
// the recent tail (what the worker is doing NOW), and bounding the read bounds
// the LLM input. The seek may land mid-line; the first (partial) line after the
// seek is skipped by the scanner's natural line split — acceptable, the tail is
// what matters. Non-conversation / structural records are skipped via the same
// isConversation / payload-kind predicates the classifier uses.
//
// This is the only symbol that extracts transcript CONTENT; the Classify path
// only derives liveness and never surfaces text (extending it would break its
// single-purpose offset contract and every caller).
func FormatTranscript(handle TranscriptHandle, maxBytes int64) (string, error) {
	switch handle.Format {
	case FormatClaude:
		return formatClaudeTranscript(handle.Path, maxBytes)
	case FormatCodex:
		return formatCodexTranscript(handle.Path, maxBytes)
	default:
		return "", fmt.Errorf("FormatTranscript: unknown transcript format %q", handle.Format)
	}
}

// openTail opens path and seeks to max(0, size-maxBytes) so only the trailing
// maxBytes are read. Returns the open file (caller closes) and a scanner sized
// for large lines (the same 8MiB buffer idiom as the readers).
func openTail(path string, maxBytes int64) (*os.File, *bufio.Scanner, error) {
	f, err := os.Open(path) //nolint:gosec // path is a resolved transcript path, not user text.
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if start := info.Size() - maxBytes; start > 0 {
		if _, err := f.Seek(start, 0); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return f, sc, nil
}

// formatClaudeTranscript renders claude conversation records as `role: text`,
// `tool: name(args)`, and `tool_result: ...` lines.
func formatClaudeTranscript(path string, maxBytes int64) (string, error) {
	f, sc, err := openTail(path, maxBytes)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !isConversation(rec.Type) || rec.Message == nil {
			continue
		}
		renderClaudeRecord(&b, rec.Type, rec.Message.Content)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderClaudeRecord appends one conversation record's blocks to b: text blocks
// as `role: text`, tool_use as `tool: name(...)`, tool_result as `tool_result`.
func renderClaudeRecord(b *strings.Builder, role string, blocks []claudeBlock) {
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			if t := strings.TrimSpace(blk.Text); t != "" {
				fmt.Fprintf(b, "%s: %s\n", role, t)
			}
		case "tool_use":
			fmt.Fprintf(b, "tool: %s\n", blk.Name)
		case "tool_result":
			b.WriteString("tool_result\n")
		}
	}
}

// formatCodexTranscript renders codex rollout payloads as `message: text`,
// `reasoning: text`, `tool: name(args)`, and `tool_result: output` lines.
func formatCodexTranscript(path string, maxBytes int64) (string, error) {
	f, sc, err := openTail(path, maxBytes)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var rec codexRolloutRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Payload == nil {
			continue
		}
		renderCodexPayload(&b, rec.Payload)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderCodexPayload appends one rollout payload to b based on its kind.
func renderCodexPayload(b *strings.Builder, p *codexRolloutPayload) {
	switch {
	case isCodexToolCall(p.PayloadType):
		fmt.Fprintf(b, "tool: %s(%s)\n", p.Name, truncate(p.Arguments, maxToolArgLen))
	case isCodexToolOutput(p.PayloadType):
		if out := strings.TrimSpace(p.Output); out != "" {
			fmt.Fprintf(b, "tool_result: %s\n", truncate(out, maxToolArgLen))
		} else {
			b.WriteString("tool_result\n")
		}
	case p.PayloadType == "message" || p.PayloadType == "reasoning":
		if text := codexContentText(p); text != "" {
			fmt.Fprintf(b, "%s: %s\n", p.PayloadType, text)
		}
	}
}

// codexContentText flattens a message/reasoning payload's content into a single
// trimmed string. content decodes either as an array of {type,text} blocks (the
// real rollout shape) or as a flat string; either is handled, plus the flat Text
// field as a last resort.
func codexContentText(p *codexRolloutPayload) string {
	if t := strings.TrimSpace(p.Text); t != "" {
		return t
	}
	if len(p.Content) == 0 {
		return ""
	}
	var blocks []codexContentBlock
	if err := json.Unmarshal(p.Content, &blocks); err == nil {
		var parts []string
		for _, c := range blocks {
			if t := strings.TrimSpace(c.Text); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, " ")
	}
	var flat string
	if err := json.Unmarshal(p.Content, &flat); err == nil {
		return strings.TrimSpace(flat)
	}
	return ""
}

// truncate clips s to at most n runes, appending an ellipsis marker when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
