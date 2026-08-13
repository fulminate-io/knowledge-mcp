# ReplaceBucket / withReplaced call-site census

Run before adding `SegmentedIndex.ReplaceBucketGroup`. Regenerate with:

```
ast({operation:"match", language:"go", pattern:"$X.ReplaceBucket($$$A)", include_tests:true})
```

**A bare `grep -rn '\.ReplaceBucket('` MERGES TWO DIFFERENT METHODS** and that
error has already been made once on this step. There are two:

- `SegmentedIndex.ReplaceBucket(bucket, bucketCount, constituents, superseded, docs)` —
  engine level, `searchengine/bucket_swap.go`.
- `Manager.ReplaceBucket(ctx, gt, name, superseded, docs)` — owner level,
  `segmentdist/manager_bucket.go`, which derives the partition count and fans out.

The census splits them by receiver. 23 sites total, both flavors, tests included.

## Engine level — `SegmentedIndex.ReplaceBucket` (10)

| file | line | receiver | kind |
| --- | --- | --- | --- |
| `segmentdist/manager_bucket.go` | 282 | `dm.engine` | **production** — the per-partition fan-out inside `replaceBucketGroups` |
| `segmentdist/duplicate_layer_diag_test.go` | 76 | `dm.engine` | investigation instrumentation (serial drive) |
| `searchengine/bucket_swap_test.go` | 65 | `e` | reclaim hook fires |
| `searchengine/bucket_swap_test.go` | 108 | `e` | reclaim hook fires |
| `searchengine/bucket_swap_alias_test.go` | 35 | `e` | alias helper `aliasedReEmit` |
| `searchengine/bucket_swap_alias_test.go` | 132 | `e` | hook never reports its own merged id |
| `searchengine/bucket_swap_alias_test.go` | 167 | `e` | hook never reports its own merged id |
| `searchengine/duplicate_layer_ledger_test.go` | 125 | `e` | offered/admitted/survived ledger |
| `searchengine/formats/hnsw/bucket_swap_test.go` | 144 | `eng` | no duplicate window |
| `searchengine/formats/hnsw/bucket_swap_test.go` | 229 | `eng` | pure-delete consolidates |

## Owner level — `Manager.ReplaceBucket` (13)

| file | line | receiver | kind |
| --- | --- | --- | --- |
| `segmentdist/manager_bucket.go` | 164 | `m` | **production** — `DeleteFromBuckets`, the pure-delete shape |
| `segmentdist/manager_bucket_test.go` | 134, 143 | `mgr` | publishes complete manifest |
| `segmentdist/manager_bucket_test.go` | 166, 176 | `mgr` | ships once per call |
| `segmentdist/manager_bucket_test.go` | 245 | `mgr` | embed drain coalesces onto the tick |
| `segmentdist/manager_bucket_countchange_test.go` | 52, 224, 263, 301 | `mgr` | partition-count change |
| `segmentdist/manager_bucket_defect_test.go` | 66 | `mgr` | re-emit keeps partitions pure |
| `segmentdist/duplicate_layer_repro_test.go` | 84, 91 | `mgrA`, `mgrB` | two-layer fixture seeding |

## `withReplaced` (`searchengine/segmentset.go:74`)

Single caller before this change: `SegmentedIndex.ReplaceBucket`. The group form
adds `withReplacedGroup`, which takes the group's whole removal set and ALL of its
new entries so one CAS carries the entire group.

`withReplaced` rebuilds the WHOLE route map — O(resident corpus) per call. Paying
that once per group instead of once per partition is why the group form is also
the cheaper one, and why serialising the swaps is the worst option on both axes.

## Disposition

`ReplaceBucket` is **retained** — every site above keeps compiling unchanged.
`ReplaceBucketGroup` is added alongside it, and the one production fan-out
(`manager_bucket.go:282`) moves to it.
