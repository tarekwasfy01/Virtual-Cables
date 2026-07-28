
# Virtual Cables

Virtual Cables takes a different approach from traditional virtual audio cable software. Instead of installing a custom audio kernel driver, it creates virtual USB Audio Class devices through USB/IP. Windows can then use its built-in USB audio driver to expose them as standard playback and recording devices.

## Features

- Create up to 32 independent virtual audio cables
- Each cable provides a playback and recording endpoint
- Audio sent to the playback endpoint is routed to the matching recording endpoint
- Uses the standard USB Audio Class 1.0 protocol
- Uses Windows’ built-in USB audio driver
- Simple native Windows interface
- Add or remove cables from the application
- Independent audio buffer for every cable
- No emulation of proprietary audio hardware
<img width="801" height="526" alt="Screenshot 2026-07-28 064309" src="https://github.com/user-attachments/assets/d4fd1b6e-2ecc-4192-b10e-92e59e491922" />
## How It Works

Each virtual cable is presented to Windows as a separate USB audio device. The application runs a local USB/IP server that exports these devices through the USB/IP transport.

Windows detects every cable as two connected audio endpoints:

1. `Virtual Cable XX` playback device
2. `Virtual Cable XX` recording device

Audio written to the playback side is stored in an internal ring buffer and returned through the recording side of the same cable.

This architecture is different from conventional virtual audio cables that depend on a dedicated custom audio driver.

## Audio Format

The current version uses a fixed format:

- 48,000 Hz
- Stereo
- 16-bit PCM

## Requirements

- Windows 10 or Windows 11
- x64 processor
- USB/IP for Windows transport driver
- Administrator approval when the virtual USB transport is configured

## Installation

1. Download and run the Virtual Cables installer.
2. Start **Virtual Cables**.
3. Select **Download and Install Driver** if the USB/IP transport is not installed.
4. Approve the Windows administrator request when required.
5. Use **Add Cable** or **Remove Cable** to select the desired number of cables.
6. Open Windows Sound settings to select the new playback and recording devices.

The application supports between 1 and 32 cables.

## Example

To route audio from one application into another:

1. Select `Virtual Cable 01` as the playback device in the source application.
2. Select `Virtual Cable 01` as the recording device in the destination application.
3. Start playback in the source application.

The destination application will receive the audio stream from the matching cable.

## Driver Model

Virtual Cables does not imitate MOTU, Realtek, or other proprietary hardware and does not reuse their drivers.

The application exports standards-based USB Audio Class devices. Windows supplies its built-in USB audio class driver, while the separately installed USB/IP driver provides the virtual USB transport.


## Project Status

Virtual Cables is an experimental project. Its USB descriptors, USB/IP protocol handling, audio buffers, control requests, and packet construction are covered by automated tests.

Real-world stability can depend on the Windows system, USB/IP transport, system load, and the number of simultaneously active audio cables. Testing is recommended before using the application in critical production or live-performance environments.

## Privacy

Virtual Cables processes audio locally on the computer. The virtual audio stream is routed through local memory and the USB/IP server listens only on the local Windows system by default.

## Third-Party Components

Virtual Cables uses the separately distributed `usbip-win2` transport. Third-party licenses and notices are included with the application.

## License

See `LICENSE.txt` and the files in the `LICENSES` directory for licensing information.
