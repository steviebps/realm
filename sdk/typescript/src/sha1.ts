// The message schedule, reused across calls.
const w = new Uint32Array(80);

/** Returns the SHA-1 digest of data. */
export function sha1(data: Uint8Array): Uint8Array {
  const out = new Uint8Array(20);
  const view = new DataView(out.buffer);
  sha1Words(data).forEach((h, i) => view.setUint32(i * 4, h));
  return out;
}

/**
 * Returns the SHA-1 digest of data as five big-endian 32-bit words. SHA-1 is
 * used only for rollout bucketing, not for security; it is implemented here so
 * evaluation stays synchronous in every runtime (Web Crypto's digest is
 * asynchronous).
 */
export function sha1Words(data: Uint8Array): Uint32Array {
  // Pad to a multiple of 64 bytes: 0x80, zeros, then the bit length.
  const total = Math.ceil((data.length + 9) / 64) * 64;
  const msg = new Uint8Array(total);
  msg.set(data);
  msg[data.length] = 0x80;
  const view = new DataView(msg.buffer);
  const bits = data.length * 8;
  view.setUint32(total - 8, Math.floor(bits / 0x100000000));
  view.setUint32(total - 4, bits >>> 0);

  let h0 = 0x67452301;
  let h1 = 0xefcdab89;
  let h2 = 0x98badcfe;
  let h3 = 0x10325476;
  let h4 = 0xc3d2e1f0;

  for (let off = 0; off < total; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4);
    for (let i = 16; i < 80; i++) {
      const x = w[i - 3]! ^ w[i - 8]! ^ w[i - 14]! ^ w[i - 16]!;
      w[i] = (x << 1) | (x >>> 31);
    }

    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    for (let i = 0; i < 80; i++) {
      let f: number;
      let k: number;
      if (i < 20) {
        f = (b & c) | (~b & d);
        k = 0x5a827999;
      } else if (i < 40) {
        f = b ^ c ^ d;
        k = 0x6ed9eba1;
      } else if (i < 60) {
        f = (b & c) | (b & d) | (c & d);
        k = 0x8f1bbcdc;
      } else {
        f = b ^ c ^ d;
        k = 0xca62c1d6;
      }
      const t = (((a << 5) | (a >>> 27)) + f + e + k + w[i]!) >>> 0;
      e = d;
      d = c;
      c = ((b << 30) | (b >>> 2)) >>> 0;
      b = a;
      a = t;
    }
    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
  }

  return Uint32Array.of(h0, h1, h2, h3, h4);
}
