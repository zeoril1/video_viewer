using System;
using System.Collections.Concurrent;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Text;
using System.Threading.Tasks;
using Jackett.Common.Utils.Clients;

internal static class Program
{
    private static int checks;

    private static void Check(bool success, string message)
    {
        if (!success) throw new Exception(message);
        checks++;
    }

    private static HttpClient Client(TorrentDownloadProxy proxy) => new HttpClient(new HttpClientHandler
    {
        Proxy = proxy,
        UseProxy = true,
        AllowAutoRedirect = true
    }) { Timeout = TimeSpan.FromSeconds(5) };

    private static async Task ExpectNetworkError(Func<Task> action)
    {
        try { await action(); }
        catch (HttpRequestException) { return; }
        throw new Exception("Expected the VPN request to fail without a direct retry.");
    }

    private static async Task Main()
    {
        using var origin = new TestServer();
        using var proxyServer = new TestServer();
        var policy = new TorrentDownloadProxy(proxyServer.Url);
        using var client = Client(policy);
        var destination = new Uri(origin.Url + "/file.torrent");

        Check(policy.IsBypassed(destination), "ordinary requests must bypass the VPN proxy");
        using (TorrentDownloadScope.Begin())
        {
            await Task.Yield();
            Check(!policy.IsBypassed(destination), "download scope must survive await");
            Check(policy.GetProxy(destination).AbsoluteUri == proxyServer.Url + "/", "download proxy address");
            using (TorrentDownloadScope.Begin()) { await Task.Yield(); }
            Check(TorrentDownloadScope.IsActive, "nested scope must preserve the outer download scope");
        }
        Check(!TorrentDownloadScope.IsActive, "download scope must be restored after disposal");

        var oldProxy = Environment.GetEnvironmentVariable("HTTP_PROXY");
        var oldNoProxy = Environment.GetEnvironmentVariable("NO_PROXY");
        try
        {
            Environment.SetEnvironmentVariable("HTTP_PROXY", "http://127.0.0.1:1");
            Environment.SetEnvironmentVariable("NO_PROXY", "*");
            await client.GetStringAsync(origin.Url + "/direct");
            using (TorrentDownloadScope.Begin()) await client.GetStringAsync(origin.Url + "/metadata");
            Check(origin.Requests.Any(x => x.StartsWith("GET /direct ")), "ordinary HTTP must connect directly despite HTTP_PROXY");
            Check(!origin.Requests.Any(x => x.Contains("/metadata")), "metadata must not reach the origin directly");
            Check(proxyServer.Requests.Any(x => x.Contains("/metadata")), "NO_PROXY must not bypass the download proxy, even on localhost");

            var tasks = Enumerable.Range(0, 40).Select(async i =>
            {
                if (i % 2 == 0)
                {
                    using (TorrentDownloadScope.Begin())
                    {
                        await Task.Yield();
                        await client.GetStringAsync(origin.Url + "/download-" + i);
                    }
                }
                else
                {
                    await Task.Yield();
                    await client.GetStringAsync(origin.Url + "/search-" + i);
                }
            });
            await Task.WhenAll(tasks);
            Check(origin.Requests.Count(x => x.Contains("/search-")) == 20, "all concurrent searches must remain direct");
            Check(!origin.Requests.Any(x => x.Contains("/download-")), "concurrent download scope must not leak onto the direct route");
            Check(proxyServer.Requests.Count(x => x.Contains("/download-")) == 20, "all concurrent downloads must use VPN");
            Check(!proxyServer.Requests.Any(x => x.Contains("/search-")), "concurrent search must not inherit another request's scope");

            using (TorrentDownloadScope.Begin()) await client.GetStringAsync("http://metadata.invalid/redirect");
            Check(proxyServer.Requests.Any(x => x.Contains("http://metadata.invalid/final")), "redirect must keep the VPN route without local destination DNS");

            using (TorrentDownloadScope.Begin())
                await ExpectNetworkError(() => client.GetStringAsync("https://metadata.invalid/file.torrent"));
            Check(proxyServer.Requests.Any(x => x.StartsWith("CONNECT metadata.invalid:443 ")), "HTTPS must use CONNECT through the VPN proxy");

            var closedListener = new TcpListener(IPAddress.Loopback, 0);
            closedListener.Start();
            var closedPort = ((IPEndPoint)closedListener.LocalEndpoint).Port;
            closedListener.Stop();
            using var unavailable = Client(new TorrentDownloadProxy("http://127.0.0.1:" + closedPort));
            var directBefore = origin.Requests.Count;
            using (TorrentDownloadScope.Begin())
                await ExpectNetworkError(() => unavailable.GetStringAsync(origin.Url + "/must-not-fallback"));
            Check(origin.Requests.Count == directBefore, "an unavailable VPN must never retry directly");
            await unavailable.GetStringAsync(origin.Url + "/search-with-vpn-down");
            Check(origin.Requests.Any(x => x.Contains("/search-with-vpn-down")), "ordinary requests must work while VPN is down");
        }
        finally
        {
            Environment.SetEnvironmentVariable("HTTP_PROXY", oldProxy);
            Environment.SetEnvironmentVariable("NO_PROXY", oldNoProxy);
        }

        foreach (var invalid in new[] { "https://vpn:8888", "http://user:secret@vpn:8888", "http://vpn:8888/path", "not a URI" })
        {
            try { new TorrentDownloadProxy(invalid); }
            catch (ArgumentException) { checks++; continue; }
            throw new Exception("Invalid proxy configuration accepted");
        }
        Console.WriteLine("PASS: " + checks + " routing checks; await/nesting, concurrency, redirects, HTTPS CONNECT, environment isolation and VPN failure.");
    }

    private sealed class TestServer : IDisposable
    {
        private readonly TcpListener listener = new TcpListener(IPAddress.Loopback, 0);
        public readonly ConcurrentQueue<string> Requests = new ConcurrentQueue<string>();
        public string Url { get; }

        public TestServer()
        {
            listener.Start();
            Url = "http://127.0.0.1:" + ((IPEndPoint)listener.LocalEndpoint).Port;
            _ = Accept();
        }

        private async Task Accept()
        {
            try
            {
                while (true)
                {
                    var socket = await listener.AcceptTcpClientAsync();
                    _ = Respond(socket);
                }
            }
            catch (ObjectDisposedException) { }
            catch (SocketException) { }
        }

        private async Task Respond(TcpClient socket)
        {
            using (socket)
            using (var stream = socket.GetStream())
            using (var reader = new StreamReader(stream, Encoding.ASCII, false, 1024, true))
            {
                var first = await reader.ReadLineAsync();
                if (first == null) return;
                Requests.Enqueue(first);
                while (!string.IsNullOrEmpty(await reader.ReadLineAsync())) { }
                var response = first.Contains("/redirect ")
                    ? "HTTP/1.1 302 Found\r\nLocation: http://metadata.invalid/final\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                    : "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok";
                var bytes = Encoding.ASCII.GetBytes(response);
                await stream.WriteAsync(bytes, 0, bytes.Length);
            }
        }

        public void Dispose() => listener.Stop();
    }
}
