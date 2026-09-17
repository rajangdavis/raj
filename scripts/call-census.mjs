#!/usr/bin/env node
// Call census over opencode's session store.
//
// The orchestrator's efficiency loop (docs/RECURSIVE-RAJ.md §8) should be fed by
// data, not memory. opencode keeps every session and tool call in opencode.db,
// and a `raj ctl` verb arrives as a bash part, so the calls we care about are
// one regex away. This reads the store directly — no plugin, no editor change,
// read-only — and answers "where did the calls go?".
//
// Usage (run where the sessions ran; the store is container-side):
//   node scripts/call-census.mjs [--db PATH] [--sessions N] [--seq TITLE]
//
// --db defaults to $XDG_DATA_HOME/opencode/opencode.db. --seq prints one
// session's verb sequence, collapsed into runs, which is how a loop's shape is
// read.

import { DatabaseSync } from "node:sqlite";
import { homedir } from "node:os";
import { join } from "node:path";

const argv = process.argv.slice(2);
const opt = (name, dflt) => {
  const i = argv.indexOf(name);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : dflt;
};
const dataHome = process.env.XDG_DATA_HOME || join(homedir(), ".local", "share");
const db = new DatabaseSync(opt("--db", join(dataHome, "opencode", "opencode.db")), { readOnly: true });

const isTool = "json_extract(data,'$.type')='tool'";
const tool = (name) => `json_extract(data,'$.tool')='${name}'`;
const VERB = /raj\s+ctl\s+([a-z-]+)/g;
const verbsOf = (cmd) => (cmd ? [...cmd.matchAll(VERB)].map((m) => m[1]) : []);

// --seq: one session's verb sequence, collapsed into runs.
const seqTitle = opt("--seq", "");
if (seqTitle) {
  const s = db
    .prepare("select id,title from session where title like ? order by time_updated desc limit 1")
    .get(`%${seqTitle}%`);
  if (!s) {
    console.error(`no session matching ${seqTitle}`);
    process.exit(1);
  }
  const rows = db
    .prepare(
      `select json_extract(data,'$.state.input.command') cmd from part
       where session_id=? and ${isTool} and ${tool("bash")} order by time_created`
    )
    .all(s.id);
  const seq = [];
  for (const { cmd } of rows) {
    const v = verbsOf(cmd);
    if (v.length) seq.push(...v);
    else if (cmd) seq.push("(sh)");
  }
  const runs = [];
  for (const v of seq) {
    const last = runs[runs.length - 1];
    if (last && last.v === v) last.n++;
    else runs.push({ v, n: 1 });
  }
  console.log(`${s.title}\n${seq.length} raj ctl verbs\n`);
  console.log(runs.map((r) => (r.n > 1 ? `${r.v}x${r.n}` : r.v)).join(" "));
  process.exit(0);
}

console.log("total tool calls:", db.prepare(`select count(*) n from part where ${isTool}`).get().n);
console.log("\nby tool:");
for (const r of db
  .prepare(`select json_extract(data,'$.tool') t, count(*) n from part where ${isTool} group by t order by n desc`)
  .all())
  console.log("  ", String(r.n).padStart(7), r.t);

const counts = { bash: 0, rajBash: 0 };
const verbs = {};
for (const { cmd } of db
  .prepare(`select json_extract(data,'$.state.input.command') cmd from part where ${isTool} and ${tool("bash")}`)
  .all()) {
  counts.bash++;
  const v = verbsOf(cmd);
  if (v.length) {
    counts.rajBash++;
    for (const name of v) verbs[name] = (verbs[name] || 0) + 1;
  }
}
console.log(`\nbash calls: ${counts.bash} (${counts.rajBash} contain raj ctl)`);
const ranked = Object.entries(verbs).sort((a, b) => b[1] - a[1]);
const total = ranked.reduce((s, [, n]) => s + n, 0);
console.log(`raj ctl verb invocations: ${total}`);
for (const [k, n] of ranked.slice(0, 20))
  console.log("  ", String(n).padStart(7), k, `${((100 * n) / total).toFixed(1)}%`);

// Adjacent transitions, per session, which is what exposes the loop.
const bySession = new Map();
for (const p of db
  .prepare(
    `select session_id sid, time_created t, json_extract(data,'$.state.input.command') cmd
     from part where ${isTool} and ${tool("bash")} order by sid, t`
  )
  .all()) {
  const v = verbsOf(p.cmd);
  const one = v.length ? v[0] : p.cmd ? "(sh)" : null;
  if (!one) continue;
  if (!bySession.has(p.sid)) bySession.set(p.sid, []);
  bySession.get(p.sid).push(one);
}
const trans = {};
for (const seq of bySession.values())
  for (let i = 1; i < seq.length; i++) {
    const k = `${seq[i - 1]} -> ${seq[i]}`;
    trans[k] = (trans[k] || 0) + 1;
  }
console.log("\ntop transitions:");
for (const [k, n] of Object.entries(trans).sort((a, b) => b[1] - a[1]).slice(0, 15))
  console.log("  ", String(n).padStart(7), k);

const n = Number(opt("--sessions", "10"));
console.log(`\nrecent sessions (top ${n}):`);
for (const r of db
  .prepare(
    `select s.title title, s.agent agent, round(s.cost,4) cost, count(*) calls, max(p.time_created) last
     from part p join session s on s.id=p.session_id where ${isTool.replace(/data/g, "p.data")}
     group by p.session_id order by last desc limit ?`
  )
  .all(n))
  console.log(
    "  ",
    String(r.calls).padStart(5),
    "calls  $" + String(r.cost).padEnd(7),
    String(r.agent || "").padEnd(7),
    "|",
    String(r.title || "").slice(0, 44)
  );
