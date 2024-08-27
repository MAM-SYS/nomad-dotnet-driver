using System;
using System.Threading;

namespace HelloWorldService
{
        class Program
        {
            static void Main(string[] args)
            {
                Console.WriteLine("NancyFX service running");
                // Keep the application running indefinitely
                Task.Run(() => KeepRunning()).Wait();
        }

            private static void KeepRunning()
            {
                while (true)
                {
                    Thread.Sleep(Timeout.Infinite);
                }
            }
        }
}
