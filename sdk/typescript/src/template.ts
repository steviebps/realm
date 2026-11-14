// Path templates, which pick a chamber of a subtree for each evaluation
// context (see docs/design/subtree.md). The Go SDK resolves them the same
// way; sdk/conformance/subtree.json holds the cases both run.

import { attribute } from './evaluate.js';
import type { EvaluationContext } from './types.js';

/** A segment of a path template after its root: literal text, or a placeholder for an attribute. */
type Segment = { literal: string } | { attribute: string };

/** A parsed path template, such as `/prod/tenants/{tenantId}/{locationId}/`. */
export interface PathTemplate {
  /**
   * The literal prefix up to the first placeholder, such as `/prod/tenants/`:
   * the subtree to read. Like every chamber path, it starts and ends with `/`.
   */
  readonly root: string;
  // The segments from the first placeholder on.
  readonly segments: readonly Segment[];
}

const placeholder = /^\{([A-Za-z0-9_.-]+)\}$/;

/**
 * Parses template, adding a missing trailing `/`. It throws a TypeError for
 * an invalid template: one that does not start with `/`, or that has an
 * empty segment (`//`), a segment that is `.` or `..`, a brace outside a
 * placeholder (`x{a}`), or a placeholder whose name is empty or uses other
 * characters than `A-Z a-z 0-9 _ - .`.
 */
export function parseTemplate(template: string): PathTemplate {
  const invalid = (why: string) => new TypeError(`invalid path template ${JSON.stringify(template)}: ${why}`);
  if (typeof template !== 'string' || !template.startsWith('/')) throw invalid('it must start with "/"');
  const t = template.endsWith('/') ? template : template + '/';
  const parts = t === '/' ? [] : t.slice(1, -1).split('/');
  let root = '/';
  const segments: Segment[] = [];
  for (const part of parts) {
    let segment: Segment;
    const m = placeholder.exec(part);
    if (m) {
      segment = { attribute: m[1]! };
    } else if (part === '') {
      throw invalid('it has an empty segment');
    } else if (part.includes('{') || part.includes('}')) {
      throw invalid(`${JSON.stringify(part)} is neither literal text nor a placeholder such as "{tenantId}"`);
    } else if (part === '.' || part === '..') {
      throw invalid(`it has a ${JSON.stringify(part)} segment`);
    } else {
      segment = { literal: part };
    }
    if (segments.length === 0 && 'literal' in segment) root += `${segment.literal}/`;
    else segments.push(segment);
  }
  return { root, segments };
}

/**
 * Returns the path of the chamber template picks for context: the deepest
 * candidate that has (reports holding a view), or undefined when none does.
 * The candidates are the root and, segment by segment, the path built so
 * far; a placeholder is filled with the attribute of its name, looked up as
 * targeting clauses look it up (context over defaults; `key` is the
 * context's key). Resolution stops at a placeholder whose value is missing
 * or empty, contains `/` or `..`, or is `.`.
 */
export function resolvePath(
  template: PathTemplate,
  has: (path: string) => boolean,
  context: EvaluationContext,
  defaults?: Record<string, string>,
): string | undefined {
  let path = template.root;
  let found = has(path) ? path : undefined;
  for (const segment of template.segments) {
    let part: string;
    if ('literal' in segment) {
      part = segment.literal;
    } else {
      const value = attribute(context, defaults, segment.attribute);
      if (!value || value.includes('/') || value === '.' || value.includes('..')) break;
      part = value;
    }
    path += `${part}/`;
    if (has(path)) found = path;
  }
  return found;
}
