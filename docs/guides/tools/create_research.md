# create_research

## Overview

`create_research` creates a research project together with its nested questions in
one call. Each question becomes a node you can later answer with findings, so the
research is a living record of what you set out to learn and what you discovered.

## When & how to use

Reach for `create_research` at the start of an investigation — before planning —
to frame the open questions you need answered. Pass `ticket_id` to link the
research under the ticket it informs.

Required fields: `name`, `goal`, `summary`, and `questions`. Each question carries
`question` text and optional `context` explaining why it matters.

```jsonc
create_research({
  "name": "Cache system analysis",
  "goal": "Understand current cache invalidation and hot paths",
  "summary": "Cache architecture audit",
  "questions": [
    { "question": "What is the current cache invalidation strategy?", "context": "Baseline before changes" },
    { "question": "Where are the hot paths?", "context": "Guide optimization work" }
  ]
})
```

As you investigate, answer each question with a finding so the research closes out
with evidence. For the full field reference, run `help("create_research")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `format` | string |  |  | Output format: 'text' (default, walks the tree) or 'json' (structured: {id, name, question_ids}). |
| `goal` | string | yes |  | What this research aims to answer |
| `name` | string | yes |  | Research project name |
| `questions` | array of object | yes |  | Ordered list of research questions. Each question REQUIRES a summary (search-optimized rendering of the question itself, distinct from context which is background). |
| `questions[]` | object |  |  | Question object: {"question":"...","summary":"required search-optimized one-line summary, max 500 chars","context":"why this question matters"} |
| `questions[].context` | string |  |  | Why this question matters (background) |
| `questions[].question` | string |  |  | The research question (required) |
| `questions[].summary` | string |  |  | Required search-optimized one-line summary, max 500 chars (max length: 500) |
| `summary` | string | yes |  | Required search-optimized one-line summary, max 500 chars (handler enforces). (max length: 500) |
| `ticket_id` | string |  |  | Ticket node ID to link this research under (optional) |
<!-- END GENERATED: params -->
