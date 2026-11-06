export const trimPrefix = (str: string, prefix: string) => {
  if (str.startsWith(prefix)) {
    return str.slice(prefix.length);
  }
  return str;
};

export const trimSuffix = (str: string, suffix: string) => {
  if (str.endsWith(suffix)) {
    return str.slice(0, -suffix.length);
  }
  return str;
};

/** "1 rule", "2 rules". */
export const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
