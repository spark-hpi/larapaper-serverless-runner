const enc = new TextEncoder();
const dec = new TextDecoder();

const elog = (...a) => {
    const w = tjs.stderr.getWriter();
    w.write(enc.encode(a.map(String).join(' ') + '\n'));
    w.releaseLock();
};

console.log = elog;
console.warn = elog;
console.info = elog;
console.debug = elog;

const reader = tjs.stdin.getReader();
const chunks = [];
for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
}

const concat = (parts) => {
    let len = 0;
    for (const c of parts) len += c.length;
    const out = new Uint8Array(len);
    let off = 0;
    for (const c of parts) { out.set(c, off); off += c.length; }
    return out;
};

const input = JSON.parse(dec.decode(concat(chunks)));

const dir = new URL('.', import.meta.url).pathname;
const { __trmnl_entry: entry } = await import(dir + 'transform.js');
if (typeof entry !== 'function') {
    throw new Error('transform must define a `run(input)` function');
}
const out = await entry(input);
if (out === null || typeof out !== 'object' || Array.isArray(out)) {
    throw new Error(`run() must return an object, got ${Array.isArray(out) ? 'array' : out === null ? 'null' : typeof out}`);
}

const writer = tjs.stdout.getWriter();
await writer.write(enc.encode(JSON.stringify(out)));
writer.releaseLock();
// Exit once written so leftover timers or open sockets don't run into the timeout.
tjs.exit(0);
