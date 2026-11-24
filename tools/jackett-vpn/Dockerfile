# syntax=docker/dockerfile:1
# Keep the upstream source and runtime at the same release. The source commit is
# checked after fetching the release tag so a moved tag cannot change the build.
ARG JACKETT_RUNTIME=lscr.io/linuxserver/jackett:v0.25.28-ls45
FROM mcr.microsoft.com/dotnet/sdk:10.0-alpine AS build
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src
RUN apk add --no-cache git
RUN git init . \
 && git remote add origin https://github.com/Jackett/Jackett.git \
 && git fetch --depth 1 origin refs/tags/v0.25.28 \
 && git checkout --detach FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = bd5e25fa591821d320f61eb09e301c2af1404c8b
COPY tools/jackett-vpn/ /patch/
RUN git apply --check /patch/download-only.patch \
 && git apply /patch/download-only.patch \
 && cp /patch/TorrentDownloadProxy.cs src/Jackett.Common/Utils/Clients/
# No external test packages: the test exercises the same C# classes as Jackett,
# including concurrent requests and an unavailable VPN proxy.
RUN dotnet restore /patch/tests/ProxyRouting.csproj -p:TargetFramework=net10.0 --configfile /patch/tests/NuGet.Config \
 && dotnet run --project /patch/tests/ProxyRouting.csproj -c Release -p:TargetFramework=net10.0 --no-restore
RUN set -eu; \
    case "${TARGETARCH:-amd64}" in \
      amd64) rid=linux-musl-x64 ;; \
      arm64) rid=linux-musl-arm64 ;; \
      arm) test "${TARGETVARIANT:-v7}" = v7; rid=linux-musl-arm ;; \
      *) echo 'Unsupported Jackett architecture' >&2; exit 1 ;; \
    esac; \
    dotnet publish src/Jackett.Server/Jackett.Server.csproj \
      --configuration Release --framework net10.0 --runtime "$rid" \
      --self-contained true --output /publish \
      -p:TargetFrameworks=net10.0 -p:Version=0.25.28 -p:UseAppHost=true -m:1

FROM ${JACKETT_RUNTIME}
ENV AUTO_UPDATE=false
# Preserve the LinuxServer entry point, PUID/PGID setup, /config layout, port and
# health checks. Only replace the application, never the live configuration.
COPY --from=build /publish/ /app/Jackett/
COPY tools/jackett-vpn/svc-jackett-run /etc/s6-overlay/s6-rc.d/svc-jackett/run
RUN chmod 755 /app/Jackett/jackett /etc/s6-overlay/s6-rc.d/svc-jackett/run
