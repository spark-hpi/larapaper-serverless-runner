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

output = mod.run(input)

os.write(stdout, json.dumps(output).encode())
os.close(stdout)
