# Benchmarks

Measured numbers only. Rationale and root causes live in INVESTIGATIONS.md;
open work lives in TODO.md.

Reproduce with `go test ./... -bench . -benchmem`. Every figure here came from
a run, not an estimate.

## Paste

Paste, a 1080-byte block into a 200-line file, setup excluded from both:

| cursors | path | time | allocs | pieces | ops | bytes stored |
|---|---|---|---|---|---|---|
| 1 | block | 7.5 us | 16 | 1 | 1 | 1080 |
| 1 | per-cursor | 7.0 us | 13 | 1 | 1 | 1080 |
| 4 | block | 6.9 us | 16 | 1 | 1 | 1080 |
| 4 | per-cursor | 22.2 us | 62 | 7 | 4 | 4320 |
| 16 | block | 7.2 us | 16 | 1 | 1 | 1080 |
| 16 | per-cursor | 95.5 us | 217 | 31 | 16 | 17280 |

The block path is flat in cursor count on every axis; the per-cursor path is
linear on all of them, including bytes stored, because each cursor appended its
own copy of the pasted text. At one cursor the two are within noise — the
dedicated path costs nothing to have.

No regression elsewhere: `DocInsert` is 791 ns at 1k pieces and 871 ns at 100k,
0 allocs, unchanged; `Spans` over a document built from 200 pastes is 138 ns.

## Clipboard

Copy captures piece records as well as text. Pasting an internal clip splices
those records, so it **appends nothing**, at any size:

| cursors | clipboard | pieces added | bytes stored |
|---|---|---|---|
| 1 | 40 B | 1 | 0 |
| 4 | 163 B | 7 | 0 |
| 16 | 655 B | 31 | 0 |
| 64 | 2623 B | 127 | 0 |

Verified at 100 B, 10 KB and 1 MB: zero bytes stored in every case. The clip
also outlives deletion of the region it came from, because stores never erase —
the same property that makes undo free. Pasted text keeps its original author,
so agent-written code stays tinted however often it is moved.

Timing, splice versus append:

| size | spliced | appended |
|---|---|---|
| 1 KB | 11.0 us, 6 allocs | 12.1 us, 9 allocs |
| 100 KB | 895 us, 6 allocs | 949 us, 9 allocs |

**The 100 KB row is not the paste.** Storage is O(1) either way; the cost is
`applyToIndex`, which materialises the inserted text to count newlines — O(bytes)
plus a full copy. That is the next thing to fix (below), and fixing it makes a
large internal paste genuinely O(pieces).

## External clipboard

A clipboard from another program cannot splice — its bytes are not in the
buffer — so the question was only whether to distribute it across cursors.

| cursors | clipboard | distribute | insert whole |
|---|---|---|---|
| 4 | 147 B | 7 pieces, 144 B, 8.5 us | 1 piece, 147 B, 3.7 us |
| 16 | 603 B | 31 pieces, 588 B, 24 us | 1 piece, 603 B, 5.0 us |
| 64 | 2475 B | 127 pieces, 2412 B, 127 us | 1 piece, 2475 B, 11 us |

The numbers do not decide it: distributing is 11x slower at 64 cursors and 127x
the pieces, but 127 us is a hundredth of a frame and both store the clipboard
once. It is a semantics question wearing a performance costume.

**Decided: distribute when the line count matches the cursor count.** The
alternative puts the whole clipboard at the primary cursor and leaves the other
N-1 cursors doing nothing, which is not a useful outcome under any reading. The
failure mode — a coincidental match producing an unwanted distribution — is rare
and costs one cmd+z. Mismatched counts still insert whole.

## Wrapping: break policy

Three policies, one scan; only the break-opportunity predicate differs.
Character wrap allows every position, word wrap only whitespace, hybrid adds the
separators that occur in code. Medians of five runs, 80 columns.

