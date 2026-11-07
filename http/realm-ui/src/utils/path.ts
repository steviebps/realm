export function encodePath(path: string) {
  return path
    ? path
        .split('/')
        .map((segment) => encodeURIComponent(segment))
        .join('/')
    : path;
}

/**
 * Decodes each segment of a URL path (such as location.pathname, which keeps
 * the encoding), leaving a malformed one as it is: the inverse of encodePath.
 */
export function decodePath(path: string) {
  return path
    .split('/')
    .map((segment) => {
      try {
        return decodeURIComponent(segment);
      } catch {
        return segment;
      }
    })
    .join('/');
}

export function ensureTrailingSlash(path: string) {
  return path.endsWith('/') ? path : path + '/';
}
