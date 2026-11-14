import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { format } from 'node:util';

// stdout carries the JSON result; route all console output, and direct
// process.stdout writes by the transform, to stderr (logs).
const writeResult = process.stdout.write.bind(process.stdout);
process.stdout.write = process.stderr.write.bind(process.stderr);
const elog = (...a) => { process.stderr.write(format(...a) + '\n'); };
console.log = elog;
console.info = elog;
console.debug = elog;
console.warn = elog;

const dir = new URL('.', import.meta.url).pathname;

// TRMNL snippets are backwards compatible with CommonJS (`require(...)`,
// `module.exports = ...`, `if (require.main === module)`). The transform is
// loaded as ESM, so expose the CommonJS globals. `require.main` is undefined
// here, so "run when executed directly" blocks are skipped.
const transformPath = dir + 'transform.mjs';
const cjsModule = { exports: {}, filename: transformPath, id: '.', loaded: false };
globalThis.require = createRequire(transformPath);
globalThis.module = cjsModule;
globalThis.exports = cjsModule.exports;
globalThis.__filename = transformPath;
globalThis.__dirname = dir.replace(/\/$/, '');

const input = JSON.parse(readFileSync(0, 'utf8'));

const { __trmnl_entry: declared } = await import(transformPath);
// Fall back to `module.exports = run`, `module.exports = { run }` or `exports.run = ...`.
const cjs = cjsModule.exports;
const entry = declared
    ?? (typeof cjs === 'function' ? cjs : (cjs && (typeof cjs.run === 'function' ? cjs.run : cjs.transform)));
if (typeof entry !== 'function') {
    throw new Error('transform must define a `run(input)` function');
}
const out = await entry(input);
if (out === null || typeof out !== 'object' || Array.isArray(out)) {
    throw new Error(`run() must return an object, got ${Array.isArray(out) ? 'array' : out === null ? 'null' : typeof out}`);
}

// Exit once written so leftover timers or open sockets don't run into the timeout.
writeResult(JSON.stringify(out), () => process.exit(0));