| case | char | word | hybrid | word/char | hyb/char | max spread |
|---|---:|---:|---:|---:|---:|---:|
| normal_code | 497 ns | 621 ns | 684 ns | 1.25x | 1.38x | 15% |
| prose_200 | 1,654 | 2,065 | 2,362 | 1.25x | 1.43x | 4% |
| long_token_5k | 36,338 | 41,752 | 45,587 | 1.15x | 1.25x | 16% |
| base64_4k | 29,456 | 32,017 | 35,711 | 1.09x | 1.21x | 21% |
| minified_4k | 32,840 | 36,449 | 49,909 | 1.11x | 1.52x | 17% |
| deep_path_3k | 23,902 | 26,745 | 33,263 | 1.12x | 1.39x | 24% |
| cjk_2k | 26,332 | 24,524 | 25,365 | 0.93x | 0.96x | 6% |
| emoji_1k | 14,641 | 13,650 | 13,933 | 0.93x | 0.95x | 129% |
| tabs_only | 35,293 | 35,982 | 36,431 | 1.02x | 1.03x | 28% |
| separators_1k | 7,618 | 8,211 | 9,030 | 1.08x | 1.19x | 5% |

Medians across cases: word/char 1.10x, hybrid/char 1.23x, hybrid/word 1.10x.
The two sub-1.0 rows have 129% and 6% run-to-run spread, so `emoji_1k` in
particular is noise and should not be read in either direction.

Row counts and break positions, 80 columns:

```
minified   char rows=57  widths [80 80 80 80 80]
           word rows=57  widths [80 80 80 80 80]
           hyb  rows=58  widths [79 79 79 80 79]

deep_path  char rows=40  widths [80 80 80 80 80]
           word rows=40  widths [80 80 80 80 80]
           hyb  rows=41  widths [73 80 80 80 80]

prose      char rows=3   widths [80 80]
           word rows=3   widths [78 74]
           hyb  rows=3   widths [78 74]
```

Word wrap is byte-identical to character wrap on minified output and deep paths,
because those lines hold no whitespace at all. It differs from char only where
whitespace exists. Hybrid's extra row is wasted right margin — 1 to 7 columns
per row, accumulating over 57 rows — which is inherent to any non-character wrap.

## Wrapping: viewport model

Four candidates for how the viewport addresses a wrapped document. 40k-line
corpus, 50-row pane, 80 columns. Operations per second:

| operation | A (line top) | B (prefix sum) | B' (composite) | B' + window cache |
|---|---:|---:|---:|---:|
| Resize (per width change) | 388,500,389/s | 57/s | 466,418/s | 464,253/s |
| ScrollTo (per keystroke) | 74,349,442/s | 62,189,055/s | 13,749/s | 1,565,680/s |
| ScrollBy one row | 375,516,335/s | 286,123,033/s | 937,207/s | 115,340,254/s |
| Center (seek) | 81,300,813/s | 68,870,523/s | 119,689/s | 12,676/s |
| Visible (per frame) | 71,449/s | 11,234,693/s | 63,528/s | 7,836,991/s |

Auxiliary memory beyond the document:

| document | A | B | B' | B' + cache |
|---|---:|---:|---:|---:|
| 1k lines (81 KB) | 0 B | 7.8 KB | 0 B | 1.4 KB |
| 40k lines (3.1 MB) | 0 B | 312.5 KB | 0 B | 1.4 KB |
| 500k lines (39 MB) | 0 B | 3.8 MB | 0 B | 1.4 KB |

Frequency-weighted composites, since `Visible` runs every frame and the rest
once per event:

| scenario | A | B | B' | B'+cache |
|---|---:|---:|---:|---:|
| keystroke (ScrollTo + Visible) | 14.01 us | 0.11 us | 88.47 us | 0.77 us |
| wheel tick (ScrollBy x3 + Visible) | 14.00 us | 0.10 us | 18.94 us | 0.15 us |
| resize step (Resize + Visible) | 14.00 us | 17,408 us | 17.89 us | 2.28 us |
| goto-line (Center + Visible) | 14.01 us | 0.10 us | 24.10 us | 79.02 us |

A at 14 us per keystroke is 0.084% of a 60 Hz frame, is O(pane height) rather
than O(document), and would win every row outright if given the same 1.4 KB
window cache. **Performance did not decide this** — see INVESTIGATIONS.md. What
the numbers do establish is that B's global prefix sum is disqualified: 17.4 ms
per width change is more than a whole frame, repeated dozens of times during a
drag, plus 3.8 MB on a large file.

## Wrapping: layout cost on overflow content

Row counting, zero allocations, 80 columns:

