+++
title = 'Start your library'
description = 'Install Pharos on your Mac or an external drive, then choose the conversations to bring into your library.'
+++

Pharos runs on macOS 14 or later, on Apple Silicon and Intel Macs.

## Download Pharos

1. Open [the latest GitHub release](https://github.com/Pythia-Software/pharos/releases/latest) and download the `.dmg` file from its assets.
2. Open the disk image and copy **Pharos.app** to Applications, or to a dedicated folder on your external drive.
3. Open your copied app and choose where to keep your library.

## Keep it on this Mac

Choose **Set Up on This Mac**. Pharos stores your library under `~/Library/Application Support/Pharos`. Choose which sources to enable, then use **Capture & index** to bring them into your library.

## Take it with you

Create a dedicated folder on an external drive, put **Pharos.app** inside, and choose **Create Library Beside App**. Keep the app and the library’s other files together when moving between Macs.

## Find your way around

Use **Library** to search your conversations. Explore token usage, model costs, and tool activity to understand your work. The **MCP** tab provides configuration for giving an agent read-only access to your history.

Pharos reads source conversations without modifying them. Captures and derived data stay in your chosen library. Its local service listens only on loopback; optional features such as update checks contact GitHub.

## Update an existing library

Quit Pharos and any connected MCP clients. Download the new disk image and replace your existing **Pharos.app**, leaving the library’s other files in place. Reopen Pharos and reconnect your clients.

For detailed setup and configuration, see [the project documentation](https://github.com/Pythia-Software/pharos#readme).
