# transform-runner

HTTP service that executes untrusted user-supplied transform scripts in a sandboxed environment. Each request provides code and JSON input; the service runs the code and returns JSON output.

## How it works

1. Request arrives with `language`, `code`, `input` (JSON), and optional `timeout`
2. Code is written to a temp directory alongside an embedded harness script
3. Harness is executed inside a [bubblewrap](https://github.com/containers/bubblewrap) sandbox — new user/PID/IPC/UTS namespaces, UID 65534 (nobody), read-only filesystem mounts, a clean environment, `ulimit -v` memory cap and `ulimit -f` file size cap
4. Harness loads the user's `run()` function, passes `input` via stdin, and writes the return value as JSON to stdout
5. Output is validated as a JSON object and returned to the caller

## Supported languages

| Language | Runtime | Bundled for HTTP |
|----------|---------|------------------|
| `python` | python3 | `requests` |
| `node`   | Node.js 22 (default image) or [txiki.js](https://github.com/saghul/txiki.js) (`-slim` image) | global `fetch()` |
| `php`    | php84 | `curl` extension (+ `mbstring`, `openssl`) |
| `ruby`   | ruby | `httparty` gem |

Aliases are accepted case-insensitively: `javascript`/`js`/`nodejs` → `node`, `py`/`python3` → `python`, `rb` → `ruby`.

The sandbox has network access. It aims to run [TRMNL Serverless](https://help.trmnl.com/en/articles/14130649-serverless) snippets unchanged.

## Image variants

| Image | JS runtime | Size | Peak RSS (trivial script) |
|-------|------------|------|---------------------------|
| `:latest`, `:<version>` | Node.js 22, full ICU | ~265 MB | ~43 MB |
| `:latest-slim`, `:<version>-slim` | txiki.js | ~160 MB | ~15 MB |

Python, PHP and Ruby are the same in both images. Releases are tagged `X.Y.Z` with no `v` prefix. Images get `X.Y.Z`, `X.Y` and `X` tags, and `latest` moves only on non-prerelease versions.

- **Default (Node.js):** `require()` (Node built-ins only, no npm packages), `import`, `process`, `Buffer`, `node:*` modules and full `Intl` with all locales.
- **Slim (txiki.js):** `fetch` and ESM work. **`Intl` and `require` are not available**, and neither are `process`, `Buffer` or `node:*`. Choose slim only when memory footprint matters more than compatibility.

## API

### `POST /run`

When `TRANSFORM_API_KEY` is set, requests need `Authorization: Bearer <key>`.

```json
{
  "language": "python",
  "code": "def run(input):\n    return {'doubled': input['value'] * 2}",
  "input": {"value": 5},
  "timeout": 10
}
```

**Response `200`**
```json
{"output": {"doubled": 10}, "logs": ""}
```

`input` defaults to `{}` when omitted.

**Response `400`** — invalid request or unsupported language  
**Response `401`** — missing or wrong API key  
**Response `413`** — request body larger than `TRANSFORM_MAX_REQUEST_BYTES`  
**Response `422`** — runtime error, timeout, non-zero exit, non-object output or output over 10 MB: `{"error": "...", "logs": "..."}`  
**Response `503`** — `TRANSFORM_MAX_CONCURRENCY` transforms are already running; retry later

`logs` holds the transform's stderr (print/puts/echo/console.log output and runtime errors). Only the last 64 KB is kept.

### `GET /manifest` and `GET /`

Describes this runner instance. `/` is an alias.

```json
{
  "name": "BYOS Transform Runner",
  "version": "0.1.0",
  "author": "Benjamin Nussbaum, Spark HPI",
  "license": "EUPL-1.2",
  "repository": "https://github.com/bnussbau/byos-transform-runner",
  "contributor_info": "Based on the initial work of https://github.com/spark-hpi/larapaper-serverless-runner",
  "language_capabilities": ["node", "php", "python", "ruby"]
}
```

`name` is `BYOS Transform Runner (slim)` on the slim image (JS on txiki.js). `version` is the release tag the image was built from (`docker build --build-arg VERSION=...`), or `dev` for local builds. `language_capabilities` lists the supported languages whose interpreter is installed in the image, sorted. Clients such as LaraPaper use it to offer only runnable languages.

### `GET /health`

Returns `200 OK`:

```json
{"status": "UP"}
```

## Writing transforms

Each language expects a `run` function that takes the input object and returns a JSON-serializable object. If no `run` exists, a function named `transform` is used instead (the entry point in TRMNL legacy exports). `console.log` / `print` / `puts` / `echo` goes to stderr and comes back as `logs`.

**Python**
```python
def run(input):
    return {"result": input["value"] * 2}
```

**Node** (`export` optional, may be `async`)
```js
async function run(input) {
    const res = await fetch("https://dummyjson.com/quotes?limit=1");
    return { quote: (await res.json()).quotes[0] };
}
```

**PHP** (`<?php` tag is optional — stripped automatically)
```php
<?php
function run($input) {
    return ["result" => $input["value"] * 2];
}
```

**Ruby**
```ruby
require 'httparty'

def run(input)
  { quotes: HTTParty.get('https://dummyjson.com/quotes?limit=10')['quotes'] }
end
```

Input keys are strings (`input['trmnl']`). Returned hashes may use symbol or string keys.

### Encoding

Code, input and output are UTF-8. A leading byte order mark in the code is stripped. Invalid UTF-8 and unpaired UTF-16 surrogates in the output (for example from a JS `str.slice()` that cuts an emoji in half) are replaced with U+FFFD, so strict decoders such as PHP's `json_decode` accept the result. `NaN` and `Infinity` are not valid JSON and fail the run.

### Known limitations

- **Node.js:** transforms load as ES modules, which are always strict mode. CommonJS snippets that assign undeclared variables (`x = 1`) or use `with` throw.
- **PHP:** input is decoded with `json_decode($json, true)`, so empty objects nested in the input become `[]`. Only a top-level `[]` return value is sent as `{}`.

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `PORT` | `3000` | Listen port |
| `TRANSFORM_TIMEOUT` | `30` | Max execution seconds |
| `TRANSFORM_MEMORY_LIMIT` | `134217728` | Memory limit in bytes (128 MB). Applied as `ulimit -v`, except under Node.js, where it is the V8 heap limit (`--max-old-space-size`) because V8 cannot start under a small address-space limit |
| `TRANSFORM_NODE_RUNTIME` | `node` | JS runtime: `node` or `txiki`. Each image sets it to match the runtime it ships |
| `TRANSFORM_API_KEY` | unset | When set, `POST /run` requires `Authorization: Bearer <key>`. `/health` and `/manifest` stay public |
| `TRANSFORM_MAX_CONCURRENCY` | 2 × CPUs | Transforms running at once; more get `503` |
| `TRANSFORM_MAX_REQUEST_BYTES` | `5242880` | Max request body (5 MB) |
| `LOG_LEVEL` | `info` | `debug` also logs input and output payloads (up to 64 KB each). They may contain plugin secrets |

## Running

**Docker Compose**
```sh
docker compose up
```

**Build image manually**
```sh
docker build -t transform-runner .                         # Node.js (default)
docker build --target slim -t transform-runner:slim .      # txiki.js
docker run -p 3000:3000 -e TRANSFORM_API_KEY=change-me \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
  --cap-drop ALL --read-only --tmpfs /tmp --pids-limit 256 --memory 1g \
  transform-runner
```

> `seccomp=unconfined` and `apparmor=unconfined` are required for bubblewrap's user namespace sandboxing to work inside Docker.

## Security

- **Authentication:** set `TRANSFORM_API_KEY` unless the port is reachable only by LaraPaper. Without it, anyone who can reach the port can run code. The runner logs a warning at startup.
- **Network:** the sandbox shares the container's network. Transforms can reach anything the container can, including other containers, the cloud metadata endpoint (`169.254.169.254`) and the runner itself. Put the runner on its own Docker network, and don't give it access to databases or internal services.
- **`/proc`:** with `--security-opt systempaths=unconfined`, each sandbox gets a private `/proc` for its own PID namespace. Without it, Docker blocks that mount and transforms run with no `/proc` (only a stub `/proc/self/exe`). The container's `/proc` is never exposed, because it would leak the runner's environment and other transforms' data. The runner logs which mode is active at startup.
- **Memory:** `TRANSFORM_MEMORY_LIMIT` is a per-process `ulimit -v`. Under Node.js it caps only the V8 heap, so `Buffer`/`ArrayBuffer` memory is not limited. Always set a container memory limit, sized for `TRANSFORM_MAX_CONCURRENCY` × `TRANSFORM_MEMORY_LIMIT` plus headroom.
- **Processes:** there is no per-transform process limit. Set `--pids-limit` (compose: `pids_limit`) so a fork bomb stays inside the container.
- **Logs:** at the default `LOG_LEVEL=info`, only language, status, duration and sizes are logged, never payloads.

**Local (sandbox tests require `bwrap` on PATH)**
```sh
go test ./...
go run .
```

## Contributors
This project is based on work by [spark-hpi](https://github.com/spark-hpi/larapaper-serverless-runner).