| line | bytes | rows | ns/op |
|---|---:|---:|---:|
| normal_code | 71 | 1 | 687 |
| prose_200 | 222 | 3 | 2,239 |
| emoji_1k | 4,000 | 25 | 12,573 |
| deep_path_3k | 3,200 | 41 | 35,677 |
| tabs_only | 2,000 | 50 | 35,049 |
| base64_4k | 4,000 | 50 | 35,298 |
| minified_4k | 4,500 | 58 | 46,031 |
| long_token_5k | 5,000 | 63 | 42,276 |

Roughly 100-300 MB/s, flat across policies. `WrapRows` walks without
materialising anything; `AppendWrap` takes a caller-owned buffer for the drawn
lines. A frame reusing one buffer allocates nothing. Appending fresh cost 1016
bytes and 7 allocations on a 63-row line, which is why the buffer is passed in.

## Workspace search

`BenchmarkRun`, 400 files of 200 lines in 20 directories (Go 1.22, linux/amd64):

| query | ns/op | what it does |
| --- | --- | --- |
| `handler7` | 9.0 M | matches early and often, so `MaxMatches` stops the walk |
| `zzz_no_such_thing` | 74.2 M | matches nothing, so every file is opened and scanned |

The second row is the one that mattered: a query that matches nothing is what
you have typed most of the way through every query, and the old pane paid it
once per keystroke on the event thread. The cap only bounds the queries that
match a lot. Debouncing does not make the walk faster — it makes a six-character
word cost one walk instead of six, off-thread, where a slow one delays a result
rather than a frame.

### Literal fast path and the scan buffer

Two changes, measured together on a 97.3 MB / 9,502-file tree (the Go 1.22
source distribution, `REPO_CORPUS=`), warm page cache, Go 1.22 linux/amd64, one
core. Baseline is the code as it was: every query compiled to a regexp, and a
fresh 64 KB `bufio.Scanner` buffer per file.

| query | baseline | after | speedup | B/op before | B/op after |
| --- | --- | --- | --- | --- | --- |
| literal, case-sensitive, no match | 328 ms | 176 ms | 1.9x | 578.8 M | 9.28 M |
| literal, case-insensitive, no match | 1,473 ms | 246 ms | **6.0x** | 578.8 M | 9.55 M |
| literal, case-insensitive, 25 hits | 1,584 ms | 258 ms | **6.1x** | 578.8 M | 9.55 M |
| regexp (control, same path both ways) | 325 ms | 317 ms | 1.0x | 578.8 M | 9.29 M |

Isolating the matcher from the walk and the I/O, over 20,000 real source lines:

| matcher | throughput | allocs/op |
| --- | --- | --- |
| `regexp`, case-sensitive literal | 567 MB/s | 1 |
| `bytes.Index` | 1,172 MB/s | 0 |
| `regexp` with `(?i)` | 52 MB/s | 1 |
| SWAR fold + `bytes.Index` | 588 MB/s | 2 |

`(?i)` was the whole story: **11x** slower than folding the line and calling
`bytes.Index`. A case-sensitive literal only gains 2x, because Go's regexp
already has a literal-prefix path.

The buffer was the whole allocation story: 64 KB per file over 9,502 files is
608 MB of garbage per search, and hoisting one buffer out of the walk removed
**62x** of the allocated bytes. It also accounts for most of the 1.9x on the
case-sensitive row — matching was never the bottleneck there.

### The fold, and why it is SWAR

| fold of 20,000 source lines | throughput |
| --- | --- |
| naive byte loop | 960 MB/s |
| SWAR, 8 bytes per word | 3,260 MB/s |
| `strings.ToLower` (allocates 1.1 MB) | 271 MB/s |

3.4x over the naive loop and 12x over the stdlib, zero allocations. Note the
ceiling: `copy` on the same machine runs at 46 GB/s, so this is 14x short of
memory bandwidth. See INVESTIGATIONS.md for why that gap is not worth closing.

### Debounce and cancellation

Not a throughput number: a concurrency one. Typing `handler` used to leave seven
walks running, because `gen` drops a stale *result* without stopping the *work*
that produces it. On a tree that searches slower than it is typed in, that is a
full concurrent read of every file per keystroke, and the cost lands on the disk
and the collector rather than on the search — so the symptom is a laggy editor.

