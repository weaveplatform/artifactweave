# The Maven client for the acceptance features (decision 0015): Maven 3.9 on
# Temurin 21. Pinned by digest; Dependabot's docker entry moves it. The job
# builds it as artifactweave-accept-maven and runs it with --network host, so
# mvn reaches the binary on 127.0.0.1.
FROM maven:3.9-eclipse-temurin-26@sha256:b2c1ad85954592f9928e84327c65201f308ad9b5d8ed7d5b823717c97bf23fbb
