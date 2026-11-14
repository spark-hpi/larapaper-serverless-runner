require 'json'

# Don't depend on the locale: input, code and output are always UTF-8.
Encoding.default_external = Encoding::UTF_8
Encoding.default_internal = nil

# Keep the real stdout for the result; route everything else (puts, p, warn) to stderr.
result_out = STDOUT.dup
result_out.sync = true
$stdout = $stderr
STDOUT.reopen($stderr)

input = JSON.parse($stdin.binmode.read.force_encoding(Encoding::UTF_8))

load File.join(__dir__, 'transform.rb')

entry =
  if Object.private_method_defined?(:run) || Object.method_defined?(:run)
    :run
  elsif Object.private_method_defined?(:transform) || Object.method_defined?(:transform)
    :transform
  end

raise NameError, 'transform must define a `run(input)` function' if entry.nil?

output = send(entry, input)

result_out.write(JSON.generate(output))
result_out.flush
