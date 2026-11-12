# Client pins: NuGet bundled with the .NET SDK 11.0 image.
# The .NET client on the SDK's newest line (#534), beside the LTS line that
# dotnet-10/Dockerfile holds, so NuGet's newest client runs too. Pinned by
# digest; Dependabot's /test/acceptance/clients entry moves the digest as the
# tag moves, and takes the next major once MCR publishes its plain tag (12.0
# for .NET 12), as it does for node.Dockerfile and maven.Dockerfile. So the
# file, the image and the ".NET" scenario label name no version (#1241). The
# job builds it as artifactweave-accept-dotnet and runs it with --network
# host, so NuGet reaches the binary on 127.0.0.1.
FROM mcr.microsoft.com/dotnet/sdk:11.0@sha256:65f3c7c6a25a239ab3e6719f2896147e7b68242ff2d8818dd70498bde6ef8bea
