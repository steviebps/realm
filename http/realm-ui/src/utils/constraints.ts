// Value imports name their .ts file, so node:test can run this file with Node's type stripping.
import type { Flag, Lineage, RuleType, Schema } from '../models/response';
import { get } from './provenance.ts';
import { plural } from './strings.ts';

// A port of the server's checks of flag constraints (pkg/schema.go), so the editor can explain a value before it is
// saved. The server stays the authority; sdk/conformance/constraints.json holds the cases both must agree on.

/** The keywords realm checks, as errors and help text name them. */
export const KEYWORDS =
  'type, enum, minimum, maximum, multipleOf, minLength, maxLength, properties, required, additionalProperties, and items';

const TYPES = ['object', 'array', 'string', 'number', 'integer', 'boolean', 'null'];

/** The keywords about values of one JSON type only; type and enum are about any. */
const TYPE_KEYWORDS: Record<string, string[]> = {
  number: ['minimum', 'maximum', 'multipleOf'],
  integer: ['minimum', 'maximum', 'multipleOf'],
  string: ['minLength', 'maxLength'],
  object: ['properties', 'required', 'additionalProperties'],
  array: ['items'],
};

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

const own = (o: Record<string, unknown>, key: string) => Object.prototype.hasOwnProperty.call(o, key);

/** Appends a member to a path: dotted when it is an identifier, quoted in brackets otherwise. */
const joinPath = (at: string, name: string) => {
  if (!/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name)) return `${at}[${JSON.stringify(name)}]`;
  return at ? `${at}.${name}` : name;
};

/** A value as JSON for a message, shortened when long. */
const describeValue = (v: unknown): string => {
  const s = JSON.stringify(v) ?? String(v);
  const chars = [...s];
  return chars.length <= 60 ? s : `${chars.slice(0, 59).join('')}…`;
};

/** A value a schema refuses: where in the value (empty for the value itself) and why. */
export type ValueError = { path: string; reason: string };

export const formatValueError = ({ path, reason }: ValueError) => (path ? `${path}: ${reason}` : reason);

const withArticle = (t: string) =>
  t === 'null' ? 'null' : ['object', 'array', 'integer'].includes(t) ? `an ${t}` : `a ${t}`;

const hasType = (v: unknown, t: string): boolean => {
  switch (t) {
    case 'null':
      return v === null;
    case 'boolean':
      return typeof v === 'boolean';
    case 'string':
      return typeof v === 'string';
    case 'number':
      return typeof v === 'number';
    case 'integer':
      return typeof v === 'number' && Number.isInteger(v);
    case 'object':
      return isObject(v);
    case 'array':
      return Array.isArray(v);
  }
  return false;
};

/** Whether two JSON values are equal: numbers by value, objects whatever their member order. */
export const jsonEqual = (a: unknown, b: unknown): boolean => {
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((x, i) => jsonEqual(x, b[i]));
  }
  if (isObject(a) || isObject(b)) {
    if (!isObject(a) || !isObject(b)) return false;
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every((k) => own(b, k) && jsonEqual(a[k], b[k]));
  }
  return a === b;
};

const isMultiple = (v: number, m: number) => {
  const q = v / m;
  if (!Number.isFinite(q)) return false;
  return Math.abs(q - Math.round(q)) <= 1e-9 * Math.max(1, Math.abs(q));
};

/** The first part of value that schema refuses, or undefined when it allows value. */
export const validate = (schema: Schema, value: unknown, at = ''): ValueError | undefined => {
  const fail = (reason: string): ValueError => ({ path: at, reason });
  if (schema.type !== undefined && !hasType(value, schema.type)) {
    return fail(`${describeValue(value)} is not ${withArticle(schema.type)}`);
  }
  if (schema.enum !== undefined && !schema.enum.some((e) => jsonEqual(e, value))) {
    return fail(
      `${describeValue(value)} is not one of the allowed values: ${schema.enum.map(describeValue).join(', ')}`
    );
  }
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) {
      return fail(`${value} is less than the minimum, ${schema.minimum}`);
    }
    if (schema.maximum !== undefined && value > schema.maximum) {
      return fail(`${value} is greater than the maximum, ${schema.maximum}`);
    }
    if (schema.multipleOf !== undefined && !isMultiple(value, schema.multipleOf)) {
      return fail(`${value} is not a multiple of ${schema.multipleOf}`);
    }
  } else if (typeof value === 'string') {
    // lengths count code points, as the server does, not UTF-16 units
    const n = [...value].length;
    if (schema.minLength !== undefined && n < schema.minLength) {
      return fail(
        `${describeValue(value)} is shorter than the minimum length, ${schema.minLength} (it has ${n} characters)`
      );
    }
    if (schema.maxLength !== undefined && n > schema.maxLength) {
      return fail(
        `${describeValue(value)} is longer than the maximum length, ${schema.maxLength} (it has ${n} characters)`
      );
    }
  } else if (isObject(value)) {
    for (const name of schema.required ?? []) {
      if (!own(value, name)) return fail(`required property ${JSON.stringify(name)} is missing`);
    }
    for (const name of Object.keys(value).sort()) {
      const property = get(schema.properties, name);
      if (property) {
        const err = validate(property, value[name], joinPath(at, name));
        if (err) return err;
      } else if (schema.additionalProperties === false) {
        return fail(`property ${JSON.stringify(name)} is not allowed`);
      }
    }
  } else if (Array.isArray(value) && schema.items) {
    for (let i = 0; i < value.length; i++) {
      const err = validate(schema.items, value[i], `${at}[${i}]`);
      if (err) return err;
    }
  }
  return undefined;
};

