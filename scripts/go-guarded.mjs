// Guarded Go test wrapper for imot-cli. Run ONLY through
// /home/victor/.pi/agent/extensions/tests/run-guarded.sh --runner tsx scripts/go-guarded.mjs
// It asserts the runner cgroup so Go tests can never escape the guard.
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

assert.match(
  readFileSync('/proc/self/cgroup', 'utf8'),
  /\/pi-test-suite-guarded\.service(?:\/|\n|$)/,
  'Go tests require the canonical guarded runner',
)

const result = spawnSync('go', ['test', './internal/scraper/', './internal/cli/', './internal/radarclient/', './internal/mcpserver/'], {
  cwd: new URL('..', import.meta.url).pathname,
  stdio: 'inherit',
  env: { ...process.env, GOFLAGS: '-count=1' },
})
assert.ifError(result.error)
assert.equal(result.status, 0, 'go test failed')
