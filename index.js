const express = require('express');
const app = express();
const port = 3000;

const MAX_BYTES = 1e8;  // 100 MB
const DEFAULT_NUM_BYTES = 0;

// Initialize the server
console.info('Initializing server...');

// Logic for /__down
app.get('/__down', async (req, res) => {
  try {
    const reqTime = new Date();
    const value = req.query.bytes;
    const numBytes = value != null ? Math.min(MAX_BYTES, Math.abs(+value)) : DEFAULT_NUM_BYTES;

    // Log information about the request
    console.info(`Info: Received a request at /down with bytes=${value}. Using ${numBytes} bytes.`);

    res.set({
      "Access-Control-Allow-Origin": "*",
      "Timing-Allow-Origin": "*",
      "Cache-Control": "no-store",
      "Content-Type": "application/octet-stream",
      "Access-Control-Expose-Headers": "cf-meta-request-time",
      "cf-meta-request-time": String(+reqTime),
    });

    res.send('0'.repeat(Math.max(0, numBytes)));
  } catch (error) {
    console.error(`Error: An error occurred in /down - ${error}`);
  }
});

// Logic for /__up
app.post('/__up', async (req, res) => {
  try {
    const reqTime = new Date();

    console.info(`Info: Received a request at /up.`)

    res.set({
      "Access-Control-Allow-Origin": "*",
      "Timing-Allow-Origin": "*",
      "Access-Control-Expose-Headers": "cf-meta-request-time",
      "cf-meta-request-time": String(+reqTime),
    });

    res.send('OK');
  } catch (error) {
    console.error(`Error: An error occurred in /up - ${error}`);
  }
});

// Start the server
app.listen(port, '0.0.0.0', () => {
  console.log(`Server running at http://0.0.0.0:${port}/`);
  console.info('Server has started successfully.');
}).on('error', (error) => {
  console.error(`Fatal Error: Could not start the server - ${error}`);
});
