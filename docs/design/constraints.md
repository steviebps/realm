# Design: flag descriptions and value constraints

Status: **implemented** · Issue #138

## Goals

- Say what a flag is for, who owns it, and whether it is meant to go away, so
  that people who did not create it can edit it safely.
- Refuse values a flag cannot sensibly take (`7200` hours where `1`–`72` is
  meant) on the server, once, instead of in every client.
- Keep a guardrail set on a parent chamber in force below it: a key that may
  write only `/prod/tenants/acme/` must not be able to widen the bounds that
  `/prod/` sets.

## Non-goals

Enforcing ownership or lifecycle (they are labels) · a full JSON Schema
validator · checking values on reads · labels for enum values · a permission
to change constraints separate from writing the chamber.

## Data model

A flag gains four optional fields, written after its default value:

```json
"orders.holdHours": {
  "type": "number",
  "value": 24,
  "description": "How long an online order is held before its stock is released",
  "owner": "ordering",
  "lifecycle": "permanent",
  "constraints": { "minimum": 1, "maximum": 72, "multipleOf": 1 }
}
```

- `description`, `owner`: free text.
- `lifecycle`: `temporary` (a release toggle or experiment, meant to be
  removed) or `permanent` (a setting or kill switch). Nothing enforces it; the
  UI shows it and filters by it.
- `constraints`: a schema the flag's values must satisfy (below).

### Constraints: a JSON Schema subset

`constraints` is a JSON Schema, restricted to the keywords realm checks:

| Keyword | Applies to | Meaning |
|---|---|---|
| `type` | any | `object`, `array`, `string`, `number`, `integer`, `boolean`, or `null` |
| `enum` | any | the only values allowed, compared as JSON (`1` = `1.0`; member order ignored) |
| `minimum`, `maximum` | numbers | inclusive bounds |
| `multipleOf` | numbers | a positive number; `1` allows only whole numbers (checked within a billionth, so `0.3` is a multiple of `0.1`) |
| `minLength`, `maxLength` | strings | inclusive bounds, in Unicode code points |
| `properties` | objects | the schemas of members, by name |
| `required` | objects | members every object must have |
| `additionalProperties` | objects | `false` refuses members `properties` does not name (a schema here is not supported) |
| `items` | arrays | one schema for every element (a list is not supported) |

One vocabulary covers every flag type: a number flag's `{"minimum": 1}` and
a custom flag's `{"type": "object", "properties": {…}}` are the same kind of
object, and moving to full JSON Schema later changes no stored constraint.

Writes are strict, so the subset refuses what it does not check:

- Any other keyword (`pattern`, `format`, `exclusiveMaximum`, `$ref`, …) is
  refused by name, so no one believes it is enforced. Keywords match exactly,
  as in JSON Schema (`Minimum` is unknown).
- A scalar flag's constraints must be about its type: a number flag may use
  `type` (`number` or `integer`), `enum`, `minimum`, `maximum`, and
  `multipleOf`; a string flag `type`, `enum`, `minLength`, and `maxLength`; a
  boolean flag `type` and `enum`.
- Within a schema, a `type` that rules out a keyword (`{"type": "string",
  "minimum": 1}`), bounds that cross, a required member that
  `additionalProperties: false` forbids, and an enum value the rest of the
  schema refuses are all refused: each is surely a mistake.
- As in JSON Schema, a keyword about one type says nothing about values of
  another when no `type` rules it out: `{"minimum": 1}` in a custom flag's
  schema allows `"x"`.

`pkg/schema.go` implements the subset (`Schema`, `ParseSchema`,
`Schema.Validate`); `http/realm-ui/src/utils/constraints.ts` ports it so the
editor can explain a value before saving. `sdk/conformance/constraints.json`
holds the cases both run.

## Writes are strict, reads are lenient

