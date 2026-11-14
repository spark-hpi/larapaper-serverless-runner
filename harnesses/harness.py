import sys
import json
import os
import importlib.util

stdout = os.dup(1)
os.dup2(2, 1)
sys.stdout = sys.stderr

input = json.loads(sys.stdin.buffer.read())

spec = importlib.util.spec_from_file_location(
    "transform",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "transform.py"),
)

if spec is None or spec.loader is None:
    raise Exception("Invalid module transform.py")

mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

entry = getattr(mod, "run", None) or getattr(mod, "transform", None)
if not callable(entry):
    raise NameError("transform must define a `run(input)` function")

output = entry(input)

# allow_nan=False: NaN/Infinity are not JSON; fail here with a clear error.
os.write(stdout, json.dumps(output, default=str, allow_nan=False).encode())
os.close(stdout)
