# Speedtest API

This is a simple Node.js API built using the Express framework. It provides two endpoints `/__down` and `/__up` with 
specific functionalities. This project uses PM2 for process management and deployment.

## Table of Contents

- [Requirements](#requirements)
- [Installation](#installation)
- [Running the API](#running-the-api)
- [Deployment with PM2](#deployment-with-pm2)
- [API Endpoints](#api-endpoints)

## Requirements

- Node.js >= 18.x
- PM2 (Optional for deployment)

## Installation

1. Clone the repository:

    ```bash
    git clone https://github.com/Onemind-Services-LLC/speedtest-api.git
    ```

2. Navigate to the project directory:

    ```bash
    cd /path/to/your/project
    ```

3. Install required packages:

    ```bash
    npm install
    ```

## Running the API

To run the API, execute the following command:

```bash
npm run dev
```

This will start the server on `http://0.0.0.0:3000/`.

## Deployment with PM2

1. Navigate to the project directory and run:

    ```bash
    npm run start
    ```

## API Endpoints

### `/__down`

- **Method**: `GET`
- **Description**: On receiving a GET request, it captures the current time and inspects a `bytes` query parameter. It then limits this value based on a predefined maximum or falls back to a default size. The server responds with a string of zeroes, the length of which is determined by the processed byte size. HTTP headers are set for cross-origin allowance, content-type, and custom meta information.

### `/__up`

- **Method**: `POST`
- **Description**: Upon receiving a POST request, it logs the current time and the endpoint accessed. The response is then set with several HTTP headers for cross-origin and timing allowance, as well as a custom header indicating the request time. Finally, an "OK" message is sent back as the response body.