`realm.ParseChamber` (every write, and `realm client put` before it sends)
refuses a flag with an unknown lifecycle, constraints it cannot parse, or a
default value or targeting rule value, **paused or not**, that its
constraints refuse. Paused rules are checked because turning targeting back
on should never be the moment a bad value is discovered.

`Flag.UnmarshalJSON`, which SDKs and the server's reads use, checks none of
this. A stored value outside its constraints (a hand-edited file, or a race
below) is still served, and `constraints` is kept as raw JSON
(`Flag.Constraints`), so a newer realm's keywords survive an older one's
read-modify-write byte for byte. `Flag.ParseConstraints` decodes it on
demand.

## Parents' constraints bind the chambers below

A child chamber replaces a parent's flag whole, so constraints on the
parent's definition alone would bind nothing below it: an override could
drop them, widen them, or change the flag's type. Instead, the server holds
every definition to the constraints of the flag in **every** chamber above
it that defines it with constraints, as well as its own:

- A definition below may **tighten** (add its own constraints, which bind
  further down) but never **loosen**: it must satisfy each parent's
  constraints separately, so the effective bound is their intersection.
- It must have the **type** of each constraining parent's definition.
- As with seals, only definitions a write **adds or changes** are checked;
  one stored before the constraints may stay as it is.

Path-scoped write keys make this an access control as well as a guardrail:
the key for `/prod/tenants/acme/` cannot widen `/prod/`'s bounds. (Signed-in
users' roles are global, so any editor can change any chamber's constraints;
the review diff shows it.)

The server reads the parents with one pass that also yields their seals
(`storage.Ancestry`), before the write, as it reads seals.

### Adding or tightening constraints

A write that adds a flag's constraints, changes them, or changes the type of
a constrained flag must not leave a chamber below it serving a value the
flag no longer allows. So the server lists the chambers below the path
(`storage.ListTree`, one range read on Postgres, etcd, and boltdb), reads
them (`ScanLayers`, or one read each elsewhere), and **refuses** the write
when any serves a definition of the flag with another type or a refused
value, naming up to ten of them:

```
flag "orders.holdHours": chambers below define it outside its new constraints:
/prod/tenants/acme/ (default value: 48 is greater than the maximum, 36)
```

Definitions a seal hides are skipped: they are not served. So are chambers
that cannot be parsed, which fail every read until fixed.

Only writes that change constraints pay for the scan. It happens before the
write, outside its transaction (`storage.Update` may run its function more
than once, and file backends hold a lock while it runs), so:

- within the write, the server recomputes which flags' constraints change
  against the chamber as stored, and answers `409` ("try again") if that is
  not the set it scanned for;
- a definition written below at the same moment can escape the check, as one
  can escape a seal; it is checked against the constraints when it next
  changes.

Without inheritance no chamber is above another: a chamber's constraints bind
only itself, and nothing is scanned.

## UI

- The flag editor has description, owner, and lifecycle fields, and an
  *Allowed values* section: minimum, maximum, and multiple-of for numbers;
  allowed values and maximum length for strings; a schema for custom flags.
  Keywords the fields do not cover are kept and listed.
- Parents' constraints are listed ("Set by `/`: from 1 to 72, whole
  numbers"), and value inputs are bounded by all of them: a number input's
  `min`, `max`, and `step`, a dropdown for an enum. A refused value is
  explained under its input and blocks *Review changes*.
- Flag rows show the description and lifecycle; the details panel shows
  owner and allowed values. An override that says nothing about itself shows
  what the definition it overrides says, marked "(from `/`)".
- The flags table filters by lifecycle, and the review diff lists changes to
  each field.

## SDKs

The Go `Flag` exposes `Description`, `Owner`, `Lifecycle`, and
`Constraints` (with `ParseConstraints` and `CheckValues`); the TypeScript
SDK's `Flag` type has `description`, `owner`, `lifecycle`, and
`constraints` (`Schema`). SDKs never check constraints: values are checked
once, when written. Server-side evaluation (`POST /v1/evaluate/…`) returns
only types and values, so browsers never receive metadata.
