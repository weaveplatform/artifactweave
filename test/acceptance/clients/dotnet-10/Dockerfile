# Client pins: NuGet bundled with the .NET SDK 10.0 image.
# The .NET client for the acceptance features (decision 0015, #534): the .NET
# SDK on its LTS line, whose dotnet restore and dotnet nuget commands carry
# NuGet's client. Pinned by digest; Dependabot's entry for this directory
# moves it within the 10.0 line and ignores .NET 11 and later, so moving to
# the next LTS is deliberate. It has a directory of its own because that
# ignore, on an entry shared with dotnet-11.Dockerfile, would stop that image
# taking the next .NET major too. The job builds it as
# artifactweave-accept-dotnet and runs it with --network host, so NuGet
# reaches the binary on 127.0.0.1. dotnet-11.Dockerfile is the second .NET
# client.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:fe0fd4e93bd2fbb47c883b7a6369e5c8d3db8bf01cdc5981f6ee35fbdf3ff4fb
