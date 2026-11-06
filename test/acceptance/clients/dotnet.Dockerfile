# Client pins: NuGet bundled with the .NET SDK 10.0 image.
# SDK on its LTS line, whose dotnet restore and dotnet nuget commands carry
# NuGet's client. Pinned by digest; Dependabot's docker entry moves it within
# the line and holds the next major, so moving to the next LTS is deliberate.
# The job builds it as artifactweave-accept-dotnet and runs it with
# --network host, so NuGet reaches the binary on 127.0.0.1.
# dotnet-11.Dockerfile is the second .NET client.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:212fe4cb74627d5adf9817a4088906ae3edff1bd853bb27565d4b46731efe61e
