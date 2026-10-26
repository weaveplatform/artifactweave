# The second .NET client (#534): the SDK's newest line, .NET 11, beside the
# LTS line dotnet.Dockerfile holds, so NuGet's newest client runs too. Until
# .NET 11's release on 2026-11-10 the 11.0 tag is its go-live release
# candidate; Dependabot moves the digest as the tag moves, and holds the next
# major as it does for every image here. The job builds it as
# artifactweave-accept-dotnet11.
FROM mcr.microsoft.com/dotnet/sdk:11.0@sha256:3a9175b6c54b2f076817af1360f691c9994447b192faa48a1a1f112a516844c7
