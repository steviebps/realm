// The chamber edit session: what the user proposes to change in a chamber, saving it against the chamber they
// loaded, and re-applying it when someone else changed the chamber first. It holds no React state and does no I/O of
// its own, so ChamberView only lays it out, and it is tested without rendering.

import type { LoadedChamber } from '../api';
import type { Chamber, Flag } from '../models/response';
import { rulesOf, targetingPaused, withTargetingPaused } from './flags.ts';
import { get, type Provenance, type Served } from './provenance.ts';

/** A change the user is reviewing before it is saved. */
export type Proposal = {
  title: string;
  /** Makes the change to a copy of a chamber: the one loaded, or a newer one after a conflict. */
  apply: (chamber: Chamber) => Chamber;
  /** The chamber the change is made to, and saved against (its ETag). */
  base: LoadedChamber;
  /** The chamber as it would be saved. */
  after: Chamber;
  saving: boolean;
  /** Set once the change was re-applied to a newer chamber, after a conflict. */
  rebased?: boolean;
  /**
   * Flags someone else changed since the user loaded the chamber that this change replaces or deletes whole:
   * saving it again overwrites what they did to those flags.
   */
  overwrites?: string[];
  error?: string;
};

/** Proposes the change apply makes to the chamber loaded in base, titled title. */
export const propose = (base: LoadedChamber, title: string, apply: Proposal['apply']): Proposal => ({
  title,
  apply,
  base,
  after: apply(structuredClone(base.own ?? { rules: {} })),
  saving: false,
});

/** What confirm needs: saving and loading the chamber, and telling a conflict from other failures. */
export type SessionIO = {
  save: (chamber: Chamber, etag: string | null) => Promise<void>;
  load: () => Promise<LoadedChamber>;
  /** Whether a save failed because the chamber changed since it was loaded (its ETag no longer matches). */
  isConflict: (err: unknown) => boolean;
  messageOf: (err: unknown) => string;
};

/** What confirming a proposal led to. */
export type Confirmed =
  | { saved: true }
  /** Not saved: the proposal to show next, with why, or re-applied to the newer chamber a conflict loaded. */
  | { saved: false; proposal: Proposal; reloaded?: LoadedChamber };

/**
 * Saves proposal against the chamber it was made to. When someone else changed the chamber first, it loads the
 * latest and re-applies the change to it, for the user to review again, naming the flags whose changes by others
 * saving it would overwrite. When that load fails, it keeps the old base, so confirming again retries both.
 */
export const confirm = async (proposal: Proposal, io: SessionIO): Promise<Confirmed> => {
  try {
    await io.save(proposal.after, proposal.base.etag);
    return { saved: true };
  } catch (err) {
    if (!io.isConflict(err)) {
      return { saved: false, proposal: { ...proposal, saving: false, error: io.messageOf(err) } };
    }
  }
  let fresh: LoadedChamber;
  try {
    fresh = await io.load();
  } catch (err) {
    return {
      saved: false,
      proposal: {
        ...proposal,
        saving: false,
        error: `Someone else changed this chamber, and loading their change failed: ${io.messageOf(err)}`,
      },
    };
  }
  const after = proposal.apply(structuredClone(fresh.own ?? { rules: {} }));
  // Their change to a flag survives when re-applying this one builds on it (pausing targeting, say); it is lost
  // when the flag comes out just as this change made it before, ignoring theirs (replacing or deleting the flag).
  const theirs = changedFlags(proposal.base.own, fresh.own);
  const mine = new Set(changedFlags(proposal.base.own, proposal.after));
  const overwrites = theirs.filter(
    (name) =>
      mine.has(name) &&
      sameDefinition(after.rules[name], proposal.after.rules[name]) &&
      !sameDefinition(after.rules[name], fresh.own?.rules[name])
  );
  return {
    saved: false,
    reloaded: fresh,
    proposal: {
      ...proposal,
      base: fresh,
      after,
      saving: false,
      rebased: true,
      overwrites: overwrites.length > 0 ? overwrites : undefined,
      error: undefined,
    },
  };
};

const sameDefinition = (a: Flag | undefined, b: Flag | undefined) => JSON.stringify(a) === JSON.stringify(b);

/** The names of the flags whose definitions differ between a and b, sorted. */
export const changedFlags = (a: Chamber | null, b: Chamber | null): string[] => {
  const ra = a?.rules ?? {};
  const rb = b?.rules ?? {};
  return [...new Set([...Object.keys(ra), ...Object.keys(rb)])]
    .filter((name) => !sameDefinition(ra[name], rb[name]))
    .sort();
};

/** Whether a flag named name may be added to own, and what the user should be told about it. */
export type AddCheck = {
  /** The name as typed, trimmed. */
  name: string;
  /** A parent's definition the new one would override, when there is one. */
  inherited?: Served;
  /** The farthest parent that seals the flag: this chamber cannot define it. */
  sealedBy?: string;
  /** Whether the chamber already defines it. */
  exists: boolean;
  allowed: boolean;
};

/** Checks adding the flag typed as typed to own, given where its flags come from (prov). */
export const checkAdd = (own: Chamber, prov: Provenance | null, typed: string): AddCheck => {
  const name = typed.trim();
  const exists = name in own.rules;
  const inherited = exists ? undefined : get(prov?.inherited, name);
  const sealedBy = exists ? undefined : get(prov?.sealedBy, name);
  return { name, inherited, sealedBy, exists, allowed: name !== '' && !exists && sealedBy === undefined };
};

/**
 * The definition an override of an inherited flag starts from: its type, default value, and targeting. Its
 * constraints, which the server holds every definition below to anyway, and its description and owner, which the
 * override shows until it sets its own, are left with the parent, so they keep following it.
 */
export const overrideSeed = (flag: Flag): Flag => {
  const seed: Flag = { type: flag.type, value: structuredClone(flag.value) };
  if (flag.targeting) seed.targeting = structuredClone(flag.targeting);
  if (flag.pausedTargeting) seed.pausedTargeting = structuredClone(flag.pausedTargeting);
  return seed;
};

/** What a row of the chamber's own flags lets the user do. */
export type RowActions = {
  /** The parent that seals the flag, when one does: this definition is not served and cannot change. */
  hiddenBy?: string;
  editable: boolean;
  /** Whether targeting can be paused or resumed: the flag has rules and is not hidden. */
  pausable: boolean;
  paused: boolean;
};

/** What the row of own's flag name lets the user do. */
export const rowActions = (own: Chamber, prov: Provenance | null, name: string): RowActions => {
  const flag = own.rules[name];
  const hiddenBy = get(prov?.sealedBy, name);
  return {
    hiddenBy,
    editable: hiddenBy === undefined,
    pausable: hiddenBy === undefined && rulesOf(flag).length > 0,
    paused: targetingPaused(flag),
  };
};

/** Changes that are re-applied to the latest chamber after a conflict, which may have changed the flag. */
export const changes = {
  put: (name: string, flag: Flag) => (c: Chamber) => {
    c.rules[name] = flag;
    return c;
  },
  remove: (name: string) => (c: Chamber) => {
    delete c.rules[name];
    return c;
  },
  /** Pauses (or resumes) targeting, if the flag is still there. */
  pause: (name: string, paused: boolean) => (c: Chamber) => {
    if (c.rules[name]) c.rules[name] = withTargetingPaused(c.rules[name], paused);
    return c;
  },
  replace: (chamber: Chamber) => () => structuredClone(chamber),
};
