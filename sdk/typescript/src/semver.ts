// A port of Go's golang.org/x/mod/semver, which the realm server and Go SDK
// use, so versions compare identically everywhere. Versions carry a "v"
// prefix; "v1" and "v1.2" are shorthand for "v1.0.0" and "v1.2.0"; build
// metadata is ignored.

export interface Parsed {
  major: string;
  minor: string;
  patch: string;
  prerelease: string;
}

/** Prefixes v with "v" when missing, so "1.2.3" and "v1.2.3" are both accepted. */
export function canonical(v: string): string {
  return v === '' || v[0] === 'v' ? v : 'v' + v;
}

/**
 * Compares two parsed versions: -1 if v < w, 0 if equal, +1 if v > w. Build
 * metadata is ignored.
 */
export function compareParsed(v: Parsed, w: Parsed): number {
  return (
    compareInt(v.major, w.major) ||
    compareInt(v.minor, w.minor) ||
    compareInt(v.patch, w.patch) ||
    comparePrerelease(v.prerelease, w.prerelease)
  );
}

/** Parses v (with its "v" prefix), or returns undefined when it is invalid. */
export function parse(v: string): Parsed | undefined {
  if (v === '' || v[0] !== 'v') return undefined;
  const p: Parsed = { major: '', minor: '0', patch: '0', prerelease: '' };
  let rest: string;
  let int = parseNumber(v.slice(1));
  if (!int) return undefined;
  [p.major, rest] = int;
  if (rest === '') return p;
  if (rest[0] !== '.') return undefined;
  int = parseNumber(rest.slice(1));
  if (!int) return undefined;
  [p.minor, rest] = int;
  if (rest === '') return p;
  if (rest[0] !== '.') return undefined;
  int = parseNumber(rest.slice(1));
  if (!int) return undefined;
  [p.patch, rest] = int;
  if (rest[0] === '-') {
    const pre = parsePrerelease(rest);
    if (!pre) return undefined;
    [p.prerelease, rest] = pre;
  }
  if (rest[0] === '+') {
    const build = parseBuild(rest);
    if (!build) return undefined;
    rest = build[1];
  }
  return rest === '' ? p : undefined;
}

function parseNumber(v: string): [string, string] | undefined {
  if (v === '' || !isDigit(v[0]!)) return undefined;
  let i = 1;
  while (i < v.length && isDigit(v[i]!)) i++;
  if (v[0] === '0' && i !== 1) return undefined;
  return [v.slice(0, i), v.slice(i)];
}

function parsePrerelease(v: string): [string, string] | undefined {
  let i = 1;
  let start = 1;
  while (i < v.length && v[i] !== '+') {
    if (!isIdentChar(v[i]!) && v[i] !== '.') return undefined;
    if (v[i] === '.') {
      if (start === i || isBadNum(v.slice(start, i))) return undefined;
      start = i + 1;
    }
    i++;
  }
  if (start === i || isBadNum(v.slice(start, i))) return undefined;
  return [v.slice(0, i), v.slice(i)];
}

function parseBuild(v: string): [string, string] | undefined {
  let i = 1;
  let start = 1;
  while (i < v.length) {
    if (!isIdentChar(v[i]!) && v[i] !== '.') return undefined;
    if (v[i] === '.') {
      if (start === i) return undefined;
      start = i + 1;
    }
    i++;
  }
  if (start === i) return undefined;
  return [v.slice(0, i), v.slice(i)];
}

function isDigit(c: string): boolean {
  return c >= '0' && c <= '9';
}

function isIdentChar(c: string): boolean {
  return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || isDigit(c) || c === '-';
}

function isNum(v: string): boolean {
  for (const c of v) if (!isDigit(c)) return false;
  return true;
}

function isBadNum(v: string): boolean {
  return isNum(v) && v.length > 1 && v[0] === '0';
}

function compareInt(x: string, y: string): number {
  if (x === y) return 0;
  if (x.length !== y.length) return x.length < y.length ? -1 : 1;
  return x < y ? -1 : 1;
}

function comparePrerelease(x: string, y: string): number {
  if (x === y) return 0;
  if (x === '') return 1;
  if (y === '') return -1;
  const xs = x.slice(1).split('.');
  const ys = y.slice(1).split('.');
  for (let i = 0; i < xs.length && i < ys.length; i++) {
    const dx = xs[i]!;
    const dy = ys[i]!;
    if (dx === dy) continue;
    const ix = isNum(dx);
    const iy = isNum(dy);
    if (ix !== iy) return ix ? -1 : 1;
    if (ix && dx.length !== dy.length) return dx.length < dy.length ? -1 : 1;
    return dx < dy ? -1 : 1;
  }
  return xs.length < ys.length ? -1 : 1;
}
