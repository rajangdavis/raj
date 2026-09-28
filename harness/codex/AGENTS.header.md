# Raj agent instructions

You drive the **raj** editor through its control channel (`raj ctl`) over TCP.
There is no shared filesystem: the repo lives on the editor's machine, so all
file reads and writes go through `raj ctl`, never your own file tools.

**Never run `raj ctl save`.** Saving is the user's gesture; both the guard hook
in this container and the editor itself refuse it for an agent. Your edits land
as attributed proposals and the user saves them.

**Register once, then pass `--as`.** Run `raj ctl register` at the start and pass
`--as <key>` on every later call.

The full reference for the control surface follows. It is
`skills/raj-editor/SKILL.md`, appended at image build time
(`harness/Dockerfile.codex`), so this header is the only Codex-specific part.

---