type Parsed = { schema: Schema; error?: undefined } | { schema?: undefined; error: string };

/**
 * Checks raw as the constraints of a flag of flagType, as the server does on write: only realm's keywords, of the
 * right form, consistent with each other and, for a number, string, or boolean flag, about values of that type.
 */
export const parseConstraints = (raw: unknown, flagType: RuleType): Parsed =>
  parseSchema(raw, '', flagType === 'custom' ? '' : flagType);

const parseSchema = (raw: unknown, at: string, implied: string): Parsed => {
  const fail = (msg: string): Parsed => ({ error: at ? `${at}: ${msg}` : msg });
  if (!isObject(raw)) return fail('a schema must be a JSON object');

  const s: Schema = {};
  const keys = Object.keys(raw).sort();
  for (const kw of keys) {
    const v = raw[kw];
    switch (kw) {
      case 'type':
        if (typeof v !== 'string' || !TYPES.includes(v)) {
          return fail(`type must be one of ${TYPES.map((t) => JSON.stringify(t)).join(', ')}`);
        }
        s.type = v as Schema['type'];
        break;
      case 'enum':
        if (!Array.isArray(v) || v.length === 0) return fail('enum must be an array of at least one value');
        s.enum = v;
        break;
      case 'minimum':
      case 'maximum':
      case 'multipleOf':
        if (typeof v !== 'number') return fail(`${kw} must be a number`);
        if (kw === 'multipleOf' && v <= 0) return fail('multipleOf must be greater than 0');
        s[kw] = v;
        break;
      case 'minLength':
      case 'maxLength':
        if (typeof v !== 'number' || !Number.isInteger(v) || v < 0 || v > 2147483647) {
          return fail(`${kw} must be a non-negative integer`);
        }
        s[kw] = v;
        break;
      case 'properties': {
        if (!isObject(v)) return fail('properties must be an object of schemas');
        const properties: Record<string, Schema> = {};
        for (const name of Object.keys(v).sort()) {
          const p = parseSchema(v[name], joinPath(joinPath(at, 'properties'), name), '');
          if (p.error !== undefined) return p;
          properties[name] = p.schema;
        }
        s.properties = properties;
        break;
      }
      case 'required':
        if (!Array.isArray(v) || !v.every((n) => typeof n === 'string')) {
          return fail('required must be an array of property names');
        }
        for (let i = 0; i < v.length; i++) {
          if (v.indexOf(v[i]) !== i) return fail(`required names ${JSON.stringify(v[i])} twice`);
        }
        s.required = v;
        break;
      case 'additionalProperties':
        if (typeof v !== 'boolean') {
          return fail('additionalProperties must be true or false (realm does not check a schema there)');
        }
        s.additionalProperties = v;
        break;
      case 'items': {
        if (Array.isArray(v)) return fail('items must be one schema (realm does not check an array of them)');
        const items = parseSchema(v, joinPath(at, 'items'), '');
        if (items.error !== undefined) return items;
        s.items = items.schema;
        break;
      }
      default:
        return fail(`unknown keyword ${JSON.stringify(kw)}: realm checks only ${KEYWORDS}`);
    }
  }

  if (implied && s.type !== undefined && !(s.type === implied || (implied === 'number' && s.type === 'integer'))) {
    return fail(`type ${JSON.stringify(s.type)} does not match the flag's type ${JSON.stringify(implied)}`);
  }
  const effective = s.type ?? implied;
  if (effective) {
    const applies = TYPE_KEYWORDS[effective] ?? [];
    for (const kw of keys) {
      if (kw === 'type' || kw === 'enum' || applies.includes(kw)) continue;
      return fail(
        s.type !== undefined
          ? `${kw} does not apply to type ${JSON.stringify(s.type)}`
          : `${kw} does not apply to a ${implied} flag`
      );
    }
  }
  if (s.minimum !== undefined && s.maximum !== undefined && s.minimum > s.maximum) {
    return fail(`minimum ${s.minimum} is greater than maximum ${s.maximum}`);
  }
  if (s.minLength !== undefined && s.maxLength !== undefined && s.minLength > s.maxLength) {
    return fail(`minLength ${s.minLength} is greater than maxLength ${s.maxLength}`);
  }
  if (s.additionalProperties === false) {
    for (const name of s.required ?? []) {
      if (!s.properties || !own(s.properties, name)) {
        return fail(
          `required property ${JSON.stringify(name)} is not in properties, and additionalProperties is false`
        );
      }
    }
  }
  if (s.enum) {
    const rest: Schema = { ...s, enum: undefined, type: (effective || undefined) as Schema['type'] };
    for (const e of s.enum) {
      const err = validate(rest, e);
      if (err) return fail(`enum value ${describeValue(e)} is never allowed: ${formatValueError(err)}`);
    }
  }
  return { schema: s };
};

