<?php
ini_set('display_errors', 'stderr');
error_reporting(E_ALL);

$input = json_decode(file_get_contents('php://stdin'), true);

// Anything echoed by the transform (at include time or inside run()) goes to stderr.
ob_start(static function (string $buffer): string {
    if ($buffer !== '') {
        fwrite(STDERR, $buffer);
    }
    return '';
}, 1);

include __DIR__ . '/transform.php';

if (function_exists('run')) {
    $output = run($input);
} elseif (function_exists('transform')) {
    $output = transform($input);
} else {
    fwrite(STDERR, "transform must define a `run(\$input)` function\n");
    exit(1);
}

ob_end_flush();

if (is_array($output) && $output === []) {
    $output = new stdClass();
}

fwrite(STDOUT, json_encode($output, JSON_THROW_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE | JSON_UNESCAPED_SLASHES));
