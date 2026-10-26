# The .NET client for the acceptance features (decision 0015, #534): the .NET
# SDK on its LTS line, whose dotnet restore and dotnet nuget commands carry
# NuGet's client. Pinned by digest; Dependabot's docker entry moves it within
# the line and holds the next major, so moving to the next LTS is deliberate.
# The job builds it as artifactweave-accept-dotnet and runs it with
# --network host, so NuGet reaches the binary on 127.0.0.1.
# dotnet-11.Dockerfile is the second .NET client.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:de4fe90a98845d75910b9769662465c84de1a934665c431279d1382b2bddc02b
