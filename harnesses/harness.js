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
const { run } = await import(dir + 'transform.js');
const out = await run(input);

const writer = tjs.stdout.getWriter();
await writer.write(enc.encode(JSON.stringify(out)));
writer.releaseLock();
