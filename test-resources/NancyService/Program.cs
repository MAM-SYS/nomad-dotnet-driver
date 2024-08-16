using System;
using System.Threading;
using System.Threading.Tasks;
using Nancy;
using Nancy.Hosting.Self;

namespace NancySelfHost
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

                // Keep the application running indefinitely
                Task.Run(() => KeepRunning()).Wait();
            }
        }

        private static void KeepRunning()
        {
            while (true)
            {
                Thread.Sleep(Timeout.Infinite);
            }
        }
    }

    public class MyModule : NancyModule
    {
        public MyModule()
        {
            Get("/", _ => "Hello from NancyFX");
        }
    }
}
