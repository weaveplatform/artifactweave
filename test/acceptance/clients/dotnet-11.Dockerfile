# The second .NET client (#534): the SDK's newest line, .NET 11, beside the
# LTS line dotnet.Dockerfile holds, so NuGet's newest client runs too. Until
# .NET 11's release on 2026-11-10 the 11.0 tag is its go-live release
# candidate; Dependabot moves the digest as the tag moves, and holds the next
# major as it does for every image here. The job builds it as
# artifactweave-accept-dotnet11.
FROM mcr.microsoft.com/dotnet/sdk:11.0@sha256:bfabf51e74c5a7cf2fc4eac852145db86f4867d475257d809518d6a2b0fe71e1
