using System;
using System.Threading;
using System.Threading.Tasks;
using Nancy;
using Nancy.Hosting.Self;
using System.IO;
using System.Runtime;

namespace NancyWebApp
{
    class Program
    {
        static void Main(string[] args)
        {
            var uri = new Uri("http://localhost:1234");
            var hostConfig = new HostConfiguration { UrlReservations = new UrlReservations { CreateAutomatically = true } };

            using (var host = new NancyHost(hostConfig, uri))
            {
                host.Start();
                Console.WriteLine("NancyFX service running on " + uri);
                Console.ReadLine();
            }
        }
    }

    public class MyModule : NancyModule
    {
        public MyModule()
        {
            Get("/__health", _ =>
            {
                var response = new
                {
                    status = "ok",
                    datetime = DateTime.UtcNow
                };
                return Response.AsJson(response);
            });

            Get("/config/gc", _ =>
            {
                var gcConfig = new
                {
                    ServerGC = GCSettings.IsServerGC,
                    LatencyMode = GCSettings.LatencyMode.ToString()
                };
                return Response.AsJson(gcConfig);
            });
        }
    }
}
