<?php
$input = json_decode(file_get_contents('php://stdin'), true);
ob_start();

include __DIR__ . '/transform.php';

ob_end_clean();
fwrite(STDOUT, json_encode(run($input)));
