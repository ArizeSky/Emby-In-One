const winston = require('winston');
const path = require('path');
const fs = require('fs');

const logFormat = winston.format.combine(
  winston.format.timestamp({ format: 'YYYY-MM-DD HH:mm:ss' }),
  winston.format.errors({ stack: true }),
  winston.format.printf(({ timestamp, level, message, stack }) => {
    return stack
      ? `${timestamp} [${level.toUpperCase()}] ${message}\n${stack}`
      : `${timestamp} [${level.toUpperCase()}] ${message}`;
  })
);

const validLogLevels = ['error', 'warn', 'info', 'debug'];

const logger = winston.createLogger({
  level: 'debug',
  format: logFormat,
  transports: [
    new winston.transports.Console({ level: validLogLevels.includes(process.env.LOG_LEVEL) ? process.env.LOG_LEVEL : 'info' }),
  ],
});

let fileTransport = null;

/**
 * Set the data directory and add a file transport for persistent logging.
 * Call this after config is loaded to know where data/ lives.
 */
function setDataDir(dataDir) {
  if (fileTransport) return; // already initialized

  const dir = dataDir || (fs.existsSync('/app/data') ? '/app/data' : path.resolve(__dirname, '..', '..', 'data'));
  if (!fs.existsSync(dir)) fs.mkdirSync(dir, { recursive: true });

  const logFile = path.join(dir, 'emby-in-one.log');
  fileTransport = new winston.transports.File({
    filename: logFile,
    level: validLogLevels.includes(process.env.FILE_LOG_LEVEL) ? process.env.FILE_LOG_LEVEL : 'info',
    maxsize: 5 * 1024 * 1024,
    maxFiles: 1,
    tailable: true,
  });
  logger.add(fileTransport);
  logger.info(`Log file: ${logFile}`);
}

/**
 * Get the path of the current log file (for download/clear endpoints).
 */
function getLogFilePath() {
  return fileTransport ? fileTransport.filename : null;
}

module.exports = logger;
module.exports.setDataDir = setDataDir;
module.exports.getLogFilePath = getLogFilePath;
