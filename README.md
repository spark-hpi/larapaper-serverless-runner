# transform-runner

HTTP service that executes untrusted user-supplied transform scripts in a sandboxed environment. Each request provides code and JSON input; the service runs the code and returns JSON output.

## How it works

1. Request arrives with `language`, `code`, `input` (JSON), and optional `timeout`
2. Code is written to a temp directory alongside an embedded harness script
3. Harness is executed inside a [bubblewrap](https://github.com/containers/bubblewrap) sandbox — new user/PID/IPC/UTS namespaces, UID 65534 (nobody), read-only filesystem mounts, `ulimit -v` memory cap
4. Harness loads the user's `run()` function, passes `input` via stdin, and writes the return value as JSON to stdout
5. Output is validated as a JSON object and returned to the caller

## Supported languages

| Language | Runtime |
|----------|---------|
| `python` | python3 |
| `node`   | [txiki.js](https://github.com/saghul/txiki.js) (tjs) |
| `php`    | php83 |

## API

### `POST /run`

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
{"output": {"doubled": 10}}
```

**Response `400`** — invalid request or unsupported language  
**Response `422`** — runtime error, timeout, non-zero exit, or non-object output

### `GET /health`

Returns `200 OK`.

## Writing transforms

Each language expects a `run` function that accepts the input object and returns a JSON-serializable object. `console.log` / `print` / `echo` goes to stderr (visible in server logs, not in the response).

**Python**
```python
def run(input):
    return {"result": input["value"] * 2}
```

**Node (ESM or CJS `export`)**
```js
export function run(input) {
    return { result: input.value * 2 };
}
```

**PHP** (`<?php` tag is optional — stripped automatically)
```php
<?php
function run($input) {
    return ["result" => $input["value"] * 2];
}
```

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `PORT` | `3000` | Listen port |
| `TRANSFORM_TIMEOUT` | `30` | Max execution seconds (cap applied per-request) |
| `TRANSFORM_MEMORY_LIMIT` | `134217728` | Virtual memory limit in bytes (128 MB) |

## Running

**Docker Compose**
```sh
docker compose up
```

**Build image manually**
```sh
docker build -t transform-runner .
docker run -p 3000:3000 --security-opt seccomp=unconfined transform-runner
```

> `seccomp=unconfined` is required for bubblewrap's user namespace sandboxing to work inside Docker.

**Local (sandbox tests require `bwrap` on PATH)**
```sh
go test ./...
go run .
```
