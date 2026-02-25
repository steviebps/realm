export function encodePath(path: string) {
  return path
    ? path
        .split('/')
        .map((segment) => encodeURIComponent(segment))
        .join('/')
    : path;
}

export function ensureTrailingSlash(path: string) {
  return path.endsWith('/') ? path : path + '/';
}
