/**
 * Builds cernbox-syncd once before any test runs. Building it lazily inside
 * the first test of each worker counted the (cold-cache) compilation against
 * that test's timeout; the workers pick up the binary via the env variable.
 */

import { buildDaemon, DAEMON_BIN_ENV } from "./daemon";

export default function globalSetup() {
  process.env[DAEMON_BIN_ENV] = buildDaemon();
}
