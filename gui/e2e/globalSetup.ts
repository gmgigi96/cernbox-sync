/**
 * Builds cernbox-syncd once before any test runs. Building inside a fixture
 * counts against the first test's timeout, which a cold Go build cache (e.g.
 * on CI) easily exceeds. Workers inherit the binary path via the environment.
 */

import { buildDaemon, DAEMON_BIN_ENV } from "./daemon";

export default function globalSetup(): void {
  process.env[DAEMON_BIN_ENV] = buildDaemon();
}
