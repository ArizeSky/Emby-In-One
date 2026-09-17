const { Router } = require('express');

function createSystemRoutes(config) {
  const router = Router();

  // GET /System/Info/Public — no auth required
  router.get('/System/Info/Public', (req, res) => {
    res.json({
      LocalAddress: `${req.protocol}://${req.get('host')}`,
      ServerName: config.server.name,
      Version: '4.7.14.0',
      ProductName: 'Emby Server',
      Id: config.server.id,
      StartupWizardCompleted: true,
      OperatingSystem: 'Linux',
      CanSelfRestart: false,
      CanLaunchWebBrowser: false,
      HasUpdateAvailable: false,
      SupportsAutoRunAtStartup: false,
      HardwareAccelerationRequiresPremiere: false,
    });
  });

  // GET /System/Info — requires auth
  router.get('/System/Info', (req, res) => {
    if (!req.proxyUser) return res.status(401).json({ message: 'Unauthorized' });

    res.json({
      LocalAddress: `${req.protocol}://${req.get('host')}`,
      WanAddress: '',
      ServerName: config.server.name,
      Version: '4.7.14.0',
      ProductName: 'Emby Server',
      Id: config.server.id,
      StartupWizardCompleted: true,
      OperatingSystem: 'Linux',
      OperatingSystemDisplayName: 'Linux',
      CanSelfRestart: false,
      CanLaunchWebBrowser: false,
      HasUpdateAvailable: false,
      SupportsAutoRunAtStartup: false,
      SystemUpdateLevel: 'Release',
      HardwareAccelerationRequiresPremiere: false,
      HasPendingRestart: false,
      IsShuttingDown: false,
      TranscodingTempPath: '/tmp',
      LogPath: '/tmp',
      InternalMetadataPath: '/tmp',
      CachePath: '/tmp',
      ProgramDataPath: '/tmp',
      ItemsByNamePath: '/tmp',
    });
  });

  // GET /System/Endpoint
  router.get('/System/Endpoint', (req, res) => {
    res.json({
      IsLocal: false,
      IsInNetwork: false,
    });
  });

  // GET /System/Ping
  router.post('/System/Ping', (req, res) => {
    res.send('Emby Aggregator');
  });
  router.get('/System/Ping', (req, res) => {
    res.send('Emby Aggregator');
  });

  return router;
}

module.exports = { createSystemRoutes };
