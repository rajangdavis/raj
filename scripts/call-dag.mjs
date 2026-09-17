#!/usr/bin/env node
// Call-flow DAG over opencode's session store.
//
// The census (tools/call-census.mjs) says *how many* calls go where; this says
// what shape the flow is. Verbs are mapped to phases — explore, decide, write,
// verify, identity — and consecutive same-phase calls are collapsed, so each
// session becomes a phase chain and the aggregate is a graph of where the work
// actually goes. Emits Graphviz DOT by default, Mermaid with --mermaid.
//
// Usage:
//   node scripts/call-dag.mjs [--db PATH] [--verbs] [--mermaid] [--seq TITLE] [--min N]
//
//   (default)        aggregate phase DAG over every session
//   --verbs          aggregate verb DAG instead of phases
//   --seq TITLE      one session's linear flow (runs collapsed), as a DAG
//   --min N          drop edges lighter than N (default 1)

import { DatabaseSync } from "node:sqlite";
import { homedir } from "node:os";
import { join } from "node:path";

const argv = process.argv.slice(2);
const has = (n) => argv.includes(n);
const opt = (n, d) => { const i = argv.indexOf(n); return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d; };
const dataHome = process.env.XDG_DATA_HOME || join(homedir(), ".local", "share");
const db = new DatabaseSync(opt("--db", join(dataHome, "opencode", "opencode.db")), { readOnly: true });
const data = (col) => `json_extract(${col},'$.state.input.command')`;
const VERB = /raj\s+ctl\s+([a-z-]+)/g;
const verbsOf = (cmd) => (cmd ? [...cmd.matchAll(VERB)].map((m) => m[1]) : []);

const PHASE = {
  search: "explore", read: "explore", open: "explore", version: "explore", buffers: "explore",
  ls: "explore", find: "explore", list: "explore", goto: "explore", diff: "explore",
  groups: "decide", proposals: "decide", review: "decide", accept: "decide",
  reject: "decide", clear: "decide", claim: "decide", deletions: "decide", rmdirs: "decide",
  edit: "write", apply: "write", patch: "write", dump: "write", mkdir: "write", rename: "write",
  delete: "write", rmdir: "write", revert: "write", save: "write",
  lsp: "verify", exec: "verify", stats: "verify",
  register: "identity", whoami: "identity", who: "identity", hello: "identity", recv: "talk",
  close: "lifecycle", help: "meta", review: "decide",
};
const phase = (v) => PHASE[v] || "other";

// Every session's verb sequence.
const bySession = new Map();
for (const p of db
  .prepare(`select session_id sid, time_created t, ${data("data")} cmd from part
            where json_extract(data,'$.type')='tool' and json_extract(data,'$.tool')='bash'
            order by sid, t`)
  .all()) {
  const v = verbsOf(p.cmd);
  const one = v.length ? v[0] : p.cmd ? "(sh)" : null;
  if (!one) continue;
  if (!bySession.has(p.sid)) bySession.set(p.sid, []);
  bySession.get(p.sid).push(one);
}

const title = opt("--seq", "");
if (title) {
  const s = db.prepare("select id,title from session where title like ? order by time_updated desc limit 1").get(`%${title}%`);
  if (!s) { console.error(`no session matching ${title}`); process.exit(1); }
  const seq = bySession.get(s.id) || [];
  const runs = [];
  for (const v of seq) { const l = runs[runs.length - 1]; if (l && l.v === v) l.n++; else runs.push({ v, n: 1 }); }
  console.log(`// ${s.title} — ${seq.length} calls, ${runs.length} runs`);
  emit(runs.map((r) => ({ id: r.v + r.n, label: r.n > 1 ? `${r.v} x${r.n}` : r.v, group: phase(r.v) })),
       runs.slice(1).map((r, i) => [runs[i].v + runs[i].n, r.v + r.n]));
  process.exit(0);
}

// Collapse consecutive same-phase calls, then count transitions.
const edges = new Map();
const nodes = new Map();
for (const seq of bySession.values()) {
  const units = has("--verbs")
    ? seq
    : seq.map(phase).filter((p, i, a) => i === 0 || p !== a[i - 1]);
  for (const u of units) nodes.set(u, (nodes.get(u) || 0) + 1);
  for (let i = 1; i < units.length; i++) {
    const k = `${units[i - 1]}\u0000${units[i]}`;
    edges.set(k, (edges.get(k) || 0) + 1);
  }
}
const min = Number(opt("--min", "1"));
const kept = [...edges.entries()].filter(([, n]) => n >= min);
const seen = new Set();
for (const [k] of kept) for (const v of k.split("\u0000")) seen.add(v);
emit([...seen].map((v) => ({ id: v, label: `${v} (${nodes.get(v)})`, group: has("--verbs") ? phase(v) : v })),
     kept.map(([k, n]) => { const [a, b] = k.split("\u0000"); return [a, b, n]; }),
     [...seen].map((v) => [v, nodes.get(v)]));

function emit(ns, es, sizes = []) {
  const size = new Map(sizes);
  if (has("--mermaid")) {
    console.log("flowchart LR");
    for (const n of ns) console.log(`  ${id(n.id)}["${n.label}"]`);
    for (const [a, b, w] of es) console.log(`  ${id(a)} -->${w ? `|${w}|` : ""} ${id(b)}`);
    return;
  }
  console.log("digraph calls {");
  console.log('  rankdir=LR; node [shape=box, style=rounded, fontname="monospace"];');
  for (const n of ns) {
    const w = Math.max(1, Math.round((size.get(n.id) || 1) / 10));
    console.log(`  ${id(n.id)} [label="${n.label}" penwidth=${w}]${n.group ? `; // ${n.group}` : ""}`);
  }
  for (const [a, b, w] of es) console.log(`  ${id(a)} -> ${id(b)} [label="${w || 1}"];`);
  console.log("}");
}
function id(s) { return '"' + String(s).replace(/"/g, "'") + '"'; }