/** Constraints on a flag: the schema, and the path of the parent that sets them (none for the flag's own). */
export type Bound = { path?: string; schema: Schema };

/**
 * The constraints that bind a definition of name in the chamber whose lineage this is: those of each parent that
 * defines it with constraints, farthest first, as the server checks them. Parents that cannot be parsed, and
 * constraints this version cannot parse, are left out; the server reports them on save.
 */
export const parentBounds = (lineage: Lineage | null, name: string, type: RuleType): Bound[] => {
  if (!lineage) return [];
  return lineage.layers
    .slice(1)
    .reverse()
    .flatMap((layer) => {
      const flag = layer.status === 'ok' ? get(layer.rules, name) : undefined;
      if (!flag?.constraints || flag.type !== type) return [];
      const parsed = parseConstraints(flag.constraints, type);
      return parsed.schema ? [{ path: layer.path, schema: parsed.schema }] : [];
    });
};

/** Why the first of bounds that refuses value does, naming the parent that sets it; undefined when all allow it. */
export const refusal = (bounds: Bound[], value: unknown): string | undefined => {
  for (const { path, schema } of bounds) {
    const err = validate(schema, value);
    if (err) return `${formatValueError(err)}${path ? ` (constraints of ${path})` : ''}`;
  }
  return undefined;
};

/** Each of a flag's values, paused or not, that bounds refuse, named for the editor. */
export const constraintProblems = (flag: Flag, bounds: Bound[]): string[] => {
  const rules = [...(flag.targeting ?? []), ...(flag.pausedTargeting ?? [])];
  return [['Default', flag.value] as const, ...rules.map((r, i) => [`Rule ${i + 1}`, r.value] as const)].flatMap(
    ([where, value]) => {
      const why = refusal(bounds, value);
      return why ? [`${where}: ${why}.`] : [];
    }
  );
};

/** The bounds of the value of a number flag that every schema allows, for an input's min, max, and step. */
export const numberRange = (schemas: Schema[]) => {
  const mins = schemas.flatMap((s) => (s.minimum === undefined ? [] : [s.minimum]));
  const maxes = schemas.flatMap((s) => (s.maximum === undefined ? [] : [s.maximum]));
  const step =
    schemas.find((s) => s.multipleOf !== undefined)?.multipleOf ??
    (schemas.some((s) => s.type === 'integer') ? 1 : undefined);
  return {
    min: mins.length ? Math.max(...mins) : undefined,
    max: maxes.length ? Math.min(...maxes) : undefined,
    step,
  };
};

/** The values every schema with an enum allows, in the first one's order; undefined when none has one. */
export const allowedValues = (schemas: Schema[]): unknown[] | undefined => {
  const enums = schemas.flatMap((s) => (s.enum ? [s.enum] : []));
  if (!enums.length) return undefined;
  return enums[0].filter((v) => schemas.every((s) => validate(s, v) === undefined));
};

/** A plain-language summary of the values a schema allows, for a flag of the given type. */
export const describeConstraints = (schema: Schema, type: RuleType): string => {
  if (type === 'custom') return `matches ${JSON.stringify(schema)}`;
  const parts: string[] = [];
  if (schema.enum) parts.push(`one of ${schema.enum.map(describeValue).join(', ')}`);
  const { minimum: min, maximum: max } = schema;
  if (min !== undefined && max !== undefined) parts.push(`from ${min} to ${max}`);
  else if (min !== undefined) parts.push(`at least ${min}`);
  else if (max !== undefined) parts.push(`at most ${max}`);
  if (schema.type === 'integer' || schema.multipleOf === 1) parts.push('whole numbers');
  else if (schema.multipleOf !== undefined) parts.push(`multiples of ${schema.multipleOf}`);
  const { minLength, maxLength } = schema;
  if (minLength !== undefined && maxLength !== undefined)
    parts.push(`${minLength} to ${plural(maxLength, 'character')}`);
  else if (maxLength !== undefined) parts.push(`up to ${plural(maxLength, 'character')}`);
  else if (minLength !== undefined) parts.push(`at least ${plural(minLength, 'character')}`);
  return parts.length ? parts.join(', ') : 'any value';
};

/** A flag's constraints in plain language, or as JSON when this version cannot parse them. */
export const summarizeConstraints = (constraints: Schema, type: RuleType): string => {
  const parsed = parseConstraints(constraints, type);
  return parsed.schema ? describeConstraints(parsed.schema, type) : JSON.stringify(constraints);
};
