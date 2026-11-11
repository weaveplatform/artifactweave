# Client pins: NuGet bundled with the .NET SDK 11.0 image.
# The second .NET client (#534): the SDK's newest line, .NET 11, beside the
# LTS line dotnet-10/Dockerfile holds, so NuGet's newest client runs too. Until
# .NET 11's release on 2026-11-10 the 11.0 tag is its go-live release
# candidate; Dependabot moves the digest as the tag moves, and holds the next
# major as it does for every image here. The job builds it as
# artifactweave-accept-dotnet11.
FROM mcr.microsoft.com/dotnet/sdk:11.0@sha256:65f3c7c6a25a239ab3e6719f2896147e7b68242ff2d8818dd70498bde6ef8bea
