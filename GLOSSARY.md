# realm

realm is a feature-flag and configuration platform: it holds flag definitions in a hierarchy of chambers and serves each application the value a flag should take for a given context.

## Language

### Flags and values

**Flag**:
A named, typed value an application reads, with a default value and ordered targeting rules.
_Avoid_: Switch, toggle, feature, setting, rule

**Flag key**:
The name a flag is known by within a chamber.
_Avoid_: Flag name, flag ID

**Flag type**:
The kind of value a flag holds: string, number, boolean, or custom (structured JSON).

**Default value**:
The value a flag serves when no targeting rule applies.
_Avoid_: Fallback, base value

**Caller's default**:
The value an application passes when reading a flag, returned instead when the flag is missing or cannot be served.
_Avoid_: Default value (that is the flag's)

**Definition**:
One chamber's version of a flag: its default value, targeting, constraints, and labels.

**Lifecycle**:
Whether a flag is meant to be removed once it has served its purpose (temporary) or to stay (permanent).

**Owner**:
The person or team accountable for a flag.

**Constraints**:
The limits every value of a flag must satisfy, which bind its definitions in every chamber below; a lower chamber may tighten them but never loosen them.
_Avoid_: Validation, schema (when talking to users)

### Targeting

**Targeting**:
A flag's ordered list of targeting rules; the first that applies decides the served value.

**Targeting rule**:
A set of clauses, a value to serve when they all match, and optionally a rollout and a schedule. "Rule" on its own always means a targeting rule.
_Avoid_: Override, variation

**Clause**:
One condition of a targeting rule: an attribute, an operator, and the values to compare against.
_Avoid_: Condition, predicate

**Operator**:
How a clause compares an attribute to its values: in, not in, starts with, at least version, at most version.

**Rollout**:
A limit on a targeting rule to a stable percentage of the contexts that match it.
_Avoid_: Percentage rollout, canary, experiment

**Bucket by**:
The attribute whose value decides which side of a rollout a context lands on; the context key unless chosen otherwise.

**Schedule**:
A time window, open at either end, during which a targeting rule applies.

**Paused targeting**:
A flag's targeting rules while targeting is turned off: kept, but never applied, so the flag serves its default value.
_Avoid_: Disabled flag, kill switch

### Context

**Evaluation context**:
The facts about one request that targeting is matched against: a key and a set of attributes.
_Avoid_: User, subject, identity

**Context key**:
The attribute that identifies the thing being evaluated for, used by default to bucket rollouts.

**Attribute**:
One named fact in an evaluation context, such as tenant, location, environment, service, or version.
_Avoid_: Trait, property, dimension

**Environment**:
An attribute naming the deployment a request comes from, such as prod or staging. A chamber is never an environment, even one named after one.
_Avoid_: Stage, tier, zone

**Catalog**:
The human-readable labels for an attribute's known values.

**Evaluation**:
Working out the value a flag serves for an evaluation context.
_Avoid_: Resolution, lookup

### Chambers

**Chamber**:
A node in the hierarchy, named by its path, holding definitions of flags.
_Avoid_: Namespace, environment, folder, project, zone

**Path**:
The slash-separated name of a chamber, which also places it in the hierarchy.

**Inheritance**:
A chamber serving the definitions of its ancestors for every flag it does not define itself, and for every flag an ancestor seals.

**Override**:
A chamber's own definition of a flag that an ancestor also defines, which replaces the inherited one unless an ancestor seals the flag.

**Seal**:
A mark on a definition that stops chambers below it from overriding the flag.
_Avoid_: Lock, freeze, pin

**Hidden definition**:
A definition in a chamber below a seal, written before the seal; never served.

**Broken chamber**:
A stored chamber that cannot be parsed. Every read at or below it fails, and nothing below it can be written, until it is fixed.
_Avoid_: Corrupt chamber, invalid chamber

**Lineage**:
A path's own chamber (if one is stored there) and each of its ancestors, nearest first: everything its inherited view and ancestry are built from.
_Avoid_: Parent chain, hierarchy (that is the whole tree)

**Inherited view**:
What a read of a chamber serves: its own definitions plus those it inherits, with seals applied.
_Avoid_: Merged chamber, effective config

**Ancestry**:
What a path's ancestors say about the flags a chamber there may define: which they seal, and which they constrain.

**Subtree**:
A chamber together with every chamber below it.

**Path template**:
A chamber path with placeholders filled from the evaluation context, which picks the deepest existing chamber for each request.

**Revision**:
The version of the stored data a read, evaluation, or change notice reflects.
_Avoid_: Version (that is an attribute), generation

### Delivery

**SDK**:
A library an application embeds to read flags, either from a local snapshot kept current by the server or by asking the server to evaluate them.
_Avoid_: Agent

**Snapshot**:
The SDK's local copy of a chamber (or subtree) that evaluations read from.
_Avoid_: Cache

**Polling**:
An SDK keeping its snapshot current by re-reading on an interval.

**Streaming**:
An SDK keeping its snapshot current by holding a connection the server pushes changes down.
_Avoid_: Subscription, push

**Server-side evaluation**:
The server evaluating flags for a context and returning only the served values, for callers that must not see definitions, such as browsers.

### Access

**Principal**:
Whoever is making a request: an API key, the admin key, a signed-in user, or anyone when access control is off.
_Avoid_: Caller, actor, identity

**API key**:
A secret, scoped to a path, that lets software evaluate, read, or write flags.
_Avoid_: Token (that is how it travels), client key

**Evaluate key**:
An API key that can only request server-side evaluation, safe to ship to browsers.
_Avoid_: Public key, client-side key

**Admin key**:
The one key declared in the server's configuration, with full access, for installs without sign-in.
_Avoid_: Root key, master key

**Role**:
What a signed-in user may do: viewer, editor, or admin.

**Grant**:
A role given to users, either on every chamber or only beneath certain paths.
_Avoid_: Permission, policy, binding

### Writes

**Chamber write**:
Adding, replacing, patching, or deleting a chamber (with or without the chambers below it), checked against its lineage and the chambers below, recorded in its history, and announced to streams.
_Avoid_: Save, update (for the concept)

**Precondition**:
A write's condition on the chamber as currently stored, so that an editor does not overwrite a change it has not seen.

### History

**History**:
The record of changes made to a chamber.
_Avoid_: Audit log, changelog
