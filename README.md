# OneMind Services Speedtest API

The regional measurement service behind OneMind Services' browser-based speed testing experience.

Speedtest API helps users understand how their internet connection performs when connecting to a specific datacenter. It works with [Speedtest UI](https://github.com/Onemind-Services-LLC/speedtest-ui) to support download and upload speed tests, latency measurements, packet-loss testing, and network identification.

## Capabilities

- **Connection performance:** Supports download and upload measurements, alongside latency checks while the connection is idle and during transfers.
- **Packet loss:** Enables the companion browser application to assess packet delivery before and during transfers, providing additional context about connection quality.
- **Regional testing:** Gives each test location a distinct identity so the browser application can discover available regions and select a responsive endpoint.
- **Network insights:** Provides the observed IP address and address family, with optional identification of the associated network organization through a local database.

## A regional perspective

Each API instance represents an independent test location. Measurement traffic flows directly between the user's browser and the selected regional server, while the companion application manages the test experience and presents the results.

This approach supports connectivity troubleshooting and comparisons across datacenters. Results describe performance along the path to the selected location; the user's local network, provider routing, and server capacity can all influence the outcome.

## Data handling

The API does not retain client IP addresses or individual test results. Application logs exclude client addresses and transfer contents. Optional network identification uses a local database without sending client addresses to an external lookup service.

## Project resources

- [Speedtest UI](https://github.com/Onemind-Services-LLC/speedtest-ui) — the companion browser application.
- [Technical reference](docs/technical-reference.md) — setup, configuration, integration, and operational details.

Maintained by [OneMind Services](https://github.com/Onemind-Services-LLC).
