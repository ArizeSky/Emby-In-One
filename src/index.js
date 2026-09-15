const { loadConfig } = require('./config');
const logger = require('./utils/logger');
const { setDataDir } = require('./utils/logger');
const capturedHeaders = require('./utils/captured-headers');

async function main() {
  // 密码重置 CLI
  const args = process.argv.slice(2);
  const resetIdx = args.indexOf('--reset-password');
  if (resetIdx !== -1) {
    const newPass = args[resetIdx + 1];
    if (!newPass) {
      console.error('Usage: node src/index.js --reset-password <new-password>');
      process.exit(1);
    }
    const config = loadConfig();
    const { hashPassword } = require('./auth');
    const { saveConfig } = require('./config');
    config.admin.password = hashPassword(newPass);
    await saveConfig(config);
    console.log('Admin password has been reset successfully.');
    process.exit(0);
  }

  logger.info('Emby-in-One starting...');

  const config = loadConfig();
  setDataDir(config.dataDir);
  capturedHeaders.init(config.dataDir);
  logger.info(`Loaded config: ${config.upstream.length} upstream server(s)`);

  // These will be filled in as we implement each module
  const { createIdManager } = require('./id-manager');
  const { createUpstreamManager } = require('./upstream-manager');
  const { createApp } = require('./server');

  const idManager = createIdManager(config.dataDir);
  const upstreamManager = createUpstreamManager(config, idManager);

  // Login to all upstream servers
  await upstreamManager.loginAll();

  // Create and start Express server
  const app = createApp(config, idManager, upstreamManager);
  const port = config.server.port;

  const server = app.listen(port, () => {
    logger.info(`Emby-in-One listening on port ${port}`);
    logger.info(`Public info: http://localhost:${port}/System/Info/Public`);
  });

  // Graceful shutdown
  function shutdown(signal) {
    logger.info(`${signal} received, shutting down...`);
    upstreamManager.stopHealthChecks();
    server.close(() => {
      idManager.close();
      logger.info('Server closed');
      process.exit(0);
    });
    // Force exit after 10s if connections don't close
    setTimeout(() => process.exit(1), 10000).unref();
  }
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));
}

main().catch(err => {
  logger.error('Fatal error:', err);
  process.exit(1);
});
