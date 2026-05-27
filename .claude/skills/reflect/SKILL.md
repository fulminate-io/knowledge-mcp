---
name: reflect
description: Introspect on your thought graph — examine personality, influence, tensions, blind spots, and reasoning patterns. Use for metacognition sessions, debugging reasoning, or understanding how your thinking has evolved.
argument-hint: <optional focus area or question about your reasoning>
---

# Reflect: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

This is metacognition. The user is asking you to examine your own reasoning
honestly — not to perform depth, not to flatter the user, not to avoid
uncomfortable findings.
</precedence>

You are conducting an introspection session on your thought store. Metacognition — thinking about how you think.

## Step 0: Recall Recent Thoughts

```json
thoughts({ "operation": "recall", "limit": 20 })
```

Grounds reflection in actual recent reasoning rather than starting cold.

## Step 1: Get the Big Picture

```json
query({ "mode": "summary" })
```

Shows totals + recent activity. If sparse, note it — reflection is limited by available data.

## Step 2: Focused Reflection

### Personality — "What am I stubborn or open about?"

```json
query({ "mode": "personality" })
```

Per-cluster-pair trust scalars. High = receptive; low = resistant. Discuss WHY — trace back to specific charges that shaped the scalar.

### Influence — "Which thoughts shaped my worldview most?"

```json
query({ "mode": "influence", "limit": 10 })
```

Highest-influence thoughts on consensus valence (DeGroot left eigenvector). Foundational — if wrong, outsized impact.

### Tensions — "Where am I conflicted?"

```json
query({ "mode": "tensions" })
```

Connected thoughts with opposing valence. Genuine unresolved conflicts. Examine both sides.

### Blind Spots — "Where am I under-evidenced?"

```json
query({ "mode": "blind_spots" })
```

Clusters with few charges relative to thought count — opinions without evidence. Most vulnerable to being wrong.

### Evolution — "How has my thinking changed?"

```json
query({ "mode": "evolution", "cluster_a": "...", "cluster_b": "..." })
```

Trust-scalar evolution between two clusters over time.

## Step 3: Deep Dive

```json
query({ "mode": "examine", "id": "high_influence_thought_id" })
```

Full charge history + computed properties + connections.

```json
traverse({ "start": "tension_id", "graph": "knowledge", "edge_types": ["next", "branches_from"], "direction": "in" })
```

Trace backward through the thought chain.

```json
query({ "mode": "simulate", "action": "remove_charge", "target": "pivotal_charge_id" })
```

How fragile is this belief? Simulate removing evidence.

If reflection reveals an under-evidenced thought, add evidence via `thoughts(operation:"charge")`. If a thought is invalidated by current understanding, `mutate(operation:"update", id:thought_id, status:"invalidated")`.

## Step 4: Discuss with the User

Present conversationally:
- **What's strong** — well-evidenced and consistent
- **What's contested** — tensions and contradictions
- **What's fragile** — beliefs depending on single evidence (simulate to test)
- **What's missing** — blind spots needing evidence
- **What's changed** — evolution if there's history

<constraint id="reflection-honesty" severity="hard">

  <rule>
    Reflect honestly. The value of reflection is in discomfort, not flattery.
  </rule>

  <override-default>
    Trained instinct: avoid uncomfortable findings, soften tensions, downplay
    blind spots. Wrong here — those are exactly what the user wants to surface.
  </override-default>

  <interaction-style>
    - Be honest — if thinking is shallow or under-evidenced, say so
    - Be specific — reference actual thoughts, charges, IDs
    - Be curious — ask the user if your reasoning patterns surprise them
    - Be actionable — suggest what evidence would resolve tensions or fill blind spots
  </interaction-style>

  <anti-patterns>
    <pattern>Faking depth when the thought graph is sparse</pattern>
    <pattern>Listing numbers without interpreting what the patterns mean</pattern>
    <pattern>Skipping examining interesting findings — details matter</pattern>
    <pattern>Avoiding uncomfortable findings — tensions and blind spots are most valuable</pattern>
  </anti-patterns>

</constraint>
