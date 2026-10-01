// Validate the canonical production configuration; never starts the stack.
// --config-only skips credential checks when using placeholders for local QA.
import { readFileSync, statSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const root = fileURLToPath(new URL('../../', import.meta.url));
const args = process.argv.slice(2);
if (args.some(arg => arg !== '--config-only')) {
  console.error('Usage: node scripts/ops/check-deploy.mjs [--config-only]');
  process.exit(2);
}
function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}
function docker(arguments_, message) {
  const result = spawnSync('docker', arguments_, {cwd: root, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 120000});
  // Compose output contains resolved secrets: never print stdout or stderr.
  requireValue(!result.error && result.status === 0, message);
  return result.stdout;
}
function databaseURL(environment, key, schema) {
  let url;
  try { url = new URL(environment[key]); } catch { throw new Error(`${key} is invalid.`); }
  requireValue(url.protocol === 'mysql:' && url.pathname === `/${schema}`, `${key} must target ${schema}.`);
}
function redisArgument(service, key) {
  requireValue(Array.isArray(service.command), 'Redis command must be an argument list.');
  const index = service.command.indexOf(key);
  return index < 0 ? undefined : service.command[index + 1];
}
try {
  const output = docker(['compose', '-f', 'docker-compose.yml', 'config', '--format', 'json'], 'Compose validation failed. Check .env and run docker compose config --quiet.');
  let config;
  try { config = JSON.parse(output); } catch { throw new Error('Unable to read Compose configuration JSON.'); }
  const services = config.services;
  for (const name of ['mysql', 'central', 'gateway', 'worker', 'migrate', 'caddy', 'redis-cache', 'redis-stream']) {
    requireValue(services[`chargepilot-${name}`], `Missing service: chargepilot-${name}.`);
  }
  for (const [name, service] of Object.entries(services)) {
    for (const port of service.ports ?? []) {
      const allowed = name === 'chargepilot-caddy' ? [80, 443] : name === 'chargepilot-gateway' ? [9100] : [];
      requireValue(allowed.includes(Number(port.target)) && Number(port.published) === Number(port.target), `Unexpected published port on ${name}.`);
    }
    for (const volume of service.volumes ?? []) {
      if (volume.type !== 'bind') continue;
      const info = statSync(volume.source);
      requireValue(info.isFile() || info.isDirectory(), `Invalid bind path on ${name}.`);
    }
    if (service.build) {
      statSync(resolve(service.build.context, service.build.dockerfile));
    }
  }
  for (const key of ['USER', 'ADMIN', 'BILLING']) {
    requireValue(!services['chargepilot-central'].environment[`DATABASE_URL_${key}`], 'Central must use one database URL.');
  }
  databaseURL(services['chargepilot-central'].environment, 'DATABASE_URL_CENTRAL', 'central_db');
  databaseURL(services['chargepilot-worker'].environment, 'DATABASE_URL_CENTRAL', 'central_db');
  databaseURL(services['chargepilot-gateway'].environment, 'DATABASE_URL', 'gateway_db');
  databaseURL(services['chargepilot-worker'].environment, 'DATABASE_URL', 'worker_db');
  for (const [name, policy] of [['redis-cache', 'allkeys-lru'], ['redis-stream', 'noeviction']]) {
    requireValue(redisArgument(services[`chargepilot-${name}`], '--maxmemory-policy') === policy, `${name} must use ${policy}.`);
  }
  const central = services['chargepilot-central'].environment;
  if (!args.includes('--config-only')) {
    const secrets = {
      MYSQL_ROOT_PASSWORD: services['chargepilot-mysql'].environment.MYSQL_ROOT_PASSWORD,
      DB_PASSWORD: services['chargepilot-mysql'].environment.MYSQL_PASSWORD,
      JWT_SECRET: central.JWT_SECRET,
      SERVICE_TOKEN: central.SERVICE_TOKEN,
      REDIS_PASSWORD: redisArgument(services['chargepilot-redis-cache'], '--requirepass'),
      REDIS_STREAM_PASSWORD: redisArgument(services['chargepilot-redis-stream'], '--requirepass'),
    };
    for (const [name, value] of Object.entries(secrets)) {
      requireValue(value && !value.startsWith('change-me'), `${name} is missing or still uses a template value.`);
    }
    requireValue(central.JWT_SECRET.length >= 32, 'JWT_SECRET must contain at least 32 characters.');
  }
  const caddy = resolve(root, 'docker/Caddyfile');
  requireValue(/handle \/api\/v1\/internal\/\*/.test(readFileSync(caddy, 'utf8')), 'Caddy must block the internal API routes.');
  console.log('Compose and structure checks passed; validating Caddy configuration...');
  docker(['run', '--rm', '--network', 'none', '-e', `PUBLIC_DOMAIN=${services['chargepilot-caddy'].environment.PUBLIC_DOMAIN}`, '-v', `${caddy}:/etc/caddy/Caddyfile:ro`, services['chargepilot-caddy'].image, 'caddy', 'adapt', '--config', '/etc/caddy/Caddyfile', '--validate'], 'Caddy validation failed. Check Docker image availability and docker/Caddyfile.');
  console.log('Compose, bind paths, ports, database URLs, Redis policies and Caddy validation passed.');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
