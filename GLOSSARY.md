# Realm

A feature-flag platform: flags live in a tree of chambers that inherit from
the chambers above them.

## Chambers and inheritance

**Chamber**:
A namespace of flags at a path such as `/prod/eu/`, inheriting every flag it lacks from the chambers above it.
_Avoid_: environment, namespace, folder

**Parent chamber**:
A chamber above another on its path; the nearest is its parent, the farthest is the root chamber `/`.
_Avoid_: ancestor (in user-facing text), upper chamber

**Broken chamber**:
A stored chamber that cannot be parsed. Every read at or below it fails, and nothing below it can be written, until it is fixed.
_Avoid_: corrupt chamber, invalid chamber

**Layer**:
One chamber of a lineage as stored: its flags, or nothing when no chamber is stored at that path, or the reason it is broken.

**Lineage**:
A chamber's own layer plus the layer of each parent chamber, nearest first: everything its inherited view, ancestry, and resolution are built from. A path has a lineage whether or not a chamber is stored there.
_Avoid_: ancestors, parent chain, hierarchy

**Inherited view**:
What a read of a path serves: the chamber's flags plus each flag it lacks from the nearest parent chamber that defines it, with seals applied.
_Avoid_: merged chamber, effective config

**Ancestry**:
What the parent chambers of a path say about the flags a chamber there may define: which they seal, and which they constrain.

**Resolution**:
Where each flag of an inherited view comes from, which parent chamber seals it, and which definitions a seal hides.

## Rules on flags

**Seal**:
A parent chamber's mark on a flag that serves its definition to every chamber below it and keeps them from defining their own.
_Avoid_: lock, pin

**Hidden definition**:
A flag definition in a chamber below a seal, stored before the seal; never served.

**Constraints**:
The values a flag may take, which bind its definitions in the chambers below too: they may tighten them, never loosen them.
_Avoid_: schema (for the concept), validation

## Writes

**Chamber write**:
A PUT, PATCH, or DELETE of a chamber (a recursive delete included), checked against its lineage and the chambers below, recorded in the chamber's history, and announced to watchers.
_Avoid_: save, update (for the concept)

**Precondition**:
A write's condition on the ETag of the chamber as stored, so that an editor does not overwrite a change it has not seen.
