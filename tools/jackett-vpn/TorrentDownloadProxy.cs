using System;
using System.Net;
using System.Threading;

namespace Jackett.Common.Utils.Clients
{
    // An asynchronous request scope, never a process-wide proxy switch. Search
    // requests running concurrently with a download keep their direct route.
    public sealed class TorrentDownloadScope : IDisposable
    {
        private static readonly AsyncLocal<int> Depth = new AsyncLocal<int>();
        private readonly int previousDepth;
        private bool disposed;

        private TorrentDownloadScope()
        {
            previousDepth = Depth.Value;
            Depth.Value = previousDepth + 1;
        }

        public static bool IsActive => Depth.Value > 0;
        public static IDisposable Begin() => new TorrentDownloadScope();

        public void Dispose()
        {
            if (disposed) return;
            Depth.Value = previousDepth;
            disposed = true;
        }
    }

    // HttpClientHandler asks IWebProxy for each request, including HTTPS CONNECT.
    // No ProxyFromEnvironment, NO_PROXY, local-address bypass or direct fallback.
    public sealed class TorrentDownloadProxy : IWebProxy
    {
        private readonly Uri proxy;

        public TorrentDownloadProxy(string address)
        {
            if (string.IsNullOrWhiteSpace(address)) address = "http://torrent-vpn:8888";
            if (!Uri.TryCreate(address, UriKind.Absolute, out var parsed) ||
                parsed.Scheme != Uri.UriSchemeHttp || string.IsNullOrEmpty(parsed.Host) ||
                !string.IsNullOrEmpty(parsed.UserInfo) || parsed.AbsolutePath != "/" ||
                !string.IsNullOrEmpty(parsed.Query) || !string.IsNullOrEmpty(parsed.Fragment))
                throw new ArgumentException("TORRENT_DOWNLOAD_PROXY must be an HTTP proxy origin without credentials or a path.");
            proxy = parsed;
        }

        public ICredentials Credentials { get; set; }
        public bool IsBypassed(Uri destination) => !TorrentDownloadScope.IsActive;
        public Uri GetProxy(Uri destination) => TorrentDownloadScope.IsActive ? proxy : destination;
    }
}
