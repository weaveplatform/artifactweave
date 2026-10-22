# The .NET client for the acceptance features (decision 0015, #534): the .NET
# SDK on its LTS line, whose dotnet restore and dotnet nuget commands carry
# NuGet's client. Pinned by digest; Dependabot's docker entry moves it within
# the line and holds the next major, so moving to the next LTS is deliberate.
# The job builds it as artifactweave-accept-dotnet and runs it with
# --network host, so NuGet reaches the binary on 127.0.0.1.
# dotnet-11.Dockerfile is the second .NET client.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:747cbade5bfd918a175ac32f8f1d931edec2c7d0d5397a3de72eaef1ac1f66d2
