# Jackett: download-scoped VPN routing

This image retains LinuxServer's `/config`, PUID/PGID, port 9117 and s6 startup.
It builds Jackett **v0.25.28**, source commit
`bd5e25fa591821d320f61eb09e301c2af1404c8b`, on the matching LinuxServer
`v0.25.28-ls45` image, with a small routing patch.
Both the release tag and the exact commit are checked during the build.

Jackett's `/dl` endpoint downloads torrent bytes itself. Sending only the Go
request to `/dl` through a proxy would not change Jackett's tracker connection.
The patch therefore enters an `AsyncLocal` scope around the indexer's download
operation. `IWebProxy` routes that operation and its redirects through
`TORRENT_DOWNLOAD_PROXY` (default `http://torrent-vpn:8888`). Searches remain
direct, including when a download runs concurrently. Stored Jackett proxy
settings and ambient HTTP(S)_PROXY/NO_PROXY cannot override this policy.

The cached HTTP client bypasses FlareSolverr while in the download scope.
Otherwise a remote FlareSolverr process could fetch torrent bytes outside the
VPN. A download that requires that external browser service fails instead of
using an uncontrolled route. Ordinary searches may still use FlareSolverr
directly; its legacy proxy settings are not forwarded.

There is no direct retry when the VPN proxy is unavailable. Blackhole downloads
use the same scope. Regular updates must rebuild this image: the startup command
always supplies `--NoUpdates`, even if an old stack still sets AUTO_UPDATE=true.

Build from the repository root:

```sh
docker build -f tools/jackett-vpn/Dockerfile -t video-viewer-jackett .
```

The Docker build checks that the patch applies, runs the routing tests and
publishes the full patched application. Local tests need no NuGet packages:

```sh
dotnet run --project tools/jackett-vpn/tests/ProxyRouting.csproj -c Release
```

The tests target .NET 6 for the installed development SDK; the image tests the
same source against .NET 10, which is the pinned Jackett release's framework.
The full self-contained `net10.0` / `linux-musl-x64` publish was also checked
with the portable .NET SDK 10.0.404. Publish uses `-m:1` because Jackett's
project-reference graph can otherwise compile `DateTimeRoutines` twice into
the same output directory concurrently.
All 20 routing checks also passed on .NET 10, including concurrent searches,
redirects, HTTPS CONNECT and an unavailable VPN proxy.
When updating Jackett, update the release, commit and runtime image together,
reapply the patch against that exact source and rebuild.

On machines with a protected user NuGet configuration, restore explicitly before
running the package-free tests:

```sh
dotnet restore tools/jackett-vpn/tests/ProxyRouting.csproj --configfile tools/jackett-vpn/tests/NuGet.Config
dotnet run --project tools/jackett-vpn/tests/ProxyRouting.csproj -c Release --no-restore
```