| keystrokes | peak concurrent walks, before | after |
| --- | --- | --- |
| 3 | 3 | 2 |
| 24 | 24 | 2 |

Two rather than one because cancelling is not instantaneous: a replacement can
start while its predecessor is still noticing. The property that matters is that
the number does not grow with typing.

The debounce window now tracks measured cost — half the last completed search,
clamped to [60 ms, 500 ms] — because one constant cannot serve both a tree that
answers in 15 ms and one that takes two seconds. A cancelled search does not
update the measurement, or the window would collapse to the floor after the
first abandoned walk.

### Where a search actually spends its time

Measured against the ghostty checkout: 5,770 files opened, 43.3 MB read, 574,639
lines, warm page cache, one core. Stages are cumulative, each adding one layer
to the one above.

| stage | cumulative | this stage |
| --- | --- | --- |
| walk + stat | 16.7 ms | 16.7 |
| + open/close | 31.9 ms | 15.2 |
| + read every byte | 44.4 ms | 12.5 |
| + `bufio` line splitting | 61.5 ms | 17.1 |
| + matching | 62.5 ms | 1.0 |

Matching is one millisecond of sixty-two. Splitting the file into lines cost
seventeen — spent turning 574,639 lines into slices in order to find nothing,
since almost every file in a repository contains no match at all.

So `scan` reads each file whole into a reused buffer and sweeps it with
`bytes.Index`, counting newlines only across the span leading to a hit. A file
with no matches never looks for a line boundary.

| query on ghostty | before | after |
| --- | --- | --- |
| literal miss, case-sensitive | 62.5 ms | 43.5 ms |
| literal miss, case-insensitive | 88.1 ms | 48.6 ms |
| with 8 `Exclude` globs | 106.7 ms | 43.0 ms |
| with 10 `Include` globs | 77.2 ms | 19.9 ms |

The glob rows were the surprise: filtering made a search **slower** than not
filtering, because `filepath.Match` ran once per pattern per file — 46,000 calls
to avoid reading a hundred images. Patterns of the form `*.ext`, which is nearly
all of them, are now answered by comparing the extension.

Case-insensitive costs the same as case-sensitive now, where it used to cost 40%
more. The fold leaves bytes above 0x7f untouched, so a file with one accented
character in it is still swept rather than being handed to the regexp: 43 MB of
folding disappears into the I/O it overlaps with.

Remaining time is 51% syscalls (walk, open, close). That is what parallelism
attacks, and nothing else does — see TODO.md.

## Line index update

Finding the newlines in an 800 KB insertion, `BenchmarkNewlines*`:

| strategy | ns/op | B/op |
| --- | --- | --- |
| materialise the span, then scan the string | 224,167 | 802,816 |
| scan the op's inserted pieces where they lie | 12,638 | 0 |

The allocation is the point: it was a full copy of the pasted text, made to
count a handful of newlines and thrown away immediately. Reading the pieces is
also the only correct way to do it — see INVESTIGATIONS.md.


## Symbol scan

go-to-symbol rescans the buffer each time the overlay opens rather than caching
and invalidating, so the question was whether that is affordable on the
keystroke path.

| input | time | throughput |
| --- | --- | --- |
| 188 KB of Go, 2000 declarations | 0.42 ms | 444 MB/s |

It is a byte scan over line prefixes with no allocation per non-matching line,
so cost tracks file size rather than symbol count. At 0.42 ms for a file larger
than anything in this repository, a cache would be invalidation logic bought
with a third of a frame — the scan is cheaper than the bookkeeping to avoid it.


## Word completion

Candidates are recomputed on every keystroke, so the question is what that
costs against realistic open buffers.

| open buffers | time | throughput |
| --- | --- | --- |
| 5 files, 400 KB each (2 MB total) | 5.3 ms | 301 MB/s |

Cost tracks the total bytes open, because every buffer is rescanned for words
each time. At a more usual 50 KB a file that is well under a frame, but 5.3 ms
is a third of one — and this is the keystroke path, which elsewhere in raj is
held to a stricter standard than that (retokenising is deliberately kept off it
for the same reason). The scan is not the problem; rescanning unchanged buffers
is. See the TODO item on caching the word set per buffer version.
