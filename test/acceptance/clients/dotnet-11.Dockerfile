# Client pins: NuGet bundled with the .NET SDK 11.0 image.
# LTS line dotnet.Dockerfile holds, so NuGet's newest client runs too. Until
# .NET 11's release on 2026-11-10 the 11.0 tag is its go-live release
# candidate; Dependabot moves the digest as the tag moves, and holds the next
# major as it does for every image here. The job builds it as
# artifactweave-accept-dotnet11.
FROM mcr.microsoft.com/dotnet/sdk:11.0@sha256:b55f191e80c1828476d2b080d05d33e1183068f5bcaeee684f28a1d4e31a10ad
