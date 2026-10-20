'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');

test('production Node code has no direct privileged host access', () => {
  const root = path.resolve(__dirname, '../..');
  const directories = ['config', 'controllers', 'lib', 'middleware', 'routes', 'services'];
  const files = directories.flatMap(directory => fs.readdirSync(path.join(root, directory), { withFileTypes: true })
    .filter(entry => entry.isFile() && entry.name.endsWith('.js'))
    .map(entry => path.join(root, directory, entry.name)));
  const forbidden = [
    /node:child_process|child_process/,
    /\bexecFile(?:Sync)?\b|\bspawn(?:Sync)?\b/,
    /\bsystemctl\b|\bjournalctl\b/,
    /\/usr\/local\/etc\/xray|\/var\/log\/xray/,
    /\b(?:readFile|readFileSync|writeFile|writeFileSync|rename|chown|chmod)\b/
  ];
  for (const file of files) {
    const source = fs.readFileSync(file, 'utf8');
    for (const pattern of forbidden) assert.doesNotMatch(source, pattern, `${path.relative(root, file)} crosses the helper boundary`);
  }
});
